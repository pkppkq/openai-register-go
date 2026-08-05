package worker

// workerparity_test.go pins the worker-slice functions whose Go spelling had
// to differ from the obvious one to reproduce CPython. Every `want` below was
// COMPUTED by exec'ing the VERBATIM app.py line slice named in each test's
// comment under CPython 3.12 over these exact inputs -- none of it is
// hand-derived, and each listed input is one the differential sweep exercised.
//
// The dialect gaps under test here:
//
//	RE2 \d is [0-9]              Python \d is \p{Nd} (680 code points)
//	RE2 \s is [\t\n\f\r ]        Python \s adds VT, U+001C-001F, U+0085, \p{Z}
//	Go s[:n] counts BYTES         Python s[:n] counts CODE POINTS
//	strings.TrimSpace             str.strip() also removes U+001C-001F
//	Go nil checks                 Python `x or D` also fires on False/0/""/[]/{}

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// TestPhoneLooksLikeCodePageDigitClassPythonParity is _looks_like_register_phone_code_page (app.py:10440-10449).
//
// The `\+\d` marker at app.py:10446 is why the fullwidth / Arabic-Indic /
// Devanagari / mathematical dial codes below must match: Python's `\d` is
// \p{Nd}. Missing them makes _has_otp_input mistake the SMS-code screen for the
// email-OTP screen and type the email code into the phone form.
func TestPhoneLooksLikeCodePageDigitClassPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"   ", false},
		{"\u00a0", false},
		{"\u3000", false},
		{"\u000b", false},
		{"\u001c", false},
		{"\u001f", false},
		{"\u0085", false},
		{"\u2028", false},
		{"\u2029", false},
		{"SMS code", true},
		{"sms\u00a0code", true},
		{"phone number code", true},
		{"email code", false},
		{"hello", false},
		{"\u77ed\u4fe1 \u9a8c\u8bc1\u7801", true},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", true},
		{"+1 code", true},
		{"+\uff11 code", true},
		{"+\u0661 code", true},
		{"+\u06f1 code", true},
		{"+\u0966 code", true},
		{"+\U0001d7ce code", true},
		{"\u96fb\u8a71 \u30b3\u30fc\u30c9", true},
		{"verification +\uff19", true},
		{"phone\u00a0number verification", true},
		{"SMS\u000bcode", true},
		{"SMS\u001fcode", true},
		{"Bad gateway", false},
		{"bad GATEWAY", false},
		{"Error code 502", false},
		{"Host Error", false},
		{"Host  Error", false},
		{"Host\u00a0Error", false},
		{"Host\u000bError", false},
		{"Host\u001fError", false},
		{"Host\u3000Error", false},
		{"Host\u000aError", false},
		{"Host\u0009Error", false},
		{"HTTP502", false},
		{"HTTP 502", false},
		{"HTTP\u00a0502", false},
		{"HTTP\u000b502", false},
		{"HTTP\u3000502", false},
		{"host error", false},
		{"HostError", false},
		{"Choose a workspace", false},
		{"Choose\u00a0a\u00a0workspace", false},
		{"Choose  workspace", false},
		{"Choose\u3000workspace", false},
		{"select a workspace", false},
		{"select\u00a0workspace", false},
		{"espacio de trabajo", false},
		{"espacio\u00a0de\u00a0trabajo", false},
		{"espace de travail", false},
		{"espace\u00a0de\u00a0travail", false},
		{"Escolha um espa\u00e7o de trabalho", false},
		{"Escolha\u00a0um\u00a0espa\u00e7o\u00a0de\u00a0trabalho", false},
		{"espa\u00e7o de trabalho", false},
		{"espa\u00e7o\u3000de\u3000trabalho", false},
		{"\u5de5\u4f5c\u7a7a\u95f4", false},
		{"workspace", false},
		{"sign in", false},
		{"nothing here", false},
		{"\u501f\u52a9 Codex", false},
		{"\u501f\u52a9Codex", false},
		{"\u501f\u52a9\u00a0Codex", false},
		{"\u501f\u52a9\u3000Codex", false},
		{"\u501f\u52a9\u000bCodex", false},
		{"\u501f\u52a9  Codex", false},
		{"Maybe later", false},
		{"Skip", false},
		{"\u8df3\u8fc7", false},
		{"work apps", false},
		{"United States", false},
		{"country", false},
		{"\u65e5\u672c", false},
		{"\u7f8e\u56fd", false},
		{"COUNTRY", false},
		{"u\u00a0nited", false},
		{"\u570b\u5bb6", false},
	}
	for _, tt := range tests {
		if got := phoneLooksLikeCodePageText(tt.in); got != tt.want {
			t.Errorf("%q: got %v, python says %v", tt.in, got, tt.want)
		}
	}
}

