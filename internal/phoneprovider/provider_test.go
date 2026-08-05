package phoneprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
)

// ---------------------------------------------------------------------------
// fakes — no test in this file may touch the network: a real GetNumber rents a
// billable number.
// ---------------------------------------------------------------------------

type numberCall struct{ service, country, maxPrice string }

type numberResult struct {
	number smsbower.Number
	err    error
}

type statusCall struct {
	activationID string
	status       int
}

type fakeClient struct {
	mu sync.Mutex

	tiers    []smsbower.PriceTier
	tiersErr error
	quote    smsbower.PriceQuote
	quoteErr error

	// numbers is consumed in order, one entry per GetNumber call.
	numbers     []numberResult
	numberCalls []numberCall

	statusResult string
	statusErr    error
	statusCalls  []statusCall

	code     string
	codeErr  error
	waitArgs []string

	seq int
}

func (f *fakeClient) GetPriceTiers(_ context.Context, _, _ string) ([]smsbower.PriceTier, error) {
	if f.tiersErr != nil {
		return nil, f.tiersErr
	}
	return f.tiers, nil
}

func (f *fakeClient) GetPriceQuote(_ context.Context, _, _ string) (smsbower.PriceQuote, error) {
	if f.quoteErr != nil {
		return smsbower.PriceQuote{}, f.quoteErr
	}
	return f.quote, nil
}

func (f *fakeClient) GetNumber(_ context.Context, service, country, maxPrice string) (smsbower.Number, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.numberCalls = append(f.numberCalls, numberCall{service, country, maxPrice})
	if f.seq < len(f.numbers) {
		out := f.numbers[f.seq]
		f.seq++
		return out.number, out.err
	}
	return smsbower.Number{}, &smsbower.Error{Msg: "fake: no scripted GetNumber result"}
}

func (f *fakeClient) SetStatus(_ context.Context, activationID string, status int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls = append(f.statusCalls, statusCall{activationID, status})
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if f.statusResult == "" {
		return "ACCESS_READY", nil
	}
	return f.statusResult, nil
}

func (f *fakeClient) WaitForCode(_ context.Context, activationID string, timeout, interval int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitArgs = append(f.waitArgs, fmt.Sprintf("%s/%d/%d", activationID, timeout, interval))
	return f.code, f.codeErr
}

func (f *fakeClient) statuses() []statusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statusCall(nil), f.statusCalls...)
}

func (f *fakeClient) prices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.numberCalls))
	for _, call := range f.numberCalls {
		out = append(out, call.maxPrice)
	}
	return out
}

type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) log(email, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, email+"|"+msg)
}

func (r *recorder) contains(needle string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range r.lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

type harness struct {
	provider *Provider
	client   *fakeClient
	pool     *MemoryPool
	log      *recorder
	accounts []*models.MailAccount
	phones   []*models.PhoneEntry
}

func newHarness(t *testing.T, raw Raw, client *fakeClient, accounts []*models.MailAccount, phones []*models.PhoneEntry) *harness {
	t.Helper()
	log := &recorder{}
	pool := NewMemoryPool(accounts, phones)
	if client == nil {
		// A fake with no scripted numbers: any rental attempt fails loudly instead
		// of reaching the real API.
		client = &fakeClient{}
	}
	provider := New(Config{
		Settings: raw,
		Pool:     pool,
		Log:      log.log,
		NewClient: func(apiKey string) (SMSClient, error) {
			return client, nil
		},
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			t.Fatalf("unexpected HTTP call in this test")
			return "", nil
		},
		Sleep: func(time.Duration) {},
	})
	return &harness{provider: provider, client: client, pool: pool, log: log, accounts: accounts, phones: phones}
}

func enabledRaw() Raw {
	return Raw{Enabled: true, APIKey: "key", Service: "dr", Country: "33", MaxPrice: "0.07"}
}

// ---------------------------------------------------------------------------
// next
// ---------------------------------------------------------------------------

