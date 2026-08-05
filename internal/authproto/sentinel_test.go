package authproto

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// Every expected value in this file was produced by running the ORIGINAL
// app.py functions under this machine's CPython 3.12.0 and pasting the output.
// Nothing here is derived from the Go implementation.

// ---------------------------------------------------------------------------
// sentinel_hash_hex (app.py:5515-5525)
// ---------------------------------------------------------------------------

func TestSentinelHashHex(t *testing.T) {
	// CPython 3.12.0, sentinel_hash_hex(<input>).
	cases := []struct{ in, want string }{
		{"", "ab3e7c0b"},
		{"a", "1a80b1b3"},
		{"abc", "1cc93dbc"},
		{"hello world", "b90456ec"},
		{"0123456789abcdef", "a439268b"},
		{"seed-abc123", "9e3452a5"},
		// Non-ASCII: proves the hash consumes CODE POINTS, not UTF-8 bytes.
		{"中文测试", "fa8ef507"},
		{"\U0001F600emoji", "0210efd7"},
		{"éèê", "327495ca"},
		{"\uffff", "0201b946"},
		{"\U0010FFFF", "7f40d3d4"},
		{"\x00\x01\x7f", "f5fcb973"},
		{"gAAAAAB" + strings.Repeat("x", 40), "5374ae12"},
		{"The quick brown fox jumps over the lazy dog", "cc6bf03d"},
	}
	for _, c := range cases {
		if got := sentinelHashHex(c.in); got != c.want {
			t.Errorf("sentinelHashHex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// base64_json (app.py:5511-5512)
// ---------------------------------------------------------------------------

func TestBase64Json(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"list", []any{1, 2, 3}, "WzEsMiwzXQ=="},
		{"fallback string", "max attempts exceeded", "Im1heCBhdHRlbXB0cyBleGNlZWRlZCI="},
		{"mixed", []any{"a", 1, true, false, nil}, "WyJhIiwxLHRydWUsZmFsc2UsbnVsbF0="},
		// json.dumps does NOT escape < > &; encoding/json does unless
		// SetEscapeHTML(false). ensure_ascii=False keeps 中文 literal.
		{"html+cjk", []any{"<&>", "中文"}, "WyI8Jj4iLCLkuK3mlociXQ=="},
	}
	for _, c := range cases {
		if got := base64Json(c.in); got != c.want {
			t.Errorf("%s: base64Json = %q, want %q", c.name, got, c.want)
		}
	}
}

// sentinelStubData is collect_sentinel_fingerprint_data's output with every
// random / clock field pinned, so the encoder and the PoW predicate can be
// diffed against CPython byte for byte. Field positions are app.py:5529-5559.
func sentinelStubData() []any {
	return []any{
		2134,
		"Sun Jul 26 2026 21:30:00 GMT+0800 (China Standard Time)",
		4294967296,
		0,
		openai.DefaultUserAgent,
		"https://sentinel.openai.com/sentinel/20260219f9f6/sdk.js",
		"20260219f9f6",
		"zh-CN",
		"zh-CN,zh",
		0,
		"hardwareConcurrency−8", // U+2212 separator
		"location",
		"navigator",
		42,
		"9f3c1b7a-0000-4000-8000-abcdefabcdef",
		"sv",
		8,
		int64(1769000000000),
		0, 1, 1, 0, 0, 0, 1,
	}
}

// TestSentinelAnswerLoopVectors pins the exact (payload, digest) pair the PoW
// loop computes for the first four attempts, against CPython.
func TestSentinelAnswerLoopVectors(t *testing.T) {
	const seed = "0.5678"
	// CPython: data[9] = 3, data[3] = attempt.
	want := []struct {
		attempt int
		enc     string
		digest  string
	}{
		{0, "WzIxMzQsIlN1biBKdWwgMjYgMjAyNiAyMTozMDowMCBHTVQrMDgwMCAoQ2hpbmEgU3RhbmRhcmQgVGltZSkiLDQyOTQ5NjcyOTYsMCwiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0Ni4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9zZW50aW5lbC5vcGVuYWkuY29tL3NlbnRpbmVsLzIwMjYwMjE5ZjlmNi9zZGsuanMiLCIyMDI2MDIxOWY5ZjYiLCJ6aC1DTiIsInpoLUNOLHpoIiwzLCJoYXJkd2FyZUNvbmN1cnJlbmN54oiSOCIsImxvY2F0aW9uIiwibmF2aWdhdG9yIiw0MiwiOWYzYzFiN2EtMDAwMC00MDAwLTgwMDAtYWJjZGVmYWJjZGVmIiwic3YiLDgsMTc2OTAwMDAwMDAwMCwwLDEsMSwwLDAsMCwxXQ==", "7cbdb886"},
		{1, "WzIxMzQsIlN1biBKdWwgMjYgMjAyNiAyMTozMDowMCBHTVQrMDgwMCAoQ2hpbmEgU3RhbmRhcmQgVGltZSkiLDQyOTQ5NjcyOTYsMSwiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0Ni4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9zZW50aW5lbC5vcGVuYWkuY29tL3NlbnRpbmVsLzIwMjYwMjE5ZjlmNi9zZGsuanMiLCIyMDI2MDIxOWY5ZjYiLCJ6aC1DTiIsInpoLUNOLHpoIiwzLCJoYXJkd2FyZUNvbmN1cnJlbmN54oiSOCIsImxvY2F0aW9uIiwibmF2aWdhdG9yIiw0MiwiOWYzYzFiN2EtMDAwMC00MDAwLTgwMDAtYWJjZGVmYWJjZGVmIiwic3YiLDgsMTc2OTAwMDAwMDAwMCwwLDEsMSwwLDAsMCwxXQ==", "2aee47a7"},
		{2, "WzIxMzQsIlN1biBKdWwgMjYgMjAyNiAyMTozMDowMCBHTVQrMDgwMCAoQ2hpbmEgU3RhbmRhcmQgVGltZSkiLDQyOTQ5NjcyOTYsMiwiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0Ni4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9zZW50aW5lbC5vcGVuYWkuY29tL3NlbnRpbmVsLzIwMjYwMjE5ZjlmNi9zZGsuanMiLCIyMDI2MDIxOWY5ZjYiLCJ6aC1DTiIsInpoLUNOLHpoIiwzLCJoYXJkd2FyZUNvbmN1cnJlbmN54oiSOCIsImxvY2F0aW9uIiwibmF2aWdhdG9yIiw0MiwiOWYzYzFiN2EtMDAwMC00MDAwLTgwMDAtYWJjZGVmYWJjZGVmIiwic3YiLDgsMTc2OTAwMDAwMDAwMCwwLDEsMSwwLDAsMCwxXQ==", "d0b78098"},
		{3, "WzIxMzQsIlN1biBKdWwgMjYgMjAyNiAyMTozMDowMCBHTVQrMDgwMCAoQ2hpbmEgU3RhbmRhcmQgVGltZSkiLDQyOTQ5NjcyOTYsMywiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0Ni4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9zZW50aW5lbC5vcGVuYWkuY29tL3NlbnRpbmVsLzIwMjYwMjE5ZjlmNi9zZGsuanMiLCIyMDI2MDIxOWY5ZjYiLCJ6aC1DTiIsInpoLUNOLHpoIiwzLCJoYXJkd2FyZUNvbmN1cnJlbmN54oiSOCIsImxvY2F0aW9uIiwibmF2aWdhdG9yIiw0MiwiOWYzYzFiN2EtMDAwMC00MDAwLTgwMDAtYWJjZGVmYWJjZGVmIiwic3YiLDgsMTc2OTAwMDAwMDAwMCwwLDEsMSwwLDAsMCwxXQ==", "9bb0ea28"},
	}
	data := sentinelStubData()
	for _, w := range want {
		data[3] = w.attempt
		data[9] = 3
		enc := base64Json(data)
		if enc != w.enc {
			t.Fatalf("attempt %d: base64Json mismatch\n got %q\nwant %q", w.attempt, enc, w.enc)
		}
		if digest := sentinelHashHex(seed + enc); digest != w.digest {
			t.Errorf("attempt %d: digest = %q, want %q", w.attempt, digest, w.digest)
		}
	}
}

// TestRequirementsTokenPayloadEncoding pins the base64 payload
// generate_sentinel_requirements_token emits for a fixed fingerprint array.
func TestRequirementsTokenPayloadEncoding(t *testing.T) {
	const want = "WzIxMzQsIlN1biBKdWwgMjYgMjAyNiAyMTozMDowMCBHTVQrMDgwMCAoQ2hpbmEgU3RhbmRhcmQgVGltZSkiLDQyOTQ5NjcyOTYsMSwiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0Ni4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9zZW50aW5lbC5vcGVuYWkuY29tL3NlbnRpbmVsLzIwMjYwMjE5ZjlmNi9zZGsuanMiLCIyMDI2MDIxOWY5ZjYiLCJ6aC1DTiIsInpoLUNOLHpoIiwyNywiaGFyZHdhcmVDb25jdXJyZW5jeeKIkjgiLCJsb2NhdGlvbiIsIm5hdmlnYXRvciIsNDIsIjlmM2MxYjdhLTAwMDAtNDAwMC04MDAwLWFiY2RlZmFiY2RlZiIsInN2Iiw4LDE3NjkwMDAwMDAwMDAsMCwxLDEsMCwwLDAsMV0="
	data := sentinelStubData()
	data[3] = 1
	data[9] = 27
	if got := base64Json(data); got != want {
		t.Errorf("requirements payload mismatch\n got %q\nwant %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Token assembly (app.py:5562-5585)
// ---------------------------------------------------------------------------

// TestGenerateSentinelProofToken drives the real PoW loop (no network) and
// checks the wire shape: "gAAAAAB" + base64(payload) + "~S", where the payload
// is the 25-field array and the digest satisfies the difficulty predicate.
func TestGenerateSentinelProofToken(t *testing.T) {
	const seed = "test-seed"
	const difficulty = "0" // digest[:1] <= "0": ~1/16 of attempts qualify.
	token := generateSentinelProofToken(seed, difficulty)
	if !strings.HasPrefix(token, "gAAAAAB") {
		t.Fatalf("proof token prefix = %q", truncRunes(token, 16))
	}
	answer := strings.TrimPrefix(token, "gAAAAAB")
	if !strings.HasSuffix(answer, "~S") {
		t.Fatalf("answer must end with the ~S marker, got tail %q", answer[max(0, len(answer)-8):])
	}
	encoded := strings.TrimSuffix(answer, "~S")
	if digest := sentinelHashHex(seed + encoded); digest[:1] > difficulty {
		t.Errorf("digest %q does not satisfy difficulty %q", digest, difficulty)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("payload is not standard base64: %v", err)
	}
	var fields []any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("payload is not a JSON array: %v", err)
	}
	if len(fields) != 25 {
		t.Fatalf("fingerprint array has %d fields, want 25", len(fields))
	}
	if fields[6] != sentinelSDKVersion {
		t.Errorf("field 6 (sdk version) = %v", fields[6])
	}
	if fields[4] != openai.DefaultUserAgent {
		t.Errorf("field 4 (user agent) = %v", fields[4])
	}
	if fields[11] != "location" || fields[15] != "sv" {
		t.Errorf("positional constants moved: %v / %v", fields[11], fields[15])
	}
}

// TestGenerateSentinelRequirementsToken checks the no-PoW variant's prefix and
// its two pinned indices.
func TestGenerateSentinelRequirementsToken(t *testing.T) {
	token := generateSentinelRequirementsToken()
	if !strings.HasPrefix(token, "gAAAAAC") {
		t.Fatalf("requirements token prefix = %q", truncRunes(token, 16))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "gAAAAAC"))
	if err != nil {
		t.Fatalf("payload is not standard base64: %v", err)
	}
	var fields []any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("payload is not a JSON array: %v", err)
	}
	if len(fields) != 25 {
		t.Fatalf("fingerprint array has %d fields, want 25", len(fields))
	}
	if fields[3] != float64(1) {
		t.Errorf("field 3 must be pinned to 1, got %v", fields[3])
	}
	if v, ok := fields[9].(float64); !ok || v < 5 || v > 50 {
		t.Errorf("field 9 must be round(uniform(5,50)), got %v", fields[9])
	}
}

// TestGenerateSentinelAnswerLongDifficulty guards the one place Go would panic
// where Python silently truncates: digest[:len(difficulty)] with a difficulty
// longer than the 8-character digest.
func TestGenerateSentinelAnswerLongDifficulty(t *testing.T) {
	answer := generateSentinelAnswer("seed", "ffffffffffffffff")
	if !strings.HasSuffix(answer, "~S") {
		t.Fatalf("expected an immediate solve, got tail %q", answer[max(0, len(answer)-8):])
	}
}

// TestCollectSentinelFingerprintDataShape checks the positional constants of
// the live collector (the randomized fields are checked only for membership).
func TestCollectSentinelFingerprintDataShape(t *testing.T) {
	data := collectSentinelFingerprintData("sid-1")
	if len(data) != 25 {
		t.Fatalf("len = %d, want 25", len(data))
	}
	if data[0] != 1366+768 {
		t.Errorf("field 0 = %v, want 2134", data[0])
	}
	if data[2] != 4294967296 {
		t.Errorf("field 2 = %v", data[2])
	}
	if data[5] != "https://sentinel.openai.com/sentinel/"+sentinelSDKVersion+"/sdk.js" {
		t.Errorf("field 5 = %v", data[5])
	}
	if data[7] != "zh-CN" || data[8] != "zh-CN,zh" {
		t.Errorf("locale fields = %v / %v", data[7], data[8])
	}
	if data[14] != "sid-1" {
		t.Errorf("field 14 (sid) = %v", data[14])
	}
	if data[16] != 8 {
		t.Errorf("field 16 = %v", data[16])
	}
	salt, _ := data[10].(string)
	if !strings.Contains(salt, "\u2212") {
		t.Errorf("field 10 must use U+2212 MINUS SIGN, got %q", salt)
	}
	found := false
	for _, choice := range sentinelGlobalChoices {
		if data[12] == choice {
			found = true
		}
	}
	if !found {
		t.Errorf("field 12 = %v, not in the choice pool", data[12])
	}
	tail := []any{0, 1, 1, 0, 0, 0, 1}
	for i, want := range tail {
		if data[18+i] != want {
			t.Errorf("field %d = %v, want %v", 18+i, data[18+i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// The json.dumps port that base64_json depends on
// ---------------------------------------------------------------------------

func TestPyJSONDumps(t *testing.T) {
	cases := []struct {
		name        string
		value       any
		ensureASCII bool
		compact     bool
		want        string
	}{
		{
			// CPython: json.dumps([...], ensure_ascii=False, separators=(",",":"))
			// Note: < > & stay literal, U+2028/U+2029 stay literal, DEL stays
			// literal — all three differ from encoding/json.
			name:    "compact, ensure_ascii=False",
			value:   []any{"<a>&b", "中\u2028\u2029", "tab\tnl\nbs\b ff\f cr\r q\" bs\\", "\x00\x1f\x7f", "\U0001F600"},
			compact: true,
			want:    "[\"<a>&b\",\"中\u2028\u2029\",\"tab\\tnl\\nbs\\b ff\\f cr\\r q\\\" bs\\\\\",\"\\u0000\\u001f\x7f\",\"\U0001F600\"]",
		},
		{
			// CPython: json.dumps([...], ensure_ascii=True, separators=(",",":"))
			name:        "compact, ensure_ascii=True",
			value:       []any{"<a>&b", "中\u2028\u2029", "\U0001F600", "\x7f"},
			ensureASCII: true,
			compact:     true,
			want:        `["<a>&b","\u4e2d\u2028\u2029","\ud83d\ude00","\u007f"]`,
		},
		{
			// CPython: json.dumps({...}, ensure_ascii=False) — DEFAULT separators
			// (", " / ": "), which is what the sentinel failure message uses.
			name: "default separators keep insertion order",
			value: newOrderedMap(
				"b", 1,
				"a", newOrderedMap("z", []any{1, 2}),
				"c", nil,
				"d", true,
			),
			want: `{"b": 1, "a": {"z": [1, 2]}, "c": null, "d": true}`,
		},
	}
	for _, c := range cases {
		if got := pyJSONDumps(c.value, c.ensureASCII, c.compact); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	// CPython json.dumps(<float>).
	cases := []struct {
		in   float64
		want string
	}{
		{1.0, "1.0"},
		{0.5, "0.5"},
		{1e-05, "1e-05"},
		{1e20, "1e+20"},
		{0.8444218515250481, "0.8444218515250481"},
	}
	for _, c := range cases {
		if got := pyFloatRepr(c.in); got != c.want {
			t.Errorf("pyFloatRepr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyRoundHalfEven(t *testing.T) {
	// CPython round(): ties to even.
	cases := []struct {
		in   float64
		want int64
	}{
		{0.5, 0}, {1.5, 2}, {2.5, 2}, {3.5, 4},
		{-0.5, 0}, {-1.5, -2}, {-2.5, -2},
		{4.9, 5}, {5.1, 5}, {49.999, 50},
	}
	for _, c := range cases {
		if got := pyRoundHalfEven(c.in); got != c.want {
			t.Errorf("pyRoundHalfEven(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
