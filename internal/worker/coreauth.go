// Package worker holds the port of app.py's OpenAIRegisterPayLinkWorker.
//
// coreauth.go ports the core register/login SUPPORT helpers (app.py 9935-10208):
// route-error detection/retry, ChatGPT CSRF + device-id extraction, signin/login
// URL minting, the localized "Continue" click ladder and the email autofill.
//
// The state machine that drives these lives elsewhere.
package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// RouteErrorMaxRetries mirrors the `route_error_retries < 3` cap the three
// auth loops apply around _detect_route_error / _retry_route_error
// (app.py:9359, 9794, 9904).
const RouteErrorMaxRetries = 3

// RouteErrorRetryDelay mirrors the `time.sleep(5)` those same loops perform
// after a successful _retry_route_error (app.py:9797).
const RouteErrorRetryDelay = 5 * time.Second

// EmailInputSelectors mirrors the selector list _fill_email_if_visible feeds to
// _visible_inputs (app.py:10196-10201). Order is a priority ladder.
var EmailInputSelectors = []string{
	`input[type="email"]`,
	`input[name="email"]`,
	`input[name="username"]`,
	`input[autocomplete="email"]`,
}

// authCSRFCookieName is the next-auth CSRF cookie read in
// _get_chatgpt_csrf_and_device (app.py:10032).
const authCSRFCookieName = "__Host-next-auth.csrf-token"

// authDeviceCookieName is the OpenAI device-id cookie (app.py:10034).
const authDeviceCookieName = "oai-did"

// authCookieHosts mirrors the URL list passed to context.cookies(...)
// (app.py:10028): the ChatGPT origin plus openai.com.
var authCookieHosts = []string{"chatgpt.com", "openai.com"}

// AuthURLBuilder bundles the live browser, account and HTTP transport that the
// register/login support helpers of app.py's OpenAIRegisterPayLinkWorker need
// (app.py 9935-10208).
//
// The signin URL is minted by an HTTP POST that Playwright ran through
// context.request — i.e. through the BROWSER's cookie jar, the browser's proxy
// and the browser's TLS stack. Go has no such shared request context, so the
// equivalent is reconstructed here: cookies are read out of the rod browser and
// attached by hand, Client MUST already be bound to the same proxy chain the
// browser uses, and it must be a Chrome-impersonating tlsclient. Sending this
// POST from a plain net/http client fails Cloudflare.
type AuthURLBuilder struct {
	// Page is the tab the register/login flow is running in.
	Page *browser.Page
	// Browser owns the cookie jar the auth POST must reuse.
	Browser *browser.Browser
	// Account supplies login_hint / the email autofill value.
	Account *models.MailAccount
	// Fingerprint supplies locale + Accept-Language, matching the browser.
	Fingerprint models.DeviceFingerprint
	// Client is the Chrome-TLS HTTP client, bound to the SAME proxy as Browser.
	// Its construction-time timeout stands in for Python's per-call
	// timeout=30000 (app.py:10041); tls-client has no per-request timeout.
	Client *tlsclient.Client
	// Log is the worker log sink; may be nil.
	Log func(string)
}

// NewAuthURLBuilder constructs an AuthURLBuilder. It mirrors the subset of
// OpenAIRegisterPayLinkWorker state that app.py 9935-10208 touches
// (self.page/self.browser context, self.account, self.fingerprint, self.log).
func NewAuthURLBuilder(
	page *browser.Page,
	br *browser.Browser,
	account *models.MailAccount,
	fingerprint models.DeviceFingerprint,
	client *tlsclient.Client,
	log func(string),
) *AuthURLBuilder {
	return &AuthURLBuilder{
		Page:        page,
		Browser:     br,
		Account:     account,
		Fingerprint: fingerprint,
		Client:      client,
		Log:         log,
	}
}

func (a *AuthURLBuilder) logf(format string, args ...any) {
	if a == nil || a.Log == nil {
		return
	}
	a.Log(fmt.Sprintf(format, args...))
}

func (a *AuthURLBuilder) email() string {
	if a == nil || a.Account == nil {
		return ""
	}
	return a.Account.Email
}

