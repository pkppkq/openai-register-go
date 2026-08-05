package authproto

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ---------------------------------------------------------------------------
// Fake transport
//
// NO TEST IN THIS PACKAGE MAY OPEN A SOCKET. Every step below runs against this
// in-memory transport; there is no code path here that can reach
// sentinel.openai.com, auth.openai.com or chatgpt.com.
// ---------------------------------------------------------------------------

type fakeTransport struct {
	handler  func(req *Request) (*Response, error)
	requests []*Request
	jar      *cookieJar
}

func newFakeTransport(handler func(req *Request) (*Response, error)) *fakeTransport {
	return &fakeTransport{handler: handler, jar: newCookieJar()}
}

func (f *fakeTransport) Do(req *Request) (*Response, error) {
	f.requests = append(f.requests, req)
	resp, err := f.handler(req)
	if resp != nil && resp.URL == "" {
		resp.URL = req.URL
	}
	return resp, err
}

func (f *fakeTransport) SetCookie(name, value, domain string) { f.jar.set(name, value, domain, "/") }
func (f *fakeTransport) Cookies() []Cookie                    { return f.jar.all() }

// find returns the first recorded request whose URL contains needle.
func (f *fakeTransport) find(needle string) *Request {
	for _, r := range f.requests {
		if strings.Contains(r.URL, needle) {
			return r
		}
	}
	return nil
}

func jsonResponse(status int, body string) *Response {
	return &Response{StatusCode: status, Body: []byte(body), Header: http.Header{}}
}

// sentinelOK is the canned sentinel requirements payload: no turnstile, no PoW,
// so fetchSentinelToken takes its cheap path.
const sentinelOK = `{"token":"tok-123","proofofwork":{"required":false},"turnstile":{"dx":null}}`

func newTestFlow(t *testing.T, tr Transport, mutate func(*Options)) *Flow {
	t.Helper()
	opts := Options{
		Account:   &models.MailAccount{Email: "User@Example.com", Password: "importedPassword12"},
		Log:       func(string) {},
		Transport: tr,
	}
	if mutate != nil {
		mutate(&opts)
	}
	f, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.sleep = func(time.Duration) {} // never actually sleep in tests
	return f
}

// ---------------------------------------------------------------------------
// Header ORDER — the fingerprinted property of every request
// ---------------------------------------------------------------------------

// baseOrder is openai_browser_headers' dict order (app.py:5589-5598).
var baseOrder = []string{
	"user-agent", "accept-language", "sec-ch-ua", "sec-ch-ua-full-version-list",
	"sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-ch-ua-platform-version",
	"sec-ch-viewport-width",
}

func withBase(extra ...string) []string {
	out := append([]string{}, baseOrder...)
	out = append(out, extra...)
	return append(out, "cookie")
}

func TestRequestHeaderOrder(t *testing.T) {
	cases := []struct {
		name  string
		drive func(f *Flow)
		match string
		want  []string
	}{
		{
			// app.py:8113-8120 — this call does NOT use _headers(); it sends
			// its own seven-header dict, in this order.
			name:  "_fetch_sentinel_token",
			drive: func(f *Flow) { _, _ = f.fetchSentinelToken("authorize_continue") },
			match: "sentinel.openai.com",
			want: []string{
				"content-type", "origin", "referer", "user-agent",
				"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "cookie",
			},
		},
		{
			// app.py:8173-8177
			name:  "_authorize_continue",
			drive: func(f *Flow) { _, _ = f.authorizeContinue() },
			match: "/api/accounts/authorize/continue",
			want:  withBase("content-type", "auth0-client", "openai-sentinel-token"),
		},
		{
			// app.py:8188
			name:  "_send_email_otp",
			drive: func(f *Flow) { _, _ = f.sendEmailOTP() },
			match: "/api/accounts/email-otp/send",
			want:  withBase("accept", "referer"),
		},
		{
			// app.py:8241-8246
			name:  "_email_otp_validate",
			drive: func(f *Flow) { _, _ = f.emailOTPValidate() },
			match: "/api/accounts/email-otp/validate",
			want:  withBase("accept", "content-type", "origin", "referer"),
		},
		{
			// app.py:8288-8296
			name:  "_password_verify",
			drive: func(f *Flow) { _, _ = f.passwordVerify() },
			match: "/api/accounts/password/verify",
			want: withBase("accept", "content-type", "auth0-client", "origin",
				"referer", "openai-sentinel-token", "oai-device-id"),
		},
		{
			// app.py:8325-8333
			name:  "_username_password_create",
			drive: func(f *Flow) { _, _ = f.usernamePasswordCreate() },
			match: "/api/accounts/user/register",
			want: withBase("accept", "content-type", "auth0-client", "origin",
				"referer", "openai-sentinel-token", "oai-device-id"),
		},
		{
			// app.py:8353-8360
			name:  "_create_account_profile",
			drive: func(f *Flow) { _, _ = f.createAccountProfile() },
			match: "/api/accounts/create_account",
			want: withBase("accept", "content-type", "auth0-client", "origin",
				"referer", "oai-device-id"),
		},
		{
			// app.py:8397-8400 (the consent GET)
			name:  "_select_workspace consent GET",
			drive: func(f *Flow) { _, _ = f.selectWorkspace("https://auth.openai.com/consent") },
			match: "/consent",
			want:  withBase("accept", "referer"),
		},
		{
			// app.py:8422-8427
			name:  "_send_phone_otp",
			drive: func(f *Flow) { _, _ = f.sendPhoneOTP("+15550000000") },
			match: "/api/accounts/add-phone/send",
			want:  withBase("accept", "content-type", "origin", "referer"),
		},
		{
			// app.py:8442-8447
			name:  "_validate_phone_otp",
			drive: func(f *Flow) { _, _ = f.validatePhoneOTP("123456") },
			match: "/api/accounts/phone-otp/validate",
			want:  withBase("accept", "content-type", "origin", "referer"),
		},
		{
			// app.py:8539
			name:  "_follow_oauth_redirects",
			drive: func(f *Flow) { _, _ = f.followOAuthRedirects("https://auth.openai.com/hop") },
			match: "/hop",
			want:  withBase("accept"),
		},
		{
			// app.py:8599-8606
			name:  "_exchange_code_for_token",
			drive: func(f *Flow) { _, _ = f.exchangeCodeForToken("code-1") },
			match: "/api/oauth/oauth2/token",
			want: withBase("accept", "content-type", "auth0-client",
				"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site"),
		},
		{
			// app.py:8643-8653
			name:  "_open_oauth_url",
			drive: func(f *Flow) { _, _ = f.openOAuthURL("https://auth.openai.com/api/accounts/authorize?x=1") },
			match: "/api/accounts/authorize",
			want: withBase("accept", "accept-encoding", "priority", "referer",
				"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site",
				"sec-fetch-user", "upgrade-insecure-requests"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newFakeTransport(func(req *Request) (*Response, error) {
				if strings.Contains(req.URL, "sentinel.openai.com") {
					return jsonResponse(200, sentinelOK), nil
				}
				// Anything else answers with a terminal error payload; the
				// header set has already been recorded by then.
				return jsonResponse(400, `{"error":{"code":"stop"}}`), nil
			})
			f := newTestFlow(t, tr, func(o *Options) {
				o.ManualEmailOTP = true
				o.InputCallback = func(kind, email, prompt string) (string, error) { return "123456", nil }
			})
			c.drive(f)
			req := tr.find(c.match)
			if req == nil {
				t.Fatalf("no request matching %q was issued (issued %d)", c.match, len(tr.requests))
			}
			if got := headerOrderOf(req.Header); !reflect.DeepEqual(got, c.want) {
				t.Errorf("header order:\n got %v\nwant %v", got, c.want)
			}
			// Every ordered name must actually be present (except "cookie",
			// which fhttp fills from the jar).
			for _, name := range c.want {
				if name == "cookie" {
					continue
				}
				if _, ok := req.Header[name]; !ok {
					t.Errorf("header %q is in the order list but absent from the header set", name)
				}
			}
		})
	}
}

