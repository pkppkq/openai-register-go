package authproto

import (
	"bytes"
	"io"
	"net/url"
	"strings"
	"sync"

	http "github.com/bogdanfinn/fhttp"

	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// ---------------------------------------------------------------------------
// Transport
//
// Python drove one curl_cffi Session (impersonate=chrome136) with a cookie jar
// that survives every step of the flow. The Go equivalent is one
// internal/tlsclient Client plus the jar below, hidden behind the Transport
// interface so tests can drive the whole pipeline against a fake — NO TEST IN
// THIS PACKAGE MAY OPEN A SOCKET.
// ---------------------------------------------------------------------------

// Request is one outbound call. Header already carries fhttp's HeaderOrderKey:
// this endpoint fingerprints clients, so the order is part of the payload.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
	// FollowRedirects mirrors requests' allow_redirects. _follow_oauth_redirects
	// needs it off so it can read the Location header itself.
	FollowRedirects bool
}

// Response mirrors the subset of curl_cffi's Response that the flow reads.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// URL is curl's EFFECTIVE_URL: the final URL after any redirects
	// (curl_cffi Response.url, app.py:8571/8583/8680/8693/8703).
	URL string
}

// OK mirrors curl_cffi's Response.ok, which is `200 <= status_code < 400`
// (curl_cffi requests/session.py:309). NOTE this is NOT requests' `ok`
// (`status_code < 400`): a 1xx would be ok under requests and not here. The
// production path is curl_cffi, so curl_cffi's definition is the one ported.
func (r *Response) OK() bool { return r != nil && r.StatusCode >= 200 && r.StatusCode < 400 }

// Text mirrors Response.text.
func (r *Response) Text() string {
	if r == nil {
		return ""
	}
	return string(r.Body)
}

// Location is response.headers.get("location") — the first value, "" if absent.
// curl_cffi's Headers lookup is case-insensitive regardless of how the wire
// spelled it, and fhttp only canonicalizes keys it parsed itself, so the fold
// is done explicitly here.
func (r *Response) Location() string {
	if r == nil {
		return ""
	}
	if v := r.Header.Get("location"); v != "" {
		return v
	}
	for k, values := range r.Header {
		if strings.EqualFold(k, "location") && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

// Cookie is one jar entry, in the shape Python's http.cookiejar exposes.
type Cookie struct {
	Name   string
	Value  string
	Domain string
	Path   string
}

// Transport is the HTTP surface of the protocol flow.
type Transport interface {
	Do(req *Request) (*Response, error)
	// SetCookie is session.cookies.set(name, value, domain=...).
	SetCookie(name, value, domain string)
	// Cookies returns the jar in ITERATION ORDER — `for cookie in jar` in
	// _read_cookie takes the FIRST name match, so order decides the result.
	Cookies() []Cookie
}

// ---------------------------------------------------------------------------
// Cookie jar
// ---------------------------------------------------------------------------

// cookieJar is an insertion-ordered jar with Python's requests/http.cookiejar
// matching rules. It implements fhttp's http.CookieJar so tls-client can drive
// it across a redirect chain, and exposes the raw ordered entry list that
// _read_cookie walks.
//
// DIVERGENCE: CPython's CookieJar.__iter__ walks a nested
// domain -> path -> name dict, so entries group by domain-then-path insertion
// order; this jar is one flat insertion-ordered list. The two orders only
// differ when the same cookie NAME exists under several domains, and the only
// such cookie here is oai-did, which _set_device_cookie writes with the same
// value under all three domains.
//
// DIVERGENCE: Secure/HttpOnly/Expires are not modelled. Every URL in this flow
// is https and the flow is shorter-lived than any session cookie, so no
// expiring or scheme-gated cookie is ever suppressed.
type cookieJar struct {
	mu      sync.Mutex
	entries []Cookie
}

func newCookieJar() *cookieJar { return &cookieJar{} }

// SetCookies absorbs Set-Cookie headers. A repeated (name, domain, path) keeps
// its original position and only its value is replaced — CPython's dict-keyed
// jar behaves the same way.
func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if u == nil {
		return
	}
	host := strings.ToLower(u.Hostname())
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		domain := c.Domain
		if domain == "" {
			domain = host
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		j.set(c.Name, c.Value, domain, path)
	}
}

// Cookies is the outbound side: every entry whose domain and path match.
func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	if u == nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	path := u.Path
	if path == "" {
		path = "/"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.entries))
	for _, e := range j.entries {
		if !cookieDomainMatch(host, e.Domain) {
			continue
		}
		if !cookiePathMatch(path, e.Path) {
			continue
		}
		out = append(out, &http.Cookie{Name: e.Name, Value: e.Value})
	}
	return out
}

