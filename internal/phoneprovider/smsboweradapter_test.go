package phoneprovider

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
)

// MONEY SAFETY: nothing here may reach smsbower.page. Every provider in this
// file is built with a NewClient that hands back a fake, so GetNumber/SetStatus
// stay in-process. The real client is never constructed.

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// releaseSpy is fakeClient plus the context state seen by each SetStatus call.
// The release path is only worth anything if it runs on a LIVE context — a
// cancelled one would fail before the request left the process — so the ctx
// error is recorded and asserted.
type releaseSpy struct {
	*fakeClient
	mu      sync.Mutex
	ctxErrs []error
}

func newReleaseSpy(numbers ...numberResult) *releaseSpy {
	return &releaseSpy{fakeClient: &fakeClient{numbers: numbers}}
}

func (r *releaseSpy) SetStatus(ctx context.Context, activationID string, status int) (string, error) {
	r.mu.Lock()
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	r.mu.Unlock()
	return r.fakeClient.SetStatus(ctx, activationID, status)
}

func (r *releaseSpy) contextErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.ctxErrs...)
}

// stateSnapshot 构造 UI 传入的 state.json 结构；smsbower_* 键位于
// "settings" 对象内，值使用脱敏后的离线测试样例。
func stateSnapshot(overrides map[string]any) map[string]any {
	sm := map[string]any{
		"smsbower_enabled":   true,
		"smsbower_api_key":   "LIVE-KEY",
		"smsbower_service":   "dr",
		"smsbower_country":   "33",
		"smsbower_max_price": "0.07",
	}
	for k, v := range overrides {
		sm[k] = v
	}
	return map[string]any{"settings": sm}
}

type adapterHarness struct {
	provider *SMSBowerProvider
	client   *releaseSpy
	log      *recorder
	cancel   context.CancelFunc
}

func newAdapterHarness(t *testing.T, snapshot map[string]any, client *releaseSpy) *adapterHarness {
	t.Helper()
	if client == nil {
		client = newReleaseSpy()
	}
	log := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	provider := NewSMSBowerProvider(SMSBowerConfig{
		Snapshot:  func() map[string]any { return snapshot },
		Log:       log.log,
		Context:   ctx,
		NewClient: func(string) (SMSClient, error) { return client, nil },
		HTTPGet: func(context.Context, string, time.Duration) (string, error) {
			t.Fatalf("unexpected HTTP call in this test")
			return "", nil
		},
		Sleep: func(time.Duration) {},
	})
	t.Cleanup(func() {
		cancel()
		provider.Close()
	})
	return &adapterHarness{provider: provider, client: client, log: log, cancel: cancel}
}

func rented(id, number string) numberResult {
	return numberResult{number: smsbower.Number{ActivationID: id, Number: number}}
}

// waitFor polls until cond holds; the release triggered by ctx.Done runs in a
// goroutine, so the assertion cannot be immediate.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---------------------------------------------------------------------------
// SnapshotSettings — the settings keys
// ---------------------------------------------------------------------------

func TestSnapshotSettingsReadsTheRealStateKeys(t *testing.T) {
	src := SnapshotSettings{Snapshot: func() map[string]any {
		return stateSnapshot(map[string]any{"phone_max_receive_count": 3})
	}}

	if !src.SMSBowerEnabled() {
		t.Fatalf("smsbower_enabled not read")
	}
	if src.SMSBowerAPIKey() != "LIVE-KEY" {
		t.Fatalf("smsbower_api_key = %q", src.SMSBowerAPIKey())
	}
	if src.PhoneReceiveLimit() != 3 {
		t.Fatalf("phone_max_receive_count = %d", src.PhoneReceiveLimit())
	}
	got, err := src.SMSBowerSettings()
	if err != nil {
		t.Fatalf("SMSBowerSettings error: %v", err)
	}
	want := Settings{Enabled: true, APIKey: "LIVE-KEY", Service: "dr", Country: "33", MaxPrice: "0.07"}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}

// MONEY: save writes "" for "no cap" (app.py:14278) but load turns it back into
// "0.07" (app.py:14190). If the adapter read the key literally, the empty string
// would mean NO price cap and rent at any price.
func TestSnapshotSettingsEmptyMaxPriceIsNotNoCap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot map[string]any
	}{
		{"empty string", stateSnapshot(map[string]any{"smsbower_max_price": ""})},
		{"whitespace", stateSnapshot(map[string]any{"smsbower_max_price": "   "})},
		{"nil", stateSnapshot(map[string]any{"smsbower_max_price": nil})},
		{"key absent", map[string]any{"settings": map[string]any{"smsbower_enabled": true}}},
		{"no settings object", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SnapshotSettings{Snapshot: func() map[string]any { return tc.snapshot }}.SMSBowerSettings()
			if err != nil {
				t.Fatalf("SMSBowerSettings error: %v", err)
			}
			if got.MaxPrice != "0.07" {
				t.Fatalf("max price = %q, want 0.07 (an empty cap would rent at any price)", got.MaxPrice)
			}
			if got.Service != "dr" || got.Country != "33" {
				t.Fatalf("service/country defaults = %q/%q", got.Service, got.Country)
			}
		})
	}
}

