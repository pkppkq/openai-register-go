package batch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// ---------------------------------------------------------------------------
// test doubles. No real Runner is ever constructed here: the injected ones do
// arithmetic and nothing else, so this file cannot spend money or open a
// socket.
// ---------------------------------------------------------------------------

// listSource is a FIFO of proxies. Take reports !ok when the list is empty,
// which is queue.Empty at app.py:23656.
type listSource struct {
	mu    sync.Mutex
	items []string
	taken []string
	recyc []string
	// rotate mirrors proxypool.Pool.TakeN (app.py:17316): the entry goes back
	// to the tail immediately, so the pool never runs dry.
	rotate bool
}

func (s *listSource) Take(context.Context) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "", false
	}
	head := s.items[0]
	s.items = s.items[1:]
	if s.rotate {
		s.items = append(s.items, head)
	}
	s.taken = append(s.taken, head)
	return head, true
}

func (s *listSource) Recycle(proxy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recyc = append(s.recyc, proxy)
	s.items = append(s.items, proxy)
}

func (s *listSource) takenList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.taken...)
}

func (s *listSource) recycledList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recyc...)
}

// noRecycleSource is a listSource without the ProxyRecycler method, so a used
// proxy is gone for good.
type noRecycleSource struct{ inner listSource }

func (s *noRecycleSource) Take(ctx context.Context) (string, bool) { return s.inner.Take(ctx) }

func jobs(keys ...string) []Job[int] {
	out := make([]Job[int], len(keys))
	for i, key := range keys {
		out[i] = Job[int]{Key: key, Payload: i}
	}
	return out
}

func namedJobs(n int) []Job[int] {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("a%02d@example.com", i)
	}
	return jobs(keys...)
}

// errRetryable stands in for a falsy _generate_opll_link_for_account result
// (app.py:23634) or a 代理检测失败 precheck error (app.py:17705): the kind of
// failure that earns a fresh proxy.
var errRetryable = errors.New("test: retryable attempt failure")

func retryableOnly(err error) Disposition {
	if errors.Is(err, errRetryable) {
		return Retry
	}
	return Fail
}

func fastLink[P any]() Options[int, P] {
	return Options[int, P]{Messages: LinkMessages(), DisableRetryDelay: true, Classify: retryableOnly}
}

// ---------------------------------------------------------------------------
// clamping (app.py:17622-17629, 17029-17033, 12473-12477)
// ---------------------------------------------------------------------------

func TestClampAuthConcurrency(t *testing.T) {
	cases := []struct{ value, jobs, want int }{
		{0, 10, 1},     // max(1, 0)
		{-5, 10, 1},    // max(1, -5)
		{7, 10, 7},     //
		{999, 10, 10},  // capped by the job count, not by 30
		{999, 100, 30}, // MAX_AUTH_CONCURRENCY
		{10, 3, 3},     // max(1, len(accounts))
		{10, 0, 1},     // empty selection still yields one worker
		{settings.DefaultAuthConcurrency, 50, settings.DefaultAuthConcurrency},
	}
	for _, c := range cases {
		if got := ClampAuthConcurrency(c.value, c.jobs); got != c.want {
			t.Errorf("ClampAuthConcurrency(%d, %d) = %d, want %d", c.value, c.jobs, got, c.want)
		}
	}
}

