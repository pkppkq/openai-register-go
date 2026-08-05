package proxypool

import (
	"regexp"
	"strings"
)

var (
	// (?i)^(socks5h?|https?|http)://([^/@\s]+)$ — app.py:2341.
	reSchemeColonParts = regexp.MustCompile(`(?i)^(socks5h?|https?|http)://([^/@` + pySpaceChars + `]+)$`)

	// ^[\w+.-]+:// — app.py:2361. Python's \w is Unicode-aware; RE2's is ASCII,
	// so "代理://x" would slip past an \w-based port and be treated as host:port.
	reAnySchemePrefix = regexp.MustCompile(`^[\p{L}\p{N}_+.-]+://`)

	// (?i)^(socks5h?|https?|http):// and (?i)^socks5:// — app.py:2412/2413.
	reKnownSchemePrefix = regexp.MustCompile(`(?i)^(socks5h?|https?|http)://`)
	reSocks5Prefix      = regexp.MustCompile(`(?i)^socks5://`)

	// (?i)(?:socks5h?|https?|http)://[^\s,，;；]+ — app.py:2435.
	reURLInLine = regexp.MustCompile(`(?i)(?:socks5h?|https?|http)://[^` + pySpaceChars + `,，;；]+`)

	// (?i)^(?:socks5h?|https?|http):// — app.py:2453.
	reTokenSchemePrefix = regexp.MustCompile(`(?i)^(?:socks5h?|https?|http)://`)

	// \s+ — app.py:2448.
	rePySpaceRun = regexp.MustCompile(`[` + pySpaceChars + `]+`)
)

// proxyPoolTrimCutset is Python's strip(" \t,，;；") — ASCII space/tab plus the
// half- and full-width comma and semicolon. Note it does NOT include newlines.
const proxyPoolTrimCutset = " \t,，;；"

// curlFlagWords are the alternatives of
// \b(?:curl|--proxy-user|-U|--socks5|--socks5h|-x)\b at app.py:2443.
var curlFlagWords = []string{"curl", "--proxy-user", "-U", "--socks5", "--socks5h", "-x"}

// hasCurlFlagWord implements that re.search with Python's Unicode \b.
//
// The boundary rule has a consequence worth knowing before "fixing" it: every
// alternative except "curl" starts with '-', a non-word char, so \b before it
// only holds when the preceding character is a word character. " -x" and a
// leading "-x" therefore never match — only something like "abc-x" does. That
// is app.py's behaviour and the remainder heuristic depends on it.
func hasCurlFlagWord(s string) bool {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		for _, word := range curlFlagWords {
			w := []rune(word)
			if i+len(w) > len(runes) {
				continue
			}
			if !strings.EqualFold(string(runes[i:i+len(w)]), word) {
				continue
			}
			if !isWordBoundary(runes, i) || !isWordBoundary(runes, i+len(w)) {
				continue
			}
			return true
		}
	}
	return false
}

// isWordBoundary is Python re's \b at rune index i: the wordness of the
// characters either side of the position must differ (out of range = non-word).
func isWordBoundary(runes []rune, i int) bool {
	before := i > 0 && isPyWord(runes[i-1])
	after := i < len(runes) && isPyWord(runes[i])
	return before != after
}

// ParseProxyPoolText ports parse_proxy_pool_text (app.py:2421) with app.py's
// default_scheme="http".
func ParseProxyPoolText(value string) []string {
	return ParseProxyPoolTextWithScheme(value, "http")
}

// ParseProxyPoolTextWithScheme is parse_proxy_pool_text with an explicit
// default_scheme. The returned slice is never nil-safe-sensitive: callers treat
// len()==0 as "pool empty", matching Python's empty list.
func ParseProxyPoolTextWithScheme(value, defaultScheme string) []string {
	proxies := []string{}
	for _, rawLine := range pySplitlines(value) {
		line := cleanProxyInput(rawLine)
		if line == "" {
			continue
		}
		if hasCurlPrefix(line) {
			if proxy := NormalizeProxyURLWithScheme(line, defaultScheme); proxy != "" {
				proxies = append(proxies, proxy)
			}
			continue
		}

		var urlMatches []string
		for _, match := range reURLInLine.FindAllString(line, -1) {
			urlMatches = append(urlMatches, strings.Trim(match, proxyPoolTrimCutset))
		}
		if len(urlMatches) > 1 {
			for _, item := range urlMatches {
				if item == "" {
					continue
				}
				// Python appends the normalized value unconditionally here, so an
				// empty normalization really does enter the pool as "".
				proxies = append(proxies, NormalizeProxyURLWithScheme(item, defaultScheme))
			}
			continue
		}
		if len(urlMatches) == 1 {
			url := urlMatches[0]
			remainder := strings.Trim(strings.Replace(line, url, "", 1), proxyPoolTrimCutset)
			if remainder != "" && !hasCurlFlagWord(remainder) {
				proxies = append(proxies, NormalizeProxyURLWithScheme(url, defaultScheme))
				continue
			}
		}

		var tokenProxies []string
		for _, token := range rePySpaceRun.Split(line, -1) {
			item := strings.Trim(token, proxyPoolTrimCutset)
			if item == "" {
				continue
			}
			if reTokenSchemePrefix.MatchString(item) ||
				proxyURLFromColonParts(item, defaultScheme) != "" ||
				(strings.Contains(item, "@") && strings.Contains(item, ":")) {
				if proxy := NormalizeProxyURLWithScheme(item, defaultScheme); proxy != "" {
					tokenProxies = append(tokenProxies, proxy)
				}
			}
		}
		if len(tokenProxies) > 1 {
			proxies = append(proxies, tokenProxies...)
			continue
		}

		// A single token, or none: normalize the whole line instead. This is why
		// "http://a b" ends up stored verbatim rather than as one proxy.
		if proxy := NormalizeProxyURLWithScheme(line, defaultScheme); proxy != "" {
			proxies = append(proxies, proxy)
		}
	}
	return proxies
}
