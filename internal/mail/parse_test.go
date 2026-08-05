package mail

import (
	"reflect"
	"testing"
)

// TestExtractOpenAICode pins the OTP extraction against the Python
// extract_openai_code behavior (app.py:6315): a context-keyword pattern first,
// then a bare 6-digit fallback. If these regexes drift, OTP silently breaks.
func TestExtractOpenAICode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"context-english", "Your ChatGPT verification code is 048213. It expires in 10 minutes.", "048213"},
		{"context-code-word", "Please use code 550132 to continue signing in.", "550132"},
		{"context-chinese", "您的验证码是 739210，请勿泄露给他人。", "739210"},
		{"bare-fallback", "One time PIN 661204 do not share", "661204"},
		{"no-code-welcome", "Welcome to OpenAI. Get started today.", ""},
		{"too-few-digits", "Your PIN is 12345 only.", ""},
		{"empty", "", ""},
		{"whitespace-collapsed", "code\n\n  \t 900817", "900817"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractOpenAICode(c.in); got != c.want {
				t.Fatalf("extractOpenAICode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestExtractLinksFromText pins link harvesting (href + bare URL, dedup,
// trailing-punctuation trim, non-http rejection) — used for magic-link /
// team-invite flows.
func TestExtractLinksFromText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			"href-and-bare-dedup",
			`<a href="https://auth.openai.com/verify?token=abc">Verify</a> or visit https://auth.openai.com/verify?token=abc`,
			[]string{"https://auth.openai.com/verify?token=abc"},
		},
		{
			"trailing-period-trimmed",
			"Open https://chatgpt.com/invite/xyz.",
			[]string{"https://chatgpt.com/invite/xyz"},
		},
		{
			"mailto-rejected",
			`<a href="mailto:noreply@openai.com">mail us</a>`,
			nil,
		},
		{
			"html-entity-decoded",
			`<a href="https://openai.com/p?a=1&amp;b=2">x</a>`,
			[]string{"https://openai.com/p?a=1&b=2"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractLinksFromText(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("extractLinksFromText(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

// TestIsOpenAIDeactivationNotice pins the ban-detection classifier: it must
// require an OpenAI/ChatGPT mention AND a deactivation marker (subject or body).
func TestIsOpenAIDeactivationNotice(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		from    string
		body    string
		want    bool
	}{
		{
			"body-marker",
			"Update about your account",
			"noreply@openai.com",
			"We're writing to let you know your account has been deactivated for violating our terms.",
			true,
		},
		{
			"subject-suspended",
			"Your ChatGPT access has been suspended",
			"noreply@openai.com",
			"See details in your dashboard.",
			true,
		},
		{
			"chinese-marker",
			"账户通知",
			"noreply@openai.com",
			"您的OpenAI账号已停用。",
			true,
		},
		{
			"no-openai-mention",
			"Your account has been deactivated",
			"noreply@example.com",
			"Your account has been deactivated.",
			false,
		},
		{
			"benign-newsletter",
			"News from OpenAI",
			"news@openai.com",
			"Check out our latest models and features.",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOpenAIDeactivationNotice(c.subject, c.from, c.body); got != c.want {
				t.Fatalf("isOpenAIDeactivationNotice(%q,%q,%q) = %v, want %v", c.subject, c.from, c.body, got, c.want)
			}
		})
	}
}