// ---------------------------------------------------------------------------
// _detect_route_error / _retry_route_error (app.py:9935-9967)
// ---------------------------------------------------------------------------

// authBodyInnerTextJS is page.locator("body").inner_text() (app.py:9937).
const authBodyInnerTextJS = `() => (document.body && document.body.innerText) || ''`

// DetectRouteError mirrors _detect_route_error (app.py:9935-9943): read the body
// text under a tight 700ms budget, collapse whitespace, and return the first 400
// characters when one of the localized "the page blew up" markers is present.
// Any read failure yields "" (the Python bare except).
func (a *AuthURLBuilder) DetectRouteError() string {
	if a == nil || a.Page == nil || a.Page.Rod == nil {
		return ""
	}
	v, err := a.Page.Rod.Timeout(700 * time.Millisecond).Eval(authBodyInnerTextJS)
	if err != nil || v == nil {
		return ""
	}
	return authRouteErrorFromText(v.Value.Str())
}

// authRouteErrorFromText is the pure half of _detect_route_error
// (app.py:9940-9943), split out of the page read so it can be differentially
// tested against the Python source.
func authRouteErrorFromText(raw string) string {
	// re.sub(r"\s+", " ", text).strip() (app.py:9940). unicode.IsSpace would have
	// left U+001C..U+001F that str.strip() removes.
	normalized := pyCollapseStrip(raw)
	if strings.Contains(normalized, "糟糕，出错了") ||
		strings.Contains(normalized, "Operation timed out") ||
		strings.Contains(normalized, "Route Error") {
		// Python slices the str by CHARACTERS, not bytes.
		return authTruncateRunes(normalized, 400)
	}
	return ""
}

// authRouteErrorLadder is the ':has-text()' ladder of _retry_route_error
// (app.py:9946-9953), split into (css, text) pairs because go-rod has no
// :has-text() pseudo-selector. ORDER IS LOAD-BEARING.
var authRouteErrorLadder = []authClickTarget{
	{CSS: "button", Text: "Try again"},
	{CSS: "button", Text: "重试"},
	{CSS: "a", Text: "Try again"},
	{CSS: "a", Text: "重试"},
	{CSS: `[role="button"]`, Text: "Try again"},
	{CSS: `[role="button"]`, Text: "重试"},
}

