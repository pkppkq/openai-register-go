package providerproxy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// ---------------------------------------------------------------------------
// Test doubles. Nothing here dials anything: the Detector is a channel and the
// Clock is a counter, which is the whole reason both are injected — a real
// probe of a minted URL is a billed provider session.
// ---------------------------------------------------------------------------

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	// parked receives once per After call, i.e. every time some goroutine
	// starts a Condition.wait. It is how a test knows the pool has gone idle
	// without sleeping.
	parked chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		parked: make(chan time.Duration, 4096),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		c.mu.Unlock()
		return ch
	}
	c.timers = append(c.timers, &fakeTimer{deadline: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	select {
	case c.parked <- d:
	default:
	}
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var fire, keep []*fakeTimer
	for _, timer := range c.timers {
		if !timer.deadline.After(now) {
			fire = append(fire, timer)
		} else {
			keep = append(keep, timer)
		}
	}
	c.timers = keep
	c.mu.Unlock()
	for _, timer := range fire {
		select {
		case timer.ch <- now:
		default:
		}
	}
}

// driveUntil advances fake time by step every time the pool parks, until ch
// yields. The real-time arm is a failure guard only; the happy path never
// sleeps.
func driveUntil[T any](t *testing.T, clock *fakeClock, ch <-chan T, step time.Duration) T {
	t.Helper()
	guard := time.After(5 * time.Second)
	for {
		select {
		case value := <-ch:
			return value
		case <-clock.parked:
			clock.Advance(step)
		case <-guard:
			t.Fatal("timed out waiting for the pool to make progress")
		}
	}
}

// probeHarness is a Detector whose every call is observable and whose every
// result is dictated by the test.
type probeHarness struct {
	calls   chan Candidate
	replies chan string
	mints   chan int // sid sequence number, sampled at mint time under the lock
	closed  chan struct{}

	mu     sync.Mutex
	minted int
}

func newProbeHarness() *probeHarness {
	return &probeHarness{
		calls:   make(chan Candidate),
		replies: make(chan string),
		mints:   make(chan int, 256),
		closed:  make(chan struct{}),
	}
}

func (h *probeHarness) detect(_ context.Context, candidate Candidate, _ string) (string, error) {
	select {
	case h.calls <- candidate:
	case <-h.closed:
		return "", errors.New("harness closed")
	}
	select {
	case reply := <-h.replies:
		if reply == "" {
			return "", errors.New("boom")
		}
		return reply, nil
	case <-h.closed:
		return "", errors.New("harness closed")
	}
}

// sid hands out 00000001, 00000002, … It runs inside nextCandidateLocked, i.e.
// under the manager lock, so the counter is the mint order even though the
// probe goroutines that follow race freely.
func (h *probeHarness) sid() (string, error) {
	h.mu.Lock()
	h.minted++
	n := h.minted
	h.mu.Unlock()
	select {
	case h.mints <- n:
	default:
	}
	return fmt.Sprintf("%08d", n), nil
}

func (h *probeHarness) mintCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.minted
}

func (h *probeHarness) close() { close(h.closed) }

func passExit(region string) string {
	return "203.0.113.7 " + region + "/City Asia/Tokyo AS1 Org ChatGPT=200 Stripe=200"
}

func poolConfig(regions string) settings.ProviderProxyConfig {
	return settings.ProviderProxyConfig{
		Enabled:  true,
		Username: "acct",
		Password: "pw",
		Endpoint: "us2.proxy.invalid:3010",
		Duration: 5,
		Regions:  regions,
	}
}

func disabledConfig() settings.ProviderProxyConfig {
	config := poolConfig("JP")
	config.Enabled = false
	return config
}

func configs(create, followup, approve settings.ProviderProxyConfig) map[proxypool.Role]settings.ProviderProxyConfig {
	return map[proxypool.Role]settings.ProviderProxyConfig{
		proxypool.RoleCreate:   create,
		proxypool.RoleFollowup: followup,
		proxypool.RoleApprove:  approve,
	}
}