func TestClampRaceAndAttemptLimit(t *testing.T) {
	raceCases := map[int]int{0: 1, -3: 1, 1: 1, 30: 30, 50: settings.MaxLinkRaceConcurrency}
	for in, want := range raceCases {
		if got := ClampRaceConcurrency(in); got != want {
			t.Errorf("ClampRaceConcurrency(%d) = %d, want %d", in, got, want)
		}
	}
	attemptCases := map[int]int{0: 1, -1: 1, 3: 3, 10000: 10000, 99999: settings.MaxLinkAttemptLimit}
	for in, want := range attemptCases {
		if got := ClampAttemptLimit(in); got != want {
			t.Errorf("ClampAttemptLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCapConcurrencyByProxyPool(t *testing.T) {
	if got := CapConcurrencyByProxyPool(10, 3); got != 3 {
		t.Errorf("cap by pool: got %d want 3", got)
	}
	if got := CapConcurrencyByProxyPool(10, 0); got != 10 {
		t.Errorf("unknown pool size must not cap: got %d want 10", got)
	}
	if got := CapConcurrencyByProxyPool(2, 9); got != 2 {
		t.Errorf("a bigger pool must not raise the window: got %d want 2", got)
	}
}

func TestConcurrencyCapLoggedThenWindow(t *testing.T) {
	var lines []string
	src := &listSource{items: []string{"p1", "p2"}, rotate: true}
	opts := Options[int, string]{
		Concurrency:       10,
		ProxyPoolSize:     2,
		Proxies:           src,
		Messages:          AuthMessages(),
		DisableRetryDelay: true,
		Log:               func(_, message string) { lines = append(lines, message) },
	}
	report := Run(context.Background(), namedJobs(6), RunnerFunc[int, string](func(context.Context, Job[int], string) error {
		return nil
	}), opts)
	if report.Concurrency != 2 {
		t.Fatalf("concurrency: got %d want 2", report.Concurrency)
	}
	want := []string{
		"认证并发已按代理池数量自动降到 2（原设置 6；避免同一代理被多个账号同时挤爆）",
		"注册/登录认证并发窗口数: 2",
	}
	if len(lines) < 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("log lines = %q, want prefix %q", lines, want)
	}
}

// ---------------------------------------------------------------------------
// bounded concurrency (app.py:17638-17661)
// ---------------------------------------------------------------------------

func TestConcurrencyIsBounded(t *testing.T) {
	const window = 4
	const total = 20

	var inFlight, peak atomic.Int32
	reached := make(chan struct{})
	var once sync.Once

	runner := RunnerFunc[int, string](func(ctx context.Context, _ Job[int], _ string) error {
		cur := inFlight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		if cur == window {
			once.Do(func() { close(reached) })
		}
		// Hold until the window is provably full, so a too-small window fails
		// loudly (via the timeout) instead of passing by accident.
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
		}
		inFlight.Add(-1)
		return nil
	})

	opts := fastLink[string]()
	opts.Concurrency = window
	report := Run(context.Background(), namedJobs(total), runner, opts)

	if got := peak.Load(); got > window {
		t.Fatalf("observed %d attempts in flight, window is %d", got, window)
	} else if got != window {
		t.Fatalf("observed peak %d, want exactly the window %d", got, window)
	}
	if report.Concurrency != window {
		t.Fatalf("report.Concurrency = %d, want %d", report.Concurrency, window)
	}
	if c := report.Counts(); c.Succeeded != total {
		t.Fatalf("counts = %+v, want %d succeeded", c, total)
	}
}

func TestRaceMultipliesAttemptsNotAccounts(t *testing.T) {
	// Concurrency bounds accounts; Race bounds lanes per account. Tk does the
	// same (app.py:23678-23683 spawns race lanes inside one account thread).
	var mu sync.Mutex
	lanes := map[string]int{}
	accountsInFlight, accountPeak, lanePeak := 0, 0, 0

	runner := RunnerFunc[int, string](func(ctx context.Context, job Job[int], _ string) error {
		mu.Lock()
		lanes[job.Key]++
		if lanes[job.Key] == 1 {
			accountsInFlight++
			accountPeak = max(accountPeak, accountsInFlight)
		}
		lanePeak = max(lanePeak, lanes[job.Key])
		mu.Unlock()

		time.Sleep(2 * time.Millisecond)

		mu.Lock()
		lanes[job.Key]--
		if lanes[job.Key] == 0 {
			accountsInFlight--
		}
		mu.Unlock()
		return errRetryable
	})

	src := &listSource{items: []string{}, rotate: true}
	for i := range 200 {
		src.items = append(src.items, fmt.Sprintf("p%d", i))
	}
	opts := fastLink[string]()
	opts.Concurrency = 2
	opts.Race = 3
	opts.AttemptLimit = 3
	opts.Proxies = src
	Run(context.Background(), namedJobs(6), runner, opts)

	mu.Lock()
	defer mu.Unlock()
	if accountPeak > 2 {
		t.Fatalf("accounts in flight peaked at %d, window is 2", accountPeak)
	}
	if lanePeak > 3 {
		t.Fatalf("lanes per account peaked at %d, race is 3", lanePeak)
	}
	if lanePeak < 2 {
		t.Fatalf("lanes per account peaked at %d, want the race to actually run lanes in parallel", lanePeak)
	}
}

// ---------------------------------------------------------------------------
// retry with a FRESH proxy (app.py:17681-17708 / 23624-23644)
// ---------------------------------------------------------------------------

func TestRetryConsumesAFreshProxyEachAttempt(t *testing.T) {
	src := &noRecycleSource{inner: listSource{items: []string{"p1", "p2", "p3", "p4"}}}
	var seen []string
	var mu sync.Mutex

	runner := RunnerFunc[int, string](func(_ context.Context, _ Job[int], proxy string) error {
		mu.Lock()
		seen = append(seen, proxy)
		n := len(seen)
		mu.Unlock()
		if n < 3 {
			return errRetryable
		}
		return nil
	})

	opts := fastLink[string]()
	opts.AttemptLimit = 5
	opts.Proxies = src
	report := Run(context.Background(), jobs("one@example.com"), runner, opts)

	res := report.Results[0]
	if res.Outcome != OutcomeSucceeded {
		t.Fatalf("outcome = %s (%v), want succeeded", res.Outcome, res.Err)
	}
	if res.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", res.Attempts)
	}
	want := []string{"p1", "p2", "p3"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("proxies used = %v, want %v (a fresh one per attempt)", seen, want)
	}
	if got := src.inner.takenList(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("proxies taken from the source = %v, want %v", got, want)
	}
	if counts := report.AttemptCounts(); counts["one@example.com"] != 3 {
		t.Fatalf("link_attempt_counts = %v, want 3", counts)
	}
}