// RetryRouteError mirrors _retry_route_error (app.py:9945-9967): click the first
// visible "Try again"/"重试" control in ladder order and wait for
// DOMContentLoaded; if none is clickable, fall back to reloading the page.
// Returns whether anything was done.
//
// Callers must keep the surrounding cadence from app.py:9793-9799 —
// at most RouteErrorMaxRetries attempts, RouteErrorRetryDelay between them,
// then raise the localized "OpenAI 页面错误..." failure.
func (a *AuthURLBuilder) RetryRouteError() bool {
	if a == nil || a.Page == nil || a.Page.Rod == nil {
		return false
	}
	for _, target := range authRouteErrorLadder {
		if !a.clickTarget(target) {
			continue
		}
		// Python: wait_for_load_state("domcontentloaded", timeout=15000) inside
		// the try — a timeout there falls through to the next selector.
		if err := a.Page.WaitDOMContentLoaded(15 * time.Second); err != nil {
			continue
		}
		return true
	}
	// page.reload(wait_until="domcontentloaded", timeout=30000) (app.py:9964).
	if err := a.Page.Rod.Timeout(30 * time.Second).Reload(); err != nil {
		return false
	}
	if err := a.Page.WaitDOMContentLoaded(30 * time.Second); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// _create_openai_signin_url / _create_login_url / _get_chatgpt_csrf_and_device
// (app.py:9969-10060)
// ---------------------------------------------------------------------------

// CreateOpenAISigninURL mirrors _create_openai_signin_url (app.py:9969-9996):
// mint the auth.openai.com redirect for the SIGNUP screen by POSTing to
// /api/auth/signin/openai with the live browser's CSRF token.
func (a *AuthURLBuilder) CreateOpenAISigninURL() (string, error) {
	return a.createSigninURL("signup", "认证页")
}

// CreateLoginURL mirrors _create_login_url (app.py:9998-10025): identical to
// CreateOpenAISigninURL except screen_hint=login and the localized error text.
func (a *AuthURLBuilder) CreateLoginURL() (string, error) {
	return a.createSigninURL("login", "登录页")
}

// createSigninURL is the shared body of _create_openai_signin_url and
// _create_login_url; pageLabel is the only differing token in the Chinese error
// strings ("认证页" vs "登录页").
func (a *AuthURLBuilder) createSigninURL(screenHint, pageLabel string) (string, error) {
	csrfValue, deviceID := a.ChatGPTCSRFAndDevice()
	if csrfValue == "" {
		return "", fmt.Errorf("未找到 ChatGPT CSRF cookie，无法打开%s", pageLabel)
	}
	if deviceID == "" {
		deviceID = authRandomUUID4()
	}

	// urlencode() of a dict preserves INSERTION order on CPython 3.7+, and the
	// endpoint is order-sensitive in practice — url.Values.Encode() would sort
	// the keys, so the query is assembled by hand.
	query := authEncodePairs([][2]string{
		{"prompt", "login"},
		{"ext-oai-did", deviceID},
		{"auth_session_logging_id", authRandomUUID4()}, // FRESH uuid4 per call
		{"ext-passkey-client-capabilities", "0111"},
		{"screen_hint", screenHint},
		{"login_hint", a.email()},
		{"locale", a.Fingerprint.Locale},
	})
	form := authEncodePairs([][2]string{
		{"callbackUrl", openai.ChatGPTBaseURL + "/"},
		{"csrfToken", csrfValue},
		{"json", "true"},
	})

	status, body, err := a.authRequest(
		"POST",
		openai.ChatGPTBaseURL+"/api/auth/signin/openai?"+query,
		"application/x-www-form-urlencoded",
		[]byte(form),
		map[string]string{
			"accept":          "application/json",
			"accept-language": a.Fingerprint.AcceptLanguage(),
		},
	)
	if err != nil {
		// DELIBERATE DIVERGENCE: Python has no try/except around
		// context.request.post (app.py:9985), so a transport failure propagates
		// as the raw Playwright error. Go has no exception to propagate, and the
		// caller (_register) only prints err, so the failure is wrapped in the
		// same localized prefix the HTTP-status branch below uses.
		return "", fmt.Errorf("打开%s失败: %s", pageLabel, authTruncateRunes(err.Error(), 300))
	}
	// Playwright's response.ok is 200<=status<300.
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("打开%s失败: HTTP %d %s", pageLabel, status, authTruncateRunes(string(body), 300))
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal(body, &payload); jsonErr != nil {
		// Python would raise out of response.json(); the localized "missing URL"
		// message is the closest faithful surface, carrying the raw body instead
		// of a parsed dict (truncated — Python's dict repr had no huge-HTML case).
		return "", fmt.Errorf("打开%s缺少跳转 URL: %s", pageLabel, authTruncateRunes(string(body), 300))
	}
	signinURL, _ := payload["url"].(string)
	if signinURL == "" {
		return "", fmt.Errorf("打开%s缺少跳转 URL: %v", pageLabel, payload)
	}
	return signinURL, nil
}

// ChatGPTCSRFAndDevice mirrors _get_chatgpt_csrf_and_device
// (app.py:10027-10060): read __Host-next-auth.csrf-token and oai-did out of the
// browser jar; if the CSRF token is missing, GET /api/auth/csrf (which
// Set-Cookies it) and re-read. Returns (csrfToken, deviceID); either may be "".
func (a *AuthURLBuilder) ChatGPTCSRFAndDevice() (string, string) {
	csrfValue, deviceID := a.scanCSRFAndDevice()
	if csrfValue == "" {
		status, body, err := a.authRequest(
			"GET",
			openai.ChatGPTBaseURL+"/api/auth/csrf",
			"",
			nil,
			map[string]string{
				"accept":          "application/json",
				"accept-language": a.Fingerprint.AcceptLanguage(),
				"referer":         openai.ChatGPTBaseURL + "/",
			},
		)
		if err != nil {
			a.logf("获取 ChatGPT CSRF 接口失败: %s", authTruncateRunes(err.Error(), 160))
		} else if status >= 200 && status < 300 {
			var payload map[string]any
			if jsonErr := json.Unmarshal(body, &payload); jsonErr != nil {
				a.logf("获取 ChatGPT CSRF 接口失败: %s", authTruncateRunes(jsonErr.Error(), 160))
			} else {
				// str(...).strip(): str.strip() also removes U+001C..U+001F,
				// which strings.TrimSpace leaves in place.
				csrfValue = pyStrip(authAsString(payload["csrfToken"]))
			}
		}
		if csrfValue == "" {
			// authRequest replayed the response's Set-Cookie into the browser
			// (Playwright's context.request shared the jar automatically), so the
			// token is readable from the cookies now.
			refreshed, _ := a.scanCSRFAndDeviceFirst()
			csrfValue = refreshed
		}
	}
	if deviceID == "" {
		_, refreshed := a.scanCSRFAndDeviceFirst()
		deviceID = refreshed
	}
	return csrfValue, deviceID
}

// scanCSRFAndDevice is the FIRST cookie scan of _get_chatgpt_csrf_and_device
// (app.py:10031-10035). That loop has no `break`, so when the jar holds two
// cookies of the same name (oai-did is commonly set on both chatgpt.com and
// .openai.com) the LAST one wins.
func (a *AuthURLBuilder) scanCSRFAndDevice() (string, string) {
	return a.scanCookiePair(false)
}

// scanCSRFAndDeviceFirst is the two RE-scans (app.py:10049-10053 and
// 10055-10059). Those loops DO `break`, so the FIRST match wins — the opposite
// tie-break from the initial scan. The asymmetry is only reachable with
// duplicate cookie names, which is exactly when it decides which device id is
// sent as ext-oai-did.
func (a *AuthURLBuilder) scanCSRFAndDeviceFirst() (string, string) {
	return a.scanCookiePair(true)
}

func (a *AuthURLBuilder) scanCookiePair(firstWins bool) (string, string) {
	csrfValue := ""
	csrfSeen := false
	deviceID := ""
	deviceSeen := false
	for _, c := range a.scopedCookies() {
		if c.Name == authCSRFCookieName && !(firstWins && csrfSeen) {
			csrfValue = authCSRFTokenFromCookie(c.Value)
			csrfSeen = true
		}
		if c.Name == authDeviceCookieName && !(firstWins && deviceSeen) {
			deviceID = c.Value
			deviceSeen = true
		}
	}
	return csrfValue, deviceID
}

// scopedCookies is context.cookies([CHATGPT_BASE_URL, "https://openai.com"])
// (app.py:10028). Playwright filters by full URL; here only the domain is
// matched (both cookies of interest are Path=/), which is strictly more
// permissive and cannot drop a cookie Python would have seen.
func (a *AuthURLBuilder) scopedCookies() []*proto.NetworkCookie {
	if a == nil || a.Browser == nil || a.Browser.Rod == nil {
		return nil
	}
	cookies, err := a.Browser.Rod.GetCookies()
	if err != nil {
		return nil
	}
	out := make([]*proto.NetworkCookie, 0, len(cookies))
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		for _, host := range authCookieHosts {
			if authCookieDomainMatches(c.Domain, host) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// authCSRFTokenFromCookie mirrors unquote(value).split("|")[0] (app.py:10033).
//
// url.PathUnescape is NOT a substitute for unquote: it fails the WHOLE string on
// one malformed escape, so a value like "tok%7Chash%zz" came back undecoded and
// the split on "|" then never fired, sending "tok%7Chash%zz" as the CSRF token
// and failing /api/auth/signin/openai. Python decodes what it can and leaves the
// rest literal, which is what pyUnquote does.
func authCSRFTokenFromCookie(value string) string {
	return strings.Split(pyUnquote(value), "|")[0]
}

// pyUnquote is urllib.parse.unquote(s) with its defaults (encoding="utf-8",
// errors="replace"): every well-formed %XX is decoded, a malformed one is kept
// verbatim, '+' is left alone, and undecodable bytes become U+FFFD.
//
// DELIBERATE DIVERGENCE, one case: CPython's errors="replace" emits one U+FFFD
// per "maximal subpart of an ill-formed subsequence", while ToValidUTF8 emits
// one per RUN of invalid bytes — so %FF%FE is "��" in Python and
// "�" here. Unreachable in practice: RFC 6265 restricts cookie-octets to a
// US-ASCII subset, and the two cookies this decodes are a hex CSRF token and a
// UUID, so no undecodable byte sequence can occur. Reproducing CPython's
// maximal-subpart algorithm would add a branch no input could ever exercise.
func pyUnquote(value string) string {
	if !strings.Contains(value, "%") {
		return value
	}
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			hi, okHi := authUnhex(value[i+1])
			lo, okLo := authUnhex(value[i+2])
			if okHi && okLo {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, value[i])
	}
	return strings.ToValidUTF8(string(out), "�")
}

func authUnhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// _click_continue / _fill_email_if_visible (app.py:10117-10207)
// ---------------------------------------------------------------------------

// authClickTarget is one rung of a Playwright ':has-text()' ladder decomposed
// into a CSS selector plus (optional) textContent substring.
type authClickTarget struct {
	CSS  string
	Text string // "" -> pure CSS rung, no text filter
}

// authContinueLadder is the selector list of _click_continue
// (app.py:10118-10142) with ':has-text(...)' rewritten as textContent matching.
// THE ORDER IS THE PRIORITY: the "Finish creating account" variants must be
// tried before the generic Continue rungs, which must be tried before the
// generic button[type=submit] rung, otherwise the final-step submit can be
// clicked on an intermediate screen. All localized strings are verbatim.
var authContinueLadder = []authClickTarget{
	{CSS: "button", Text: "Finish creating account"},
	{CSS: "button", Text: "Finalizar la creación de la cuenta"},
	{CSS: "button", Text: "Finalizar la creacion de la cuenta"},
	{CSS: `button[data-dd-action-name="Continue"][type="submit"]`},
	{CSS: "button", Text: "Continue"},
	{CSS: "button", Text: "Continuar"},
	{CSS: "button", Text: "アカウントの作成を完了する"},
	{CSS: "button", Text: "作成を完了"},
	{CSS: "button", Text: "继续"},
	{CSS: "button", Text: "完成帐户创建"},
	{CSS: "button", Text: "完成账户创建"},
	{CSS: "button", Text: "Next"},
	{CSS: "button", Text: "下一步"},
	{CSS: "button", Text: "Create"},
	{CSS: "button", Text: "完成"},
	{CSS: `button[type="submit"]`},
	{CSS: `[role="button"]`, Text: "Finish creating account"},
	{CSS: `[role="button"]`, Text: "Finalizar la creación de la cuenta"},
	{CSS: `[role="button"]`, Text: "Finalizar la creacion de la cuenta"},
	{CSS: `[role="button"]`, Text: "Continue"},
	{CSS: `[role="button"]`, Text: "Continuar"},
	{CSS: `[role="button"]`, Text: "アカウントの作成を完了する"},
	{CSS: `[role="button"]`, Text: "作成を完了"},
}

// authLocateClickTargetJS finds the first VISIBLE element matching `sel` whose
// textContent contains `text` (or the first visible match when text is empty),
// scrolls it into view and returns its viewport-centre coordinates so the caller
// can issue a real mouse click (anti-detection; el.Click() is avoided).
const authLocateClickTargetJS = `(sel, text) => {
    const visible = (el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    // Playwright's :has-text() lowercases AND whitespace-normalizes both sides,
    // and reads rendered text (innerText skips script/style and hidden nodes).
    // A raw textContent.includes() is strictly stricter and silently skips rungs
    // whose label is uppercased by CSS or wrapped across lines.
    const norm = (s) => (s || '').replace(/\s+/g, ' ').trim().toLowerCase();
    const rendered = (el) => norm(el.innerText || el.textContent);
    const needle = norm(text);
    let all;
    try { all = Array.from(document.querySelectorAll(sel)); } catch (e) { return null; }
    const el = all.filter(visible).find(e => !needle || rendered(e).includes(needle));
    if (!el) return null;
    if (el.getAttribute('aria-disabled') === 'true' || el.disabled) return null;
    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2, text: el.textContent || '' };
}`

// clickTarget resolves one ladder rung and mouse-clicks it. Returns whether a
// click was issued.
//
// Playwright's locator.first.is_visible(timeout=700) IGNORES its timeout and
// returns immediately, so a single non-polling eval reproduces the original
// cadence exactly. One deliberate deviation: Playwright took `.first` (first DOM
// match) and then asked whether THAT node was visible, while this picks the
// first VISIBLE match — the rungs are alternative renderings of one button, so
// skipping a hidden duplicate is the intended behaviour and matches
// browser.ClickButtonByText.
func (a *AuthURLBuilder) clickTarget(target authClickTarget) bool {
	if a == nil || a.Page == nil || a.Page.Rod == nil {
		return false
	}
	v, err := a.Page.Rod.Timeout(700*time.Millisecond).Eval(authLocateClickTargetJS, target.CSS, target.Text)
	if err != nil || v == nil || v.Value.Nil() {
		return false
	}
	x := v.Value.Get("x").Num()
	y := v.Value.Get("y").Num()
	if err := a.Page.Rod.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return false
	}
	if err := a.Page.Rod.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false
	}
	return true
}

