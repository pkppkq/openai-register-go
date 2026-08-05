package authproto

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	mrand "math/rand/v2"
	"time"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ---------------------------------------------------------------------------
// sentinel.openai.com proof-of-work
//
// The whole file is a re-implementation of the browser-side sentinel SDK that
// app.py reverse-engineered. Every byte of the emitted token is hashed by the
// server, so the encoder, the field order of the fingerprint array and the FNV
// variant below are all load-bearing.
// ---------------------------------------------------------------------------

// sentinelSDKVersion is the SDK build id baked into two of the fingerprint
// fields (app.py:5535-5536).
const sentinelSDKVersion = "20260219f9f6"

// sentinelRequirementsURL is the endpoint _fetch_sentinel_token posts to
// (app.py:8112).
const sentinelRequirementsURL = "https://sentinel.openai.com/backend-api/sentinel/req"

// processStart anchors performanceNowMs. Python reads time.perf_counter_ns(),
// a monotonic clock with an unspecified epoch; Go's monotonic reading is only
// reachable through a difference, so the process start is used as that epoch.
//
// DIVERGENCE: the ABSOLUTE value at fingerprint index 13 therefore differs from
// CPython's (Python's perf_counter on Windows is time-since-boot, Go's is
// time-since-process-start). The server only hashes the field — it never
// validates it against a wall clock — and index 9, the only field the PoW loop
// derives from it, is a DIFFERENCE of two readings and so is unaffected.
var processStart = time.Now()

// performanceNowMs mirrors performance_now_ms (app.py:5507-5508):
// time.perf_counter_ns() // 1_000_000, i.e. floor-divided to whole ms.
func performanceNowMs() int64 {
	return int64(time.Since(processStart) / time.Millisecond)
}

// base64Json mirrors base64_json (app.py:5511-5512): compact JSON with
// ensure_ascii=False, UTF-8 encoded, standard base64 WITH padding.
func base64Json(value any) string {
	return base64.StdEncoding.EncodeToString([]byte(pyJSONDumps(value, false, true)))
}

// sentinelHashHex mirrors sentinel_hash_hex (app.py:5515-5525): FNV-1a over the
// string's CODE POINTS, then the MurmurHash3 fmix32 avalanche, printed as 8 hex
// digits.
//
// Iterating code points (Go's `for range` over a string) rather than bytes is
// mandatory: Python's `for char in value` / ord(char) yields one integer per
// code point, so a single U+4E2D contributes 0x4E2D to the hash, not three
// bytes 0xE4 0xB8 0xAD.
func sentinelHashHex(value string) string {
	hash := uint32(2166136261)
	for _, char := range value {
		hash ^= uint32(char)
		hash *= 16777619
	}
	hash ^= hash >> 16
	hash *= 2246822507
	hash ^= hash >> 13
	hash *= 3266489909
	hash ^= hash >> 16
	return fmt.Sprintf("%08x", hash)
}

// sentinelSaltChoices is the random.choice pool at app.py:5540-5544. The
// separator between the label and the value is U+2212 MINUS SIGN, not an ASCII
// hyphen — copied verbatim from the Python source.
var sentinelSaltChoices = []string{
	"userAgent−" + openai.DefaultUserAgent,
	"language−zh-CN",
	"hardwareConcurrency−8",
}

// sentinelGlobalChoices is the random.choice pool at app.py:5546.
var sentinelGlobalChoices = []string{
	"window", "self", "document", "navigator", "location", "screen", "history",
}

// sentinelTimeLayout is the Go spelling of Python's
// "%a %b %d %Y %H:%M:%S GMT%z (%Z)" (app.py:5531) — a JS Date.toString().
//
// DIVERGENCE: Python's %Z on this platform renders the LOCALIZED long zone name
// (CPython prints "中国标准时间" for Asia/Shanghai on a zh-CN Windows host)
// while Go's MST verb can only produce the short abbreviation ("CST"). The
// field is opaque fingerprint filler that the server hashes and never parses,
// and both forms are shapes a real browser emits, so the difference is safe.
const sentinelTimeLayout = "Mon Jan 02 2006 15:04:05 GMT-0700 (MST)"