// TestTeamSSOBadGatewayPythonParity is _refresh_bad_gateway_if_visible's regex (app.py:9430).
//
// `Host\s+Error` / `HTTP\s*502` run over page.title()+innerText, where a
// Cloudflare 502 interstitial separates the words with NBSP. An ASCII `\s`
// makes RegisterTeamSSO never reload and burn its full 600s budget.
func TestTeamSSOBadGatewayPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"   ", false},
		{"\u00a0", false},
		{"\u3000", false},
		{"\u000b", false},
		{"\u001c", false},
		{"\u001f", false},
		{"\u0085", false},
		{"\u2028", false},
		{"\u2029", false},
		{"SMS code", false},
		{"sms\u00a0code", false},
		{"phone number code", false},
		{"email code", false},
		{"hello", false},
		{"\u77ed\u4fe1 \u9a8c\u8bc1\u7801", false},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", false},
		{"+1 code", false},
		{"+\uff11 code", false},
		{"+\u0661 code", false},
		{"+\u06f1 code", false},
		{"+\u0966 code", false},
		{"+\U0001d7ce code", false},
		{"\u96fb\u8a71 \u30b3\u30fc\u30c9", false},
		{"verification +\uff19", false},
		{"phone\u00a0number verification", false},
		{"SMS\u000bcode", false},
		{"SMS\u001fcode", false},
		{"Bad gateway", true},
		{"bad GATEWAY", true},
		{"Error code 502", true},
		{"Host Error", true},
		{"Host  Error", true},
		{"Host\u00a0Error", true},
		{"Host\u000bError", true},
		{"Host\u001fError", true},
		{"Host\u3000Error", true},
		{"Host\u000aError", true},
		{"Host\u0009Error", true},
		{"HTTP502", true},
		{"HTTP 502", true},
		{"HTTP\u00a0502", true},
		{"HTTP\u000b502", true},
		{"HTTP\u3000502", true},
		{"host error", true},
		{"HostError", false},
		{"Choose a workspace", false},
		{"Choose\u00a0a\u00a0workspace", false},
		{"Choose  workspace", false},
		{"Choose\u3000workspace", false},
		{"select a workspace", false},
		{"select\u00a0workspace", false},
		{"espacio de trabajo", false},
		{"espacio\u00a0de\u00a0trabajo", false},
		{"espace de travail", false},
		{"espace\u00a0de\u00a0travail", false},
		{"Escolha um espa\u00e7o de trabalho", false},
		{"Escolha\u00a0um\u00a0espa\u00e7o\u00a0de\u00a0trabalho", false},
		{"espa\u00e7o de trabalho", false},
		{"espa\u00e7o\u3000de\u3000trabalho", false},
		{"\u5de5\u4f5c\u7a7a\u95f4", false},
		{"workspace", false},
		{"sign in", false},
		{"nothing here", false},
		{"\u501f\u52a9 Codex", false},
		{"\u501f\u52a9Codex", false},
		{"\u501f\u52a9\u00a0Codex", false},
		{"\u501f\u52a9\u3000Codex", false},
		{"\u501f\u52a9\u000bCodex", false},
		{"\u501f\u52a9  Codex", false},
		{"Maybe later", false},
		{"Skip", false},
		{"\u8df3\u8fc7", false},
		{"work apps", false},
		{"United States", false},
		{"country", false},
		{"\u65e5\u672c", false},
		{"\u7f8e\u56fd", false},
		{"COUNTRY", false},
		{"u\u00a0nited", false},
		{"\u570b\u5bb6", false},
	}
	for _, tt := range tests {
		if got := teamSSOBadGatewayRe.MatchString(tt.in); got != tt.want {
			t.Errorf("%q: got %v, python says %v", tt.in, got, tt.want)
		}
	}
}