func sidOf(t *testing.T, candidate Candidate) string {
	t.Helper()
	const marker = "-sid-"
	i := indexOf(candidate.URL, marker)
	if i < 0 {
		t.Fatalf("no sid in %s", candidate.URL)
	}
	rest := candidate.URL[i+len(marker):]
	j := indexOf(rest, "-t-")
	if j < 0 {
		t.Fatalf("no -t- in %s", candidate.URL)
	}
	return rest[:j]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Minting through the pump
// ---------------------------------------------------------------------------

// TestPumpMintsPerRoleInRoleOrder covers the round-robin of app.py:1217-1221.
// The three probe goroutines race, so the assertion is on the sid sequence,
// which is handed out inside nextCandidateLocked under the manager lock.
func TestPumpMintsPerRoleInRoleOrder(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		clock := newFakeClock()
		harness := newProbeHarness()
		manager := New(harness.detect,
			WithClock(clock), WithSIDSource(harness.sid),
			WithStock(50, 0), WithMaxWorkers(3))
		if err := manager.Configure(configs(poolConfig("JP"), poolConfig("US"), poolConfig("BR")), ""); err != nil {
			t.Fatalf("Configure: %v", err)
		}

		bySID := make(map[string]Candidate, 3)
		for i := 0; i < 3; i++ {
			candidate := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
			bySID[sidOf(t, candidate)] = candidate
		}
		want := []struct {
			sid    string
			role   proxypool.Role
			region string
		}{
			{"00000001", proxypool.RoleCreate, "JP"},
			{"00000002", proxypool.RoleFollowup, "US"},
			{"00000003", proxypool.RoleApprove, "BR"},
		}
		for _, w := range want {
			got, ok := bySID[w.sid]
			if !ok {
				t.Fatalf("attempt %d: sid %s never minted; got %v", attempt, w.sid, bySID)
			}
			if got.Role != w.role {
				t.Fatalf("attempt %d: sid %s minted for %q, want %q (app.py:293 order)", attempt, w.sid, got.Role, w.role)
			}
			if got.Region != w.region {
				t.Fatalf("attempt %d: sid %s region %q, want %q", attempt, w.sid, got.Region, w.region)
			}
		}
		harness.close()
		manager.Stop()
	}
}

