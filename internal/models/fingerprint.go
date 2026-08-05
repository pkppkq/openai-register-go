package models

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// deviceProfile is one (locale, languages, timezone) triple a fingerprint is
// built from. Mirrors the dicts in app.py DEVICE_PROFILES etc.
type deviceProfile struct {
	Locale    string
	Languages []string
	Timezone  string
}

// Profile pools — ported verbatim from app.py:668-691.
var (
	deviceProfiles = []deviceProfile{
		{"en-US", []string{"en-US", "en"}, "America/New_York"},
		{"en-US", []string{"en-US", "en"}, "America/Chicago"},
		{"en-US", []string{"en-US", "en"}, "America/Los_Angeles"},
		{"en-GB", []string{"en-GB", "en"}, "Europe/London"},
	}
	registerDeviceProfiles = []deviceProfile{
		{"ja-JP", []string{"ja-JP", "ja"}, "Asia/Tokyo"},
	}
	teamDeviceProfiles = []deviceProfile{
		{"en-US", []string{"en-US", "en"}, "America/New_York"},
		{"en-US", []string{"en-US", "en"}, "America/Chicago"},
		{"en-US", []string{"en-US", "en"}, "America/Los_Angeles"},
	}
	paymentDeviceProfiles = []deviceProfile{
		{"ja-JP", []string{"ja-JP", "ja"}, "Asia/Tokyo"},
	}
)

// CountryBrowserLocale maps a proxy-exit country to a browser locale
// (app.py COUNTRY_BROWSER_LOCALE).
var CountryBrowserLocale = map[string]string{
	"AU": "en-AU", "BR": "pt-BR", "CA": "en-CA", "DE": "de-DE", "ES": "es-ES",
	"FR": "fr-FR", "GB": "en-GB", "ID": "id-ID", "IN": "en-IN", "IT": "it-IT",
	"JP": "ja-JP", "KR": "ko-KR", "MX": "es-MX", "NL": "nl-NL", "NZ": "en-NZ",
	"PT": "pt-PT", "SG": "en-SG", "TH": "th-TH", "TW": "zh-TW", "US": "en-US",
	"VN": "vi-VN",
}

// AcceptLanguage mirrors the DeviceFingerprint.accept_language property: the
// first language verbatim, each subsequent language with a decreasing q-value
// (0.9, 0.8, ... floored at 0.5).
func (f DeviceFingerprint) AcceptLanguage() string {
	if len(f.Languages) == 0 {
		return f.Locale
	}
	parts := []string{f.Languages[0]}
	for i, lang := range f.Languages[1:] {
		q := 0.9 - float64(i)*0.1
		if q < 0.5 {
			q = 0.5
		}
		parts = append(parts, fmt.Sprintf("%s;q=%.1f", lang, q))
	}
	return strings.Join(parts, ",")
}

// viewportSpec is (viewportW, viewportH, screenW, screenH, scale).
type viewportSpec struct {
	VW, VH, SW, SH int
	Scale          float64
}

var viewportChoices = []viewportSpec{
	{1280, 720, 1280, 720, 1},
	{1365, 768, 1366, 768, 1},
	{1440, 900, 1440, 900, 1},
	{1536, 864, 1536, 864, 1.25},
	{1600, 900, 1600, 900, 1},
	{1920, 1080, 1920, 1080, 1},
}

var (
	hardwareConcurrencyChoices = []int{4, 6, 8, 8, 12, 16}
	deviceMemoryChoices        = []int{4, 8, 8, 16}
)

// randInt returns a random int in [lo, hi] inclusive (Python random.randint).
func randInt(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rand.IntN(hi-lo+1)
}

