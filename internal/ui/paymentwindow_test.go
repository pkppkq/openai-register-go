package ui

// 本文件全部替换支付浏览器 runner；不会打开 Chromium、访问代理或触发支付。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/paymentwindow"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

func paymentWindowFixture() map[string]any {
	account := models.MailAccount{
		Email:        "payer@example.com",
		Password:     "pw",
		ClientID:     "client",
		RefreshToken: "mail-rt",
		AccountType:  "free",
		Group:        models.AccountDefaultGroup,
	}
	snapshot := localOpsSnapshot([]models.MailAccount{account}, map[string]any{
		account.Email: map[string]any{
			"link_proxy": "http://saved:1000",
		},
	})
	snapshot["results"] = map[string]any{
		account.Email: "https://pay.openai.com/c/pay/cs_test_fixture",
	}
	return snapshot
}

func TestPaymentWindowRequiresTwoExplicitConfirmations(t *testing.T) {
	snapshot := paymentWindowFixture()
	addPaymentExtensionFixture(t, snapshot)
	app, _ := newLocalOpsTestApp(t, snapshot)
	if _, err := app.StartOpenPaymentWindow(OpenPaymentWindowRequest{
		Email: "payer@example.com",
	}); err == nil || !strings.Contains(err.Error(), "明确确认") {
		t.Fatalf("缺少打开确认时 err=%v", err)
	}
	if _, err := app.StartOpenPaymentWindow(OpenPaymentWindowRequest{
		Email:       "payer@example.com",
		Confirmed:   true,
		AutoConfirm: true,
	}); err == nil || !strings.Contains(err.Error(), "单独确认") {
		t.Fatalf("缺少自动扣款确认时 err=%v", err)
	}
	if got := app.ListJobs(); len(got) != 0 {
		t.Fatalf("确认失败不应登记任务: %#v", got)
	}
}

func TestPreparePaymentWindowAtomicallyRotatesMaterialsAndProxy(t *testing.T) {
	snapshot := paymentWindowFixture()
	snapshot["settings"] = map[string]any{
		"paypal_phone_pool": strings.Join([]string{
			"+15550000001----https://sms.invalid/one",
			"+15550000002----https://sms.invalid/two",
		}, "\n"),
		"paypal_phone_pool_index": 0,
		"paypal_card":             "old----2029/1----999----phone----sms----name----address",
		"followup_dynamic_proxy":  "first.invalid:1000\nsecond.invalid:2000",
	}
	addPaymentExtensionFixture(t, snapshot)
	snapshot["payment_cards"] = []any{
		models.CardToMap(models.PaymentCard{
			Card: "4111111111111111", Month: "2", Year: "2031", CVV: "123", Status: "未用",
		}),
		models.CardToMap(models.PaymentCard{
			Card: "5555555555554444", Month: "3", Year: "2032", CVV: "456", Status: "未用",
		}),
	}
	app, stateFile := newLocalOpsTestApp(t, snapshot)

	prepared, err := app.preparePaymentWindow("payer@example.com", PaymentProxyNew)
	if err != nil {
		t.Fatalf("preparePaymentWindow: %v", err)
	}
	if prepared.Phone != "+15550000001" || prepared.SMSURL != "https://sms.invalid/one" {
		t.Fatalf("PP 手机轮换异常: %#v", prepared)
	}
	if prepared.DynamicProxy != "http://first.invalid:1000" {
		t.Fatalf("代理轮换异常: %q", prepared.DynamicProxy)
	}
	if !strings.HasPrefix(prepared.Card, "4111111111111111----2031/2----123----") {
		t.Fatalf("卡资料替换异常: %q", prepared.Card)
	}

	raw := readLocalOpsRawState(t, stateFile)
	settingsMap, _ := raw["settings"].(map[string]any)
	if settingsMap["paypal_phone_pool_index"] != float64(1) {
		t.Fatalf("PP 手机索引未落盘: %#v", settingsMap["paypal_phone_pool_index"])
	}
	if settingsMap["followup_dynamic_proxy"] != "http://second.invalid:2000\nhttp://first.invalid:1000" {
		t.Fatalf("代理池未持久轮转: %#v", settingsMap["followup_dynamic_proxy"])
	}
	cards := localPaymentCardsFromSnapshot(raw)
	if len(cards) != 2 || cards[0].Status != "已用" || cards[1].Status != "未用" {
		t.Fatalf("卡池消费状态异常: %#v", cards)
	}
}

func TestPreparePaymentWindowFailureDoesNotPartiallyConsume(t *testing.T) {
	snapshot := paymentWindowFixture()
	snapshot["settings"] = map[string]any{
		"paypal_phone_pool":       "bad-phone-line",
		"paypal_phone_pool_index": 0,
		"paypal_card":             "old----2029/1----999----phone----sms----name----address",
		"followup_dynamic_proxy":  "first.invalid:1000\nsecond.invalid:2000",
	}
	addPaymentExtensionFixture(t, snapshot)
	snapshot["payment_cards"] = []any{
		models.CardToMap(models.PaymentCard{
			Card: "4111111111111111", Month: "2", Year: "2031", CVV: "123", Status: "未用",
		}),
	}
	app, stateFile := newLocalOpsTestApp(t, snapshot)
	if _, err := app.preparePaymentWindow("payer@example.com", PaymentProxyNew); err == nil {
		t.Fatal("错误 PP 手机行应拒绝")
	}
	raw := readLocalOpsRawState(t, stateFile)
	settingsMap, _ := raw["settings"].(map[string]any)
	if settingsMap["followup_dynamic_proxy"] != "first.invalid:1000\nsecond.invalid:2000" {
		t.Fatalf("失败后代理不应部分轮换: %#v", settingsMap["followup_dynamic_proxy"])
	}
	cards := localPaymentCardsFromSnapshot(raw)
	if len(cards) != 1 || cards[0].Status != "未用" {
		t.Fatalf("失败后卡不应被消费: %#v", cards)
	}
}