// ClickContinue mirrors _click_continue (app.py:10117-10155): walk the localized
// continue/finish button ladder in priority order, clicking the first visible
// rung and waiting for DOMContentLoaded; if no rung matches, fall back to
// Page.ClickSubmitButtonByDOM (the ported _click_submit_button_by_dom).
func (a *AuthURLBuilder) ClickContinue() bool {
	if a == nil || a.Page == nil {
		return false
	}
	for _, target := range authContinueLadder {
		if !a.clickTarget(target) {
			continue
		}
		// Python: wait_for_load_state("domcontentloaded", timeout=10000) sits
		// INSIDE the try, so a timeout there drops to the next selector even
		// though the click already landed. Preserved verbatim.
		if err := a.Page.WaitDOMContentLoaded(10 * time.Second); err != nil {
			continue
		}
		return true
	}
	if a.Page.ClickSubmitButtonByDOM() {
		// Python leaves this wait unguarded; a bool-returning Go func cannot
		// propagate, so the wait error is dropped and the click still counts.
		_ = a.Page.WaitDOMContentLoaded(10 * time.Second)
		return true
	}
	return false
}

// FillEmailIfVisible mirrors _fill_email_if_visible (app.py:10195-10207): if an
// email/username input is visible, fill the account address and press continue.
// Returns whether the form was found (Python ignores the fill/click outcome).
//
// Playwright's locator.fill() already used the React-safe native value setter;
// go-rod's Input() alone would be reverted by the controlled component, so
// Page.ForceFill is used instead.
func (a *AuthURLBuilder) FillEmailIfVisible() bool {
	if a == nil || a.Page == nil {
		return false
	}
	inputs := a.Page.VisibleInputs(EmailInputSelectors)
	if len(inputs) == 0 {
		return false
	}
	a.logf("[认证] 填写邮箱")
	// Python's inputs[0].fill() RAISES on a detached/non-editable field, aborting
	// the caller. Clicking Continue over an empty email box instead leaves the
	// caller waiting 60s for a navigation that can never happen, so a failed fill
	// must not be ignored.
	if !a.Page.ForceFill(inputs[0], a.email()) {
		a.logf("[认证] 邮箱填写失败，跳过继续按钮")
		return false
	}
	a.ClickContinue()
	return true
}