// TestTeamSSOWorkspaceGatePythonParity is the page-text gate of _select_team_workspace_if_visible (app.py:9454-9460).
//
// The pt/es/fr arms are the ONLY alternatives that can match those locales,
// so an ASCII `\s` locks a Spanish/French/Portuguese Team account out of the
// workspace picker entirely.
func TestTeamSSOWorkspaceGatePythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"   ", false},
		{"\u00a0", false},
		{"\u3000", false},
		{"\u000b", false},
		{"\u001c", false},
		{"\u001f", false},
		{"\u0085", false},
		{"\u2028", false},
		{"\u2029", false},
		{"SMS code", false},
		{"sms\u00a0code", false},
		{"phone number code", false},
		{"email code", false},
		{"hello", false},
		{"\u77ed\u4fe1 \u9a8c\u8bc1\u7801", false},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", false},
		{"+1 code", false},
		{"+\uff11 code", false},
		{"+\u0661 code", false},
		{"+\u06f1 code", false},
		{"+\u0966 code", false},
		{"+\U0001d7ce code", false},
		{"\u96fb\u8a71 \u30b3\u30fc\u30c9", false},
		{"verification +\uff19", false},
		{"phone\u00a0number verification", false},
		{"SMS\u000bcode", false},
		{"SMS\u001fcode", false},
		{"Bad gateway", false},
		{"bad GATEWAY", false},
		{"Error code 502", false},
		{"Host Error", false},
		{"Host  Error", false},
		{"Host\u00a0Error", false},
		{"Host\u000bError", false},
		{"Host\u001fError", false},
		{"Host\u3000Error", false},
		{"Host\u000aError", false},
		{"Host\u0009Error", false},
		{"HTTP502", false},
		{"HTTP 502", false},
		{"HTTP\u00a0502", false},
		{"HTTP\u000b502", false},
		{"HTTP\u3000502", false},
		{"host error", false},
		{"HostError", false},
		{"Choose a workspace", true},
		{"Choose\u00a0a\u00a0workspace", true},
		{"Choose  workspace", true},
		{"Choose\u3000workspace", true},
		{"select a workspace", true},
		{"select\u00a0workspace", true},
		{"espacio de trabajo", true},
		{"espacio\u00a0de\u00a0trabajo", true},
		{"espace de travail", true},
		{"espace\u00a0de\u00a0travail", true},
		{"Escolha um espa\u00e7o de trabalho", true},
		{"Escolha\u00a0um\u00a0espa\u00e7o\u00a0de\u00a0trabalho", true},
		{"espa\u00e7o de trabalho", true},
		{"espa\u00e7o\u3000de\u3000trabalho", true},
		{"\u5de5\u4f5c\u7a7a\u95f4", true},
		{"workspace", true},
		{"sign in", true},
		{"nothing here", false},
		{"\u501f\u52a9 Codex", false},
		{"\u501f\u52a9Codex", false},
		{"\u501f\u52a9\u00a0Codex", false},
		{"\u501f\u52a9\u3000Codex", false},
		{"\u501f\u52a9\u000bCodex", false},
		{"\u501f\u52a9  Codex", false},
		{"Maybe later", false},
		{"Skip", false},
		{"\u8df3\u8fc7", false},
		{"work apps", false},
		{"United States", false},
		{"country", false},
		{"\u65e5\u672c", false},
		{"\u7f8e\u56fd", false},
		{"COUNTRY", false},
		{"u\u00a0nited", false},
		{"\u570b\u5bb6", false},
	}
	for _, tt := range tests {
		if got := teamSSOWorkspaceGateRe.MatchString(tt.in); got != tt.want {
			t.Errorf("%q: got %v, python says %v", tt.in, got, tt.want)
		}
	}
}