func TestAttemptEventsMirrorLinkAttemptCounter(t *testing.T) {
	var mu sync.Mutex
	var events []int
	src := &listSource{items: []string{"p1", "p2", "p3"}, rotate: true}
	opts := fastLink[string]()
	opts.AttemptLimit = 3
	opts.Proxies = src
	opts.OnAttempt = func(_ string, count int) {
		mu.Lock()
		events = append(events, count)
		mu.Unlock()
	}
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error { return errRetryable }), opts)

	if fmt.Sprint(events) != "[1 2 3]" {
		t.Fatalf("link-attempt events = %v, want [1 2 3]", events)
	}
	res := report.Results[0]
	if res.Outcome != OutcomeAttemptsExhausted || res.Status != "提取长链失败" {
		t.Fatalf("outcome = %s status = %q, want attempts-exhausted / 提取长链失败", res.Outcome, res.Status)
	}
	if !errors.Is(res.Err, ErrAttemptsExhausted) {
		t.Fatalf("err = %v, want ErrAttemptsExhausted", res.Err)
	}
}

func TestRaceRoundAttemptAccounting(t *testing.T) {
	// app.py:23653 wants min(race, limit-attempt) and app.py:23663 adds the
	// whole batch, so race=3 limit=5 spends 3 then 2.
	src := &listSource{items: []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"}}
	var rounds []int
	var mu sync.Mutex
	var inRound atomic.Int32

	runner := RunnerFunc[int, string](func(context.Context, Job[int], string) error {
		inRound.Add(1)
		return errRetryable
	})
	opts := fastLink[string]()
	opts.Race = 3
	opts.AttemptLimit = 5
	opts.Proxies = src
	opts.Log = func(_, message string) {
		mu.Lock()
		rounds = append(rounds, len(message))
		mu.Unlock()
	}
	report := Run(context.Background(), jobs("one@example.com"), runner, opts)

	if got := report.Results[0].Attempts; got != 5 {
		t.Fatalf("attempts = %d, want exactly the limit 5", got)
	}
	if got := len(src.takenList()); got != 5 {
		t.Fatalf("proxies taken = %d, want 5 (never more than the attempt limit)", got)
	}
	if report.Results[0].Outcome != OutcomeAttemptsExhausted {
		t.Fatalf("outcome = %s, want attempts-exhausted", report.Results[0].Outcome)
	}
}

func TestNoProxySourceGivesExactlyOneAttempt(t *testing.T) {
	// app.py:17689-17692: without a pool, attempts > 0 returns immediately.
	var calls atomic.Int32
	opts := fastLink[string]()
	opts.AttemptLimit = 9
	opts.Race = 5
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(_ context.Context, _ Job[int], proxy string) error {
			calls.Add(1)
			if proxy != "" {
				t.Errorf("proxy = %q, want the zero value with no pool", proxy)
			}
			return errRetryable
		}), opts)

	if got := calls.Load(); got != 1 {
		t.Fatalf("runner called %d times, want 1", got)
	}
	if res := report.Results[0]; res.Outcome != OutcomeAttemptsExhausted || res.Attempts != 1 {
		t.Fatalf("result = %+v, want one attempt then attempts-exhausted", res)
	}
}

