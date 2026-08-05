package sessionconv

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// pyIsSpace reports whether r is whitespace to Python. strings.TrimSpace is NOT
// a substitute: Go's unicode.IsSpace omits the C0 information separators
// U+001C-U+001F, which str.strip() removes. Verified exhaustively against
// CPython 3.12 (Unicode 15.0) over every code point.
func pyIsSpace(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

// pyStrip mirrors Python str.strip(). Sibling copies live in internal/opll and
// internal/logs; keep them in step.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyLower mirrors Python str.lower(). strings.ToLower applies only the SIMPLE
// case mapping, so it turns U+0130 (İ) into "i" where Python produces "i" plus
// a combining dot — which email_key then renders as an extra "_" separator.
// Verified against CPython over every code point.
//
// The one remaining gap is the context-sensitive final sigma (Python lowers a
// word-final Σ to ς, Go to σ). Every consumer in this package immediately
// filters the result down to [a-z0-9] or compares it against an ASCII literal,
// so both spellings collapse to the same output; implementing the word-boundary
// rule here would add a branch nothing can observe.
func pyLower(s string) string {
	if strings.ContainsRune(s, 'İ') {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

// pyFloatRepr mirrors CPython repr() of a float, which is what str() of a
// json-decoded JSON float returns. Sibling copy in internal/opll.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if strings.HasPrefix(e, "-") {
		sign, e = "-", e[1:]
	}
	mant, expPart, _ := strings.Cut(e, "e")
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return sign + e
	}
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1
	// CPython float_repr_style 'r': exponent form when decpt <= -4 || decpt > 16.
	if decpt <= -4 || decpt > 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		return fmt.Sprintf("%s%se%+03d", sign, out, decpt-1)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}

// pyNumStr mirrors Python str() of a number that came out of json.loads.
// json.Number keeps the WIRE literal, but Python already turned it into an int
// (arbitrary precision) or a float whose str() is repr(), so "2000.00" prints
// as "2000.0", "1e3" as "1000.0", "1.5e-7" as "1.5e-07" and "-0" as "0". Those
// spellings land inside the synthetic id_token's base64 payload and in every
// account_id / plan_type string, so json.Number.String() is not usable here.
func pyNumStr(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return s
	}
	return pyFloatRepr(f)
}

// pyReprStr mirrors Python repr() of a str, needed verbatim inside the
// int() ValueError message convert re-raises (app.py:5352).
func pyReprStr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case !unicode.IsPrint(r):
			switch {
			case r < 0x100:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyStr mirrors Python's str(value) for the value kinds that can reach these
// builders (whatever json.loads produces). It is only used where app.py itself
// calls str().
//
// DIVERGENCE: str() of a multi-key dict prints Python's INSERTION order and Go
// has already lost it (encoding/json decodes an object into an unordered map),
// so the keys are sorted here to stay deterministic. Only reachable when a JSON
// object is supplied where the record wants a string (e.g. "email": {...}),
// which is corrupt input either way; single-key objects and lists are exact.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case json.Number:
		return pyNumStr(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, pyRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, pyReprStr(k)+": "+pyRepr(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// pyRepr mirrors Python repr() of a json.loads value.
func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return pyReprStr(s)
	}
	return pyStr(v)
}

// pyFirstNonEmpty ports first_non_empty (app.py:2667-2674): the first value
// whose str().strip() is non-empty, and it returns the STRIPPED text.
//
// openai.FirstNonEmpty is deliberately NOT used: it trims with
// strings.TrimSpace (which leaves U+001C-U+001F in place) and stringifies a
// json.Number by its wire literal, so "1e3" would reach an exported account_id
// as "1e3" where Python writes "1000.0". Both differences are visible in the
// exported bytes. See the report note about the same bug in internal/openai.
func pyFirstNonEmpty(values ...any) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		if text := pyStrip(pyStr(v)); text != "" {
			return text
		}
	}
	return ""
}

// pyTruthy mirrors Python truthiness: None / False / "" / 0 / empty container
// are falsy, everything else is truthy. Used for `record.get("disabled")`
// (app.py:5313, 5353) and the `record.get("priority") or ""` chain
// (app.py:5352). strings.TrimSpace is NOT Python truthiness — " " is truthy.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil {
			return t.String() != ""
		}
		return f != 0
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
	case float32:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	case *OrderedMap:
		return t.Len() > 0
	default:
		return true
	}
}

// pyIsNumber mirrors `isinstance(value, (int, float))`.
//
// json.Number is included because Python's json.loads turns a JSON number into
// an int/float, and Go's decoder (with UseNumber) turns it into json.Number.
// bool is included because in Python bool is a subclass of int, so True takes
// the numeric branch of normalize_iso_timestamp (app.py:5084).
// Numeric *strings* are deliberately excluded — Python's isinstance rejects
// them and the string branch then fails fromisoformat.
func pyIsNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil {
			return 0, false
		}
		return f, true
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
	case float32:
		return float64(t), true
	case float64:
		return t, true
	default:
		return 0, false
	}
}

