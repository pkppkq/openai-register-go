package settings

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// The load-bearing property: never drop a key we do not model.
// ---------------------------------------------------------------------------

func TestRoundTripPreservesUnknownKeys(t *testing.T) {
	prior := map[string]any{
		"updated_at":     "2026-07-26T12:00:00",
		"schema_version": float64(2),
		"accounts":       []any{map[string]any{"email": "a@b.c", "group": "team"}},
		"session_results": map[string]any{
			"a@b.c": map[string]any{"access_token": "tok"},
		},
		// A key no version of this program has ever heard of.
		"future_top_level_key": map[string]any{"nested": []any{float64(1), "two"}},
		"settings": map[string]any{
			"headless": true,
			// Unknown settings keys — the Python app may already write these.
			"future_setting":       "keep me",
			"another_future_thing": []any{float64(1), float64(2)},
			"provider_proxy_configs": map[string]any{
				"create": map[string]any{
					"enabled": true, "username": "u", "password": "p",
					"endpoint": "h:1", "duration": float64(7), "regions": "JP US",
					"future_role_field": "keep me too",
				},
				"future_role": map[string]any{"enabled": true},
			},
		},
	}

	s := FromSnapshot(prior)
	out := ToSnapshot(s, prior)

	// Top level.
	for _, key := range []string{"updated_at", "schema_version", "accounts", "session_results", "future_top_level_key"} {
		if !reflect.DeepEqual(out[key], prior[key]) {
			t.Fatalf("top-level %q not preserved: got %#v want %#v", key, out[key], prior[key])
		}
	}

	// Settings level.
	priorSettings := prior["settings"].(map[string]any)
	outSettings, ok := out["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings is not a map: %#v", out["settings"])
	}
	if !reflect.DeepEqual(outSettings["future_setting"], priorSettings["future_setting"]) {
		t.Fatalf("unknown settings key dropped: %#v", outSettings["future_setting"])
	}
	if !reflect.DeepEqual(outSettings["another_future_thing"], priorSettings["another_future_thing"]) {
		t.Fatalf("unknown settings list dropped: %#v", outSettings["another_future_thing"])
	}

	// Provider-proxy level.
	providers := outSettings["provider_proxy_configs"].(map[string]any)
	if _, ok := providers["future_role"]; !ok {
		t.Fatal("unknown provider role dropped")
	}
	create := providers["create"].(map[string]any)
	if create["future_role_field"] != "keep me too" {
		t.Fatalf("unknown provider field dropped: %#v", create["future_role_field"])
	}

	// Prior must not have been mutated.
	if len(priorSettings) != 4 {
		t.Fatalf("prior snapshot was mutated: %#v", priorSettings)
	}

	// And every one of the 60 modelled keys must be present on the way out.
	for _, key := range ModelledKeys {
		if _, ok := outSettings[key]; !ok {
			t.Fatalf("modelled key %q missing from output", key)
		}
	}
	if len(ModelledKeys) != 60 {
		t.Fatalf("UI_SPEC §3 says 60 persisted keys, ModelledKeys has %d", len(ModelledKeys))
	}
}

func TestToSnapshotWithNilPrior(t *testing.T) {
	out := ToSnapshot(Defaults(), nil)
	sm, ok := out["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing: %#v", out)
	}
	if len(sm) != len(ModelledKeys) {
		t.Fatalf("want %d keys, got %d", len(ModelledKeys), len(sm))
	}
	// Must be JSON-encodable without surprises.
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Boolean wire-type quirks
// ---------------------------------------------------------------------------

func TestBooleansAcceptBothWireTypes(t *testing.T) {
	cases := []struct {
		raw  any
		want bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"false", false}, // Python's bool("false") would be True; we diverge on purpose
		{"True", true},
		{"False", false},
		{"TRUE", true},
		{"1", true},
		{"0", false},
		{"", false},
		{nil, false},
		{float64(0), false},
		{float64(1), true},
	}
	for _, tc := range cases {
		s := FromSnapshot(map[string]any{"settings": map[string]any{"headless": tc.raw}})
		if s.Headless != tc.want {
			t.Errorf("headless=%#v: got %v want %v", tc.raw, s.Headless, tc.want)
		}
	}
}

// REGRESSION (defect: string boolean write-back). ToSnapshot used to echo back
// whatever spelling the key already had on disk — a string prior "true" made it
// write the STRING "false" when the box was unchecked. app.py always writes
// bool(...) (app.py:14237, 14248-14250, 14268, 14274, 14279-14280, 14283, 14285,
// 14293) and reads with bool(value), and Python's bool("false") is True: the
// echoed spelling silently re-enabled the setting the next time the Tk app —
// which still shares this file — loaded it. Every boolean now goes out as a
// real JSON bool, exactly as Python writes it.
func TestBooleanWriteBackIsAlwaysARealBool(t *testing.T) {
	priors := []any{nil, true, false, "true", "false", "True", "False", "TRUE",
		"1", "0", "yes", "no", "garbage", float64(1), float64(0)}
	for _, prior := range priors {
		for _, set := range []bool{true, false} {
			p := map[string]any{"settings": map[string]any{}}
			if prior != nil {
				p["settings"].(map[string]any)["success_sound_enabled"] = prior
			}
			s := Defaults()
			s.SuccessSoundEnabled = set
			got := ToSnapshot(s, p)["settings"].(map[string]any)["success_sound_enabled"]
			if !reflect.DeepEqual(got, set) {
				t.Errorf("prior=%#v set=%v: got %#v (%T), want the bool %v",
					prior, set, got, got, set)
			}
		}
	}
}

