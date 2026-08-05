package providerproxy

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/settings"
)

// sidAlphabet is random_provider_sid's alphabet (app.py:1034): 62 characters,
// upper before lower before digits.
const sidAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// SIDLength is the 8 of `secrets.choice(...) for _ in range(8)` (app.py:1035)
// and of the `[A-Za-z0-9]{8}` guard (app.py:997).
const SIDLength = 8

// RandomProviderSID ports random_provider_sid (app.py:1033-1035).
// secrets.choice is a CSPRNG, so crypto/rand is the right mirror: the sid is
// what makes one provider session distinct from the next, and a predictable one
// would collide across concurrent mints.
func RandomProviderSID() (string, error) {
	limit := big.NewInt(int64(len(sidAlphabet)))
	out := make([]byte, SIDLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = sidAlphabet[n.Int64()]
	}
	return string(out), nil
}

// validSID is re.fullmatch(r"[A-Za-z0-9]{8}", sid) (app.py:997), spelled out
// rather than compiled: RE2's `$` and Python's differ around a trailing
// newline, and fullmatch has no `$` semantics at all.
func validSID(sid string) bool {
	if len(sid) != SIDLength {
		return false
	}
	for i := 0; i < len(sid); i++ {
		c := sid[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// BuildProxyURL ports ProxyProviderConfig.build_proxy_url (app.py:991-1004).
//
// The result is the provider's rotating-session URL:
//
//	http://<user>-region-<REGION>-sid-<SID>-t-<DURATION>:<pass>@<host>:<port>
//
// Every component of the username segment is a provider directive, and that is
// where each config field lands:
//   - username  → the segment prefix (app.py:1002, quoted at app.py:1002)
//   - regions   → -region-XX, one entry of parse_provider_regions (app.py:995)
//   - duration  → -t-N, the session hold time in minutes (app.py:1004)
//   - password  → the userinfo password (app.py:1003)
//   - endpoint  → host:port (app.py:999-1001)
//   - enabled   → gates validated() (app.py:971), see the note below
//
// Pass sid == "" to mint a fresh session; a caller-supplied sid re-requests the
// same one (that is how -t- can be observed: same sid inside the window is the
// same exit).
//
// The scheme is unconditionally http, even if endpoint carries one of its own
// (app.py:1004) — the provider speaks HTTP CONNECT.
func BuildProxyURL(config settings.ProviderProxyConfig, region, sid string) (string, error) {
	// app.py:992. Validate is a no-op for a disabled role (app.py:971-972),
	// exactly as in Python; the manager only ever mints for an enabled one
	// (app.py:1224).
	if err := config.Validate(); err != nil {
		return "", err
	}
	// app.py:993: str(region or "").strip().upper() — pyStrip because
	// str.strip() also eats U+001C..U+001F, pyUpper because str.upper() is full
	// case mapping (this exact pair already produced a region-code bug in
	// internal/settings).
	region = pyUpper(pyStrip(region))
	regions, err := settings.ParseProviderRegions(config.Regions)
	if err != nil {
		return "", err
	}
	if !containsString(regions, region) {
		// app.py:995, verbatim.
		return "", fmt.Errorf("region 未配置: %s", region)
	}
	// app.py:996: `str(sid or random_provider_sid())` — Python truthiness, so
	// an empty string means "mint one".
	if sid == "" {
		sid, err = RandomProviderSID()
		if err != nil {
			return "", err
		}
	}
	if !validSID(sid) {
		// app.py:998, verbatim.
		return "", errors.New("sid 必须是 8 位字母或数字")
	}
	host, port, err := endpointHostPort(config.Endpoint)
	if err != nil {
		return "", err
	}
	// app.py:1001: re-bracket a bare IPv6 literal, because urlsplit's .hostname
	// strips the brackets it needs back.
	hostText := host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		hostText = "[" + host + "]"
	}
	// app.py:1002-1003. The username is stripped again here (the raw field is
	// used, not validated()'s local); the password is NOT stripped — a provider
	// password may legitimately start or end with a space.
	username := pyQuote(pyStrip(config.Username))
	password := pyQuote(config.Password)
	return fmt.Sprintf("http://%s-region-%s-sid-%s-t-%d:%s@%s:%d",
		username, region, sid, config.Duration, password, hostText, port), nil
}

// endpointHostPort reproduces app.py:999-1001 —
//
//	parsed = urlsplit(endpoint if "://" in endpoint else f"http://{endpoint}")
//	host   = parsed.hostname or ""
//	port   = parsed.port
//
// net/url.Parse is deliberately NOT used. urlsplit lower-cases .hostname and
// Go's url.Hostname() does not, so "US2.Proxy.invalid:3010" would mint a URL that
// differs from Python's byte for byte; and .port is an int, so ":03010" renders
// as 3010 in Python and would render as "03010" from url.Port().
//
// DIVERGENCE: when the endpoint carries no port, Python's f-string at
// app.py:1004 interpolates the literal string "None" and hands back a URL that
// can never connect. That is only reachable for a *disabled* role (validated()
// at app.py:992 already demands a port for an enabled one, and the pump only
// mints for enabled roles, app.py:1224), so this returns the validator's error
// text instead of forging a "None" port.
func endpointHostPort(endpoint string) (string, int, error) {
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	hostname, portText := urlsplitHostinfo(raw)
	host := pyLower(hostname)
	if portText == "" {
		return "", 0, errors.New("主机端口格式应为 hostname:port")
	}
	port, ok := pyParseInt10(portText)
	if !ok || port < 0 || port > 65535 {
		// urlsplit's .port property raises ValueError here (app.py:981 relies
		// on it); build_proxy_url does not catch it.
		return "", 0, errors.New("主机端口格式应为 hostname:port")
	}
	return host, port, nil
}

// whatwgTrimCutset is urllib's _WHATWG_C0_CONTROL_OR_SPACE: U+0000..U+0020.
func isWHATWGTrim(r rune) bool { return r <= 0x20 }

var whatwgRemove = strings.NewReplacer("\t", "", "\r", "", "\n", "")

// schemeChars mirrors urllib.parse.scheme_chars.
func isSchemeChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		r == '+' || r == '-' || r == '.'
}

// urlsplitHostinfo is CPython's SplitResult._hostinfo (Lib/urllib/parse.py)
// preceded by urlsplit's own scheme/netloc split, returning (hostname, port)
// as the raw strings the .hostname/.port properties are derived from.
//
// The exact split matters: _hostinfo partitions the netloc at the FIRST ':'
// (not the last), and takes anything after the last '@' as the host part.
func urlsplitHostinfo(rawURL string) (string, string) {
	url := whatwgRemove.Replace(strings.TrimFunc(rawURL, isWHATWGTrim))
	// urlsplit's scheme detection.
	if i := strings.IndexByte(url, ':'); i > 0 {
		first := rune(url[0])
		if (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') {
			valid := true
			for _, r := range url[:i] {
				if !isSchemeChar(r) {
					valid = false
					break
				}
			}
			if valid {
				url = url[i+1:]
			}
		}
	}
	netloc := ""
	if strings.HasPrefix(url, "//") {
		netloc = url[2:]
		if idx := strings.IndexAny(netloc, "/?#"); idx >= 0 {
			netloc = netloc[:idx]
		}
	}
	// _, _, hostinfo = netloc.rpartition('@')
	hostinfo := netloc
	if at := strings.LastIndexByte(netloc, '@'); at >= 0 {
		hostinfo = netloc[at+1:]
	}
	// _, have_open_br, bracketed = hostinfo.partition('[')
	if open := strings.IndexByte(hostinfo, '['); open >= 0 {
		bracketed := hostinfo[open+1:]
		hostname, rest := partition(bracketed, ']')
		_, port := partition(rest, ':')
		return hostname, port
	}
	hostname, port := partition(hostinfo, ':')
	return hostname, port
}

// partition is Python's str.partition(sep) reduced to (head, tail); tail is ""
// when sep is absent, which is what `if not port: port = None` keys off.
func partition(s string, sep byte) (string, string) {
	if i := strings.IndexByte(s, sep); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// pyParseInt10 is int(text, 10): surrounding whitespace and an optional sign
// are allowed, and PEP 515 permits single underscores between digits
// ("3_010" is 3010 to Python and a syntax error to strconv.Atoi).
//
// DIVERGENCE: Python also accepts non-ASCII decimal digits (int("٣٠١٠") is
// 3010). A port written in Arabic-Indic digits is not a case this port serves,
// and it is rejected by settings.Validate one step earlier regardless.
func pyParseInt10(text string) (int, bool) {
	t := pyStrip(text)
	switch {
	case strings.HasPrefix(t, "+"):
		t = t[1:]
	case strings.HasPrefix(t, "-"):
		// A negative port fails the 0..65535 range check either way; report the
		// value so the caller's range test rejects it.
		if n, ok := pyParseInt10(t[1:]); ok {
			return -n, true
		}
		return 0, false
	}
	if t == "" || strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") {
		return 0, false
	}
	t = strings.ReplaceAll(t, "_", "")
	if t == "" {
		return 0, false
	}
	value := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		value = value*10 + int(c-'0')
		if value > 1<<30 {
			return 0, false
		}
	}
	return value, true
}