// pyFloat mirrors Python's float(value): numbers pass through, numeric strings
// are parsed, everything else raises (ok=false).
//
// strings.TrimSpace (NOT pyStrip) is correct here: float()/int() strip exactly
// Go's unicode.IsSpace set and reject U+001C-U+001F, which is why
// int("\x1c7\x1f") is a ValueError even though "\x1c7\x1f".strip() is "7".
func pyFloat(v any) (float64, bool) {
	if f, ok := pyIsNumber(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// pyIntTrunc mirrors Python's int(float) — truncation toward zero, not floor.
func pyIntTrunc(f float64) int64 {
	return int64(f)
}

// pyExtraDigitRanges are the code points str.isdigit() accepts that are NOT in
// Unicode's Nd category (superscripts, circled and parenthesised digits, ...).
// Generated from CPython 3.12 / Unicode 15.0 and pinned by a table test: they
// pass the isdigit() gate at app.py:5352 and then make int() raise, which skips
// the whole account.
var pyExtraDigitRanges = [][2]rune{
	{0x00b2, 0x00b3}, {0x00b9, 0x00b9}, {0x1369, 0x1371}, {0x19da, 0x19da},
	{0x2070, 0x2070}, {0x2074, 0x2079}, {0x2080, 0x2089},
	{0x2460, 0x2468}, {0x2474, 0x247c}, {0x2488, 0x2490},
	{0x24ea, 0x24ea}, {0x24f5, 0x24fd}, {0x24ff, 0x24ff},
	{0x2776, 0x277e}, {0x2780, 0x2788}, {0x278a, 0x2792},
	{0x10a40, 0x10a43}, {0x10e60, 0x10e68}, {0x11052, 0x1105a},
	{0x1f100, 0x1f10a},
}

// pyIsDigitString mirrors str.isdigit(): non-empty and every rune a digit.
func pyIsDigitString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsDigit(r) {
			continue
		}
		extra := false
		for _, rg := range pyExtraDigitRanges {
			if r >= rg[0] && r <= rg[1] {
				extra = true
				break
			}
		}
		if !extra {
			return false
		}
	}
	return true
}

// errPyValueError carries a Python ValueError message verbatim so the caller
// can reproduce app.py's `f"{email}: {exc}"` skip line.
type errPyValueError struct{ msg string }

func (e errPyValueError) Error() string { return e.msg }

// pyIntFromValue mirrors Python's int(value) for the shapes that reach the
// priority parse (app.py:5352): a JSON number or a digit string.
//
// The result is a json.Number so an arbitrary-precision Python int survives
// into the output document — encoding/json writes a json.Number verbatim, and
// int64 would silently wrap on `"priority": 99999999999999999999999`.
func pyIntFromValue(v any) (json.Number, error) {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if !strings.ContainsAny(s, ".eE") {
			if b, ok := new(big.Int).SetString(s, 10); ok {
				return json.Number(b.String()), nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", errPyValueError{"invalid literal for int() with base 10: " + pyReprStr(s)}
		}
		return json.Number(big.NewFloat(math.Trunc(f)).Text('f', 0)), nil
	case bool:
		if t {
			return "1", nil
		}
		return "0", nil
	case string:
		// int(str) strips only unicode.IsSpace, accepts a sign and '_' digit
		// separators, and rejects every non-Nd "digit" rune.
		text := strings.TrimSpace(t)
		neg := false
		if strings.HasPrefix(text, "+") || strings.HasPrefix(text, "-") {
			neg = text[0] == '-'
			text = text[1:]
		}
		var digits strings.Builder
		prevDigit := false
		for i, r := range text {
			if r == '_' {
				if !prevDigit || i+1 >= len(text) {
					return "", errPyValueError{"invalid literal for int() with base 10: " + pyReprStr(t)}
				}
				prevDigit = false
				continue
			}
			if !unicode.IsDigit(r) {
				return "", errPyValueError{"invalid literal for int() with base 10: " + pyReprStr(t)}
			}
			digits.WriteRune(rune('0' + digitValue(r)))
			prevDigit = true
		}
		if digits.Len() == 0 {
			return "", errPyValueError{"invalid literal for int() with base 10: " + pyReprStr(t)}
		}
		b, ok := new(big.Int).SetString(digits.String(), 10)
		if !ok {
			return "", errPyValueError{"invalid literal for int() with base 10: " + pyReprStr(t)}
		}
		if neg {
			b.Neg(b)
		}
		return json.Number(b.String()), nil
	}
	return "", errPyValueError{"int() argument must be a string, a bytes-like object or a real number"}
}

// digitValue is unicodedata.decimal for an Nd rune. Unicode allocates decimal
// digits in aligned blocks of ten starting at that script's zero, and Go's Nd
// table is a set of maximal runs of such blocks, so the offset within the
// containing range modulo ten is the digit value. A table test pins this
// against CPython for every Nd code point.
func digitValue(r rune) int {
	for _, rg := range unicode.Nd.R16 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return int(r-'0') % 10
}
