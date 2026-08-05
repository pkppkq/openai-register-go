package ui

// 本文件的邮箱、注册、邀请请求、浏览器接受和 Session 刷新全部使用 fake。
// 测试不会访问真实业务服务、启动 Chromium 或申请短信号码。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

type fakeInviteFlowReader struct {
	teamURL  string
	k12URL   string
	onTeam   func(float64, int)
	onK12    func(string, float64, int)
	closeErr error
}

func (f *fakeInviteFlowReader) Close() error {
	return f.closeErr
}

func (f *fakeInviteFlowReader) WaitForTeamInvite(
	_ context.Context,
	minTimestamp float64,
	timeout int,
) (string, error) {
	if f.onTeam != nil {
		f.onTeam(minTimestamp, timeout)
	}
	if f.teamURL == "" {
		return "", errors.New("fake Team 邀请不存在")
	}
	return f.teamURL, nil
}

func (f *fakeInviteFlowReader) WaitForLink(
	_ context.Context,
	keyword string,
	minTimestamp float64,
	timeout int,
) (string, error) {
	if f.onK12 != nil {
		f.onK12(keyword, minTimestamp, timeout)
	}
	if f.k12URL == "" {
		return "", errors.New("fake K12 邀请不存在")
	}
	return f.k12URL, nil
}

func preserveInviteFlowSeams(t *testing.T) {
	t.Helper()
	oldReader := inviteFlowNewMailReader
	oldAuth := inviteFlowAuthenticate
	oldRequest := inviteFlowRequestK12
	oldAccept := inviteFlowAcceptInvite
	oldRefresh := inviteFlowRefreshSession
	t.Cleanup(func() {
		inviteFlowNewMailReader = oldReader
		inviteFlowAuthenticate = oldAuth
		inviteFlowRequestK12 = oldRequest
		inviteFlowAcceptInvite = oldAccept
		inviteFlowRefreshSession = oldRefresh
	})
}

func fakeInviteAccepted(
	account models.MailAccount,
	inviteURL string,
	workspaceID string,
) WorkspaceInviteResult {
	return WorkspaceInviteResult{
		InviteURL:        inviteURL,
		WorkspaceID:      workspaceID,
		FinalURL:         "https://chatgpt.com/",
		ClickedText:      "接受邀请",
		StorageStateJSON: `{"cookies":[{"name":"accepted","value":"yes"}],"origins":[]}`,
		AcceptedAt:       "2026-07-27T12:00:00Z",
		Fingerprint:      models.FingerprintToMap(account.BrowserFingerprint),
	}
}

func fakeInviteRefreshed(
	account models.MailAccount,
	storageStateJSON string,
	workspaceID string,
	plan string,
) SessionRefreshResult {
	return SessionRefreshResult{
		Email:            account.Email,
		AccessToken:      "refreshed-" + account.Email,
		SessionJSON:      `{"accessToken":"refreshed"}`,
		StorageStateJSON: storageStateJSON,
		AccessSummary: map[string]any{
			"plan_type":       plan,
			"account_id":      workspaceID,
			"account_id_tail": tailText(workspaceID, 8),
		},
		WorkspaceID: workspaceID,
		RefreshedAt: "2026-07-27T12:00:01",
	}
}

func TestInviteFlowsRequireExplicitConfirmation(t *testing.T) {
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("confirm@example.com", "free", "")},
		map[string]any{},
	)
	calls := []func() error{
		func() error {
			_, err := app.StartTeamInviteScanJoin(TeamInviteScanJoinRequest{
				Emails: []string{"confirm@example.com"},
			})
			return err
		},
		func() error {
			_, err := app.StartK12AcceptAndRefresh(K12InviteFlowRequest{
				Emails: []string{"confirm@example.com"}, WorkspaceID: "workspace",
			})
			return err
		},
		func() error {
			_, err := app.StartK12RegisterAndJoin(K12InviteFlowRequest{
				Emails: []string{"confirm@example.com"}, WorkspaceID: "workspace",
			})
			return err
		},
	}
	for index, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "必须先确认") {
			t.Fatalf("第 %d 个未确认入口 err=%v", index+1, err)
		}
	}
	if len(app.ListJobs()) != 0 {
		t.Fatal("未确认的组合流程不应创建任务")
	}
}

