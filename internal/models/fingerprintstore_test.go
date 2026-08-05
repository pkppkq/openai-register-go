package models

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// sampleFP is a fully populated, valid fingerprint.
func sampleFP() DeviceFingerprint {
	return DeviceFingerprint{
		UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.7123.99 Safari/537.36",
		Locale:              "ja-JP",
		Languages:           []string{"ja-JP", "ja"},
		Timezone:            "Asia/Tokyo",
		ViewportWidth:       1536,
		ViewportHeight:      864,
		ScreenWidth:         1536,
		ScreenHeight:        864,
		OuterWidth:          1548,
		OuterHeight:         948,
		DeviceScaleFactor:   1.25,
		HardwareConcurrency: 12,
		DeviceMemory:        16,
		Platform:            "Win32",
		Vendor:              "Google Inc.",
		MaxTouchPoints:      0,
	}
}

// ---------------------------------------------------------------------------
// reuse vs regenerate
// ---------------------------------------------------------------------------

func TestResolveAccountFingerprintReusesStored(t *testing.T) {
	saved := sampleFP()
	acc := &MailAccount{Email: "a@b.c", BrowserFingerprint: &saved}

	d := ResolveAccountFingerprint(acc, func() DeviceFingerprint {
		t.Fatal("generator must not be called when a fingerprint is stored (Python `or` short-circuits, app.py:20993)")
		return DeviceFingerprint{}
	})
	if !d.Reused {
		t.Fatal("Reused = false, want true")
	}
	if !FingerprintsEqual(&d.Fingerprint, &saved) {
		t.Fatalf("reused fingerprint differs:\n got %+v\nwant %+v", d.Fingerprint, saved)
	}
	// Must be a copy: mutating the decision must not touch the account.
	d.Fingerprint.Languages[0] = "MUTATED"
	if acc.BrowserFingerprint.Languages[0] != "ja-JP" {
		t.Fatal("decision aliases the account's Languages slice")
	}
}

// A saved fingerprint survives a proxy exit in a completely different country.
// This is the rule at app.py:9168 — `_fingerprint_fixed` is already true, so the
// exit-derived generator never runs.
func TestResolveAccountFingerprintIgnoresProxyExit(t *testing.T) {
	saved := sampleFP() // ja-JP / Asia/Tokyo
	acc := &MailAccount{Email: "a@b.c", BrowserFingerprint: &saved}

	exit := ProxyHealthResult{Success: true, Country: "DE", Timezone: "Europe/Berlin"}
	d := ResolveAccountFingerprint(acc, func() DeviceFingerprint {
		fp, err := GenerateFingerprintForExit(exit)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		return fp
	})
	if !d.Reused || d.Fingerprint.Locale != "ja-JP" || d.Fingerprint.Timezone != "Asia/Tokyo" {
		t.Fatalf("proxy exit invalidated the saved fingerprint: %+v", d.Fingerprint)
	}
}

func TestResolveAccountFingerprintGenerates(t *testing.T) {
	cases := []struct {
		name string
		acc  *MailAccount
	}{
		{"nil account", nil},
		{"no stored fingerprint", &MailAccount{Email: "a@b.c"}},
		{"malformed stored fingerprint", &MailAccount{
			Email:              "a@b.c",
			BrowserFingerprint: &DeviceFingerprint{Locale: "ja-JP"}, // no UA/tz/platform
		}},
		{"blank-only stored fingerprint", &MailAccount{
			Email: "a@b.c",
			BrowserFingerprint: &DeviceFingerprint{
				UserAgent: "  ", Locale: "ja-JP", Timezone: "Asia/Tokyo", Platform: "Win32",
			},
		}},
	}
	for _, c := range cases {
		calls := 0
		want := sampleFP()
		want.Locale = "de-DE"
		d := ResolveAccountFingerprint(c.acc, func() DeviceFingerprint { calls++; return want })
		if d.Reused {
			t.Fatalf("%s: Reused = true, want false", c.name)
		}
		if calls != 1 {
			t.Fatalf("%s: generator called %d times, want 1", c.name, calls)
		}
		if d.Fingerprint.Locale != "de-DE" {
			t.Fatalf("%s: got %q", c.name, d.Fingerprint.Locale)
		}
	}
}

