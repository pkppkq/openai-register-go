package phoneprovider

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestNormalizeSettings(t *testing.T) {
	tests := []struct {
		name    string
		raw     Raw
		want    Settings
		wantErr error
	}{
		{
			name: "empty falls back to dr/33",
			raw:  Raw{Enabled: true, APIKey: "  key  "},
			want: Settings{Enabled: true, APIKey: "key", Service: "dr", Country: "33"},
		},
		{
			// Python strips first and then applies `or DEFAULT`, so whitespace-only
			// input becomes the default, not " ".
			name: "whitespace-only falls back to the defaults",
			raw:  Raw{Service: "   ", Country: "\t", MaxPrice: "  "},
			want: Settings{Service: "dr", Country: "33"},
		},
		{
			name: "valid custom values",
			raw:  Raw{Service: "go_1", Country: "187", MaxPrice: " 0.07 "},
			want: Settings{Service: "go_1", Country: "187", MaxPrice: "0.07"},
		},
		{name: "service with dash", raw: Raw{Service: "go-1"}, wantErr: ErrBadService},
		{name: "service with inner space", raw: Raw{Service: "go 1"}, wantErr: ErrBadService},
		{name: "country not digits", raw: Raw{Country: "us"}, wantErr: ErrBadCountry},
		{name: "country with sign", raw: Raw{Country: "+33"}, wantErr: ErrBadCountry},
		{name: "max price zero", raw: Raw{MaxPrice: "0"}, wantErr: ErrBadMaxPrice},
		{name: "max price negative", raw: Raw{MaxPrice: "-1"}, wantErr: ErrBadMaxPrice},
		{name: "max price garbage", raw: Raw{MaxPrice: "abc"}, wantErr: ErrBadMaxPrice},
		{
			// float("nan") <= 0 is False in Python, so "nan" survives validation and
			// later makes every tier ineligible. Reproduced deliberately.
			name: "nan passes validation like Python",
			raw:  Raw{MaxPrice: "nan"},
			want: Settings{Service: "dr", Country: "33", MaxPrice: "nan"},
		},
		{
			name: "scientific notation is a valid float",
			raw:  Raw{MaxPrice: "5e-2"},
			want: Settings{Service: "dr", Country: "33", MaxPrice: "5e-2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSettings(tc.raw)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseReceiveLimit(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"0", 0},
		{"3", 3},
		{" 3 ", 3},  // Python's int() strips whitespace
		{"3.5", 0},  // int("3.5") raises -> except -> 0
		{"0x10", 0}, // int("0x10") raises -> 0
		{"-2", 0},   // max(0, ...)
		{"abc", 0},  // bare except -> 0
		{"+5", 5},   // int("+5") == 5
		{"99999", 99999},
	}
	for _, tc := range tests {
		if got := ParseReceiveLimit(tc.in); got != tc.want {
			t.Errorf("ParseReceiveLimit(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestIsFrozen(t *testing.T) {
	tests := []struct {
		name  string
		count int
		limit int
		want  bool
	}{
		{"limit 0 means unlimited", 100, 0, false},
		{"below the cap", 1, 2, false},
		{"at the cap is already frozen", 2, 2, true},
		{"above the cap", 5, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsFrozen(models.PhoneEntry{ReceiveCount: tc.count}, tc.limit)
			if got != tc.want {
				t.Fatalf("IsFrozen(count=%d, limit=%d) = %v, want %v", tc.count, tc.limit, got, tc.want)
			}
		})
	}
}

// TestFormatG guards the string that becomes the SMSBower maxPrice parameter:
// Go's default shortest 'g' formatting is NOT Python's %g, and a wrong cap here
// buys a more expensive number than the user allowed.
func TestFormatG(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0.07, "0.07"},
		{0.1, "0.1"},
		{1, "1"},
		{1.5, "1.5"},
		{0.123456789, "0.123457"}, // 6 significant digits, like Python's %g
		{1e6, "1e+06"},
		{0.0000001, "1e-07"},
		{12.0, "12"},
	}
	for _, tc := range tests {
		if got := formatG(tc.in); got != tc.want {
			t.Errorf("formatG(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

const (
	nbsp             = " "
	ideographicSpace = "　"
)

func TestExtractPhoneCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"openai pattern", "Your OpenAI code is 123456", "123456"},
		{"openai pattern is case-insensitive", "your openai verification code: 654321", "654321"},
		{
			// 21 NBSPs are wider than the [^\d]{0,20} window, so this only matches
			// because Python's Unicode-aware \s collapses them into a single space
			// first. Go's ASCII-only \s would leave them in place and find nothing.
			name: "nbsp run is collapsed before the fixed-width window",
			in:   "验证码" + strings.Repeat(nbsp, 21) + "112233",
			want: "112233",
		},
		{
			name: "ideographic space counts as whitespace too",
			in:   "验证代码" + strings.Repeat(ideographicSpace, 21) + "445577",
			want: "445577",
		},
		{"验证代码 before 验证码", "验证代码 445566", "445566"},
		{"bare six digits with punctuation around", "code: (778899).", "778899"},
		{"bare six digits alone", "334455", "334455"},
		{
			// Python's \b is Unicode-aware: ド and 1 are both word characters, so
			// there is no boundary and the fourth pattern does NOT match. Go's
			// ASCII-only \b would have matched here and returned a bogus code.
			name: "no match when digits touch a CJK letter",
			in:   "コード123456",
			want: "",
		},
		{
			// Same trap on the Latin side, where Python and Go agree.
			name: "no match when digits touch an ascii letter",
			in:   "ref123456",
			want: "",
		},
		{"seven digit run has no interior boundary", "1234567", ""},
		{"five digits are not a code", "12345", ""},
		{"underscore is a word char so no boundary", "_123456", ""},
		{
			name: "openai window accepts exactly 80 non-digits",
			in:   "OpenAI" + strings.Repeat(".", 80) + "123456x",
			want: "123456",
		},
		{
			// 81 non-digits overflow the window; the bare-digit fallback then fails
			// too because the run is followed by a word character.
			name: "openai window rejects 81 non-digits",
			in:   "OpenAI" + strings.Repeat(".", 81) + "123456x",
			want: "",
		},
		{"digits inside a longer word are ignored", "abc123456def", ""},
		{"empty input", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPhoneCode(tc.in); got != tc.want {
				t.Fatalf("extractPhoneCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
