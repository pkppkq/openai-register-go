package authproto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ---------------------------------------------------------------------------
// Pure helpers (app.py:5495-5628)
// ---------------------------------------------------------------------------

// TokenDebugFingerprint mirrors token_debug_fingerprint (app.py:5495-5497): the
// first 12 hex characters of sha256(token), or "-" for an empty token.
func TokenDebugFingerprint(token string) string {
	value := token
	if value == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// NormalizeAuthContinueURL mirrors normalize_auth_continue_url
// (app.py:5500-5504): strip, pass through anything that literally starts with
// "http", otherwise resolve against AUTH_BASE_URL.
//
// The "http" test is a CASE-SENSITIVE prefix check, so "hTTps://A/B" does NOT
// pass through — it goes to urljoin, which lowercases the scheme but leaves the
// host alone, yielding "https://A/B". That is copied, not corrected.
func NormalizeAuthContinueURL(value string) string {
	text := pyStrip(value)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "http") {
		return text
	}
	return urlJoin(openai.AuthBaseURL, text)
}

// ---------------------------------------------------------------------------
// urllib.parse
//
// net/url is NOT urllib.parse, and the difference is not cosmetic here:
// url.Parse REJECTS a path containing a stray "%" (so a continue_url of
// "/verify?x=100%" came back as the RELATIVE string instead of an absolute
// URL, and the next request went nowhere), and url.URL.String() RE-ENCODES
// the path, turning "/a b" into "/a%20b" and "/<zh>" into "/%E9%82%AE".
// CPython's urljoin does neither: it is pure string surgery over the split
// components and never re-encodes anything. The split/join pair below is
// CPython's, transcribed.
// ---------------------------------------------------------------------------

// urlSplitParts is urllib.parse.ParseResult.
type urlSplitParts struct {
	scheme, netloc, path, params, query, fragment string
}

// whatwgC0OrSpace is urllib.parse._WHATWG_C0_CONTROL_OR_SPACE: U+0000-U+0020.
const whatwgC0OrSpace = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\v\f\r" +
	"\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "

// usesRelative / usesNetloc / usesParams are urllib.parse's scheme lists,
// verbatim. urljoin consults all three.
var (
	usesRelative = map[string]bool{
		"": true, "ftp": true, "http": true, "gopher": true, "nntp": true,
		"imap": true, "wais": true, "file": true, "https": true, "shttp": true,
		"mms": true, "prospero": true, "rtsp": true, "rtsps": true, "rtspu": true,
		"sftp": true, "svn": true, "svn+ssh": true, "ws": true, "wss": true,
	}
	usesNetloc = map[string]bool{
		"": true, "ftp": true, "http": true, "gopher": true, "nntp": true,
		"telnet": true, "imap": true, "wais": true, "file": true, "mms": true,
		"https": true, "shttp": true, "snews": true, "prospero": true, "rtsp": true,
		"rtsps": true, "rtspu": true, "rsync": true, "svn": true, "svn+ssh": true,
		"sftp": true, "nfs": true, "git": true, "git+ssh": true, "ws": true,
		"wss": true, "itms-services": true,
	}
	usesParams = map[string]bool{
		"": true, "ftp": true, "hdl": true, "prospero": true, "http": true,
		"imap": true, "https": true, "shttp": true, "rtsp": true, "rtsps": true,
		"rtspu": true, "sip": true, "sips": true, "mms": true, "sftp": true,
		"tel": true,
	}
)

func isSchemeChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '+', c == '-', c == '.':
		return true
	}
	return false
}