// TestTeamSSOOnboardingPendingPythonParity is _team_onboarding_pending (app.py:9558-9567).
//
// `借助\s*Codex` again: with an ASCII `\s` the loop calls Team SSO complete
// while the onboarding modal is still up.
func TestTeamSSOOnboardingPendingPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"   ", false},
		{"\u00a0", false},
		{"\u3000", false},
		{"\u000b", false},
		{"\u001c", false},
		{"\u001f", false},
		{"\u0085", false},
		{"\u2028", false},
		{"\u2029", false},
		{"SMS code", false},
		{"sms\u00a0code", false},
		{"phone number code", false},
		{"email code", false},
		{"hello", false},
		{"\u77ed\u4fe1 \u9a8c\u8bc1\u7801", false},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", false},
		{"+1 code", false},
		{"+\uff11 code", false},
		{"+\u0661 code", false},
		{"+\u06f1 code", false},
		{"+\u0966 code", false},
		{"+\U0001d7ce code", false},
		{"\u96fb\u8a71 \u30b3\u30fc\u30c9", false},
		{"verification +\uff19", false},
		{"phone\u00a0number verification", false},
		{"SMS\u000bcode", false},
		{"SMS\u001fcode", false},
		{"Bad gateway", false},
		{"bad GATEWAY", false},
		{"Error code 502", false},
		{"Host Error", false},
		{"Host  Error", false},
		{"Host\u00a0Error", false},
		{"Host\u000bError", false},
		{"Host\u001fError", false},
		{"Host\u3000Error", false},
		{"Host\u000aError", false},
		{"Host\u0009Error", false},
		{"HTTP502", false},
		{"HTTP 502", false},
		{"HTTP\u00a0502", false},
		{"HTTP\u000b502", false},
		{"HTTP\u3000502", false},
		{"host error", false},
		{"HostError", false},
		{"Choose a workspace", false},
		{"Choose\u00a0a\u00a0workspace", false},
		{"Choose  workspace", false},
		{"Choose\u3000workspace", false},
		{"select a workspace", false},
		{"select\u00a0workspace", false},
		{"espacio de trabajo", false},
		{"espacio\u00a0de\u00a0trabajo", false},
		{"espace de travail", false},
		{"espace\u00a0de\u00a0travail", false},
		{"Escolha um espa\u00e7o de trabalho", false},
		{"Escolha\u00a0um\u00a0espa\u00e7o\u00a0de\u00a0trabalho", false},
		{"espa\u00e7o de trabalho", false},
		{"espa\u00e7o\u3000de\u3000trabalho", false},
		{"\u5de5\u4f5c\u7a7a\u95f4", false},
		{"workspace", false},
		{"sign in", false},
		{"nothing here", false},
		{"\u501f\u52a9 Codex", true},
		{"\u501f\u52a9Codex", true},
		{"\u501f\u52a9\u00a0Codex", true},
		{"\u501f\u52a9\u3000Codex", true},
		{"\u501f\u52a9\u000bCodex", true},
		{"\u501f\u52a9  Codex", true},
		{"Maybe later", true},
		{"Skip", true},
		{"\u8df3\u8fc7", true},
		{"work apps", true},
		{"United States", false},
		{"country", false},
		{"\u65e5\u672c", false},
		{"\u7f8e\u56fd", false},
		{"COUNTRY", false},
		{"u\u00a0nited", false},
		{"\u570b\u5bb6", false},
	}
	for _, tt := range tests {
		if got := teamSSOOnboardingPendingRe.MatchString(tt.in); got != tt.want {
			t.Errorf("%q: got %v, python says %v", tt.in, got, tt.want)
		}
	}
}

