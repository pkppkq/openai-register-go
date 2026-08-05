package proxyroute

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// ---------------------------------------------------------------------------
// 撞链代理地区 (link_proxy_region)
// ---------------------------------------------------------------------------

// DefaultPaymentMode is the PAYMENT_MODES fallback key used by
// _payment_mode_country (app.py:16757-16761): an unknown payment mode name
// resolves to 无卡长链接 US/USD, i.e. country US — NOT to "no region".
const DefaultPaymentMode = "无卡长链接 US/USD"

// PaymentModeCountry mirrors _payment_mode_country (app.py:16756-16761).
func PaymentModeCountry(paymentMode string) string {
	mode, ok := models.PaymentModes[paymentMode]
	if !ok {
		mode = models.PaymentModes[DefaultPaymentMode]
	}
	return pyUpper(pyStrip(mode.Country))
}

// LinkRegionSelectionToCode mirrors link_proxy_region_selection_to_code
// (app.py:2527-2534).
//
//	""/不限                 -> ""      (no region lock, every pool entry passes)
//	自动(跟随支付地区)      -> payment mode country
//	"JP 日本"               -> "JP"
func LinkRegionSelectionToCode(value, paymentCountry string) string {
	text := pyStrip(value)
	if text == "" || text == settings.LinkProxyRegionAny {
		return ""
	}
	if text == settings.LinkProxyRegionAuto {
		return pyUpper(pyStrip(paymentCountry))
	}
	// pyUpper, not strings.ToUpper: str.upper() is the FULL mapping, so "ß x"
	// becomes "SS X" and yields the region code "SS".
	return firstBoundedTwoUpper(pyUpper(text))
}

// RegionCode is _link_proxy_region_code (app.py:16774-16778) over persisted
// settings. link_proxy_region is validated against LINK_PROXY_REGION_OPTIONS at
// load time (app.py:14113-14115); settings.FromSnapshot already did that, so an
// unrecognised saved value has collapsed to 不限 == no region lock.
func RegionCode(cfg settings.Settings) string {
	return LinkRegionSelectionToCode(cfg.LinkProxyRegion, PaymentModeCountry(cfg.PaymentMode))
}

// regionLabel is _link_proxy_region_label (app.py:16780-16784): "JP 日本".
// LINK_PROXY_REGION_NAMES is not exported from internal/settings, but
// LinkProxyRegionOptions is built out of it as "CODE NAME" (app.py:516-520), so
// the label is recoverable without duplicating the table.
func regionLabel(code string) string {
	if code == "" {
		return ""
	}
	for _, option := range settings.LinkProxyRegionOptions {
		if strings.HasPrefix(option, code+" ") {
			return option
		}
	}
	// Python: f"{code} {name}".strip() with name="" -> just the code.
	return code
}

// ---------------------------------------------------------------------------
// region code extraction / rewriting
// ---------------------------------------------------------------------------

