// Package authproto ports app.py's OpenAIJsonAuthFlow (app.py:7997-8743) — the
// "protocol mode" (no-browser) OpenAI login/registration client.
//
// It drives the whole auth.openai.com pipeline over plain HTTP, with no browser
// anywhere:
//
//	sentinel PoW -> authorize URL -> authorize/continue -> email OTP
//	  -> password verify / user register -> create_account -> add-phone
//	  -> workspace select -> OAuth redirect chain -> code->token exchange
//
// Everything runs through internal/tlsclient (Chrome TLS impersonation), the Go
// replacement for Python's curl_cffi(impersonate="chrome136") session.
//
// Header NAMES, VALUES and ORDER are load-bearing here: auth.openai.com and
// sentinel.openai.com fingerprint their clients. Every request builder in this
// package emits an fhttp header with HeaderOrderKey pinned to the exact order
// the Python dict had; see session.go.
//
// Python reference: app.py:5495-5642 (pure helpers + session factory) and
// app.py:7997-8743 (the flow class itself).
//
// # Ported
//
//	app.py:5495-5642   token_debug_fingerprint, normalize_auth_continue_url,
//	                   performance_now_ms, base64_json, sentinel_hash_hex,
//	                   collect_sentinel_fingerprint_data,
//	                   generate_sentinel_answer,
//	                   generate_sentinel_requirements_token,
//	                   generate_sentinel_proof_token, openai_browser_headers,
//	                   is_transient_http_error, new_openai_http_session
//	app.py:7997-8743   the whole OpenAIJsonAuthFlow class: __init__, _headers,
//	                   _set_device_cookie, _format_error_response, _read_cookie,
//	                   _prepare_login_url, _prepare_legacy_login_url,
//	                   _fetch_sentinel_token, _authorize_continue,
//	                   _send_email_otp, _continue_url_from_payload,
//	                   _read_email_otp_code, _email_otp_validate,
//	                   _openai_password_for_account,
//	                   _generate_protocol_password, _password_verify,
//	                   _username_password_create, _create_account_profile,
//	                   _resolve_workspace_id, _select_workspace,
//	                   _send_phone_otp, _validate_phone_otp, _handle_add_phone,
//	                   _extract_auth_result, _follow_oauth_redirects,
//	                   _is_transient_http_error, _exchange_code_for_token,
//	                   _open_oauth_url, _response_has_auth_challenge, run
//
// # Injection points
//
//	app.py:179-263     solve_turnstile_token — the local turnstile_solver HTTP
//	                   client (POST /v1/leases, POST /v1/solve, the
//	                   /v1/leases/{id}/consume poll loop). Reachable here as the
//	                   TurnstileSolver callback; a nil solver behaves exactly
//	                   like Python's "solver offline" branch. PORTED in
//	                   internal/turnstile: assign turnstile.SolveToken.
//	app.py:7991-7994   create_mail_reader — reachable as MailReaderFactory.
//	                   PORTED in mailotp.go, which adapts internal/mail's
//	                   IMAP/Graph/CloudMail readers: assign
//	                   DefaultMailReaderFactory.
//	app.py:3708-3860   SMSBowerClient — intentionally NOT wired. Renting a
//	                   number costs money; the PhoneProvider callback is the
//	                   caller's own.
package authproto

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ---------------------------------------------------------------------------
// Callbacks — the collaborators app.py injected into __init__ (app.py:7998-8027)
// ---------------------------------------------------------------------------

// Log is the human-facing progress sink (Python's `log` callable).
type Log func(msg string)

func (l Log) emit(msg string) {
	if l != nil {
		l(msg)
	}
}

func (l Log) emitf(format string, args ...any) { l.emit(fmt.Sprintf(format, args...)) }

// PhoneProvider mirrors app.py's self._phone_provider(action, email, payload).
// Actions used by this flow, in order: "next" (payload ""), "sent", "code",
// "good", "bad" (payload is the phone dict, "bad" with error/status added).
//
// "next" returns the phone dict or nil when the pool is empty; "code" returns
// the SMS code. Errors from "next" propagate; errors from the others are caught
// by the per-number retry loop, exactly as Python's try/except did.
//
// NOT WIRED TO internal/smsbower BY THIS PACKAGE. Renting a number costs money;
// the caller supplies the provider and owns that decision.
type PhoneProvider func(action, email string, payload any) (any, error)

// InputCallback mirrors self.input_callback(kind, email, prompt) — the
// human-in-the-loop prompt used for manual email OTP and manual phone entry.
type InputCallback func(kind, email, prompt string) (string, error)

// OTPReader is the slice of the mail reader that _read_email_otp_code uses
// (app.py:8229-8233).
type OTPReader interface {
	// WaitForCode is otp_reader.wait_for_code(min_timestamp), min_timestamp in
	// Unix seconds.
	WaitForCode(minTimestamp float64) (string, error)
	Close() error
}

// MailReaderFactory is create_mail_reader(account, log, "") (app.py:8229).
// It stays an injection point so this package never depends on IMAP/Graph.
type MailReaderFactory func(account *models.MailAccount, log Log) (OTPReader, error)