func TestResolveAccountFingerprintDefaultGenerator(t *testing.T) {
	d := ResolveAccountFingerprint(&MailAccount{Email: "a@b.c"}, nil)
	if d.Reused {
		t.Fatal("Reused = true, want false")
	}
	// Default is GenerateRegisterFingerprint (app.py:8861 / 20993) -> JP profile.
	if d.Fingerprint.Locale != "ja-JP" || d.Fingerprint.Timezone != "Asia/Tokyo" {
		t.Fatalf("default generator = %s/%s, want ja-JP/Asia/Tokyo", d.Fingerprint.Locale, d.Fingerprint.Timezone)
	}
}

func TestSavedAccountFingerprint(t *testing.T) {
	if SavedAccountFingerprint(nil) != nil {
		t.Fatal("nil account must give nil")
	}
	if SavedAccountFingerprint(&MailAccount{}) != nil {
		t.Fatal("account without fingerprint must give nil")
	}
	fp := sampleFP()
	if got := SavedAccountFingerprint(&MailAccount{BrowserFingerprint: &fp}); got != &fp {
		t.Fatal("stored fingerprint must be returned as-is")
	}
}

// ---------------------------------------------------------------------------
// validity / normalization
// ---------------------------------------------------------------------------

func TestValidFingerprint(t *testing.T) {
	if ValidFingerprint(nil) {
		t.Fatal("nil must be invalid")
	}
	ok := sampleFP()
	if !ValidFingerprint(&ok) {
		t.Fatal("sample must be valid")
	}
	for _, mut := range []func(*DeviceFingerprint){
		func(f *DeviceFingerprint) { f.UserAgent = "" },
		func(f *DeviceFingerprint) { f.Locale = " " },
		func(f *DeviceFingerprint) { f.Timezone = "" },
		func(f *DeviceFingerprint) { f.Platform = "\t" },
	} {
		bad := sampleFP()
		mut(&bad)
		if ValidFingerprint(&bad) {
			t.Fatalf("must be invalid: %+v", bad)
		}
	}
}

func TestNormalizeFingerprintForStorageDefaults(t *testing.T) {
	// Every numeric zero must take Python's `or` default, NOT be clamped to 1.
	in := DeviceFingerprint{
		UserAgent: " ua ", Locale: " en-US ", Timezone: " UTC ", Platform: " Win32 ",
		Languages: []string{"", "  en-US  ", " "},
	}
	got := NormalizeFingerprintForStorage(&in)
	if got == nil {
		t.Fatal("nil result")
	}
	want := &DeviceFingerprint{
		UserAgent: "ua", Locale: "en-US", Languages: []string{"en-US"}, Timezone: "UTC",
		ViewportWidth: 1280, ViewportHeight: 720,
		ScreenWidth: 1280, ScreenHeight: 720,
		OuterWidth: 1296, OuterHeight: 800,
		DeviceScaleFactor: 1, HardwareConcurrency: 8, DeviceMemory: 8,
		Platform: "Win32", Vendor: "Google Inc.", MaxTouchPoints: 0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %+v\nwant %+v", *got, *want)
	}
}