func TestProviderEnabledWriteBackIsARealBool(t *testing.T) {
	prior := map[string]any{"settings": map[string]any{
		"provider_proxy_configs": map[string]any{
			"create": map[string]any{"enabled": "True"},
		},
	}}
	s := FromSnapshot(prior)
	if !s.ProviderProxyConfigs["create"].Enabled {
		t.Fatal(`string "True" should read as enabled`)
	}
	out := ToSnapshot(s, prior)["settings"].(map[string]any)["provider_proxy_configs"].(map[string]any)
	if got := out["create"].(map[string]any)["enabled"]; got != true {
		t.Fatalf("got %#v want the bool true", got)
	}
	if got := out["followup"].(map[string]any)["enabled"]; got != false {
		t.Fatalf("untouched role should use a real bool, got %#v", got)
	}
}

// REGRESSION (defect: str(int) rendered "20.0"). encoding/json turns every JSON
// number into float64, and pyStr used to format integral float64 with a forced
// ".0". Python's json keeps ints, so str(20) is "20" — a state.json holding
// "target_amount": 20 loaded as the string "20.0" and was then SAVED back as
// "20.0", permanently corrupting the amount.
func TestPyStrOfWholeJSONNumberHasNoDotZero(t *testing.T) {
	cases := []struct {
		raw  any
		want string
	}{
		{float64(20), "20"},
		{float64(0), "0"},
		{float64(-3), "-3"},
		{float64(3.5), "3.5"},
		{float64(0.1), "0.1"},
		{float64(1e16), "1e+16"},
	}
	for _, tc := range cases {
		s := FromSnapshot(map[string]any{"settings": map[string]any{"target_amount": tc.raw}})
		if s.TargetAmount != tc.want {
			t.Errorf("target_amount=%v: got %q want %q", tc.raw, s.TargetAmount, tc.want)
		}
	}
}

// REGRESSION (defect: int()/float() rejected Unicode decimal digits). CPython
// folds every Nd codepoint to ASCII before parsing, so int("０７") is 7; strconv
// rejects it and the key silently fell back to its default.
func TestUnicodeDecimalDigitsParseLikePython(t *testing.T) {
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"auth_concurrency":        "０７",  // fullwidth 07
		"phone_max_receive_count": "٤",   // Arabic-Indic 4
		"link_attempt_limit":      "-１２", // fullwidth -12, clamps up to 1
		"main_sash_ratio":         "０.５",
		"ui_layout_version":       UILayoutVersion,
	}})
	if s.AuthConcurrency != 7 {
		t.Errorf("auth_concurrency: got %d want 7", s.AuthConcurrency)
	}
	if s.PhoneMaxReceiveCount != 4 {
		t.Errorf("phone_max_receive_count: got %d want 4", s.PhoneMaxReceiveCount)
	}
	if s.LinkAttemptLimit != 1 {
		t.Errorf("link_attempt_limit: got %d want 1", s.LinkAttemptLimit)
	}
	if s.MainSashRatio != 0.5 {
		t.Errorf("main_sash_ratio: got %v want 0.5", s.MainSashRatio)
	}
	// Python's int() raises on Numeric_Type=Digit characters outside Nd, so the
	// fallback (not 2) is the faithful answer.
	s = FromSnapshot(map[string]any{"settings": map[string]any{"auth_concurrency": "²"}})
	if s.AuthConcurrency != DefaultAuthConcurrency {
		t.Errorf(`auth_concurrency="²": got %d want the default %d`, s.AuthConcurrency, DefaultAuthConcurrency)
	}
}

