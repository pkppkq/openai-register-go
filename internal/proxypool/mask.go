package proxypool

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// mask_proxy_url (app.py:2564-2576) lives here rather than next to its callers
// because it is a sibling of normalize_proxy_url in app.py's proxy-string layer
// and needs the same urlsplit/urlunsplit port. internal/proxychain and
// internal/proxyroute both already depend on this package; giving each its own
// copy would mean three copies of the SplitResult.port/.hostname semantics
// below, which are where every observed mismatch came from.

// pyLower is str.lower(). U+0130 is the only code point whose full lowercase is
// longer than one rune, and strings.ToLower (simple case mapping) drops the
// combining dot. Reachable here: SplitResult.hostname lowercases the host.
func pyLower(s string) string {
	if strings.ContainsRune(s, 'İ') {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

// hostinfo ports SplitResult._hostinfo: the netloc after the LAST "@", split
// into host and port. With a bracketed host the port is whatever follows the
// first ":" after "]" — note that "[::1]x:9" therefore yields port "9", not
// no port at all.
func (s splitResult) hostinfo() (host, port string, hasPort bool) {
	info := s.Netloc
	if i := strings.LastIndexByte(info, '@'); i >= 0 {
		info = info[i+1:]
	}
	if i := strings.IndexByte(info, '['); i >= 0 {
		bracketed := info[i+1:]
		if j := strings.IndexByte(bracketed, ']'); j >= 0 {
			host = bracketed[:j]
			rest := bracketed[j+1:]
			if k := strings.IndexByte(rest, ':'); k >= 0 {
				port = rest[k+1:]
			}
		} else {
			host = bracketed
		}
	} else if i := strings.IndexByte(info, ':'); i >= 0 {
		host, port = info[:i], info[i+1:]
	} else {
		host = info
	}
	return host, port, port != ""
}

// hostname ports SplitResult.hostname: lowercased, brackets already unwrapped,
// "" where Python returns None. The lowercasing is not cosmetic — a pool line
// carrying "HOST.EX" masks to "host.ex" in every log line app.py writes.
//
// Only the part BEFORE the first "%" is lowercased: CPython keeps an IPv6 zone
// id ("[fe80::1%tESt]") in its original case, and that carve-out also catches
// the percent-escapes 711-style hostnames sometimes carry, so
// "hzone%BR.ex" stays "hzone%BR.ex".
func (s splitResult) hostname() string {
	host, _, _ := s.hostinfo()
	if host == "" {
		return ""
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		return pyLower(host[:i]) + host[i:]
	}
	return pyLower(host)
}

// port ports SplitResult.port, which is an int and therefore normalizes "0080"
// to 80, and which RAISES for a non-ASCII-digit port or one outside 0..65535.
// Callers that mirror a Python `try:` must treat err as "take the except path".
func (s splitResult) port() (value int, hasPort bool, err error) {
	_, text, has := s.hostinfo()
	if !has {
		return 0, false, nil
	}
	// Python: port.isdigit() and port.isascii(). str.isdigit() is wider than
	// [0-9] but isascii() narrows it back, so this really is ASCII digits only —
	// "8080²" and "٣٠١٠" both raise.
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, true, errors.New("Port could not be cast to integer value as " + text)
		}
	}
	value, convErr := strconv.Atoi(text)
	if convErr != nil {
		// Only reachable for a digit run too long for an int; Python's int() is
		// unbounded and would fail the range check below instead. Same outcome.
		return 0, true, errors.New("Port out of range 0-65535")
	}
	if value < 0 || value > 65535 {
		return 0, true, errors.New("Port out of range 0-65535")
	}
	return value, true, nil
}

// userinfo ports SplitResult.username/.password. The bools are Python's None:
// "@h" gives username "" with no password, which is FALSY on both counts and
// is why mask_proxy_url leaves "http://@h:1" completely alone.
func (s splitResult) userinfo() (username string, hasUser bool, password string, hasPass bool) {
	i := strings.LastIndexByte(s.Netloc, '@')
	if i < 0 {
		return "", false, "", false
	}
	raw := s.Netloc[:i]
	hasUser = true
	if j := strings.IndexByte(raw, ':'); j >= 0 {
		return raw[:j], true, raw[j+1:], true
	}
	return raw, true, "", false
}

// nfkcNetlocUnsafe are the code points whose NFKC expansion contains one of
// "/?#@:", i.e. exactly the ones _checknetloc rejects. Enumerated against this
// repo's CPython (Unicode 15.0.0) rather than pulling in x/text/unicode/norm:
// NFKC composition never PRODUCES an ASCII delimiter, so a netloc can only grow
// one by containing a character from this list.
var nfkcNetlocUnsafe = map[rune]bool{
	0x2047: true, 0x2048: true, 0x2049: true, 0x2100: true, 0x2101: true,
	0x2105: true, 0x2106: true, 0x2a74: true, 0xfe13: true, 0xfe16: true,
	0xfe55: true, 0xfe56: true, 0xfe5f: true, 0xfe6b: true, 0xff03: true,
	0xff0f: true, 0xff1a: true, 0xff1f: true, 0xff20: true,
}

