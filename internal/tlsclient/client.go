// Package tlsclient wraps bogdanfinn/tls-client with recent-Chrome TLS
// impersonation and optional upstream proxy — the Go replacement for the
// Python curl_cffi (impersonate=chrome136) HTTP path. Proven in cmd/tlspoc:
// reproduces a Chrome JA3 and passes auth.openai.com's Cloudflare.
package tlsclient

import (
	"bytes"
	"io"
	"sort"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// chromeProfileCandidates mirrors the Python resolve_curl_cffi_impersonate order:
// prefer the most recent Chrome profile the installed library actually ships.
var chromeProfileCandidates = []string{
	"chrome_150", "chrome_146", "chrome_144", "chrome_133", "chrome_131", "chrome_120",
}

// ResolveChromeProfile returns the newest available Chrome client profile.
func ResolveChromeProfile() (string, profiles.ClientProfile) {
	for _, name := range chromeProfileCandidates {
		if p, ok := profiles.MappedTLSClients[name]; ok {
			return name, p
		}
	}
	return "default", profiles.DefaultClientProfile
}

// Client is a thin wrapper adding Chrome-like default headers.
type Client struct {
	HTTP        tls_client.HttpClient
	ProfileName string
	UserAgent   string
}

// New builds a Chrome-impersonating client. proxyURL may be "" (direct) or a
// full URL like http://user:pass@host:port or socks5://host:port.
func New(proxyURL string, timeoutSeconds int) (*Client, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	name, profile := ResolveChromeProfile()
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(timeoutSeconds),
		tls_client.WithClientProfile(profile),
		// tls_client only installs a jar when asked, so WITHOUT this every
		// Set-Cookie is silently dropped and each request looks like a brand new
		// visitor. Python's counterpart is a curl_cffi Session, which keeps cookies
		// by definition — every flow ported on top of this assumed the same.
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	}
	if proxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxyURL))
	}
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	return &Client{
		HTTP:        c,
		ProfileName: name,
		UserAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	}, nil
}

// NewOrNil is New but returns nil on construction error, for callers that treat
// client creation as infallible.
func NewOrNil(proxyURL string, timeoutSeconds int) *Client {
	c, _ := New(proxyURL, timeoutSeconds)
	return c
}

// SetProxy swaps the upstream proxy on the live client (tls-client supports this).
func (c *Client) SetProxy(proxyURL string) error {
	return c.HTTP.SetProxy(proxyURL)
}

// DoSimple is a convenience over Do for plain JSON/form APIs: string-map headers
// and a []byte body, returning status, body, error.
func (c *Client) DoSimple(method, url string, body []byte, header map[string]string) (int, []byte, error) {
	extra := http.Header{}
	for k, v := range header {
		extra[k] = []string{v}
	}
	var r io.Reader
	if len(body) > 0 {
		r = bytes.NewReader(body)
	}
	return c.Do(method, url, r, extra)
}

// ChromeHeaders returns a Chrome-like header set with correct header ORDER
// (order is part of the fingerprint). Callers may override/extend entries.
func (c *Client) ChromeHeaders() http.Header {
	return http.Header{
		// Values copied from new_openai_http_session (app.py:5586-5600), the
		// general-purpose session factory these calls are ported from.
		//
		// The old sec-ch-ua claimed bare "Chromium" while user-agent claims
		// "Chrome/146"; a client hint that contradicts the UA is itself a signal,
		// on endpoints picked precisely because they fingerprint. accept-language
		// is zh-CN in Python and is left as Python has it rather than "corrected",
		// because that is the combination getting through in production today.
		"sec-ch-ua":          {`"Google Chrome";v="146", "Chromium";v="146", "Not.A/Brand";v="24"`},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"user-agent":         {c.UserAgent},
		"accept":             {"application/json, text/plain, */*"},
		"accept-language":    {"zh-CN,zh;q=0.9,en;q=0.8"},
		"accept-encoding":    {"gzip, deflate, br"},
		http.HeaderOrderKey:  {"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "user-agent", "accept", "accept-language", "accept-encoding"},
	}
}

// Do issues a request with the given method/url/body and Chrome headers merged
// with any extra headers provided. Returns status, body, error.
func (c *Client) Do(method, url string, body io.Reader, extra http.Header) (int, []byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header = c.ChromeHeaders()
	for k, vals := range extra {
		req.Header[k] = vals
	}
	completeHeaderOrder(req.Header)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// completeHeaderOrder makes the wire order of a merged header set deterministic.
//
// A caller that supplies its own HeaderOrderKey REPLACES the default order, and
// any Chrome default header it did not name is then emitted in Go map-iteration
// position — i.e. shuffled on every request. That is a moving HTTP fingerprint on
// endpoints chosen precisely because they fingerprint clients, and it is the same
// defect already fixed in internal/openai/teamapi.go.
//
// Names the caller ordered keep their exact positions; anything left over is
// appended in sorted order so the result is stable run to run.
func completeHeaderOrder(h http.Header) {
	order := h[http.HeaderOrderKey]
	if len(order) == 0 {
		return
	}
	named := make(map[string]bool, len(order))
	for _, k := range order {
		named[strings.ToLower(k)] = true
	}
	var missing []string
	for k := range h {
		lower := strings.ToLower(k)
		if lower == strings.ToLower(http.HeaderOrderKey) || lower == strings.ToLower(http.PHeaderOrderKey) {
			continue
		}
		if !named[lower] {
			missing = append(missing, lower)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	h[http.HeaderOrderKey] = append(append([]string(nil), order...), missing...)
}
