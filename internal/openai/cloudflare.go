package openai

import (
	"html"
	"regexp"
	"strings"
)

// cfChallengeMarkers are single substrings that alone indicate a Cloudflare
// challenge (app.py _is_cloudflare_challenge, 10669).
var cfChallengeMarkers = []string{
	"challenges.cloudflare.com",
	"__cf_chl",
	"just a moment",
	"checking your browser",
	"attention required",
	"cf-browser-verification",
	"challenge-platform",
	"cf-turnstile",
	"verify you are human",
	"needs to review the security",
}

// IsCloudflareChallengeText mirrors _is_cloudflare_challenge: case-insensitive
// marker match over arbitrary text (title/url/body/html), plus two AND-combos.
// This matcher and CFInterstitialDetectJS jointly decide pass/fail, so both must
// stay faithful — a drift causes false passes or hangs.
func IsCloudflareChallengeText(text string) bool {
	lower := pyLower(text)
	if lower == "" {
		return false
	}
	for _, m := range cfChallengeMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	if strings.Contains(lower, "cloudflare") && strings.Contains(lower, "challenge") {
		return true
	}
	if strings.Contains(lower, "turnstile") && strings.Contains(lower, "cloudflare") {
		return true
	}
	return false
}

// app.py:10691/10696 — both `\s` are Python str-mode, i.e. Unicode. A minified
// OpenAI bundle carrying a U+00A0 between the key and its value would hide the
// challenge URL from an ASCII-only `\s*`, and the direct-URL fallback would run
// past a NBSP instead of stopping at it.
var (
	reCFObfuscatedVar = regexp.MustCompile(`cUPMDTk:[` + pyWSClass + `]*"([^"]+)"`)
	reCFReplaceState  = regexp.MustCompile(`history\.replaceState\([^,]+,[^,]+,"([^"]+)"`)
	reCFDirectURL     = regexp.MustCompile(`https://challenges\.cloudflare\.com/[^` + pyWSClass + `"']+`)
)

// ExtractCloudflareChallengeURL mirrors _extract_cloudflare_challenge_url:
// HTML-unescape, then pull the challenge URL out of OpenAI's obfuscated JS var
// or a history.replaceState call, un-escaping backslash-slashes and making
// relative URLs absolute against auth.openai.com. Falls back to a direct
// challenges.cloudflare.com URL. Returns "" when nothing matches.
//
// NOTE: the `cUPMDTk` var name is OpenAI-build-specific and will silently break
// when they rebuild; the direct-URL fallback is the resilient path.
func ExtractCloudflareChallengeURL(text string) string {
	value := html.UnescapeString(text)
	for _, re := range []*regexp.Regexp{reCFObfuscatedVar, reCFReplaceState} {
		if m := re.FindStringSubmatch(value); m != nil {
			raw := strings.ReplaceAll(m[1], `\/`, "/")
			if strings.HasPrefix(raw, "http") {
				return raw
			}
			return AuthBaseURL + raw
		}
	}
	if m := reCFDirectURL.FindString(value); m != "" {
		return m
	}
	return ""
}