// ---------------------------------------------------------------------------
// HTTP plumbing: the Go substitute for Playwright's context.request
// ---------------------------------------------------------------------------

// authRequest issues a request over the Chrome-TLS client while borrowing the
// live browser's cookie jar in both directions, which is what Playwright's
// context.request did implicitly (app.py:9985, 10038):
//
//	out: browser cookies matching the target URL are serialized into a Cookie header
//	in:  the response's Set-Cookie headers are written back into the rod browser
//
// The write-back is what makes the CSRF hand-off work: GET /api/auth/csrf mints
// __Host-next-auth.csrf-token via Set-Cookie, and the subsequent
// POST /api/auth/signin/openai is rejected unless that exact cookie accompanies
// the csrfToken form field.
//
// Client.Do is bypassed in favour of Client.HTTP because response headers (and
// therefore Set-Cookie) are not exposed by Do.
func (a *AuthURLBuilder) authRequest(method, rawURL, contentType string, body []byte, extra map[string]string) (int, []byte, error) {
	if a == nil || a.Client == nil || a.Client.HTTP == nil {
		return 0, nil, fmt.Errorf("缺少 TLS 客户端，无法复用浏览器代理与指纹")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, err
	}

	var req *http.Request
	if len(body) > 0 {
		req, err = http.NewRequest(method, rawURL, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, rawURL, nil)
	}
	if err != nil {
		return 0, nil, err
	}

	header := a.Client.ChromeHeaders()
	if ua := strings.TrimSpace(a.Fingerprint.UserAgent); ua != "" {
		header["user-agent"] = []string{ua}
	}
	for k, v := range extra {
		header[strings.ToLower(k)] = []string{v}
	}
	if contentType != "" {
		header["content-type"] = []string{contentType}
	}
	if cookie := a.cookieHeaderFor(parsed); cookie != "" {
		header["cookie"] = []string{cookie}
	}
	// Header ORDER is part of the TLS/HTTP fingerprint; keep Chrome's shape with
	// cookie last.
	header[http.HeaderOrderKey] = []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "user-agent",
		"content-type", "accept", "accept-language", "referer",
		"accept-encoding", "cookie",
	}
	req.Header = header

	resp, err := a.Client.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	a.storeBrowserCookies(authResponseSetCookies(resp), parsed)
	return resp.StatusCode, buf.Bytes(), nil
}

