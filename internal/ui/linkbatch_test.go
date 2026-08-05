package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

func linkBatchFixture() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"accounts": []any{
			accountMap("a@example.com", "free", "", "未分组"),
			accountMap("b@example.com", "plus", "", "未分组"),
			accountMap("missing@example.com", "free", "", "未分组"),
		},
		"session_results": map[string]any{
			"a@example.com": map[string]any{"access_token": "token-a"},
			"b@example.com": map[string]any{"access_token": "token-b"},
			"missing@example.com": map[string]any{
				"access_token": "",
			},
		},
		"settings": map[string]any{
			"payment_mode":           "无卡长链接 US/USD",
			"target_amount":          "",
			"local_proxy":            "",
			"payment_dynamic_proxy":  "http://create-a:1000\nhttp://create-b:1000",
			"followup_dynamic_proxy": "http://follow-a:1000\nhttp://follow-b:1000",
			"approve_dynamic_proxy":  "http://approve-a:1000\nhttp://approve-b:1000",
			"link_race_concurrency":  1,
			"link_attempt_limit":     2,
			// 本测试要验证两个账号各自的重试与落盘；暂停语义由
			// linksuccess_test.go 独立覆盖。
			"pause_others_on_link_success": false,
		},
	}
}

func TestResolveLinkBatchSelectionUsesSavedAccessTokens(t *testing.T) {
	snapshot := linkBatchFixture()
	got, skipped, err := resolveLinkBatchSelection(snapshot, []string{
		"B@example.com",
		"missing@example.com",
		"a@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].AccessToken != "token-b" || got[1].AccessToken != "token-a" {
		t.Fatalf("解析出的批量账号不正确: %#v", got)
	}
	if len(skipped) != 1 || skipped[0] != "missing@example.com" {
		t.Fatalf("跳过列表不正确: %v", skipped)
	}

	if _, _, err := resolveLinkBatchSelection(snapshot, []string{"a@example.com", "A@example.com"}); err == nil {
		t.Fatal("重复邮箱没有被拒绝")
	}
}

func TestStartBatchGenerateLinksRetriesAndPersistsResults(t *testing.T) {
	app := newTempApp(t, linkBatchFixture())

	original := runLinkAttempt
	defer func() { runLinkAttempt = original }()

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
	)
	runLinkAttempt = func(
		_ context.Context,
		account models.MailAccount,
		_ string,
		_ settings.Settings,
		triple [3]string,
		_ func(string),
	) (*worker.PayLinkResult, error) {
		mu.Lock()
		attempts[account.Email]++
		n := attempts[account.Email]
		mu.Unlock()

		if triple[0] == "" || triple[1] == "" || triple[2] == "" {
			t.Errorf("收到不完整三段代理: %v", triple)
		}
		if account.Email == "a@example.com" && n == 1 {
			return nil, errors.New("temporary transport failure")
		}
		return &worker.PayLinkResult{
			URL:             "https://example.test/pay/" + account.Email,
			CheckoutURL:     "https://example.test/pay/" + account.Email,
			AccessToken:     "saved-" + account.Email,
			PaymentLinkType: "paypal_approve",
		}, nil
	}

	summary, err := app.StartBatchGenerateLinks(StartLinkBatchRequest{
		Emails:    []string{"a@example.com", "missing@example.com", "b@example.com"},
		Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped) != 1 || summary.Skipped[0] != "missing@example.com" {
		t.Fatalf("跳过列表=%v", summary.Skipped)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		view, ok := app.jobView(summary.Job.ID)
		if !ok {
			t.Fatalf("父任务丢失: %s", summary.Job.ID)
		}
		if view.Status != StatusRunning {
			if view.Status != StatusSucceeded {
				t.Fatalf("父任务状态=%s error=%s", view.Status, view.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待批量提链任务超时")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	if attempts["a@example.com"] != 2 || attempts["b@example.com"] != 1 {
		t.Fatalf("调用次数不正确: %v", attempts)
	}
	mu.Unlock()

	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	results := resultsFromSnapshot(snapshot)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if got := settings.PyStr(results[email]); !strings.Contains(got, email) {
			t.Errorf("%s 的链接未保存: %q", email, got)
		}
	}
	counts := subMap(snapshot, "link_attempt_counts")
	if settings.PyStr(counts["a@example.com"]) != "2" ||
		settings.PyStr(counts["b@example.com"]) != "1" {
		t.Fatalf("撞链次数未保存: %v", counts)
	}
}

func TestStartBatchGenerateLinksRejectsInvalidProviderBeforeStarting(t *testing.T) {
	snapshot := linkBatchFixture()
	snapshot["settings"] = map[string]any{
		"payment_mode": "无卡长链接 US/USD",
		"local_proxy":  "",
		"provider_proxy_configs": map[string]any{
			"create": map[string]any{
				"enabled": true, "username": "u", "password": "p",
				"endpoint": "", "duration": 5, "regions": "JP",
			},
		},
	}
	app := newTempApp(t, snapshot)
	_, err := app.StartBatchGenerateLinks(StartLinkBatchRequest{
		Emails: []string{"a@example.com"}, Confirmed: true,
	})
	if err == nil {
		t.Fatal("无效 Provider 配置应在创建任务前被拒绝")
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Fatalf("被拒绝的请求仍创建了任务: %v", jobs)
	}
}

func TestLinkTripleSourceRecyclesAtTail(t *testing.T) {
	source := newLinkTripleSource([][3]string{{"a", "b", "c"}, {"d", "e", "f"}})
	first, ok := source.Take(context.Background())
	if !ok {
		t.Fatal("第一次 Take 失败")
	}
	source.Recycle(first)
	second, _ := source.Take(context.Background())
	third, _ := source.Take(context.Background())
	if second[0] != "d" || third[0] != "a" {
		t.Fatalf("回队顺序错误: second=%v third=%v", second, third)
	}
}
