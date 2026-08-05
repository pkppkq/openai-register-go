package models

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Per-account browser-fingerprint persistence.
//
// THE RULE, and it has exactly one line proving it — app.py:9168
//
//	if not self._fingerprint_fixed:
//	    self.fingerprint = generate_fingerprint_for_exit(health)
//
// combined with app.py:8861-8862
//
//	self.fingerprint = saved_fingerprint or generate_register_fingerprint()
//	self._fingerprint_fixed = saved_fingerprint is not None
//
// A saved fingerprint sets `_fingerprint_fixed = True` at construction, so the
// `if not self._fingerprint_fixed` gate at 9168 NEVER fires and the stored
// fingerprint is reused VERBATIM. Consequences worth spelling out:
//
//   - A proxy-exit change does NOT invalidate a saved fingerprint. Even if the
//     exit geo now says a different country, the stored locale/timezone win.
//     Fingerprint stability beats exit-geo match, deliberately: OpenAI sees one
//     device per account forever, which is the whole point of storing it.
//   - A saved fingerprint also suppresses the Team-profile switch (app.py:9004
//     `if not self._fingerprint_fixed: self.fingerprint = generate_team_fingerprint()`).
//   - Only the absence of a stored fingerprint regenerates. Same shape at all
//     three call sites: app.py:20993 (_get_or_create_account_fingerprint),
//     app.py:8861 (worker ctor), app.py:17984 (payment window).
//
// Malformed storage is handled at PARSE time in Python: fingerprint_from_dict
// (app.py:1466-1496) returns None when user_agent / locale / timezone / platform
// are not all non-blank, or when any numeric coercion raises. A None reaches the
// `or` above and the account simply regenerates.
// ---------------------------------------------------------------------------

// FingerprintDecision is the outcome of ResolveAccountFingerprint.
type FingerprintDecision struct {
	// Fingerprint is the fingerprint the run must use.
	Fingerprint DeviceFingerprint
	// Reused is true when Fingerprint came verbatim from the account's stored
	// value. It is exactly the Python `_fingerprint_fixed` seed (app.py:8862):
	// true means no later stage may regenerate.
	//
	// !Reused means the account had nothing usable stored, so whatever
	// fingerprint the run finally settles on MUST be written back. Note the
	// write timing differs per call site: _get_or_create_account_fingerprint
	// (app.py:20994-20996) and the payment window (app.py:17985-17989) persist
	// immediately, but the worker deliberately does NOT persist its ctor value —
	// that one is provisional and is replaced by the exit-matched fingerprint in
	// _prepare_fingerprint_for_proxy (app.py:9170), which is what gets stored.
	Reused bool
}

// SavedAccountFingerprint mirrors _account_saved_fingerprint (app.py:20983-20985):
// the account's stored fingerprint, or nil. Returned as-is with no validation,
// matching Python — a DeviceFingerprint dataclass instance is always truthy, so
// only None is falsy there.
func SavedAccountFingerprint(a *MailAccount) *DeviceFingerprint {
	if a == nil {
		return nil
	}
	return a.BrowserFingerprint
}

// ResolveAccountFingerprint decides reuse-vs-regenerate for one run. It mirrors
// the `saved or generate()` idiom at app.py:20993, app.py:8861 and app.py:17984.
//
// generate is called ONLY when there is nothing usable stored — Python's `or`
// short-circuits, which matters because the payment-window generator is
// generate_fingerprint_for_exit, and that raises on unusable exit info
// (app.py:1431-1432). A nil generate defaults to GenerateRegisterFingerprint,
// the generator used by both the worker ctor and _get_or_create.
//
// This function does not mutate the account; the caller owns the write-back
// (see FingerprintDecision.Reused).
//
// DIVERGENCE: Python only checks `is not None`, so a structurally broken stored
// fingerprint would be reused. Here an invalid stored value is treated as
// absent. Same end state as Python — its parser (fingerprint_from_dict) already
// reduces malformed storage to None before it can ever reach the account — but
// it also covers a struct built in code that bypassed the parser, where reuse
// would launch a browser with an empty user agent.
func ResolveAccountFingerprint(a *MailAccount, generate func() DeviceFingerprint) FingerprintDecision {
	if saved := SavedAccountFingerprint(a); ValidFingerprint(saved) {
		return FingerprintDecision{Fingerprint: cloneFingerprint(*saved), Reused: true}
	}
	if generate == nil {
		generate = GenerateRegisterFingerprint
	}
	return FingerprintDecision{Fingerprint: generate(), Reused: false}
}