func TestBrowserHeaderBaseValues(t *testing.T) {
	// app.py:5589-5598, verbatim.
	h := browserHeaders()
	want := map[string]string{
		"user-agent":                  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		"accept-language":             "zh-CN,zh;q=0.9,en;q=0.8",
		"sec-ch-ua":                   `"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"`,
		"sec-ch-ua-full-version-list": `"Google Chrome";v="146.0.0.0", "Chromium";v="146.0.0.0", "Not.A/Brand";v="24.0.0.0"`,
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"15.0.0"`,
		"sec-ch-viewport-width":       `"1365"`,
	}
	for k, v := range want {
		if got := h[k]; len(got) != 1 || got[0] != v {
			t.Errorf("header %s = %v, want [%q]", k, got, v)
		}
	}
}

func TestBuildHeaderUpdateKeepsOriginalPosition(t *testing.T) {
	// dict.update semantics: an overridden key does NOT move to the end.
	h := buildHeader([]headerPair{
		{"a", "1"}, {"b", "2"}, {"c", "3"}, {"b", "override"},
	})
	want := []string{"a", "b", "c", "cookie"}
	if got := headerOrderOf(h); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	// Header keys are stored lowercase here (fhttp's Get would canonicalize).
	if got := h["b"]; len(got) != 1 || got[0] != "override" {
		t.Errorf("b = %v, want [override]", got)
	}
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

func TestRequestBodiesAreCompactJSON(t *testing.T) {
	// curl_cffi serializes json= with separators=(",", ":")
	// (curl_cffi/requests/utils.py:452), NOT requests' spaced default.
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		if strings.Contains(req.URL, "sentinel.openai.com") {
			return jsonResponse(200, sentinelOK), nil
		}
		return jsonResponse(400, "{}"), nil
	})
	f := newTestFlow(t, tr, nil)
	_, _ = f.authorizeContinue()
	req := tr.find("/authorize/continue")
	// normalize_email_address strips and extracts, but does NOT lowercase
	// (app.py:1610-1614), so the imported casing survives onto the wire.
	const want = `{"username":{"kind":"email","value":"User@Example.com"}}`
	if got := string(req.Body); got != want {
		t.Errorf("authorize/continue body:\n got %s\nwant %s", got, want)
	}

	// The sentinel POST body is its own compact dict (app.py:8122).
	sent := tr.find("sentinel.openai.com")
	var probe map[string]any
	if err := json.Unmarshal(sent.Body, &probe); err != nil {
		t.Fatalf("sentinel body is not JSON: %v", err)
	}
	if probe["flow"] != "authorize_continue" || probe["id"] != f.DeviceID() {
		t.Errorf("sentinel body = %s", sent.Body)
	}
	if !strings.HasPrefix(probe["p"].(string), "gAAAAAC") {
		t.Errorf("sentinel p must be a requirements token, got %.16s", probe["p"])
	}
	if strings.Contains(string(sent.Body), ", ") {
		t.Errorf("sentinel body must be compact JSON: %s", sent.Body)
	}
}

func TestExchangeCodeForTokenFormOrder(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return jsonResponse(400, `{"error":{"code":"bad"}}`), nil
	})
	f := newTestFlow(t, tr, nil)
	f.codeVerifier = "verifier-1"
	_, _ = f.exchangeCodeForToken("the-code")
	req := tr.find("/api/oauth/oauth2/token")
	// app.py:8607-8613 dict order.
	const want = "grant_type=authorization_code&client_id=app_EMoamEEZ73f0CkXaXp7hrann&code=the-code&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&code_verifier=verifier-1"
	if got := string(req.Body); got != want {
		t.Errorf("token form:\n got %s\nwant %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// _prepare_login_url / _prepare_legacy_login_url (app.py:8066-8107)
// ---------------------------------------------------------------------------

func TestPrepareLoginURLParameterOrder(t *testing.T) {
	f := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil }), nil)
	got := f.PrepareLoginURL()
	if !strings.HasPrefix(got, "https://auth.openai.com/api/accounts/authorize?") {
		t.Fatalf("wrong endpoint: %s", got)
	}
	query := strings.TrimPrefix(got, "https://auth.openai.com/api/accounts/authorize?")
	names := make([]string, 0, 19)
	for _, part := range strings.Split(query, "&") {
		names = append(names, strings.SplitN(part, "=", 2)[0])
	}
	want := []string{
		"issuer", "client_id", "audience", "response_type", "response_mode",
		"redirect_uri", "device_id", "scope", "state", "nonce", "code_challenge",
		"code_challenge_method", "screen_hint", "max_age", "prompt",
		"id_token_add_organizations", "codex_cli_simplified_flow", "login_hint",
		"auth0Client",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("parameter order:\n got %v\nwant %v", names, want)
	}
	if f.State() == "" || f.codeVerifier == "" || f.nonce == "" {
		t.Error("PrepareLoginURL must mint state / nonce / code_verifier")
	}
	if len(f.codeVerifier) != 64 || len(f.State()) != 24 {
		t.Errorf("verifier/state lengths = %d/%d, want 64/24", len(f.codeVerifier), len(f.State()))
	}
}

func TestPrepareLegacyLoginURLReusesState(t *testing.T) {
	f := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil }), nil)
	_ = f.PrepareLoginURL()
	state := f.State()
	legacy := f.prepareLegacyLoginURL()
	if f.State() != state {
		t.Error("the legacy URL must NOT mint a new state")
	}
	query := strings.TrimPrefix(legacy, "https://auth.openai.com/oauth/authorize?")
	names := make([]string, 0, 11)
	for _, part := range strings.Split(query, "&") {
		names = append(names, strings.SplitN(part, "=", 2)[0])
	}
	want := []string{
		"client_id", "response_type", "redirect_uri", "scope", "state",
		"code_challenge", "code_challenge_method", "prompt",
		"id_token_add_organizations", "codex_cli_simplified_flow", "login_hint",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("parameter order:\n got %v\nwant %v", names, want)
	}
}

// ---------------------------------------------------------------------------
// Cookies (app.py:8032-8064)
// ---------------------------------------------------------------------------

func TestSetDeviceCookieWritesAllThreeDomains(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	f := newTestFlow(t, tr, nil)
	var domains []string
	for _, c := range tr.Cookies() {
		if c.Name == "oai-did" {
			domains = append(domains, c.Domain)
		}
	}
	want := []string{".auth.openai.com", "auth.openai.com", ".openai.com"}
	if !reflect.DeepEqual(domains, want) {
		t.Errorf("device cookie domains = %v, want %v", domains, want)
	}
	if f.readCookie("https://auth.openai.com", "oai-did") != f.DeviceID() {
		t.Error("readCookie must find the device id")
	}
}

func TestReadCookieUsesPythonsSuffixRule(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	f := newTestFlow(t, tr, nil)
	tr.SetCookie("session", "s-openai", ".openai.com")
	tr.SetCookie("other", "s-elsewhere", "example.org")

	if got := f.readCookie("https://auth.openai.com", "session"); got != "s-openai" {
		t.Errorf("suffix match failed: %q", got)
	}
	if got := f.readCookie("https://auth.openai.com", "other"); got != "" {
		t.Errorf("non-matching domain returned %q", got)
	}
	if got := f.readCookie("https://auth.openai.com", "missing"); got != "" {
		t.Errorf("missing cookie returned %q", got)
	}
	// Python's test is a bare endswith with NO dot boundary, so this
	// (arguably wrong) match is reproduced on purpose.
	if got := f.readCookie("https://notopenai.com", "session"); got != "s-openai" {
		t.Errorf("Python's boundary-free endswith must still match, got %q", got)
	}
	// First match in jar order wins.
	tr.SetCookie("session", "second", ".auth.openai.com")
	if got := f.readCookie("https://auth.openai.com", "session"); got != "s-openai" {
		t.Errorf("jar order must decide, got %q", got)
	}
}

func TestCookieJarOutboundMatching(t *testing.T) {
	jar := newCookieJar()
	jar.set("a", "1", ".openai.com", "/")
	jar.set("b", "2", "auth.openai.com", "/")
	jar.set("c", "3", "example.org", "/")
	names := func(host string) []string {
		u := mustURL(t, host)
		var out []string
		for _, c := range jar.Cookies(u) {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names("https://auth.openai.com/x"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("auth.openai.com -> %v", got)
	}
	if got := names("https://example.org/x"); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("example.org -> %v", got)
	}
	// Repeated set replaces the value in place, keeping insertion order.
	jar.set("a", "updated", ".openai.com", "/")
	all := jar.all()
	if len(all) != 3 || all[0].Name != "a" || all[0].Value != "updated" {
		t.Errorf("jar after update = %+v", all)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("bad url %q: %v", raw, err)
	}
	return u
}

// ---------------------------------------------------------------------------
// _fetch_sentinel_token behaviour (app.py:8109-8167)
// ---------------------------------------------------------------------------

func TestFetchSentinelTokenSolvesProofOfWork(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return jsonResponse(200, `{"token":"tok-9","proofofwork":{"required":true,"seed":"0.1","difficulty":"0"}}`), nil
	})
	f := newTestFlow(t, tr, nil)
	header, err := f.fetchSentinelToken("password_verify")
	if err != nil {
		t.Fatalf("fetchSentinelToken: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(header), &got); err != nil {
		t.Fatalf("token header is not JSON: %v", err)
	}
	// Key order of the emitted header value (app.py:8167).
	if !strings.HasPrefix(header, `{"p":"`) {
		t.Errorf("key order changed: %.20s", header)
	}
	for i, key := range []string{"p", "t", "c", "id", "flow"} {
		if !strings.Contains(header, `"`+key+`":`) {
			t.Errorf("key %d (%s) missing from %s", i, key, header)
		}
	}
	if got["t"] != "" || got["c"] != "tok-9" || got["flow"] != "password_verify" {
		t.Errorf("token header = %s", header)
	}
	if p, _ := got["p"].(string); !strings.HasPrefix(p, "gAAAAAB") {
		t.Errorf("with a required PoW, p must be a PROOF token (gAAAAAB), got %.10s", p)
	}
	// app.py:8164 — the token is mirrored into the oai-sc cookie with a "0"
	// prefix, on .openai.com.
	found := false
	for _, c := range tr.Cookies() {
		if c.Name == "oai-sc" {
			found = true
			if c.Value != "0tok-9" || c.Domain != ".openai.com" {
				t.Errorf("oai-sc = %q on %q", c.Value, c.Domain)
			}
		}
	}
	if !found {
		t.Error("oai-sc cookie was not set")
	}
}

func TestFetchSentinelTokenSkipsProofWhenNotRequired(t *testing.T) {
	// `required and seed and difficulty` — any falsy member skips the PoW.
	for _, body := range []string{
		`{"token":"t","proofofwork":{"required":true,"seed":"","difficulty":"0"}}`,
		`{"token":"t","proofofwork":{"required":true,"seed":"0.1","difficulty":""}}`,
		`{"token":"t","proofofwork":{"required":false,"seed":"0.1","difficulty":"0"}}`,
		`{"token":"t"}`,
	} {
		tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, body), nil })
		f := newTestFlow(t, tr, nil)
		header, err := f.fetchSentinelToken("x")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		var got map[string]any
		_ = json.Unmarshal([]byte(header), &got)
		if p, _ := got["p"].(string); !strings.HasPrefix(p, "gAAAAAC") {
			t.Errorf("%s: p should stay a requirements token, got %.10s", body, p)
		}
	}
}

