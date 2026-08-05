package proxypool

import (
	"errors"
	"strings"
)

// The proxy-URL accessors ProxyChainServer needs (app.py:6111-6135). They are
// exported from here rather than reimplemented in internal/proxychain because
// they must agree, byte for byte, with the urlsplit port that built the URL in
// the first place — the chain dials whatever normalize_proxy_url produced.
//
// net/url is the wrong tool for this and was the bug: url.Parse REJECTS
// "http://u:p@h:notaport" and "http://u:p%zz@h:1" outright, where urlparse
// happily yields a hostname and credentials; url.URL.Hostname() does not
// lowercase; and url.Port() is a string, so Python's `parsed.port or 443`
// (which fires on port 0) has no direct equivalent.

// URL is urllib.parse.urlsplit() applied to a proxy URL.
type URL struct {
	parts splitResult
}

// ParseURL is urlsplit(raw). It never fails; see urlsplit's own note on the
// ValueError cases app.py does not catch here either.
func ParseURL(raw string) URL { return URL{parts: urlsplit(raw)} }

// Scheme is SplitResult.scheme, already lowercased by urlsplit.
func (u URL) Scheme() string { return u.parts.Scheme }

// Hostname is SplitResult.hostname: brackets unwrapped and lowercased ("" for
// Python's None).
func (u URL) Hostname() string { return u.parts.hostname() }

// PortOr is Python's `parsed.port or fallback`. Note that `or`: an explicit
// port 0 is falsy and yields the fallback, not 0. The error is the ValueError
// SplitResult.port raises for a non-ASCII-digit port or one outside 0..65535 —
// app.py lets that propagate out of _connect_proxy as the chain error.
func (u URL) PortOr(fallback int) (int, error) {
	value, has, err := u.parts.port()
	if err != nil {
		return 0, err
	}
	if !has || value == 0 {
		return fallback, nil
	}
	return value, nil
}

// Credentials is (unquote(parsed.username or ""), unquote(parsed.password or
// "")) — app.py:6155-6156 and 6229-6231. The values are percent-DECODED,
// because _join_proxy_url percent-encoded them on the way in.
//
// DIVERGENCE: Python decodes with errors="replace", so an undecodable byte
// becomes U+FFFD and is then re-encoded as EF BF BD into the SOCKS5 auth
// packet; this keeps the raw byte. Only reachable for a hand-typed "%FF" in a
// credential — anything normalize_proxy_url built is valid UTF-8.
func (u URL) Credentials() (username, password string) {
	user, hasUser, pass, _ := u.parts.userinfo()
	if !hasUser {
		return "", ""
	}
	return pyUnquote(user), pyUnquote(pass)
}

// HasUsername reports `parsed.username` being truthy, which is the exact test
// _proxy_auth (app.py:6229) uses before emitting Proxy-Authorization.
func (u URL) HasUsername() bool {
	user, hasUser, _, _ := u.parts.userinfo()
	return hasUser && user != ""
}

// Query is SplitResult.query, raw (never percent-decoded).
func (u URL) Query() string { return u.parts.Query }

// usesParams is urllib.parse.uses_params, the schemes for which urlparse (NOT
// urlsplit) peels a ";params" tail off the last path segment.
var usesParams = map[string]bool{
	"": true, "ftp": true, "hdl": true, "prospero": true, "http": true,
	"imap": true, "https": true, "shttp": true, "rtsp": true, "rtsps": true,
	"rtspu": true, "sip": true, "sips": true, "mms": true, "sftp": true,
	"tel": true,
}

// Path is urlPARSE's path: urlsplit's path with the ";params" tail removed for
// a uses_params scheme. _rewrite_plain_request (app.py:6068) uses urlparse, so
// "GET /a;b HTTP/1.1" through the chain really does go out as "GET /a".
//
// It is the RAW path: urlsplit never percent-decodes, so "/a%2Fb" stays
// "/a%2Fb". net/url.URL.Path would hand back the decoded "/a/b" and the
// rewritten request line would ask the origin for a different resource.
func (u URL) Path() string {
	path := u.parts.Path
	if !usesParams[u.parts.Scheme] || !strings.Contains(path, ";") {
		return path
	}
	// _splitparams: the ';' must be at or after the last '/'.
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		i := strings.IndexByte(path[slash:], ';')
		if i < 0 {
			return path
		}
		return path[:slash+i]
	}
	return path[:strings.IndexByte(path, ';')]
}

// ErrNoHost is `代理地址缺少 host` (app.py:6117), raised before any dial.
var ErrNoHost = errors.New("代理地址缺少 host")

// pyUnquote is urllib.parse.unquote: decode %XX, leave malformed escapes alone.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, okHi := hexDigit(s[i+1])
			lo, okLo := hexDigit(s[i+2])
			if okHi && okLo {
				out = append(out, byte(hi<<4|lo))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

func hexDigit(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}