func TestFailStopsTheAccountAndSetsExceptionStatus(t *testing.T) {
	src := &listSource{items: []string{"p1", "p2", "p3"}}
	fatal := models.NewPhoneRejectedError("no stock")
	var calls atomic.Int32
	opts := fastLink[string]()
	opts.AttemptLimit = 5
	opts.Proxies = src
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error {
			calls.Add(1)
			return fatal
		}), opts)

	if got := calls.Load(); got != 1 {
		t.Fatalf("runner called %d times, want 1: a Fail ends the account", got)
	}
	res := report.Results[0]
	if res.Outcome != OutcomeFailed || !errors.Is(res.Err, fatal) {
		t.Fatalf("result = %+v, want failed with the runner error", res)
	}
	if want := models.ExceptionStatus(fatal, StatusFailed); res.Status != want {
		t.Fatalf("status = %q, want %q", res.Status, want)
	}
	// app.py:23701-23702 returns the whole round to the queue.
	if got := src.recycledList(); fmt.Sprint(got) != "[p1]" {
		t.Fatalf("recycled = %v, want [p1]", got)
	}
}

func TestRecyclingFollowsPythonRequeueRules(t *testing.T) {
	// Two lanes, one wins: only the loser goes back (app.py:23696-23697).
	src := &listSource{items: []string{"p1", "p2"}}
	opts := fastLink[string]()
	opts.Race = 2
	opts.AttemptLimit = 2
	opts.Proxies = src
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(_ context.Context, _ Job[int], proxy string) error {
			if proxy == "p2" {
				return nil
			}
			return errRetryable
		}), opts)

	if report.Results[0].Outcome != OutcomeSucceeded {
		t.Fatalf("outcome = %s, want succeeded", report.Results[0].Outcome)
	}
	if got := src.recycledList(); fmt.Sprint(got) != "[p1]" {
		t.Fatalf("recycled = %v, want only the losing lane [p1]", got)
	}
}

// ---------------------------------------------------------------------------
// 代理耗尽 (app.py:17683-17688 / 23658-23661)
// ---------------------------------------------------------------------------

func TestProxyExhaustionIsADistinctCondition(t *testing.T) {
	src := &noRecycleSource{inner: listSource{items: []string{"p1"}}}
	var lines []string
	var mu sync.Mutex
	opts := fastLink[string]()
	opts.AttemptLimit = 5
	opts.Proxies = src
	opts.Log = func(_, message string) {
		mu.Lock()
		lines = append(lines, message)
		mu.Unlock()
	}
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error { return errRetryable }), opts)

	res := report.Results[0]
	if res.Outcome != OutcomeProxyExhausted {
		t.Fatalf("outcome = %s, want proxy-exhausted", res.Outcome)
	}
	if !errors.Is(res.Err, ErrProxyExhausted) {
		t.Fatalf("err = %v, want ErrProxyExhausted", res.Err)
	}
	if res.Status != "代理耗尽" {
		t.Fatalf("status = %q, want 代理耗尽", res.Status)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (the one proxy that existed)", res.Attempts)
	}
	if c := report.Counts(); c.ProxyExhausted != 1 || c.Failed != 0 {
		t.Fatalf("counts = %+v, want exactly one proxy-exhausted and no plain failure", c)
	}
	found := false
	for _, line := range lines {
		if line == "支付代理池已耗尽，停止重试" {
			found = true
		}
	}
	if !found {
		t.Fatalf("log lines = %q, want the verbatim app.py:23626 line", lines)
	}
}

func TestEmptyPoolExhaustsEveryAccountBeforeAnyAttempt(t *testing.T) {
	src := &noRecycleSource{inner: listSource{}}
	opts := fastLink[string]()
	opts.Concurrency = 4
	opts.AttemptLimit = 3
	opts.Proxies = src
	report := Run(context.Background(), namedJobs(5),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error {
			t.Error("runner must not run when the pool is empty")
			return nil
		}), opts)

	if c := report.Counts(); c.ProxyExhausted != 5 {
		t.Fatalf("counts = %+v, want 5 proxy-exhausted", c)
	}
	for _, res := range report.Results {
		if res.Attempts != 0 || res.Status != StatusProxyExhausted {
			t.Fatalf("%s: %+v, want 0 attempts and 代理耗尽", res.Key, res)
		}
	}
}

