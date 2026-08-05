package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestPhonePoolListImportResetAndConfirmedClear(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["phones"] = []any{
		models.PhoneToMap(models.PhoneEntry{
			Number:       "+15550001",
			SMSURL:       "https://sms.example/old",
			Status:       "不可用",
			LastCode:     "111111",
			LastError:    "旧错误",
			ReceiveCount: 3,
		}),
		models.PhoneToMap(models.PhoneEntry{
			Number:       "+15550002",
			SMSURL:       "https://sms.example/2",
			Status:       "冻结",
			LastCode:     "222222",
			LastError:    "次数已满",
			ReceiveCount: 8,
		}),
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	listed, err := app.ListPhones()
	if err != nil {
		t.Fatalf("ListPhones: %v", err)
	}
	if listed.Total != 2 || len(listed.Phones) != 2 || listed.Phones[0].SMSURL != "https://sms.example/old" {
		t.Fatalf("ListPhones = %#v", listed)
	}

	imported, err := app.ImportPhones(strings.Join([]string{
		"+15550001----https://sms.example/new",
		"不是手机号",
		"+15550003https://sms.example/3",
	}, "\r\n"))
	if err != nil {
		t.Fatalf("ImportPhones: %v", err)
	}
	if imported.Imported != 2 || imported.Updated != 1 || imported.Total != 3 || len(imported.Errors) != 1 {
		t.Fatalf("ImportPhones = %#v", imported)
	}
	if !strings.Contains(imported.Errors[0], "第 2 行") {
		t.Fatalf("导入错误未保留行号: %#v", imported.Errors)
	}
	byNumber := phoneViewsByNumber(imported.Phones)
	updated := byNumber["+15550001"]
	if updated.SMSURL != "https://sms.example/new" ||
		updated.Status != "可用" ||
		updated.LastError != "" ||
		updated.LastCode != "111111" ||
		updated.ReceiveCount != 3 {
		t.Fatalf("不可用号码的合并语义错误: %#v", updated)
	}
	if existing := byNumber["+15550002"]; existing.Status != "冻结" || existing.LastError != "次数已满" {
		t.Fatalf("未导入号码被意外修改: %#v", existing)
	}
	if created := byNumber["+15550003"]; created.Status != "可用" || created.SMSURL != "https://sms.example/3" {
		t.Fatalf("新号码错误: %#v", created)
	}

	reset, err := app.ResetPhones()
	if err != nil {
		t.Fatalf("ResetPhones: %v", err)
	}
	if reset.Total != 3 || reset.Updated != 2 {
		t.Fatalf("ResetPhones = %#v", reset)
	}
	for _, phone := range reset.Phones {
		if phone.Status != "可用" || phone.LastError != "" || phone.ReceiveCount != 0 {
			t.Fatalf("号码未重置: %#v", phone)
		}
	}
	if phoneViewsByNumber(reset.Phones)["+15550001"].LastCode != "111111" {
		t.Fatal("重置不应删除最近验证码")
	}

	if _, err := app.ClearPhones(false); err == nil || !strings.Contains(err.Error(), "明确确认") {
		t.Fatalf("未确认清空错误 = %v", err)
	}
	stillThere, err := app.ListPhones()
	if err != nil || stillThere.Total != 3 {
		t.Fatalf("未确认却改写了手机号池: %#v, %v", stillThere, err)
	}

	cleared, err := app.ClearPhones(true)
	if err != nil {
		t.Fatalf("ClearPhones(true): %v", err)
	}
	if cleared.Updated != 3 || cleared.Total != 0 || len(cleared.Phones) != 0 {
		t.Fatalf("ClearPhones = %#v", cleared)
	}
	emptyAgain, err := app.ClearPhones(true)
	if err != nil || emptyAgain.Updated != 0 || emptyAgain.Message != "" {
		t.Fatalf("空池重复清空应无副作用: %#v, %v", emptyAgain, err)
	}
}

func TestStartManualPhoneCodeUsesOnlySavedURLAndDoesNotConsumeCount(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("<html>OpenAI 验证代码：654321</html>"))
	}))
	defer server.Close()

	snapshot := localOpsSnapshot(nil, nil)
	snapshot["phones"] = []any{models.PhoneToMap(models.PhoneEntry{
		Number:       "+15550100",
		SMSURL:       server.URL + "/saved-sms",
		Status:       "可用",
		LastCode:     "123123",
		LastError:    "旧错误",
		ReceiveCount: 4,
	})}
	app, _ := newLocalOpsTestApp(t, snapshot)

	view, err := app.StartManualPhoneCode("+15550100")
	if err != nil {
		t.Fatalf("StartManualPhoneCode: %v", err)
	}
	if view.Kind != JobManualPhoneCode || view.Status != StatusRunning || view.Email != "+15550100" {
		t.Fatalf("初始 JobView = %#v", view)
	}
	finished := waitForPhoneJob(t, app, view.ID)
	if finished.Status != StatusSucceeded {
		t.Fatalf("手动取码任务未成功: %#v", finished)
	}
	result, err := app.GetNetworkJobResult(view.ID)
	if err != nil {
		t.Fatalf("GetNetworkJobResult: %v", err)
	}
	codeResult, ok := result.Result.(ManualPhoneCodeResult)
	if !ok || codeResult.Number != "+15550100" || codeResult.Code != "654321" {
		t.Fatalf("手动取码结果 = %#v", result.Result)
	}
	if requests.Load() != 1 {
		t.Fatalf("短信回环服务请求数 = %d, want 1", requests.Load())
	}
	phones, err := app.ListPhones()
	if err != nil {
		t.Fatalf("ListPhones: %v", err)
	}
	phone := phones.Phones[0]
	if phone.LastCode != "654321" || phone.LastError != "" {
		t.Fatalf("最近取码结果未保存: %#v", phone)
	}
	if phone.ReceiveCount != 4 || phone.Status != "可用" {
		t.Fatalf("手动取码不应消费接码次数或改变状态: %#v", phone)
	}
}

