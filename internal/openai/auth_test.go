package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func jwt(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	seg := base64.RawURLEncoding.EncodeToString(b)
	return "aGVhZGVy." + seg + ".c2ln"
}

func TestPKCECodeChallenge(t *testing.T) {
	// RFC 7636 Appendix B worked example.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCECodeChallenge(verifier); got != want {
		t.Fatalf("PKCECodeChallenge = %q, want %q", got, want)
	}
}

func TestRandomURLSafeString(t *testing.T) {
	if got := RandomURLSafeString(43); len(got) != 43 {
		t.Fatalf("len = %d, want 43 (%q)", len(got), got)
	}
	if got := RandomURLSafeString(0); got != "" {
		t.Fatalf("RandomURLSafeString(0) = %q, want empty", got)
	}
	a, b := RandomURLSafeString(32), RandomURLSafeString(32)
	if a == b {
		t.Fatalf("two calls returned identical strings %q", a)
	}
}

func TestDecodeJWTPayload(t *testing.T) {
	tok := jwt(map[string]any{"exp": 123, "email": "a@b.com"})
	got := DecodeJWTPayload(tok)
	if got["email"] != "a@b.com" {
		t.Fatalf("email = %v, want a@b.com", got["email"])
	}
	if len(DecodeJWTPayload("not-a-jwt")) != 0 {
		t.Fatalf("malformed token should decode to empty map")
	}
	if len(DecodeJWTPayload("")) != 0 {
		t.Fatalf("empty token should decode to empty map")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty(nil, "", "   ", "x", "y"); got != "x" {
		t.Fatalf("FirstNonEmpty = %q, want x", got)
	}
	if got := FirstNonEmpty(nil, "", "  "); got != "" {
		t.Fatalf("FirstNonEmpty of all-empty = %q, want empty", got)
	}
}

func TestNormalizeOpenAIAuthRecord(t *testing.T) {
	access := jwt(map[string]any{
		"exp":                         4102444800, // 2100-01-01T00:00:00Z
		"email":                       "acc@ex.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_123"},
	})
	id := jwt(map[string]any{"email": "id@ex.com"})
	rec, err := NormalizeOpenAIAuthRecord("fallback@ex.com", map[string]any{
		"access_token":  access,
		"refresh_token": "rt_x",
		"id_token":      id,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rec.AccountID != "acc_123" {
		t.Fatalf("AccountID = %q, want acc_123", rec.AccountID)
	}
	if rec.Email != "id@ex.com" {
		t.Fatalf("Email = %q, want id@ex.com (id token wins)", rec.Email)
	}
	if rec.Expired != "2100-01-01T00:00:00Z" {
		t.Fatalf("Expired = %q, want 2100-01-01T00:00:00Z", rec.Expired)
	}
	if rec.Type != "codex" || rec.RefreshToken != "rt_x" {
		t.Fatalf("bad record: %+v", rec)
	}

	// Missing refresh_token must error loudly.
	if _, err := NormalizeOpenAIAuthRecord("x", map[string]any{"access_token": access, "id_token": id}); err == nil {
		t.Fatalf("expected error for missing refresh_token")
	}
}

func TestOpenAIBrowserHeaders(t *testing.T) {
	h := OpenAIBrowserHeaders(map[string]string{"x-extra": "1"})
	if h["user-agent"] != DefaultUserAgent {
		t.Fatalf("user-agent = %q", h["user-agent"])
	}
	if h["x-extra"] != "1" {
		t.Fatalf("extra header not merged")
	}
	if h["sec-ch-ua-platform"] != `"Windows"` {
		t.Fatalf("platform hint = %q", h["sec-ch-ua-platform"])
	}
}

// first_non_empty (app.py:2667) is str(value).strip() — it skips only None, so a
// zero or a False is a WINNING value, and a JSON integer must not come out in
// exponent form (the id tails downstream are taken with [-8:]).
func TestFirstNonEmptyPythonStr(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []any
		want string
	}{
		{"big integral float keeps its digits", []any{float64(1234567890)}, "1234567890"},
		{"json.Number is passed through verbatim", []any{json.Number("12345678901234567890")}, "12345678901234567890"},
		{"false is a value, not a skip", []any{false, "later"}, "False"},
		{"zero is a value, not a skip", []any{0, "later"}, "0"},
		{"nil is skipped", []any{nil, "later"}, "later"},
		{"empty and blank strings are skipped", []any{"", "   ", "later"}, "later"},
		{"non-integral float", []any{1.5}, "1.5"},
		{"nothing left", []any{nil, ""}, ""},
	} {
		if got := FirstNonEmpty(tc.in...); got != tc.want {
			t.Errorf("%s: FirstNonEmpty(%v) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