// cookieHeaderFor serializes the browser cookies that a real Chrome would send
// to u (domain + path + secure match, longest path first per RFC 6265).
func (a *AuthURLBuilder) cookieHeaderFor(u *url.URL) string {
	if a == nil {
		return ""
	}
	return browserCookieHeaderFor(a.Browser, u)
}

// browserCookieHeaderFor builds the Cookie header a browser would send to u.
// Playwright's context.request shares the context's cookie jar automatically;
// an out-of-band HTTP client does not, so any call that stands in for a
// context.request must bridge the jar by hand or it reaches auth.openai.com
// with no cf_clearance and gets challenged.
func browserCookieHeaderFor(b *browser.Browser, u *url.URL) string {
	if b == nil || b.Rod == nil || u == nil {
		return ""
	}
	cookies, err := b.Rod.GetCookies()
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}

	matched := make([]*proto.NetworkCookie, 0, len(cookies))
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		if !authCookieDomainMatches(c.Domain, host) {
			continue
		}
		if !authCookiePathMatches(c.Path, path) {
			continue
		}
		if c.Secure && u.Scheme != "https" {
			continue
		}
		matched = append(matched, c)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return len(matched[i].Path) > len(matched[j].Path)
	})

	seen := make(map[string]bool, len(matched))
	parts := make([]string, 0, len(matched))
	for _, c := range matched {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// storeBrowserCookies replays Set-Cookie into the rod browser. Cookies are set
// one at a time so a single rejected cookie cannot take the CSRF token down with
// it (a broad Python `except: pass` equivalent).
func (a *AuthURLBuilder) storeBrowserCookies(cookies []*http.Cookie, ref *url.URL) {
	if a == nil || a.Browser == nil || a.Browser.Rod == nil || ref == nil || len(cookies) == 0 {
		return
	}
	origin := ref.Scheme + "://" + ref.Host + "/"
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		param := &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HttpOnly,
		}
		if param.Domain == "" {
			param.URL = origin
		}
		// __Host- prefixed cookies are host-only, path-/ and secure by spec;
		// Chrome rejects a Network.setCookie that says otherwise.
		if strings.HasPrefix(c.Name, "__Host-") {
			param.Domain = ""
			param.URL = origin
			param.Path = "/"
			param.Secure = true
		}
		if !c.Expires.IsZero() {
			param.Expires = proto.TimeSinceEpoch(float64(c.Expires.Unix()))
		} else if c.MaxAge > 0 {
			param.Expires = proto.TimeSinceEpoch(float64(time.Now().Add(time.Duration(c.MaxAge) * time.Second).Unix()))
		}
		switch c.SameSite {
		case http.SameSiteLaxMode:
			param.SameSite = proto.NetworkCookieSameSiteLax
		case http.SameSiteStrictMode:
			param.SameSite = proto.NetworkCookieSameSiteStrict
		case http.SameSiteNoneMode:
			param.SameSite = proto.NetworkCookieSameSiteNone
		}
		_ = a.Browser.Rod.SetCookies([]*proto.NetworkCookieParam{param})
	}
}