// ValidFingerprint reports whether fp would survive a round trip through
// storage. It is the required-field gate of fingerprint_from_dict
// (app.py:1469-1475): user_agent, locale, timezone and platform must all be
// non-blank after stripping. Anything failing this can be written to state.json
// but will come back as nil on the next load, so it is not worth storing.
func ValidFingerprint(fp *DeviceFingerprint) bool {
	if fp == nil {
		return false
	}
	return strings.TrimSpace(fp.UserAgent) != "" &&
		strings.TrimSpace(fp.Locale) != "" &&
		strings.TrimSpace(fp.Timezone) != "" &&
		strings.TrimSpace(fp.Platform) != ""
}

// NormalizeFingerprintForStorage returns fp exactly as it will read back after a
// state.json round trip, i.e. Python's fingerprint_from_dict(fingerprint_to_dict(fp))
// (app.py:1443-1496). Returns nil when fp cannot survive the trip.
//
// Every default here fires on Python's `or`, which is truthiness, NOT
// nil-ness: a stored 0 / "" / false takes the default. That is why a 0 viewport
// becomes 1280 rather than 1, and a 0 device_scale_factor becomes 1 rather than
// staying 0 (a 0 scale factor would be a broken browser).
func NormalizeFingerprintForStorage(fp *DeviceFingerprint) *DeviceFingerprint {
	if !ValidFingerprint(fp) {
		return nil
	}
	locale := strings.TrimSpace(fp.Locale)

	langs := make([]string, 0, len(fp.Languages))
	for _, l := range fp.Languages {
		if l = strings.TrimSpace(l); l != "" {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		langs = []string{locale} // app.py:1480 `languages or [locale]`
	}

	vendor := fp.Vendor
	if vendor == "" {
		vendor = "Google Inc." // app.py:1492 — note: no strip, so "  " stays "  "
	}

	return &DeviceFingerprint{
		UserAgent:           strings.TrimSpace(fp.UserAgent),
		Locale:              locale,
		Languages:           langs,
		Timezone:            strings.TrimSpace(fp.Timezone),
		ViewportWidth:       maxi(1, orInt(fp.ViewportWidth, 1280)),
		ViewportHeight:      maxi(1, orInt(fp.ViewportHeight, 720)),
		ScreenWidth:         maxi(1, orInt(fp.ScreenWidth, 1280)),
		ScreenHeight:        maxi(1, orInt(fp.ScreenHeight, 720)),
		OuterWidth:          maxi(1, orInt(fp.OuterWidth, 1296)),
		OuterHeight:         maxi(1, orInt(fp.OuterHeight, 800)),
		DeviceScaleFactor:   orFloat(fp.DeviceScaleFactor, 1),
		HardwareConcurrency: maxi(1, orInt(fp.HardwareConcurrency, 8)),
		DeviceMemory:        maxi(1, orInt(fp.DeviceMemory, 8)),
		Platform:            strings.TrimSpace(fp.Platform),
		Vendor:              vendor,
		MaxTouchPoints:      maxi(0, fp.MaxTouchPoints), // app.py:1493, no `or` default
	}
}

// ParseStoredFingerprint reads a fingerprint out of a decoded state.json map.
// Line-for-line port of fingerprint_from_dict (app.py:1466-1496) — it is the
// ONLY decoder Python has, and account_from_dict (app.py:1848/1873) uses it for
// every account on every load, so AccountFromMap routes here too.
//
// The shape it reproduces, exactly:
//
//	user_agent = str(value.get("user_agent") or "").strip()   # and locale,
//	                                                          # timezone, platform
//	if not all((user_agent, locale, timezone_name, platform)): return None
//	viewport_width = max(1, int(value.get("viewport_width") or 1280))
//	...
//	except Exception: return None
//
// Three things that a two-stage "read raw, then apply the defaults" version got
// wrong and this one does not:
//
//   - `or` is applied to the RAW value, not to the coerced int. A stored -0.5 is
//     TRUTHY, so Python takes int(-0.5) == 0 and max(1, 0) == 1; treating the
//     truncated 0 as falsy substituted 1280 instead.
//   - `str(x or "")` makes a stored 0 / false / null an EMPTY user_agent, which
//     fails the required-field gate and regenerates the fingerprint. A bare Go
//     type switch rendered them "0"/"False" and produced a fingerprint Python
//     would have thrown away — a browser launched with a user agent of "0".
//   - int()/float() raise on garbage and the except clause returns None (so the
//     account regenerates). Falling back to the per-field default instead kept a
//     half-corrupt fingerprint alive forever, because it round-trips clean.
//
// DIVERGENCE (documented, both unreachable from data app.py writes):
//   - str() of a JSON array or object yields Python's repr ("[1]", "{'a': 1}");
//     Go's decoder throws away dict insertion order so no function can reproduce
//     it. "" is returned, which for the four required fields means "regenerate".
//   - A non-iterable "languages" (a number) makes Python raise TypeError OUT of
//     fingerprint_from_dict — the comprehension sits above the try — which aborts
//     the whole load_state. Here it is simply an empty language list.
func ParseStoredFingerprint(v map[string]any) *DeviceFingerprint {
	if v == nil {
		return nil
	}
	userAgent := pyFieldStrip(v["user_agent"])
	locale := pyFieldStrip(v["locale"])
	timezoneName := pyFieldStrip(v["timezone"])
	platform := pyFieldStrip(v["platform"])
	if userAgent == "" || locale == "" || timezoneName == "" || platform == "" {
		return nil
	}
	var languages []string
	for _, item := range pyIterate(v["languages"]) {
		if s := pyTrimStrip(pyValueStr(item)); s != "" {
			languages = append(languages, s)
		}
	}
	if len(languages) == 0 {
		languages = []string{locale} // app.py:1480 `languages or [locale]`
	}

	fp := &DeviceFingerprint{
		UserAgent: userAgent, Locale: locale, Languages: languages,
		Timezone: timezoneName, Platform: platform,
	}
	type intField struct {
		key string
		def int
		lo  int
		dst *int
	}
	for _, f := range []intField{
		{"viewport_width", 1280, 1, &fp.ViewportWidth},
		{"viewport_height", 720, 1, &fp.ViewportHeight},
		{"screen_width", 1280, 1, &fp.ScreenWidth},
		{"screen_height", 720, 1, &fp.ScreenHeight},
		{"outer_width", 1296, 1, &fp.OuterWidth},
		{"outer_height", 800, 1, &fp.OuterHeight},
		{"hardware_concurrency", 8, 1, &fp.HardwareConcurrency},
		{"device_memory", 8, 1, &fp.DeviceMemory},
		{"max_touch_points", 0, 0, &fp.MaxTouchPoints},
	} {
		n, ok := pyIntOfOr(v[f.key], f.def)
		if !ok {
			return nil // Python's int() raised; the except clause returns None
		}
		*f.dst = maxi(f.lo, n)
	}
	scale, ok := pyFloatOfOr(v["device_scale_factor"], 1)
	if !ok {
		return nil
	}
	fp.DeviceScaleFactor = scale
	// app.py:1492 `str(value.get("vendor") or "Google Inc.")` — NOT stripped, so
	// a vendor of "  " stays "  ".
	fp.Vendor = pyValueStr(pyOr(v["vendor"], "Google Inc."))
	return fp
}

// ---------------------------------------------------------------------------
// Python value coercion, for the storage decoder only.
//
// These are the same semantics internal/settings/snapshot.go and
// internal/accounts/pyvalue.go implement; they are duplicated rather than shared
// because all three sets are unexported package internals and models must not
// import either package (both import models).
// ---------------------------------------------------------------------------

// pyTruth mirrors Python truth testing over the value kinds json.Unmarshal and
// an in-process map can produce.
func pyTruth(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case float32:
		return t != 0
	case int:
		return t != 0
	case int8:
		return t != 0
	case int16:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	case uint:
		return t != 0
	case uint8:
		return t != 0
	case uint16:
		return t != 0
	case uint32:
		return t != 0
	case uint64:
		return t != 0
	case uintptr:
		return t != 0
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n != 0
		}
		if f, err := t.Float64(); err == nil {
			return f != 0
		}
		return t.String() != ""
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// pyOr mirrors `v or fallback`.
func pyOr(v any, fallback any) any {
	if pyTruth(v) {
		return v
	}
	return fallback
}

// pyValueStr mirrors Python's str(). Containers return "" — see the DIVERGENCE
// note on ParseStoredFingerprint.
func pyValueStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(t)
	case int8:
		return strconv.FormatInt(int64(t), 10)
	case int16:
		return strconv.FormatInt(int64(t), 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint8:
		return strconv.FormatUint(uint64(t), 10)
	case uint16:
		return strconv.FormatUint(uint64(t), 10)
	case uint32:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case uintptr:
		return strconv.FormatUint(uint64(t), 10)
	case json.Number:
		return t.String()
	case float32:
		return pyNumStr(float64(t))
	case float64:
		return pyNumStr(t)
	}
	return ""
}