var ipvFutureRe = regexp.MustCompile(`^v[a-fA-F0-9]+\.[^\n]+$`)

// netlocValueError reports the ValueError CPython's urlsplit raises for a
// netloc, or nil. Split out from urlsplit itself so that _join_proxy_url keeps
// app.py's never-catching (and therefore never-recovering) behaviour while
// mask_proxy_url, which wraps urlsplit in try/except, can take the except path.
func netlocValueError(netloc string) error {
	open := strings.Contains(netloc, "[")
	closeBr := strings.Contains(netloc, "]")
	if open != closeBr {
		return errors.New("Invalid IPv6 URL")
	}
	if open && closeBr {
		// netloc.partition('[')[2].partition(']')[0]
		host := netloc[strings.Index(netloc, "[")+1:]
		if j := strings.Index(host, "]"); j >= 0 {
			host = host[:j]
		}
		if err := checkBracketedHost(host); err != nil {
			return err
		}
	}
	if netloc == "" || isASCII(netloc) {
		return nil
	}
	for _, r := range netloc {
		if r == '@' || r == ':' || r == '#' || r == '?' {
			continue // _checknetloc deletes these before normalizing
		}
		if nfkcNetlocUnsafe[r] {
			return errors.New("netloc '" + netloc + "' contains invalid characters under NFKC normalization")
		}
	}
	return nil
}

// checkBracketedHost ports urllib.parse._check_bracketed_host.
//
// DIVERGENCE: Python validates with ipaddress.ip_address; this uses
// netip.ParseAddr. They agree on every form a proxy endpoint can plausibly
// carry (plain IPv6, IPv4-mapped, zone ids) and both reject leading zeros in an
// embedded IPv4. Where they could still disagree the only consequence is which
// of two masking strategies runs on a hand-typed bracketed host.
func checkBracketedHost(host string) error {
	if strings.HasPrefix(host, "v") {
		if !ipvFutureRe.MatchString(host) {
			return errors.New("IPvFuture address is invalid")
		}
		return nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("'" + host + "' does not appear to be an IPv4 or IPv6 address")
	}
	if addr.Is4() {
		return errors.New("An IPv4 address cannot be in brackets")
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// maskFallbackRe is re.sub(r"(?<=://)[^/@\s]+@", "***@", text). RE2 has no
// lookbehind, so the "://" is consumed and re-emitted. That is exact, not an
// approximation: [^/@\s]+ cannot contain "/", so no Python match can start
// inside the three characters a Go match additionally consumes.
var maskFallbackRe = regexp.MustCompile(`://[^/@` + pySpaceChars + `]+@`)

// MaskProxyURL ports mask_proxy_url (app.py:2564-2576): keep credentials out of
// log lines, "直连" for the empty string.
//
// The urlsplit branch is not a cosmetic detail — it is where Python lowercases
// the host, renders the port as an int ("0080" -> "80") and DROPS the IPv6
// brackets, and where a port it cannot parse falls back to the regex form that
// preserves the text verbatim. app.py:6027 masks raw exception strings with
// this same function, which is why the fallback has to replace every "://…@",
// not just the first.
func MaskProxyURL(proxyURL string) string {
	text := pyStrip(proxyURL)
	if text == "" {
		return "直连"
	}
	if masked, ok := maskViaSplit(text); ok {
		return masked
	}
	return maskFallbackRe.ReplaceAllString(text, "://***@")
}

// maskViaSplit is the body of app.py's try block; ok=false means "Python either
// raised or fell out of the if", both of which land on the regex fallback.
func maskViaSplit(text string) (string, bool) {
	parsed := urlsplit(text)
	if netlocValueError(parsed.Netloc) != nil {
		return "", false
	}
	username, _, password, _ := parsed.userinfo()
	// `if parsed.username or parsed.password` — None and "" are both falsy, so
	// "http://@h:1" and "http://:@h:1" skip this branch entirely.
	if username == "" && password == "" {
		return "", false
	}
	host := parsed.hostname()
	portValue, hasPort, err := parsed.port()
	if err != nil {
		return "", false
	}
	portText := ""
	// `f":{parsed.port}" if parsed.port else ""` — port 0 is falsy in Python and
	// disappears from the masked URL.
	if hasPort && portValue != 0 {
		portText = ":" + strconv.Itoa(portValue)
	}
	return urlunsplit(parsed.Scheme, "***@"+host+portText, parsed.Path, parsed.Query, parsed.Fragment), true
}
