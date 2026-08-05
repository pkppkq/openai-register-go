package ui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

func TestStartDomainRandomRTCreatesCloudAccountBeforeFakeJob(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["settings"] = map[string]any{
		"account_groups":           []any{models.AccountDefaultGroup},
		"account_group_filter":     settings.AccountAllGroup,
		"cloud_mail_enabled":       true,
		"cloud_mail_base":          "https://mail.invalid",
		"cloud_mail_token":         "fake-token",
		"domain_mail_domain":       models.DefaultDomainMailDomain,
		"smsbower_enabled":         true,
		"smsbower_api_key":         "fake-never-called",
		"smsbower_country":         "1",
		"smsbower_service":         "oi",
		"smsbower_max_price":       "0.01",
		"turnstile_solver_url":     "http://127.0.0.1:1",
		"turnstile_solver_enabled": false,
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	if _, err := app.StartDomainRandomRT(DomainRandomRTRequest{}); err == nil {
		t.Fatal("未确认的随机取 RT 操作应被拒绝")
	}

	old := startDomainRandomRTJob
	t.Cleanup(func() { startDomainRandomRTJob = old })
	captured := ""
	startDomainRandomRTJob = func(_ *App, email string) (JobView, error) {
		captured = email
		return JobView{ID: "fake-domain-job", Kind: JobRegisterAndRT, Email: email, Status: StatusRunning}, nil
	}

	result, err := app.StartDomainRandomRT(DomainRandomRTRequest{Confirmed: true})
	if err != nil {
		t.Fatalf("StartDomainRandomRT: %v", err)
	}
	if result.Email == "" || result.Email != captured || !strings.HasSuffix(result.Email, "@"+models.DefaultDomainMailDomain) {
		t.Fatalf("随机邮箱或任务参数异常: %#v captured=%q", result, captured)
	}
	page, err := app.ListAccounts(AccountFilter{})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("随机账号未落盘: page=%#v err=%v", page, err)
	}
	row := page.Rows[0]
	if row.MailProvider != "cloudmail" || row.Status != "域名邮箱待注册" {
		t.Fatalf("随机 Cloud Mail 账号字段异常: %#v", row)
	}
}

func TestStartBatchRelinkUsesPerAccountAssignmentsWithFakeRunner(t *testing.T) {
	p1 := "http://u:p@10.0.0.1:8001"
	p2 := "http://u:p@10.0.0.2:8002"
	p3 := "http://u:p@10.0.0.3:8003"
	c1 := "http://u:p@10.1.0.1:8101"
	c2 := "http://u:p@10.1.0.2:8102"
	f1 := "http://u:p@10.2.0.1:8201"
	f2 := "http://u:p@10.2.0.2:8202"
	a1 := "http://u:p@10.3.0.1:8301"
	a2 := "http://u:p@10.3.0.2:8302"
	snapshot := localOpsSnapshot([]models.MailAccount{
		{Email: "a@example.com", Password: "pw-a"},
		{Email: "b@example.com", Password: "pw-b"},
	}, nil)
	snapshot["settings"] = map[string]any{
		"dynamic_proxies":        strings.Join([]string{p1, p2, p3}, "\n"),
		"payment_dynamic_proxy":  strings.Join([]string{c1, c2}, "\n"),
		"followup_dynamic_proxy": strings.Join([]string{f1, f2}, "\n"),
		"approve_dynamic_proxy":  strings.Join([]string{a1, a2}, "\n"),
		"proxy_route_mode":       settings.ProxyRouteModeDefault,
		// 本测试要观察两个账号的固定分配；长链成功暂停由
		// linksuccess_test.go 独立验证。
		"pause_others_on_link_success": false,
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	if _, err := app.StartBatchRelink(BatchRelinkRequest{Emails: []string{"a@example.com"}}); err == nil {
		t.Fatal("未确认的批量重新获取应被拒绝")
	}

	type assignment struct {
		email   string
		extract string
		triple  [3]string
	}
	var mu sync.Mutex
	assignments := []assignment{}
	old := runRelinkAssignment
	t.Cleanup(func() { runRelinkAssignment = old })
	runRelinkAssignment = func(
		_ context.Context,
		_ *App,
		_ string,
		account models.MailAccount,
		extract string,
		triple [3]string,
		_ func(string),
	) (any, string, error) {
		mu.Lock()
		assignments = append(assignments, assignment{email: account.Email, extract: extract, triple: triple})
		mu.Unlock()
		return &worker.PayLinkResult{
			URL:         "https://pay.example/" + account.Email,
			CheckoutURL: "https://pay.example/" + account.Email,
		}, extract, nil
	}

	summary, err := app.StartBatchRelink(BatchRelinkRequest{
		Emails: []string{"a@example.com", "b@example.com"}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("StartBatchRelink: %v", err)
	}
	waitStatus(t, app, summary.Job.ID, StatusSucceeded)

	mu.Lock()
	got := append([]assignment(nil), assignments...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("fake runner 调用次数=%d，期望 2: %#v", len(got), got)
	}
	byEmail := map[string]assignment{}
	for _, item := range got {
		byEmail[item.email] = item
	}
	want := map[string]assignment{
		"a@example.com": {
			email: "a@example.com", extract: proxypool.NormalizeProxyURL(p1),
			triple: [3]string{
				proxypool.NormalizeProxyURL(c1),
				proxypool.NormalizeProxyURL(f1),
				proxypool.NormalizeProxyURL(a1),
			},
		},
		"b@example.com": {
			email: "b@example.com", extract: proxypool.NormalizeProxyURL(p2),
			triple: [3]string{
				proxypool.NormalizeProxyURL(c2),
				proxypool.NormalizeProxyURL(f2),
				proxypool.NormalizeProxyURL(a2),
			},
		},
	}
	for email, expected := range want {
		actual := byEmail[email]
		if actual.extract != expected.extract || actual.triple != expected.triple {
			t.Fatalf("%s 分配异常: got=%#v want=%#v", email, actual, expected)
		}
	}
	state, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	rotated := settings.FromSnapshot(state).DynamicProxies
	wantRotated := strings.Join([]string{
		proxypool.NormalizeProxyURL(p3),
		proxypool.NormalizeProxyURL(p1),
		proxypool.NormalizeProxyURL(p2),
	}, "\n")
	if rotated != wantRotated {
		t.Fatalf("注册代理池未按 TakeN 轮转:\n got=%q\nwant=%q", rotated, wantRotated)
	}
}