// TestRegionRoundRobin covers _next_candidate_locked (app.py:1201-1206): the
// per-role cursor walks the configured regions in order and wraps. maxWorkers
// is 1 so mints are strictly serialised.
func TestRegionRoundRobin(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(50, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP,US,BR"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	want := []string{"JP", "US", "BR", "JP", "US"}
	for i, region := range want {
		candidate := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
		if candidate.Region != region {
			t.Fatalf("mint %d region = %q, want %q", i+1, candidate.Region, region)
		}
		if candidate.Role != proxypool.RoleCreate {
			t.Fatalf("mint %d role = %q, want create (the only enabled role)", i+1, candidate.Role)
		}
		// A fresh sid per mint: a new provider session every time (app.py:996).
		if got := sidOf(t, candidate); got != fmt.Sprintf("%08d", i+1) {
			t.Fatalf("mint %d sid = %q, want %08d", i+1, got, i+1)
		}
		harness.replies <- passExit(region)
	}
}

// TestRegionCursorAdvancesOnFailedMint pins app.py:1206: the cursor moves for
// every mint, including the ones whose probe fails, so a bad region does not
// pin the pool to itself.
func TestRegionCursorAdvancesOnFailedMint(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(50, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP,US"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	first := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
	if first.Region != "JP" {
		t.Fatalf("first region = %q, want JP", first.Region)
	}
	harness.replies <- "检测失败[出口]: HTTP 429"

	second := driveUntil(t, clock, harness.calls, 1500*time.Millisecond)
	if second.Region != "US" {
		t.Fatalf("second region = %q, want US", second.Region)
	}
	harness.replies <- passExit("US")
}

// TestDisabledRolesAreNeverMinted covers app.py:1224 and take()'s early return
// at app.py:1175-1176.
func TestDisabledRolesAreNeverMinted(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(3, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(disabledConfig(), poolConfig("JP"), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	if got := manager.EnabledRoles(); len(got) != 1 || got[0] != proxypool.RoleFollowup {
		t.Fatalf("EnabledRoles = %v, want [followup]", got)
	}
	for i := 0; i < 3; i++ {
		candidate := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
		if candidate.Role != proxypool.RoleFollowup {
			t.Fatalf("minted for disabled role %q", candidate.Role)
		}
		harness.replies <- passExit("JP")
	}
	// A disabled role short-circuits before it can wait, so a 60 s budget costs
	// no time at all (app.py:1175-1176).
	if _, ok := manager.Take(proxypool.RoleCreate, TakeTimeout, nil); ok {
		t.Fatal("Take on a disabled role returned a candidate")
	}
	if _, ok := manager.Take(proxypool.RoleApprove, TakeTimeout, nil); ok {
		t.Fatal("Take on a disabled role returned a candidate")
	}
	if got := manager.ReadyCount(proxypool.RoleCreate); got != 0 {
		t.Fatalf("disabled role stocked %d candidates", got)
	}
}

// TestEnabledRolesOrderIsStable guards the map-iteration trap: the answer must
// be Roles order, every time.
func TestEnabledRolesOrderIsStable(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), poolConfig("JP"), poolConfig("JP")), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	manager.Stop() // Configure starts the pump; nothing may mint in this test
	want := []proxypool.Role{proxypool.RoleCreate, proxypool.RoleFollowup, proxypool.RoleApprove}
	for i := 0; i < 200; i++ {
		got := manager.EnabledRoles()
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("EnabledRoles = %v, want %v", got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Rotation timing: the backoff, the only clock-driven gate in the pool
// ---------------------------------------------------------------------------

// TestFailureBackoffGatesTheNextMint covers app.py:1234 and app.py:1269-1272.
// The clock is frozen while the assertion runs: a mint is impossible until it
// moves, which is what makes the "no extra mint" check sound without a sleep.
func TestFailureBackoffGatesTheNextMint(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	statuses := make(chan Status, 64)
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(50, 0), WithMaxWorkers(1),
		WithStatusCallback(func(role proxypool.Role, status Status) {
			if role == proxypool.RoleCreate {
				select {
				case statuses <- status:
				default:
				}
			}
		}))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	first := driveUntil(t, clock, harness.calls, 0)
	if first.Region != "JP" {
		t.Fatalf("region = %q", first.Region)
	}
	// A probe that comes back from the wrong country is a failure even though
	// it succeeded technically (app.py:1254).
	harness.replies <- passExit("US")

	for {
		status := driveUntil(t, clock, statuses, 0)
		if status.Failures == 1 {
			break
		}
	}
	if got := harness.mintCount(); got != 1 {
		t.Fatalf("minted %d times during the 1 s backoff with a frozen clock, want 1", got)
	}

	// 999 ms is inside the first backoff window; still no mint.
	clock.Advance(999 * time.Millisecond)
	if got := harness.mintCount(); got != 1 {
		t.Fatalf("minted %d times at t+999ms, want 1 (backoff is 1 s, app.py:292)", got)
	}

	clock.Advance(2 * time.Millisecond)
	second := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
	if second.Region != "JP" {
		t.Fatalf("second region = %q, want JP (one region configured)", second.Region)
	}
	harness.replies <- passExit("JP")

	for {
		status := driveUntil(t, clock, statuses, 250*time.Millisecond)
		if status.Ready == 1 {
			if status.Failures != 0 {
				t.Fatalf("a pass must reset the failure count (app.py:1262); got %d", status.Failures)
			}
			break
		}
	}
}

// TestBackoffTable walks PROVIDER_PROXY_BACKOFF_SECONDS (app.py:292) including
// the clamp at app.py:1271. completeCheck is driven directly so that no session
// is minted at all.
func TestBackoffTable(t *testing.T) {
	clock := newFakeClock()
	manager := New(nil, WithClock(clock), WithStock(50, 0))
	// Deliberately NOT started: this exercises the completion path alone.
	manager.configs[proxypool.RoleCreate] = poolConfig("JP")

	want := []time.Duration{1, 2, 5, 10, 30, 30, 30}
	for i, seconds := range want {
		manager.completeCheck(
			Candidate{Role: proxypool.RoleCreate, Region: "JP"},
			manager.generation[proxypool.RoleCreate],
			"检测失败: boom")
		status := manager.Snapshot(proxypool.RoleCreate)
		if status.Failures != i+1 {
			t.Fatalf("failure %d: count = %d", i+1, status.Failures)
		}
		got := manager.nextAllowed[proxypool.RoleCreate].Sub(clock.Now())
		if got != seconds*time.Second {
			t.Fatalf("failure %d: backoff = %s, want %ds", i+1, got, seconds)
		}
	}

	// A pass clears both the counter and the gate (app.py:1261-1263).
	manager.completeCheck(
		Candidate{Role: proxypool.RoleCreate, Region: "JP"},
		manager.generation[proxypool.RoleCreate],
		passExit("JP"))
	if status := manager.Snapshot(proxypool.RoleCreate); status.Failures != 0 || status.Ready != 1 {
		t.Fatalf("after a pass: %+v", status)
	}
	if !manager.nextAllowed[proxypool.RoleCreate].IsZero() {
		t.Fatal("a pass must clear _next_allowed (app.py:1263)")
	}
}

// TestCompleteCheckGeneration covers app.py:1259: a result minted under an old
// configuration is dropped, but its inflight decrement still lands
// (app.py:1258).
func TestCompleteCheckGeneration(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()), WithStock(50, 0))
	manager.configs[proxypool.RoleCreate] = poolConfig("JP")
	manager.inflight[proxypool.RoleCreate] = 1
	manager.generation[proxypool.RoleCreate] = 7

	manager.completeCheck(Candidate{Role: proxypool.RoleCreate, Region: "JP"}, 6, passExit("JP"))
	status := manager.Snapshot(proxypool.RoleCreate)
	if status.Ready != 0 {
		t.Fatalf("a stale generation must not be stocked; ready = %d", status.Ready)
	}
	if status.Inflight != 0 {
		t.Fatalf("inflight = %d, want 0 (app.py:1258 decrements unconditionally)", status.Inflight)
	}
	// The floor at 0 (max(0, x-1)) holds for a second stale completion.
	manager.completeCheck(Candidate{Role: proxypool.RoleCreate, Region: "JP"}, 6, passExit("JP"))
	if got := manager.Snapshot(proxypool.RoleCreate).Inflight; got != 0 {
		t.Fatalf("inflight = %d, want 0", got)
	}
}

// TestCompleteCheckFullQueue covers the branch shape at app.py:1259-1274: a
// pass that arrives at a full queue is neither stocked nor counted as a
// failure, and refilling is switched off.
func TestCompleteCheckFullQueue(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()), WithStock(1, 0))
	manager.configs[proxypool.RoleCreate] = poolConfig("JP")
	manager.refilling[proxypool.RoleCreate] = true

	manager.completeCheck(Candidate{Role: proxypool.RoleCreate, Region: "JP"}, 0, passExit("JP"))
	manager.completeCheck(Candidate{Role: proxypool.RoleCreate, Region: "JP"}, 0, passExit("JP"))
	status := manager.Snapshot(proxypool.RoleCreate)
	if status.Ready != 1 {
		t.Fatalf("ready = %d, want the target of 1", status.Ready)
	}
	if status.Failures != 0 {
		t.Fatalf("failures = %d, want 0 — a pass at a full queue is not a failure", status.Failures)
	}
	if manager.refilling[proxypool.RoleCreate] {
		t.Fatal("refilling must be cleared once the target is reached (app.py:1273)")
	}
}

