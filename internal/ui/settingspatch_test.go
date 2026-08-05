package ui

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestPatchSettingsPreservesConcurrentProxyRotation(t *testing.T) {
	snapshot := localOpsSnapshot([]models.MailAccount{{
		Email: "settings@example.com", AccountType: "free", Group: models.AccountDefaultGroup,
	}}, nil)
	snapshot["settings"] = map[string]any{
		"dynamic_proxies": "rotated-second:2000\nrotated-first:1000",
		"headless":        false,
		"target_amount":   "20",
	}
	app, stateFile := newLocalOpsTestApp(t, snapshot)

	updated, err := app.PatchSettings(map[string]any{
		"headless": true,
	})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	if !updated.Headless || updated.DynamicProxies != "rotated-second:2000\nrotated-first:1000" {
		t.Fatalf("返回设置覆盖了代理轮换: %#v", updated)
	}
	raw := readLocalOpsRawState(t, stateFile)
	settingsMap, _ := raw["settings"].(map[string]any)
	if settingsMap["dynamic_proxies"] != "rotated-second:2000\nrotated-first:1000" {
		t.Fatalf("代理池被旧设置恢复: %#v", settingsMap["dynamic_proxies"])
	}
	if settingsMap["headless"] != true || settingsMap["target_amount"] != "20" {
		t.Fatalf("字段级 patch 异常: %#v", settingsMap)
	}
}

func TestPatchSettingsDeepMergesProviderRole(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["settings"] = map[string]any{
		"provider_proxy_configs": map[string]any{
			"create": map[string]any{
				"enabled":  true,
				"username": "create-user",
				"endpoint": "create.example:1000",
			},
			"approve": map[string]any{
				"enabled":  false,
				"endpoint": "old.example:2000",
			},
		},
	}
	app, _ := newLocalOpsTestApp(t, snapshot)
	updated, err := app.PatchSettings(map[string]any{
		"provider_proxy_configs": map[string]any{
			"approve": map[string]any{
				"enabled":  true,
				"endpoint": "new.example:3000",
			},
		},
	})
	if err != nil {
		t.Fatalf("PatchSettings provider: %v", err)
	}
	if got := updated.ProviderProxyConfigs["create"]; !got.Enabled || got.Username != "create-user" || got.Endpoint != "create.example:1000" {
		t.Fatalf("未修改 role 被覆盖: %#v", got)
	}
	if got := updated.ProviderProxyConfigs["approve"]; !got.Enabled || got.Endpoint != "new.example:3000" {
		t.Fatalf("Approve patch 未生效: %#v", got)
	}
}

func TestPatchSettingsRejectsUnknownField(t *testing.T) {
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(nil, nil))
	if _, err := app.PatchSettings(map[string]any{"dynamic_proxiez": "bad"}); err == nil ||
		!strings.Contains(err.Error(), "未知的设置字段") {
		t.Fatalf("未知字段 err=%v", err)
	}
}

func TestPatchSettingsAppliesCloudMailRuntimeConfigAtomically(t *testing.T) {
	snapshot := localOpsSnapshot([]models.MailAccount{
		{
			Email: "child@" + models.DefaultDomainMailDomain,
			Group: models.AccountDefaultGroup,
		},
		{
			Email:          "legacy@example.com",
			Group:          models.AccountDefaultGroup,
			MailProvider:   "cloudmail",
			CloudMailBase:  "https://old.invalid",
			CloudMailToken: "old-token",
		},
	}, nil)
	snapshot["settings"] = map[string]any{
		"cloud_mail_enabled": false,
		"cloud_mail_base":    "https://old.invalid",
		"cloud_mail_token":   "old-token",
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	updated, err := app.PatchSettings(map[string]any{
		"cloud_mail_enabled": true,
		"cloud_mail_base":    " https://mail.example.test/// ",
		"cloud_mail_token":   " new-token ",
	})
	if err != nil {
		t.Fatalf("PatchSettings Cloud Mail: %v", err)
	}
	if !updated.CloudMailEnabled ||
		updated.CloudMailBase != "https://mail.example.test" ||
		updated.CloudMailToken != "new-token" {
		t.Fatalf("Cloud Mail 设置未规范化: %#v", updated)
	}

	child, err := app.GetAccountDetails("child@" + models.DefaultDomainMailDomain)
	if err != nil {
		t.Fatalf("GetAccountDetails child: %v", err)
	}
	if child.Account.MailProvider != "cloudmail" ||
		child.Account.CloudMailBase != "https://mail.example.test" ||
		child.Account.CloudMailToken != "new-token" {
		t.Fatalf("目标域名账号未应用 Cloud Mail: %#v", child)
	}
	legacy, err := app.GetAccountDetails("legacy@example.com")
	if err != nil {
		t.Fatalf("GetAccountDetails legacy: %v", err)
	}
	if legacy.Account.MailProvider != "" ||
		legacy.Account.CloudMailBase != "" ||
		legacy.Account.CloudMailToken != "" {
		t.Fatalf("非目标域名旧 Cloud Mail 字段未清理: %#v", legacy)
	}
}

func TestPatchSettingsRejectsInvalidCloudMailWithoutPartialWrite(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["settings"] = map[string]any{
		"cloud_mail_enabled": false,
		"cloud_mail_base":    "https://saved.example.test",
		"cloud_mail_token":   "",
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	if _, err := app.PatchSettings(map[string]any{
		"cloud_mail_enabled": true,
	}); err == nil || !strings.Contains(err.Error(), "程序 Token") {
		t.Fatalf("启用空 Token 时 err=%v", err)
	}
	if _, err := app.PatchSettings(map[string]any{
		"cloud_mail_base": "ftp://invalid.example.test",
	}); err == nil || !strings.Contains(err.Error(), "Base URL") {
		t.Fatalf("无效 Base URL 时 err=%v", err)
	}
	after, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if after.CloudMailEnabled ||
		after.CloudMailBase != "https://saved.example.test" ||
		after.CloudMailToken != "" {
		t.Fatalf("失败 patch 发生了部分写入: %#v", after)
	}
}
