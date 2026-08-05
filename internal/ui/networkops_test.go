package ui

// 本文件所有远程能力都替换为 fake；没有任何测试会访问真实 OpenAI、
// Team、K12、Stripe、SMSBower、Cloud Mail 或 Turnstile 服务。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/opll"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
	"github.com/pkppkq/openai-register-go/internal/state"
)

func newNetworkOpsTestApp(t *testing.T, accounts []any, sessions map[string]any) *App {
	t.Helper()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "fixture.json")
	dataDir := filepath.Join(dir, "fixture-data")
	snapshot := map[string]any{
		"schema_version":  1,
		"accounts":        accounts,
		"session_results": sessions,
		"settings": map[string]any{
			"local_proxy":            "",
			"proxy_route_mode":       settings.ProxyRouteModeDefault,
			"dynamic_proxies":        "",
			"payment_dynamic_proxy":  "",
			"followup_dynamic_proxy": "",
			"k12_workspace_id":       "workspace-default",
			"cloud_mail_base":        "https://cloud.invalid",
			"cloud_mail_token":       "saved-token",
			"smsbower_api_key":       "saved-sms-key",
			"smsbower_service":       "dr",
			"smsbower_country":       "33",
			"turnstile_solver_url":   "http://solver.invalid",
		},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return &App{
		stateFile: stateFile,
		dataDir:   dataDir,
		store:     state.New(stateFile, dataDir),
		jobs:      newJobRegistry(),
		logSink:   func(string) {},
	}
}

func networkAccountFixture(email, accountType, openaiRT string) map[string]any {
	return map[string]any{
		"email":         email,
		"password":      "pw",
		"client_id":     "client",
		"refresh_token": "mail-rt",
		"openai_rt":     openaiRT,
		"account_type":  accountType,
		"status":        "旧状态",
		"group":         models.AccountDefaultGroup,
	}
}

func networkTestJWT(plan, accountID string) string {
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"exp": float64(2_000_000_000),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-1",
		},
	})
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims) + "." + strings.Repeat("s", 48)
}

func waitNetworkJob(t *testing.T, app *App, id string, want JobStatus) NetworkJobResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, ok := app.jobView(id)
		if ok && view.Status == want {
			result, err := app.GetNetworkJobResult(id)
			if err != nil {
				t.Fatalf("GetNetworkJobResult(%s): %v", id, err)
			}
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	view, _ := app.jobView(id)
	t.Fatalf("任务 %s 状态=%q，期望=%q", id, view.Status, want)
	return NetworkJobResult{}
}

func loadedNetworkAccount(t *testing.T, app *App, email string) (models.MailAccount, map[string]any) {
	t.Helper()
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accountsFromSnapshot(snapshot) {
		if strings.EqualFold(account.Email, email) {
			return account, networkSessionByEmail(snapshot, account.Email)
		}
	}
	t.Fatalf("未找到账号 %s", email)
	return models.MailAccount{}, nil
}

