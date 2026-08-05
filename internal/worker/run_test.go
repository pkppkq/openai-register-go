package worker

import (
	"errors"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// TestBuildFlowsWiring guards the one failure mode the split-flow design added.
// The Python worker is a god-object whose methods call each other directly; the
// Go port breaks it into flow types joined by function-valued hooks. A hook left
// nil compiles fine and only surfaces at runtime — mid-registration, possibly
// after a phone number has already been paid for.
func TestBuildFlowsWiring(t *testing.T) {
	w := New(Config{Account: &models.MailAccount{Email: "a@b.com"}})
	fl := w.buildFlows(nil, nil, models.ProxyConfig{})

	for name, dep := range map[string]any{
		"Auth": fl.Register.Auth, "CF": fl.Register.CF, "Team": fl.Register.Team,
		"OTP": fl.Register.OTP, "AboutYou": fl.Register.AboutYou, "Phone": fl.Register.Phone,
	} {
		if dep == nil {
			t.Errorf("RegisterFlow.%s not wired", name)
		}
	}

	// Team SSO refuses to start unless every hook is present; assert that the
	// assembly actually satisfies it rather than trusting the field list.
	if err := fl.Team.requireHooks(); err != nil {
		t.Errorf("Team hooks incomplete: %v", err)
	}

	for name, hook := range map[string]any{
		"OTPHandler.ClickContinue":                  fl.OTP.ClickContinue,
		"OTPHandler.HasAboutYouForm":                fl.OTP.HasAboutYouForm,
		"OTPHandler.LooksLikeRegisterPhoneCodePage": fl.OTP.LooksLikeRegisterPhoneCodePage,
		"OTPHandler.WaitAfterOTPSubmit":             fl.OTP.WaitAfterOTPSubmit,
		"OTPHandler.HandleCloudflareChallenge":      fl.OTP.HandleCloudflareChallenge,

		"AboutYouFiller.HasChatGPTSession":          fl.AboutYou.HasChatGPTSession,
		"AboutYouFiller.HasRegisterPhoneNumberForm": fl.AboutYou.HasRegisterPhoneNumberForm,
		"AboutYouFiller.HasVisiblePassword":         fl.AboutYou.HasVisiblePassword,
		"AboutYouFiller.ClickContinue":              fl.AboutYou.ClickContinue,

		// CFSolver.LowerWindows is deliberately nil (Win32 z-order, UI layer).
		"CFSolver.HasAboutYouForm": fl.CF.HasAboutYouForm,
		"CFSolver.HasOTPInput":     fl.CF.HasOTPInput,

		"PhoneHandler.HasAboutYouForm": fl.Phone.HasAboutYouForm,
	} {
		if hook == nil {
			t.Errorf("%s not wired", name)
		}
	}
}

// TestMarshalIndentNoEscape pins the json.dumps(ensure_ascii=False, indent=2)
// behaviour. encoding/json escapes <, > and & by default and json.dumps does
// not, so a session blob would otherwise differ byte-for-byte from the Python
// tool's — these files are read back by the existing UI.
func TestMarshalIndentNoEscape(t *testing.T) {
	got, err := marshalIndentNoEscape(map[string]any{"u": "a?b=1&c=2<x>"})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"u\": \"a?b=1&c=2<x>\"\n}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The escaped forms encoding/json emits by default; none may appear.
	for _, escaped := range []string{"\\u0026", "\\u003c", "\\u003e"} {
		if strings.Contains(got, escaped) {
			t.Fatalf("HTML escaping not disabled (%s present): %q", escaped, got)
		}
	}

	// ensure_ascii=False keeps non-ASCII literal.
	got, err = marshalIndentNoEscape(map[string]any{"s": "认证"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "认证") {
		t.Fatalf("non-ASCII was escaped: %q", got)
	}
}

// TestParkBrowserReplacesPrevious pins the pop-then-close half of
// KEPT_REGISTER_BROWSER_SESSIONS (app.py:8894): parking a second browser for the
// same account must evict the first, and lookup is case-insensitive.
func TestParkBrowserReplacesPrevious(t *testing.T) {
	const email = "Case@Example.com"
	defer CloseParkedBrowser(email)

	// A nil *browser.Browser is safe here: CloseBrowser short-circuits on nil,
	// which is what the eviction path calls.
	ParkBrowser(email, nil, "proxy-a")
	ParkBrowser(strings.ToUpper(email), nil, "proxy-b")

	got := TakeParkedBrowser(strings.ToLower(email))
	if got == nil {
		t.Fatal("no parked session found (key not normalised?)")
	}
	if got.DynamicProxy != "proxy-b" {
		t.Fatalf("second park did not replace the first: %q", got.DynamicProxy)
	}
	if again := TakeParkedBrowser(email); again != nil {
		t.Fatal("TakeParkedBrowser did not remove the entry")
	}
}

func TestCloseAllParkedBrowsersDrainsRegistry(t *testing.T) {
	ParkBrowser("one@example.com", nil, "proxy-a")
	ParkBrowser("two@example.com", nil, "proxy-b")

	CloseAllParkedBrowsers()

	if got := TakeParkedBrowser("one@example.com"); got != nil {
		t.Fatal("关闭后仍能取到第一个保留浏览器")
	}
	if got := TakeParkedBrowser("two@example.com"); got != nil {
		t.Fatal("关闭后仍能取到第二个保留浏览器")
	}
}

func TestParkedBrowserOwnsAttachedProxyCleanup(t *testing.T) {
	CloseAllParkedBrowsers()
	cleaned := 0
	ParkBrowser("cleanup@example.com", nil, "proxy-a")
	before := ParkedBrowserGeneration("cleanup@example.com")
	if AttachParkedCleanupSince("cleanup@example.com", before, func() { cleaned += 100 }) {
		t.Fatal("旧保留窗口不应接管本次失败任务的资源")
	}
	if !AttachParkedCleanup("cleanup@example.com", func() { cleaned++ }) {
		t.Fatal("已保留浏览器应接受代理链清理函数")
	}
	if AttachParkedCleanup("missing@example.com", func() { cleaned += 100 }) {
		t.Fatal("不存在的保留浏览器不应接管资源")
	}
	CloseParkedBrowser("cleanup@example.com")
	if cleaned != 1 {
		t.Fatalf("关闭保留浏览器时 cleanup=%d，期望 1", cleaned)
	}
}

func TestIsAuthProxyTransportError(t *testing.T) {
	if !IsAuthProxyTransportError(errors.New("ERR_CONNECTION_RESET")) {
		t.Fatal("连接重置应允许代理故障转移")
	}
	if IsAuthProxyTransportError(errors.New("wrong_email_otp_code")) {
		t.Fatal("业务错误不得触发代理故障转移")
	}
}