func TestNormalizeFingerprintForStorageEdges(t *testing.T) {
	if NormalizeFingerprintForStorage(nil) != nil {
		t.Fatal("nil in, nil out")
	}
	if NormalizeFingerprintForStorage(&DeviceFingerprint{Locale: "x"}) != nil {
		t.Fatal("malformed in, nil out")
	}

	// Languages empty -> [locale] (app.py:1480).
	in := sampleFP()
	in.Languages = nil
	if got := NormalizeFingerprintForStorage(&in); len(got.Languages) != 1 || got.Languages[0] != "ja-JP" {
		t.Fatalf("languages fallback = %v, want [ja-JP]", got.Languages)
	}

	// Negative numerics are truthy in Python, so they skip the default and hit
	// the max() clamp instead.
	neg := sampleFP()
	neg.ViewportWidth = -5
	neg.MaxTouchPoints = -3
	got := NormalizeFingerprintForStorage(&neg)
	if got.ViewportWidth != 1 {
		t.Fatalf("negative viewport = %d, want 1", got.ViewportWidth)
	}
	if got.MaxTouchPoints != 0 {
		t.Fatalf("negative touch points = %d, want 0", got.MaxTouchPoints)
	}

	// Vendor is not stripped in Python (app.py:1492 has no .strip()).
	blank := sampleFP()
	blank.Vendor = "  "
	if got := NormalizeFingerprintForStorage(&blank); got.Vendor != "  " {
		t.Fatalf("vendor = %q, want %q", got.Vendor, "  ")
	}
}

func TestNormalizeFingerprintForStorageIdempotent(t *testing.T) {
	for i := 0; i < 20; i++ {
		fp := GenerateRegisterFingerprint()
		once := NormalizeFingerprintForStorage(&fp)
		twice := NormalizeFingerprintForStorage(once)
		if !reflect.DeepEqual(once, twice) {
			t.Fatalf("not idempotent:\n once %+v\ntwice %+v", *once, *twice)
		}
		// A freshly generated fingerprint must already be storage-clean.
		if !FingerprintsEqual(&fp, once) {
			t.Fatalf("generated fingerprint changed under normalization:\n got %+v\nwant %+v", *once, fp)
		}
	}
}

// ---------------------------------------------------------------------------
// storage round trip
// ---------------------------------------------------------------------------

func TestFingerprintRoundTripThroughJSON(t *testing.T) {
	for i := 0; i < 30; i++ {
		fp := GenerateRegisterFingerprint()
		if i%3 == 1 {
			fp = GenerateTeamFingerprint()
		}
		if i%3 == 2 {
			fp = GenerateFingerprintForLocale("de-DE", []string{"de-DE", "de"}, "Europe/Berlin")
		}
		fp.MaxTouchPoints = i % 4

		payload, ok := FingerprintSavePayload(&fp)
		if !ok {
			t.Fatal("valid fingerprint produced no payload")
		}
		// Through real JSON: ints become float64 on the way back, exactly as
		// state.json delivers them.
		blob, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(blob, &decoded); err != nil {
			t.Fatal(err)
		}
		back := ParseStoredFingerprint(decoded)
		if back == nil {
			t.Fatalf("round trip lost the fingerprint: %s", blob)
		}
		if !FingerprintsEqual(&fp, back) {
			t.Fatalf("lossy round trip:\n got %+v\nwant %+v", *back, fp)
		}
		// And through the account map, which is the real state.json path.
		acc := MailAccount{Email: "a@b.c", ClientID: "cid", RefreshToken: "rt", BrowserFingerprint: &fp}
		accBlob, err := json.Marshal(AccountToMap(acc))
		if err != nil {
			t.Fatal(err)
		}
		var accDecoded map[string]any
		if err := json.Unmarshal(accBlob, &accDecoded); err != nil {
			t.Fatal(err)
		}
		got := AccountFromMap(accDecoded)
		if !FingerprintsEqual(got.BrowserFingerprint, &fp) {
			t.Fatalf("AccountToMap/AccountFromMap lost fingerprint data:\n got %+v\nwant %+v", got.BrowserFingerprint, fp)
		}
	}
}

// state.json bytes must not depend on Go's random map iteration order.
func TestFingerprintPayloadSerializationIsDeterministic(t *testing.T) {
	fp := sampleFP()
	payload, _ := FingerprintSavePayload(&fp)
	first, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("map key order reached output:\n%s\n%s", first, again)
		}
	}
}