func TestAuthLoopEndsWhenTheRotatingPoolIsDrained(t *testing.T) {
	// The 认证 loop has no attempt cap (app.py:17680); it ends because the
	// caller removes dead proxies (app.py:17706) until Take fails.
	src := &shrinkingSource{items: []string{"p1", "p2", "p3"}}
	opts := Options[int, string]{
		UnlimitedAttempts: true,
		Proxies:           src,
		Classify:          retryableOnly,
		Messages:          AuthMessages(),
		DisableRetryDelay: true,
	}
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error { return errRetryable }), opts)

	res := report.Results[0]
	if res.Outcome != OutcomeProxyExhausted {
		t.Fatalf("outcome = %s, want proxy-exhausted", res.Outcome)
	}
	if res.Attempts != 3 {
		t.Fatalf("attempts = %d, want one per pool entry (3)", res.Attempts)
	}
}

// shrinkingSource drops every proxy it hands out, standing in for a caller that
// removes failed proxies from the pool (_remove_failed_auth_proxy app.py:17403).
type shrinkingSource struct {
	mu    sync.Mutex
	items []string
}

func (s *shrinkingSource) Take(context.Context) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "", false
	}
	head := s.items[0]
	s.items = s.items[1:]
	return head, true
}

// ---------------------------------------------------------------------------
// cancellation (app.py:17643-17645, 17662-17663, 23707-23708)
// ---------------------------------------------------------------------------

func TestCancellationMidBatchStopsPromptly(t *testing.T) {
	const total = 12
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started atomic.Int32
	release := make(chan struct{})
	var once sync.Once

	runner := RunnerFunc[int, string](func(ctx context.Context, _ Job[int], _ string) error {
		if started.Add(1) == 2 {
			once.Do(func() { close(release) })
		}
		<-release
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	})

	opts := fastLink[string]()
	opts.Concurrency = 2
	opts.AttemptLimit = 3
	opts.RetryDelay = time.Hour // must never be waited out
	opts.DisableRetryDelay = false

	go func() {
		<-release
		cancel()
	}()

	done := make(chan Report[int], 1)
	begin := time.Now()
	go func() { done <- Run(ctx, namedJobs(total), runner, opts) }()

	var report Report[int]
	select {
	case report = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation: that is a hung UI")
	}
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Fatalf("Run took %s after cancellation, want prompt", elapsed)
	}
	if !report.Stopped {
		t.Fatal("report.Stopped = false, want true after cancellation")
	}
	counts := report.Counts()
	if counts.Cancelled == 0 {
		t.Fatalf("counts = %+v, want at least one cancelled", counts)
	}
	if counts.Skipped == 0 {
		t.Fatalf("counts = %+v, want the untouched tail reported as skipped", counts)
	}
	if counts.Failed != 0 {
		t.Fatalf("counts = %+v, want cancellation reported as cancelled, not failed", counts)
	}
	if got := counts.Cancelled + counts.Skipped + counts.Succeeded; got != total {
		t.Fatalf("counts = %+v, want %d results accounted for", counts, total)
	}
	for _, res := range report.Results {
		if res.Outcome == OutcomeCancelled && res.Status != "" {
			t.Fatalf("%s: cancelled rows must carry no status, got %q", res.Key, res.Status)
		}
	}
}

func TestAlreadyCancelledContextSkipsEverything(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := fastLink[string]()
	opts.Concurrency = 3
	report := Run(ctx, namedJobs(4),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error {
			t.Error("runner must not run with an already-cancelled context")
			return nil
		}), opts)

	if c := report.Counts(); c.Skipped != 4 {
		t.Fatalf("counts = %+v, want 4 skipped", c)
	}
	if !report.Stopped {
		t.Fatal("report.Stopped = false, want true")
	}
}

func TestCancellationDuringRetryDelayDoesNotWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &listSource{items: []string{"p1", "p2"}, rotate: true}
	opts := fastLink[string]()
	opts.AttemptLimit = 5
	opts.Proxies = src
	opts.DisableRetryDelay = false
	opts.RetryDelay = time.Hour

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	begin := time.Now()
	report := Run(ctx, jobs("one@example.com"),
		RunnerFunc[int, string](func(context.Context, Job[int], string) error { return errRetryable }), opts)

	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("Run waited %s, want the retry delay to be interruptible", elapsed)
	}
	if report.Results[0].Outcome != OutcomeCancelled {
		t.Fatalf("outcome = %s, want cancelled", report.Results[0].Outcome)
	}
}