func TestNextPrefersImportedAuthPhone(t *testing.T) {
	account := &models.MailAccount{Email: "User@Example.com", AuthPhoneNumber: "+15550001111", AuthPhoneSMSURL: "http://sms/1"}
	h := newHarness(t, enabledRaw(), nil, []*models.MailAccount{account}, nil)

	// The account match is case-insensitive on both sides (app.py:16540).
	got, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	want := map[string]string{"number": "+15550001111", "sms_url": "http://sms/1", "account_bound": "true"}
	assertMap(t, got, want)
	if !accountBound(got) {
		t.Fatalf("account_bound must be truthy")
	}
	if len(h.client.statuses()) != 0 || len(h.client.prices()) != 0 {
		t.Fatalf("SMSBower must not be touched when an imported number is reused")
	}
}

func TestNextSkipsImportedAuthPhone(t *testing.T) {
	tests := []struct {
		name      string
		number    string
		saved     *models.PhoneEntry
		requested string
	}{
		{name: "non US number when US requested", number: "+819000000", requested: "US"},
		{
			name:      "saved entry marked 不可用",
			number:    "+15550001111",
			saved:     &models.PhoneEntry{Number: "+15550001111", Status: StatusUnusable, LastError: "手机号已使用: x"},
			requested: "US",
		},
		{
			name:      "saved entry marked 冻结",
			number:    "+15550001111",
			saved:     &models.PhoneEntry{Number: "+15550001111", Status: StatusFrozen},
			requested: "US",
		},
		{
			name:      "saved entry marked 使用中",
			number:    "+15550001111",
			saved:     &models.PhoneEntry{Number: "+15550001111", Status: StatusInUse},
			requested: "US",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			account := &models.MailAccount{Email: "a@b.c", AuthPhoneNumber: tc.number, AuthPhoneSMSURL: "http://sms/1"}
			var phones []*models.PhoneEntry
			if tc.saved != nil {
				phones = append(phones, tc.saved)
			}
			// SMSBower disabled so the skip falls through to the (empty) manual pool.
			raw := enabledRaw()
			raw.Enabled = false
			h := newHarness(t, raw, nil, []*models.MailAccount{account}, phones)

			got, err := h.provider.Next("a@b.c", map[string]string{"country": tc.requested})
			if err != nil {
				t.Fatalf("Next error: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("expected the imported number to be skipped, got %v", got)
			}
		})
	}
}

func TestNextRentsFromCheapestEligibleTier(t *testing.T) {
	client := &fakeClient{
		tiers: []smsbower.PriceTier{{Cost: 0.03, Count: 2}, {Cost: 0.05, Count: 7}, {Cost: 0.2, Count: 99}},
		numbers: []numberResult{
			{err: &smsbower.Error{Msg: "当前地区没有可用号码 (NO_NUMBERS)"}},
			{number: smsbower.Number{ActivationID: "A1", Number: "+15551230000"}},
		},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	got, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}

	assertMap(t, got, map[string]string{
		"number":        "+15551230000",
		"sms_url":       "smsbower://A1",
		"provider":      "smsbower",
		"activation_id": "A1",
		"price":         "0.05",
		"stock":         "9", // 2 + 7 eligible, the 0.2 tier is over the 0.07 cap
	})

	// The 0.2 tier must never be attempted, and the price strings must be the
	// Python %g rendering.
	if want := []string{"0.03", "0.05"}; !equalStrings(client.prices(), want) {
		t.Fatalf("maxPrice sequence = %v, want %v", client.prices(), want)
	}
	if h.client.numberCalls[0].country != "187" {
		t.Fatalf("country = %q, want 187 (US mapped through SMSBowerCountryIDs)", h.client.numberCalls[0].country)
	}
	if h.provider.AttemptCount("USER@example.com ") != 1 {
		t.Fatalf("attempt counter = %d, want 1 (key is trimmed + lower-cased)", h.provider.AttemptCount("user@example.com"))
	}
	if !h.log.contains("本账号第 1 次") || !h.log.contains("限价≤0.07 可用档=0.03(2), 0.05(7)") {
		t.Fatalf("missing success log, got:\n%s", h.log.dump())
	}
}

