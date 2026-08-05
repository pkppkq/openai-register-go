package authproto

import (
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// str.lower() and str.casefold()
//
// strings.ToLower is SIMPLE case mapping. CPython's str.lower() is the FULL
// mapping (U+0130 lowercases to two code points) and is context sensitive
// (final sigma), and str.casefold() is a third mapping again. The ported code
// folds attacker-influenced text and then looks for ASCII markers in it, so a
// fold that produces different ASCII from CPython's is a behaviour change:
// casefold("<U+1E9E>L") is "ssl" and matches is_transient_http_error's "ssl"
// marker, while strings.ToLower gives "<U+00DF>l" and does not.
// ---------------------------------------------------------------------------

// pyLower is CPython's str.lower(). Two things separate it from
// strings.ToLower, and both are reproduced here:
//
//   - U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE lowercases to the TWO code
//     points "i" + U+0307. strings.ToLower yields a bare "i", which can
//     manufacture an ASCII marker match CPython would not make.
//   - Final sigma: a U+03A3 that ends a word lowercases to U+03C2, not U+03C3.
//     CPython implements the Unicode Final_Sigma condition; Go does not.
//
// Python line refs for the call sites: app.py:8307 (error_msg.lower()) and
// app.py:8660 (_response_has_auth_challenge).
func pyLower(s string) string {
	if isASCIIOnly(s) {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == 0x0130:
			b.WriteString("i\u0307")
		case r == 0x03A3 && isFinalSigma(runes, i):
			b.WriteRune(0x03C2)
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// isFinalSigma is the Unicode Final_Sigma context condition: the sigma is
// preceded by a cased letter (ignoring case-ignorable code points) and is NOT
// followed by one.
func isFinalSigma(runes []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if isCaseIgnorable(runes[j]) {
			continue
		}
		before = isCased(runes[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(runes); j++ {
		if isCaseIgnorable(runes[j]) {
			continue
		}
		return !isCased(runes[j])
	}
	return true
}

// isCased is the Unicode Cased derived property: Lowercase + Uppercase + Lt.
func isCased(r rune) bool {
	return unicode.IsLower(r) || unicode.IsUpper(r) || unicode.Is(unicode.Lt, r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

// isCaseIgnorable is the Unicode Case_Ignorable derived property: Mn, Me, Cf,
// Lm, Sk plus the Word_Break MidLetter / MidNumLet / Single_Quote classes.
func isCaseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', 0x00A8, 0x00AD, 0x00AF, 0x00B4, 0x00B7, 0x00B8,
		0x02D8, 0x02D9, 0x02DA, 0x02DB, 0x02DC, 0x02DD, 0x0387, 0x055F, 0x05F4,
		0x2018, 0x2019, 0x2024, 0x2027, 0x2054, 0xFE13, 0xFE52, 0xFE55, 0xFF07,
		0xFF0E, 0xFF1A, 0xFF3E, 0xFF40, 0xFFE3:
		return true
	}
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) ||
		unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Lm, r) ||
		unicode.Is(unicode.Sk, r)
}

// pyCasefold is CPython's str.casefold(): the FULL case folding of
// CaseFolding.txt, which is neither strings.ToLower nor pyLower.
// is_transient_http_error (app.py:5605) folds with it before looking for its
// ASCII markers.
//
// Unlike pyLower it is NOT context sensitive -- casefold("A" + U+03A3) is
// "a" + U+03C3, where lower() would give the final sigma U+03C2 -- so the two
// must not be layered on each other.
func pyCasefold(s string) string {
	if isASCIIOnly(s) {
		return strings.ToLower(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if folded, special := pyCasefoldExceptions[r]; special {
			b.WriteString(folded)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func isASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pyCasefoldExceptions is every code point whose CPython str.casefold() differs
// from unicode.ToLower, plus U+0130 (whose str.lower() is itself two code
// points). Generated from CPython 3.12 / Unicode 15.0.0 by the differential
// harness; regenerate rather than editing by hand.
var pyCasefoldExceptions = map[rune]string{
	// 298 entries
	0x00B5: "\u03bc",
	0x00DF: "\u0073\u0073",
	0x0130: "\u0069\u0307",
	0x0149: "\u02bc\u006e",
	0x017F: "\u0073",
	0x01F0: "\u006a\u030c",
	0x0345: "\u03b9",
	0x0390: "\u03b9\u0308\u0301",
	0x03B0: "\u03c5\u0308\u0301",
	0x03C2: "\u03c3",
	0x03D0: "\u03b2",
	0x03D1: "\u03b8",
	0x03D5: "\u03c6",
	0x03D6: "\u03c0",
	0x03F0: "\u03ba",
	0x03F1: "\u03c1",
	0x03F5: "\u03b5",
	0x0587: "\u0565\u0582",
	0x13A0: "\u13a0",
	0x13A1: "\u13a1",
	0x13A2: "\u13a2",
	0x13A3: "\u13a3",
	0x13A4: "\u13a4",
	0x13A5: "\u13a5",
	0x13A6: "\u13a6",
	0x13A7: "\u13a7",
	0x13A8: "\u13a8",
	0x13A9: "\u13a9",
	0x13AA: "\u13aa",
	0x13AB: "\u13ab",
	0x13AC: "\u13ac",
	0x13AD: "\u13ad",
	0x13AE: "\u13ae",
	0x13AF: "\u13af",
	0x13B0: "\u13b0",
	0x13B1: "\u13b1",
	0x13B2: "\u13b2",
	0x13B3: "\u13b3",
	0x13B4: "\u13b4",
	0x13B5: "\u13b5",
	0x13B6: "\u13b6",
	0x13B7: "\u13b7",
	0x13B8: "\u13b8",
	0x13B9: "\u13b9",
	0x13BA: "\u13ba",
	0x13BB: "\u13bb",
	0x13BC: "\u13bc",
	0x13BD: "\u13bd",
	0x13BE: "\u13be",
	0x13BF: "\u13bf",
	0x13C0: "\u13c0",
	0x13C1: "\u13c1",
	0x13C2: "\u13c2",
	0x13C3: "\u13c3",
	0x13C4: "\u13c4",
	0x13C5: "\u13c5",
	0x13C6: "\u13c6",
	0x13C7: "\u13c7",
	0x13C8: "\u13c8",
	0x13C9: "\u13c9",
	0x13CA: "\u13ca",
	0x13CB: "\u13cb",
	0x13CC: "\u13cc",
	0x13CD: "\u13cd",
	0x13CE: "\u13ce",
	0x13CF: "\u13cf",
	0x13D0: "\u13d0",
	0x13D1: "\u13d1",
	0x13D2: "\u13d2",
	0x13D3: "\u13d3",
	0x13D4: "\u13d4",
	0x13D5: "\u13d5",
	0x13D6: "\u13d6",
	0x13D7: "\u13d7",
	0x13D8: "\u13d8",
	0x13D9: "\u13d9",
	0x13DA: "\u13da",
	0x13DB: "\u13db",
	0x13DC: "\u13dc",
	0x13DD: "\u13dd",
	0x13DE: "\u13de",
	0x13DF: "\u13df",
	0x13E0: "\u13e0",
	0x13E1: "\u13e1",
	0x13E2: "\u13e2",
	0x13E3: "\u13e3",
	0x13E4: "\u13e4",
	0x13E5: "\u13e5",
	0x13E6: "\u13e6",
	0x13E7: "\u13e7",
	0x13E8: "\u13e8",
	0x13E9: "\u13e9",
	0x13EA: "\u13ea",
	0x13EB: "\u13eb",
	0x13EC: "\u13ec",
	0x13ED: "\u13ed",
	0x13EE: "\u13ee",
	0x13EF: "\u13ef",
	0x13F0: "\u13f0",
	0x13F1: "\u13f1",
	0x13F2: "\u13f2",
	0x13F3: "\u13f3",
	0x13F4: "\u13f4",
	0x13F5: "\u13f5",
	0x13F8: "\u13f0",
	0x13F9: "\u13f1",
	0x13FA: "\u13f2",
	0x13FB: "\u13f3",
	0x13FC: "\u13f4",
	0x13FD: "\u13f5",
	0x1C80: "\u0432",
	0x1C81: "\u0434",
	0x1C82: "\u043e",
	0x1C83: "\u0441",
	0x1C84: "\u0442",
	0x1C85: "\u0442",
	0x1C86: "\u044a",
	0x1C87: "\u0463",
	0x1C88: "\ua64b",
	0x1E96: "\u0068\u0331",
	0x1E97: "\u0074\u0308",
	0x1E98: "\u0077\u030a",
	0x1E99: "\u0079\u030a",
	0x1E9A: "\u0061\u02be",
	0x1E9B: "\u1e61",
	0x1E9E: "\u0073\u0073",
	0x1F50: "\u03c5\u0313",
	0x1F52: "\u03c5\u0313\u0300",
	0x1F54: "\u03c5\u0313\u0301",
	0x1F56: "\u03c5\u0313\u0342",
	0x1F80: "\u1f00\u03b9",
	0x1F81: "\u1f01\u03b9",
	0x1F82: "\u1f02\u03b9",
	0x1F83: "\u1f03\u03b9",
	0x1F84: "\u1f04\u03b9",
	0x1F85: "\u1f05\u03b9",
	0x1F86: "\u1f06\u03b9",
	0x1F87: "\u1f07\u03b9",
	0x1F88: "\u1f00\u03b9",
	0x1F89: "\u1f01\u03b9",
	0x1F8A: "\u1f02\u03b9",
	0x1F8B: "\u1f03\u03b9",
	0x1F8C: "\u1f04\u03b9",
	0x1F8D: "\u1f05\u03b9",
	0x1F8E: "\u1f06\u03b9",
	0x1F8F: "\u1f07\u03b9",
	0x1F90: "\u1f20\u03b9",
	0x1F91: "\u1f21\u03b9",
	0x1F92: "\u1f22\u03b9",
	0x1F93: "\u1f23\u03b9",
	0x1F94: "\u1f24\u03b9",
	0x1F95: "\u1f25\u03b9",
	0x1F96: "\u1f26\u03b9",
	0x1F97: "\u1f27\u03b9",
	0x1F98: "\u1f20\u03b9",
	0x1F99: "\u1f21\u03b9",
	0x1F9A: "\u1f22\u03b9",
	0x1F9B: "\u1f23\u03b9",
	0x1F9C: "\u1f24\u03b9",
	0x1F9D: "\u1f25\u03b9",
	0x1F9E: "\u1f26\u03b9",
	0x1F9F: "\u1f27\u03b9",
	0x1FA0: "\u1f60\u03b9",
	0x1FA1: "\u1f61\u03b9",
	0x1FA2: "\u1f62\u03b9",
	0x1FA3: "\u1f63\u03b9",
	0x1FA4: "\u1f64\u03b9",
	0x1FA5: "\u1f65\u03b9",
	0x1FA6: "\u1f66\u03b9",
	0x1FA7: "\u1f67\u03b9",
	0x1FA8: "\u1f60\u03b9",
	0x1FA9: "\u1f61\u03b9",
	0x1FAA: "\u1f62\u03b9",
	0x1FAB: "\u1f63\u03b9",
	0x1FAC: "\u1f64\u03b9",
	0x1FAD: "\u1f65\u03b9",
	0x1FAE: "\u1f66\u03b9",
	0x1FAF: "\u1f67\u03b9",
	0x1FB2: "\u1f70\u03b9",
	0x1FB3: "\u03b1\u03b9",
	0x1FB4: "\u03ac\u03b9",
	0x1FB6: "\u03b1\u0342",
	0x1FB7: "\u03b1\u0342\u03b9",
	0x1FBC: "\u03b1\u03b9",
	0x1FBE: "\u03b9",
	0x1FC2: "\u1f74\u03b9",
	0x1FC3: "\u03b7\u03b9",
	0x1FC4: "\u03ae\u03b9",
	0x1FC6: "\u03b7\u0342",
	0x1FC7: "\u03b7\u0342\u03b9",
	0x1FCC: "\u03b7\u03b9",
	0x1FD2: "\u03b9\u0308\u0300",
	0x1FD3: "\u03b9\u0308\u0301",
	0x1FD6: "\u03b9\u0342",
	0x1FD7: "\u03b9\u0308\u0342",
	0x1FE2: "\u03c5\u0308\u0300",
	0x1FE3: "\u03c5\u0308\u0301",
	0x1FE4: "\u03c1\u0313",
	0x1FE6: "\u03c5\u0342",
	0x1FE7: "\u03c5\u0308\u0342",
	0x1FF2: "\u1f7c\u03b9",
	0x1FF3: "\u03c9\u03b9",
	0x1FF4: "\u03ce\u03b9",
	0x1FF6: "\u03c9\u0342",
	0x1FF7: "\u03c9\u0342\u03b9",
	0x1FFC: "\u03c9\u03b9",
	0xAB70: "\u13a0",
	0xAB71: "\u13a1",
	0xAB72: "\u13a2",
	0xAB73: "\u13a3",
	0xAB74: "\u13a4",
	0xAB75: "\u13a5",
	0xAB76: "\u13a6",
	0xAB77: "\u13a7",
	0xAB78: "\u13a8",
	0xAB79: "\u13a9",
	0xAB7A: "\u13aa",
	0xAB7B: "\u13ab",
	0xAB7C: "\u13ac",
	0xAB7D: "\u13ad",
	0xAB7E: "\u13ae",
	0xAB7F: "\u13af",
	0xAB80: "\u13b0",
	0xAB81: "\u13b1",
	0xAB82: "\u13b2",
	0xAB83: "\u13b3",
	0xAB84: "\u13b4",
	0xAB85: "\u13b5",
	0xAB86: "\u13b6",
	0xAB87: "\u13b7",
	0xAB88: "\u13b8",
	0xAB89: "\u13b9",
	0xAB8A: "\u13ba",
	0xAB8B: "\u13bb",
	0xAB8C: "\u13bc",
	0xAB8D: "\u13bd",
	0xAB8E: "\u13be",
	0xAB8F: "\u13bf",
	0xAB90: "\u13c0",
	0xAB91: "\u13c1",
	0xAB92: "\u13c2",
	0xAB93: "\u13c3",
	0xAB94: "\u13c4",
	0xAB95: "\u13c5",
	0xAB96: "\u13c6",
	0xAB97: "\u13c7",
	0xAB98: "\u13c8",
	0xAB99: "\u13c9",
	0xAB9A: "\u13ca",
	0xAB9B: "\u13cb",
	0xAB9C: "\u13cc",
	0xAB9D: "\u13cd",
	0xAB9E: "\u13ce",
	0xAB9F: "\u13cf",
	0xABA0: "\u13d0",
	0xABA1: "\u13d1",
	0xABA2: "\u13d2",
	0xABA3: "\u13d3",
	0xABA4: "\u13d4",
	0xABA5: "\u13d5",
	0xABA6: "\u13d6",
	0xABA7: "\u13d7",
	0xABA8: "\u13d8",
	0xABA9: "\u13d9",
	0xABAA: "\u13da",
	0xABAB: "\u13db",
	0xABAC: "\u13dc",
	0xABAD: "\u13dd",
	0xABAE: "\u13de",
	0xABAF: "\u13df",
	0xABB0: "\u13e0",
	0xABB1: "\u13e1",
	0xABB2: "\u13e2",
	0xABB3: "\u13e3",
	0xABB4: "\u13e4",
	0xABB5: "\u13e5",
	0xABB6: "\u13e6",
	0xABB7: "\u13e7",
	0xABB8: "\u13e8",
	0xABB9: "\u13e9",
	0xABBA: "\u13ea",
	0xABBB: "\u13eb",
	0xABBC: "\u13ec",
	0xABBD: "\u13ed",
	0xABBE: "\u13ee",
	0xABBF: "\u13ef",
	0xFB00: "\u0066\u0066",
	0xFB01: "\u0066\u0069",
	0xFB02: "\u0066\u006c",
	0xFB03: "\u0066\u0066\u0069",
	0xFB04: "\u0066\u0066\u006c",
	0xFB05: "\u0073\u0074",
	0xFB06: "\u0073\u0074",
	0xFB13: "\u0574\u0576",
	0xFB14: "\u0574\u0565",
	0xFB15: "\u0574\u056b",
	0xFB16: "\u057e\u0576",
	0xFB17: "\u0574\u056d",
}
