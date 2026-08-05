package ui

// 本文件所有认证执行器均替换为 fake；不会打开 Chromium、访问 OpenAI、
// 连接邮箱、申请代理或租用短信号码。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/authproto"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

func fakeAuthRecord(email, plan string) openai.AuthRecord {
	return openai.AuthRecord{
		AccessToken:  networkTestJWT(plan, "account-fixture"),
		AccountID:    "account-fixture",
		Email:        email,
		RefreshToken: "rt-fixture",
		IDToken:      "id-fixture",
		Expired:      "2033-05-18T03:33:20Z",
		Type:         "codex",
	}
}

func TestProtocolRegisterUsesHTTPFakeWithoutPhoneAndPersists(t *testing.T) {
	const email = "protocol@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{},
	)

	old := runProtocolRecord
	t.Cleanup(func() { runProtocolRecord = old })
	runProtocolRecord = func(
		ctx context.Context,
		account models.MailAccount,
		_ settings.Settings,
		proxyURL string,
		input authproto.InputCallback,
		phone authproto.PhoneProvider,
		allowManualPhone bool,
		_ func(string),
	) (openai.AuthRecord, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if account.Email != email || proxyURL != "" {
			t.Fatalf("参数异常: account=%#v proxy=%q", account, proxyURL)
		}
		if input == nil {
			t.Fatal("取消/手动验证码回调必须可用")
		}
		if phone != nil || allowManualPhone {
			t.Fatalf("协议注册不得接入手机号: phone=%v allowManual=%v", phone != nil, allowManualPhone)
		}
		return fakeAuthRecord(email, "plus"), nil
	}

	summary, err := app.StartProtocolRegisterSession(AuthBatchRequest{Emails: []string{email}})
	if err != nil {
		t.Fatalf("StartProtocolRegisterSession: %v", err)
	}
	waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)

	account, session := loadedNetworkAccount(t, app, email)
	if account.Status != "协议Session已获取" || account.AccountType != "plus" ||
		account.OpenaiRT != "rt-fixture" {
		t.Fatalf("账号未正确持久化: %#v", account)
	}
	if session["access_token"] == "" || session["openai_rt"] != "rt-fixture" ||
		session["plan_type"] != "plus" || session["account_id"] != "account-fixture" {
		t.Fatalf("协议 Session 未正确持久化: %#v", session)
	}
	if text, _ := session["session_json"].(string); !strings.Contains(text, `"source": "protocol_oauth"`) {
		t.Fatalf("session_json 格式异常: %q", text)
	}
}

func TestOAuthAuthorizeRequiresConfirmationAndPersistsFake(t *testing.T) {
	const email = "oauth@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{},
	)
	if _, err := app.StartOAuthAuthorizeRT(AuthBatchRequest{Emails: []string{email}}); err == nil ||
		!strings.Contains(err.Error(), "明确确认") {
		t.Fatalf("未确认时 err=%v", err)
	}
	if len(app.ListJobs()) != 0 {
		t.Fatal("确认失败不应登记任务")
	}

	old := runOAuthRecord
	t.Cleanup(func() { runOAuthRecord = old })
	runOAuthRecord = func(
		context.Context,
		models.MailAccount,
		settings.Settings,
		string,
		authproto.InputCallback,
		authproto.PhoneProvider,
		bool,
		func(string),
	) (openai.AuthRecord, error) {
		return fakeAuthRecord(email, "team"), nil
	}

	summary, err := app.StartOAuthAuthorizeRT(AuthBatchRequest{
		Emails: []string{email}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("StartOAuthAuthorizeRT: %v", err)
	}
	waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)
	account, _ := loadedNetworkAccount(t, app, email)
	if account.OpenaiRT != "rt-fixture" || account.AccountType != "team" ||
		account.Status != "Team RT已获取" {
		t.Fatalf("OAuth 结果未正确持久化: %#v", account)
	}
}

func TestBrowserAuthBindingsUseFakeAndNeverLaunchBrowser(t *testing.T) {
	const email = "manual@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{},
	)

	oldKeep := runKeepLoginAction
	oldReader := runSessionReaderAction
	oldCode := runManualLoginCodeAction
	t.Cleanup(func() {
		runKeepLoginAction = oldKeep
		runSessionReaderAction = oldReader
		runManualLoginCodeAction = oldCode
	})
	runKeepLoginAction = func(context.Context, *worker.Worker, string) (worker.BrowserActionResult, error) {
		return worker.BrowserActionResult{Status: "已登录"}, nil
	}
	runSessionReaderAction = func(context.Context, *worker.Worker, string) (worker.BrowserActionResult, error) {
		return worker.BrowserActionResult{Status: "已填邮箱", NeedsManual: true}, nil
	}
	runManualLoginCodeAction = func(context.Context, *worker.Worker, string) (worker.BrowserActionResult, error) {
		return worker.BrowserActionResult{Status: "验证码已弹出", Code: "123456"}, nil
	}

	keep, err := app.StartKeepLogin(email)
	if err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, keep.ID, StatusSucceeded)
	account, _ := loadedNetworkAccount(t, app, email)
	if account.Status != "已登录" {
		t.Fatalf("登录状态未保存: %#v", account)
	}

	reader, err := app.StartOpenSessionReader(email)
	if err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, reader.ID, StatusSucceeded)

	codeJob, err := app.StartManualLoginCode(email)
	if err != nil {
		t.Fatal(err)
	}
	result := waitNetworkJob(t, app, codeJob.ID, StatusSucceeded)
	code, ok := result.Result.(worker.BrowserActionResult)
	if !ok || code.Code != "123456" {
		t.Fatalf("手动取码结果异常: %#v", result.Result)
	}
}

func TestExternalOAuthValidationPreventsCredentialLeak(t *testing.T) {
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("oauth@example.com", "free", "")},
		map[string]any{},
	)
	if _, err := app.StartExternalOAuth(ExternalOAuthRequest{
		Email: "oauth@example.com",
		URL:   "https://evil.example/oauth/authorize",
	}); err == nil || !strings.Contains(err.Error(), "auth.openai.com") {
		t.Fatalf("第三方域名 err=%v", err)
	}
	if _, err := app.StartExternalOAuth(ExternalOAuthRequest{
		Email: "oauth@example.com",
		URL:   "https://auth.openai.com/unexpected",
	}); err == nil || !strings.Contains(err.Error(), "额外确认") {
		t.Fatalf("非标准路径未确认 err=%v", err)
	}
	if len(app.ListJobs()) != 0 {
		t.Fatal("URL 校验失败不应登记任务")
	}
}

func TestProtocolRetryClassifierPreservesMoneySafeTerminalErrors(t *testing.T) {
	for _, text := range []string{
		"OauthUrl请求失败: 503",
		"proxy connection reset",
		"Cloudflare challenge 被拦截",
	} {
		if !protocolErrorRetryable(errors.New(text)) {
			t.Fatalf("%q 应可重试", text)
		}
	}
	for _, text := range []string{
		"等待 OpenAI 邮箱验证码超时",
		"wrong_email_otp_code 403",
		"进入 add-phone 页面 connection reset",
	} {
		if protocolErrorRetryable(errors.New(text)) {
			t.Fatalf("%q 不得重试", text)
		}
	}
}
