package ui

// MONEY SAFETY: a batch is the most expensive thing this app can do — one
// billable phone rental and one real payment link PER ACCOUNT. Nothing in this
// file may construct a worker. The orchestration is exercised through
// runBatchWith with a fake Runner, and everything else is the pure selection /
// pool / classification code.
//
// StartBatchRegister appears below ONLY in cases that are refused before any
// job exists.

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/batch"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

func batchApp(t *testing.T, rows []any, s map[string]any) *App {
	t.Helper()
	snapshot := map[string]any{"schema_version": 2, "accounts": rows}
	if s != nil {
		snapshot["settings"] = s
	}
	return newTempApp(t, snapshot)
}

// A typo must not silently shrink a batch the user is about to pay for, and a
// duplicated row must not double-charge one address.
func TestResolveBatchSelectionRefusesBeforeSpendingAnything(t *testing.T) {
	locked := accountMap("box@example.com", "free", statusEmailLocked, "未分组")
	app := batchApp(t, []any{
		accountMap("a@example.com", "free", "", "未分组"),
		accountMap("b@example.com", "free", "", "未分组"),
		locked,
	}, nil)
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if _, _, err := app.resolveBatchSelection(snapshot, []string{"a@example.com", "ghost@example.com"}); err == nil {
		t.Error("an unknown address was accepted; the batch would silently shrink")
	}
	if _, _, err := app.resolveBatchSelection(snapshot, []string{"a@example.com", "A@example.com"}); err == nil {
		t.Error("a duplicated address was accepted; it would be charged twice")
	}
	if _, _, err := app.resolveBatchSelection(snapshot, []string{"  "}); err == nil {
		t.Error("a blank address was accepted")
	}

	// A locked mailbox is skipped rather than rejecting the whole batch — Python
	// logs it and moves on (app.py:16694-16696).
	accounts, skipped, err := app.resolveBatchSelection(snapshot, []string{"a@example.com", "box@example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("resolveBatchSelection: %v", err)
	}
	if len(accounts) != 2 || accounts[0].Email != "a@example.com" || accounts[1].Email != "b@example.com" {
		t.Errorf("accounts = %v, want a and b in selection order", accountEmails(accounts))
	}
	if len(skipped) != 1 || skipped[0] != "box@example.com" {
		t.Errorf("skipped = %v, want the locked mailbox", skipped)
	}
}

// A batch with nothing runnable must not create a job at all.
func TestStartBatchRefusesAnEmptySelection(t *testing.T) {
	app := batchApp(t, []any{accountMap("a@example.com", "free", "", "未分组")}, nil)

	if _, err := app.StartBatchRegister(StartBatchRequest{Confirmed: true}); err == nil {
		t.Error("an empty selection started a batch")
	}
	if _, err := app.StartBatchRegister(StartBatchRequest{
		Emails: []string{"ghost@example.com"}, Confirmed: true,
	}); err == nil {
		t.Error("an unknown address started a batch")
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Fatalf("a refused batch created %d job(s)", len(jobs))
	}
}

// app.py:16684's `if self.running` forbids a SECOND 批量 run. Two batches over
// disjoint selections each open their own 认证 window, so the machine would run
// 2 x AuthConcurrency browsers — the bound's whole purpose. Driven through
// registerJob directly, because StartBatchRegister would launch real workers.
func TestASecondBatchIsRefusedWhileOneIsRunning(t *testing.T) {
	app := batchApp(t, nil, nil)

	first, err := app.registerJob(JobBatchRegister, "", "", func() {})
	if err != nil {
		t.Fatalf("registerJob: %v", err)
	}
	if _, err := app.registerJob(JobBatchRegister, "", "", func() {}); err == nil {
		t.Fatal("a second batch started while the first was running")
	}

	// A standalone run is NOT serialised against a batch: Python's guard gates
	// only the 批量 buttons, and refusing here would be more restrictive than Tk.
	child, err := app.registerJob(JobRegister, "solo@example.com", "", func() {})
	if err != nil {
		t.Fatalf("a standalone run was refused during a batch: %v", err)
	}
	// ... and neither is a child of the batch itself, which is the same code path.
	if _, err := app.registerJob(JobRegister, "kid@example.com", first, func() {}); err != nil {
		t.Fatalf("a batch child was refused: %v", err)
	}

	app.finishJob(child, JobRegister, models.MailAccount{Email: "solo@example.com"}, nil, errors.New("stop"), false)
	app.markJobFinished(first, batch.Report[models.MailAccount]{}, nil, false)
	if _, err := app.registerJob(JobBatchRegister, "", "", func() {}); err != nil {
		t.Fatalf("a batch was refused after the previous one finished: %v", err)
	}
}

// app.py:17671-17679 takes a FRESH proxy per attempt, and app.py:17683-17688
// stops that account with 代理耗尽 once the pool is dry. The single-account path
// deliberately does neither — registerDynamicProxy takes the pool's first entry
// and keeps it — so rotation and exhaustion only exist here.
func TestBatchRotatesProxiesAndReportsExhaustion(t *testing.T) {
	app := batchApp(t, nil, nil)
	st := settings.Settings{AuthConcurrency: 1}
	pool := proxypool.NewSet()
	pool.SetText(proxypool.RoleRegister, "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p")

	accounts := []models.MailAccount{
		{Email: "a@example.com"}, {Email: "b@example.com"},
	}

	var (
		mu   sync.Mutex
		seen []string
	)
	// Every attempt fails with the ONE error class that earns a fresh proxy, and
	// the pool is dropped from as it goes, so both accounts must end 代理耗尽
	// rather than looping forever.
	runner := batch.RunnerFunc[models.MailAccount, string](func(_ context.Context, job batch.Job[models.MailAccount], proxy string) error {
		mu.Lock()
		seen = append(seen, job.Key+" via "+proxy)
		mu.Unlock()
		pool.Remove(proxypool.RoleRegister, proxy)
		return models.NewProxyExitCheckError("exit check failed")
	})

	parentID, err := app.registerJob(JobBatchRegister, "", "", func() {})
	if err != nil {
		t.Fatalf("registerJob: %v", err)
	}
	report := app.runBatchWith(context.Background(), parentID, accounts, st, pool, 2, func(string) {}, runner)

	if got := report.Counts().ProxyExhausted; got != 2 {
		t.Errorf("代理耗尽 count = %d, want 2 (got %+v)", got, report.Counts())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("attempts = %v, want one per pool entry before it ran dry", seen)
	}
	if seen[0] == seen[1] {
		t.Errorf("both attempts used the same exit: %v", seen)
	}
	for _, res := range report.Results {
		if res.Status != batch.StatusProxyExhausted {
			t.Errorf("%s: status = %q, want %q", res.Key, res.Status, batch.StatusProxyExhausted)
		}
	}
	// The parent reflects the finished count.
	view, ok := app.jobView(parentID)
	if !ok {
		t.Fatal("the parent job vanished")
	}
	if view.Done != 2 || view.Total != 2 {
		t.Errorf("parent progress = %d/%d, want 2/2", view.Done, view.Total)
	}
	if view.Status != StatusSucceeded {
		t.Errorf("parent status = %q, want the batch to be finished", view.Status)
	}
}

// Without a pool every account gets exactly ONE attempt: app.py:17689-17692
// returns immediately once attempts > 0.
func TestBatchWithoutAPoolRunsEachAccountOnce(t *testing.T) {
	app := batchApp(t, nil, nil)
	var attempts int32
	var mu sync.Mutex
	runner := batch.RunnerFunc[models.MailAccount, string](func(context.Context, batch.Job[models.MailAccount], string) error {
		mu.Lock()
		attempts++
		mu.Unlock()
		return models.NewProxyExitCheckError("exit check failed") // classified Retry
	})
	parentID, _ := app.registerJob(JobBatchRegister, "", "", func() {})
	report := app.runBatchWith(context.Background(), parentID, []models.MailAccount{
		{Email: "a@example.com"}, {Email: "b@example.com"},
	}, settings.Settings{AuthConcurrency: 4}, nil, 0, func(string) {}, runner)

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("%d attempts for 2 accounts; without a pool a Retry must not loop", attempts)
	}
	// internal/batch forces limit=1 when there is no pool, so a retryable failure
	// lands as AttemptsExhausted. Python simply `return`s there and pushes no
	// status at all (app.py:17690-17691) — which is why the per-account 状态
	// written to state.json comes from finishJob, never from Result.Status.
	if got := report.Counts().AttemptsExhausted; got != 2 {
		t.Errorf("attempts-exhausted = %d, want 2 (%+v)", got, report.Counts())
	}
	if got := report.Counts().Succeeded; got != 0 {
		t.Errorf("succeeded = %d, want 0", got)
	}
}