// authResponseSetCookies extracts Set-Cookie from a response, tolerating the
// non-canonical header casing fhttp preserves for fingerprinting purposes.
func authResponseSetCookies(resp *http.Response) []*http.Cookie {
	if resp == nil {
		return nil
	}
	if cookies := resp.Cookies(); len(cookies) > 0 {
		return cookies
	}
	normalized := http.Header{}
	for k, vals := range resp.Header {
		if strings.EqualFold(k, "set-cookie") {
			normalized["Set-Cookie"] = append(normalized["Set-Cookie"], vals...)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return (&http.Response{Header: normalized}).Cookies()
}

// authCookieDomainMatches implements RFC 6265 domain matching, treating a
// leading dot as "and subdomains".
func authCookieDomainMatches(cookieDomain, host string) bool {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cookieDomain)), ".")
	if d == "" || host == "" {
		return false
	}
	h := strings.ToLower(host)
	return h == d || strings.HasSuffix(h, "."+d)
}

// authCookiePathMatches implements RFC 6265 §5.1.4 path matching.
func authCookiePathMatches(cookiePath, requestPath string) bool {
	if cookiePath == "" || cookiePath == "/" {
		return true
	}
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	if strings.HasSuffix(cookiePath, "/") {
		return true
	}
	return len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/'
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// authEncodePairs is urlencode() over an ordered sequence: Python's quote_plus
// and Go's url.QueryEscape agree (space -> '+', '@' -> %40).
func authEncodePairs(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
	}
	return strings.Join(parts, "&")
}