// REGRESSION (defect: providerRegionSplitRe missed U+001C..U+001F). Python's
// `\s` in a str pattern is exactly str.isspace(), which includes the four C0
// information separators; RE2's `\s` omits them (and \v).
func TestProviderRegionSplitClass(t *testing.T) {
	for _, sep := range []string{"\x1c", "\x1d", "\x1e", "\x1f", "\v", "",
		" ", "　", " ", " ", "\t", ",", "，", ";", "；"} {
		got, err := ParseProviderRegions("us" + sep + "jp")
		if err != nil {
			t.Errorf("separator %q: %v", sep, err)
			continue
		}
		if !reflect.DeepEqual(got, []string{"US", "JP"}) {
			t.Errorf("separator %q: got %v", sep, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Defaults and per-key clamps / validators (UI_SPEC §3)
// ---------------------------------------------------------------------------

func TestEmptySnapshotYieldsDefaults(t *testing.T) {
	got := FromSnapshot(nil)
	want := Defaults()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromSnapshot(nil) != Defaults()\n got %+v\nwant %+v", got, want)
	}
	// A settings object present but empty is the same thing.
	got = FromSnapshot(map[string]any{"settings": map[string]any{}})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty settings != Defaults()")
	}
}

func TestIntClamps(t *testing.T) {
	cases := []struct {
		key  string
		raw  any
		want int
		get  func(Settings) int
	}{
		// `DEFAULT if raw in ("", None) else int(raw)` then clamp 1..30.
		{"auth_concurrency", float64(0), 1, func(s Settings) int { return s.AuthConcurrency }},
		{"auth_concurrency", "", DefaultAuthConcurrency, func(s Settings) int { return s.AuthConcurrency }},
		{"auth_concurrency", nil, DefaultAuthConcurrency, func(s Settings) int { return s.AuthConcurrency }},
		{"auth_concurrency", float64(999), 30, func(s Settings) int { return s.AuthConcurrency }},
		{"auth_concurrency", "17", 17, func(s Settings) int { return s.AuthConcurrency }},
		// app.py clamps k12 to MAX_AUTH_CONCURRENCY even though UI_SPEC is silent.
		{"k12_concurrency", float64(999), 30, func(s Settings) int { return s.K12Concurrency }},
		{"k12_concurrency", float64(0), 1, func(s Settings) int { return s.K12Concurrency }},
		// `int(raw or 1)` — 0 is falsy, so it becomes 1 (not clamped from 0).
		{"link_race_concurrency", float64(0), 1, func(s Settings) int { return s.LinkRaceConcurrency }},
		{"link_race_concurrency", float64(50), 30, func(s Settings) int { return s.LinkRaceConcurrency }},
		{"link_proxy_precheck_limit", float64(0), DefaultLinkProxyPrecheckLimit, func(s Settings) int { return s.LinkProxyPrecheckLimit }},
		{"link_proxy_precheck_limit", float64(-5), 1, func(s Settings) int { return s.LinkProxyPrecheckLimit }},
		{"link_proxy_precheck_concurrency", float64(9999), 300, func(s Settings) int { return s.LinkProxyPrecheckConcurrency }},
		{"link_proxy_precheck_concurrency", float64(0), 1, func(s Settings) int { return s.LinkProxyPrecheckConcurrency }},
		{"link_attempt_limit", float64(99999), 10000, func(s Settings) int { return s.LinkAttemptLimit }},
		{"link_attempt_limit", float64(0), 1, func(s Settings) int { return s.LinkAttemptLimit }},
		{"phone_max_receive_count", float64(-3), 0, func(s Settings) int { return s.PhoneMaxReceiveCount }},
		{"paypal_phone_pool_index", float64(-1), 0, func(s Settings) int { return s.PaypalPhonePoolIndex }},
	}
	for _, tc := range cases {
		s := FromSnapshot(map[string]any{"settings": map[string]any{tc.key: tc.raw}})
		if got := tc.get(s); got != tc.want {
			t.Errorf("%s=%#v: got %d want %d", tc.key, tc.raw, got, tc.want)
		}
	}
}

func TestSashRatiosGatedOnLayoutVersion(t *testing.T) {
	sm := map[string]any{
		"ui_layout_version": float64(3),
		"main_sash_ratio":   0.7,
		"log_sash_ratio":    0.71,
		"body_sash_ratio":   0.72,
	}
	s := FromSnapshot(map[string]any{"settings": sm})
	if s.MainSashRatio != DefaultMainSashRatio || s.LogSashRatio != DefaultLogSashRatio || s.BodySashRatio != DefaultBodySashRatio {
		t.Fatalf("stale layout version must discard saved ratios: %v %v %v",
			s.MainSashRatio, s.LogSashRatio, s.BodySashRatio)
	}

	sm["ui_layout_version"] = float64(4)
	s = FromSnapshot(map[string]any{"settings": sm})
	if s.MainSashRatio != 0.7 {
		t.Fatalf("current layout version must keep saved ratios, got %v", s.MainSashRatio)
	}

	// Clamps.
	sm["main_sash_ratio"] = 0.99
	sm["log_sash_ratio"] = 0.01
	s = FromSnapshot(map[string]any{"settings": sm})
	if s.MainSashRatio != MaxMainSashRatio {
		t.Fatalf("main clamp: got %v", s.MainSashRatio)
	}
	if s.LogSashRatio != MinLogSashRatio {
		t.Fatalf("log clamp: got %v", s.LogSashRatio)
	}

	// ui_layout_version is always written back as the current constant.
	out := ToSnapshot(s, nil)["settings"].(map[string]any)
	if out["ui_layout_version"] != UILayoutVersion {
		t.Fatalf("ui_layout_version: got %#v", out["ui_layout_version"])
	}
}

func TestEnumFallbacks(t *testing.T) {
	sm := map[string]any{
		"payment_mode":           "nonsense",
		"proxy_route_mode":       "nonsense",
		"link_proxy_region":      "nonsense",
		"session_convert_format": "NONSENSE",
		"workspace_page":         "nonsense",
		"account_sort_column":    "nonsense",
		"account_sort_direction": "nonsense",
		"account_status_filter":  "nonsense",
		"account_group_filter":   "nonsense",
	}
	s := FromSnapshot(map[string]any{"settings": sm})
	if s.PaymentMode != "无卡长链接 US/USD" {
		t.Errorf("payment_mode: %q", s.PaymentMode)
	}
	if s.ProxyRouteMode != ProxyRouteModeDefault {
		t.Errorf("proxy_route_mode: %q", s.ProxyRouteMode)
	}
	if s.LinkProxyRegion != LinkProxyRegionAny {
		t.Errorf("link_proxy_region: %q", s.LinkProxyRegion)
	}
	if s.SessionConvertFormat != DefaultSessionConvertFormat {
		t.Errorf("session_convert_format: %q", s.SessionConvertFormat)
	}
	if s.WorkspacePage != DefaultWorkspacePage {
		t.Errorf("workspace_page: %q", s.WorkspacePage)
	}
	if s.AccountSortColumn != "email" || s.AccountSortDirection != AccountSortCustom {
		t.Errorf("sort: %q %q", s.AccountSortColumn, s.AccountSortDirection)
	}
	if s.AccountStatusFilter != AccountStatusFilterAll {
		t.Errorf("status filter: %q", s.AccountStatusFilter)
	}
	if s.AccountGroupFilter != AccountAllGroup {
		t.Errorf("group filter: %q", s.AccountGroupFilter)
	}

	// Valid values survive; session_convert_format is lower-cased first.
	sm["payment_mode"] = "Apple Pay 支付页 JP/JPY"
	sm["proxy_route_mode"] = "  " + ProxyRouteModeLocalOnly + "  " // stripped on load
	sm["link_proxy_region"] = "JP 日本"
	sm["session_convert_format"] = " CPA "
	sm["workspace_page"] = "settings"
	sm["account_sort_column"] = "attempts"
	sm["account_sort_direction"] = "desc"
	s = FromSnapshot(map[string]any{"settings": sm})
	if s.PaymentMode != "Apple Pay 支付页 JP/JPY" || s.ProxyRouteMode != ProxyRouteModeLocalOnly ||
		s.LinkProxyRegion != "JP 日本" || s.SessionConvertFormat != "cpa" ||
		s.WorkspacePage != "settings" || s.AccountSortColumn != "attempts" ||
		s.AccountSortDirection != "desc" {
		t.Fatalf("valid enum values not kept: %+v", s)
	}
}

func TestPaymentModeAliasRemap(t *testing.T) {
	// PAYMENT_MODE_ALIASES: "长链接" → "短链" (app.py:490).
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"payment_mode": "PayPal 短链 US/USD",
	}})
	if s.PaymentMode != "PayPal 长链接 US/USD" {
		t.Fatalf("alias not remapped: %q", s.PaymentMode)
	}
	if len(PaymentModeAliases) == 0 {
		t.Fatal("alias table is empty")
	}
}

