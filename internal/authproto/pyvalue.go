package authproto

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Python value semantics
//
// Everything in this file exists because the ported code is diffed against
// CPython: `x or y` fires on 0/""/False/[]/{}, str() has its own spelling for
// every type, slicing counts CHARACTERS not bytes, and json.dumps has a
// different escape table from encoding/json.
// ---------------------------------------------------------------------------

// orderedObject is the read side of an insertion-ordered JSON object. Both the
// local orderedMap and the value openai.DecodeOrderedJSON returns satisfy it,
// which is how this package walks a decoded payload in CPython dict order
// without importing an unexported type.
type orderedObject interface {
	Get(key string) any
	Keys() []string
}

// orderedMap is a literal, insertion-ordered dict, used to BUILD request bodies
// (Go map iteration is random; a JSON body whose keys reshuffle per run is a
// different request as far as a fingerprinting endpoint is concerned).
type orderedMap struct {
	keys []string
	vals map[string]any
}

func newOrderedMap(pairs ...any) *orderedMap {
	o := &orderedMap{vals: map[string]any{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		o.Set(key, pairs[i+1])
	}
	return o
}

// Set mirrors CPython dict assignment: a repeated key keeps its ORIGINAL
// position and only its value is replaced.
func (o *orderedMap) Set(key string, value any) {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Get is dict.get(key) — nil when absent.
func (o *orderedMap) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Keys returns the keys in insertion order.
func (o *orderedMap) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// asObject is the `isinstance(x, dict)` guard that guards nearly every payload
// read in this port.
func asObject(v any) (orderedObject, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case orderedObject:
		if t == nil {
			return nil, false
		}
		return t, true
	case map[string]any:
		// DIVERGENCE: a plain map has already lost CPython's insertion order.
		// Nothing in this package decodes into one (openai.DecodeOrderedJSON is
		// always used), but tests and callers may hand one in, so it is wrapped
		// in SORTED key order — deterministic, just not the wire order.
		o := &orderedMap{vals: map[string]any{}}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, t[k])
		}
		return o, true
	default:
		return nil, false
	}
}

// objGet is `payload.get(key)` behind the isinstance(payload, dict) guard.
func objGet(v any, key string) any {
	if o, ok := asObject(v); ok {
		return o.Get(key)
	}
	return nil
}

// pyTruthy mirrors Python truthiness — the semantics behind every `x or y` and
// every `if x:` in the ported source. "" / 0 / 0.0 / False / [] / {} / None are
// all falsy; " " and "0" are NOT.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i != 0
		}
		if f, err := t.Float64(); err == nil {
			return f != 0
		}
		return t.String() != ""
	case []any:
		return len(t) > 0
	case *orderedMap:
		return t != nil && len(t.keys) > 0
	case orderedObject:
		return t != nil && len(t.Keys()) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// pyStr mirrors str(v) for the shapes json.loads produces. Numbers stay
