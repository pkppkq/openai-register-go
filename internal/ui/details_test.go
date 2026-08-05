package ui

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestGetAccountDetailsReturnsCompleteIndependentSnapshot(t *testing.T) {
	account := models.MailAccount{
		Email:           "Case@Example.com",
		Password:        "password",
		ClientID:        "client-id",
		RefreshToken:    "refresh-token",
		Raw:             "Case@Example.com----password----client-id----refresh-token",
		AccountType:     "plus",
		Status:          "Session已获取",
		OpenaiRT:        "openai-rt",
		AuthPhoneNumber: "+15550001",
		AuthPhoneSMSURL: "https://sms.example/1",
		ReceiveMailbox:  "receive@example.com",
		MailProvider:    "outlook",
		Group:           "生产组",
		BrowserFingerprint: &models.DeviceFingerprint{
			UserAgent:         "Mozilla/5.0 Chrome/146.0.7000.100 Safari/537.36",
			Locale:            "en-US",
			Languages:         []string{"en-US", "en"},
			Timezone:          "Asia/Tokyo",
			ViewportWidth:     1280,
			ViewportHeight:    720,
			ScreenWidth:       1280,
			ScreenHeight:      720,
			OuterWidth:        1290,
			OuterHeight:       800,
			DeviceScaleFactor: 1,
			Platform:          "Win32",
		},
	}
	session := map[string]any{
		"access_token": "access-token",
		"access_summary": map[string]any{
			"plan_type":  "plus",
			"expires_at": "2027-01-02T03:04:05Z",
		},
		"plus_trial_status":         "checking",
		"plus_trial_eligible":       false,
		"nested":                    map[string]any{"keep": "original"},
		"link_proxy":                "generic-proxy",
		"link_proxy_label":          "generic-label",
		"link_proxy_exit":           "JP/generic",
		"link_create_proxy":         "create-proxy",
		"link_create_proxy_label":   "create-label",
		"link_create_proxy_exit":    "JP/create",
		"link_followup_proxy":       "followup-proxy",
		"link_followup_proxy_label": "followup-label",
		"link_followup_proxy_exit":  "JP/followup",
		"link_approve_proxy":        "approve-proxy",
		"link_approve_proxy_label":  "approve-label",
		"link_approve_proxy_exit":   "JP/approve",
		"workflow": map[string]any{
			"auth": map[string]any{
				"state":      "失败",
				"detail":     "显式认证失败",
				"updated_at": "2026-07-27T12:00:00",
			},
			"export": map[string]any{
				"state":  "跳过",
				"detail": "暂不导出",
			},
		},
	}
	snapshot := localOpsSnapshot([]models.MailAccount{account}, map[string]any{
		"case@example.COM": session,
	})
	snapshot["results"] = map[string]any{"CASE@example.com": "https://pay.example/link"}
	app, _ := newLocalOpsTestApp(t, snapshot)
	app.logs.Append("[认证] 登录失败", account.Email)

	details, err := app.GetAccountDetails(" case@example.COM ")
	if err != nil {
		t.Fatalf("GetAccountDetails: %v", err)
	}
	if details.Account.Email != account.Email ||
		details.Account.Password != account.Password ||
		details.Account.ClientID != account.ClientID ||
		details.Account.RefreshToken != account.RefreshToken ||
		details.Account.OpenaiRT != account.OpenaiRT ||
		details.Account.AuthPhoneNumber != account.AuthPhoneNumber ||
		details.Account.ReceiveMailbox != account.ReceiveMailbox {
		t.Fatalf("完整账户字段丢失: %#v", details.Account)
	}
	if details.Link != "https://pay.example/link" {
		t.Fatalf("Link = %q", details.Link)
	}
	if details.LinkProxy != "generic-proxy" ||
		details.LinkCreateProxy != "create-proxy" ||
		details.LinkFollowupProxy != "followup-proxy" ||
		details.LinkApproveProxy != "approve-proxy" {
		t.Fatalf("各阶段代理不完整: %#v", details)
	}
	if details.LinkProxyExit != "JP/generic" ||
		details.LinkCreateProxyLabel != "create-label" ||
		details.LinkFollowupProxyExit != "JP/followup" ||
		details.LinkApproveProxyLabel != "approve-label" {
		t.Fatalf("代理标签或出口不完整: %#v", details)
	}
	if details.Fingerprint["locale"] != "en-US" ||
		details.Account.BrowserFingerprint["timezone"] != "Asia/Tokyo" {
		t.Fatalf("指纹未返回: fingerprint=%#v account=%#v", details.Fingerprint, details.Account)
	}
	if len(details.Logs) != 1 ||
		details.Logs[0].Scope != "account" ||
		details.Logs[0].Email != strings.ToLower(account.Email) {
		t.Fatalf("结构化账户日志不正确: %#v", details.Logs)
	}

	if got := details.Workflow["email"]; got.State != "未开始" || got.Detail != "已导入账号，尚未检查邮箱" {
		t.Fatalf("email workflow = %#v", got)
	}
	if got := details.Workflow["auth"]; got.State != "失败" || got.Detail != "显式认证失败" {
		t.Fatalf("显式 auth workflow 未保留: %#v", got)
	}
	if got := details.Workflow["session"]; got.State != "成功" ||
		got.Detail != "plan=plus，到期=2027-01-02T03:04:05Z" {
		t.Fatalf("Session 派生错误: %#v", got)
	}
	if got := details.Workflow["trial"]; got.State != "进行中" || got.Detail != "checking eligible=" {
		t.Fatalf("未知试用状态应为进行中: %#v", got)
	}
	if got := details.Workflow["link"]; got.State != "成功" || got.Detail != "长链已保存" {
		t.Fatalf("Link 派生错误: %#v", got)
	}
	if got := details.Workflow["export"]; got.State != "跳过" || got.Detail != "暂不导出" {
		t.Fatalf("显式 export workflow 未保留: %#v", got)
	}

	// 修改返回对象的每一层，再次读取必须仍得到磁盘中的原值。
	details.Session["access_token"] = "changed"
	details.Session["nested"].(map[string]any)["keep"] = "changed"
	details.Session["workflow"].(map[string]any)["auth"].(map[string]any)["state"] = "成功"
	details.Fingerprint["locale"] = "changed"
	details.Account.BrowserFingerprint["timezone"] = "changed"

	again, err := app.GetAccountDetails(account.Email)
	if err != nil {
		t.Fatalf("GetAccountDetails again: %v", err)
	}
	if again.Session["access_token"] != "access-token" ||
		again.Session["nested"].(map[string]any)["keep"] != "original" ||
		again.Session["workflow"].(map[string]any)["auth"].(map[string]any)["state"] != "失败" {
		t.Fatalf("返回 Session 不是深拷贝: %#v", again.Session)
	}
	if again.Fingerprint["locale"] != "en-US" ||
		again.Account.BrowserFingerprint["timezone"] != "Asia/Tokyo" {
		t.Fatalf("返回指纹不是深拷贝: %#v", again.Fingerprint)
	}
}