func TestReuseFollowupSeededFromPaymentProxy(t *testing.T) {
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"reuse_payment_proxy": "http://a",
	}})
	if s.ReuseFollowupProxy != "http://a" {
		t.Fatalf("absent reuse_followup_proxy should be seeded, got %q", s.ReuseFollowupProxy)
	}
	// Present-but-empty must NOT be seeded.
	s = FromSnapshot(map[string]any{"settings": map[string]any{
		"reuse_payment_proxy":  "http://a",
		"reuse_followup_proxy": "",
	}})
	if s.ReuseFollowupProxy != "" {
		t.Fatalf("present empty key must win, got %q", s.ReuseFollowupProxy)
	}
}

func TestDomainMailDomainAlwaysForced(t *testing.T) {
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"domain_mail_domain": "attacker.example",
	}})
	if s.DomainMailDomain != DefaultDomainMailDomain {
		t.Fatalf("load: %q", s.DomainMailDomain)
	}
	s.DomainMailDomain = "attacker.example"
	out := ToSnapshot(s, nil)["settings"].(map[string]any)
	if out["domain_mail_domain"] != DefaultDomainMailDomain {
		t.Fatalf("save: %#v", out["domain_mail_domain"])
	}
}

func TestStringDefaultsAndAsymmetries(t *testing.T) {
	// Empty strings fall back to their constants on LOAD...
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"payment_extension_dir": "   ",
		"k12_workspace_id":      "",
		"cloud_mail_base":       "",
		"smsbower_service":      "",
		"smsbower_country":      "",
		"smsbower_max_price":    "",
		"turnstile_solver_url":  "",
		"success_audio_device":  "",
	}})
	if s.PaypalExtensionDir != DefaultPaypalExtensionDir ||
		s.K12WorkspaceID != DefaultK12WorkspaceID ||
		s.CloudMailBase != DefaultCloudMailBase ||
		s.SMSBowerService != SMSBowerDefaultService ||
		s.SMSBowerCountry != SMSBowerDefaultCountry ||
		s.SMSBowerMaxPrice != SMSBowerDefaultMaxPrice ||
		s.TurnstileSolverURL != TurnstileSolverDefaultURL ||
		s.SuccessAudioDevice != AudioDefaultDeviceLabel {
		t.Fatalf("load defaults not applied: %+v", s)
	}

	// ...but on SAVE app.py:14261/14271/14278 write the empty string through
	// for payment_extension_dir, k12_workspace_id and smsbower_max_price.
	blank := Defaults()
	blank.PaypalExtensionDir = ""
	blank.K12WorkspaceID = ""
	blank.SMSBowerMaxPrice = ""
	blank.CloudMailBase = ""
	out := ToSnapshot(blank, nil)["settings"].(map[string]any)
	if out["payment_extension_dir"] != "" {
		t.Errorf("payment_extension_dir: %#v", out["payment_extension_dir"])
	}
	if out["k12_workspace_id"] != "" {
		t.Errorf("k12_workspace_id: %#v", out["k12_workspace_id"])
	}
	if out["smsbower_max_price"] != "" {
		t.Errorf("smsbower_max_price: %#v", out["smsbower_max_price"])
	}
	// cloud_mail_base DOES fall back on save (app.py:14269).
	if out["cloud_mail_base"] != DefaultCloudMailBase {
		t.Errorf("cloud_mail_base: %#v", out["cloud_mail_base"])
	}
}