func TestFetchSentinelTokenErrors(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(503, "upstream down"), nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.fetchSentinelToken("x")
	if err == nil || err.Error() != "请求 sentinel requirements 失败: 503 body=upstream down" {
		t.Errorf("err = %v", err)
	}

	// Missing token: the message re-serializes the PARSED payload with
	// json.dumps' DEFAULT separators and ensure_ascii=False.
	tr2 := newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(200, `{"token":"","note":"没有令牌","n":1}`), nil
	})
	f2 := newTestFlow(t, tr2, nil)
	_, err = f2.fetchSentinelToken("x")
	const want = `请求 sentinel token 失败: body={"token": "", "note": "没有令牌", "n": 1}`
	if err == nil || err.Error() != want {
		t.Errorf("err =\n %v\nwant\n %s", err, want)
	}
}

func TestFetchSentinelTokenTurnstileLogging(t *testing.T) {
	var logs []string
	tr := newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(200, `{"token":"t","turnstile":{"dx":"x","siteKey":"0xSITEKEY"}}`), nil
	})
	solved := ""
	f := newTestFlow(t, tr, func(o *Options) {
		o.Log = func(m string) { logs = append(logs, m) }
		o.TurnstileSolverEnabled = true
		o.TurnstileSolver = func(sitekey, pageURL, solverURL string, timeout time.Duration) (string, error) {
			solved = sitekey
			if pageURL != "https://auth.openai.com/api/accounts/authorize/continue" {
				t.Errorf("solver page url = %s", pageURL)
			}
			return "cf-token", nil
		}
	})
	if _, err := f.fetchSentinelToken("x"); err != nil {
		t.Fatal(err)
	}
	// dict-key precedence: sitekey, then siteKey, then site_key.
	if solved != "0xSITEKEY" {
		t.Errorf("solver sitekey = %q", solved)
	}
	if f.turnstileToken != "cf-token" {
		t.Errorf("turnstile token = %q", f.turnstileToken)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "Turnstile solver 返回 token（len=8）") {
		t.Errorf("logs = %v", logs)
	}

	// Solver disabled -> the "keep going" notice, verbatim.
	logs = nil
	f2 := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(200, `{"token":"t","turnstile":{"dx":"x"}}`), nil
	}), func(o *Options) { o.Log = func(m string) { logs = append(logs, m) } })
	if _, err := f2.fetchSentinelToken("x"); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0] != "Sentinel 返回 Turnstile 提示；未启用 solver，协议模式先继续尝试；若被拦截请换代理或改用浏览器流程" {
		t.Errorf("logs = %v", logs)
	}

	// Enabled but no sitekey.
	logs = nil
	f3 := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(200, `{"token":"t","turnstile":{"dx":"x","sitekey":"  "}}`), nil
	}), func(o *Options) {
		o.Log = func(m string) { logs = append(logs, m) }
		o.TurnstileSolverEnabled = true
	})
	if _, err := f3.fetchSentinelToken("x"); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0] != "Sentinel 返回 Turnstile 但未提供 sitekey，无法调用 solver；协议将继续尝试" {
		t.Errorf("logs = %v", logs)
	}
}