func TestManualPhoneCodeCancellationIsImmediateAndDoesNotWriteError(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
	}))
	defer server.Close()

	snapshot := localOpsSnapshot(nil, nil)
	snapshot["phones"] = []any{models.PhoneToMap(models.PhoneEntry{
		Number:       "+15550200",
		SMSURL:       server.URL,
		Status:       "冻结",
		LastCode:     "777777",
		LastError:    "取消前错误",
		ReceiveCount: 9,
	})}
	app, _ := newLocalOpsTestApp(t, snapshot)

	view, err := app.StartManualPhoneCode("+15550200")
	if err != nil {
		t.Fatalf("StartManualPhoneCode: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("手动取码未请求回环服务")
	}
	if _, err := app.ClearPhones(true); err == nil || !strings.Contains(err.Error(), "任务正在运行") {
		t.Fatalf("运行中清空错误 = %v", err)
	}
	if err := app.CancelJob(view.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	finished := waitForPhoneJob(t, app, view.ID)
	if finished.Status != StatusCancelled {
		t.Fatalf("取消后的 JobView = %#v", finished)
	}
	phones, err := app.ListPhones()
	if err != nil {
		t.Fatalf("ListPhones: %v", err)
	}
	phone := phones.Phones[0]
	if phone.LastCode != "777777" ||
		phone.LastError != "取消前错误" ||
		phone.ReceiveCount != 9 ||
		phone.Status != "冻结" {
		t.Fatalf("取消任务不应改写手机号: %#v", phone)
	}
}

func TestManualPhoneCodeFailureUpdatesOnlyLastError(t *testing.T) {
	original := fetchManualPhoneCode
	fetchManualPhoneCode = func(context.Context, string, string) (string, error) {
		return "", errors.New("回环短信页读取失败")
	}
	defer func() { fetchManualPhoneCode = original }()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["phones"] = []any{models.PhoneToMap(models.PhoneEntry{
		Number:       "+15550300",
		SMSURL:       server.URL,
		Status:       "可用",
		LastCode:     "888888",
		LastError:    "",
		ReceiveCount: 2,
	})}
	app, _ := newLocalOpsTestApp(t, snapshot)

	view, err := app.StartManualPhoneCode("+15550300")
	if err != nil {
		t.Fatalf("StartManualPhoneCode: %v", err)
	}
	finished := waitForPhoneJob(t, app, view.ID)
	if finished.Status != StatusFailed || !strings.Contains(finished.Error, "回环短信页读取失败") {
		t.Fatalf("失败 JobView = %#v", finished)
	}
	phones, _ := app.ListPhones()
	phone := phones.Phones[0]
	if phone.LastError != "回环短信页读取失败" ||
		phone.LastCode != "888888" ||
		phone.ReceiveCount != 2 ||
		phone.Status != "可用" {
		t.Fatalf("失败任务改写字段错误: %#v", phone)
	}
}

func TestStartManualPhoneCodeRejectsUnsavedAndNonHTTPURLs(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["phones"] = []any{models.PhoneToMap(models.PhoneEntry{
		Number: "+15550400",
		SMSURL: "smsbower://activation/paid-number",
		Status: "可用",
	})}
	app, _ := newLocalOpsTestApp(t, snapshot)

	if _, err := app.StartManualPhoneCode("+19999999"); err == nil || !strings.Contains(err.Error(), "手机号不存在") {
		t.Fatalf("未保存号码错误 = %v", err)
	}
	if _, err := app.StartManualPhoneCode("+15550400"); err == nil || !strings.Contains(err.Error(), "http://") {
		t.Fatalf("非 HTTP 短信链接错误 = %v", err)
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Fatalf("校验失败前不应创建任务: %#v", jobs)
	}
}

func waitForPhoneJob(t *testing.T, app *App, id string) JobView {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if view, ok := app.jobView(id); ok && view.Status != StatusRunning {
			return view
		}
		select {
		case <-deadline.C:
			view, _ := app.jobView(id)
			t.Fatalf("等待任务结束超时: %#v", view)
		case <-ticker.C:
		}
	}
}

func phoneViewsByNumber(phones []PhoneView) map[string]PhoneView {
	out := make(map[string]PhoneView, len(phones))
	for _, phone := range phones {
		out[phone.Number] = phone
	}
	return out
}