// TestPhoneFormBodyTextPythonParity is _has_register_phone_number_form's body fallback (app.py:10308).
func TestPhoneFormBodyTextPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{" ", false},
		{"   ", false},
		{"\u00a0", false},
		{"\u3000", false},
		{"\u000b", false},
		{"\u001c", false},
		{"\u001f", false},
		{"\u0085", false},
		{"\u2028", false},
		{"\u2029", false},
		{"SMS code", false},
		{"sms\u00a0code", false},
		{"phone number code", false},
		{"email code", false},
		{"hello", false},
		{"\u77ed\u4fe1 \u9a8c\u8bc1\u7801", false},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", false},
		{"+1 code", false},
		{"+\uff11 code", false},
		{"+\u0661 code", false},
		{"+\u06f1 code", false},
		{"+\u0966 code", false},
		{"+\U0001d7ce code", false},
		{"\u96fb\u8a71 \u30b3\u30fc\u30c9", false},
		{"verification +\uff19", false},
		{"phone\u00a0number verification", false},
		{"SMS\u000bcode", false},
		{"SMS\u001fcode", false},
		{"Bad gateway", false},
		{"bad GATEWAY", false},
		{"Error code 502", false},
		{"Host Error", false},
		{"Host  Error", false},
		{"Host\u00a0Error", false},
		{"Host\u000bError", false},
		{"Host\u001fError", false},
		{"Host\u3000Error", false},
		{"Host\u000aError", false},
		{"Host\u0009Error", false},
		{"HTTP502", false},
		{"HTTP 502", false},
		{"HTTP\u00a0502", false},
		{"HTTP\u000b502", false},
		{"HTTP\u3000502", false},
		{"host error", false},
		{"HostError", false},
		{"Choose a workspace", false},
		{"Choose\u00a0a\u00a0workspace", false},
		{"Choose  workspace", false},
		{"Choose\u3000workspace", false},
		{"select a workspace", false},
		{"select\u00a0workspace", false},
		{"espacio de trabajo", false},
		{"espacio\u00a0de\u00a0trabajo", false},
		{"espace de travail", false},
		{"espace\u00a0de\u00a0travail", false},
		{"Escolha um espa\u00e7o de trabalho", false},
		{"Escolha\u00a0um\u00a0espa\u00e7o\u00a0de\u00a0trabalho", false},
		{"espa\u00e7o de trabalho", false},
		{"espa\u00e7o\u3000de\u3000trabalho", false},
		{"\u5de5\u4f5c\u7a7a\u95f4", false},
		{"workspace", false},
		{"sign in", false},
		{"nothing here", false},
		{"\u501f\u52a9 Codex", false},
		{"\u501f\u52a9Codex", false},
		{"\u501f\u52a9\u00a0Codex", false},
		{"\u501f\u52a9\u3000Codex", false},
		{"\u501f\u52a9\u000bCodex", false},
		{"\u501f\u52a9  Codex", false},
		{"Maybe later", false},
		{"Skip", false},
		{"\u8df3\u8fc7", false},
		{"work apps", false},
		{"United States", true},
		{"country", true},
		{"\u65e5\u672c", true},
		{"\u7f8e\u56fd", true},
		{"COUNTRY", true},
		{"u\u00a0nited", false},
		{"\u570b\u5bb6", true},
	}
	for _, tt := range tests {
		if got := rePhoneFormBodyText.MatchString(tt.in); got != tt.want {
			t.Errorf("%q: got %v, python says %v", tt.in, got, tt.want)
		}
	}
}

// TestPhoneLocalNumberGuardPythonParity is app.py:10231-10240 -- the
// `str(phone['number'] or ”).strip()` -> normalize_us_phone_for_form ->
// `len(local_number) < 10` -> `local_number[-10:]` chain that decides whether a
// RENTED number is POSTed to OpenAI.
//
// normalize_us_phone_for_form keeps every \p{Nd} digit (app.py:1941), so the
// local number is not necessarily ASCII. Byte counting both let a too-short
// number through the guard AND cut the -10 slice mid-rune, and once the number
// is submitted the failure path marks it Bad and rents the next one.
func TestPhoneLocalNumberGuardPythonParity(t *testing.T) {
	tests := []struct {
		in    string
		local string
		bad   bool
		fed   string // "" when the guard aborts before filling
	}{
		{"+12025550123", "2025550123", false, "2025550123"},
		{"+1 202 555 0123", "2025550123", false, "2025550123"},
		{"12025550123", "2025550123", true, ""},
		{"2025550123", "2025550123", true, ""},
		{"+1202555012", "1202555012", false, "1202555012"},
		{"", "", true, ""},
		{"+1", "1", true, ""},
		{"+1\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", "\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", false, "\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13"},
		{"+1\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12", "1\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12", false, "1\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12"},
		{"+1\u0662\u0660\u0662\u0665\u0665\u0665\u0660\u0661\u0662\u0663", "\u0662\u0660\u0662\u0665\u0665\u0665\u0660\u0661\u0662\u0663", false, "\u0662\u0660\u0662\u0665\u0665\u0665\u0660\u0661\u0662\u0663"},
		{"+1\u0966\u0967\u0968\u0969\u0960\u0961\u0962\u0963\u0964\u0965", "1\u0966\u0967\u0968\u0969", true, ""},
		{"+\uff11\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", "\uff11\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", true, ""},
		{"+1(202) 555-0123", "2025550123", false, "2025550123"},
		{"  +12025550123  ", "2025550123", false, "2025550123"},
		{"+81 90 1234 5678", "819012345678", true, ""},
		{"\uff0b\uff11\uff12\uff10", "\uff11\uff12\uff10", true, ""},
		{"+1\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1\U0001d7d2\U0001d7d3\U0001d7d4\U0001d7d5\U0001d7d6\U0001d7d7", "\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1\U0001d7d2\U0001d7d3\U0001d7d4\U0001d7d5\U0001d7d6\U0001d7d7", false, "\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1\U0001d7d2\U0001d7d3\U0001d7d4\U0001d7d5\U0001d7d6\U0001d7d7"},
	}
	for _, tt := range tests {
		phoneNumber := pyStrip(tt.in)
		local := models.NormalizeUSPhoneForForm(phoneNumber)
		bad := !strings.HasPrefix(phoneNumber, "+1") || phoneLocalTooShort(local)
		fed := ""
		if !bad {
			fed = phoneLastTenRunes(local)
		}
		if local != tt.local || bad != tt.bad || fed != tt.fed {
			t.Errorf("%q: got local=%q bad=%v fed=%q, python says local=%q bad=%v fed=%q",
				tt.in, local, bad, fed, tt.local, tt.bad, tt.fed)
		}
	}
}