// pyURLSplit is urllib.parse.urlsplit(url, scheme). Note the asymmetry CPython
// documents: the URL is only LEFT-stripped of C0-and-space, while tab, CR and
// LF are removed from ANYWHERE in it.
func pyURLSplit(rawURL, defaultScheme string) urlSplitParts {
	u := strings.TrimLeft(rawURL, whatwgC0OrSpace)
	scheme := strings.Trim(defaultScheme, whatwgC0OrSpace)
	for _, b := range []string{"\t", "\r", "\n"} {
		u = strings.ReplaceAll(u, b, "")
		scheme = strings.ReplaceAll(scheme, b, "")
	}
	var netloc, query, fragment string
	// `if i > 0 and url[0].isascii() and url[0].isalpha()`. A leading multi-byte
	// rune has a first BYTE >= 0x80, which is neither ASCII nor a letter, so the
	// byte test and CPython's code-point test agree.
	if i := strings.Index(u, ":"); i > 0 &&
		((u[0] >= 'a' && u[0] <= 'z') || (u[0] >= 'A' && u[0] <= 'Z')) {
		ok := true
		for j := 0; j < i; j++ {
			if !isSchemeChar(u[j]) {
				ok = false
				break
			}
		}
		if ok {
			// The scheme is ASCII by construction, so ToLower is CPython's lower.
			scheme, u = strings.ToLower(u[:i]), u[i+1:]
		}
	}
	if strings.HasPrefix(u, "//") {
		netloc, u = splitNetloc(u, 2)
		// DIVERGENCE: CPython raises ValueError for an unbalanced "[" or a netloc
		// whose NFKC normalization introduces one of "/?#@:". Nothing upstream
		// catches that exception, so a Python crash becomes a plain join here.
	}
	if i := strings.Index(u, "#"); i >= 0 {
		u, fragment = u[:i], u[i+1:]
	}
	if i := strings.Index(u, "?"); i >= 0 {
		u, query = u[:i], u[i+1:]
	}
	return urlSplitParts{scheme: scheme, netloc: netloc, path: u, query: query, fragment: fragment}
}

// splitNetloc is urllib.parse._splitnetloc.
func splitNetloc(u string, start int) (netloc, rest string) {
	delim := len(u)
	for _, c := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(u[start:], c); i >= 0 && start+i < delim {
			delim = start + i
		}
	}
	return u[start:delim], u[delim:]
}

// pyURLParse is urllib.parse.urlparse: urlsplit plus the ";params" tail, which
// is only split off for the schemes in uses_params (https is one of them).
func pyURLParse(rawURL, defaultScheme string) urlSplitParts {
	p := pyURLSplit(rawURL, defaultScheme)
	if usesParams[p.scheme] && strings.Contains(p.path, ";") {
		p.path, p.params = splitParams(p.path)
	}
	return p
}

// splitParams is urllib.parse._splitparams: only a ";" in the LAST path segment
// counts, and a path with a "/" but no later ";" keeps its params empty.
func splitParams(path string) (string, string) {
	var i int
	if strings.Contains(path, "/") {
		last := strings.LastIndex(path, "/")
		rel := strings.Index(path[last:], ";")
		if rel < 0 {
			return path, ""
		}
		i = last + rel
	} else {
		i = strings.Index(path, ";")
	}
	return path[:i], path[i+1:]
}

// pyURLUnparse is urllib.parse.urlunparse composed with urlunsplit.
func pyURLUnparse(p urlSplitParts) string {
	u := p.path
	if p.params != "" {
		u = u + ";" + p.params
	}
	if p.netloc != "" || (p.scheme != "" && usesNetloc[p.scheme] && !strings.HasPrefix(u, "//")) {
		if u != "" && !strings.HasPrefix(u, "/") {
			u = "/" + u
		}
		u = "//" + p.netloc + u
	}
	if p.scheme != "" {
		u = p.scheme + ":" + u
	}
	if p.query != "" {
		u = u + "?" + p.query
	}
	if p.fragment != "" {
		u = u + "#" + p.fragment
	}
	return u
}