func TestSnapshotSettingsNilSnapshotIsDisabled(t *testing.T) {
	src := SnapshotSettings{}
	if src.SMSBowerEnabled() || src.SMSBowerAPIKey() != "" || src.PhoneReceiveLimit() != 0 {
		t.Fatalf("an unwired snapshot must never look enabled")
	}
}

// Python re-read the Tk variables on every action (app.py:16409, 16526, 16586),
// so a settings edit mid-run takes effect on the next call.
func TestSnapshotSettingsAreReadLive(t *testing.T) {
	current := stateSnapshot(nil)
	src := SnapshotSettings{Snapshot: func() map[string]any { return current }}
	if !src.SMSBowerEnabled() {
		t.Fatalf("expected enabled")
	}
	current = stateSnapshot(map[string]any{"smsbower_enabled": false, "smsbower_api_key": "  NEXT  "})
	if src.SMSBowerEnabled() {
		t.Fatalf("expected the disable to be picked up on the next read")
	}
	if src.SMSBowerAPIKey() != "NEXT" {
		t.Fatalf("api key = %q, want the stripped NEXT", src.SMSBowerAPIKey())
	}
}

func TestSnapshotSettingsInvalidValuesSurfaceThePythonError(t *testing.T) {
	src := SnapshotSettings{Snapshot: func() map[string]any {
		return stateSnapshot(map[string]any{"smsbower_country": "33a"})
	}}
	if _, err := src.SMSBowerSettings(); err == nil || !strings.Contains(err.Error(), "SMSBower 国家 ID 必须是数字") {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// the adapter: rental + release
// ---------------------------------------------------------------------------

func TestAdapterRentsThroughTheSnapshotSettings(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	client.tiers = []smsbower.PriceTier{{Cost: 0.05, Count: 4}, {Cost: 0.09, Count: 9}}
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	phone, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if phone["number"] != "+15551230000" || phone["provider"] != "smsbower" || phone["activation_id"] != "A1" {
		t.Fatalf("phone = %v", phone)
	}
	if phone["sms_url"] != "smsbower://A1" {
		t.Fatalf("sms_url = %q", phone["sms_url"])
	}
	// MONEY: the 0.09 tier is above the configured 0.07 cap and must never be
	// offered to getNumber; the country id is the US mapping, not the stored 33.
	if prices := client.prices(); len(prices) != 1 || prices[0] != "0.05" {
		t.Fatalf("maxPrice sent = %v, want [0.05]", prices)
	}
	if got := client.numberCalls[0].country; got != "187" {
		t.Fatalf("country = %q, want 187", got)
	}
	if ids := h.provider.OutstandingActivations(); len(ids) != 1 || ids[0] != "A1" {
		t.Fatalf("outstanding = %v, want [A1]", ids)
	}
}

func TestAdapterCloseReleasesAnUnfinishedRental(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	if _, err := h.provider.Next("user@example.com", map[string]string{"country": "US"}); err != nil {
		t.Fatalf("Next error: %v", err)
	}
	// Nothing else happens — this is the pre-submit abort of app.py:10259-10260,
	// where Python re-raised and never released the number.
	h.provider.Close()

	want := []statusCall{{"A1", smsbower.StatusCancel}}
	if got := client.statuses(); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("status calls = %v, want %v", got, want)
	}
	if ids := h.provider.OutstandingActivations(); len(ids) != 0 {
		t.Fatalf("still outstanding after Close: %v", ids)
	}
	if !h.log.contains("SMSBower 释放未完成的激活: +15551230000 激活ID=A1") {
		t.Fatalf("missing release log:\n%s", h.log.dump())
	}
	// Close is idempotent: a second call must not fire another cancel.
	h.provider.Close()
	if got := client.statuses(); len(got) != 1 {
		t.Fatalf("second Close sent more status calls: %v", got)
	}
}

func TestAdapterContextCancelReleasesOnALiveContext(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	if _, err := h.provider.Next("user@example.com", map[string]string{"country": "US"}); err != nil {
		t.Fatalf("Next error: %v", err)
	}
	h.cancel()

	waitFor(t, "the cancellation watchdog to release the rental", func() bool {
		return len(client.statuses()) == 1
	})
	if got := client.statuses(); got[0] != (statusCall{"A1", smsbower.StatusCancel}) {
		t.Fatalf("status call = %v", got)
	}
	// The release must NOT ride on the cancelled job context, or the request
	// would fail before reaching SMSBower and the rental would leak silently.
	for i, err := range client.contextErrors() {
		if err != nil {
			t.Fatalf("release call %d ran on a dead context: %v", i, err)
		}
	}
}

func TestAdapterGoodEndsTheReleaseResponsibility(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	phone, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if err := h.provider.Sent("user@example.com", phone); err != nil {
		t.Fatalf("Sent error: %v", err)
	}
	if err := h.provider.Good("user@example.com", phone); err != nil {
		t.Fatalf("Good error: %v", err)
	}
	h.provider.Close()

	// 1 = sent, 6 = finish, and NOTHING after it: a status 8 following a finish
	// would try to cancel an activation that is already paid for.
	want := []statusCall{{"A1", smsbower.StatusReadyToReceive}, {"A1", smsbower.StatusFinish}}
	got := client.statuses()
	if len(got) != len(want) {
		t.Fatalf("status calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status calls = %v, want %v", got, want)
		}
	}
}