// pyNumStr renders a JSON number the way Python's str() does. encoding/json
// collapses int and float into float64, so the int/float distinction Python's
// json module keeps is gone; integral values render WITHOUT the ".0" because
// every number app.py writes to these keys came from int() (app.py:1454-1465).
func pyNumStr(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// pyFieldStrip mirrors `str(value.get(k) or "").strip()`.
func pyFieldStrip(v any) string {
	if !pyTruth(v) {
		return ""
	}
	return pyTrimStrip(pyValueStr(v))
}

// pyTrimStrip is Python's str.strip() — the 29 code points str.isspace()
// reports, four more than Go's unicode.IsSpace. pyStripCutset lives in phone.go.
func pyTrimStrip(s string) string { return strings.Trim(s, pyStripCutset) }

// pyIterate mirrors `for item in (value.get(k) or [])`. A Python str iterates
// its CHARACTERS and a dict iterates its KEYS, both of which a naive
// "is it a []any?" check silently turns into an empty list.
//
// Dict keys come back sorted: Python yields them in insertion order, which
// encoding/json discards. Only reachable from a hand-corrupted state.json.
func pyIterate(v any) []any {
	if !pyTruth(v) {
		return nil
	}
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case string:
		out := make([]any, 0, len(t))
		for _, r := range t {
			out = append(out, string(r))
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = k
		}
		return out
	}
	return nil
}