func TestFingerprintSavePayload(t *testing.T) {
	if payload, ok := FingerprintSavePayload(nil); ok || len(payload) != 0 {
		t.Fatalf("nil fingerprint must not be queued, got ok=%v payload=%v", ok, payload)
	}
	fp := sampleFP()
	payload, ok := FingerprintSavePayload(&fp)
	if !ok {
		t.Fatal("valid fingerprint must be queued")
	}
	if len(payload) != 16 {
		t.Fatalf("payload has %d keys, want 16", len(payload))
	}
	for _, k := range []string{
		"user_agent", "locale", "languages", "timezone",
		"viewport_width", "viewport_height", "screen_width", "screen_height",
		"outer_width", "outer_height", "device_scale_factor",
		"hardware_concurrency", "device_memory", "platform", "vendor", "max_touch_points",
	} {
		if _, present := payload[k]; !present {
			t.Fatalf("payload missing %q", k)
		}
	}
}

func TestParseStoredFingerprint(t *testing.T) {
	if ParseStoredFingerprint(nil) != nil {
		t.Fatal("nil map must give nil")
	}
	if ParseStoredFingerprint(map[string]any{}) != nil {
		t.Fatal("empty map must give nil (app.py:1474 required-field gate)")
	}
	// Missing platform -> nil, the classic half-written entry.
	if ParseStoredFingerprint(map[string]any{
		"user_agent": "ua", "locale": "en-US", "timezone": "UTC",
	}) != nil {
		t.Fatal("missing platform must give nil")
	}
	// Explicit zeros take Python's `or` defaults, not the 1 clamp.
	got := ParseStoredFingerprint(map[string]any{
		"user_agent": "ua", "locale": "en-US", "timezone": "UTC", "platform": "Win32",
		"viewport_width": float64(0), "device_scale_factor": float64(0),
		"hardware_concurrency": float64(0), "device_memory": float64(0),
	})
	if got == nil {
		t.Fatal("nil result")
	}
	if got.ViewportWidth != 1280 || got.DeviceScaleFactor != 1 || got.HardwareConcurrency != 8 || got.DeviceMemory != 8 {
		t.Fatalf("stored zeros did not take Python defaults: vw=%d dsf=%v hc=%d mem=%d",
			got.ViewportWidth, got.DeviceScaleFactor, got.HardwareConcurrency, got.DeviceMemory)
	}
	if len(got.Languages) != 1 || got.Languages[0] != "en-US" {
		t.Fatalf("languages = %v, want [en-US]", got.Languages)
	}
	if got.Vendor != "Google Inc." {
		t.Fatalf("vendor = %q", got.Vendor)
	}
}

// The persistence payload goes worker -> state writer in memory, never through
// JSON, so ParseStoredFingerprint must read the native Go ints FingerprintToMap
// emits. (models.mInt does not — see the report.)
func TestParseStoredFingerprintNativeNumerics(t *testing.T) {
	fp := sampleFP()
	payload := FingerprintToMap(&fp) // Go ints, not float64
	back := ParseStoredFingerprint(payload)
	if back == nil {
		t.Fatal("nil result")
	}
	if !FingerprintsEqual(&fp, back) {
		t.Fatalf("in-memory map round trip is lossy:\n got %+v\nwant %+v", *back, fp)
	}

	// Assorted numeric widths must all read through.
	got := ParseStoredFingerprint(map[string]any{
		"user_agent": "ua", "locale": "en-US", "timezone": "UTC", "platform": "Win32",
		"viewport_width": int64(1600), "viewport_height": int32(900),
		"screen_width": uint(1600), "screen_height": float32(900),
		"hardware_concurrency": "6", "device_memory": float64(16),
		"device_scale_factor": 2, "max_touch_points": int8(5),
	})
	if got == nil {
		t.Fatal("native numeric map unexpectedly failed to parse")
	}
	if got.ViewportWidth != 1600 || got.ViewportHeight != 900 ||
		got.ScreenWidth != 1600 || got.ScreenHeight != 900 ||
		got.HardwareConcurrency != 6 || got.DeviceMemory != 16 ||
		got.DeviceScaleFactor != 2 || got.MaxTouchPoints != 5 {
		t.Fatalf("numeric coercion failed: %+v", *got)
	}
}