func TestAdapterBadRetriesTheCancelAtClose(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	phone, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	bad := phoneCopy(phone)
	bad["error"] = "手机号已被使用"
	bad["status"] = "手机号不可用"
	if err := h.provider.Bad("user@example.com", bad); err != nil {
		t.Fatalf("Bad error: %v", err)
	}
	h.provider.Close()

	// Two cancels on purpose: the first one lands inside SMSBower's
	// EARLY_CANCEL_DENIED window most of the time, and Python threw the outcome
	// away (app.py:16532), so the retry is the only way the hold is really freed.
	got := client.statuses()
	if len(got) != 2 || got[0] != (statusCall{"A1", smsbower.StatusCancel}) || got[1] != (statusCall{"A1", smsbower.StatusCancel}) {
		t.Fatalf("status calls = %v, want two cancels", got)
	}
	if !h.log.contains("SMSBower 重试释放未完成的激活: +15551230000 激活ID=A1") {
		t.Fatalf("missing retry log:\n%s", h.log.dump())
	}
}

// MONEY: pressing Stop must not buy one more number.
func TestAdapterNextRefusesToRentAfterCancel(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)
	h.cancel()

	phone, err := h.provider.Next("user@example.com", map[string]string{"country": "US"})
	if err == nil {
		t.Fatalf("expected an error, got %v", phone)
	}
	if !strings.Contains(err.Error(), "任务已取消，停止取号") {
		t.Fatalf("err = %v", err)
	}
	if len(client.prices()) != 0 {
		t.Fatalf("a cancelled job rented a number: %v", client.prices())
	}
}

func TestAdapterNextRefusesToRentAfterClose(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230000"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)
	h.provider.Close()

	if _, err := h.provider.Next("user@example.com", map[string]string{"country": "US"}); err == nil {
		t.Fatalf("expected ErrClosed")
	}
	if len(client.prices()) != 0 {
		t.Fatalf("a closed provider rented a number: %v", client.prices())
	}
}

// A manual-pool number carries no activation id, so the adapter must not invent
// a release for it — status 8 with an empty id is exactly what
// _smsbower_set_activation_status refuses to send (app.py:16527).
func TestAdapterIgnoresManualPoolNumbers(t *testing.T) {
	client := newReleaseSpy()
	snapshot := stateSnapshot(map[string]any{"smsbower_enabled": false})
	log := &recorder{}
	pool := NewMemoryPool(nil, []*models.PhoneEntry{{Number: "+15559990000", SMSURL: "http://sms/1", Status: StatusAvailable}})
	provider := NewSMSBowerProvider(SMSBowerConfig{
		Snapshot:  func() map[string]any { return snapshot },
		Pool:      pool,
		Log:       log.log,
		NewClient: func(string) (SMSClient, error) { return client, nil },
		Sleep:     func(time.Duration) {},
	})

	phone, err := provider.Next("user@example.com", map[string]string{"country": "US"})
	if err != nil {
		t.Fatalf("Next error: %v", err)
	}
	if phone["number"] != "+15559990000" {
		t.Fatalf("phone = %v", phone)
	}
	if ids := provider.OutstandingActivations(); len(ids) != 0 {
		t.Fatalf("a manual number must not be tracked as a rental: %v", ids)
	}
	provider.Close()
	if got := client.statuses(); len(got) != 0 {
		t.Fatalf("SMSBower was called for a manual number: %v", got)
	}
}

// Every rental of a rotation is released, in rental order, even when the flow
// dies without ever calling bad/good.
func TestAdapterReleasesEveryRentalInOrder(t *testing.T) {
	client := newReleaseSpy(rented("A1", "+15551230001"), rented("A2", "+15551230002"))
	h := newAdapterHarness(t, stateSnapshot(nil), client)

	for i := 0; i < 2; i++ {
		if _, err := h.provider.Next("user@example.com", map[string]string{"country": "US"}); err != nil {
			t.Fatalf("Next %d error: %v", i, err)
		}
	}
	if ids := h.provider.OutstandingActivations(); len(ids) != 2 || ids[0] != "A1" || ids[1] != "A2" {
		t.Fatalf("outstanding = %v", ids)
	}

	h.provider.Release()
	got := client.statuses()
	if len(got) != 2 || got[0] != (statusCall{"A1", smsbower.StatusCancel}) || got[1] != (statusCall{"A2", smsbower.StatusCancel}) {
		t.Fatalf("status calls = %v, want A1 then A2 cancelled", got)
	}
	// Release does not close: the provider stays usable.
	if err := h.provider.stopped(); err != nil {
		t.Fatalf("Release must not close the provider: %v", err)
	}
}

func phoneCopy(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+2)
	for k, v := range src {
		out[k] = v
	}
	return out
}