func TestNetworkRefreshAccountTypePersistsFakeSuccess(t *testing.T) {
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("refresh@example.com", "free", "rt_old")},
		map[string]any{},
	)
	old := networkDetectAccountType
	t.Cleanup(func() { networkDetectAccountType = old })
	networkDetectAccountType = func(ctx context.Context, refreshToken, proxyURL string) (string, string, string, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if refreshToken != "rt_old" || proxyURL != "" {
			t.Fatalf("参数不符: rt=%q proxy=%q", refreshToken, proxyURL)
		}
		return "plus", "payload.plan=plus", "rt_rotated", nil
	}

	job, err := app.StartRefreshAccountType(RefreshAccountTypeRequest{Email: "refresh@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	result := waitNetworkJob(t, app, job.ID, StatusSucceeded)
	typed, ok := result.Result.(RefreshAccountTypeResult)
	if !ok || typed.AccountType != "plus" || !typed.RefreshTokenRotated {
		t.Fatalf("结果不符: %#v", result.Result)
	}
	account, _ := loadedNetworkAccount(t, app, "refresh@example.com")
	if account.AccountType != "plus" || account.OpenaiRT != "rt_rotated" || account.Status != "已绑定手机号" {
		t.Fatalf("账号未正确持久化: %#v", account)
	}
}

func TestNetworkTeamBindingsUseFakesAndPersist(t *testing.T) {
	token := networkTestJWT("team", "team-workspace")

	t.Run("邀请必须显式确认计费席位", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("owner@example.com", "team", "")},
			map[string]any{"owner@example.com": map[string]any{"access_token": token}},
		)
		old := networkSendTeamInvite
		t.Cleanup(func() { networkSendTeamInvite = old })
		var calls atomic.Int32
		networkSendTeamInvite = func(context.Context, string, string, string, string) (int, string, error) {
			calls.Add(1)
			return 200, "unexpected", nil
		}
		if _, err := app.StartTeamInvite(TeamInviteRequest{
			Email: "owner@example.com", TargetEmail: "member@example.com",
		}); err == nil {
			t.Fatal("未确认计费席位时应拒绝")
		}
		if calls.Load() != 0 || len(app.ListJobs()) != 0 {
			t.Fatalf("拒绝路径触发了远程调用或任务: calls=%d jobs=%d", calls.Load(), len(app.ListJobs()))
		}
	})

	t.Run("邀请成功持久化", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("owner@example.com", "team", "")},
			map[string]any{"owner@example.com": map[string]any{
				"access_token": token,
				"access_summary": map[string]any{
					"plan_type":         "team",
					"team_workspace_id": "team-workspace",
				},
			}},
		)
		old := networkSendTeamInvite
		t.Cleanup(func() { networkSendTeamInvite = old })
		networkSendTeamInvite = func(_ context.Context, accessToken, accountID, targetEmail, proxyURL string) (int, string, error) {
			if accessToken != token || accountID != "team-workspace" ||
				targetEmail != "member@example.com" || proxyURL != "" {
				t.Fatalf("邀请参数不符: %q %q %q %q", accessToken, accountID, targetEmail, proxyURL)
			}
			return 201, `{"ok":true}`, nil
		}
		job, err := app.StartTeamInvite(TeamInviteRequest{
			Email:               "owner@example.com",
			TargetEmail:         "member@example.com",
			ConfirmBillableSeat: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		account, session := loadedNetworkAccount(t, app, "owner@example.com")
		if account.Status != "Team邀请已发送" ||
			session["team_invite_status"] != "201" ||
			session["team_invite_target_email"] != "member@example.com" {
			t.Fatalf("邀请结果未持久化: account=%#v session=%#v", account, session)
		}
	})

	t.Run("退出成功持久化", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("member@example.com", "team", "")},
			map[string]any{"member@example.com": map[string]any{
				"access_token": token,
				"access_summary": map[string]any{
					"plan_type":         "team",
					"team_workspace_id": "team-workspace",
				},
			}},
		)
		old := networkLeaveTeam
		t.Cleanup(func() { networkLeaveTeam = old })
		networkLeaveTeam = func(_ context.Context, accessToken, accountID, memberEmail, proxyURL string) (int, string, openai.TeamLeaveDetail, error) {
			if accessToken != token || accountID != "team-workspace" ||
				memberEmail != "member@example.com" || proxyURL != "" {
				t.Fatalf("退出参数不符: %q %q %q %q", accessToken, accountID, memberEmail, proxyURL)
			}
			return 204, "", openai.TeamLeaveDetail{Role: "member", MemberID: "member-1"}, nil
		}
		job, err := app.StartTeamLeave(TeamLeaveRequest{Email: "member@example.com", Confirmed: true})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		account, session := loadedNetworkAccount(t, app, "member@example.com")
		if account.Status != "已退出Team（待刷新）" ||
			session["team_leave_status"] != "204" ||
			session["team_leave_member_id"] != "member-1" {
			t.Fatalf("退出结果未持久化: account=%#v session=%#v", account, session)
		}
	})
}