// ---------------------------------------------------------------------------
// _continue_url_from_payload (app.py:8196-8210)
// ---------------------------------------------------------------------------

func TestContinueURLFromPayload(t *testing.T) {
	f := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil }), nil)
	cases := []struct{ body, want string }{
		{`{"continue_url":"/a"}`, "https://auth.openai.com/a"},
		{`{"continue_url":"  "}`, ""},
		{`{"page":{"payload":{"url":"/b"}}}`, "https://auth.openai.com/b"},
		{`{"page":{"payload":{"continue_url":"/c"}}}`, "https://auth.openai.com/c"},
		// "url" wins over "continue_url" inside page.payload.
		{`{"page":{"payload":{"url":"/b","continue_url":"/c"}}}`, "https://auth.openai.com/b"},
		// An empty "url" is falsy, so the `or` falls through.
		{`{"page":{"payload":{"url":"","continue_url":"/c"}}}`, "https://auth.openai.com/c"},
		{`{"page":{"type":"email_otp_verification"}}`, "https://auth.openai.com/email-verification"},
		{`{"page":{"type":"other"}}`, ""},
		{`{}`, ""},
		{`[]`, ""},
		// A non-dict "page" is ignored entirely.
		{`{"page":"nope"}`, ""},
	}
	for _, c := range cases {
		payload, err := openai.DecodeOrderedJSON([]byte(c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if got := f.continueURLFromPayload(payload); got != c.want {
			t.Errorf("%s -> %q, want %q", c.body, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// _resolve_workspace_id (app.py:8375-8392)
// ---------------------------------------------------------------------------

func TestResolveWorkspaceIDPrefersNonPersonal(t *testing.T) {
	cases := []struct {
		name       string
		workspaces string
		want       string
		wantErr    bool
	}{
		{"team wins over personal", `[{"id":"p1","kind":"personal"},{"id":"t1","kind":"team"}]`, "t1", false},
		{"personal is the fallback", `[{"id":"p1","kind":"personal"}]`, "p1", false},
		{"no kind counts as non-personal", `[{"id":"x1"}]`, "x1", false},
		{"empty list errors", `[]`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := `{"workspaces":` + c.workspaces + `}`
			// base64url of the JSON, then a ".sig" tail — the cookie shape
			// app.py:8379 splits on ".".
			cookie := base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
			tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
			f := newTestFlow(t, tr, nil)
			tr.SetCookie("oai-client-auth-session", cookie, ".openai.com")
			got, err := f.resolveWorkspaceID()
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				if !strings.HasPrefix(err.Error(), "当前会话未发现 workspace: ") {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Errorf("workspace = %q, want %q", got, c.want)
			}
		})
	}

	// No cookie at all.
	f := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil }), nil)
	if _, err := f.resolveWorkspaceID(); err == nil ||
		err.Error() != "未找到 oai-client-auth-session cookie，无法提取 workspace" {
		t.Errorf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// _extract_auth_result (app.py:8516-8527)
// ---------------------------------------------------------------------------

func TestExtractAuthResult(t *testing.T) {
	f := newTestFlow(t, newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil }), nil)
	f.state = "st-1"

	got, err := f.extractAuthResult("http://localhost:1455/auth/callback?code=c1&state=st-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Code != "c1" || got.State != "st-1" {
		t.Errorf("result = %+v", got)
	}

	// parse_qs drops blank values, so an empty code reads as MISSING.
	if _, err := f.extractAuthResult("http://localhost:1455/auth/callback?code=&state=st-1"); err == nil ||
		!strings.HasPrefix(err.Error(), "callback 中缺少 code: ") {
		t.Errorf("err = %v", err)
	}
	if _, err := f.extractAuthResult("http://localhost:1455/auth/callback?code=c1"); err == nil ||
		!strings.HasPrefix(err.Error(), "callback 中缺少 state: ") {
		t.Errorf("err = %v", err)
	}
	if _, err := f.extractAuthResult("http://localhost:1455/auth/callback?code=c1&state=other"); err == nil ||
		err.Error() != "callback state 不匹配: expected=st-1 actual=other" {
		t.Errorf("err = %v", err)
	}
	// `if self.state and ...` — an unset state skips the comparison.
	f.state = ""
	if _, err := f.extractAuthResult("http://localhost:1455/auth/callback?code=c1&state=whatever"); err != nil {
		t.Errorf("unset state must not compare: %v", err)
	}
}

// ---------------------------------------------------------------------------
// _openai_password_for_account (app.py:8264-8276)
// ---------------------------------------------------------------------------

func TestOpenAIPasswordForAccount(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })

	// >= 12 characters: used verbatim.
	f := newTestFlow(t, tr, nil)
	if got := f.openaiPasswordForAccount(); got != "importedPassword12" {
		t.Errorf("long password = %q", got)
	}

	// < 12 characters: padded with "A7!" + sha256(email:password)[:12].
	// CPython: hashlib.sha256(b"user@example.com:short").hexdigest()[:12].
	f2 := newTestFlow(t, tr, func(o *Options) {
		o.Account = &models.MailAccount{Email: "user@example.com", Password: "short"}
	})
	got := f2.openaiPasswordForAccount()
	if !strings.HasPrefix(got, "shortA7!") || len(got) != len("shortA7!")+12 {
		t.Errorf("padded password = %q", got)
	}

	// Empty: a fresh 15-character password is generated AND stored back.
	acc := &models.MailAccount{Email: "user@example.com"}
	f3 := newTestFlow(t, tr, func(o *Options) { o.Account = acc })
	gen := f3.openaiPasswordForAccount()
	if len(gen) != 15 || !strings.HasSuffix(gen, "A7!") {
		t.Errorf("generated password = %q", gen)
	}
	if acc.Password != gen {
		t.Errorf("the generated password must be written back to the account")
	}
	for _, r := range gen[:12] {
		if !strings.ContainsRune(protocolPasswordAlphabet, r) {
			t.Errorf("character %q is outside the alphabet", r)
		}
	}
}