// TurnstileSolver is solve_turnstile_token(sitekey, page_url,
// solver_url=..., timeout=120.0) (app.py:179-236). Python swallowed every
// failure and returned ""; a non-nil error here is treated the same way.
type TurnstileSolver func(sitekey, pageURL, solverURL string, timeout time.Duration) (string, error)

// ---------------------------------------------------------------------------
// Flow
// ---------------------------------------------------------------------------

// Options mirrors OpenAIJsonAuthFlow.__init__'s parameters (app.py:7998-8008).
//
// CAUTION: Python defaults allow_manual_phone to TRUE while Go's zero value is
// false. The two protocol-mode call sites disagree — app.py:15792 passes
// allow_manual_phone=False for the batch register path and app.py:15834 passes
// `not smsbower_auto` for the interactive one — so the field is always set
// explicitly there and must be set explicitly here too.
type Options struct {
	Account                *models.MailAccount
	Log                    Log
	PhoneProvider          PhoneProvider
	InputCallback          InputCallback
	ProxyURL               string
	AllowManualPhone       bool
	ManualEmailOTP         bool
	TurnstileSolverEnabled bool
	TurnstileSolverURL     string

	// MailReaderFactory supplies the email-OTP reader. Required unless
	// ManualEmailOTP is set.
	MailReaderFactory MailReaderFactory
	// TurnstileSolver is optional; a nil solver behaves like an offline solver
	// (Python's "solver 未返回 token" branch).
	TurnstileSolver TurnstileSolver
	// Transport overrides the tls-client transport. Tests MUST set it; leaving
	// it nil builds a real Chrome-impersonating client bound to ProxyURL.
	Transport Transport
}

// Flow is one protocol-mode auth attempt. It is NOT safe for concurrent use:
// like the Python object it owns a single cookie jar and a single PKCE state.
type Flow struct {
	account                *models.MailAccount
	log                    Log
	phoneProvider          PhoneProvider
	inputCallback          InputCallback
	allowManualPhone       bool
	manualEmailOTP         bool
	proxyURL               string
	turnstileSolverEnabled bool
	turnstileSolverURL     string
	turnstileToken         string
	turnstileSolver        TurnstileSolver
	mailReaderFactory      MailReaderFactory
	transport              Transport

	state        string
	nonce        string
	codeVerifier string
	deviceID     string
	// emailOTPRequestedAt is Unix seconds; 0 is Python's falsy 0.0 initial value.
	emailOTPRequestedAt float64

	// test seams — production values are time.Sleep / time.Now.
	sleep func(time.Duration)
	now   func() time.Time
}

// New mirrors OpenAIJsonAuthFlow.__init__ (app.py:7998-8027), including the
// device-id mint and the initial oai-did cookie write.
func New(opts Options) (*Flow, error) {
	if opts.Account == nil {
		return nil, errors.New("authproto: 缺少账号")
	}
	transport := opts.Transport
	if transport == nil {
		t, err := newHTTPTransport(opts.ProxyURL)
		if err != nil {
			return nil, err
		}
		transport = t
	}
	// app.py:8011 — the account's email is normalized in place.
	opts.Account.Email = models.NormalizeEmailAddress(opts.Account.Email)
	// app.py:8019 — `str(x or DEFAULT).strip() or DEFAULT`: a whitespace-only
	// URL falls back to the default too, which one TrimSpace alone would not do.
	solverURL := strings.TrimSpace(opts.TurnstileSolverURL)
	if solverURL == "" {
		solverURL = openai.TurnstileSolverDefaultURL
	}
	f := &Flow{
		account:                opts.Account,
		log:                    opts.Log,
		phoneProvider:          opts.PhoneProvider,
		inputCallback:          opts.InputCallback,
		allowManualPhone:       opts.AllowManualPhone,
		manualEmailOTP:         opts.ManualEmailOTP,
		proxyURL:               opts.ProxyURL,
		turnstileSolverEnabled: opts.TurnstileSolverEnabled,
		turnstileSolverURL:     solverURL,
		turnstileSolver:        opts.TurnstileSolver,
		mailReaderFactory:      opts.MailReaderFactory,
		transport:              transport,
		deviceID:               randomUUID(),
		sleep:                  time.Sleep,
		now:                    time.Now,
	}
	f.setDeviceCookie(f.deviceID)
	return f, nil
}

// DeviceID exposes the minted oai-did, for diagnostics.
func (f *Flow) DeviceID() string { return f.deviceID }

// State exposes the OAuth state PrepareLoginURL minted, for diagnostics.
func (f *Flow) State() string { return f.state }

// ---------------------------------------------------------------------------
// _set_device_cookie / _read_cookie (app.py:8032-8064)
// ---------------------------------------------------------------------------

// deviceCookieDomains is the tuple _set_device_cookie writes to (app.py:8033).
var deviceCookieDomains = []string{".auth.openai.com", "auth.openai.com", ".openai.com"}