// pyIntOfOr mirrors `int(v or def)`; ok is false where Python raises.
func pyIntOfOr(v any, def int) (int, bool) {
	return pyIntOf(pyOr(v, def))
}

func pyIntOf(v any) (int, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case int:
		return t, true
	case int8:
		return int(t), true
	case int16:
		return int(t), true
	case int32:
		return int(t), true
	case int64:
		return signedIntToNative(t), true
	case uint:
		return unsignedIntToNative(uint64(t)), true
	case uint8:
		return int(t), true
	case uint16:
		return int(t), true
	case uint32:
		return unsignedIntToNative(uint64(t)), true
	case uint64:
		return unsignedIntToNative(t), true
	case uintptr:
		return unsignedIntToNative(uint64(t)), true
	case float32:
		return truncSat(float64(t)), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return truncSat(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return signedIntToNative(n), true
		}
		f, err := t.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return truncSat(f), true
	case string:
		return pyIntString(t)
	}
	return 0, false
}

// pyFloatOfOr mirrors `float(v or def)`; ok is false where Python raises.
func pyFloatOfOr(v any, def float64) (float64, bool) {
	switch t := pyOr(v, def).(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case uintptr:
		return float64(t), true
	case float32:
		return float64(t), true
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		return pyFloatString(t)
	}
	return 0, false
}

// signedIntToNative 将 Python 可表示但本机 int 可能容纳不下的整数饱和到边界，
// 避免窄平台直接转换时回绕。
func signedIntToNative(v int64) int {
	maxInt := int64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if v > maxInt {
		return int(maxInt)
	}
	if v < minInt {
		return int(minInt)
	}
	return int(v)
}

