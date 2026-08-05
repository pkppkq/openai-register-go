package worker

// pytext_test.go pins the three Python character-class equivalences pytext.go
// claims, against ground truth taken from CPython 3.12 (Unicode 15.0.0) rather
// than from Go's own tables.
//
// pyDigitValue is the one with a real assumption inside it: that category Nd is
// laid out as complete, ALIGNED runs of ten, so the offset from the start of the
// containing unicode.Nd range modulo ten is the digit's value. That is true of
// Unicode 15, and TestNdRunsAreAlignedTensOfDigits re-derives it from the table
// Go ships rather than trusting it — if a future Unicode revision ever added a
// partial run, aboutYouYearOK would start reading years wrong instead of
// failing loudly.

import (
	"testing"
	"unicode"
)

// ndRunStarts is every code point c where CPython says
// unicodedata.category(chr(c)) == "Nd" and int(chr(c)) == 0 — i.e. the zero of
// each decimal-digit run. 68 runs, 680 code points, Unicode 15.0.0. Generated,
// not typed.
var ndRunStarts = []rune{
	0x0030, 0x0660, 0x06F0, 0x07C0, 0x0966, 0x09E6, 0x0A66, 0x0AE6,
	0x0B66, 0x0BE6, 0x0C66, 0x0CE6, 0x0D66, 0x0DE6, 0x0E50, 0x0ED0,
	0x0F20, 0x1040, 0x1090, 0x17E0, 0x1810, 0x1946, 0x19D0, 0x1A80,
	0x1A90, 0x1B50, 0x1BB0, 0x1C40, 0x1C50, 0xA620, 0xA8D0, 0xA900,
	0xA9D0, 0xA9F0, 0xAA50, 0xABF0, 0xFF10, 0x104A0, 0x10D30, 0x11066,
	0x110F0, 0x11136, 0x111D0, 0x112F0, 0x11450, 0x114D0, 0x11650, 0x116C0,
	0x11730, 0x118E0, 0x11950, 0x11C50, 0x11D50, 0x11DA0, 0x11F50, 0x16A60,
	0x16AC0, 0x16B50, 0x1D7CE, 0x1D7D8, 0x1D7E2, 0x1D7EC, 0x1D7F6, 0x1E140,
	0x1E2F0, 0x1E4F0, 0x1E950, 0x1FBF0,
}

// TestPyDigitValueMatchesCPython walks every run CPython reported and checks all
// ten members, so all 680 Nd code points are covered.
func TestPyDigitValueMatchesCPython(t *testing.T) {
	for _, zero := range ndRunStarts {
		for want := 0; want < 10; want++ {
			r := zero + rune(want)
			got, ok := pyDigitValue(r)
			if !ok {
				t.Fatalf("pyDigitValue(U+%04X) not a digit, python says %d", r, want)
			}
			if got != want {
				t.Fatalf("pyDigitValue(U+%04X) = %d, python says %d", r, got, want)
			}
		}
	}
}

// TestPyDigitValueRejectsNonDigits covers the near-misses: characters Go's
// unicode package or a careless reader might mistake for decimal digits.
// Python's \d matches none of them (str.isdigit() is true for the superscripts,
// but re's \d and int() are Nd-only).
func TestPyDigitValueRejectsNonDigits(t *testing.T) {
	for _, r := range []rune{
		'a', ' ', '-', '.', 0x00B2, /* ² SUPERSCRIPT TWO, No */
		0x00BD /* ½ VULGAR FRACTION, No */, 0x2160, /* Ⅰ ROMAN NUMERAL, Nl */
		0x3007 /* 〇 IDEOGRAPHIC NUMBER ZERO, Nl */, 0x4E00, /* 一, Lo */
		0x1F100, /* 🄀 DIGIT ZERO FULL STOP, No */
	} {
		if _, ok := pyDigitValue(r); ok {
			t.Fatalf("pyDigitValue(U+%04X) accepted; python's \\d rejects it", r)
		}
	}
}