// Only a 代理检测失败 exit precheck failure earns a fresh proxy. Retrying a phone
// requirement or a locked mailbox would spend money on a condition that cannot
// change between attempts (app.py:17705-17714).
func TestBatchClassifyOnlyRetriesTheExitPrecheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want batch.Disposition
	}{
		{"exit precheck", models.NewProxyExitCheckError("bad exit"), batch.Retry},
		{"a proxy error with another status", &models.ProxyExitCheckError{Msg: "x", Status: "代理非日本"}, batch.Fail},
		{"phone required", &models.PhoneRequiredError{}, batch.Fail},
		{"deactivated", models.NewAccountDeactivatedError(), batch.Fail},
		{"anything else", errors.New("boom"), batch.Fail},
		{"nil", nil, batch.Fail},
	} {
		if got := batchClassify(tc.err); got != tc.want {
			t.Errorf("%s: disposition = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Which pool the batch rotates through (app.py:16694, 17412/17424).
func TestAuthProxyPoolSelection(t *testing.T) {
	app := batchApp(t, nil, nil)
	const registerPool = "1.1.1.1:8080:u:p"
	const paymentPool = "9.9.9.9:9090:u:p"

	base := settings.Settings{DynamicProxies: registerPool, PaymentDynamicProxy: paymentPool}

	pool, size := app.authProxyPool(base)
	if pool == nil || size != 1 {
		t.Fatalf("default: pool=%v size=%d", pool, size)
	}
	if got := pool.TakeAuth(false); got != "http://u:p@1.1.1.1:8080" {
		t.Errorf("default exit = %q, want the register pool", got)
	}

	withPayment := base
	withPayment.RegisterWithPaymentProxy = true
	pool, size = app.authProxyPool(withPayment)
	if pool == nil || size != 1 {
		t.Fatalf("特殊情况: pool=%v size=%d", pool, size)
	}
	if got := pool.TakeAuth(true); got != "http://u:p@9.9.9.9:9090" {
		t.Errorf("特殊情况 exit = %q, want the payment pool", got)
	}

	// 全走本地代理 empties every pool (app.py:16719), so there is nothing to
	// rotate and every account gets one attempt through the local proxy alone.
	localOnly := base
	localOnly.ProxyRouteMode = settings.ProxyRouteModeLocalOnly
	if pool, size = app.authProxyPool(localOnly); pool != nil || size != 0 {
		t.Errorf("全走本地代理: pool=%v size=%d, want nil/0", pool, size)
	}

	empty := settings.Settings{}
	if pool, size = app.authProxyPool(empty); pool != nil || size != 0 {
		t.Errorf("no pool text: pool=%v size=%d, want nil/0", pool, size)
	}
}

// Two runs on one account means two browsers logging it in from two exits, each
// able to rent its own billable number. Python cannot produce the collision;
// a webview can.
func TestRegisterJobRefusesASecondRunForTheSameAccount(t *testing.T) {
	app := batchApp(t, nil, nil)
	if _, err := app.registerJob(JobRegister, "a@example.com", "", func() {}); err != nil {
		t.Fatalf("first registerJob: %v", err)
	}
	if _, err := app.registerJob(JobRelink, "A@EXAMPLE.COM", "", func() {}); err == nil {
		t.Error("a second run for the same address was accepted")
	} else if !strings.Contains(err.Error(), "已有任务在运行") {
		t.Errorf("unexpected error: %v", err)
	}
	// A batch PARENT carries no email and must not be blocked by it.
	if _, err := app.registerJob(JobBatchRegister, "", "", func() {}); err != nil {
		t.Errorf("a batch parent was refused: %v", err)
	}
	// Once the first run finishes, the address is free again.
	app.markJobFinished("job-1", nil, nil, false)
	if _, err := app.registerJob(JobRelink, "a@example.com", "", func() {}); err != nil {
		t.Errorf("the address stayed locked after its run finished: %v", err)
	}
}

// app.py removes a failed exit from the pool at TWO sites: 17706 (and retries
// the account) and 17710 (and gives up on it). Only honouring the first leaves a
// dead exit in the pool for every remaining account to be handed in turn, and
// each of those handoffs is another browser launch on a known-broken exit.
func TestDropFailedAuthProxyRemovesEveryDeadExit(t *testing.T) {
	const dead = "http://u:p@1.1.1.1:8080"
	newPool := func() *proxypool.Set {
		p := proxypool.NewSet()
		p.SetText(proxypool.RoleRegister, "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p")
		return p
	}

	for _, tc := range []struct {
		name      string
		err       error
		remaining int
		retryLine bool
	}{
		{"exit precheck (17706)", models.NewProxyExitCheckError("bad exit"), 1, true},
		{"a proxy error with another status (17710)", &models.ProxyExitCheckError{Msg: "x", Status: "代理非日本"}, 1, false},
		// Not a ProxyExitCheckError: the exit is fine, the account is not. Dropping
		// it would shrink the pool on every locked mailbox in the selection.
		{"phone required", &models.PhoneRequiredError{}, 2, false},
		{"anything else", errors.New("boom"), 2, false},
		{"success", nil, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newPool()
			var lines []string
			dropFailedAuthProxy(pool, proxypool.RoleRegister, dead, tc.err, func(s string) { lines = append(lines, s) })

			if got := pool.Remaining(proxypool.RoleRegister); got != tc.remaining {
				t.Errorf("pool has %d entries, want %d", got, tc.remaining)
			}
			removed := tc.remaining == 1
			if removed != (len(lines) > 0) {
				t.Fatalf("removed=%v but logged %v", removed, lines)
			}
			if !removed {
				return
			}
			if got := strings.Contains(lines[0], "自动换下一个代理重试"); got != tc.retryLine {
				t.Errorf("retry line present = %v, want %v (lines %v)", got, tc.retryLine, lines)
			}
			if last := lines[len(lines)-1]; !strings.HasPrefix(last, "失败注册代理已移除: ") {
				t.Errorf("last line = %q, want the removal notice", last)
			}
			// The pool text is where credentials live. Python's register-side
			// notice prints them raw; the port masks, like the payment side does.
			for _, line := range lines {
				if strings.Contains(line, "u:p@") {
					t.Errorf("credentials leaked into the log: %q", line)
				}
			}
		})
	}

	// A proxy that is not in the pool, and the empty proxy, must log NOTHING —
	// app.py:17404's `if not dynamic_proxy` and 17354's `if not removed`.
	for _, proxy := range []string{"", "   ", "http://u:p@9.9.9.9:9090"} {
		pool := newPool()
		var lines []string
		dropFailedAuthProxy(pool, proxypool.RoleRegister, proxy, models.NewProxyExitCheckError("bad exit"),
			func(s string) { lines = append(lines, s) })
		if len(lines) != 0 || pool.Remaining(proxypool.RoleRegister) != 2 {
			t.Errorf("proxy %q: logged %v, pool has %d", proxy, lines, pool.Remaining(proxypool.RoleRegister))
		}
	}

	// A nil pool is "no pool at all" (全走本地代理, or an empty one) and must not panic.
	dropFailedAuthProxy(nil, proxypool.RoleRegister, dead, models.NewProxyExitCheckError("bad exit"),
		func(string) { t.Error("a nil pool logged a removal") })

	// The 特殊情况 pool names itself differently (app.py:17513 vs 17357).
	if got := failedAuthProxyRemovedLine(proxypool.RoleCreate, dead); got != "失败支付代理已移除: http://***@1.1.1.1:8080" {
		t.Errorf("payment-pool notice = %q", got)
	}
}

// THE POOL IS PERSISTED STATE. Every mutation in Tk goes through
// _rotate_proxy_pool_values (app.py:17316) or _remove_*_dynamic_proxy_value
// (17342/17492), and every one of them ends with save_state() — the textarea IS
// the pool. A Set built per batch and thrown away hands every dead exit back to
// the next batch and never advances the rotation.
func TestAuthPoolRotationAndRemovalArePersisted(t *testing.T) {
	const poolText = "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p\n3.3.3.3:8080:u:p"
	app := batchApp(t, nil, map[string]any{"dynamic_proxies": poolText})

	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	pool, size := app.authProxyPool(settings.FromSnapshot(snapshot))
	if pool == nil || size != 3 {
		t.Fatalf("pool=%v size=%d", pool, size)
	}

	onDisk := func() string {
		t.Helper()
		s, _ := readJSONFile(t, app.store.StateFile)["settings"].(map[string]any)
		text, _ := s["dynamic_proxies"].(string)
		return text
	}
	// Building the pool must not write by itself — the callback goes on AFTER
	// the seeding SetText, or every batch would rewrite state.json to start.
	if got := onDisk(); got != poolText {
		t.Fatalf("authProxyPool rewrote the pool on construction:\n got %q\nwant %q", got, poolText)
	}

	const (
		p1 = "http://u:p@1.1.1.1:8080"
		p2 = "http://u:p@2.2.2.2:8080"
		p3 = "http://u:p@3.3.3.3:8080"
	)
	if got := pool.TakeAuth(false); got != p1 {
		t.Fatalf("first take = %q, want the head", got)
	}
	if got, want := onDisk(), p2+"\n"+p3+"\n"+p1; got != want {
		t.Errorf("after a take:\n got %q\nwant the taken exit rotated to the tail %q", got, want)
	}

	dropFailedAuthProxy(pool, proxypool.RoleRegister, p1, models.NewProxyExitCheckError("bad exit"), func(string) {})
	if got, want := onDisk(), p2+"\n"+p3; got != want {
		t.Errorf("after a removal:\n got %q\nwant %q", got, want)
	}

	// Nothing outside the pool key may move. accounts is the one that would hurt.
	after := readJSONFile(t, app.store.StateFile)
	if rows, _ := after["accounts"].([]any); len(rows) != 0 {
		t.Errorf("accounts = %v, want the empty list preserved", after["accounts"])
	}

	// A no-op mutation must not rewrite the file: removing an entry that is not
	// there fires no callback, and re-persisting an unchanged pool is refused by
	// errNoStateChange. Otherwise every attempt of a large batch rewrites
	// state.json against a file the Python app also holds.
	before, err := os.ReadFile(app.store.StateFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	pool.Remove(proxypool.RoleRegister, "http://u:p@9.9.9.9:9090")
	app.persistAuthPool(proxypool.RoleRegister, pool)
	unchanged, err := os.ReadFile(app.store.StateFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(unchanged) {
		t.Error("a no-op pool mutation rewrote state.json")
	}
}

// The 特殊情况 checkbox redirects the pool, so the persisted key has to follow it
// — writing the rotation of the 第一步 pool into dynamic_proxies would corrupt
// both (app.py:17492's payment-side twin of 17342).
func TestAuthPoolPersistsToTheRoleItRotated(t *testing.T) {
	app := batchApp(t, nil, map[string]any{
		"dynamic_proxies":             "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p",
		"payment_dynamic_proxy":       "8.8.8.8:8080:u:p\n9.9.9.9:9090:u:p",
		"register_with_payment_proxy": true,
	})
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	st := settings.FromSnapshot(snapshot)
	pool, size := app.authProxyPool(st)
	if pool == nil || size != 2 {
		t.Fatalf("pool=%v size=%d", pool, size)
	}
	if got := pool.TakeAuth(true); got != "http://u:p@8.8.8.8:8080" {
		t.Fatalf("take = %q, want the 第一步 pool", got)
	}

	s, _ := readJSONFile(t, app.store.StateFile)["settings"].(map[string]any)
	if got, want := s["payment_dynamic_proxy"], "http://u:p@9.9.9.9:9090\nhttp://u:p@8.8.8.8:8080"; got != want {
		t.Errorf("payment_dynamic_proxy = %q, want %q", got, want)
	}
	// Verbatim, not even re-normalized: the register pool was never loaded into a
	// proxypool.Pool, so it round-trips through settings as the raw text the user
	// typed. Seeing normalized URLs here would mean the wrong pool was rotated.
	if got, want := s["dynamic_proxies"], "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p"; got != want {
		t.Errorf("dynamic_proxies = %q, want the untouched register pool %q", got, want)
	}
}

// _next_dynamic_proxy (app.py:17600-17607) is a plain counter modulo the pool's
// CURRENT length: it survives a pool edit, never writes anything, and restarts
// from the head only when the app does. 重新获取 used to take entries[0] every
// time, so running down a list of accounts created every payment link from one
// exit — exactly what the pool exists to prevent.
func TestNextDynamicProxyRotates(t *testing.T) {
	app := batchApp(t, nil, nil)
	three := []string{"a", "b", "c"}

	var got []string
	for i := 0; i < 7; i++ {
		got = append(got, app.nextDynamicProxy(three))
	}
	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rotation = %v, want %v", got, want)
	}

	// The cursor is NOT an index into a remembered list. It has advanced 7 times;
	// against a two-entry pool the next read is 7 % 2 == 1.
	if v := app.nextDynamicProxy([]string{"x", "y"}); v != "y" {
		t.Errorf("after a pool edit = %q, want %q — the counter is not reset", v, "y")
	}
	// An empty pool is "" and must NOT burn a tick, or a run with no pool would
	// silently shift every later account's exit.
	if v := app.nextDynamicProxy(nil); v != "" {
		t.Errorf("empty pool = %q", v)
	}
	if v := app.nextDynamicProxy(three); v != "c" { // cursor is 8; 8 % 3 == 2
		t.Errorf("after the empty read = %q, want %q", v, "c")
	}

	// A fresh App starts at the head: the cursor is per-process, like Python's
	// instance attribute, and is not persisted.
	if v := batchApp(t, nil, nil).nextDynamicProxy(three); v != "a" {
		t.Errorf("a new App started at %q, want the head", v)
	}
}

// Only 重新获取 rotates. registerDynamicProxy is the pool HEAD, because the
// batch orchestrator does the taking for every other entry point and calling
// both would advance two different cursors for one run.
func TestEntryDynamicProxyRotatesOnlyForRelink(t *testing.T) {
	app := batchApp(t, nil, nil)
	st := settings.Settings{DynamicProxies: "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p"}
	const p1 = "http://u:p@1.1.1.1:8080"
	const p2 = "http://u:p@2.2.2.2:8080"

	if got := app.entryDynamicProxy(JobRelink, st); got != p1 {
		t.Errorf("relink #1 = %q", got)
	}
	if got := app.entryDynamicProxy(JobRelink, st); got != p2 {
		t.Errorf("relink #2 = %q, want the next exit", got)
	}
	for i := 0; i < 3; i++ {
		if got := app.entryDynamicProxy(JobRegister, st); got != p1 {
			t.Errorf("register #%d = %q, want the head every time", i, got)
		}
	}
	// 全走本地代理 empties the pool for both (app.py:17601, 16719).
	localOnly := st
	localOnly.ProxyRouteMode = settings.ProxyRouteModeLocalOnly
	if got := app.entryDynamicProxy(JobRelink, localOnly); got != "" {
		t.Errorf("全走本地代理 relink = %q", got)
	}
	if got := app.entryDynamicProxy(JobRegister, localOnly); got != "" {
		t.Errorf("全走本地代理 register = %q", got)
	}
}

func TestBatchRegisterChildKindRoutesTeamSafely(t *testing.T) {
	team := models.MailAccount{Email: "team@example.com", AccountType: " TEAM "}
	free := models.MailAccount{Email: "free@example.com", AccountType: "free"}

	if got := batchRegisterChildKind(team, true); got != JobTeam {
		t.Fatalf("Team collect-session kind=%q, want %q", got, JobTeam)
	}
	if got := batchRegisterChildKind(free, true); got != JobRegister {
		t.Fatalf("Free collect-session kind=%q, want %q", got, JobRegister)
	}
	if got := batchRegisterChildKind(team, false); got != JobAuthOnly {
		t.Fatalf("Team auth-only kind=%q, want %q", got, JobAuthOnly)
	}
}

// A standalone run owns no batch pool, so the pruning has to edit the PERSISTED
// pool text directly (app.py:17824 / 17862 / 17923 / 17954). Without it,
// 重新获取 on a dead exit retries it on every click forever.
func TestDropFailedStandaloneProxyPrunesTheSavedPool(t *testing.T) {
	const poolText = "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p"
	const dead = "http://u:p@1.1.1.1:8080"

	newApp := func(t *testing.T, s map[string]any) (*App, func() map[string]any) {
		t.Helper()
		app := batchApp(t, nil, s)
		return app, func() map[string]any {
			out, _ := readJSONFile(t, app.store.StateFile)["settings"].(map[string]any)
			return out
		}
	}

	app, read := newApp(t, map[string]any{"dynamic_proxies": poolText})
	var lines []string
	app.dropFailedStandaloneProxy(dead, models.NewProxyExitCheckError("bad exit"), func(s string) { lines = append(lines, s) })
	if got, want := read()["dynamic_proxies"], "2.2.2.2:8080:u:p"; got != want {
		t.Errorf("dynamic_proxies = %q, want %q", got, want)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "失败注册代理已移除: ") {
		t.Errorf("log = %v", lines)
	}

	// Anything that is not a ProxyExitCheckError leaves the pool alone: the exit
	// is fine, the account is not.
	for _, err := range []error{nil, errors.New("boom"), &models.PhoneRequiredError{}} {
		app, read = newApp(t, map[string]any{"dynamic_proxies": poolText})
		app.dropFailedStandaloneProxy(dead, err, func(string) { t.Errorf("%v logged a removal", err) })
		if got := read()["dynamic_proxies"]; got != poolText {
			t.Errorf("%v pruned the pool: %q", err, got)
		}
	}

	// The 特殊情况 checkbox points the removal at the 第一步 pool (app.py:17408).
	app, read = newApp(t, map[string]any{
		"dynamic_proxies":             poolText,
		"payment_dynamic_proxy":       "1.1.1.1:8080:u:p\n9.9.9.9:9090:u:p",
		"register_with_payment_proxy": true,
	})
	lines = nil
	app.dropFailedStandaloneProxy(dead, models.NewProxyExitCheckError("bad exit"), func(s string) { lines = append(lines, s) })
	if got, want := read()["payment_dynamic_proxy"], "9.9.9.9:9090:u:p"; got != want {
		t.Errorf("payment_dynamic_proxy = %q, want %q", got, want)
	}
	if got := read()["dynamic_proxies"]; got != poolText {
		t.Errorf("dynamic_proxies = %q, want the register pool untouched", got)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "失败支付代理已移除: ") {
		t.Errorf("log = %v", lines)
	}

	// A proxy that is not in the pool writes nothing and logs nothing.
	app, _ = newApp(t, map[string]any{"dynamic_proxies": poolText})
	before, err := os.ReadFile(app.store.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	app.dropFailedStandaloneProxy("http://u:p@7.7.7.7:7070", models.NewProxyExitCheckError("bad exit"),
		func(string) { t.Error("an absent proxy logged a removal") })
	after, err := os.ReadFile(app.store.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("an absent proxy rewrote state.json")
	}
}

func accountEmails(accounts []models.MailAccount) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a.Email)
	}
	return out
}
