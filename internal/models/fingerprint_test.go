package models

import (
	"strings"
	"testing"
)

func TestAcceptLanguage(t *testing.T) {
	cases := []struct {
		langs  []string
		locale string
		want   string
	}{
		{[]string{"ja-JP", "ja"}, "ja-JP", "ja-JP,ja;q=0.9"},
		{[]string{"en-US", "en"}, "en-US", "en-US,en;q=0.9"},
		{[]string{"a", "b", "c"}, "a", "a,b;q=0.9,c;q=0.8"},
		{nil, "de-DE", "de-DE"},
		{[]string{"solo"}, "solo", "solo"},
	}
	for _, c := range cases {
		f := DeviceFingerprint{Locale: c.locale, Languages: c.langs}
		if got := f.AcceptLanguage(); got != c.want {
			t.Fatalf("AcceptLanguage(%v) = %q, want %q", c.langs, got, c.want)
		}
	}
}

func TestGenerateRegisterFingerprint(t *testing.T) {
	for i := 0; i < 50; i++ {
		f := GenerateRegisterFingerprint()
		if f.Locale != "ja-JP" || f.Timezone != "Asia/Tokyo" {
			t.Fatalf("register FP locale/tz = %s/%s, want ja-JP/Asia/Tokyo", f.Locale, f.Timezone)
		}
		if f.Platform != "Win32" || f.Vendor != "Google Inc." {
			t.Fatalf("platform/vendor = %s/%s", f.Platform, f.Vendor)
		}
		if !strings.Contains(f.UserAgent, "Chrome/") || !strings.HasPrefix(f.UserAgent, "Mozilla/5.0 (Windows NT 10.0; Win64; x64)") {
			t.Fatalf("bad UA: %s", f.UserAgent)
		}
		maj := f.ChromeMajor()
		if maj < "134" || maj > "146" { // string compare ok for equal-width 3-digit
			t.Fatalf("chrome major out of range: %s", maj)
		}
		// viewport must be one of the known specs, and outer within +8..16 / +72..96.
		if f.OuterWidth < f.ViewportWidth+8 || f.OuterWidth > f.ViewportWidth+16 {
			t.Fatalf("outer width %d not in vw+[8,16] (vw=%d)", f.OuterWidth, f.ViewportWidth)
		}
		if f.OuterHeight < f.ViewportHeight+72 || f.OuterHeight > f.ViewportHeight+96 {
			t.Fatalf("outer height %d not in vh+[72,96] (vh=%d)", f.OuterHeight, f.ViewportHeight)
		}
		if !contains(hardwareConcurrencyChoices, f.HardwareConcurrency) {
			t.Fatalf("hw concurrency %d not a valid choice", f.HardwareConcurrency)
		}
		if !contains(deviceMemoryChoices, f.DeviceMemory) {
			t.Fatalf("device memory %d not a valid choice", f.DeviceMemory)
		}
	}
}

func TestGenerateFingerprintForExit(t *testing.T) {
	jp, err := GenerateFingerprintForExit(ProxyHealthResult{Success: true, Country: "JP", Timezone: "Asia/Tokyo"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if jp.Locale != "ja-JP" || jp.Timezone != "Asia/Tokyo" {
		t.Fatalf("JP exit FP = %s/%s", jp.Locale, jp.Timezone)
	}
	if jp.AcceptLanguage() != "ja-JP,ja;q=0.9" {
		t.Fatalf("JP accept-language = %q", jp.AcceptLanguage())
	}

	// Unknown country falls back to en-US locale, keeps the exit timezone.
	unk, err := GenerateFingerprintForExit(ProxyHealthResult{Success: true, Country: "ZZ", Timezone: "Etc/GMT"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if unk.Locale != "en-US" || unk.Timezone != "Etc/GMT" {
		t.Fatalf("unknown-country FP = %s/%s, want en-US/Etc/GMT", unk.Locale, unk.Timezone)
	}

	// Unusable exit info errors.
	if _, err := GenerateFingerprintForExit(ProxyHealthResult{Success: false, Country: "JP"}); err == nil {
		t.Fatalf("expected error for failed exit")
	}
	if _, err := GenerateFingerprintForExit(ProxyHealthResult{Success: true, Country: ""}); err == nil {
		t.Fatalf("expected error for empty country")
	}
}

func TestFingerprintSummaryText(t *testing.T) {
	f := &DeviceFingerprint{
		UserAgent: "Mozilla/5.0 Chrome/140.0.7000.100 Safari/537.36",
		Locale:    "ja-JP", Timezone: "Asia/Tokyo",
		ViewportWidth: 1440, ViewportHeight: 900,
		HardwareConcurrency: 8, DeviceMemory: 16,
	}
	want := "Chrome/140 1440x900 ja-JP Asia/Tokyo cpu=8 mem=16"
	if got := FingerprintSummaryText(f); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if FingerprintSummaryText(nil) != "" {
		t.Fatalf("nil summary should be empty")
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