func TestNextFallsBackToConfiguredCapWhenTiersFail(t *testing.T) {
	client := &fakeClient{
		tiersErr: &smsbower.Error{Msg: "SMSBower 分档价格响应无法识别: x"},
		numbers:  []numberResult{{number: smsbower.Number{ActivationID: "A9", Number: "+15559990000"}}},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	got, err := h.provider.Next("a@b.c", nil)
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if got["price"] != "0.07" {
		t.Fatalf("price = %q, want the configured cap 0.07", got["price"])
	}
	if want := []string{"0.07"}; !equalStrings(client.prices(), want) {
		t.Fatalf("maxPrice sequence = %v, want %v", client.prices(), want)
	}
	// No country requested -> the configured country id, not the US mapping.
	if client.numberCalls[0].country != "33" {
		t.Fatalf("country = %q, want 33", client.numberCalls[0].country)
	}
	if !h.log.contains("价格查询失败，继续取号") {
		t.Fatalf("price failure should be logged, got:\n%s", h.log.dump())
	}
}

func TestNextRetriesSameTierOnceOnTransientError(t *testing.T) {
	client := &fakeClient{
		tiers: []smsbower.PriceTier{{Cost: 0.05, Count: 1}, {Cost: 0.06, Count: 1}},
		numbers: []numberResult{
			{err: &smsbower.Error{Msg: "SMSBower 请求失败: connection reset by peer"}},
			{number: smsbower.Number{ActivationID: "A2", Number: "+15550002222"}},
		},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	got, err := h.provider.Next("a@b.c", nil)
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if got["activation_id"] != "A2" {
		t.Fatalf("activation = %q, want A2", got["activation_id"])
	}
	// Same tier retried, NOT the next one.
	if want := []string{"0.05", "0.05"}; !equalStrings(client.prices(), want) {
		t.Fatalf("maxPrice sequence = %v, want %v", client.prices(), want)
	}
}

func TestNextAbortsOnNonTransientNonEmptyTierError(t *testing.T) {
	client := &fakeClient{
		tiers: []smsbower.PriceTier{{Cost: 0.05, Count: 1}, {Cost: 0.06, Count: 1}},
		numbers: []numberResult{
			{err: &smsbower.Error{Msg: "账户余额不足 (NO_BALANCE)"}},
		},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	got, err := h.provider.Next("a@b.c", nil)
	if err != nil {
		t.Fatalf("Next must never return an error (Python swallowed it): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no phone, got %v", got)
	}
	// NO_BALANCE stops the walk: the second tier must not be tried, or the run
	// would keep hammering a broke account.
	if want := []string{"0.05"}; !equalStrings(client.prices(), want) {
		t.Fatalf("maxPrice sequence = %v, want %v", client.prices(), want)
	}
	if h.provider.AttemptCount("a@b.c") != 0 {
		t.Fatalf("attempt counter must roll back to the pre-increment value")
	}
	if !h.log.contains("SMSBower 自动取号失败，改用手工手机号池") {
		t.Fatalf("missing failure log:\n%s", h.log.dump())
	}
}

func TestNextRollbackKeepsEarlierAttempts(t *testing.T) {
	client := &fakeClient{
		numbers: []numberResult{
			{number: smsbower.Number{ActivationID: "A1", Number: "+15550000001"}},
			{err: &smsbower.Error{Msg: "当前地区没有可用号码 (NO_NUMBERS)"}},
		},
		tiersErr: &smsbower.Error{Msg: "no tiers"},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	if _, err := h.provider.Next("a@b.c", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.provider.AttemptCount("a@b.c"); got != 1 {
		t.Fatalf("counter after one rental = %d, want 1", got)
	}
	if _, err := h.provider.Next("a@b.c", nil); err != nil {
		t.Fatal(err)
	}
	// Python re-assigns the captured `used`, so a failed rental leaves the count
	// of successful rentals untouched (app.py:16500-16501).
	if got := h.provider.AttemptCount("a@b.c"); got != 1 {
		t.Fatalf("counter after a failed rental = %d, want 1", got)
	}
}

func TestNextTransientFailureLogsNetworkWording(t *testing.T) {
	client := &fakeClient{
		tiersErr: &smsbower.Error{Msg: "no tiers"},
		numbers: []numberResult{
			{err: &smsbower.Error{Msg: "SMSBower 请求失败: read timed out"}},
			{err: &smsbower.Error{Msg: "SMSBower 请求失败: read timed out"}},
		},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	if _, err := h.provider.Next("a@b.c", nil); err != nil {
		t.Fatal(err)
	}
	if !h.log.contains("SMSBower 自动取号网络失败，改用手工手机号池") {
		t.Fatalf("transient failures use the network wording:\n%s", h.log.dump())
	}
}

func TestNextDisabledOrInvalidSettingsFallsBackToPool(t *testing.T) {
	tests := []struct {
		name    string
		raw     Raw
		wantLog string
	}{
		{
			name: "disabled provider is silent",
			raw:  Raw{Enabled: false, APIKey: "", Service: "bad service"},
		},
		{
			name:    "invalid settings log and fall back",
			raw:     Raw{Enabled: true, APIKey: "key", Service: "bad service"},
			wantLog: "SMSBower 设置无效，改用手工手机号池: SMSBower 服务代码格式不正确",
		},
		{
			name:    "enabled without a key",
			raw:     Raw{Enabled: true},
			wantLog: "SMSBower 已启用但 API Key 为空，改用手工手机号池",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			phone := &models.PhoneEntry{Number: "+15551110000", SMSURL: "http://sms/pool", Status: StatusAvailable}
			account := &models.MailAccount{Email: "a@b.c"}
			h := newHarness(t, tc.raw, nil, []*models.MailAccount{account}, []*models.PhoneEntry{phone})

			got, err := h.provider.Next("a@b.c", map[string]string{"country": "US"})
			if err != nil {
				t.Fatalf("Next error: %v", err)
			}
			assertMap(t, got, map[string]string{"number": "+15551110000", "sms_url": "http://sms/pool"})
			if phone.Status != StatusInUse {
				t.Fatalf("pool entry status = %q, want 使用中", phone.Status)
			}
			if account.AuthPhoneNumber != "+15551110000" || account.AuthPhoneSMSURL != "http://sms/pool" {
				t.Fatalf("the reserved number must be written back onto the account, got %+v", account)
			}
			if tc.wantLog != "" && !h.log.contains(tc.wantLog) {
				t.Fatalf("missing %q in:\n%s", tc.wantLog, h.log.dump())
			}
			if tc.raw.Enabled == false && h.log.contains("SMSBower") {
				t.Fatalf("a disabled provider must not log anything about SMSBower:\n%s", h.log.dump())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// status transitions — the money-critical part
// ---------------------------------------------------------------------------

func TestSMSBowerStatusTransitions(t *testing.T) {
	phone := map[string]string{"provider": "smsbower", "activation_id": "A7", "number": "+15550007777"}

	tests := []struct {
		name       string
		act        func(p *Provider) error
		wantStatus int
		wantLog    string
	}{
		{
			name:       "sent is status 1",
			act:        func(p *Provider) error { return p.Sent("a@b.c", phone) },
			wantStatus: 1,
			wantLog:    "SMSBower 激活已标记短信已发送: A7",
		},
		{
			name:       "good is status 6",
			act:        func(p *Provider) error { return p.Good("a@b.c", phone) },
			wantStatus: 6,
			wantLog:    "SMSBower 激活已完成: A7",
		},
		{
			name: "bad is status 8",
			act: func(p *Provider) error {
				payload := copyPhone(phone)
				payload["error"] = "手机号已被使用"
				payload["status"] = "手机号已使用"
				return p.Bad("a@b.c", payload)
			},
			wantStatus: 8,
			wantLog:    "SMSBower 号码已标记不可用 [手机号已使用]",
		},
		{
			name: "transient bad still cancels with status 8",
			act: func(p *Provider) error {
				payload := copyPhone(phone)
				payload["error"] = "SMSBower 请求失败: connection reset"
				payload["status"] = "接码网络抖动"
				return p.Bad("a@b.c", payload)
			},
			wantStatus: 8,
			wantLog:    "SMSBower 网络抖动，已取消当前激活并换号 [接码网络抖动]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{}
			h := newHarness(t, enabledRaw(), client, nil, nil)
			if err := tc.act(h.provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			calls := client.statuses()
			if len(calls) != 1 {
				t.Fatalf("SetStatus calls = %v, want exactly one", calls)
			}
			if calls[0].status != tc.wantStatus || calls[0].activationID != "A7" {
				t.Fatalf("SetStatus(%q, %d), want (\"A7\", %d)", calls[0].activationID, calls[0].status, tc.wantStatus)
			}
			if !h.log.contains(tc.wantLog) {
				t.Fatalf("missing %q in:\n%s", tc.wantLog, h.log.dump())
			}
		})
	}
}

// TestBadTransientDetectedFromErrorText covers the second half of the app.py:16608
// condition: the transient marker may live in the error text rather than in the
// classified status.
func TestBadTransientDetectedFromErrorText(t *testing.T) {
	client := &fakeClient{}
	h := newHarness(t, enabledRaw(), client, nil, nil)
	err := h.provider.Bad("a@b.c", map[string]string{
		"provider":      "smsbower",
		"activation_id": "A8",
		"error":         "SMSBower 请求失败: max retries exceeded",
		"status":        "手机号不可用",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !h.log.contains("SMSBower 网络抖动，已取消当前激活并换号 [手机号不可用]") {
		t.Fatalf("transient error text must pick the wobble branch:\n%s", h.log.dump())
	}
}

func TestStatusCallbacksNeverFailTheAttempt(t *testing.T) {
	client := &fakeClient{statusErr: &smsbower.Error{Msg: "SMSBower 请求失败: boom"}}
	h := newHarness(t, enabledRaw(), client, nil, nil)
	phone := map[string]string{"provider": "smsbower", "activation_id": "A7"}

	if err := h.provider.Sent("a@b.c", phone); err != nil {
		t.Fatalf("Sent must swallow errors, got %v", err)
	}
	if err := h.provider.Good("a@b.c", phone); err != nil {
		t.Fatalf("Good must swallow errors, got %v", err)
	}
	if err := h.provider.Bad("a@b.c", phone); err != nil {
		t.Fatalf("Bad must swallow errors, got %v", err)
	}
	if !h.log.contains("SMSBower 激活标记短信已发送状态回传失败: A7") {
		t.Fatalf("failures must be logged:\n%s", h.log.dump())
	}
}

func TestStatusIsNotSentWithoutIDOrKey(t *testing.T) {
	client := &fakeClient{}

	// No activation id.
	h := newHarness(t, enabledRaw(), client, nil, nil)
	if err := h.provider.Sent("a@b.c", map[string]string{"provider": "smsbower"}); err != nil {
		t.Fatal(err)
	}

	// No API key: the RAW key var is what counts here, not the validated settings.
	raw := enabledRaw()
	raw.APIKey = "  "
	h2 := newHarness(t, raw, client, nil, nil)
	if err := h2.provider.Good("a@b.c", map[string]string{"provider": "smsbower", "activation_id": "A7"}); err != nil {
		t.Fatal(err)
	}

	if calls := client.statuses(); len(calls) != 0 {
		t.Fatalf("no status call expected, got %v", calls)
	}
}

func TestManualPhoneNeverTouchesSMSBower(t *testing.T) {
	client := &fakeClient{}
	h := newHarness(t, enabledRaw(), client, nil, []*models.PhoneEntry{{Number: "+15551110000", Status: StatusAvailable}})
	phone := map[string]string{"number": "+15551110000", "sms_url": "http://sms/pool"}

	if err := h.provider.Sent("a@b.c", phone); err != nil {
		t.Fatal(err)
	}
	if err := h.provider.Good("a@b.c", phone); err != nil {
		t.Fatal(err)
	}
	if calls := client.statuses(); len(calls) != 0 {
		t.Fatalf("a manual-pool number must not produce SMSBower status calls, got %v", calls)
	}
}

func TestGoodClearsTheAttemptCounter(t *testing.T) {
	client := &fakeClient{
		tiersErr: &smsbower.Error{Msg: "no tiers"},
		numbers:  []numberResult{{number: smsbower.Number{ActivationID: "A1", Number: "+15550000001"}}},
	}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	phone, err := h.provider.Next("A@B.c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.provider.AttemptCount("a@b.c") != 1 {
		t.Fatalf("counter should be 1 after a rental")
	}
	if err := h.provider.Good(" a@B.C ", phone); err != nil {
		t.Fatal(err)
	}
	// The counter key is trimmed + case-folded, so a differently cased address
	// still clears it (app.py:16600).
	if got := h.provider.AttemptCount("a@b.c"); got != 0 {
		t.Fatalf("counter after Good = %d, want 0", got)
	}
}

// TestConcurrentNextCountsEveryRental is the mutex test: the UI runs accounts
// concurrently and a lost increment would misreport how many numbers were
// bought for an address.
func TestConcurrentNextCountsEveryRental(t *testing.T) {
	const n = 32
	numbers := make([]numberResult, 0, n)
	for i := 0; i < n; i++ {
		numbers = append(numbers, numberResult{number: smsbower.Number{
			ActivationID: fmt.Sprintf("A%d", i),
			Number:       fmt.Sprintf("+1555000%04d", i),
		}})
	}
	client := &fakeClient{tiersErr: &smsbower.Error{Msg: "no tiers"}, numbers: numbers}
	h := newHarness(t, enabledRaw(), client, nil, nil)

	var wg sync.WaitGroup
	seen := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			phone, err := h.provider.Next("a@b.c", nil)
			if err != nil {
				t.Errorf("Next error: %v", err)
				return
			}
			seen <- phone["activation_id"]
		}()
	}
	wg.Wait()
	close(seen)

	unique := map[string]bool{}
	for id := range seen {
		if id == "" {
			t.Fatalf("a concurrent Next returned no number")
		}
		if unique[id] {
			t.Fatalf("activation %q handed out twice", id)
		}
		unique[id] = true
	}
	if got := h.provider.AttemptCount("a@b.c"); got != n {
		t.Fatalf("attempt counter = %d, want %d", got, n)
	}
}

// ---------------------------------------------------------------------------
// code
// ---------------------------------------------------------------------------

func TestCodeSMSBowerUsesRawKeyAndPythonTimeouts(t *testing.T) {
	client := &fakeClient{code: "445566"}
	// Deliberately invalid service code: the "code" action reads the RAW key var,
	// so an unvalidatable settings block must not break code retrieval.
	raw := Raw{Enabled: true, APIKey: " key ", Service: "bad service"}
	h := newHarness(t, raw, client, nil, nil)

	code, err := h.provider.Code("a@b.c", map[string]string{"provider": "smsbower", "activation_id": "A3"})
	if err != nil {
		t.Fatalf("Code error: %v", err)
	}
	if code != "445566" {
		t.Fatalf("code = %q", code)
	}
	if len(client.waitArgs) != 1 || client.waitArgs[0] != "A3/180/5" {
		t.Fatalf("WaitForCode args = %v, want [A3/180/5]", client.waitArgs)
	}
}

func TestCodeSMSBowerWithoutKeyErrors(t *testing.T) {
	raw := enabledRaw()
	raw.APIKey = ""
	h := newHarness(t, raw, &fakeClient{}, nil, nil)

	_, err := h.provider.Code("a@b.c", map[string]string{"provider": "smsbower", "activation_id": "A3"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var smsErr *smsbower.Error
	if !errors.As(err, &smsErr) || smsErr.Msg != "SMSBower API Key 为空，无法查询验证码" {
		t.Fatalf("err = %v, want the SMSBowerError text", err)
	}
}

func TestCodeManualPollsUntilCodeAndUpdatesPool(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+15551110000", SMSURL: "http://sms/pool", Status: StatusInUse, ReceiveCount: 1}
	log := &recorder{}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{phone})
	calls := 0
	provider := New(Config{
		// receive limit 3 -> after this code the count is 2, so still 可用.
		Settings: Raw{PhoneMaxReceiveCount: "3"},
		Pool:     pool,
		Log:      log.log,
		HTTPGet: func(_ context.Context, url string, timeout time.Duration) (string, error) {
			calls++
			if url != "http://sms/pool" {
				return "", fmt.Errorf("unexpected url %q", url)
			}
			if timeout <= 0 || timeout > 20*time.Second {
				return "", fmt.Errorf("request timeout out of range: %v", timeout)
			}
			if calls == 1 {
				return "no message yet", nil
			}
			return "  Your OpenAI code is 998877  ", nil
		},
		Sleep: func(time.Duration) {},
	})

	code, err := provider.Code("a@b.c", map[string]string{"number": "+15551110000", "sms_url": "http://sms/pool"})
	if err != nil {
		t.Fatalf("Code error: %v", err)
	}
	if code != "998877" {
		t.Fatalf("code = %q", code)
	}
	if phone.ReceiveCount != 2 || phone.Status != StatusAvailable || phone.LastCode != "998877" || phone.LastError != "" {
		t.Fatalf("pool entry not updated: %+v", phone)
	}
}

func TestManualCodeFreezesAtTheReceiveCap(t *testing.T) {
	phone := &models.PhoneEntry{Number: "+1555", SMSURL: "u", Status: StatusInUse, ReceiveCount: 1}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{phone})
	provider := New(Config{
		Settings: Raw{PhoneMaxReceiveCount: "2"},
		Pool:     pool,
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			return "OpenAI 123456", nil
		},
		Sleep: func(time.Duration) {},
	})

	if _, err := provider.Code("a@b.c", map[string]string{"number": "+1555", "sms_url": "u"}); err != nil {
		t.Fatal(err)
	}
	// The freeze test runs on the already-incremented count.
	if phone.Status != StatusFrozen {
		t.Fatalf("status = %q, want 冻结", phone.Status)
	}
}

func TestManualCodeTimeoutReportsLastPayload(t *testing.T) {
	provider := New(Config{
		Settings: Raw{},
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			return "still nothing", nil
		},
		// Real sleeps would make this test take a second; the loop logic is
		// unchanged, only the wall clock is.
		Sleep: func(time.Duration) { time.Sleep(5 * time.Millisecond) },
	})

	_, err := provider.waitForPhoneCode("+1555", "u", 1)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "等待手机号 +1555 短信验证码超时，最后返回: still nothing") {
		t.Fatalf("err = %v", err)
	}
}

func TestManualCodeTimeoutReportsLastTransportError(t *testing.T) {
	provider := New(Config{
		Settings: Raw{},
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			return "", errors.New("dial tcp: connection refused")
		},
		Sleep: func(time.Duration) { time.Sleep(5 * time.Millisecond) },
	})

	_, err := provider.waitForPhoneCode("+1555", "u", 1)
	if err == nil || !strings.Contains(err.Error(), "dial tcp: connection refused") {
		t.Fatalf("err = %v, want the transport error in the timeout text", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("map = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("map[%q] = %q, want %q (full: %v)", k, got[k], v, got)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func copyPhone(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