// TestNdRunsAreAlignedTensOfDigits re-derives pyDigitValue's structural
// assumption from Go's own unicode.Nd table instead of assuming it: every range
// must have stride 1 and a length that is a whole multiple of ten, and every
// run start must be one CPython reported.
func TestNdRunsAreAlignedTensOfDigits(t *testing.T) {
	starts := map[rune]bool{}
	for _, r := range ndRunStarts {
		starts[r] = true
	}
	seen := 0
	check := func(lo, hi rune, stride uint32) {
		if stride != 1 {
			t.Fatalf("unicode.Nd range U+%04X..U+%04X has stride %d; pyDigitValue's modulo is only valid for stride 1", lo, hi, stride)
		}
		n := int(hi-lo) + 1
		if n%10 != 0 {
			t.Fatalf("unicode.Nd range U+%04X..U+%04X holds %d code points, not a whole number of ten-digit runs", lo, hi, n)
		}
		for off := rune(0); off < rune(n); off += 10 {
			if !starts[lo+off] {
				t.Fatalf("U+%04X is a run start in Go's table but not in CPython's", lo+off)
			}
			seen++
		}
	}
	for _, rg := range unicode.Nd.R16 {
		check(rune(rg.Lo), rune(rg.Hi), uint32(rg.Stride))
	}
	for _, rg := range unicode.Nd.R32 {
		check(rune(rg.Lo), rune(rg.Hi), rg.Stride)
	}
	if seen != len(ndRunStarts) {
		t.Fatalf("Go's unicode.Nd holds %d runs, CPython 3.12 reported %d — the tables are from different Unicode versions", seen, len(ndRunStarts))
	}
}

// TestPyIntDigits is int(s) for the digit-only strings the `pyDigit` patterns
// hand it.
func TestPyIntDigits(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0", 0, true},
		{"1990", 1990, true},
		{"١٩٩٠", 1990, true}, // ١٩٩٠
		{"１９９０", 1990, true}, // １９９０
		{"۱۹۹۰", 1990, true}, // ۱۹۹۰
		{"\U0001d7d9\U0001d7e1\U0001d7e1\U0001d7d8", 1990, true}, // 𝟙𝟡𝟡𝟘 double-struck
		{"١٩９٠", 1990, true},                                     // mixed scripts; int() does not care
		{"٣٤", 34, true},
		{"", 0, false},
		{"19a0", 0, false},
		{" 1990", 0, false}, // the callers fullmatch first, so no strip here
		{"+1990", 0, false}, // ditto for a sign
		{"123456789", 123456789, true},
		{"1234567890", 0, false}, // past pyIntDigitsMax; Python would say 1234567890
	}
	for _, tt := range tests {
		got, ok := pyIntDigits(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("pyIntDigits(%q) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// TestPyRuneSlice is s[:n] on a str, which counts code points. The astral cases
// are the ones a byte slice mangles: ParseAboutYouBirthdate does text[:4] on a
// string that may be four MATHEMATICAL BOLD digits, sixteen bytes.
func TestPyRuneSlice(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"1990-05-17", 4, "1990"},
		{"١٩٩٠-05-17", 4, "١٩٩٠"},
		{"\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1", 4, "\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1"},
		{"\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1x", 4, "\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1"},
		{"ab", 4, "ab"}, // Python clamps rather than panicking
		{"", 4, ""},
		{"abcd", 0, ""},
	}
	for _, tt := range tests {
		if got := pyRuneSlice(tt.in, tt.n); got != tt.want {
			t.Errorf("pyRuneSlice(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// TestPyStripCutsetIsTwentyNine guards the constant against an editor or a
// diff tool quietly normalising one of the exotic spaces away.
func TestPyStripCutsetIsTwentyNine(t *testing.T) {
	seen := map[rune]bool{}
	for _, r := range pyStripCutset {
		if seen[r] {
			t.Fatalf("U+%04X appears twice in pyStripCutset", r)
		}
		seen[r] = true
	}
	if len(seen) != 29 {
		t.Fatalf("pyStripCutset holds %d distinct code points, str.strip() removes 29", len(seen))
	}
	// The four str.strip() removes and strings.TrimSpace does not.
	for r := rune(0x1C); r <= 0x1F; r++ {
		if !seen[r] {
			t.Fatalf("U+%04X missing from pyStripCutset", r)
		}
	}
}