// ---------------------------------------------------------------------------
// Take / WaitUntilReady
// ---------------------------------------------------------------------------

func TestTakeReturnsStockAndReArmsRefill(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(4, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	first := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
	harness.replies <- passExit("JP")

	type takeResult struct {
		candidate Candidate
		ok        bool
	}
	results := make(chan takeResult, 1)
	go func() {
		candidate, ok := manager.Take(proxypool.RoleCreate, TakeTimeout, nil)
		results <- takeResult{candidate, ok}
	}()
	var got takeResult
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Take to receive validated stock")
	}
	if !got.ok {
		t.Fatal("Take returned nothing")
	}
	if got.candidate.URL != first.URL {
		t.Fatalf("Take returned %s, want the minted %s", got.candidate.URL, first.URL)
	}
	if got.candidate.ProxyExit != passExit("JP") {
		t.Fatalf("candidate lost its exit string: %q", got.candidate.ProxyExit)
	}
	if n := manager.ReadyCount(proxypool.RoleCreate); n != 0 {
		t.Fatalf("ready = %d after Take, want 0 — a candidate must never be handed out twice", n)
	}
	if !manager.refilling[proxypool.RoleCreate] {
		t.Fatal("dropping to the low-water mark must re-arm refilling (app.py:1185)")
	}
}