// regionReadRe is the read pattern of proxy_region_code_from_text
// (app.py:2500-2503). Two deliberate transformations:
//
//   - the trailing (?=$|[^a-z0-9]) lookahead becomes a consuming group, because
//     RE2 has no lookahead. Only the FIRST match is ever used and Go's regexp
//     reproduces backtracking (leftmost-first) semantics, so the captured group
//     is identical; consuming one extra char only affects later matches.
//   - Python's `$` also matches just before a trailing "\n". Here the
//     [^a-z0-9] branch matches that same "\n", so the two agree anyway.
//
// The alternation order region|country|cc|geo|loc|location|zone is kept as
// written: "loc" precedes "location" and is tried first, then backtracked out
// of when the two letters after it are followed by another word char.
var regionReadRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:region|country|cc|geo|loc|location|zone)[-_=:%]*([a-z]{2})(?:$|[^a-z0-9])`)

// regionWriteRe is the rewrite pattern (app.py:2516-2518). Note it has NO
// leading boundary group, unlike regionReadRe — that asymmetry is in Python and
// is preserved.
var regionWriteRe = regexp.MustCompile(`(?i)((?:region|country|cc|geo|loc|location|zone)[-_=:%]*)([a-z]{2})($|[^a-z0-9])`)

var twoUpperRe = regexp.MustCompile(`^[A-Z]{2}$`)

// RegionCodeFromText mirrors proxy_region_code_from_text (app.py:2470-2504).
//
// DIVERGENCE: Python first runs _clean_proxy_input and, when the text looks
// like a curl command, prepends _proxy_url_from_curl_command(text) as the
// highest-priority candidate. Both helpers are unexported inside
// internal/proxypool. Neither is reachable from this package: every string that
// reaches here has already been through proxypool.NormalizeProxyURL /
// ParseProxyPoolText, which apply exactly those two steps, so the input is
// already a cleaned proxy URL and the curl candidate would equal it.
func RegionCodeFromText(value string) string {
	text := pyStrip(value)
	if text == "" {
		return ""
	}
	// app.py:2479-2495: the whole (unquoted) string first, then the individual
	// urlsplit components, each unquoted.
	candidates := make([]string, 0, 7)
	candidates = append(candidates, pyUnquote(text))
	for _, part := range urlSearchParts(text) {
		candidates = append(candidates, pyUnquote(part))
	}
	for _, candidate := range candidates {
		if m := regionReadRe.FindStringSubmatch(candidate); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// RewriteProxyRegionCode mirrors rewrite_proxy_region_code (app.py:2508-2524):
// swap the first region marker to the target region, but only keep the result
// if reading it back yields that region.
func RewriteProxyRegionCode(value, targetRegion string) string {
	proxy := proxypool.NormalizeProxyURL(value)
	region := pyUpper(pyStrip(targetRegion))
	if proxy == "" || !twoUpperRe.MatchString(region) {
		return proxy
	}
	if RegionCodeFromText(proxy) == region {
		return proxy
	}
	// re.subn(..., count=1): first match only.
	m := regionWriteRe.FindStringSubmatchIndex(proxy)
	if m == nil {
		return proxy
	}
	// m[2]:m[3] is group 1 (the keyword + separators), m[4]:m[5] is the old
	// two-letter code. Everything from m[5] on — including the boundary char
	// that the lookahead would only have peeked at — is preserved verbatim.
	rewritten := proxy[:m[3]] + region + proxy[m[5]:]
	if RegionCodeFromText(rewritten) == region {
		return rewritten
	}
	return proxy
}

// ---------------------------------------------------------------------------
// small Python-string helpers
// ---------------------------------------------------------------------------

// isPySpace matches Python's str.isspace(): unicode.IsSpace plus the C0
// separators U+001C..U+001F. (proxypool has the same helper, unexported.)
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// isPyWord matches Python re's \w for str patterns (Unicode-aware). Go's \b is
// ASCII-only, so \b searches are spelled out by hand below.
func isPyWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// pyMultiUpper holds every code point whose FULL uppercase (Python str.upper())
// is longer than one rune. strings.ToUpper applies the simple mapping and
// leaves all 102 of them alone, so without this table "ß x".upper() stays "ß x"
// where Python gets "SS X" and link_proxy_region_selection_to_code
// (app.py:2533) then extracts "SS". Generated by enumerating
// len(chr(c).upper()) > 1 against this repo's CPython (Unicode 15.0.0).
var pyMultiUpper = map[rune]string{
	0x00DF: "SS", 0x0149: "ʼN", 0x01F0: "J̌", 0x0390: "Ϊ́",
	0x03B0: "Ϋ́", 0x0587: "ԵՒ", 0x1E96: "H̱", 0x1E97: "T̈",
	0x1E98: "W̊", 0x1E99: "Y̊", 0x1E9A: "Aʾ", 0x1F50: "Υ̓",
	0x1F52: "Υ̓̀", 0x1F54: "Υ̓́", 0x1F56: "Υ̓͂", 0x1F80: "ἈΙ",
	0x1F81: "ἉΙ", 0x1F82: "ἊΙ", 0x1F83: "ἋΙ", 0x1F84: "ἌΙ",
	0x1F85: "ἍΙ", 0x1F86: "ἎΙ", 0x1F87: "ἏΙ", 0x1F88: "ἈΙ",
	0x1F89: "ἉΙ", 0x1F8A: "ἊΙ", 0x1F8B: "ἋΙ", 0x1F8C: "ἌΙ",
	0x1F8D: "ἍΙ", 0x1F8E: "ἎΙ", 0x1F8F: "ἏΙ", 0x1F90: "ἨΙ",
	0x1F91: "ἩΙ", 0x1F92: "ἪΙ", 0x1F93: "ἫΙ", 0x1F94: "ἬΙ",
	0x1F95: "ἭΙ", 0x1F96: "ἮΙ", 0x1F97: "ἯΙ", 0x1F98: "ἨΙ",
	0x1F99: "ἩΙ", 0x1F9A: "ἪΙ", 0x1F9B: "ἫΙ", 0x1F9C: "ἬΙ",
	0x1F9D: "ἭΙ", 0x1F9E: "ἮΙ", 0x1F9F: "ἯΙ", 0x1FA0: "ὨΙ",
	0x1FA1: "ὩΙ", 0x1FA2: "ὪΙ", 0x1FA3: "ὫΙ", 0x1FA4: "ὬΙ",
	0x1FA5: "ὭΙ", 0x1FA6: "ὮΙ", 0x1FA7: "ὯΙ", 0x1FA8: "ὨΙ",
	0x1FA9: "ὩΙ", 0x1FAA: "ὪΙ", 0x1FAB: "ὫΙ", 0x1FAC: "ὬΙ",
	0x1FAD: "ὭΙ", 0x1FAE: "ὮΙ", 0x1FAF: "ὯΙ", 0x1FB2: "ᾺΙ",
	0x1FB3: "ΑΙ", 0x1FB4: "ΆΙ", 0x1FB6: "Α͂", 0x1FB7: "Α͂Ι",
	0x1FBC: "ΑΙ", 0x1FC2: "ῊΙ", 0x1FC3: "ΗΙ", 0x1FC4: "ΉΙ",
	0x1FC6: "Η͂", 0x1FC7: "Η͂Ι", 0x1FCC: "ΗΙ", 0x1FD2: "Ϊ̀",
	0x1FD3: "Ϊ́", 0x1FD6: "Ι͂", 0x1FD7: "Ϊ͂", 0x1FE2: "Ϋ̀",
	0x1FE3: "Ϋ́", 0x1FE4: "Ρ̓", 0x1FE6: "Υ͂", 0x1FE7: "Ϋ͂",
	0x1FF2: "ῺΙ", 0x1FF3: "ΩΙ", 0x1FF4: "ΏΙ", 0x1FF6: "Ω͂",
	0x1FF7: "Ω͂Ι", 0x1FFC: "ΩΙ", 0xFB00: "FF", 0xFB01: "FI",
	0xFB02: "FL", 0xFB03: "FFI", 0xFB04: "FFL", 0xFB05: "ST",
	0xFB06: "ST", 0xFB13: "ՄՆ", 0xFB14: "ՄԵ", 0xFB15: "ՄԻ",
	0xFB16: "ՎՆ", 0xFB17: "ՄԽ",
}

// pyUpper is str.upper().
func pyUpper(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { _, ok := pyMultiUpper[r]; return ok }) {
		return strings.ToUpper(s)
	}
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := pyMultiUpper[r]; ok {
			b.WriteString(mapped)
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// pyLower is str.lower(). U+0130 is the only code point whose full lowercase is
// longer than one rune; strings.ToLower drops its combining dot.
func pyLower(s string) string {
	if strings.ContainsRune(s, 'İ') {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

// firstBoundedTwoUpper is re.search(r"\b([A-Z]{2})\b", text) with Python's
// Unicode-aware \b. "JP 日本" -> "JP"; "日本JP" -> "" (日 and J are both word
// characters, so there is no boundary between them), which Go's ASCII \b would
// have got wrong.
func firstBoundedTwoUpper(text string) string {
	runes := []rune(text)
	for i := 0; i+1 < len(runes); i++ {
		if !isASCIIUpper(runes[i]) || !isASCIIUpper(runes[i+1]) {
			continue
		}
		if i > 0 && isPyWord(runes[i-1]) {
			continue
		}
		if i+2 < len(runes) && isPyWord(runes[i+2]) {
			continue
		}
		return string(runes[i : i+2])
	}
	return ""
}

func isASCIIUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// pyUnquote is urllib.parse.unquote: decode %XX, leave malformed escapes alone.
//
// DIVERGENCE: Python decodes with errors="replace", turning undecodable bytes
// into U+FFFD; Go keeps the raw bytes. Both are non-[a-z0-9] as far as
// regionReadRe is concerned, so the extracted region code is unaffected.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, okHi := hexVal(s[i+1])
			lo, okLo := hexVal(s[i+2])
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

func hexVal(c byte) (int, bool) {
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

// urlSearchParts returns urlsplit's username, password, path, query, fragment
// and netloc, in that order, dropping the empty ones — the exact list
// proxy_region_code_from_text scans after the whole string (app.py:2481-2494).
//
// This is a reduced urlsplit: it does not decode anything (Python's
// SplitResult.username is the raw substring, which is why the caller unquotes
// it) and it only understands the "scheme://userinfo@host:port/path?q#f" shape
// that normalized proxy URLs have. net/url.Parse could not be used: it decodes
// userinfo, which would make the subsequent unquote a double-decode.
func urlSearchParts(raw string) []string {
	rest := raw
	if i := strings.Index(raw, ":"); i > 0 && isSchemeToken(raw[:i]) {
		rest = raw[i+1:]
	}
	var netloc, query, fragment string
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		if end := strings.IndexAny(rest, "/?#"); end >= 0 {
			netloc, rest = rest[:end], rest[end:]
		} else {
			netloc, rest = rest, ""
		}
	}
	if i := strings.Index(rest, "#"); i >= 0 {
		fragment, rest = rest[i+1:], rest[:i]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		query, rest = rest[i+1:], rest[:i]
	}
	path := rest

	var username, password string
	// urlsplit takes the LAST "@" as the userinfo separator.
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		userinfo := netloc[:i]
		if j := strings.Index(userinfo, ":"); j >= 0 {
			username, password = userinfo[:j], userinfo[j+1:]
		} else {
			username = userinfo
		}
	}

	parts := make([]string, 0, 6)
	for _, p := range []string{username, password, path, query, fragment, netloc} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// isSchemeToken matches urlsplit's scheme rule: letter, then letters/digits/+/-/.
func isSchemeToken(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !unicode.IsLetter(r) || r > unicode.MaxASCII {
				return false
			}
			continue
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// maskProxyURL is mask_proxy_url (app.py:2564-2576). It lives in
// internal/proxypool next to the urlsplit port it needs: the masked form is
// built from SplitResult.hostname (lowercased) and SplitResult.port (an int),
// not from the raw netloc text.
func maskProxyURL(proxyURL string) string { return proxypool.MaskProxyURL(proxyURL) }