func TestGetAccountDetailsExplicitWorkflowWinsOverDerivedFields(t *testing.T) {
	email := "workflow@example.com"
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{Email: email, AccountType: "free", Group: models.AccountDefaultGroup}},
		map[string]any{
			email: map[string]any{
				"access_token":        "token",
				"plus_trial_status":   "eligible",
				"plus_trial_eligible": true,
				"workflow": map[string]any{
					"session": map[string]any{"state": "失败", "detail": "Token 已失效"},
					"trial":   map[string]any{"state": "跳过", "detail": "人工跳过"},
					"link":    map[string]any{"state": "需要人工", "detail": "等待确认"},
				},
			},
		},
	))
	err := app.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		snapshot["results"] = map[string]any{email: "https://pay.example/existing"}
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		t.Fatalf("准备链接状态: %v", err)
	}

	details, err := app.GetAccountDetails(email)
	if err != nil {
		t.Fatalf("GetAccountDetails: %v", err)
	}
	if details.Workflow["session"].State != "失败" ||
		details.Workflow["trial"].State != "跳过" ||
		details.Workflow["link"].State != "需要人工" {
		t.Fatalf("显式 workflow 被派生字段覆盖: %#v", details.Workflow)
	}
	if len(details.Workflow) != 7 {
		t.Fatalf("workflow 步骤数 = %d, want 7", len(details.Workflow))
	}
}

func TestGetAccountDetailsRejectsMissingAccount(t *testing.T) {
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(nil, nil))
	if _, err := app.GetAccountDetails(" "); err == nil || !strings.Contains(err.Error(), "未指定") {
		t.Fatalf("空邮箱错误 = %v", err)
	}
	if _, err := app.GetAccountDetails("missing@example.com"); err == nil || !strings.Contains(err.Error(), "账号不存在") {
		t.Fatalf("缺失账户错误 = %v", err)
	}
}