// TestTakeTimesOut covers app.py:1177-1183: an empty pool costs the caller the
// full budget and then falls through (app.py:23366-23376 goes to the manual
// pool).
func TestTakeTimesOut(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(4, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	// Hold the one probe open so the pool stays empty.
	<-harness.calls

	done := make(chan bool, 1)
	start := clock.Now()
	go func() {
		_, ok := manager.Take(proxypool.RoleCreate, TakeTimeout, nil)
		done <- ok
	}()
	if ok := driveUntil(t, clock, done, 5*time.Second); ok {
		t.Fatal("Take returned a candidate from an empty pool")
	}
	if elapsed := clock.Now().Sub(start); elapsed < TakeTimeout {
		t.Fatalf("Take gave up after %s, want at least %s (app.py:291)", elapsed, TakeTimeout)
	}
}

// TestTakeStopsOnCallerSignal covers the stop_event arm of app.py:1178.
func TestTakeStopsOnCallerSignal(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(4, 0), WithMaxWorkers(1))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	<-harness.calls

	stop := make(chan struct{})
	close(stop)
	if _, ok := manager.Take(proxypool.RoleCreate, TakeTimeout, stop); ok {
		t.Fatal("Take ignored the caller's stop signal")
	}
}

// TestWaitUntilReady covers app.py:1160-1170, including the Python truthiness
// of `roles or PROVIDER_PROXY_ROLES` at app.py:1164.
func TestWaitUntilReady(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()), WithStock(50, 0))
	manager.configs[proxypool.RoleCreate] = poolConfig("JP")
	manager.configs[proxypool.RoleFollowup] = disabledConfig()
	manager.configs[proxypool.RoleApprove] = disabledConfig()

	// Disabled roles never hold it up.
	if !manager.WaitUntilReady(0, nil, nil) {
		t.Fatal("minimum 0 must be satisfied immediately")
	}
	manager.ready[proxypool.RoleCreate] = []Candidate{{Role: proxypool.RoleCreate}, {Role: proxypool.RoleCreate}}
	if !manager.WaitUntilReady(2, nil, nil) {
		t.Fatal("two in stock must satisfy a minimum of 2")
	}
	// An empty roles slice means every role, not no role.
	if !manager.WaitUntilReady(2, nil, []proxypool.Role{}) {
		t.Fatal("an empty roles slice must mean all roles (app.py:1164)")
	}
	// Asking only about a disabled role short-circuits: all([]) is True.
	if !manager.WaitUntilReady(500, nil, []proxypool.Role{proxypool.RoleApprove}) {
		t.Fatal("a disabled role must never block wait_until_ready")
	}
	stop := make(chan struct{})
	close(stop)
	if manager.WaitUntilReady(3, stop, nil) {
		t.Fatal("WaitUntilReady must report false once the caller stops")
	}
}

// ---------------------------------------------------------------------------
// Configure / Stop
// ---------------------------------------------------------------------------

// TestConfigureRejectsInvalidConfig mirrors app.py:1128 — the raise propagates
// out of configure, and the roles processed before it stay applied.
func TestConfigureRejectsInvalidConfig(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()))
	defer manager.Stop()
	broken := poolConfig("JP")
	broken.Endpoint = "not-a-host-port"
	err := manager.Configure(configs(poolConfig("JP"), broken, poolConfig("JP")), "")
	if err == nil {
		t.Fatal("Configure accepted an endpoint without a port")
	}
	if err.Error() != "主机端口格式应为 hostname:port" {
		t.Fatalf("Configure error = %q", err.Error())
	}
	if manager.Running() {
		t.Fatal("a failed Configure must not start the pump (app.py:1138 is never reached)")
	}
	// app.py applies role by role and aborts mid-loop.
	if !manager.Config(proxypool.RoleCreate).Enabled {
		t.Fatal("the role processed before the failure should have been applied")
	}
	if manager.Config(proxypool.RoleApprove).Enabled {
		t.Fatal("the role after the failure must not have been applied")
	}
}