func TestBareStrOfNullMatchesPython(t *testing.T) {
	// app.py:14085 does str(settings["target_amount"]) with no `or ""` guard,
	// so a JSON null becomes the literal "None". Faithful port.
	s := FromSnapshot(map[string]any{"settings": map[string]any{"target_amount": nil}})
	if s.TargetAmount != "None" {
		t.Fatalf(`want "None", got %q`, s.TargetAmount)
	}
	// Whereas keys read as str(x or "") heal to "".
	s = FromSnapshot(map[string]any{"settings": map[string]any{"cloud_mail_token": nil}})
	if s.CloudMailToken != "" {
		t.Fatalf(`want "", got %q`, s.CloudMailToken)
	}
}

func TestTargetAmountStrippedOnSaveOnly(t *testing.T) {
	s := FromSnapshot(map[string]any{"settings": map[string]any{"target_amount": "  5 "}})
	if s.TargetAmount != "  5 " {
		t.Fatalf("load must not strip: %q", s.TargetAmount)
	}
	out := ToSnapshot(s, nil)["settings"].(map[string]any)
	if out["target_amount"] != "5" {
		t.Fatalf("save must strip: %#v", out["target_amount"])
	}
	// local_proxy is deliberately NOT stripped on save (app.py:14238).
	s.LocalProxy = " http://x "
	out = ToSnapshot(s, nil)["settings"].(map[string]any)
	if out["local_proxy"] != " http://x " {
		t.Fatalf("local_proxy must not be stripped: %#v", out["local_proxy"])
	}
}

func TestPyStripHandlesUnicodeWhitespace(t *testing.T) {
	// Go's strings.TrimSpace would already cover U+3000; U+001F would not be.
	if got := pyStrip("\u3000\u00a0 x \u001f"); got != "x" {
		t.Fatalf("got %q", got)
	}
}

// ---------------------------------------------------------------------------
// account_groups
// ---------------------------------------------------------------------------