// json.Number so an id round-trips as the literal CPython saw (float64 would
// render 12345678901234567890 as 1.2345678901234567e+19).
func pyStr(v any) string {
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
	case json.Number:
		return t.String()
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// str(1.0) is "1.0", NOT "1": CPython's float.__str__ is float.__repr__,
		// which always keeps a decimal point or an exponent. An earlier version
		// printed integral floats through FormatInt and produced "1".
		return pyFloatStr(t)
	case []any:
		return pyRepr(t)
	case orderedObject:
		return pyRepr(t)
	case map[string]any:
		return pyRepr(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// pyStrOr is the ubiquitous `str(x or "")` idiom.
func pyStrOr(v any) string {
	if !pyTruthy(v) {
		return ""
	}
	return pyStr(v)
}

// pyRepr mirrors repr(v) / str(container) — what f"{payload}" prints when the
// payload is a dict or list. The error messages of this subsystem embed it.
func pyRepr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return pyStrRepr(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, pyRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case orderedObject:
		keys := t.Keys()
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, pyStrRepr(k)+": "+pyRepr(t.Get(k)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		o, _ := asObject(t)
		return pyRepr(o)
	default:
		return pyStr(v)
	}
}

// pyStrRepr is repr() of a str: CPython prefers single quotes and only switches
// to double quotes when the value contains a ' but no ".
//
// The escape set is EVERY non-printable code point, not just the C0 controls:
// CPython escapes anything whose str.isprintable() is false, i.e. anything in a
// C* or Z* general category except the ASCII space. That is exactly Go's
// unicode.IsPrint, so the test is one call — but writing the loop against
// `r < 0x20` (as this did) left U+0085, U+00A0, U+00AD, U+200B, U+2028, U+2029,
// U+3000 and every unassigned code point unescaped, so a repr of an OpenAI
// error payload rendered a raw line separator into the log line.
//
// The spelling is CPython's: \t \n \r and the active quote get short forms, and
// everything else becomes \xXX below U+0100, \uXXXX below U+10000, \UXXXXXXXX
// above. Note that \b, \f and \v are NOT short forms in repr (they are in
// json.dumps) — repr writes \x08, \x0c and \x0b.
func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// truncRunes is Python's text[:n] — a CHARACTER slice. Every `body[:500]` /
// `[:300]` in the ported error messages goes through it, because a Go byte
// slice would cut a multi-byte rune in half on the Chinese error bodies.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// runeLen is len(str) — CHARACTERS, which is what the `len(password) >= 12` and
// `digest[:len(difficulty)]` checks mean in Python.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// ---------------------------------------------------------------------------
// json.dumps
//
// encoding/json is not a drop-in: it escapes < > & (SetEscapeHTML), escapes
// U+2028/U+2029 which Python does not, spells \b and \f as /, and
// reorders object keys. pyJSONDumps below is the exact CPython escape table.
// ---------------------------------------------------------------------------

// pyJSONDumps mirrors json.dumps(value, ensure_ascii=..., separators=...).
//
// compact selects separators=(",", ":"); otherwise CPython's indent-less
// default separators (", ", ": ") are used — the difference matters because
// _fetch_sentinel_token's failure message re-serializes the parsed payload with
// the DEFAULT separators while base64_json uses the compact ones.
func pyJSONDumps(value any, ensureASCII, compact bool) string {
	itemSep, keySep := ", ", ": "
	if compact {
		itemSep, keySep = ",", ":"
	}
	var b strings.Builder
	pyJSONWrite(&b, value, ensureASCII, itemSep, keySep)
	return b.String()
}

func pyJSONWrite(b *strings.Builder, value any, ensureASCII bool, itemSep, keySep string) {
	switch t := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		pyJSONString(b, t, ensureASCII)
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		// CPython's json uses float.__repr__ (shortest round-trip), which is
		// what FormatFloat(-1) produces. Integral floats keep their ".0".
		b.WriteString(pyFloatRepr(t))
	case json.Number:
		b.WriteString(t.String())
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(itemSep)
			}
			pyJSONWrite(b, item, ensureASCII, itemSep, keySep)
		}
		b.WriteByte(']')
	case orderedObject:
		b.WriteByte('{')
		for i, k := range t.Keys() {
			if i > 0 {
				b.WriteString(itemSep)
			}
			pyJSONString(b, k, ensureASCII)
			b.WriteString(keySep)
			pyJSONWrite(b, t.Get(k), ensureASCII, itemSep, keySep)
		}
		b.WriteByte('}')
	case map[string]any:
		o, _ := asObject(t)
		pyJSONWrite(b, o, ensureASCII, itemSep, keySep)
	default:
		// Nothing else is ever serialized here; fall back to encoding/json with
		// HTML escaping off so at least the bytes stay Python-shaped.
		raw, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	}
}

// pyFloatRepr is json.dumps' float spelling. It is NOT str(float): the JSON
// encoder writes the non-finite values as the bare tokens Infinity / -Infinity
// / NaN, where str() writes inf / -inf / nan.
func pyFloatRepr(f float64) string {
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	return pyFloatStr(f)
}

// pyFloatStr is str(float) / repr(float), which json.dumps also uses for every
// finite float.
//
// strconv.FormatFloat(f, 'g', -1, 64) is NOT it. Both pick the shortest digits
// that round-trip, but they disagree on when to switch to scientific notation:
// Go's shortest-%g goes exponential once the decimal point sits past position
// 6, CPython's repr only past position 16. FormatFloat printed 1234567.0 as
// "1.234567e+06" where CPython prints "1234567.0"
// (CPython Python/pystrtod.c: use_exp = decpt <= -4 || decpt > 16).
func pyFloatStr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp := 0
	if i := strings.IndexByte(sci, 'e'); i >= 0 {
		exp, _ = strconv.Atoi(sci[i+1:])
	}
	if decpt := exp + 1; decpt <= -4 || decpt > 16 {
		// Go's 'e' already spells the exponent the way CPython does: a sign and
		// at least two digits, and no trailing ".0" on a one-digit mantissa.
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyJSONString is CPython's py_encode_basestring / py_encode_basestring_ascii.
// ESCAPE_DCT gives short forms for \\ " \b \f \n \r \t and \uXXXX for every
// other C0 control; NOTHING else is escaped when ensure_ascii is False — in
// particular not <, >, & (encoding/json escapes those) and not U+2028/U+2029
// (encoding/json escapes those too).
func pyJSONString(b *strings.Builder, s string, ensureASCII bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
			continue
		case '\\':
			b.WriteString(`\\`)
			continue
		case '\b':
			b.WriteString(`\b`)
			continue
		case '\f':
			b.WriteString(`\f`)
			continue
		case '\n':
			b.WriteString(`\n`)
			continue
		case '\r':
			b.WriteString(`\r`)
			continue
		case '\t':
			b.WriteString(`\t`)
			continue
		}
		if r < 0x20 {
			fmt.Fprintf(b, `\u%04x`, r)
			continue
		}
		if !ensureASCII || r < 0x7f {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
			continue
		}
		fmt.Fprintf(b, `\u%04x`, r)
	}
	b.WriteByte('"')
}
