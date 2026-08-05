package ui

// 所有代理检测都替换为 fake；本文件不会连接代理、OpenAI 或 Stripe。

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

func TestCountProxyPoolTextUsesPythonParser(t *testing.T) {
	app := New()
	if got := app.CountProxyPoolText("http://a.example:8001 http://b.example:8002\n\nc.example:8003"); got != 3 {
		t.Fatalf("CountProxyPoolText()=%d, want 3", got)
	}
}

func TestProxyPoolPrecheckUsesUniqueFakeChecks(t *testing.T) {
	app := newNetworkOpsTestApp(t, nil, map[string]any{})
	_, err := app.PatchSettings(map[string]any{
		"payment_dynamic_proxy":           "jp.example:8001\nbad.example:8002",
		"followup_dynamic_proxy":          "bad.example:8002\nfollow.example:8003",
		"approve_dynamic_proxy":           "follow.example:8003",
		"link_proxy_region":               "不限",
		"link_proxy_precheck_limit":       20,
		"link_proxy_precheck_concurrency": 3,
	})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}

	if _, err := app.StartProxyPoolPrecheck(ProxyPoolOperationRequest{}); err == nil ||
		!strings.Contains(err.Error(), "明确确认") {
		t.Fatalf("未确认时 err=%v", err)
	}
	if len(app.ListJobs()) != 0 {
		t.Fatal("确认失败不应登记代理任务")
	}

	old := runManualProxyHealthCheck
	t.Cleanup(func() { runManualProxyHealthCheck = old })
	var (
		mu    sync.Mutex
		calls = map[string]int{}
	)
	runManualProxyHealthCheck = func(
		_ context.Context,
		_ string,
		proxy string,
		full bool,
		_ func(string),
	) models.ProxyHealthResult {
		if !full {
			t.Error("支付代理预检必须使用完整出口检测")
		}
		mu.Lock()
		calls[proxy]++
		mu.Unlock()
		if strings.Contains(proxy, "bad.example") {
			return models.ProxyHealthResult{
				Success: false, FailedStage: "ChatGPT", Error: "连接失败",
			}
		}
		return models.ProxyHealthResult{
			Success: true, IP: "203.0.113.10", Country: "JP",
			Timezone: "Asia/Tokyo", ChatGPTStatus: 200, StripeStatus: 200,
		}
	}

	job, err := app.StartProxyPoolPrecheck(ProxyPoolOperationRequest{Confirmed: true})
	if err != nil {
		t.Fatalf("StartProxyPoolPrecheck: %v", err)
	}
	done := waitNetworkJob(t, app, job.ID, StatusSucceeded)
	result, ok := done.Result.(ProxyPoolPrecheckResult)
	if !ok {
		t.Fatalf("result 类型=%T", done.Result)
	}
	if result.UniqueChecked != 3 || result.Passed != 2 || result.Failed != 1 {
		t.Fatalf("预检统计异常: %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("重复代理没有合并检测: %#v", calls)
	}
	for proxy, count := range calls {
		if count != 1 {
			t.Fatalf("%s 检测 %d 次，期望 1", proxy, count)
		}
	}
}

func TestProxyPoolPrecheckJapanGateUsesFakeCountry(t *testing.T) {
	st := settings.Defaults()
	st.PaymentDynamicProxy = "us.example:9001"
	st.RequireJapanExtractProxy = true
	st.LinkProxyRegion = "不限"
	st.LinkProxyPrecheckLimit = 10
	st.LinkProxyPrecheckConcurrency = 1
	candidates, err := proxyPrecheckCandidates(st)
	if err != nil {
		t.Fatal(err)
	}

	old := runManualProxyHealthCheck
	t.Cleanup(func() { runManualProxyHealthCheck = old })
	runManualProxyHealthCheck = func(
		context.Context,
		string,
		string,
		bool,
		func(string),
	) models.ProxyHealthResult {
		return models.ProxyHealthResult{
			Success: true, IP: "198.51.100.2", Country: "US",
			Timezone: "America/New_York", ChatGPTStatus: 200, StripeStatus: 200,
		}
	}
	result := runProxyPoolPrecheck(context.Background(), st, candidates, nil)
	if result.Failed != 1 || len(result.Failures) != 1 ||
		!strings.Contains(result.Failures[0].Detail, "不是日本") {
		t.Fatalf("日本出口门禁未生效: %#v", result)
	}
}

func TestProxyPoolCleanupRemovesFakeFailureEverywhereAndKeeps403(t *testing.T) {
	app := newNetworkOpsTestApp(t, nil, map[string]any{})
	_, err := app.PatchSettings(map[string]any{
		"dynamic_proxies":        "good.example:7001\nbad.example:7002\nshared.example:7003",
		"payment_dynamic_proxy":  "bad.example:7002\nkeep403.example:7004",
		"followup_dynamic_proxy": "bad.example:7002\nshared.example:7003",
		"approve_dynamic_proxy":  "shared.example:7003\ngood.example:7001",
	})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}

	oldCheck := runManualProxyHealthCheck
	oldDelay := proxyCleanupRetryDelay
	t.Cleanup(func() {
		runManualProxyHealthCheck = oldCheck
		proxyCleanupRetryDelay = oldDelay
	})
	proxyCleanupRetryDelay = 0
	var (
		mu    sync.Mutex
		calls = map[string]int{}
	)
	runManualProxyHealthCheck = func(
		_ context.Context,
		_ string,
		proxy string,
		full bool,
		_ func(string),
	) models.ProxyHealthResult {
		if full {
			t.Error("清理必须使用不把 HTTP 403 判死的本地连通检测")
		}
		mu.Lock()
		calls[proxy]++
		mu.Unlock()
		if strings.Contains(proxy, "bad.example") {
			return models.ProxyHealthResult{
				Success: false, FailedStage: "Auth", Error: "connection refused",
			}
		}
		status := 200
		if strings.Contains(proxy, "keep403.example") {
			status = 403
		}
		return models.ProxyHealthResult{
			Success: true, IP: "local", Country: "US",
			Timezone: "UTC", ChatGPTStatus: status,
		}
	}

	job, err := app.StartProxyPoolCleanup(ProxyPoolOperationRequest{Confirmed: true})
	if err != nil {
		t.Fatalf("StartProxyPoolCleanup: %v", err)
	}
	done := waitNetworkJob(t, app, job.ID, StatusSucceeded)
	result, ok := done.Result.(ProxyPoolCleanupResult)
	if !ok {
		t.Fatalf("result 类型=%T", done.Result)
	}
	if result.UniqueChecked != 4 || result.Passed != 3 ||
		result.FailedProxies != 1 || result.RemovedEntries != 3 {
		t.Fatalf("清理统计异常: %#v", result)
	}

	st, err := app.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"注册": st.DynamicProxies, "第一步": st.PaymentDynamicProxy,
		"后续": st.FollowupDynamicProxy, "Approve": st.ApproveDynamicProxy,
	} {
		if strings.Contains(text, "bad.example") {
			t.Errorf("%s 池仍含失效代理: %q", name, text)
		}
	}
	if !strings.Contains(st.PaymentDynamicProxy, "keep403.example") {
		t.Fatalf("HTTP 403 代理被误删: %q", st.PaymentDynamicProxy)
	}

	mu.Lock()
	defer mu.Unlock()
	for proxy, count := range calls {
		want := 1
		if strings.Contains(proxy, "bad.example") {
			want = 2
		}
		if count != want {
			t.Errorf("%s 检测 %d 次，期望 %d", proxy, count, want)
		}
	}
}