// unsignedIntToNative 保留 Python 非负整数语义；超过本机 int 时取上界，
// 不能让 uint64 转换成负数。
func unsignedIntToNative(v uint64) int {
	maxInt := uint64(^uint(0) >> 1)
	if v > maxInt {
		return int(maxInt)
	}
	return int(v)
}

// truncSat is int(float) with Python's toward-zero truncation, saturating rather
// than wrapping: Python ints are arbitrary precision, and converting an
// out-of-range float64 to int64 is implementation-defined in Go.
func truncSat(f float64) int {
	f = math.Trunc(f)
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if f >= float64(maxInt) {
		return maxInt
	}
	if f <= float64(minInt) {
		return minInt
	}
	return int(f)
}

// pyIntString mirrors CPython int(str): optional surrounding whitespace (the 25
// code points unicode.IsSpace reports — NOT the 29 str.strip() removes), an
// optional sign, Unicode decimal digits folded to ASCII, and single underscore
// separators BETWEEN digits. strconv.ParseInt on its own rejects "3_0", which
// int() reads as 30, and base-0 would wrongly accept "0x10".
func pyIntString(s string) (int, bool) {
	body := pyDecimalASCII(strings.TrimSpace(s))
	neg := false
	if body != "" && (body[0] == '+' || body[0] == '-') {
		neg = body[0] == '-'
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	var b strings.Builder
	b.Grow(len(body))
	prevDigit := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '_' {
			if !prevDigit || i+1 >= len(body) || body[i+1] < '0' || body[i+1] > '9' {
				return 0, false
			}
			prevDigit = false
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		prevDigit = true
		b.WriteByte(c)
	}
	n, err := strconv.ParseInt(b.String(), 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			if neg {
				return signedIntToNative(math.MinInt64), true
			}
			return signedIntToNative(math.MaxInt64), true
		}
		return 0, false
	}
	if neg {
		n = -n
	}
	return signedIntToNative(n), true
}

