package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestWorkspaceInviteURLValidation(t *testing.T) {
	valid := "https://chatgpt.com/k12-invite/abc?wId=workspace-1"
	if !validChatGPTInviteURL(valid) || workspaceIDFromInviteURL(valid) != "workspace-1" {
		t.Fatalf("有效链接未识别: %s", valid)
	}
	for _, raw := range []string{
		"http://chatgpt.com/invite/a",
		"https://evil.example/invite/a",
		"https://chatgpt.com/",
		"not a url",
	} {
		if validChatGPTInviteURL(raw) {
			t.Errorf("不应接受 %q", raw)
		}
	}
}

func TestStartAcceptWorkspaceInvitePersistsFakeRefresh(t *testing.T) {
	email := "invite@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{email: map[string]any{
			"storage_state_json": `{"cookies":[],"origins":[]}`,
		}},
	)
	oldAccept := workspaceInviteAcceptOne
	oldRefresh := sessionRefreshOne
	t.Cleanup(func() {
		workspaceInviteAcceptOne = oldAccept
		sessionRefreshOne = oldRefresh
	})
	workspaceInviteAcceptOne = func(
		ctx context.Context,
		account models.MailAccount,
		storageStateJSON, inviteURL, proxyURL string,
		log func(string),
	) (WorkspaceInviteResult, error) {
		if ctx.Err() != nil || account.Email != email || proxyURL != "" ||
			!strings.Contains(inviteURL, "k12-invite") {
			t.Fatalf("接受邀请参数不符: %#v %q %q", account, inviteURL, proxyURL)
		}
		return WorkspaceInviteResult{
			FinalURL:         "https://chatgpt.com/",
			ClickedText:      "接受邀请",
			StorageStateJSON: `{"cookies":[{"name":"accepted"}],"origins":[]}`,
			AcceptedAt:       "2026-07-27T12:00:00Z",
		}, nil
	}
	sessionRefreshOne = func(
		ctx context.Context,
		account models.MailAccount,
		storageStateJSON, proxyURL, workspaceID string,
		log func(string),
	) (SessionRefreshResult, error) {
		if ctx.Err() != nil || workspaceID != "workspace-k12" ||
			!strings.Contains(storageStateJSON, "accepted") {
			t.Fatalf("刷新参数不符: %q %q", storageStateJSON, workspaceID)
		}
		return SessionRefreshResult{
			Email:            account.Email,
			AccessToken:      "k12-token",
			SessionJSON:      `{"accessToken":"k12-token"}`,
			StorageStateJSON: storageStateJSON,
			AccessSummary:    map[string]any{"plan_type": "k12", "account_id": workspaceID},
			WorkspaceID:      workspaceID,
			RefreshedAt:      "2026-07-27T12:00:01",
		}, nil
	}

	job, err := app.StartAcceptWorkspaceInvite(WorkspaceInviteRequest{
		Email:          email,
		Kind:           "k12",
		InviteURL:      "https://chatgpt.com/k12-invite/abc?wId=workspace-k12",
		RefreshSession: true,
		Confirmed:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, job.ID, StatusSucceeded)

	account, payload := loadedNetworkAccount(t, app, email)
	if account.Status != "K12请求成功/Session已刷新" || account.AccountType != "k12" {
		t.Fatalf("账号未更新: %#v", account)
	}
	if payload["k12_accept_result"] != "accepted" ||
		payload["access_token"] != "k12-token" ||
		payload["target_workspace_id"] != "workspace-k12" {
		t.Fatalf("邀请结果未写回: %#v", payload)
	}
}

func TestStartAcceptWorkspaceInviteRequiresConfirmation(t *testing.T) {
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("confirm@example.com", "team", "")},
		map[string]any{},
	)
	if _, err := app.StartAcceptWorkspaceInvite(WorkspaceInviteRequest{
		Email: "confirm@example.com", Kind: "team",
	}); err == nil || !strings.Contains(err.Error(), "必须先确认") {
		t.Fatalf("错误=%v", err)
	}
	if len(app.ListJobs()) != 0 {
		t.Fatal("未确认请求不应创建任务")
	}
}