func TestAccountGroupsMergeAndDedup(t *testing.T) {
	snapshot := map[string]any{
		"accounts": []any{
			map[string]any{"email": "a@b.c", "group": "onlyOnAccount"},
			map[string]any{"email": "d@e.f", "group": "  "}, // → 未分组
			map[string]any{"email": "g@h.i"},                // missing → 未分组
		},
		"settings": map[string]any{
			"account_groups": []any{
				"team", " team ", "TEAM", AccountAllGroup, "", AccountDefaultGroup, "plus",
			},
			"account_group_filter": "onlyOnAccount",
		},
	}
	s := FromSnapshot(snapshot)
	want := []string{AccountDefaultGroup, "team", "plus", "onlyOnAccount"}
	if !reflect.DeepEqual(s.AccountGroups, want) {
		t.Fatalf("got %#v want %#v", s.AccountGroups, want)
	}
	// The filter is validated against the POST-merge list, so a group that only
	// exists on an account must survive.
	if s.AccountGroupFilter != "onlyOnAccount" {
		t.Fatalf("group filter reset to %q", s.AccountGroupFilter)
	}
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

func TestParseProviderRegionsUnicodeSeparators(t *testing.T) {
	// Go's RE2 \s is ASCII-only; Python's is not. U+3000 (ideographic space)
	// and U+00A0 (nbsp) must still split.
	got, err := ParseProviderRegions(" jp\u3000us,\u00a0br;JP ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"JP", "US", "BR"} // upper-cased, first-seen order, de-duped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}

	if _, err := ParseProviderRegions("USA"); err == nil {
		t.Fatal("three letters must be rejected")
	}
	if _, err := ParseProviderRegions("   "); err == nil {
		t.Fatal("empty must be rejected")
	}
	// Full-width comma and semicolon are separators too (app.py:943).
	if got, err := ParseProviderRegions("JP，US；DE"); err != nil || len(got) != 3 {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestProviderProxyConfigValidate(t *testing.T) {
	base := ProviderProxyConfig{
		Enabled: true, Username: "u", Password: "p",
		Endpoint: "us2.proxy.invalid:3010", Duration: 5, Regions: "JP",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Disabled roles are never validated, even when garbage.
	bad := ProviderProxyConfig{Enabled: false, Duration: 9999}
	if err := bad.Validate(); err != nil {
		t.Fatalf("disabled role must not be validated: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(c *ProviderProxyConfig)
	}{
		{"no username", func(c *ProviderProxyConfig) { c.Username = "  " }},
		{"no password", func(c *ProviderProxyConfig) { c.Password = "" }},
		{"no port", func(c *ProviderProxyConfig) { c.Endpoint = "us2.proxy.invalid" }},
		{"has path", func(c *ProviderProxyConfig) { c.Endpoint = "us2.proxy.invalid:3010/x" }},
		{"has creds", func(c *ProviderProxyConfig) { c.Endpoint = "a:b@us2.proxy.invalid:3010" }},
		{"duration low", func(c *ProviderProxyConfig) { c.Duration = 0 }},
		{"duration high", func(c *ProviderProxyConfig) { c.Duration = 121 }},
		{"bad region", func(c *ProviderProxyConfig) { c.Regions = "JPN" }},
	} {
		c := base
		tc.mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

func TestProviderDurationClampedOnLoad(t *testing.T) {
	s := FromSnapshot(map[string]any{"settings": map[string]any{
		"provider_proxy_configs": map[string]any{
			"create":   map[string]any{"duration": float64(999)},
			"followup": map[string]any{"duration": float64(0)}, // falsy → 5
			"approve":  map[string]any{"duration": "  7 "},
		},
	}})
	if got := s.ProviderProxyConfigs["create"].Duration; got != MaxProviderProxyDuration {
		t.Errorf("create: %d", got)
	}
	if got := s.ProviderProxyConfigs["followup"].Duration; got != DefaultProviderProxyDuration {
		t.Errorf("followup: %d", got)
	}
	if got := s.ProviderProxyConfigs["approve"].Duration; got != 7 {
		t.Errorf("approve: %d", got)
	}
	// Absent regions default to "JP".
	if got := s.ProviderProxyConfigs["create"].Regions; got != DefaultProviderProxyRegions {
		t.Errorf("regions: %q", got)
	}
}

func TestValidateSMSBower(t *testing.T) {
	s := Defaults()
	if err := s.ValidateSMSBower(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	s.SMSBowerMaxPrice = "" // empty = no cap
	if err := s.ValidateSMSBower(); err != nil {
		t.Fatalf("empty max price must be allowed: %v", err)
	}
	s.SMSBowerMaxPrice = "0"
	if err := s.ValidateSMSBower(); err == nil {
		t.Error("zero max price must be rejected")
	}
	s.SMSBowerMaxPrice = "abc"
	if err := s.ValidateSMSBower(); err == nil {
		t.Error("non-numeric max price must be rejected")
	}
	s = Defaults()
	s.SMSBowerService = "bad-service"
	if err := s.ValidateSMSBower(); err == nil {
		t.Error("hyphen must be rejected by [A-Za-z0-9_]+")
	}
	s = Defaults()
	s.SMSBowerCountry = "33a"
	if err := s.ValidateSMSBower(); err == nil {
		t.Error("non-digit country must be rejected")
	}
	s = Defaults()
	s.SMSBowerEnabled = true
	if err := s.ValidateSMSBower(); err == nil {
		t.Error("enabled without an API key must be rejected")
	}
}

func TestValidateCloudMail(t *testing.T) {
	s := Defaults()
	if err := s.ValidateCloudMail(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	s.CloudMailBase = "ftp://x"
	if err := s.ValidateCloudMail(); err == nil {
		t.Error("non-http scheme must be rejected")
	}
	s.CloudMailBase = "HTTPS://EXAMPLE.COM/api/"
	base, err := s.CloudMailBaseURL()
	if err != nil {
		t.Fatalf("case-insensitive scheme: %v", err)
	}
	if base != "HTTPS://EXAMPLE.COM/api" {
		t.Errorf("trailing slash must be stripped, got %q", base)
	}
	s = Defaults()
	s.CloudMailEnabled = true
	if err := s.ValidateCloudMail(); err == nil {
		t.Error("enabled without a token must be rejected")
	}
}

// ---------------------------------------------------------------------------
// Determinism (Go map iteration is random where Python dicts are ordered)
// ---------------------------------------------------------------------------

func TestOrderedTablesAreStable(t *testing.T) {
	first := buildLinkProxyRegionOptions()
	for i := 0; i < 50; i++ {
		if !reflect.DeepEqual(buildLinkProxyRegionOptions(), first) {
			t.Fatal("LinkProxyRegionOptions is not deterministic")
		}
	}
	// Determinism alone is worthless if the fixed order is the wrong one: this
	// is the literal value of LINK_PROXY_REGION_OPTIONS (app.py:516-520), which
	// is built by iterating the LINK_PROXY_REGION_NAMES dict in insertion order.
	// Printed from the running interpreter, not transcribed from the source.
	wantRegions := []string{
		"自动(跟随支付地区)", "不限",
		"US 美国", "BR 巴西", "JP 日本", "NL 荷兰", "DE 德国", "FR 法国", "GB 英国",
		"CA 加拿大", "AU 澳洲", "ID 印尼", "SG 新加坡", "TH 泰国", "KR 韩国",
		"TW 台湾", "HK 香港", "MX 墨西哥", "ES 西班牙", "IT 意大利", "PL 波兰",
		"SE 瑞典", "NO 挪威",
	}
	if !reflect.DeepEqual(LinkProxyRegionOptions, wantRegions) {
		t.Fatalf("region combo order diverges from app.py:\n got %#v\nwant %#v",
			LinkProxyRegionOptions, wantRegions)
	}
	if len(LinkProxyRegionOptions) != 23 { // auto + any + 21 regions
		t.Fatalf("want 23 options, got %d", len(LinkProxyRegionOptions))
	}
	if len(SessionConvertFormats) != 7 {
		t.Fatalf("UI_SPEC says 7 convert formats, got %d", len(SessionConvertFormats))
	}
	if len(AccountStatusFilterOptions) != 7 {
		t.Fatalf("UI_SPEC says 7 status filters, got %d", len(AccountStatusFilterOptions))
	}
	if len(WorkspacePages) != 9 {
		t.Fatalf("UI_SPEC says 9 workspace pages, got %d", len(WorkspacePages))
	}
}

// ---------------------------------------------------------------------------
// The user's real file, when it is reachable. No network, read-only.
// ---------------------------------------------------------------------------

// Point REAL_STATE_JSON at a COPY of a production state.json to run this.
// It must never name the live file: the Python app is still writing that one,
// and a test that opens it invites someone to make the test write it back.
func TestRoundTripAgainstRealStateJSON(t *testing.T) {
	path := os.Getenv("REAL_STATE_JSON")
	if path == "" {
		t.Skip("REAL_STATE_JSON unset (point it at a COPY of a real state.json)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("real state.json copy not reachable: %v", err)
	}
	var prior map[string]any
	if err := json.Unmarshal(raw, &prior); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	s := FromSnapshot(prior)
	out := ToSnapshot(s, prior)

	// No top-level key may vanish, and unmodelled ones must be byte-identical.
	for key, want := range prior {
		got, ok := out[key]
		if !ok {
			t.Fatalf("top-level key %q dropped", key)
		}
		if key == "settings" {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("top-level key %q changed", key)
		}
	}

	priorSettings := prior["settings"].(map[string]any)
	outSettings := out["settings"].(map[string]any)
	if len(priorSettings) != 60 {
		t.Logf("note: real file has %d settings keys, UI_SPEC §3 says 60", len(priorSettings))
	}
	modelled := make(map[string]bool, len(ModelledKeys))
	for _, k := range ModelledKeys {
		modelled[k] = true
	}
	for key, want := range priorSettings {
		got, ok := outSettings[key]
		if !ok {
			t.Fatalf("settings key %q dropped", key)
		}
		if modelled[key] {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unmodelled settings key %q changed: %#v -> %#v", key, want, got)
		}
	}
	for _, key := range ModelledKeys {
		if _, ok := priorSettings[key]; !ok {
			t.Errorf("real file is missing modelled key %q", key)
		}
	}

	// Second round trip must be a fixed point.
	again := ToSnapshot(FromSnapshot(out), out)
	a, _ := json.Marshal(out["settings"])
	b, _ := json.Marshal(again["settings"])
	if string(a) != string(b) {
		t.Fatalf("round trip is not idempotent:\n%s\n%s", a, b)
	}
}

// REGRESSION (defect: ndDigitValue walked across adjacent digit blocks, and
// ValidateSMSBower parsed the max price with a bare strconv.ParseFloat).
// Python's float() folds Unicode decimal digits to ASCII first (app.py:14376),
// so float("１.５") is 1.5 and passes the > 0 test.
func TestSMSBowerMaxPriceUnicodeDigits(t *testing.T) {
	s := Defaults()
	for _, price := range []string{"１.５", "0.07", "  0.5  ", "\U0001D7E3.\U0001D7E7"} {
		s.SMSBowerMaxPrice = price
		if err := s.ValidateSMSBower(); err != nil {
			t.Errorf("max_price=%q rejected: %v", price, err)
		}
	}
	for _, price := range []string{"０", "-1", "abc", "1,5"} {
		s.SMSBowerMaxPrice = price
		if err := s.ValidateSMSBower(); err == nil {
			t.Errorf("max_price=%q should be rejected", price)
		}
	}
	// Python: float("nan") <= 0 is False, so "nan" is accepted there too.
	s.SMSBowerMaxPrice = "nan"
	if err := s.ValidateSMSBower(); err != nil {
		t.Errorf(`"nan" is accepted by Python's float(x) <= 0 test, so it must be here: %v`, err)
	}
}

func TestNdDigitValueSpansAdjacentBlocks(t *testing.T) {
	// U+1D7CE..U+1D7FF is five ten-digit blocks with no gap between them.
	for r, want := range map[rune]int{
		0x1D7CE: 0, 0x1D7D7: 9, 0x1D7D8: 0, 0x1D7E2: 0,
		0x1D7EE: 2, 0x1D7F6: 0, 0x1D7F7: 1, 0x1D7FF: 9,
		0xFF10: 0, 0xFF19: 9, 0x0660: 0,
	} {
		if got, ok := ndDigitValue(r); !ok || got != want {
			t.Errorf("ndDigitValue(U+%04X) = %d,%v want %d", r, got, ok, want)
		}
	}
	if got := pyDecimalASCII("\U0001D7E4\U0001D7E2\U0001D7E2"); got != "200" {
		t.Errorf(`pyDecimalASCII("𝟤𝟢𝟢") = %q want "200"`, got)
	}
}

// REGRESSION (three defects in ProviderProxyConfig.Validate / ParseProviderRegions,
// all found by running app.py's ProxyProviderConfig.validated over the same
// endpoints). This validator gates a BILLABLE provider proxy, so a wrong verdict
// either blocks a working endpoint or persists one that can never connect.
func TestProviderEndpointMatchesUrlsplit(t *testing.T) {
	base := ProviderProxyConfig{Enabled: true, Username: "u", Password: "p",
		Duration: 5, Regions: "JP"}
	cases := []struct {
		endpoint string
		ok       bool
		why      string
	}{
		{"host:3010", true, ""},
		{"[::1]:3010", true, "ipv6 literal"},
		{"host:0", true, "urlsplit accepts port 0"},
		{"host:65535", true, "top of the range"},
		// urlsplit(...).port raises ValueError("Port out of range 0-65535");
		// Go's url.Parse happily returns the string "65536".
		{"host:65536", false, "port out of range"},
		{"host:99999", false, "port out of range"},
		{"host:abc", false, "non-numeric port"},
		{"host", false, "no port"},
		// app.py:983 tests `parsed.username or parsed.password` — an EMPTY
		// userinfo is falsy and passes. url.User("") is non-nil, so a bare
		// `parsed.User != nil` rejected these.
		{"@host:3010", true, "empty userinfo is falsy in Python"},
		{":@host:3010", true, "empty user and password are both falsy"},
		{"a:b@host:3010", false, "real credentials"},
		{"user@host:3010", false, "real username"},
		{"host:3010/", true, "bare slash path is allowed (app.py:983)"},
		{"host:3010/x", false, "path"},
		{"host:3010?q=1", false, "query"},
		{"host:3010#f", false, "fragment"},
	}
	for _, tc := range cases {
		c := base
		c.Endpoint = tc.endpoint
		err := c.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("endpoint %q: ok=%v want %v (%s) err=%v", tc.endpoint, err == nil, tc.ok, tc.why, err)
		}
	}
}

// REGRESSION: Python's str.upper() is FULL case mapping, so "ﬁ".upper() is "FI"
// — a valid region code — while strings.ToUpper leaves the ligature alone and
// the two-letter test then failed.
func TestParseProviderRegionsUsesFullUppercase(t *testing.T) {
	for input, want := range map[string][]string{
		"ﬁ":     {"FI"},
		"ß":     {"SS"},
		"ﬂ ﬅ":   {"FL", "ST"},
		"jp ﬁ":  {"JP", "FI"},
		"ﬁ, FI": {"FI"}, // de-duplicated after upper-casing
	} {
		got, err := ParseProviderRegions(input)
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", input, got, want)
		}
	}
	// ﬃ upper-cases to "FFI" — three letters, rejected by Python too.
	if _, err := ParseProviderRegions("ﬃ"); err == nil {
		t.Error("ﬃ → FFI must be rejected")
	}
}

// REGRESSION (defect: a NaN sash ratio escaped the clamp and broke the save).
// app.py:14044's min(0.85, max(0.2, v)) yields 0.2 for NaN, because Python's
// max returns the first argument when `v > lo` is false. Go's `if v < lo` /
// `if v > hi` are both false for NaN, so NaN reached the snapshot and
// json.Marshal failed with "unsupported value: NaN" — the whole state.json save
// aborted, not just this key.
func TestSashRatioNaNIsClampedAndMarshalable(t *testing.T) {
	cases := map[string]float64{
		"nan": MinMainSashRatio, "NaN": MinMainSashRatio,
		"inf": MaxMainSashRatio, "-inf": MinMainSashRatio,
		"0.5": 0.5,
	}
	for raw, want := range cases {
		s := FromSnapshot(map[string]any{"settings": map[string]any{
			"ui_layout_version": float64(UILayoutVersion),
			"main_sash_ratio":   raw,
			"log_sash_ratio":    raw,
			"body_sash_ratio":   raw,
		}})
		if s.MainSashRatio != want {
			t.Errorf("main_sash_ratio=%q: got %v want %v", raw, s.MainSashRatio, want)
		}
		if _, err := json.Marshal(ToSnapshot(s, nil)); err != nil {
			t.Errorf("main_sash_ratio=%q: snapshot is not marshalable: %v", raw, err)
		}
	}
	// A NaN arriving from the typed side must be clamped on the way out too.
	s := Defaults()
	s.MainSashRatio = math.NaN()
	if _, err := json.Marshal(ToSnapshot(s, nil)); err != nil {
		t.Errorf("NaN from the typed side: %v", err)
	}
}

func TestEmptyAccountGroupsMarshalAsList(t *testing.T) {
	s := Defaults()
	s.AccountGroups = nil
	raw, err := json.Marshal(ToSnapshot(s, nil)["settings"].(map[string]any)["account_groups"])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("got %s, want [] (Python writes list(...), never null)", raw)
	}
}

// The complete set of codepoints CPython 3.12's str.isspace() (and therefore a
// str-pattern `\s`) accepts. Go's unicode.IsSpace omits U+001C..U+001F and
// RE2's \s omits those plus \v, U+0085 and \p{Z}.
var pythonWhitespace = []rune{
	0x0009, 0x000A, 0x000B, 0x000C, 0x000D, 0x001C, 0x001D, 0x001E, 0x001F,
	0x0020, 0x0085, 0x00A0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004,
	0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200A, 0x2028, 0x2029, 0x202F,
	0x205F, 0x3000,
}

func TestWhitespaceSetMatchesPythonExactly(t *testing.T) {
	want := make(map[rune]bool, len(pythonWhitespace))
	for _, r := range pythonWhitespace {
		want[r] = true
	}
	for cp := 0; cp < 0x110000; cp++ {
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		r, c := rune(cp), string(rune(cp))
		if got := pyStrip(c) == ""; got != want[r] {
			t.Fatalf("pyStrip strips U+%04X = %v, python str.strip() = %v", cp, got, want[r])
		}
		// providerRegionSplitRe = python's [\s,，;；]
		isSep := want[r] || r == ',' || r == '，' || r == ';' || r == '；'
		if got := providerRegionSplitRe.FindString(c) == c; got != isSep {
			t.Fatalf("providerRegionSplitRe matches U+%04X = %v, python = %v", cp, got, isSep)
		}
	}
}