// TestAuthAsStringPythonParity is `str(payload.get("csrfToken") or "").strip()`
// (app.py:10045). The `or` collapses EVERY falsy JSON value to "", which is what
// makes _get_chatgpt_csrf_and_device fall through to the cookie re-scan; a
// non-empty rendering of False/0/[]/{} would be POSTed as the CSRF token instead.
func TestAuthAsStringPythonParity(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"None", nil, ""},
		{"False", false, ""},
		{"True", true, "True"},
		{"0", float64(0), ""},
		{"0.0", float64(0), ""},
		{"1", float64(1), "1"},
		{"5.0", float64(5), "5"}, // JSON gives float64; Python's json gives int 5 here
		{"''", "", ""},
		{"'  tok  '", "  tok  ", "tok"},
		{"'tok'", "tok", "tok"},
		{"'\\x1ftok\\x1f'", "\u001ftok\u001f", "tok"},
		{"[]", []any{}, ""},
		{"{}", map[string]any{}, ""},
		{"'0'", "0", "0"},
	}
	for _, tt := range tests {
		if got := pyStrip(authAsString(tt.in)); got != tt.want {
			t.Errorf("%s: got %q, python says %q", tt.name, got, tt.want)
		}
	}
}

// TestOTPResultDetailPythonParity is
// `str(result.get("text") or result.get("status") or "")` (app.py:10654) -- the
// string _validate_email_code_api classifies as a Cloudflare block, an
// account_deactivated, or a generic failure. Dropping a bare HTTP status here
// hides the response code from the operator-facing error.
func TestOTPResultDetailPythonParity(t *testing.T) {
	tests := []struct {
		name   string
		text   any
		status any
		want   string
	}{
		{"'body'/403", "body", float64(403), "body"},
		{"''/403", "", float64(403), "403"},
		{"None/403", nil, float64(403), "403"},
		{"''/0", "", float64(0), ""},
		{"''/None", "", nil, ""},
		{"''/''", "", "", ""},
		{"''/'403'", "", "403", "403"},
		{"''/False", "", false, ""},
		{"''/True", "", true, "True"},
		{"''/200.0", "", float64(200), "200"},
		{"None/None", nil, nil, ""},
	}
	for _, tt := range tests {
		got := otpResultDetail(map[string]any{"text": tt.text, "status": tt.status})
		if got != tt.want {
			t.Errorf("%s: got %q, python says %q", tt.name, got, tt.want)
		}
	}
}

// TestMailProviderLabelPythonParity is app.py:8977. There is no .strip() in
// Python, so " cloudmail " is IMAP.
func TestMailProviderLabelPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "\u90ae\u7bb1 IMAP"},
		{"", "\u90ae\u7bb1 IMAP"},
		{"cloudmail", "Cloud Mail API"},
		{"CloudMail", "Cloud Mail API"},
		{"CLOUDMAIL", "Cloud Mail API"},
		{" cloudmail ", "\u90ae\u7bb1 IMAP"},
		{"cloudmail ", "\u90ae\u7bb1 IMAP"},
		{"imap", "\u90ae\u7bb1 IMAP"},
		{"Cloudmai\u0142", "\u90ae\u7bb1 IMAP"},
		{"cloudmai\u0131", "\u90ae\u7bb1 IMAP"},
	}
	for _, tt := range tests {
		if got := mailProviderLabel(tt.in); got != tt.want {
			t.Errorf("%q: got %q, python says %q", tt.in, got, tt.want)
		}
	}
}