// ---------------------------------------------------------------------------
// Retry ladders
// ---------------------------------------------------------------------------

func TestBackoff(t *testing.T) {
	// min(6, attempt * 1.2)
	// Values are the IEEE-754 products Python computes, not their rounded
	// decimal forms: CPython's `3 * 1.2` is 3.5999999999999996 as well, so the
	// expectation is computed at RUNTIME (a Go untyped-constant 3*1.2 would be
	// exact 3.6 and would not match either language).
	runtimeProduct := func(attempt int) time.Duration {
		return time.Duration(float64(attempt) * 1.2 * float64(time.Second))
	}
	want := map[int]time.Duration{
		1: runtimeProduct(1),
		2: runtimeProduct(2),
		3: runtimeProduct(3),
		9: 6 * time.Second,
	}
	for attempt, w := range want {
		if got := backoff(attempt); got != w {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, w)
		}
	}
}

func TestExchangeCodeForTokenRetryPolicy(t *testing.T) {
	// A business error (4xx with a body) must NOT be retried, and the second
	// endpoint must still be tried once.
	var urls []string
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		urls = append(urls, req.URL)
		return jsonResponse(400, `{"error":{"code":"invalid_grant"}}`), nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.exchangeCodeForToken("c")
	if len(urls) != 2 {
		t.Errorf("business errors must not retry: %d calls", len(urls))
	}
	if err == nil || !strings.HasPrefix(err.Error(), "Code换Token失败: endpoint=https://auth.openai.com/oauth/token 400 code=invalid_grant") {
		t.Errorf("err = %v", err)
	}

	// A 5xx retries 4 times per endpoint = 8 calls.
	urls = nil
	tr2 := newFakeTransport(func(req *Request) (*Response, error) {
		urls = append(urls, req.URL)
		return jsonResponse(502, "boom"), nil
	})
	f2 := newTestFlow(t, tr2, nil)
	if _, err := f2.exchangeCodeForToken("c"); err == nil {
		t.Error("expected an error")
	}
	if len(urls) != 8 {
		t.Errorf("5xx must retry 4x per endpoint: %d calls", len(urls))
	}

	// An empty body retries too, even on a 4xx.
	urls = nil
	tr3 := newFakeTransport(func(req *Request) (*Response, error) {
		urls = append(urls, req.URL)
		return jsonResponse(400, "   "), nil
	})
	f3 := newTestFlow(t, tr3, nil)
	_, _ = f3.exchangeCodeForToken("c")
	if len(urls) != 8 {
		t.Errorf("blank body must retry: %d calls", len(urls))
	}

	// A transient TRANSPORT error retries 3 times then moves on.
	urls = nil
	tr4 := newFakeTransport(func(req *Request) (*Response, error) {
		urls = append(urls, req.URL)
		return nil, errors.New("Connection reset by peer")
	})
	f4 := newTestFlow(t, tr4, nil)
	_, _ = f4.exchangeCodeForToken("c")
	if len(urls) != 8 {
		t.Errorf("transient transport errors must retry: %d calls", len(urls))
	}

	// A NON-transient transport error breaks immediately (1 per endpoint).
	urls = nil
	tr5 := newFakeTransport(func(req *Request) (*Response, error) {
		urls = append(urls, req.URL)
		return nil, errors.New("no such host")
	})
	f5 := newTestFlow(t, tr5, nil)
	_, _ = f5.exchangeCodeForToken("c")
	if len(urls) != 2 {
		t.Errorf("non-transient transport errors must not retry: %d calls", len(urls))
	}
}