func TestPaymentWindowFakeRunnerMarksPlusWithoutNetwork(t *testing.T) {
	snapshot := paymentWindowFixture()
	// 提取代理模式使用保存的假代理会启动本地链；本测试改用新代理模式且不
	// 配置任何上游，使 runner seam 获得直连配置但不会真的访问网络。
	snapshot["session_results"] = map[string]any{}
	addPaymentExtensionFixture(t, snapshot)
	app, _ := newLocalOpsTestApp(t, snapshot)

	oldRunner := runPaymentWindow
	t.Cleanup(func() { runPaymentWindow = oldRunner })
	called := make(chan paymentwindow.Options, 1)
	runPaymentWindow = func(ctx context.Context, opts paymentwindow.Options) (paymentwindow.Result, error) {
		called <- opts
		return paymentwindow.Result{Completed: true, MarkedPlus: true}, nil
	}

	view, err := app.StartOpenPaymentWindow(OpenPaymentWindowRequest{
		Email:             "payer@example.com",
		ProxyMode:         PaymentProxyNew,
		Confirmed:         true,
		AutoConfirm:       true,
		ConfirmAutoCharge: true,
	})
	if err != nil {
		t.Fatalf("StartOpenPaymentWindow: %v", err)
	}
	select {
	case opts := <-called:
		if !opts.AutoConfirm || opts.Link != "https://pay.openai.com/c/pay/cs_test_fixture" {
			t.Fatalf("runner 参数异常: %#v", opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake runner 未被调用")
	}
	waitNetworkJob(t, app, view.ID, StatusSucceeded)

	page, err := app.ListAccounts(AccountFilter{})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("ListAccounts: %#v, %v", page, err)
	}
	if page.Rows[0].AccountType != "plus" || page.Rows[0].Status != "Plus" {
		t.Fatalf("自动确认完成后未标记 Plus: %#v", page.Rows[0])
	}
}

func addPaymentExtensionFixture(t *testing.T, snapshot map[string]any) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":3,"name":"fixture","version":"1"}`), 0o600); err != nil {
		t.Fatalf("创建测试扩展 manifest: %v", err)
	}
	settingsMap, _ := snapshot["settings"].(map[string]any)
	if settingsMap == nil {
		settingsMap = map[string]any{}
		snapshot["settings"] = settingsMap
	}
	settingsMap["payment_extension_dir"] = dir
}

func TestSwitchPaymentWindowProxyRotatesPoolWithoutBrowser(t *testing.T) {
	snapshot := paymentWindowFixture()
	settingsMap := snapshot["settings"].(map[string]any)
	first := "http://user:pass@10.20.0.1:9001"
	second := "http://user:pass@10.20.0.2:9002"
	settingsMap["proxy_route_mode"] = settings.ProxyRouteModeDefault
	settingsMap["payment_dynamic_proxy"] = first + "\n" + second
	settingsMap["local_proxy"] = "http://127.0.0.1:1"
	app, _ := newLocalOpsTestApp(t, snapshot)

	st, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	session, err := app.openProxySession(st, "http://127.0.0.1:2", func(string) {})
	if err != nil {
		t.Fatalf("openProxySession: %v", err)
	}
	defer session.Close()
	active := app.registerActivePaymentWindow("payer@example.com", session)
	defer app.unregisterActivePaymentWindow("payer@example.com", active)

	result, err := app.SwitchPaymentWindowProxy("PAYER@example.com")
	if err != nil {
		t.Fatalf("SwitchPaymentWindowProxy: %v", err)
	}
	if result.Email != "payer@example.com" || result.Proxy != proxypool.MaskProxyURL(first) {
		t.Fatalf("切换结果异常: %#v", result)
	}
	if session.Config.DynamicProxy != proxypool.NormalizeProxyURL(first) {
		t.Fatalf("活动代理未更新: %q", session.Config.DynamicProxy)
	}
	after, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings after switch: %v", err)
	}
	wantPool := proxypool.NormalizeProxyURL(second) + "\n" + proxypool.NormalizeProxyURL(first)
	if after.PaymentDynamicProxy != wantPool {
		t.Fatalf("支付代理池未轮转: got=%q want=%q", after.PaymentDynamicProxy, wantPool)
	}

	app.unregisterActivePaymentWindow("payer@example.com", active)
	if _, err := app.SwitchPaymentWindowProxy("payer@example.com"); err == nil {
		t.Fatal("窗口关闭后仍允许切换代理")
	}
}