// generateFingerprint mirrors generate_fingerprint: pick a profile + viewport and
// synthesize a plausible Windows Chrome device fingerprint. Passing nil uses the
// default DEVICE_PROFILES pool.
//
// Unexported: deviceProfile is unexported, so an exported form could only ever be
// handed nil from outside the package. Callers use the named entry points below.
func generateFingerprint(profiles []deviceProfile) DeviceFingerprint {
	pool := profiles
	if len(pool) == 0 {
		pool = deviceProfiles
	}
	p := pool[rand.IntN(len(pool))]
	v := viewportChoices[rand.IntN(len(viewportChoices))]
	major := randInt(134, 146)
	build := randInt(6000, 9999)
	patch := randInt(50, 220)
	ua := fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.%d.%d Safari/537.36", major, build, patch)
	return DeviceFingerprint{
		UserAgent:           ua,
		Locale:              p.Locale,
		Languages:           append([]string(nil), p.Languages...),
		Timezone:            p.Timezone,
		ViewportWidth:       v.VW,
		ViewportHeight:      v.VH,
		ScreenWidth:         v.SW,
		ScreenHeight:        v.SH,
		OuterWidth:          v.VW + randInt(8, 16),
		OuterHeight:         v.VH + randInt(72, 96),
		DeviceScaleFactor:   v.Scale,
		HardwareConcurrency: hardwareConcurrencyChoices[rand.IntN(len(hardwareConcurrencyChoices))],
		DeviceMemory:        deviceMemoryChoices[rand.IntN(len(deviceMemoryChoices))],
		Platform:            "Win32",
		Vendor:              "Google Inc.",
	}
}

// GenerateFingerprintForLocale builds a fingerprint from an explicit
// locale/languages/timezone triple. This is the exported form of the Python
// `generate_fingerprint([{...}])` one-off-profile call, used for the
// en-US/UTC fallback when proxy-exit geo is unavailable (app.py:9174).
func GenerateFingerprintForLocale(locale string, languages []string, timezone string) DeviceFingerprint {
	if len(languages) == 0 {
		languages = []string{locale}
	}
	if timezone == "" {
		timezone = "UTC"
	}
	return generateFingerprint([]deviceProfile{{Locale: locale, Languages: languages, Timezone: timezone}})
}

// GenerateRegisterFingerprint mirrors generate_register_fingerprint (JP profile).
func GenerateRegisterFingerprint() DeviceFingerprint {
	return generateFingerprint(registerDeviceProfiles)
}

// GenerateTeamFingerprint mirrors generate_team_fingerprint (US profiles).
func GenerateTeamFingerprint() DeviceFingerprint {
	return generateFingerprint(teamDeviceProfiles)
}

// GeneratePaymentFingerprint mirrors generate_payment_fingerprint (JP profile).
func GeneratePaymentFingerprint() DeviceFingerprint {
	return generateFingerprint(paymentDeviceProfiles)
}

// GenerateFingerprintForExit mirrors generate_fingerprint_for_exit: derive the
// locale/timezone from the proxy exit geo so the browser matches its IP. Returns
// an error when the exit info is unusable.
func GenerateFingerprintForExit(exit ProxyHealthResult) (DeviceFingerprint, error) {
	if !exit.Success || exit.Country == "" {
		return DeviceFingerprint{}, fmt.Errorf("代理出口信息不可用，无法生成匹配指纹")
	}
	locale := CountryBrowserLocale[exit.Country]
	if locale == "" {
		locale = "en-US"
	}
	primary := locale
	if i := strings.Index(locale, "-"); i >= 0 {
		primary = locale[:i]
	}
	languages := []string{locale, primary}
	if locale == primary {
		languages = []string{locale}
	}
	tz := exit.Timezone
	if tz == "" {
		tz = "UTC"
	}
	return generateFingerprint([]deviceProfile{{Locale: locale, Languages: languages, Timezone: tz}}), nil
}

// FingerprintSummaryText mirrors fingerprint_summary_text.
func FingerprintSummaryText(fp *DeviceFingerprint) string {
	if fp == nil {
		return ""
	}
	return fmt.Sprintf("Chrome/%s %dx%d %s %s cpu=%d mem=%d",
		fp.ChromeMajor(), fp.ViewportWidth, fp.ViewportHeight, fp.Locale, fp.Timezone,
		fp.HardwareConcurrency, fp.DeviceMemory)
}
