package browser

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func sampleFP() models.DeviceFingerprint {
	return models.DeviceFingerprint{
		UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.7000.100 Safari/537.36",
		Locale:              "ja-JP",
		Languages:           []string{"ja-JP", "ja"},
		Timezone:            "Asia/Tokyo",
		ViewportWidth:       1440,
		ViewportHeight:      900,
		ScreenWidth:         1440,
		ScreenHeight:        900,
		OuterWidth:          1452,
		OuterHeight:         980,
		DeviceScaleFactor:   1,
		HardwareConcurrency: 8,
		DeviceMemory:        16,
		Platform:            "Win32",
		Vendor:              "Google Inc.",
	}
}

func balanced(s string, open, close rune) bool {
	n := 0
	for _, r := range s {
		switch r {
		case open:
			n++
		case close:
			n--
		}
		if n < 0 {
			return false
		}
	}
	return n == 0
}

func TestFingerprintInitScript(t *testing.T) {
	js := FingerprintInitScript(sampleFP())
	if strings.Contains(js, "__FP_PAYLOAD__") {
		t.Fatalf("payload placeholder was not substituted")
	}
	// Substituted values present.
	for _, want := range []string{
		`"platform":"Win32"`,
		`"languages":["ja-JP","ja"]`,
		`"hardwareConcurrency":8`,
		`"deviceMemory":16`,
		`"chromeMajor":"140"`,
		`"chromeFull":"140.0.7000.100"`,
		"defineGetter(Navigator.prototype, 'webdriver', undefined)",
		"navigator.mediaDevices.getUserMedia = deniedMedia",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("init script missing %q", want)
		}
	}
	// The bug that motivated this port: braces/parens must balance (no stray '}').
	if !balanced(js, '{', '}') {
		t.Fatalf("unbalanced braces in generated init script")
	}
	if !balanced(js, '(', ')') {
		t.Fatalf("unbalanced parens in generated init script")
	}
	if !strings.HasSuffix(strings.TrimSpace(js), "})();") {
		t.Fatalf("init script should end with the IIFE invocation })();, got tail %q", tail(js, 12))
	}
}

func TestFingerprintHeaders(t *testing.T) {
	h := FingerprintHeaders(sampleFP())
	if h["Accept-Language"] != "ja-JP,ja;q=0.9" {
		t.Fatalf("Accept-Language = %q", h["Accept-Language"])
	}
	if h["sec-ch-ua"] != `"Google Chrome";v="140", "Chromium";v="140", "Not.A/Brand";v="24"` {
		t.Fatalf("sec-ch-ua = %q", h["sec-ch-ua"])
	}
	if h["sec-ch-ua-full-version-list"] != `"Google Chrome";v="140.0.7000.100", "Chromium";v="140.0.7000.100", "Not.A/Brand";v="24.0.0.0"` {
		t.Fatalf("full-version-list = %q", h["sec-ch-ua-full-version-list"])
	}
	if h["sec-ch-ua-platform"] != `"Windows"` || h["sec-ch-ua-mobile"] != "?0" || h["sec-ch-ua-platform-version"] != `"15.0.0"` {
		t.Fatalf("static hints wrong: %+v", h)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