// urlJoin mirrors urllib.parse.urljoin(base, ref), transcribed from CPython
// 3.12. See the block comment above for why net/url cannot stand in.
func urlJoin(base, ref string) string {
	if base == "" {
		return ref
	}
	if ref == "" {
		return base
	}
	b := pyURLParse(base, "")
	r := pyURLParse(ref, b.scheme)
	// A ref whose scheme differs from the base's, or whose scheme is not
	// relative-capable ("mailto:", "javascript:"), is returned UNTOUCHED — not
	// re-serialized, so "HTTP://X" keeps its uppercase scheme.
	if r.scheme != b.scheme || !usesRelative[r.scheme] {
		return ref
	}
	if usesNetloc[r.scheme] {
		if r.netloc != "" {
			return pyURLUnparse(r)
		}
		r.netloc = b.netloc
	}
	if r.path == "" && r.params == "" {
		r.path, r.params = b.path, b.params
		if r.query == "" {
			r.query = b.query
		}
		return pyURLUnparse(r)
	}

	baseParts := strings.Split(b.path, "/")
	if baseParts[len(baseParts)-1] != "" {
		// The last element is a file, not a directory, so it drops out.
		baseParts = baseParts[:len(baseParts)-1]
	}
	var segments []string
	if strings.HasPrefix(r.path, "/") {
		segments = strings.Split(r.path, "/")
	} else {
		segments = append(append([]string{}, baseParts...), strings.Split(r.path, "/")...)
		// `segments[1:-1] = filter(None, segments[1:-1])` — drop the empty
		// interior segments that would otherwise double up the slashes.
		if len(segments) > 2 {
			kept := make([]string, 0, len(segments))
			kept = append(kept, segments[0])
			for _, s := range segments[1 : len(segments)-1] {
				if s != "" {
					kept = append(kept, s)
				}
			}
			segments = append(kept, segments[len(segments)-1])
		}
	}

	resolved := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch seg {
		case "..":
			// `try: resolved_path.pop() except IndexError: pass`
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
		case ".":
			continue
		default:
			resolved = append(resolved, seg)
		}
	}
	if last := segments[len(segments)-1]; last == "." || last == ".." {
		resolved = append(resolved, "")
	}
	r.path = strings.Join(resolved, "/")
	if r.path == "" {
		r.path = "/"
	}
	return pyURLUnparse(r)
}

// pyStrip mirrors str.strip(): Python strips every character whose
// str.isspace() is true, which includes NBSP, U+2028/9, U+3000 and the C0
// information separators U+001C-U+001F that Go's unicode.IsSpace omits.
func pyStrip(s string) string {
	return strings.TrimFunc(s, pyIsSpace)
}

func pyIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0:
		return true
	}
	switch {
	case r >= 0x2000 && r <= 0x200a:
		return true
	case r == 0x1680 || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000:
		return true
	}
	return false
}

// transientHTTPErrorMarkers mirrors is_transient_http_error's marker tuple
// (app.py:5606-5627), verbatim and in order.
var transientHTTPErrorMarkers = []string{
	"empty reply from server",
	"curl: (52)",
	"curl: (56)",
	"curl: (28)",
	"curl: (35)",
	"curl: (18)",
	"connection aborted",
	"connection reset",
	"connectionreseterror",
	"10054",
	"远程主机强迫关闭",
	"forcibly closed",
	"timed out",
	"timeout",
	"broken pipe",
	"remote end closed",
	"ssl",
	"tls",
	"eof occurred",
	"failed to perform",
}

// IsTransientHTTPError mirrors is_transient_http_error (app.py:5604-5628) and
// the static _is_transient_http_error that just forwards to it
// (app.py:8588-8590).
//
// The fold is pyCasefold, not strings.ToLower: casefold EXPANDS a handful of
// code points into ASCII that simple lowercasing does not, and at least one of
// those expansions reaches a marker — casefold("ẞL") is "ssl", which
// matches, while strings.ToLower gives "ßl", which does not.
func IsTransientHTTPError(text string) bool {
	lowered := pyCasefold(text)
	for _, marker := range transientHTTPErrorMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// isTransientErr is `is_transient_http_error(exc)`: Python stringifies the
// exception first, and str(None-ish) is "" via the `exc or ""` guard.
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	return IsTransientHTTPError(err.Error())
}