// TestClickLabelFormattingPythonParity pins two log-line formatters that use
// DIFFERENT truthiness rules on purpose:
//
//	app.py:11186  str(box.get('text','')).strip()[:40]   -- strips, then cuts
//	app.py:9540   str(result.get('text') or 'My Team')[:80] -- `or` on "" ONLY,
//	              so a whitespace-only label is kept verbatim, NOT replaced
func TestClickLabelFormattingPythonParity(t *testing.T) {
	tests := []struct {
		in        string
		clicked   string
		workspace string
	}{
		{"  Back  ", "Back", "  Back  "},
		{"\u001fBack\u001f", "Back", "\u001fBack\u001f"},
		{"", "", "My Team"},
		{"   ", "", "   "},
		{"\u00a0Back\u00a0", "Back", "\u00a0Back\u00a0"},
		{"", "", "My Team"},
	}
	for _, tt := range tests {
		if got := phoneClickedLabel(tt.in); got != tt.clicked {
			t.Errorf("phoneClickedLabel(%q): got %q, python says %q", tt.in, got, tt.clicked)
		}
		if got := teamSSOWorkspaceLabel(tt.in); got != tt.workspace {
			t.Errorf("teamSSOWorkspaceLabel(%q): got %q, python says %q", tt.in, got, tt.workspace)
		}
	}
}

// TestOTPContinueURLPythonParity is
// `payload.get("continue_url") or payload.get("page",{}).get("payload",{}).get("url") or ""`
// (app.py:10652).
func TestOTPContinueURLPythonParity(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"{\"continue_url\": \"u1\"}", map[string]any{"continue_url": "u1"}, "u1"},
		{"{\"continue_url\": \"\"}", map[string]any{"continue_url": ""}, ""},
		{"{}", map[string]any{}, ""},
		{"{\"page\": {\"payload\": {\"url\": \"u2\"}}}", map[string]any{"page": map[string]any{"payload": map[string]any{"url": "u2"}}}, "u2"},
		{"{\"continue_url\": \"\", \"page\": {\"payload\": {\"url\": \"u2\"}}}", map[string]any{"continue_url": "", "page": map[string]any{"payload": map[string]any{"url": "u2"}}}, "u2"},
		{"{\"page\": {}}", map[string]any{"page": map[string]any{}}, ""},
		{"{\"page\": {\"payload\": {}}}", map[string]any{"page": map[string]any{"payload": map[string]any{}}}, ""},
	}
	for _, tt := range tests {
		if got := otpContinueURL(tt.in); got != tt.want {
			t.Errorf("%s: got %q, python says %q", tt.name, got, tt.want)
		}
	}
}

// TestOTPDerivePasswordPythonParity is app.py:10563-10567: the >= 12 test counts
// CODE POINTS, and the pad is sha256("email:password") hex, first 12 chars.
func TestOTPDerivePasswordPythonParity(t *testing.T) {
	tests := []struct {
		email      string
		password   string
		derived    string
		longEnough bool
	}{
		{"a@b.com", "short", "shortA7!490330374c15", false},
		{"\u4e2d\u6587@b.com", "pw", "pwA7!2d871635853a", false},
		{"a@b.com", "", "A7!aff57bdb6fd0", false},
		{"a@b.com", "\u00e9\u00e9\u00e9", "\u00e9\u00e9\u00e9A7!315ca44ed875", false},
		{"\uff21@b.com", "\uff11\uff12", "\uff11\uff12A7!7ba0d7198648", false},
		{"", "", "A7!e7ac0786668e", false},
		{"a@b.com", "01234567890", "01234567890A7!8578342d247c", false},
	}
	for _, tt := range tests {
		if got := otpDerivePassword(tt.email, tt.password); got != tt.derived {
			t.Errorf("otpDerivePassword(%q,%q): got %q, python says %q", tt.email, tt.password, got, tt.derived)
		}
		if got := len([]rune(tt.password)) >= 12; got != tt.longEnough {
			t.Errorf("len(%q)>=12: got %v, python says %v", tt.password, got, tt.longEnough)
		}
	}
}