func TestPyIntOfSupportsEveryGoIntegerType(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	cases := []struct {
		name  string
		value any
		want  int
	}{
		{"int", int(-1), -1},
		{"int8", int8(-8), -8},
		{"int16", int16(-16), -16},
		{"int32", int32(-32), -32},
		{"int64", int64(-64), -64},
		{"uint", uint(1), 1},
		{"uint8", uint8(8), 8},
		{"uint16", uint16(16), 16},
		{"uint32", uint32(32), 32},
		{"uint64", uint64(64), 64},
		{"uintptr", uintptr(9), 9},
		{"uint64 overflow saturates", ^uint64(0), maxInt},
		{"JSON integer", json.Number("42"), 42},
		{"JSON float truncates", json.Number("-4.9"), -4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pyIntOf(tc.value)
			if !ok || got != tc.want {
				t.Fatalf("pyIntOf(%T(%v)) = (%d, %v), want (%d, true)", tc.value, tc.value, got, ok, tc.want)
			}
		})
	}
}

func TestPyIntOfOrTreatsEveryIntegerZeroAsFalsy(t *testing.T) {
	zeros := []any{
		int(0), int8(0), int16(0), int32(0), int64(0),
		uint(0), uint8(0), uint16(0), uint32(0), uint64(0), uintptr(0),
		json.Number("0"), json.Number("0.0"),
	}
	for _, value := range zeros {
		got, ok := pyIntOfOr(value, 73)
		if !ok || got != 73 {
			t.Fatalf("pyIntOfOr(%T(0), 73) = (%d, %v), want (73, true)", value, got, ok)
		}
	}
}

func TestFingerprintFromStorage(t *testing.T) {
	fp := sampleFP()
	payload, _ := FingerprintSavePayload(&fp)

	if got := FingerprintFromStorage(payload); !FingerprintsEqual(got, &fp) {
		t.Fatalf("map path: got %+v", got)
	}
	if got := FingerprintFromStorage(fp); !FingerprintsEqual(got, &fp) {
		t.Fatalf("value path: got %+v", got)
	}
	if got := FingerprintFromStorage(&fp); !FingerprintsEqual(got, &fp) {
		t.Fatalf("pointer path: got %+v", got)
	}
	// Copies, not aliases.
	if got := FingerprintFromStorage(&fp); got == &fp {
		t.Fatal("pointer path must copy")
	}

	for _, bad := range []any{
		nil,
		(*DeviceFingerprint)(nil),
		"a string",
		42,
		map[string]any{"locale": "en-US"},
		DeviceFingerprint{},
	} {
		if got := FingerprintFromStorage(bad); got != nil {
			t.Fatalf("FingerprintFromStorage(%#v) = %+v, want nil", bad, got)
		}
	}
}

// ---------------------------------------------------------------------------
// equality / apply
// ---------------------------------------------------------------------------

func TestFingerprintsEqual(t *testing.T) {
	a := sampleFP()
	b := sampleFP()
	if !FingerprintsEqual(&a, &b) {
		t.Fatal("identical fingerprints must be equal")
	}
	if !FingerprintsEqual(nil, nil) {
		t.Fatal("nil == nil (Python: {} == {})")
	}
	if FingerprintsEqual(nil, &a) || FingerprintsEqual(&a, nil) {
		t.Fatal("nil must differ from a real fingerprint")
	}
	// nil vs empty slice are the same list in Python terms.
	c := sampleFP()
	c.Languages = nil
	d := sampleFP()
	d.Languages = []string{}
	if !FingerprintsEqual(&c, &d) {
		t.Fatal("nil and empty Languages must compare equal")
	}

	for _, mut := range []func(*DeviceFingerprint){
		func(f *DeviceFingerprint) { f.UserAgent += "x" },
		func(f *DeviceFingerprint) { f.Locale = "en-US" },
		func(f *DeviceFingerprint) { f.Languages = []string{"ja-JP"} },
		func(f *DeviceFingerprint) { f.Languages = []string{"ja", "ja-JP"} },
		func(f *DeviceFingerprint) { f.Timezone = "UTC" },
		func(f *DeviceFingerprint) { f.ViewportWidth++ },
		func(f *DeviceFingerprint) { f.ViewportHeight++ },
		func(f *DeviceFingerprint) { f.ScreenWidth++ },
		func(f *DeviceFingerprint) { f.ScreenHeight++ },
		func(f *DeviceFingerprint) { f.OuterWidth++ },
		func(f *DeviceFingerprint) { f.OuterHeight++ },
		func(f *DeviceFingerprint) { f.DeviceScaleFactor = 2 },
		func(f *DeviceFingerprint) { f.HardwareConcurrency++ },
		func(f *DeviceFingerprint) { f.DeviceMemory++ },
		func(f *DeviceFingerprint) { f.Platform = "MacIntel" },
		func(f *DeviceFingerprint) { f.Vendor = "Apple" },
		func(f *DeviceFingerprint) { f.MaxTouchPoints++ },
	} {
		x := sampleFP()
		y := sampleFP()
		mut(&y)
		if FingerprintsEqual(&x, &y) {
			t.Fatalf("difference not detected: %+v", y)
		}
	}
}