// authChallengeMarkers is the marker tuple of _response_has_auth_challenge
// (app.py:8661-8667).
var authChallengeMarkers = []string{
	"turnstile",
	"challenges.cloudflare.com",
	"__cf_chl",
	"just a moment",
	"cf-challenge",
}

// responseHasAuthChallenge mirrors _response_has_auth_challenge
// (app.py:8657-8667): lowercase "url\nbody[:2000]" and look for a Cloudflare /
// Turnstile marker.
//
// The 2000 is a CHARACTER slice of response.text, applied BEFORE the join.
//
// pyLower rather than strings.ToLower: Go's simple mapping turns U+0130 into a
// bare ASCII "i" where CPython produces "i" + U+0307, which would let a body
// containing "TİMED"-style text match a marker CPython could not reach.
func responseHasAuthChallenge(resp *Response) bool {
	lowered := pyLower(resp.URL + "\n" + truncRunes(resp.Text(), 2000))
	for _, marker := range authChallengeMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Query building
// ---------------------------------------------------------------------------

// queryPairs is an ordered urlencode() input. url.Values.Encode() SORTS keys;
// Python's urlencode walks the dict in insertion order, and the authorize URL's
// parameter order is part of what the endpoint sees.
type queryPairs []headerPair

// Encode mirrors urllib.parse.urlencode: quote_plus per component (space -> +,
// everything outside [A-Za-z0-9_.\-~] percent-encoded), joined with "&".
// Go's url.QueryEscape has exactly that unreserved set and the same + rule.
func (q queryPairs) Encode() string {
	var b strings.Builder
	for i, p := range q {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.Key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.Value))
	}
	return b.String()
}

// parseQS mirrors urllib.parse.parse_qs(query) with its DEFAULT
// keep_blank_values=False: "?code=&state=x" yields no "code" key at all, which
// is what makes _extract_auth_result raise "callback 中缺少 code" rather than
// silently continuing with an empty code.
func parseQS(rawQuery string) map[string][]string {
	out := map[string][]string{}
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		key, value := part, ""
		if i := strings.Index(part, "="); i >= 0 {
			key, value = part[:i], part[i+1:]
		} else {
			// parse_qsl drops a bare "flag" component entirely unless
			// keep_blank_values is set, which parse_qs's default is not.
			continue
		}
		if value == "" {
			continue
		}
		out[unquotePlus(key)] = append(out[unquotePlus(key)], unquotePlus(value))
	}
	return out
}

// unquotePlus is urllib.parse.unquote_plus: "+" becomes a space FIRST, then the
// percent escapes are decoded.
//
// It replaces a url.QueryUnescape/fallback pair that got "%zz%41" wrong: Go's
// decoder fails the WHOLE string on one bad escape, so the fallback returned
// "%zz%41" where CPython returns "%zzA". _extract_auth_result reads the OAuth
// `code` and `state` through this, so a callback mixing a valid and an invalid
// escape produced a corrupt code and a doomed token exchange.
func unquotePlus(s string) string {
	return pyUnquote(strings.ReplaceAll(s, "+", " "))
}

// pyUnquote is urllib.parse.unquote(s, encoding="utf-8", errors="replace") —
// the exact defaults parse_qsl uses.
//
// Two CPython behaviours that no net/url helper has:
//
//   - Decoding is per-escape and LENIENT. "%zz" stays literal while a valid
//     escape beside it still decodes.
//   - Escapes are only decoded inside maximal ASCII RUNS (CPython splits on
//     _asciire first), so "%E4<zh>%B8" can never assemble bytes across the
//     non-ASCII character in the middle.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		start := i
		for i < len(s) && s[i] < 0x80 {
			i++
		}
		if i > start {
			b.WriteString(utf8Replace(unquoteToBytes(s[start:i])))
		}
		start = i
		for i < len(s) && s[i] >= 0x80 {
			i++
		}
		b.WriteString(s[start:i])
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
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

