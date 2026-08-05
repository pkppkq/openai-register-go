package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestStartRefreshSessionPersistsFakeK12Result(t *testing.T) {
	email := "refresh@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{email: map[string]any{
			"storage_state_json": `{"cookies":[],"origins":[]}`,
			"k12_workspace_id":   "workspace-k12",
		}},
	)
	old := sessionRefreshOne
	t.Cleanup(func() { sessionRefreshOne = old })
	fingerprint := models.GenerateRegisterFingerprint()
	sessionRefreshOne = func(
		ctx context.Context,
		account models.MailAccount,
		storageStateJSON, proxyURL, workspaceID string,
		log func(string),
	) (SessionRefreshResult, error) {
		if err := ctx.Err(); err != nil {
			return SessionRefreshResult{}, err
		}
		if account.Email != email || !strings.Contains(storageStateJSON, "cookies") ||
			proxyURL != "" || workspaceID != "workspace-k12" {
			t.Fatalf("刷新参数不符: %#v %q %q %q", account, storageStateJSON, proxyURL, workspaceID)
		}
		log("fake Session 刷新完成")
		return SessionRefreshResult{
			Email:            email,
			AccessToken:      "access-token",
			SessionJSON:      `{"accessToken":"access-token"}`,
			StorageStateJSON: `{"cookies":[{"name":"saved"}],"origins":[]}`,
			AccessSummary: map[string]any{
				"plan_type":       "k12",
				"account_id_tail": "pace-k12",
			},
			Fingerprint: models.FingerprintToMap(&fingerprint),
			WorkspaceID: "workspace-k12",
			RefreshedAt: "2026-07-27T12:00:00",
		}, nil
	}

	job, err := app.StartRefreshSession(SessionRefreshRequest{Email: email, K12: true})
	if err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, job.ID, StatusSucceeded)

	account, payload := loadedNetworkAccount(t, app, email)
	if account.Status != "K12 Session已刷新" || account.AccountType != "k12" {
		t.Fatalf("账号未更新: %#v", account)
	}
	if payload["access_token"] != "access-token" ||
		payload["target_workspace_id"] != "workspace-k12" ||
		payload["session_refresh_error"] != "" {
		t.Fatalf("Session 未完整写回: %#v", payload)
	}
	if account.BrowserFingerprint == nil || account.BrowserFingerprint.UserAgent != fingerprint.UserAgent {
		t.Fatalf("指纹未写回: %#v", account.BrowserFingerprint)
	}
}

func TestStartRefreshSessionRejectsMissingStorageBeforeJob(t *testing.T) {
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("missing@example.com", "free", "")},
		map[string]any{},
	)
	if _, err := app.StartRefreshSession(SessionRefreshRequest{Email: "missing@example.com"}); err == nil ||
		!strings.Contains(err.Error(), "storage_state_json") {
		t.Fatalf("错误=%v", err)
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Fatalf("拒绝后仍创建任务: %#v", jobs)
	}
}

func TestStartRefreshSessionPersistsFakeFailure(t *testing.T) {
	email := "failed-refresh@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "plus", "")},
		map[string]any{email: map[string]any{"storage_state_json": `{}`}},
	)
	old := sessionRefreshOne
	t.Cleanup(func() { sessionRefreshOne = old })
	sessionRefreshOne = func(
		context.Context,
		models.MailAccount,
		string, string, string,
		func(string),
	) (SessionRefreshResult, error) {
		return SessionRefreshResult{}, errors.New("fake refresh failure")
	}

	job, err := app.StartRefreshSession(SessionRefreshRequest{Email: email})
	if err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, job.ID, StatusFailed)
	account, payload := loadedNetworkAccount(t, app, email)
	if account.Status != "刷新Session失败" ||
		!strings.Contains(networkText(payload["session_refresh_error"]), "fake refresh failure") {
		t.Fatalf("失败状态未保存: account=%#v payload=%#v", account, payload)
	}
}