// TestConfigureDropsStockWhenTheLocalProxyChanges covers app.py:1123-1136: a
// different first hop means the stocked sessions would exit somewhere else.
func TestConfigureDropsStockWhenTheLocalProxyChanges(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()), WithStock(50, 0))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), "127.0.0.1:7890"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	manager.Stop()
	// normalize_proxy_url is applied on the way in (app.py:1123).
	if got := manager.LocalProxy(); got != "http://127.0.0.1:7890" {
		t.Fatalf("LocalProxy = %q, want the normalized form", got)
	}
	manager.ready[proxypool.RoleCreate] = []Candidate{{Role: proxypool.RoleCreate}}
	manager.regionIndex[proxypool.RoleCreate] = 5
	manager.failures[proxypool.RoleCreate] = 3
	generation := manager.generation[proxypool.RoleCreate]

	// Same config, same proxy: nothing is invalidated.
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), "http://127.0.0.1:7890"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	manager.Stop()
	if len(manager.ready[proxypool.RoleCreate]) != 1 || manager.generation[proxypool.RoleCreate] != generation {
		t.Fatal("an unchanged config must keep its stock (app.py:1129)")
	}

	// A different local proxy invalidates every role.
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), "socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	manager.Stop()
	if len(manager.ready[proxypool.RoleCreate]) != 0 {
		t.Fatal("a changed local proxy must drop the stock")
	}
	if manager.generation[proxypool.RoleCreate] != generation+1 {
		t.Fatalf("generation = %d, want %d", manager.generation[proxypool.RoleCreate], generation+1)
	}
	if manager.regionIndex[proxypool.RoleCreate] != 0 || manager.failures[proxypool.RoleCreate] != 0 {
		t.Fatal("the region cursor and failure count must reset with the generation")
	}
	// socks5:// must have been normalized to socks5h:// — otherwise the probe
	// resolves DNS locally.
	if got := manager.LocalProxy(); got != "socks5h://127.0.0.1:1080" {
		t.Fatalf("LocalProxy = %q, want socks5h://127.0.0.1:1080", got)
	}
}

// TestUpdateMaxWorkersInvalidatesInflight covers app.py:1104-1119.
func TestUpdateMaxWorkersInvalidatesInflight(t *testing.T) {
	manager := New(nil, WithClock(newFakeClock()), WithMaxWorkers(4))
	manager.configs[proxypool.RoleCreate] = poolConfig("JP")
	manager.inflight[proxypool.RoleCreate] = 3
	generation := manager.generation[proxypool.RoleCreate]

	manager.UpdateMaxWorkers(4) // unchanged: a no-op (app.py:1108)
	if manager.generation[proxypool.RoleCreate] != generation || manager.inflight[proxypool.RoleCreate] != 3 {
		t.Fatal("an unchanged worker count must not disturb anything")
	}
	manager.UpdateMaxWorkers(0) // max(1, …) — app.py:1105
	if manager.maxWorkers != 1 {
		t.Fatalf("maxWorkers = %d, want 1", manager.maxWorkers)
	}
	if manager.generation[proxypool.RoleCreate] != generation+1 {
		t.Fatal("changing the worker count must bump every generation")
	}
	if manager.inflight[proxypool.RoleCreate] != 0 {
		t.Fatal("changing the worker count must zero inflight (app.py:1113)")
	}
	// The orphaned probe still lands and must not push inflight negative.
	manager.completeCheck(Candidate{Role: proxypool.RoleCreate, Region: "JP"}, generation, passExit("JP"))
	if got := manager.Snapshot(proxypool.RoleCreate); got.Inflight != 0 || got.Ready != 0 {
		t.Fatalf("orphaned completion: %+v", got)
	}
}