func TestTeamInviteScanJoinUsesSavedSessionWithoutAuthentication(t *testing.T) {
	preserveInviteFlowSeams(t)
	const email = "team-join@example.com"
	accessToken := networkTestJWT("free", "personal")
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{email: map[string]any{
			"access_token":       accessToken,
			"storage_state_json": `{"cookies":[],"origins":[]}`,
		}},
	)

	inviteFlowNewMailReader = func(
		account *models.MailAccount,
		_ mail.Log,
		proxyURL string,
	) (inviteFlowMailReader, error) {
		if account.Email != email || proxyURL != "" {
			t.Fatalf("Team 邮箱参数异常: account=%s proxy=%q", account.Email, proxyURL)
		}
		return &fakeInviteFlowReader{
			teamURL: "https://chatgpt.com/admin/invite/accept?token=fake-team",
			onTeam: func(minTimestamp float64, timeout int) {
				if minTimestamp != 0 || timeout != 120 {
					t.Fatalf("Team 等待参数异常: min=%v timeout=%d", minTimestamp, timeout)
				}
			},
		}, nil
	}
	var authCalls atomic.Int32
	inviteFlowAuthenticate = func(
		context.Context, *App, string, models.MailAccount, string, bool, func(string),
	) (worker.SessionInfo, error) {
		authCalls.Add(1)
		return worker.SessionInfo{}, errors.New("不应执行认证")
	}
	inviteFlowAcceptInvite = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, inviteURL, proxyURL string,
		_ func(string),
	) (WorkspaceInviteResult, error) {
		if account.Email != email ||
			!strings.Contains(storageStateJSON, `"cookies"`) ||
			!strings.Contains(inviteURL, "fake-team") ||
			proxyURL != "" {
			t.Fatalf("Team 接受参数异常: %#v %q %q %q", account, storageStateJSON, inviteURL, proxyURL)
		}
		return fakeInviteAccepted(account, inviteURL, "workspace-team"), nil
	}
	inviteFlowRefreshSession = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, proxyURL, workspaceID string,
		_ func(string),
	) (SessionRefreshResult, error) {
		if !strings.Contains(storageStateJSON, "accepted") ||
			proxyURL != "" || workspaceID != "workspace-team" {
			t.Fatalf("Team 刷新参数异常: %q %q %q", storageStateJSON, proxyURL, workspaceID)
		}
		return fakeInviteRefreshed(account, storageStateJSON, workspaceID, "team"), nil
	}

	summary, err := app.StartTeamInviteScanJoin(TeamInviteScanJoinRequest{
		Emails: []string{email}, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)
	report, ok := parent.Result.(InviteFlowBatchResult)
	if !ok || report.Succeeded != 1 || report.Failed != 0 {
		t.Fatalf("Team 父任务结果异常: %#v", parent.Result)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("已有完整登录态仍调用认证 %d 次", authCalls.Load())
	}
	account, payload := loadedNetworkAccount(t, app, email)
	if account.Status != "Team邀请已加入" || account.AccountType != "team" {
		t.Fatalf("Team 账号未正确更新: %#v", account)
	}
	if payload["team_accept_result"] != "accepted" ||
		payload["team_workspace_id"] != "workspace-team" ||
		payload["access_token"] != "refreshed-"+email {
		t.Fatalf("Team Session 未正确保存: %#v", payload)
	}
}

func TestTeamInviteScanJoinAuthenticatesWhenSessionIsIncomplete(t *testing.T) {
	preserveInviteFlowSeams(t)
	const email = "team-auth@example.com"
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{},
	)
	inviteFlowNewMailReader = func(
		*models.MailAccount, mail.Log, string,
	) (inviteFlowMailReader, error) {
		return &fakeInviteFlowReader{
			teamURL: "https://chatgpt.com/admin/invite/accept?token=needs-auth",
		}, nil
	}
	var authCalls atomic.Int32
	inviteFlowAuthenticate = func(
		_ context.Context,
		_ *App,
		jobID string,
		account models.MailAccount,
		dynamicProxy string,
		headless bool,
		_ func(string),
	) (worker.SessionInfo, error) {
		authCalls.Add(1)
		if jobID == "" || account.Email != email || dynamicProxy != "" || headless {
			t.Fatalf("Team 认证参数异常: job=%q account=%s proxy=%q headless=%v",
				jobID, account.Email, dynamicProxy, headless)
		}
		return worker.SessionInfo{
			AccessToken:      networkTestJWT("free", "personal"),
			SessionJSON:      `{"accessToken":"fake-auth"}`,
			StorageStateJSON: `{"cookies":[{"name":"authenticated"}],"origins":[]}`,
		}, nil
	}
	inviteFlowAcceptInvite = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, inviteURL, _ string,
		_ func(string),
	) (WorkspaceInviteResult, error) {
		if !strings.Contains(storageStateJSON, "authenticated") {
			t.Fatalf("认证结果未传给邀请接受: %q", storageStateJSON)
		}
		return fakeInviteAccepted(account, inviteURL, "workspace-team"), nil
	}
	inviteFlowRefreshSession = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, _ string, workspaceID string,
		_ func(string),
	) (SessionRefreshResult, error) {
		return fakeInviteRefreshed(account, storageStateJSON, workspaceID, "team"), nil
	}

	summary, err := app.StartTeamInviteScanJoin(TeamInviteScanJoinRequest{
		Emails: []string{email}, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)
	report := result.Result.(InviteFlowBatchResult)
	if authCalls.Load() != 1 || !report.Accounts[0].Authenticated {
		t.Fatalf("Team 缺登录态认证结果异常: calls=%d result=%#v", authCalls.Load(), report)
	}
}

