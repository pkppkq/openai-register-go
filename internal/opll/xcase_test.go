package opll

// SCRATCH — deleted at the end of the sweep.
// Checks whether Go's unicode tables reproduce CPython 3.12's Cased /
// Case_Ignorable derived properties exactly.

import (
	"encoding/json"
	"os"
	"testing"
)

func TestXCaseProps(t *testing.T) {
	path := os.Getenv("OPLL_CASEPROPS")
	if path == "" {
		t.Skip("no OPLL_CASEPROPS")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cased     [][2]rune `json:"cased"`
		Ignorable [][2]rune `json:"ignorable"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	wantIgn := map[rune]bool{}
	for _, r := range doc.Ignorable {
		for c := r[0]; c <= r[1]; c++ {
			wantIgn[c] = true
		}
	}
	// CPython's probe reports "cased" only for code points that are NOT also
	// case-ignorable, so compare against pyCased && !pyCaseIgnorable.
	wantCased := map[rune]bool{}
	for _, r := range doc.Cased {
		for c := r[0]; c <= r[1]; c++ {
			wantCased[c] = true
		}
	}
	badC, badI := 0, 0
	for c := rune(0); c < 0x110000; c++ {
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		if got := pyCased(c) && !pyCaseIgnorable(c); got != wantCased[c] {
			if badC < 12 {
				t.Errorf("cased(%#06x) go=%v py=%v", c, got, wantCased[c])
			}
			badC++
		}
		if got := pyCaseIgnorable(c); got != wantIgn[c] {
			if badI < 12 {
				t.Errorf("case_ignorable(%#06x) go=%v py=%v", c, got, wantIgn[c])
			}
			badI++
		}
	}
	t.Logf("cased mismatches=%d case_ignorable mismatches=%d", badC, badI)
}

func TestXFinalSigmaExhaustive(t *testing.T) {
	path := os.Getenv("OPLL_SIGMA")
	if path == "" {
		t.Skip("no OPLL_SIGMA")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string // {input, python str.lower()}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		if got := pyLower(c[0]); got != c[1] {
			if bad < 20 {
				t.Errorf("pyLower(%q) = %q, Python says %q", c[0], got, c[1])
			}
			bad++
		}
	}
	t.Logf("checked %d, mismatches=%d", len(cases), bad)
}