func TestFollowOAuthRedirectsRoutesEachPage(t *testing.T) {
	// The chain: a page redirect to /about-you (handled by
	// _create_account_profile), whose continue_url lands on the callback.
	step := 0
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "/api/accounts/create_account"):
			return jsonResponse(200, `{"continue_url":"http://localhost:1455/auth/callback?code=cX&state=st-1"}`), nil
		case strings.Contains(req.URL, "/start"):
			step++
			return &Response{
				StatusCode: 302,
				Header:     http.Header{"Location": {"https://auth.openai.com/about-you"}},
				URL:        req.URL,
			}, nil
		default:
			return jsonResponse(200, ""), nil
		}
	})
	f := newTestFlow(t, tr, nil)
	f.state = "st-1"
	got, err := f.followOAuthRedirects("https://auth.openai.com/start")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Code != "cX" {
		t.Errorf("result = %+v", got)
	}
}

func TestFollowOAuthRedirectsTooManyHops(t *testing.T) {
	n := 0
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		n++
		return &Response{
			StatusCode: 302,
			Header:     http.Header{"Location": {fmt.Sprintf("https://auth.openai.com/hop%d", n)}},
			URL:        req.URL,
		}, nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.followOAuthRedirects("https://auth.openai.com/hop0")
	if err == nil || !strings.HasPrefix(err.Error(), "OAuth跳转次数过多，最后停在: ") {
		t.Errorf("err = %v", err)
	}
	if n != 10 {
		t.Errorf("hop cap = %d, want 10", n)
	}
}

