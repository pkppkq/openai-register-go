package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/phoneprovider"
)

func phonePoolTestSnapshot(accounts []any, phones []any, receiveLimit int) map[string]any {
	return map[string]any{
		"schema_version": 2,
		"accounts":       accounts,
		"phones":         phones,
		"settings": map[string]any{
			"smsbower_enabled":        false,
			"phone_max_receive_count": receiveLimit,
		},
		"session_results": map[string]any{},
	}
}

func phonePoolTestEntry(number, smsURL string) map[string]any {
	return map[string]any{
		"number":        number,
		"sms_url":       smsURL,
		"status":        phoneprovider.StatusAvailable,
		"receive_count": 0,
		"last_code":     "",
		"last_error":    "",
	}
}

// TestSharedPhonePoolConcurrentReservation 防止回归为“每任务一个 MemoryPool”。
// 两个批量子任务同时取一个手工号码时，只允许一个任务成功，且占用状态与
// auth_phone 必须在 Next 返回前共同落盘。
func TestSharedPhonePoolConcurrentReservation(t *testing.T) {
	app := newTempApp(t, phonePoolTestSnapshot(
		[]any{
			accountMap("one@example.com", "free", "", "未分组"),
			accountMap("two@example.com", "free", "", "未分组"),
		},
		[]any{phonePoolTestEntry("+15550100001", "https://sms.test/1")},
		3,
	))
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	first := app.phoneProvider(context.Background(), snapshot, func(string) {})
	second := app.phoneProvider(context.Background(), snapshot, func(string) {})
	defer first.Close()
	defer second.Close()

	start := make(chan struct{})
	type outcome struct {
		email string
		phone map[string]string
		err   error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for index, provider := range []*phoneprovider.SMSBowerProvider{first, second} {
		email := []string{"one@example.com", "two@example.com"}[index]
		workers.Add(1)
		go func(email string, provider *phoneprovider.SMSBowerProvider) {
			defer workers.Done()
			<-start
			phone, nextErr := provider.Next(email, map[string]string{"country": "US"})
			results <- outcome{email: email, phone: phone, err: nextErr}
		}(email, provider)
	}
	close(start)
	workers.Wait()
	close(results)

	winner := ""
	successes := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("%s Next: %v", result.email, result.err)
		}
		if len(result.phone) == 0 {
			continue
		}
		successes++
		winner = result.email
		if result.phone["number"] != "+15550100001" {
			t.Fatalf("取到意外号码: %#v", result.phone)
		}
	}
	if successes != 1 {
		t.Fatalf("并发取号成功数=%d，期望严格为 1", successes)
	}

	persisted, err := app.snapshot()
	if err != nil {
		t.Fatalf("读取落盘状态: %v", err)
	}
	phones := uiPhonesFromSnapshot(persisted)
	if len(phones) != 1 || phones[0].Status != phoneprovider.StatusInUse {
		t.Fatalf("占用状态未落盘: %+v", phones)
	}
	bound := 0
	for _, account := range accountsFromSnapshot(persisted) {
		if account.AuthPhoneNumber == "" {
			continue
		}
		bound++
		if account.Email != winner ||
			account.AuthPhoneNumber != "+15550100001" ||
			account.AuthPhoneSMSURL != "https://sms.test/1" {
			t.Fatalf("授权手机号绑定错误: winner=%s account=%+v", winner, account)
		}
	}
	if bound != 1 {
		t.Fatalf("落盘授权手机号账号数=%d，期望 1", bound)
	}
}

// TestSharedPhonePoolPersistsCodeAndReadsLiveReceiveLimit 使用纯内存 HTTP fake
// 驱动完整手工号码 Next/Code 路径，并证明同一 Provider 会读取保存后的新接码
// 上限；全程不会调用 SMSBower、OpenAI 或真实短信服务。
func TestSharedPhonePoolPersistsCodeAndReadsLiveReceiveLimit(t *testing.T) {
	app := newTempApp(t, phonePoolTestSnapshot(
		[]any{
			accountMap("first@example.com", "free", "", "未分组"),
			accountMap("second@example.com", "free", "", "未分组"),
		},
		[]any{phonePoolTestEntry("+15550100002", "https://sms.test/2")},
		2,
	))
	if err := app.refreshPhonePool(); err != nil {
		t.Fatalf("refreshPhonePool: %v", err)
	}
	provider := phoneprovider.New(phoneprovider.Config{
		Settings: phoneprovider.SnapshotSettings{
			Snapshot: func() map[string]any {
				snapshot, _ := app.snapshot()
				return snapshot
			},
		},
		Pool:    app.sharedPhonePool(),
		Context: context.Background(),
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			return "OpenAI verification code: 246810", nil
		},
		Sleep: func(time.Duration) {},
	})

	phone, err := provider.Next("first@example.com", map[string]string{"country": "US"})
	if err != nil || len(phone) == 0 {
		t.Fatalf("Next = %#v, %v", phone, err)
	}
	code, err := provider.Code("first@example.com", phone)
	if err != nil || code != "246810" {
		t.Fatalf("Code = %q, %v", code, err)
	}

	afterCode, err := app.snapshot()
	if err != nil {
		t.Fatalf("读取验证码落盘状态: %v", err)
	}
	phones := uiPhonesFromSnapshot(afterCode)
	if len(phones) != 1 ||
		phones[0].ReceiveCount != 1 ||
		phones[0].Status != phoneprovider.StatusAvailable ||
		phones[0].LastCode != "246810" ||
		phones[0].LastError != "" {
		t.Fatalf("验证码/次数/状态未完整落盘: %+v", phones)
	}

	current, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	current.PhoneMaxReceiveCount = 1
	if err := app.SaveSettings(current); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// 仍使用上面已经创建的 Provider；新的上限必须从实时 snapshot 生效。
	next, err := provider.Next("second@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("第二次 Next: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("达到新上限的号码仍被租出: %#v", next)
	}

	afterLimit, err := app.snapshot()
	if err != nil {
		t.Fatalf("读取冻结状态: %v", err)
	}
	phones = uiPhonesFromSnapshot(afterLimit)
	if len(phones) != 1 || phones[0].Status != phoneprovider.StatusFrozen {
		t.Fatalf("新接码上限未刷新或冻结未落盘: %+v", phones)
	}

	// 新 App/新任务从磁盘重建共享池，不能丢掉上次的验证码、次数和绑定。
	restarted := New()
	if err := restarted.refreshPhonePool(); err != nil {
		t.Fatalf("重启后 refreshPhonePool: %v", err)
	}
	lookup := restarted.sharedPhonePool().AccountAuthPhone("first@example.com")
	if !lookup.Found ||
		lookup.Number != "+15550100002" ||
		lookup.SMSURL != "https://sms.test/2" ||
		!lookup.SavedOK ||
		lookup.Saved.ReceiveCount != 1 ||
		lookup.Saved.LastCode != "246810" ||
		lookup.Saved.Status != phoneprovider.StatusFrozen {
		t.Fatalf("重启后共享池未恢复持久化状态: %+v", lookup)
	}
}
