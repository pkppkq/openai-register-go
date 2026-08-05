package phoneprovider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// HTTPGetFunc fetches an SMS-relay URL. Injected so tests never touch the
// network.
type HTTPGetFunc func(ctx context.Context, url string, timeout time.Duration) (string, error)

// defaultHTTPGet mirrors `requests.get(sms_url, timeout=...)` (app.py:16646).
// Note there is NO raise_for_status: Python fed the body of a 404 or 500 to the
// code extractor just the same, so a non-2xx status is not an error here.
func defaultHTTPGet(ctx context.Context, rawURL string, timeout time.Duration) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// waitForPhoneCode ports _wait_for_phone_code (app.py:16639-16667): poll the
// relay URL until a code shows up or the deadline passes. timeout is seconds.
func (p *Provider) waitForPhoneCode(number, smsURL string, timeout int) (string, error) {
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	lastText := ""

	for time.Now().Before(deadline) {
		// max(1, min(20, int(deadline - now))) — int() truncates toward zero.
		requestTimeout := int(time.Until(deadline).Seconds())
		if requestTimeout > 20 {
			requestTimeout = 20
		}
		if requestTimeout < 1 {
			requestTimeout = 1
		}

		text, err := p.cfg.HTTPGet(p.context(), smsURL, time.Duration(requestTimeout)*time.Second)
		if err != nil {
			// Python's `except Exception` stores the full str(exc) — NOT truncated
			// to 300 like the body is.
			lastText = err.Error()
		} else {
			text = strings.TrimSpace(text)
			lastText = truncRunes(text, 300)
			if code := extractPhoneCode(text); code != "" {
				if p.cfg.Pool != nil {
					p.cfg.Pool.RecordCode(number, code, p.receiveLimit())
				}
				return code, nil
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining > manualPollInterval {
			remaining = manualPollInterval
		}
		p.cfg.Sleep(remaining)
	}
	return "", fmt.Errorf("等待手机号 %s 短信验证码超时，最后返回: %s", number, lastText)
}

// phoneCodeWhitespaceRe is Python's `re.sub(r"\s+", " ", text)` (app.py:16670).
// Go's RE2 \s is ASCII-only ([\t\n\f\r ]) while Python's is Unicode-aware, so
// NBSP/ideographic spaces in rendered SMS pages would otherwise survive and
// break the fixed-width [^\d]{0,20} windows below. \v and \x1c-\x1f are in
// Python's \s but not Go's, hence the explicit ranges.
var phoneCodeWhitespaceRe = regexp.MustCompile(`[\s\p{Z}\x{0085}\x{000B}\x{001C}-\x{001F}]+`)

// The first three patterns of _extract_phone_code (app.py:16671-16676), in
// order. \d is written as \p{Nd} because Python's \d matches any Unicode decimal
// digit while Go's is ASCII-only; the same goes for the negated class.
var phoneCodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)OpenAI[^\p{Nd}]{0,80}(\p{Nd}{6})`),
	regexp.MustCompile(`(?i)验证代码[^\p{Nd}]{0,20}(\p{Nd}{6})`),
	regexp.MustCompile(`(?i)验证码[^\p{Nd}]{0,20}(\p{Nd}{6})`),
}

// extractPhoneCode ports _extract_phone_code (app.py:16669-16681).
func extractPhoneCode(text string) string {
	normalized := phoneCodeWhitespaceRe.ReplaceAllString(text, " ")
	for _, pattern := range phoneCodePatterns {
		if m := pattern.FindStringSubmatch(normalized); m != nil {
			return m[1]
		}
	}
	return findWordBoundedSixDigits(normalized)
}

// findWordBoundedSixDigits is the fourth pattern, `\b(\d{6})\b`, walked by hand.
//
// Go's \b is ASCII-only: for "コード123456" it sees a boundary between "ド" and
// "1" and would return a code, whereas Python's Unicode-aware \b sees two word
// characters, finds no boundary and returns "". Compiling the pattern in Go
// would therefore accept SMS text Python rejects — so the boundary is evaluated
// with Python's \w definition (alphanumeric or underscore) instead.
func findWordBoundedSixDigits(s string) string {
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if !unicode.IsDigit(runes[i]) {
			i++
			continue
		}
		end := i
		for end < len(runes) && unicode.IsDigit(runes[end]) {
			end++
		}
		// A run of 7+ digits has no interior boundary, so Python matches nothing
		// inside it either.
		if end-i == 6 &&
			(i == 0 || !isPyWordRune(runes[i-1])) &&
			(end == len(runes) || !isPyWordRune(runes[end])) {
			return string(runes[i:end])
		}
		i = end
	}
	return ""
}

// isPyWordRune is CPython's \w for str patterns: alphanumeric (L*, Nd, Nl, No)
// or underscore.
func isPyWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// truncRunes slices by runes because Python's text[:300] counts characters, not
// bytes — a byte slice could also split a UTF-8 sequence in the log line.
func truncRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