// ---------------------------------------------------------------------------
// Python string primitives. Copies, not imports: internal/proxypool and
// internal/settings each keep their own unexported set, and this package must
// not edit either.
// ---------------------------------------------------------------------------

// isPySpace matches Python's str.isspace(). unicode.IsSpace covers the same set
// except the C0 separators U+001C..U+001F, which Python does treat as space and
// strings.TrimSpace does not.
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// upperFullReplacer covers the codepoints whose Python str.upper() EXPANDS to
// exactly two ASCII letters — the only expansions that can change a two-letter
// region or country match. Python's str.upper() is full case mapping;
// strings.ToUpper is simple case mapping and leaves all six unchanged. Same
// list, same reasoning, as internal/settings (which enumerated it by scanning
// all 1,112,064 codepoints).
var upperFullReplacer = strings.NewReplacer(
	"ß", "SS",
	"ﬀ", "FF",
	"ﬁ", "FI",
	"ﬂ", "FL",
	"ﬅ", "ST",
	"ﬆ", "ST",
)

// pyUpper mirrors Python's str.upper() closely enough for a region or country
// code.
func pyUpper(s string) string { return strings.ToUpper(upperFullReplacer.Replace(s)) }

// lowerFullReplacer is the mirror for str.lower(): U+0130 is the one codepoint
// whose Python lowercase expands (to "i" + U+0307 COMBINING DOT ABOVE) where
// Go's simple mapping yields a bare "i". It matters because urlsplit's
// .hostname lower-cases (app.py:1000).
var lowerFullReplacer = strings.NewReplacer("İ", "i̇")

// pyLower mirrors Python's str.lower().
func pyLower(s string) string { return strings.ToLower(lowerFullReplacer.Replace(s)) }

// pyQuote is urllib.parse.quote(value, safe="") — UTF-8 bytes, uppercase hex,
// and only the RFC 3986 unreserved set left alone (app.py:1002-1003). net/url
// has no equivalent: QueryEscape turns ' ' into '+' and PathEscape leaves ':'
// '@' '&' untouched, either of which corrupts the userinfo of a proxy URL.
func pyQuote(s string) string {
	const alwaysSafe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~"
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(alwaysSafe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