func TestNetworkK12TrialAndDeactivationPersistFakeSuccess(t *testing.T) {
	token := networkTestJWT("free", "personal-account")

	t.Run("K12请求", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("k12@example.com", "free", "")},
			map[string]any{"k12@example.com": map[string]any{"access_token": token}},
		)
		old := networkRequestK12Invite
		t.Cleanup(func() { networkRequestK12Invite = old })
		networkRequestK12Invite = func(_ context.Context, accessToken, workspaceID, proxyURL string) (int, string, error) {
			if accessToken != token || workspaceID != "workspace-1" || proxyURL != "" {
				t.Fatalf("K12 参数不符: %q %q %q", accessToken, workspaceID, proxyURL)
			}
			return 202, "queued", nil
		}
		job, err := app.StartK12RequestInvite(K12RequestInviteRequest{
			Email: "k12@example.com", WorkspaceID: "workspace-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		account, session := loadedNetworkAccount(t, app, "k12@example.com")
		if account.Status != "K12请求成功" ||
			session["k12_workspace_id"] != "workspace-1" ||
			session["k12_status"] != "202" {
			t.Fatalf("K12 结果未持久化: account=%#v session=%#v", account, session)
		}
	})

	t.Run("试用资格", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("trial@example.com", "free", "")},
			map[string]any{"trial@example.com": map[string]any{"access_token": token}},
		)
		old := networkDetectTrialEligibility
		t.Cleanup(func() { networkDetectTrialEligibility = old })
		networkDetectTrialEligibility = func(_ context.Context, accessToken, proxyURL, country string) (opll.TrialEligibility, error) {
			if accessToken != token || proxyURL != "" || country != "US" {
				t.Fatalf("试用参数不符: %q %q %q", accessToken, proxyURL, country)
			}
			return opll.TrialEligibility{
				Eligible: true, Status: "eligible", Amount: "0", Currency: "USD",
				Country: "US", CheckoutSessionID: "cs_fake", ProcessorEntity: "fake",
			}, nil
		}
		job, err := app.StartTrialEligibility(TrialEligibilityRequest{
			Email: "trial@example.com", ConfirmCheckout: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		account, session := loadedNetworkAccount(t, app, "trial@example.com")
		if account.Status != "有Plus试用" ||
			session["plus_trial_status"] != "eligible" ||
			session["plus_trial_eligible"] != "true" ||
			session["plus_trial_amount"] != "0" {
			t.Fatalf("试用结果未持久化: account=%#v session=%#v", account, session)
		}
	})

	t.Run("封禁邮件", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("mail@example.com", "free", "")},
			map[string]any{"mail@example.com": map[string]any{"access_token": token}},
		)
		old := networkScanDeactivation
		t.Cleanup(func() { networkScanDeactivation = old })
		networkScanDeactivation = func(_ context.Context, account *models.MailAccount, proxyURL string, days, maxMessages int, _ func(string)) (mail.DeactivationResult, error) {
			if proxyURL != "" || days != 90 || maxMessages != 120 {
				t.Fatalf("封禁扫描参数不符: %q %d %d", proxyURL, days, maxMessages)
			}
			account.RefreshToken = "mail-rt-rotated"
			account.Raw = "mail@example.com----pw----client----mail-rt-rotated"
			return mail.DeactivationResult{
				Found: true, Count: 1, CheckedAt: "2026-07-27T12:00:00",
				Latest: &mail.MailRecord{
					Subject: "Access Deactivated", Folder: "Inbox", Date: "today",
					From: "OpenAI", To: "mail@example.com", Snippet: "notice",
				},
			}, nil
		}
		job, err := app.StartDeactivationScan(DeactivationScanRequest{Email: "mail@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		account, session := loadedNetworkAccount(t, app, "mail@example.com")
		if account.Status != "疑似已封禁" || account.RefreshToken != "mail-rt-rotated" ||
			session["openai_deactivation_status"] != "found" ||
			session["openai_deactivation_subject"] != "Access Deactivated" {
			t.Fatalf("封禁扫描结果未持久化: account=%#v session=%#v", account, session)
		}
	})
}