// authRandomUUID4 is str(uuid.uuid4()) — a random v4 UUID in canonical form.
func authRandomUUID4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; degrade to a time-derived value
		// rather than aborting a flow Python would have continued.
		now := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(b[0:8], now)
		binary.BigEndian.PutUint64(b[8:16], now^0x9e3779b97f4a7c15)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// authTruncateRunes slices by CHARACTERS like Python's str[:n], never splitting
// a multi-byte rune (the route-error text and API bodies are localized).
func authTruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// authAsString is `str(value or "")` (app.py:10045), NOT `str(value)`.
//
// The `or` is the whole point: it fires on EVERY falsy value, so False, 0, 0.0,
// "", [] and {} all collapse to "". An earlier version rendered false as
// "False" and 0 as "0", either of which would have been accepted as a CSRF
// token — non-empty, so the cookie re-scan at app.py:10048 never ran and
// POST /api/auth/signin/openai was sent with a garbage token.
//
// One documented approximation: encoding/json decodes every JSON number to
// float64, so the int/float split Python's json makes is unrecoverable here.
// Integral values are therefore rendered Python-int style ("403", not "403.0"),
// which is what the real payloads carry.
func authAsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return ""
	case float64:
		if t == 0 {
			return ""
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []any:
		if len(t) == 0 {
			return ""
		}
		return fmt.Sprintf("%v", t)
	case map[string]any:
		if len(t) == 0 {
			return ""
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