func TestFollowOAuthRedirectsUnknownTerminal(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return &Response{StatusCode: 200, URL: "https://auth.openai.com/mystery", Header: http.Header{}}, nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.followOAuthRedirects("https://auth.openai.com/start")
	const want = "OAuth跳转未到达callback: status=200 url=https://auth.openai.com/mystery"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

// ---------------------------------------------------------------------------
// _email_otp_validate retry (app.py:8235-8262)
// ---------------------------------------------------------------------------

func TestEmailOTPValidateRetriesOnWrongCode(t *testing.T) {
	validates := 0
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "/email-otp/validate"):
			validates++
			if validates == 1 {
				return jsonResponse(400, `{"error":{"code":"wrong_email_otp_code"}}`), nil
			}
			return jsonResponse(200, `{"continue_url":"/done"}`), nil
		case strings.Contains(req.URL, "/email-otp/send"):
			return jsonResponse(200, `{"continue_url":"/email-verification"}`), nil
		}
		return jsonResponse(200, "{}"), nil
	})
	f := newTestFlow(t, tr, func(o *Options) {
		o.ManualEmailOTP = true
		o.InputCallback = func(kind, email, prompt string) (string, error) { return "123456", nil }
	})
	got, err := f.emailOTPValidate()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "https://auth.openai.com/done" || validates != 2 {
		t.Errorf("continue=%q validates=%d", got, validates)
	}
}

func TestEmailOTPValidateDeactivatedAccount(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return jsonResponse(403, `{"error":{"code":"account_deactivated"}}`), nil
	})
	f := newTestFlow(t, tr, func(o *Options) {
		o.ManualEmailOTP = true
		o.InputCallback = func(kind, email, prompt string) (string, error) { return "123456", nil }
	})
	_, err := f.emailOTPValidate()
	var deactivated *models.AccountDeactivatedError
	if !errors.As(err, &deactivated) {
		t.Fatalf("err = %#v, want AccountDeactivatedError", err)
	}
	const want = "OpenAI 在邮箱验证码校验时返回 account_deactivated: 403 code=account_deactivated"
	if err.Error() != want {
		t.Errorf("err = %q", err.Error())
	}
}

func TestReadEmailOTPCodeStripsNonDigits(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	cases := []struct{ in, want string }{
		{"  123 456 ", "123456"},
		{"code: 987654", "987654"},
		// No digits at all -> `digits or code` falls back to the raw text.
		{"abc", "abc"},
		// Python's \D is Unicode-aware, so fullwidth digits are KEPT.
		{"１２３456", "１２３456"},
	}
	for _, c := range cases {
		f := newTestFlow(t, tr, func(o *Options) {
			o.ManualEmailOTP = true
			o.InputCallback = func(kind, email, prompt string) (string, error) { return c.in, nil }
		})
		got, err := f.readEmailOTPCode()
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("readEmailOTPCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Cancelled input.
	f := newTestFlow(t, tr, func(o *Options) {
		o.ManualEmailOTP = true
		o.InputCallback = func(kind, email, prompt string) (string, error) { return "   ", nil }
	})
	if _, err := f.readEmailOTPCode(); err == nil || err.Error() != "已取消邮箱验证码输入" {
		t.Errorf("err = %v", err)
	}

	// Manual mode with no callback configured.
	f2 := newTestFlow(t, tr, func(o *Options) { o.ManualEmailOTP = true })
	if _, err := f2.readEmailOTPCode(); err == nil ||
		err.Error() != "已启用手动输入邮箱验证码，但未配置输入回调" {
		t.Errorf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Phone handling (HTTP shape only — no provider is ever contacted for real)
// ---------------------------------------------------------------------------

func TestSendPhoneOTPClassifiesRejection(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) {
		return jsonResponse(400, `{"error":{"code":"phone_number_in_use"}}`), nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.sendPhoneOTP("+15550000000")
	var rejected *models.PhoneRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %#v, want PhoneRejectedError", err)
	}
	if rejected.Status != "手机号已使用" {
		t.Errorf("status = %q", rejected.Status)
	}
	if !strings.HasPrefix(err.Error(), "手机号已使用: 400 code=phone_number_in_use") {
		t.Errorf("err = %q", err.Error())
	}

	// An unclassifiable rejection stays a plain error.
	tr2 := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(500, "oops"), nil })
	f2 := newTestFlow(t, tr2, nil)
	_, err = f2.sendPhoneOTP("+1")
	if err == nil || err.Error() != "SendPhoneOtp请求失败: 500 body=oops" {
		t.Errorf("err = %v", err)
	}
	if errors.As(err, &rejected) {
		t.Error("an unclassified failure must not be a PhoneRejectedError")
	}
}

func TestHandleAddPhoneWithoutProviderOrManual(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	f := newTestFlow(t, tr, func(o *Options) { o.AllowManualPhone = false })
	_, err := f.handleAddPhone()
	var required *models.PhoneRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %#v, want PhoneRequiredError", err)
	}
	if required.Status != "协议需手机号(未接码)" {
		t.Errorf("status = %q", required.Status)
	}
	if err.Error() != "OpenAI 要求手机号验证；协议注册取Session已禁用接码，已跳过（未取号、未扣费）" {
		t.Errorf("err = %q", err.Error())
	}

	// Manual allowed but no callback.
	f2 := newTestFlow(t, tr, func(o *Options) { o.AllowManualPhone = true })
	if _, err := f2.handleAddPhone(); err == nil ||
		err.Error() != "未配置手机号池，也未配置手动输入回调" {
		t.Errorf("err = %v", err)
	}
}

func TestHandleAddPhoneRollsToNextNumber(t *testing.T) {
	// The FIRST number is rejected by OpenAI, the SECOND succeeds. No live SMS
	// provider is involved: the PhoneProvider here is a pure in-memory stub.
	var actions []string
	numbers := []string{"+1111", "+2222"}
	idx := 0
	provider := func(action, email string, payload any) (any, error) {
		actions = append(actions, action)
		switch action {
		case "next":
			if idx >= len(numbers) {
				return nil, nil
			}
			n := numbers[idx]
			idx++
			return map[string]any{"number": n}, nil
		case "code":
			return "654321", nil
		}
		return nil, nil
	}
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "/add-phone/send"):
			if strings.Contains(string(req.Body), "+1111") {
				return jsonResponse(400, `{"error":{"code":"phone_number_in_use"}}`), nil
			}
			return jsonResponse(200, `{"continue_url":"/phone-verification"}`), nil
		case strings.Contains(req.URL, "/phone-otp/validate"):
			return jsonResponse(200, `{"continue_url":"/done"}`), nil
		}
		return jsonResponse(200, "{}"), nil
	})
	f := newTestFlow(t, tr, func(o *Options) { o.PhoneProvider = provider })
	got, err := f.handleAddPhone()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "https://auth.openai.com/done" {
		t.Errorf("continue = %q", got)
	}
	want := []string{"next", "bad", "next", "sent", "code", "good"}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("provider actions = %v, want %v", actions, want)
	}
}