// pyFloatString mirrors CPython float(str): rejects the Go-only hex form
// ("0x1p3"), accepts the overflow "1e400" as ±Inf instead of erroring, and
// accepts a signed nan.
func pyFloatString(s string) (float64, bool) {
	t := pyDecimalASCII(strings.TrimSpace(s))
	rest := t
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	if len(rest) >= 2 && rest[0] == '0' && (rest[1] == 'x' || rest[1] == 'X') {
		return 0, false
	}
	if strings.EqualFold(rest, "nan") {
		return math.NaN(), true
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// pyDecimalASCII folds every Unicode decimal digit (category Nd) to its ASCII
// equivalent, the pre-pass CPython runs before int()/float() parse a str
// (_PyUnicode_TransformDecimalAndSpaceToASCII). That is why int("０７") is 7.
func pyDecimalASCII(s string) string {
	ascii := true
	for _, r := range s {
		if r > 0x7F {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if d, ok := ndDigit(r); ok && r > 0x7F {
			b.WriteByte(byte('0' + d))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ndDigit returns the decimal value of a Unicode Nd rune. Adjacent digit blocks
// are not separated by a gap (U+1D7CE..U+1D7FF is one unbroken span of five
// ten-digit blocks), so the value is the offset from the start of the whole
// contiguous span modulo ten.
func ndDigit(r rune) (int, bool) {
	if !unicode.IsDigit(r) {
		return 0, false
	}
	if r <= '9' {
		return int(r - '0'), true
	}
	start := r
	for start > 0 && unicode.IsDigit(start-1) {
		start--
	}
	return int(r-start) % 10, true
}

// FingerprintFromStorage normalizes whatever the persistence event carried into
// a fingerprint, mirroring app.py:21003
//
//	payload if isinstance(payload, DeviceFingerprint) else fingerprint_from_dict(payload if isinstance(payload, dict) else None)
//
// Accepts a decoded map, a DeviceFingerprint (value or pointer), or nil.
// Anything else is nil, matching Python's isinstance chain.
func FingerprintFromStorage(v any) *DeviceFingerprint {
	switch t := v.(type) {
	case nil:
		return nil
	case *DeviceFingerprint:
		if !ValidFingerprint(t) {
			return nil
		}
		out := cloneFingerprint(*t)
		return &out
	case DeviceFingerprint:
		if !ValidFingerprint(&t) {
			return nil
		}
		out := cloneFingerprint(t)
		return &out
	case map[string]any:
		return ParseStoredFingerprint(t)
	default:
		return nil
	}
}

// FingerprintSavePayload mirrors _queue_account_fingerprint_save
// (app.py:20987-20990): serialise, and report whether it is worth queueing.
// Python guards with `if payload:` — fingerprint_to_dict(None) is `{}`, which is
// falsy, so a nil fingerprint is dropped rather than written as an empty object.
//
// The map is handed straight to the state writer. Go's encoding/json sorts map
// keys, so key order is deterministic (alphabetical) rather than Python's
// insertion order — a cosmetic diff in state.json, never a correctness one,
// since everything reads by key. No HTML escaping concern originates here
// either: this layer emits no JSON, and internal/state already sets
// SetEscapeHTML(false) for the file it writes.
func FingerprintSavePayload(fp *DeviceFingerprint) (map[string]any, bool) {
	payload := FingerprintToMap(fp)
	return payload, len(payload) > 0
}

// FingerprintsEqual reports value equality. This is the comparison at
// app.py:21006-21008, which compares fingerprint_to_dict outputs; since
// fingerprint_to_dict is a straight field copy, comparing fields is equivalent
// and avoids serialising two maps just to diff them.
//
// nil == nil is true, matching Python's `{} == {}` for two absent fingerprints.
func FingerprintsEqual(a, b *DeviceFingerprint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UserAgent == b.UserAgent &&
		a.Locale == b.Locale &&
		a.Timezone == b.Timezone &&
		a.ViewportWidth == b.ViewportWidth &&
		a.ViewportHeight == b.ViewportHeight &&
		a.ScreenWidth == b.ScreenWidth &&
		a.ScreenHeight == b.ScreenHeight &&
		a.OuterWidth == b.OuterWidth &&
		a.OuterHeight == b.OuterHeight &&
		a.DeviceScaleFactor == b.DeviceScaleFactor &&
		a.HardwareConcurrency == b.HardwareConcurrency &&
		a.DeviceMemory == b.DeviceMemory &&
		a.Platform == b.Platform &&
		a.Vendor == b.Vendor &&
		a.MaxTouchPoints == b.MaxTouchPoints &&
		slices.Equal(a.Languages, b.Languages)
}

// ApplyAccountFingerprint mirrors _save_account_fingerprint (app.py:20999-21012)
// minus the account lookup and the save_state() call, both of which belong to
// the caller. It returns true when the account changed, i.e. when state.json
// must be rewritten. Returns false for a missing account, an unusable incoming
// fingerprint, or a no-op write — the Python return value drives exactly that
// "did anything change" decision, and returning true on a no-op would rewrite
// state.json on every proxy handshake.
//
// DIVERGENCE (deliberate, both harmless):
//   - Python accepts a DeviceFingerprint instance without validating it; here an
//     invalid one is rejected, for the reason given on ResolveAccountFingerprint.
//   - Python aliases the fingerprint object onto the account; here it is copied
//     (Languages included) so the worker and the account cannot share a slice.
//     Python never mutates a fingerprint in place, so this is behaviour-preserving.
func ApplyAccountFingerprint(a *MailAccount, incoming *DeviceFingerprint) bool {
	if a == nil || !ValidFingerprint(incoming) {
		return false
	}
	if FingerprintsEqual(a.BrowserFingerprint, incoming) {
		return false
	}
	stored := cloneFingerprint(*incoming)
	a.BrowserFingerprint = &stored
	return true
}

// cloneFingerprint deep-copies the one reference field.
func cloneFingerprint(fp DeviceFingerprint) DeviceFingerprint {
	fp.Languages = slices.Clone(fp.Languages)
	return fp
}

// orInt implements Python `int(x or def)` for an already-typed int: 0 is falsy
// and takes the default. Negative values are truthy and pass through (the
// caller's max() clamp then handles them).
func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// orFloat implements Python `float(x or def)`: 0 is falsy and takes the default.
func orFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}