// unquoteToBytes is urllib.parse.unquote_to_bytes: a "%" not followed by two
// hex digits is emitted literally, exactly as CPython's _hextobyte KeyError arm
// does.
func unquoteToBytes(run string) []byte {
	out := make([]byte, 0, len(run))
	for i := 0; i < len(run); {
		if run[i] != '%' {
			out = append(out, run[i])
			i++
			continue
		}
		if i+2 < len(run) {
			hi, okHi := hexVal(run[i+1])
			lo, okLo := hexVal(run[i+2])
			if okHi && okLo {
				out = append(out, hi<<4|lo)
				i += 3
				continue
			}
		}
		out = append(out, '%')
		i++
	}
	return out
}

// utf8Replace is bytes.decode("utf-8", errors="replace"). Go has no stdlib
// equivalent: a plain []byte->string keeps the bad bytes, iterating runes emits
// ONE U+FFFD PER BYTE, and strings.ToValidUTF8 emits one per RUN. CPython (like
// the Unicode recommendation) emits one per MAXIMAL SUBPART, so b"\xe4\xb8" is
// a single U+FFFD while b"\xff\xfe" is two.
func utf8Replace(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); {
		c := raw[i]
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		var need int
		var lo, hi byte = 0x80, 0xBF
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c == 0xE0:
			need, lo = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c >= 0xEE && c <= 0xEF:
			need = 2
		case c == 0xED:
			need, hi = 2, 0x9F
		case c == 0xF0:
			need, lo = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			need = 3
		case c == 0xF4:
			need, hi = 3, 0x8F
		default:
			// C0, C1, F5..FF: never a legal lead byte.
			b.WriteRune(0xFFFD)
			i++
			continue
		}
		valid := 0
		for k := 1; k <= need; k++ {
			if i+k >= len(raw) {
				break
			}
			c2 := raw[i+k]
			bLo, bHi := lo, hi
			if k > 1 {
				bLo, bHi = 0x80, 0xBF
			}
			if c2 < bLo || c2 > bHi {
				break
			}
			valid++
		}
		if valid < need {
			// One U+FFFD for the maximal valid subpart (at least the lead byte).
			b.WriteRune(0xFFFD)
			i += 1 + valid
			continue
		}
		b.WriteString(string(raw[i : i+need+1]))
		i += need + 1
	}
	return b.String()
}

// firstOrEmpty is `(query.get(k) or [""])[0]`.
func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// ---------------------------------------------------------------------------
// Error formatting
// ---------------------------------------------------------------------------

// formatErrorResponse mirrors _format_error_response (app.py:8039-8051).
//
// The `if code:` test is Python truthiness on the RAW json value, so a code of
// 0, false or "" falls through to the body form. `error` may be a plain string
// rather than a dict, in which case Python assigns the string itself to `code`.
func formatErrorResponse(resp *Response) string {
	body := resp.Text()
	if payload, err := openai.DecodeOrderedJSON(resp.Body); err == nil {
		var code any
		if obj, isObj := asObject(payload); isObj {
			errValue := obj.Get("error")
			if errObj, isErrObj := asObject(errValue); isErrObj {
				code = errObj.Get("code")
			} else {
				code = errValue
			}
		}
		if pyTruthy(code) {
			reason := models.OpenAIPhoneErrorReason(pyStr(code))
			suffix := ""
			if reason != "" {
				suffix = " (" + reason + ")"
			}
			return fmt.Sprintf("%d code=%s%s", resp.StatusCode, pyStr(code), suffix)
		}
	}
	return fmt.Sprintf("%d body=%s", resp.StatusCode, truncRunes(body, 500))
}