// collectSentinelFingerprintData mirrors collect_sentinel_fingerprint_data
// (app.py:5528-5559). INDEX ORDER IS THE WIRE FORMAT — indices 3 and 9 are
// overwritten by the callers below, everything else is positional.
func collectSentinelFingerprintData(sid string) []any {
	return []any{
		1366 + 768,                            // 0
		time.Now().Format(sentinelTimeLayout), // 1
		4294967296,                            // 2
		mrand.Float64(),                       // 3  (always overwritten)
		openai.DefaultUserAgent,               // 4
		"https://sentinel.openai.com/sentinel/" + sentinelSDKVersion + "/sdk.js", // 5
		sentinelSDKVersion, // 6
		"zh-CN",            // 7
		"zh-CN,zh",         // 8
		mrand.Float64(),    // 9  (always overwritten)
		sentinelSaltChoices[mrand.IntN(len(sentinelSaltChoices))], // 10
		"location", // 11
		sentinelGlobalChoices[mrand.IntN(len(sentinelGlobalChoices))], // 12
		performanceNowMs(),     // 13
		sid,                    // 14
		"sv",                   // 15
		8,                      // 16
		time.Now().UnixMilli(), // 17  int(time.time() * 1000)
		0,                      // 18
		1,                      // 19
		1,                      // 20
		0,                      // 21
		0,                      // 22
		0,                      // 23
		1,                      // 24
	}
}

// sentinelAnswerFallback is the literal app.py:5573 falls back to after 500000
// fruitless attempts.
const sentinelAnswerFallback = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"

// generateSentinelAnswer mirrors generate_sentinel_answer (app.py:5562-5573):
// grind `attempt` into index 3 until the hash of seed+payload sorts at or below
// the server's difficulty prefix.
//
// The comparison is Python's `digest[:len(difficulty)] <= difficulty`: a
// LEXICOGRAPHIC string compare of the leading hex characters, not a numeric
// one, and `<=` not `<`.
func generateSentinelAnswer(seed, difficulty string) string {
	started := performanceNowMs()
	sid := randomUUID()
	data := collectSentinelFingerprintData(sid)
	// Python slices with len(difficulty); a difficulty longer than the 8-char
	// digest makes digest[:n] simply the whole digest, where Go would panic.
	prefix := runeLen(difficulty)
	if prefix > 8 {
		prefix = 8
	}
	for attempt := 0; attempt < 500000; attempt++ {
		data[3] = attempt
		// round() of an int-minus-int is that int; no rounding happens here.
		data[9] = performanceNowMs() - started
		encoded := base64Json(data)
		digest := sentinelHashHex(seed + encoded)
		if digest[:prefix] <= difficulty {
			return encoded + "~S"
		}
	}
	return sentinelAnswerFallback + base64Json("max attempts exceeded")
}

// generateSentinelRequirementsToken mirrors
// generate_sentinel_requirements_token (app.py:5576-5581): no proof of work,
// just a plausible fingerprint payload under the "gAAAAAC" prefix.
func generateSentinelRequirementsToken() string {
	sid := randomUUID()
	data := collectSentinelFingerprintData(sid)
	data[3] = 1
	// round(random.uniform(5, 50)) — CPython's round() is banker's rounding,
	// but uniform() hits an exact .5 with probability ~0, so nearest-even and
	// nearest-away agree in practice.
	data[9] = int(pyRoundHalfEven(5 + mrand.Float64()*45))
	return "gAAAAAC" + base64Json(data)
}

// generateSentinelProofToken mirrors generate_sentinel_proof_token
// (app.py:5584-5585).
func generateSentinelProofToken(seed, difficulty string) string {
	return "gAAAAAB" + generateSentinelAnswer(seed, difficulty)
}

// pyRoundHalfEven is Python's builtin round(float) -> int: ties go to even.
func pyRoundHalfEven(f float64) int64 {
	floor := int64(f)
	if f < 0 && float64(floor) != f {
		floor--
	}
	frac := f - float64(floor)
	switch {
	case frac > 0.5:
		return floor + 1
	case frac < 0.5:
		return floor
	default:
		if floor%2 == 0 {
			return floor
		}
		return floor + 1
	}
}

// randomUUID returns an RFC-4122 v4 UUID string (Python str(uuid.uuid4())).
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// uuid4() cannot fail in Python; degrade rather than abort the flow.
		for i := range b {
			b[i] = byte(mrand.UintN(256))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