// ---------------------------------------------------------------------------
// deterministic ordering
// ---------------------------------------------------------------------------

func TestResultsAreInInputOrder(t *testing.T) {
	in := namedJobs(24)
	for run := range 12 {
		src := &listSource{items: []string{"p1", "p2", "p3"}, rotate: true}
		opts := fastLink[string]()
		opts.Concurrency = 8
		opts.AttemptLimit = 2
		opts.Proxies = src
		report := Run(context.Background(), in,
			RunnerFunc[int, string](func(_ context.Context, job Job[int], _ string) error {
				// Uneven work so completion order differs from input order.
				time.Sleep(time.Duration(job.Payload%5) * time.Millisecond)
				if job.Payload%3 == 0 {
					return errRetryable
				}
				return nil
			}), opts)

		if len(report.Results) != len(in) {
			t.Fatalf("run %d: %d results, want %d", run, len(report.Results), len(in))
		}
		for i, res := range report.Results {
			if res.Index != i || res.Key != in[i].Key || res.Payload != in[i].Payload {
				t.Fatalf("run %d: result %d = %+v, want job %+v", run, i, res, in[i])
			}
		}
	}
}

func TestOnResultFiresOncePerJob(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	opts := fastLink[string]()
	opts.Concurrency = 4
	opts.OnResult = func(res Result[int]) {
		mu.Lock()
		seen[res.Key]++
		mu.Unlock()
	}
	in := namedJobs(9)
	Run(context.Background(), in,
		RunnerFunc[int, string](func(context.Context, Job[int], string) error { return nil }), opts)

	if len(seen) != len(in) {
		t.Fatalf("OnResult fired for %d keys, want %d", len(seen), len(in))
	}
	for key, n := range seen {
		if n != 1 {
			t.Fatalf("%s: OnResult fired %d times, want 1", key, n)
		}
	}
}

func TestEmptySelection(t *testing.T) {
	report := Run(context.Background(), nil,
		RunnerFunc[int, string](func(context.Context, Job[int], string) error {
			t.Error("runner must not run for an empty selection")
			return nil
		}), fastLink[string]())
	if len(report.Results) != 0 {
		t.Fatalf("results = %v, want none", report.Results)
	}
	if report.Concurrency != 1 {
		t.Fatalf("concurrency = %d, want 1 (app.py:17623 max(1, len(accounts)))", report.Concurrency)
	}
}

// ---------------------------------------------------------------------------
// triples: the 提链 proxy shape is a value type, not a string
// ---------------------------------------------------------------------------

type triple struct{ create, followup, approve string }

type tripleSource struct {
	mu    sync.Mutex
	items []triple
}

func (s *tripleSource) Take(context.Context) (triple, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return triple{}, false
	}
	head := s.items[0]
	s.items = s.items[1:]
	return head, true
}

func (s *tripleSource) Recycle(t triple) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, t)
}

func TestProxyTypeCanBeALinkTriple(t *testing.T) {
	src := &tripleSource{items: []triple{
		{"c1", "f1", "a1"},
		{"c2", "f2", "a2"},
	}}
	var used []triple
	var mu sync.Mutex
	opts := Options[int, triple]{
		AttemptLimit:      4,
		Proxies:           src,
		Classify:          retryableOnly,
		Messages:          LinkMessages(),
		DisableRetryDelay: true,
	}
	report := Run(context.Background(), jobs("one@example.com"),
		RunnerFunc[int, triple](func(_ context.Context, _ Job[int], p triple) error {
			mu.Lock()
			used = append(used, p)
			n := len(used)
			mu.Unlock()
			if n == 1 {
				return errRetryable
			}
			return nil
		}), opts)

	if report.Results[0].Outcome != OutcomeSucceeded {
		t.Fatalf("outcome = %s, want succeeded", report.Results[0].Outcome)
	}
	if len(used) != 2 || used[0] != (triple{"c1", "f1", "a1"}) || used[1] != (triple{"c2", "f2", "a2"}) {
		t.Fatalf("triples used = %+v, want c1 then c2", used)
	}
}