func (j *cookieJar) set(name, value, domain, path string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.entries {
		if j.entries[i].Name == name && j.entries[i].Domain == domain && j.entries[i].Path == path {
			j.entries[i].Value = value
			return
		}
	}
	j.entries = append(j.entries, Cookie{Name: name, Value: value, Domain: domain, Path: path})
}

func (j *cookieJar) all() []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Cookie, len(j.entries))
	copy(out, j.entries)
	return out
}

// cookieDomainMatch is the standard domain-cookie rule used for the OUTBOUND
// direction. (_read_cookie uses a looser suffix test of its own — see
// readCookie — and the two are deliberately not shared.)
func cookieDomainMatch(host, domain string) bool {
	if domain == "" {
		return true
	}
	d := strings.ToLower(strings.TrimLeft(domain, "."))
	return host == d || strings.HasSuffix(host, "."+d)
}

func cookiePathMatch(requestPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath == "/" {
		return true
	}
	if requestPath == cookiePath {
		return true
	}
	if strings.HasPrefix(requestPath, cookiePath) {
		return strings.HasSuffix(cookiePath, "/") || strings.HasPrefix(requestPath[len(cookiePath):], "/")
	}
	return false
}

// ---------------------------------------------------------------------------
// tls-client transport
// ---------------------------------------------------------------------------

// httpTransport is new_openai_http_session (app.py:5631-5642): one
// Chrome-impersonating client, one jar, an optional upstream proxy.
type httpTransport struct {
	client *tlsclient.Client
	jar    *cookieJar
}

// transportTimeoutSeconds: Python passes a per-request timeout (30 almost
// everywhere, 45 for the token exchange, 60 for the OAuth entry, and a stray
// `timeout=30000` at app.py:8248 that is plainly a ms-for-s typo). tls-client
// only has a client-wide timeout, so — exactly as internal/openai/teamapi.go
// does — the largest genuine value is applied once at construction.
//
// DIVERGENCE: a hung sentinel POST therefore waits up to 60s instead of 30s.
const transportTimeoutSeconds = 60

// newHTTPTransport mirrors new_openai_http_session(proxy_url) (app.py:5631).
// Python also passes verify=False; tls-client's Chrome profile does its own
// verification and the flow never talks to a MITM proxy that would need it off.
func newHTTPTransport(proxyURL string) (*httpTransport, error) {
	c, err := tlsclient.New(proxyURL, transportTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	jar := newCookieJar()
	c.HTTP.SetCookieJar(jar)
	return &httpTransport{client: c, jar: jar}, nil
}

// interface guards
var (
	_ http.CookieJar = (*cookieJar)(nil)
	_ Transport      = (*httpTransport)(nil)
)

func (t *httpTransport) Do(req *Request) (*Response, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	r, err := http.NewRequest(req.Method, req.URL, body)
	if err != nil {
		return nil, err
	}
	// The header set is taken WHOLESALE, not merged over
	// tlsclient.ChromeHeaders(): a leftover default that is not named in
	// HeaderOrderKey would be emitted in map-iteration position and shuffle the
	// fingerprint between runs.
	r.Header = req.Header
	t.client.HTTP.SetFollowRedirect(req.FollowRedirects)
	resp, err := t.client.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	effective := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		effective = resp.Request.URL.String()
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       raw,
		URL:        effective,
	}, nil
}

