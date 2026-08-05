// Package browser is the go-rod anti-detection layer for the OpenAI register
// worker: it launches Chromium with the palpay MV3 extension, injects a
// device-fingerprint spoofing script + matching client-hint headers on every
// document, and provides the React-safe fill / synthetic-click / session-probe /
// Turnstile-iframe primitives the worker clusters build on.
package browser

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// fingerprintInitTemplate is the anti-detection init script, ported from the
// worker _install_fingerprint (app.py:9247). NOTE: the Python source had a stray
// extra '}' ("}})();") making it a JS SyntaxError, so Playwright silently dropped
// it and the worker path never actually applied these spoofs. The embedded asset
// fixes that (verified with `node --check`); __FP_PAYLOAD__ is replaced at runtime.
//
//go:embed fingerprint_init.js
var fingerprintInitTemplate string

// fpPayload is the JSON object substituted into the init script. Keys match the
// JS field accesses exactly (app.py:9224-9238).
type fpPayload struct {
	Platform            string   `json:"platform"`
	Vendor              string   `json:"vendor"`
	Languages           []string `json:"languages"`
	HardwareConcurrency int      `json:"hardwareConcurrency"`
	DeviceMemory        int      `json:"deviceMemory"`
	MaxTouchPoints      int      `json:"maxTouchPoints"`
	ScreenWidth         int      `json:"screenWidth"`
	ScreenHeight        int      `json:"screenHeight"`
	OuterWidth          int      `json:"outerWidth"`
	OuterHeight         int      `json:"outerHeight"`
	DeviceScaleFactor   float64  `json:"deviceScaleFactor"`
	ChromeMajor         string   `json:"chromeMajor"`
	ChromeFull          string   `json:"chromeFull"`
}

// FingerprintInitScript returns the fingerprint spoof script for fp, ready to
// register via EvalOnNewDocument on every page/popup (per-target in go-rod).
func FingerprintInitScript(fp models.DeviceFingerprint) string {
	payload := fpPayload{
		Platform:            fp.Platform,
		Vendor:              orDefault(fp.Vendor, "Google Inc."),
		Languages:           fp.Languages,
		HardwareConcurrency: fp.HardwareConcurrency,
		DeviceMemory:        fp.DeviceMemory,
		MaxTouchPoints:      fp.MaxTouchPoints,
		ScreenWidth:         fp.ScreenWidth,
		ScreenHeight:        fp.ScreenHeight,
		OuterWidth:          fp.OuterWidth,
		OuterHeight:         fp.OuterHeight,
		DeviceScaleFactor:   fp.DeviceScaleFactor,
		ChromeMajor:         fp.ChromeMajor(),
		ChromeFull:          fp.ChromeFull(),
	}
	// ensure_ascii=False equivalent: don't HTML-escape <, >, &.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	js := strings.TrimRight(buf.String(), "\n")
	return strings.Replace(fingerprintInitTemplate, "__FP_PAYLOAD__", js, 1)
}

// FingerprintHeaders returns the client-hint + Accept-Language headers that must
// accompany the fingerprint (app.py:9239-9246). They must stay consistent with
// the injected payload and the UA.
func FingerprintHeaders(fp models.DeviceFingerprint) map[string]string {
	major := fp.ChromeMajor()
	full := fp.ChromeFull()
	return map[string]string{
		"Accept-Language":             fp.AcceptLanguage(),
		"sec-ch-ua":                   fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not.A/Brand";v="24"`, major, major),
		"sec-ch-ua-full-version-list": fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not.A/Brand";v="24.0.0.0"`, full, full),
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"15.0.0"`,
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