type fakeSMSBowerReadClient struct {
	calls []string
}

func (f *fakeSMSBowerReadClient) GetBalance(context.Context) (string, error) {
	f.calls = append(f.calls, "balance")
	return "12.34", nil
}

func (f *fakeSMSBowerReadClient) GetPriceQuote(context.Context, string, string) (smsbower.PriceQuote, error) {
	f.calls = append(f.calls, "quote")
	return smsbower.PriceQuote{Cost: 0.07, Count: 8}, nil
}

func (f *fakeSMSBowerReadClient) GetPriceTiers(context.Context, string, string) ([]smsbower.PriceTier, error) {
	f.calls = append(f.calls, "tiers")
	return []smsbower.PriceTier{{Cost: 0.06, Count: 3}}, nil
}

func TestNetworkReadOnlyProbesUseOnlyFakes(t *testing.T) {
	app := newNetworkOpsTestApp(t, []any{}, map[string]any{})

	t.Run("Turnstile", func(t *testing.T) {
		old := networkProbeTurnstile
		t.Cleanup(func() { networkProbeTurnstile = old })
		networkProbeTurnstile = func(_ context.Context, base string) (TurnstileProbeResult, error) {
			if base != "http://solver.invalid" {
				t.Fatalf("base=%q", base)
			}
			return TurnstileProbeResult{
				URL: base + "/health", Status: 204, Attempts: []string{base + "/health"},
			}, nil
		}
		job, err := app.StartTurnstileProbe(TurnstileProbeRequest{URL: "http://solver.invalid"})
		if err != nil {
			t.Fatal(err)
		}
		result := waitNetworkJob(t, app, job.ID, StatusSucceeded)
		if got := result.Result.(TurnstileProbeResult).Status; got != 204 {
			t.Fatalf("status=%d", got)
		}
	})

	t.Run("SMSBower只读接口", func(t *testing.T) {
		oldFactory := networkNewSMSBowerReadClient
		oldRead := networkReadSMSBower
		t.Cleanup(func() {
			networkNewSMSBowerReadClient = oldFactory
			networkReadSMSBower = oldRead
		})
		fake := &fakeSMSBowerReadClient{}
		networkNewSMSBowerReadClient = func(apiKey string) (smsbowerReadClient, error) {
			if apiKey != "fake-key" {
				t.Fatalf("apiKey=%q", apiKey)
			}
			return fake, nil
		}
		// 运行真实只读编排函数；其客户端接口没有 GetNumber，无法租号。
		networkReadSMSBower = readSMSBower
		job, err := app.StartSMSBowerReadTest(SMSBowerReadRequest{
			APIKey: "fake-key", Service: "dr", Country: "33", IncludePrices: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		result := waitNetworkJob(t, app, job.ID, StatusSucceeded)
		if strings.Join(fake.calls, ",") != "balance,quote,tiers" {
			t.Fatalf("调用序列=%v", fake.calls)
		}
		typed := result.Result.(SMSBowerReadResult)
		if typed.Balance != "12.34" || typed.Quote == nil || len(typed.Tiers) != 1 {
			t.Fatalf("只读结果不符: %#v", typed)
		}
	})

	t.Run("CloudMail连通", func(t *testing.T) {
		old := networkProbeCloudMail
		t.Cleanup(func() { networkProbeCloudMail = old })
		networkProbeCloudMail = func(_ context.Context, base, token, probe string) error {
			if base != "https://cloud.invalid" || token != "fake-token" ||
				probe != "probe@mail.example.com" {
				t.Fatalf("Cloud Mail 参数不符: %q %q %q", base, token, probe)
			}
			return nil
		}
		job, err := app.StartCloudMailProbe(CloudMailProbeRequest{
			BaseURL: "https://cloud.invalid", Token: "fake-token", ProbeEmail: "probe@mail.example.com",
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
	})
}

func TestCloudMailTokenFakeSuccessAndFailureWriteBoundary(t *testing.T) {
	t.Run("成功才保存Token并应用域名账号", func(t *testing.T) {
		account := networkAccountFixture("child@mail.example.com", "free", "")
		app := newNetworkOpsTestApp(t, []any{account}, map[string]any{})
		old := networkGenerateCloudMailToken
		t.Cleanup(func() { networkGenerateCloudMailToken = old })
		networkGenerateCloudMailToken = func(_ context.Context, base, email, password string) (string, error) {
			if base != "https://cloud.invalid" || email != "admin@example.com" || password != "secret" {
				t.Fatalf("Token 参数不符: %q %q %q", base, email, password)
			}
			return "new-program-token", nil
		}
		job, err := app.StartCloudMailTokenGeneration(CloudMailTokenRequest{
			BaseURL: "https://cloud.invalid", AdminEmail: "admin@example.com",
			AdminPassword: "secret", ConfirmInvalidate: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusSucceeded)
		snapshot, err := app.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		st := settings.FromSnapshot(snapshot)
		updated := accountsFromSnapshot(snapshot)[0]
		if !st.CloudMailEnabled || st.CloudMailToken != "new-program-token" ||
			updated.MailProvider != "cloudmail" {
			t.Fatalf("Cloud Mail 成功结果未保存: settings=%#v account=%#v", st, updated)
		}
	})

	t.Run("远程失败不写状态", func(t *testing.T) {
		app := newNetworkOpsTestApp(t,
			[]any{networkAccountFixture("child@mail.example.com", "free", "")},
			map[string]any{},
		)
		before, err := os.ReadFile(app.stateFile)
		if err != nil {
			t.Fatal(err)
		}
		old := networkGenerateCloudMailToken
		t.Cleanup(func() { networkGenerateCloudMailToken = old })
		networkGenerateCloudMailToken = func(context.Context, string, string, string) (string, error) {
			return "", errors.New("fake offline failure")
		}
		job, err := app.StartCloudMailTokenGeneration(CloudMailTokenRequest{
			BaseURL: "https://cloud.invalid", AdminEmail: "admin@example.com",
			AdminPassword: "secret", ConfirmInvalidate: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitNetworkJob(t, app, job.ID, StatusFailed)
		after, err := os.ReadFile(app.stateFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("Cloud Mail Token 远程失败时不应写入状态")
		}
	})
}

func TestNetworkJobCancellationAndSameAccountConflict(t *testing.T) {
	token := networkTestJWT("free", "personal-account")
	app := newNetworkOpsTestApp(t,
		[]any{networkAccountFixture("cancel@example.com", "free", "")},
		map[string]any{"cancel@example.com": map[string]any{"access_token": token}},
	)
	before, err := os.ReadFile(app.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	old := networkDetectTrialEligibility
	t.Cleanup(func() { networkDetectTrialEligibility = old })
	started := make(chan struct{})
	networkDetectTrialEligibility = func(ctx context.Context, _, _, _ string) (opll.TrialEligibility, error) {
		close(started)
		<-ctx.Done()
		return opll.TrialEligibility{}, ctx.Err()
	}

	first, err := app.StartTrialEligibility(TrialEligibilityRequest{
		Email: "cancel@example.com", ConfirmCheckout: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fake 远程函数未启动")
	}
	if _, err := app.StartTrialEligibility(TrialEligibilityRequest{
		Email: "cancel@example.com", ConfirmCheckout: true,
	}); err == nil {
		t.Fatal("同一账号已有任务时应拒绝第二个任务")
	}
	if err := app.CancelJob(first.ID); err != nil {
		t.Fatal(err)
	}
	waitNetworkJob(t, app, first.ID, StatusCancelled)
	after, err := os.ReadFile(app.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("取消任务不应写入失败结果")
	}
}