func (t *httpTransport) SetCookie(name, value, domain string) {
	t.jar.set(name, value, domain, "/")
}

func (t *httpTransport) Cookies() []Cookie { return t.jar.all() }

// ---------------------------------------------------------------------------
// Header construction
// ---------------------------------------------------------------------------

// headerPair is one ordered header. An ordered slice replaces Python's dict
// because Go map iteration is random and the emitted order is fingerprinted.
type headerPair struct{ Key, Value string }

// browserHeaderBase is openai_browser_headers' literal dict (app.py:5589-5598).
// ORDER IS THE DICT'S INSERTION ORDER and must not be sorted or regrouped.
// internal/openai.OpenAIBrowserHeaders holds the same values but returns a map,
// which cannot carry the order this endpoint reads.
var browserHeaderBase = []headerPair{
	{"user-agent", openai.DefaultUserAgent},
	{"accept-language", "zh-CN,zh;q=0.9,en;q=0.8"},
	{"sec-ch-ua", `"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"`},
	{"sec-ch-ua-full-version-list", `"Google Chrome";v="146.0.0.0", "Chromium";v="146.0.0.0", "Not.A/Brand";v="24.0.0.0"`},
	{"sec-ch-ua-mobile", "?0"},
	{"sec-ch-ua-platform", `"Windows"`},
	{"sec-ch-ua-platform-version", `"15.0.0"`},
	{"sec-ch-viewport-width", `"1365"`},
}

// buildHeader turns an ordered pair list into an fhttp header with
// HeaderOrderKey pinned, mirroring dict.update: a key already present keeps its
// ORIGINAL position and only its value changes; a new key is appended.
//
// DIVERGENCE: "cookie" is appended last. Python never puts it in the dict at
// all — libcurl injects it from the jar at a position curl_cffi's chrome136
// impersonation decides — so there is no Python order to copy; pinning it last
// at least keeps Go's own output stable across runs instead of letting fhttp
// place an unlisted header by map order.
//
// DIVERGENCE: curl_cffi calls curl.impersonate(profile, default_headers=True)
// (curl_cffi/requests/utils.py:664), so libcurl-impersonate ALSO injects the
// real Chrome default header block (accept, accept-encoding, sec-fetch-*,
// priority, upgrade-insecure-requests, ...) around the dict the Python code
// wrote, and the user's entries override matching names in place. This port
// emits ONLY the names app.py spelled out, in app.py's order; the browser
// defaults libcurl would have synthesized are absent. Reproducing them would
// mean hard-coding curl-impersonate's internal table, which is version-specific
// and would itself drift; the TLS/JA3 fingerprint — the part the endpoint
// actually gates on — is reproduced faithfully by internal/tlsclient.
func buildHeader(pairs []headerPair) http.Header {
	h := http.Header{}
	order := make([]string, 0, len(pairs)+1)
	for _, p := range pairs {
		key := strings.ToLower(p.Key)
		if _, exists := h[key]; !exists {
			order = append(order, key)
		}
		h[key] = []string{p.Value}
	}
	order = append(order, "cookie")
	h[http.HeaderOrderKey] = order
	return h
}

// browserHeaders is _headers(extra) = openai_browser_headers(extra)
// (app.py:8029-8030): the base dict with `extra` merged over it in the caller's
// literal order.
func browserHeaders(extra ...headerPair) http.Header {
	pairs := make([]headerPair, 0, len(browserHeaderBase)+len(extra))
	pairs = append(pairs, browserHeaderBase...)
	pairs = append(pairs, extra...)
	return buildHeader(pairs)
}

// headerOrderOf returns the pinned order, for tests and diagnostics.
func headerOrderOf(h http.Header) []string { return h[http.HeaderOrderKey] }