func TestK12AcceptAndRefreshRunsRequestWaitAcceptRefreshInOrder(t *testing.T) {
	preserveInviteFlowSeams(t)
	const email = "k12-accept@example.com"
	accessToken := networkTestJWT("free", "personal")
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture(email, "free", "")},
		map[string]any{email: map[string]any{
			"access_token":       accessToken,
			"storage_state_json": `{"cookies":[],"origins":[]}`,
		}},
	)

	var mu sync.Mutex
	order := []string{}
	appendOrder := func(value string) {
		mu.Lock()
		order = append(order, value)
		mu.Unlock()
	}
	inviteFlowRequestK12 = func(
		_ context.Context,
		token, workspaceID, proxyURL string,
	) (int, string, error) {
		appendOrder("request")
		if token != accessToken || workspaceID != "workspace-request" || proxyURL != "" {
			t.Fatalf("K12 请求参数异常: %q %q %q", token, workspaceID, proxyURL)
		}
		return 202, "queued", nil
	}
	inviteFlowNewMailReader = func(
		account *models.MailAccount,
		_ mail.Log,
		proxyURL string,
	) (inviteFlowMailReader, error) {
		if account.Email != email || proxyURL != "" {
			t.Fatalf("K12 邮箱参数异常: %s %q", account.Email, proxyURL)
		}
		return &fakeInviteFlowReader{
			k12URL: "https://chatgpt.com/k12-invite/fake?wId=workspace-from-link",
			onK12: func(keyword string, minTimestamp float64, timeout int) {
				appendOrder("wait")
				if keyword != "k12-invite" || minTimestamp != 0 || timeout != 240 {
					t.Fatalf("K12 等待参数异常: %q %v %d", keyword, minTimestamp, timeout)
				}
			},
		}, nil
	}
	inviteFlowAcceptInvite = func(
		_ context.Context,
		account models.MailAccount,
		_ string, inviteURL, _ string,
		_ func(string),
	) (WorkspaceInviteResult, error) {
		appendOrder("accept")
		return fakeInviteAccepted(account, inviteURL, ""), nil
	}
	inviteFlowRefreshSession = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, _ string, workspaceID string,
		_ func(string),
	) (SessionRefreshResult, error) {
		appendOrder("refresh")
		if workspaceID != "workspace-from-link" {
			t.Fatalf("刷新未采用邀请链接 workspace: %q", workspaceID)
		}
		return fakeInviteRefreshed(account, storageStateJSON, workspaceID, "k12"), nil
	}
	inviteFlowAuthenticate = func(
		context.Context, *App, string, models.MailAccount, string, bool, func(string),
	) (worker.SessionInfo, error) {
		return worker.SessionInfo{}, errors.New("接受并刷新不应执行注册")
	}

	summary, err := app.StartK12AcceptAndRefresh(K12InviteFlowRequest{
		Emails: []string{email}, WorkspaceID: "workspace-request", Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)
	report := parent.Result.(InviteFlowBatchResult)
	if report.Succeeded != 1 || report.Accounts[0].RequestStatus != "202" {
		t.Fatalf("K12 结果异常: %#v", report)
	}
	mu.Lock()
	gotOrder := strings.Join(order, ",")
	mu.Unlock()
	if gotOrder != "request,wait,accept,refresh" {
		t.Fatalf("K12 组合顺序=%q", gotOrder)
	}
	account, payload := loadedNetworkAccount(t, app, email)
	if account.Status != "K12接受已刷新" || account.AccountType != "k12" {
		t.Fatalf("K12 账号未正确更新: %#v", account)
	}
	if payload["k12_status"] != "202" ||
		payload["k12_workspace_id"] != "workspace-from-link" ||
		payload["k12_accept_result"] != "accepted" {
		t.Fatalf("K12 审计字段未正确保存: %#v", payload)
	}
}