// TestStopHaltsThePump covers stop() (app.py:1091-1102) — the path app.py takes
// when the route mode flips to 全走本地代理 (app.py:16719).
func TestStopHaltsThePump(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(50, 0), WithMaxWorkers(1))
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !manager.Running() {
		t.Fatal("Configure must start the pump (app.py:1138)")
	}
	candidate := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
	harness.replies <- passExit(candidate.Region)

	manager.Stop()
	if manager.Running() {
		t.Fatal("Stop must join the pump (app.py:1095-1102)")
	}
	// The pump goroutine is gone, so the mint count can no longer move.
	minted := harness.mintCount()
	clock.Advance(time.Minute)
	if got := harness.mintCount(); got != minted {
		t.Fatalf("minted %d more sessions after Stop", got-minted)
	}
	// An empty pool no longer waits at all once stopped (app.py:1178).
	if _, ok := manager.Take(proxypool.RoleFollowup, TakeTimeout, nil); ok {
		t.Fatal("Take succeeded after Stop")
	}
	start := clock.Now()
	if _, ok := manager.Take(proxypool.RoleCreate, TakeTimeout, nil); ok {
		if manager.ReadyCount(proxypool.RoleCreate) != 0 {
			t.Fatal("Take after Stop returned stock that should have been drained")
		}
	}
	if clock.Now() != start {
		t.Fatal("a stopped pool must not make Take wait")
	}

	// Stop is idempotent, and safe on a manager that never started.
	manager.Stop()
	New(nil, WithClock(clock)).Stop()

	// Start clears the stop flag again (app.py:1086).
	manager.Start()
	if !manager.Running() {
		t.Fatal("Start after Stop must revive the pump")
	}
	manager.Stop()
}

// TestPumpProbeErrorsBecomeFailureText covers app.py:1250-1252.
func TestPumpProbeErrorsBecomeFailureText(t *testing.T) {
	clock := newFakeClock()
	harness := newProbeHarness()
	defer harness.close()
	captured := make(chan Candidate, 8)
	manager := New(harness.detect,
		WithClock(clock), WithSIDSource(harness.sid),
		WithStock(50, 0), WithMaxWorkers(1),
		WithValidatedCallback(func(candidate Candidate) { captured <- candidate }))
	defer manager.Stop()
	if err := manager.Configure(configs(poolConfig("JP"), disabledConfig(), disabledConfig()), ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	<-harness.calls
	harness.replies <- "" // the harness turns this into an error
	// Nothing may be stocked; the next mint is gated by the 1 s backoff.
	for {
		status := manager.Snapshot(proxypool.RoleCreate)
		if status.Failures == 1 {
			break
		}
		select {
		case candidate := <-captured:
			t.Fatalf("an errored probe was stocked: %+v", candidate)
		case <-clock.parked:
		}
	}
	if got := manager.ReadyCount(proxypool.RoleCreate); got != 0 {
		t.Fatalf("ready = %d after an errored probe", got)
	}

	// A passing probe reaches validated_callback with its exit attached
	// (app.py:1265).
	clock.Advance(2 * time.Second)
	next := driveUntil(t, clock, harness.calls, 250*time.Millisecond)
	harness.replies <- passExit(next.Region)
	stocked := driveUntil(t, clock, captured, 250*time.Millisecond)
	if stocked.ProxyExit != passExit(next.Region) {
		t.Fatalf("validated candidate exit = %q", stocked.ProxyExit)
	}
	if stocked.URL != next.URL {
		t.Fatalf("validated candidate URL = %s, want %s", stocked.URL, next.URL)
	}
}

// TestConfigsFromSettings checks the settings bridge, including the default
// used for a role the snapshot omits (app.py:1127).
func TestConfigsFromSettings(t *testing.T) {
	got := ConfigsFromSettings(map[string]settings.ProviderProxyConfig{
		"create": poolConfig("JP"),
	})
	if len(got) != len(Roles) {
		t.Fatalf("got %d roles, want %d", len(got), len(Roles))
	}
	if !got[proxypool.RoleCreate].Enabled {
		t.Fatal("create should have come through")
	}
	if got[proxypool.RoleApprove] != settings.DefaultProviderProxyConfig() {
		t.Fatalf("missing role = %+v, want the dataclass defaults", got[proxypool.RoleApprove])
	}
}