// setDeviceCookie mirrors _set_device_cookie (app.py:8032-8037). Python wrapped
// each set in a bare try/except; nothing here can fail, so the guard is dropped.
func (f *Flow) setDeviceCookie(deviceID string) {
	for _, domain := range deviceCookieDomains {
		f.transport.SetCookie("oai-did", deviceID, domain)
	}
}

// readCookie mirrors _read_cookie (app.py:8053-8064): walk the jar in order and
// return the first cookie whose name matches and whose domain is a SUFFIX of
// the URL's hostname.
//
// The suffix test is Python's, verbatim: hostname.endswith(domain.lstrip("."))
// with no dot boundary, so a cookie scoped to "openai.com" matches
// "notopenai.com" too. Copied rather than corrected — the only cookies in this
// jar are OpenAI's own.
//
// Python computes `hostname` on line 8054 and then never uses it, recomputing
// urlparse(url).hostname inside the loop; the two are identical.
func (f *Flow) readCookie(rawURL, key string) string {
	hostname := hostnameOf(rawURL)
	for _, cookie := range f.transport.Cookies() {
		if cookie.Name != key {
			continue
		}
		if cookie.Domain == "" || strings.HasSuffix(hostname, strings.TrimLeft(cookie.Domain, ".")) {
			return cookie.Value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// _prepare_login_url / _prepare_legacy_login_url (app.py:8066-8107)
// ---------------------------------------------------------------------------

// PrepareLoginURL mirrors _prepare_login_url (app.py:8066-8091): mint state,
// nonce and the PKCE verifier, then build the accounts authorize URL.
//
// Exported because app.py:15401 calls flow._prepare_login_url() from OUTSIDE
// the class, to hand the URL to a real browser for the manual-OAuth path.
//
// The parameter ORDER is the Python dict's insertion order; urlencode preserves
// it and url.Values.Encode would sort it away.
func (f *Flow) PrepareLoginURL() string {
	f.state = openai.RandomURLSafeString(24)
	f.nonce = openai.RandomURLSafeString(24)
	f.codeVerifier = openai.RandomURLSafeString(64)
	query := queryPairs{
		{"issuer", openai.AuthBaseURL},
		{"client_id", openai.DefaultClientID},
		{"audience", openai.AuthOAuthAudience},
		{"response_type", "code"},
		{"response_mode", "query"},
		{"redirect_uri", openai.DefaultRedirectURI},
		{"device_id", f.deviceID},
		{"scope", "openid email profile offline_access"},
		{"state", f.state},
		{"nonce", f.nonce},
		{"code_challenge", openai.PKCECodeChallenge(f.codeVerifier)},
		{"code_challenge_method", "S256"},
		{"screen_hint", "login_or_signup"},
		{"max_age", "0"},
		{"prompt", "login"},
		{"id_token_add_organizations", "true"},
		{"codex_cli_simplified_flow", "true"},
		{"login_hint", f.account.Email},
		{"auth0Client", openai.Auth0ClientHeader},
	}
	return openai.AuthBaseURL + "/api/accounts/authorize?" + query.Encode()
}

// prepareLegacyLoginURL mirrors _prepare_legacy_login_url (app.py:8093-8107).
// It reuses the state / verifier PrepareLoginURL already minted — it does NOT
// mint new ones, so calling it first would sign the request with empty values.
func (f *Flow) prepareLegacyLoginURL() string {
	query := queryPairs{
		{"client_id", openai.DefaultClientID},
		{"response_type", "code"},
		{"redirect_uri", openai.DefaultRedirectURI},
		{"scope", "openid email profile offline_access"},
		{"state", f.state},
		{"code_challenge", openai.PKCECodeChallenge(f.codeVerifier)},
		{"code_challenge_method", "S256"},
		{"prompt", "login"},
		{"id_token_add_organizations", "true"},
		{"codex_cli_simplified_flow", "true"},
		{"login_hint", f.account.Email},
	}
	return openai.AuthBaseURL + "/oauth/authorize?" + query.Encode()
}

// hostnameOf is `urlparse(url).hostname or ""`: SplitResult._hostinfo drops the
// userinfo before the LAST "@" and the port after the FIRST ":" (or after "]"
// for a bracketed IPv6 literal), then lowercases.
//
// It goes through pyURLSplit rather than net/url because url.Parse REFUSES
// URLs that urlparse accepts (a stray "%" in the path, a raw control
// character), which would have collapsed the hostname to "" and made
// _read_cookie skip every domain-scoped cookie.
//
// DIVERGENCE: CPython raises AttributeError at app.py:8062 when hostname is
// None (a URL with no authority); "" is used instead, which simply matches no
// domain-scoped cookie. Both call sites pass an absolute https URL.
func hostnameOf(rawURL string) string {
	netloc := pyURLSplit(rawURL, "").netloc
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		netloc = netloc[i+1:]
	}
	host := netloc
	if i := strings.Index(netloc, "["); i >= 0 {
		bracketed := netloc[i+1:]
		if j := strings.Index(bracketed, "]"); j >= 0 {
			host = bracketed[:j]
		} else {
			host = bracketed
		}
	} else if i := strings.Index(netloc, ":"); i >= 0 {
		host = netloc[:i]
	}
	return pyLower(host)
}