func TestK12RegisterAndJoinUsesBoundedSettingsConcurrency(t *testing.T) {
	preserveInviteFlowSeams(t)
	emails := []string{
		"k12-one@example.com",
		"k12-two@example.com",
		"k12-three@example.com",
		"k12-four@example.com",
	}
	rows := make([]any, 0, len(emails))
	for _, email := range emails {
		rows = append(rows, networkAccountFixture(email, "free", ""))
	}
	app := newNetworkOpsTestApp(t, rows, map[string]any{})
	if err := app.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		values := snapshot["settings"].(map[string]any)
		values["k12_concurrency"] = 2
		values["auth_concurrency"] = 3
		return snapshot, map[string]bool{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan string, len(emails))
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	inviteFlowAuthenticate = func(
		ctx context.Context,
		_ *App,
		jobID string,
		account models.MailAccount,
		dynamicProxy string,
		_ bool,
		_ func(string),
	) (worker.SessionInfo, error) {
		if jobID == "" || dynamicProxy != "" {
			t.Fatalf("一键注册认证参数异常: job=%q proxy=%q", jobID, dynamicProxy)
		}
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- account.Email
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return worker.SessionInfo{}, ctx.Err()
		}
		active.Add(-1)
		return worker.SessionInfo{
			AccessToken:      networkTestJWT("free", "personal-"+account.Email),
			SessionJSON:      `{"accessToken":"fake-register"}`,
			StorageStateJSON: `{"cookies":[{"name":"registered"}],"origins":[]}`,
		}, nil
	}
	inviteFlowRequestK12 = func(
		_ context.Context,
		accessToken, workspaceID, proxyURL string,
	) (int, string, error) {
		if accessToken == "" || workspaceID != "workspace-batch" || proxyURL != "" {
			t.Fatalf("一键注册 K12 请求参数异常: %q %q %q", accessToken, workspaceID, proxyURL)
		}
		return 200, "ok", nil
	}
	inviteFlowNewMailReader = func(
		account *models.MailAccount,
		_ mail.Log,
		_ string,
	) (inviteFlowMailReader, error) {
		return &fakeInviteFlowReader{
			k12URL: "https://chatgpt.com/k12-invite/" + account.Email + "?wId=workspace-batch",
		}, nil
	}
	inviteFlowAcceptInvite = func(
		_ context.Context,
		account models.MailAccount,
		_ string, inviteURL, _ string,
		_ func(string),
	) (WorkspaceInviteResult, error) {
		return fakeInviteAccepted(account, inviteURL, "workspace-batch"), nil
	}
	inviteFlowRefreshSession = func(
		_ context.Context,
		account models.MailAccount,
		storageStateJSON, _ string, workspaceID string,
		_ func(string),
	) (SessionRefreshResult, error) {
		return fakeInviteRefreshed(account, storageStateJSON, workspaceID, "k12"), nil
	}

	summary, err := app.StartK12RegisterAndJoin(K12InviteFlowRequest{
		Emails: emails, WorkspaceID: "workspace-batch", Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for count := 0; count < 2; count++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("并发窗口未按时启动")
		}
	}
	select {
	case third := <-entered:
		t.Fatalf("释放前启动了第 3 个认证任务: %s", third)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	parent := waitNetworkJob(t, app, summary.Job.ID, StatusSucceeded)
	report := parent.Result.(InviteFlowBatchResult)
	if report.Succeeded != len(emails) || report.Failed != 0 {
		t.Fatalf("一键注册批量结果异常: %#v", report)
	}
	if maximum.Load() != 2 {
		t.Fatalf("认证最大并发=%d，期望=2", maximum.Load())
	}
	for _, accountResult := range report.Accounts {
		if !accountResult.Authenticated || accountResult.Status != "K12接受已刷新" {
			t.Fatalf("账号结果异常: %#v", accountResult)
		}
	}
}