func TestApplyAccountFingerprint(t *testing.T) {
	fp := sampleFP()

	if ApplyAccountFingerprint(nil, &fp) {
		t.Fatal("nil account must return false (app.py:21002)")
	}
	if ApplyAccountFingerprint(&MailAccount{}, nil) {
		t.Fatal("nil fingerprint must return false (app.py:21005)")
	}
	if ApplyAccountFingerprint(&MailAccount{}, &DeviceFingerprint{Locale: "x"}) {
		t.Fatal("malformed fingerprint must return false")
	}

	acc := &MailAccount{Email: "a@b.c"}
	if !ApplyAccountFingerprint(acc, &fp) {
		t.Fatal("first write must report a change")
	}
	if !FingerprintsEqual(acc.BrowserFingerprint, &fp) {
		t.Fatalf("account not updated: %+v", acc.BrowserFingerprint)
	}
	if acc.BrowserFingerprint == &fp {
		t.Fatal("account must hold a copy, not an alias")
	}

	// Re-applying the same value is a no-op: no state.json rewrite.
	same := sampleFP()
	if ApplyAccountFingerprint(acc, &same) {
		t.Fatal("identical write must report no change (app.py:21008-21009)")
	}

	// A different value replaces it.
	other := sampleFP()
	other.Locale = "en-US"
	if !ApplyAccountFingerprint(acc, &other) {
		t.Fatal("different write must report a change")
	}
	if acc.BrowserFingerprint.Locale != "en-US" {
		t.Fatalf("locale = %q", acc.BrowserFingerprint.Locale)
	}
}

// The full loop: generate -> persist -> reload from state.json -> reuse. The
// second run must get byte-identical device parameters.
func TestPersistenceLoopIsStable(t *testing.T) {
	acc := &MailAccount{Email: "a@b.c", ClientID: "cid", RefreshToken: "rt"}

	// Run 1: nothing stored, generate + persist.
	first := ResolveAccountFingerprint(acc, nil)
	if first.Reused {
		t.Fatal("run 1 must generate")
	}
	if !ApplyAccountFingerprint(acc, &first.Fingerprint) {
		t.Fatal("run 1 must persist")
	}

	// Write to state.json and read it back.
	blob, err := json.Marshal(AccountToMap(*acc))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	reloaded := AccountFromMap(decoded)

	// Run 2: must reuse, with no drift and no re-write.
	second := ResolveAccountFingerprint(&reloaded, func() DeviceFingerprint {
		t.Fatal("run 2 regenerated a fingerprint that was already stored")
		return DeviceFingerprint{}
	})
	if !second.Reused {
		t.Fatal("run 2 must reuse")
	}
	if !FingerprintsEqual(&first.Fingerprint, &second.Fingerprint) {
		t.Fatalf("fingerprint drifted across runs:\nrun1 %+v\nrun2 %+v", first.Fingerprint, second.Fingerprint)
	}
	if ApplyAccountFingerprint(&reloaded, &second.Fingerprint) {
		t.Fatal("run 2 must not rewrite state.json")
	}
}