func TestCloneWithErrorKeepsOrder(t *testing.T) {
	phone := newOrderedMap("number", "+1", "sms_url", "u")
	got := pyJSONDumps(cloneWithError(phone, "boom", "手机号不可用"), false, true)
	const want = `{"number":"+1","sms_url":"u","error":"boom","status":"手机号不可用"}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// run() (app.py:8669-8743)
// ---------------------------------------------------------------------------

func TestRunHappyPathLoginWithPassword(t *testing.T) {
	var tr *fakeTransport
	tr = newFakeTransport(func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "sentinel.openai.com"):
			return jsonResponse(200, sentinelOK), nil
		case strings.Contains(req.URL, "/api/accounts/authorize?"):
			// The entry redirects to the password page.
			return &Response{StatusCode: 200, URL: "https://auth.openai.com/log-in/password", Header: http.Header{}}, nil
		case strings.Contains(req.URL, "/api/accounts/password/verify"):
			return jsonResponse(200, `{"continue_url":"http://localhost:1455/auth/callback?code=CODE1&state=`+testState(tr)+`"}`), nil
		case strings.Contains(req.URL, "/oauth2/token"):
			return jsonResponse(200, testTokenPayload()), nil
		}
		return jsonResponse(200, "{}"), nil
	})
	f := newTestFlow(t, tr, nil)
	record, err := f.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if record.AccountID != "acct-42" || record.RefreshToken != "rt-1" {
		t.Errorf("record = %+v", record)
	}
	if record.Email != "user@example.com" {
		t.Errorf("email = %q", record.Email)
	}
	if record.Type != "codex" {
		t.Errorf("type = %q", record.Type)
	}
}

func TestRunFallsBackToLegacyEntryOn403(t *testing.T) {
	var seen []string
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		if strings.Contains(req.URL, "/authorize?") {
			seen = append(seen, req.URL)
			if strings.Contains(req.URL, "/api/accounts/authorize?") {
				return &Response{StatusCode: 403, Body: []byte("denied"), Header: http.Header{}, URL: req.URL}, nil
			}
			return &Response{StatusCode: 200, URL: "https://auth.openai.com/log-in", Header: http.Header{}}, nil
		}
		return jsonResponse(500, "stop"), nil
	})
	f := newTestFlow(t, tr, nil)
	_, _ = f.Run()
	if len(seen) != 2 {
		t.Fatalf("expected an accounts entry then a legacy entry, got %v", seen)
	}
	if !strings.Contains(seen[1], "/oauth/authorize?") {
		t.Errorf("fallback URL = %s", seen[1])
	}
}

func TestRunRejectsCloudflareChallenge(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return &Response{
			StatusCode: 503,
			Body:       []byte("<title>Just a moment...</title>"),
			Header:     http.Header{},
			URL:        req.URL,
		}, nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.Run()
	if err == nil || err.Error() != turnstileChallengeMsg {
		t.Errorf("err = %v", err)
	}
}

func TestRunRejectsUnexpectedLandingURL(t *testing.T) {
	tr := newFakeTransport(func(req *Request) (*Response, error) {
		return &Response{StatusCode: 200, URL: "https://chatgpt.com/", Header: http.Header{}}, nil
	})
	f := newTestFlow(t, tr, nil)
	_, err := f.Run()
	if err == nil || err.Error() != "OauthUrl重定向到错误的URL: https://chatgpt.com/" {
		t.Errorf("err = %v", err)
	}
}

func TestRunShortCircuitsWhenEntryLandsOnCallback(t *testing.T) {
	var tr *fakeTransport
	tr = newFakeTransport(func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "/authorize?"):
			return &Response{
				StatusCode: 200,
				URL:        "http://localhost:1455/auth/callback?code=CODE0&state=" + testState(tr),
				Header:     http.Header{},
			}, nil
		case strings.Contains(req.URL, "/oauth2/token"):
			return jsonResponse(200, testTokenPayload()), nil
		}
		return jsonResponse(500, "unexpected"), nil
	})
	f := newTestFlow(t, tr, nil)
	record, err := f.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if record.AccountID != "acct-42" {
		t.Errorf("record = %+v", record)
	}
	// No email/password/sentinel round trip should have happened.
	if tr.find("sentinel.openai.com") != nil {
		t.Error("the short-circuit path must not touch sentinel")
	}
}

// testState digs the state out of the authorize URL the flow already issued, so
// the fake callback can echo a matching one.
func testState(tr *fakeTransport) string {
	for _, r := range tr.requests {
		if i := strings.Index(r.URL, "&state="); i >= 0 {
			rest := r.URL[i+len("&state="):]
			if j := strings.Index(rest, "&"); j >= 0 {
				return rest[:j]
			}
			return rest
		}
	}
	return ""
}

// testTokenPayload builds a token response whose JWTs carry the claims
// normalize_openai_auth_record requires.
func testTokenPayload() string {
	claims := `{"exp":4102444800,"email":"user@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-42"}}`
	jwt := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	return fmt.Sprintf(`{"access_token":%q,"refresh_token":"rt-1","id_token":%q}`, jwt, jwt)
}
