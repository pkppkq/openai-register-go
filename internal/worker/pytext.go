package worker

// pytext.go holds the character-class equivalences this package needs in order
// to reproduce Python's `re` and `str` semantics on RENDERED page text.
//
// All three were established by exhaustively comparing CPython against Go over
// U+0000..U+10FFFF; they are not approximations:
//
//	Python str-mode `\s`  ==  Go [\s\x{000B}\x{001C}-\x{001F}\x{0085}\p{Z}]
//	Python str-mode `\w`  ==  Go [\p{L}\p{N}_]
//	Python str-mode `\d`  ==  Go \p{Nd}   (680 code points, Unicode 15.0.0)
//
// Go's own spellings are wrong here in OPPOSITE directions, which is why every
// use site matters:
//
//   - RE2 `\s` is [\t\n\f\r ]: no VT, no NBSP, no ideographic space, no U+2028.
//     page.innerText() on a localized OpenAI page routinely contains NBSP, so an
//     ASCII-only `\s` makes Go MISS cues Python matched ("Date of birth"
//     was classified birth_year instead of birth_date).
//   - RE2 `\b` is an ASCII word boundary, so it sees a boundary between a CJK
//     character and an ASCII letter where Python (whose `\w` includes CJK) sees
//     none. That makes Go match cues Python did NOT ("コードage" -> age).
//   - strings.TrimSpace omits U+001C..U+001F, which str.strip() removes.
//   - RE2 `\d` is [0-9], so Go DROPS a fullwidth １ or an Arabic-Indic ١ that
//     Python's `\d` matches. Rendered form values are exactly where those show
//     up, and strconv.Atoi rejects them even once the match succeeds — so the
//     digit class and the integer parse have to be widened together.
//
// A Python-equivalent spelling is therefore mandatory in every regex applied to
// page text or to DOM attribute blobs.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pyWSClass is the BODY of a character class equal to Python's str-mode `\s`.
const pyWSClass = `\s\x{000B}\x{001C}-\x{001F}\x{0085}\p{Z}`

// pyWS is one Python-`\s` character, ready to embed in a pattern.
const pyWS = `[` + pyWSClass + `]`

// pyWordChar is the BODY of a character class equal to Python's str-mode `\w`.
const pyWordChar = `\p{L}\p{N}_`

// pyNonWord is one Python-non-`\w` character.
const pyNonWord = `[^` + pyWordChar + `]`

// pyWhitespaceRun is the matcher of `re.sub(r"\s+", " ", text)`.
var pyWhitespaceRun = regexp.MustCompile(pyWS + `+`)

// pyB wraps a pattern in Python-compatible (Unicode-aware) word boundaries,
// standing in for `\b(...)\b`.
//
// RE2 has no lookaround, so the boundary characters are CONSUMED. That is safe
// for every call site here because all of them use the result as a boolean
// MatchString predicate: the engine retries at every start offset, so the only
// thing consumption can lose is a SECOND match that shares its delimiter with
// the first, and by then the predicate is already true.
func pyB(inner string) string {
	return `(?:^|` + pyNonWord + `)(?:` + inner + `)(?:$|` + pyNonWord + `)`
}

// pyStripCutset is exactly the 29 code points Python's str.strip() removes.
// unicode.IsSpace (hence strings.TrimSpace) covers 25 of them and omits the
// four information separators U+001C..U+001F.
const pyStripCutset = "\t\n\v\f\r  " +
	"            " +
	"    　"

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.Trim(s, pyStripCutset) }

// pyCollapseStrip is `re.sub(r"\s+", " ", text).strip()`, the normalisation
// every page-text summary in this package performs.
func pyCollapseStrip(s string) string {
	return pyStrip(pyWhitespaceRun.ReplaceAllString(s, " "))
}

// pyDigit is the BODY of a character class equal to Python's str-mode `\d`.
const pyDigit = `\p{Nd}`

// pyDigitValue is Python's int(ch) for a single character: the decimal value of
// a Unicode digit, or (0, false) for anything outside category Nd.
//
// Nd is laid out as complete, aligned runs of ten — a property Unicode
// guarantees for decimal digits and that the tests below re-verify against every
// Nd code point rather than trusting it — so the offset from the start of the
// containing range, modulo ten, IS the value. Several Nd ranges hold more than
// one run (U+1D7CE..U+1D7FF is five), which is why walking backwards to the
// nearest non-Nd rune would be wrong.
func pyDigitValue(r rune) (int, bool) {
	if r <= unicode.MaxLatin1 {
		if r >= '0' && r <= '9' {
			return int(r - '0'), true
		}
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int((r - rune(rg.Lo)) % 10), true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int((r - rune(rg.Lo)) % 10), true
		}
	}
	return 0, false
}

// pyIntDigits is Python's int(s) restricted to what the `\d`-shaped patterns in
// this package can hand it: a non-empty run of Unicode decimal digits, no sign
// and no surrounding whitespace. Reports false for anything else.
//
// Not a general int(): Python accepts a sign, surrounding whitespace and any
// number of digits, and this deliberately does not. Every caller here has
// already fullmatched at most FOUR digits, so the narrower contract is the whole
// of what they need — and pyIntDigitsMax keeps the accumulator away from
// overflow on a 32-bit int without an overflow branch that could never be
// exercised.
const pyIntDigitsMax = 9

func pyIntDigits(s string) (int, bool) {
	if s == "" || len(s) > pyIntDigitsMax*utf8.UTFMax {
		return 0, false
	}
	n, count := 0, 0
	for _, r := range s {
		v, ok := pyDigitValue(r)
		if !ok {
			return 0, false
		}
		count++
		if count > pyIntDigitsMax {
			return 0, false
		}
		n = n*10 + v
	}
	return n, true
}

// pyRuneSlice is Python's s[:n] on a str, which counts CODE POINTS. Go's s[:n]
// counts bytes and will split a multi-byte digit down the middle.
func pyRuneSlice(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
