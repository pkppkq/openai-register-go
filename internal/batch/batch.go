// Package batch is the UI_SPEC G7 batch orchestrator: a bounded worker pool
// over a selection of accounts, per-account retry with a FRESH proxy on every
// attempt, 代理耗尽 when the pool runs dry, prompt context cancellation, and a
// report in input order.
//
// 参考实现为旧版 Python/Tkinter 的 app.py：
//
//	bounded worker pool     GUI._run_accounts                          17609-17665
//	per-account retry       GUI._run_account_thread                    17667-17714
//	link batch fan-out      GUI._generate_opll_links_from_sessions_worker 23263-23327
//	link retry + racing     GUI._generate_opll_link_retry_worker       23602-23715
//	provider variant        GUI._generate_provider_link_retry_worker   23452-23545
//	unbounded refetch       GUI._refetch_links_batch_worker            17930-17947
//	attempt accounting      link_attempt_counts                        12310, 18608-18614
//	proxy rotation          GUI._take_dynamic_proxies / _rotate_proxy_pool_values 17332, 17316
//
// Nothing here does I/O. The package holds no clients, opens no sockets and
// imports only internal/models and internal/settings, both pure.
//
// MONEY SAFETY: the real Runner rents phone numbers, creates payment links and
// can create billable Team seats. This package never constructs one — that is
// the entire point of the Runner interface — so no test in this package can
// spend money or reach the network.
package batch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// DefaultRetryDelay is the pause between failed rounds, time.sleep(1) at
// app.py:23644 and app.py:23710.
const DefaultRetryDelay = time.Second

// ErrProxyExhausted is the distinct, testable 代理耗尽 condition: the injected
// ProxySource had nothing left to hand this account (app.py:17683-17688,
// 23617-23620, 23625-23628, 23658-23661). Test it with errors.Is, never by
// comparing status text.
var ErrProxyExhausted = errors.New(StatusProxyExhausted)

// ErrAttemptsExhausted is raised once an account burns link_attempt_limit
// attempts without a success (app.py:23711-23713).
var ErrAttemptsExhausted = errors.New(StatusAttemptsExhausted)

// Job is one unit of work. Key is the account e-mail: app.py keys
// link_attempt_counts and every status/log event by account.email
// (app.py:12310, 18610). Payload is whatever the caller's Runner needs and is
// never inspected here.
type Job[T any] struct {
	Key     string
	Payload T
}

// Runner performs ONE attempt for one job with one proxy, mirroring
// _run_account_once (app.py:17716) and _generate_opll_link_for_account
// (called at app.py:23633). nil means success.
//
// The implementation must honour ctx: a Runner that ignores cancellation is a
// hang in the UI, because the orchestrator waits for every in-flight attempt.
type Runner[T, P any] interface {
	RunOnce(ctx context.Context, job Job[T], proxy P) error
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc[T, P any] func(ctx context.Context, job Job[T], proxy P) error

// RunOnce implements Runner.
func (f RunnerFunc[T, P]) RunOnce(ctx context.Context, job Job[T], proxy P) error {
	return f(ctx, job, proxy)
}

// ProxySource hands out one FRESH proxy per attempt. ok=false means the pool is
// dry and the account ends in 代理耗尽.
//
// Match the rotation semantics of internal/proxypool rather than inventing
// others: Pool.Take pops the head and appends it to the tail, so a pool-backed
// source only reports !ok when the pool is empty, and a proxy that keeps
// failing leaves the rotation only when the caller removes it (app.py:17706
// _remove_failed_auth_proxy). With UnlimitedAttempts that removal is the only
// thing that ends a hopeless account short of cancellation — exactly as in Tk.
//
// P is whatever a single attempt needs: a string for the 认证 pool, or a
// create/followup/approve triple for 提链 (app.py:16990 _link_proxy_triples).
type ProxySource[P any] interface {
	Take(ctx context.Context) (proxy P, ok bool)
}

// SourceFunc adapts a plain function to ProxySource.
type SourceFunc[P any] func(ctx context.Context) (P, bool)

// Take implements ProxySource.
func (f SourceFunc[P]) Take(ctx context.Context) (P, bool) { return f(ctx) }

// ProxyRecycler is an optional interface on a ProxySource. When implemented,
// proxies are handed back after a round the way app.py re-queues them at the
// TAIL (app.py:23637, 23640, 23697, 23702, 23706):
//
//	success        → only the proxies that failed retryably go back (23696-23697)
//	non-retryable  → the whole round goes back (23701-23702)
//	retryable      → the failed proxies go back (23705-23706)
//
// A winning proxy is never recycled. A proxypool-backed source needs no
// recycler at all: Pool.Take already rotated the entry to the tail.
type ProxyRecycler[P any] interface {
	Recycle(proxy P)
}

// Disposition tells the orchestrator what a failed attempt means.
type Disposition int

const (
	// Fail ends the account immediately, reporting the error. It is the zero
	// value and the default classification, matching app.py:17711-17714 where
	// any exception other than a 代理检测失败 precheck error stops the account,
	// and app.py:23636-23639 where a non-retryable link error does the same.
	Fail Disposition = iota
	// Retry consumes another FRESH proxy and tries again — app.py:17705-17708
	// (ProxyExitCheckError with status 代理检测失败) and app.py:23640-23644
	// (a plain falsy link result).
	Retry
)

// Classifier maps an attempt error to a Disposition. A nil Classifier means
// every error is Fail.
type Classifier func(err error) Disposition

// Outcome is the terminal state of one job. It is what makes "cancelled" a
// distinct answer from "failed" in the report.
type Outcome int

const (
	// OutcomeSkipped: the job was never started, because the context was
	// already done when a worker reached it (app.py:17643-17645 returns from
	// worker_loop without touching the remaining queue, and app.py:17934-17936
	// stops spawning). Tk leaves such rows' status untouched.
	OutcomeSkipped Outcome = iota
	// OutcomeSucceeded: an attempt returned nil.
	OutcomeSucceeded
	// OutcomeFailed: an attempt returned an error classified Fail.
	OutcomeFailed
	// OutcomeProxyExhausted: 代理耗尽.
	OutcomeProxyExhausted
	// OutcomeAttemptsExhausted: link_attempt_limit attempts, no success.
	OutcomeAttemptsExhausted
	// OutcomeCancelled: the context ended while this job was in flight.
	OutcomeCancelled
)

// String is for logs and test failures, not for the UI.
func (o Outcome) String() string {
	switch o {
	case OutcomeSkipped:
		return "skipped"
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeFailed:
		return "failed"
	case OutcomeProxyExhausted:
		return "proxy-exhausted"
	case OutcomeAttemptsExhausted:
		return "attempts-exhausted"
	case OutcomeCancelled:
		return "cancelled"
	}
	return "unknown"
}

// Result is one job's entry in the report.
type Result[T any] struct {
	// Index is the position in the input slice.
	Index int
	// Key is Job.Key (the account e-mail).
	Key string
	// Payload is Job.Payload, carried through untouched.
	Payload T
	// Outcome is the terminal state.
	Outcome Outcome
	// Attempts is how many attempts this job consumed, i.e. this account's
	// contribution to link_attempt_counts (app.py:18610). Racing counts every
	// lane: app.py:23663 does attempt += len(proxy_batch).
	Attempts int
	// Err is the underlying error: the classified failure, ErrProxyExhausted,
	// ErrAttemptsExhausted or the context error.
	Err error
	// Status is the verbatim Chinese status to push into the account row, or
	// "" when Tk pushes none.
	//
	// DIVERGENCE: app.py sets no status on cancellation — 停止 leaves the row
	// as it was (app.py:23715 only logs) — so Status is "" for
	// OutcomeCancelled and OutcomeSkipped even though the Outcome distinguishes
	// them.
	Status string
}

// Report is the whole batch, in input order.
type Report[T any] struct {
	// Results has exactly one entry per input job, at the input index. Never a
	// map: Go randomises map order, so a map-collected report would be
	// nondeterministic where Python's list is not.
	Results []Result[T]
	// Concurrency is the window actually used after clamping and the
	// proxy-pool cap.
	Concurrency int
	// Stopped reports whether the context was done when the pool drained,
	// i.e. the stop_event.is_set() check at app.py:17662.
	Stopped bool
}

// Counts tallies the report by outcome.
type Counts struct {
	Succeeded         int
	Failed            int
	ProxyExhausted    int
	AttemptsExhausted int
	Cancelled         int
	Skipped           int
}

// Counts tallies Results by Outcome.
func (r Report[T]) Counts() Counts {
	var c Counts
	for _, res := range r.Results {
		switch res.Outcome {
		case OutcomeSucceeded:
			c.Succeeded++
		case OutcomeFailed:
			c.Failed++
		case OutcomeProxyExhausted:
			c.ProxyExhausted++
		case OutcomeAttemptsExhausted:
			c.AttemptsExhausted++
		case OutcomeCancelled:
			c.Cancelled++
		case OutcomeSkipped:
			c.Skipped++
		}
	}
	return c
}

// AttemptCounts is link_attempt_counts for this batch, keyed by account e-mail
// (app.py:12310). Duplicate keys in the selection accumulate, as they do in
// Python where the counter is per e-mail, not per row.
func (r Report[T]) AttemptCounts() map[string]int {
	counts := make(map[string]int, len(r.Results))
	for _, res := range r.Results {
		if res.Attempts > 0 {
			counts[res.Key] += res.Attempts
		}
	}
	return counts
}

// Options configures a run. The zero value is a usable single-attempt,
// single-lane, proxy-less batch with concurrency 1.
type Options[T, P any] struct {
	// Concurrency is settings.Settings.AuthConcurrency. It is clamped with
	// ClampAuthConcurrency against MaxAuthConcurrency and the job count.
	Concurrency int
	// ProxyPoolSize, when > 0, lowers Concurrency to it (app.py:17624-17629).
	ProxyPoolSize int
	// Race is settings.Settings.LinkRaceConcurrency: how many proxies one
	// account may try SIMULTANEOUSLY per round (app.py:23651-23683). Clamped
	// with ClampRaceConcurrency. Note that Concurrency bounds accounts, not
	// attempts: at most Concurrency*Race attempts can be in flight, which is
	// what Tk does too.
	Race int
	// AttemptLimit is settings.Settings.LinkAttemptLimit, clamped with
	// ClampAttemptLimit. Ignored when UnlimitedAttempts is set.
	AttemptLimit int
	// UnlimitedAttempts ports _run_account_thread (app.py:17680), whose loop
	// has no attempt cap: it retries until the pool is dry or the user stops.
	// Only the 认证 batch sets this; 提链 always has a limit.
	UnlimitedAttempts bool
	// Proxies is the source of FRESH proxies. nil means "no pool": every job
	// gets the zero P and exactly ONE attempt, which is app.py:17689-17692
	// (without a pool, attempts > 0 returns immediately).
	Proxies ProxySource[P]
	// Classify maps an attempt error to Retry or Fail. nil means always Fail.
	Classify Classifier
	// Log receives (account key, line). Lines come from Messages; an empty
	// message is not logged. Calls are serialised, matching the single Tk event
	// queue.
	Log func(key, message string)
	// OnAttempt fires as each attempt starts, with the running per-key count.
	// It is the ("link-attempt", email) event of app.py:23749, consumed at
	// app.py:18608-18614. Serialised with Log.
	OnAttempt func(key string, count int)
	// OnResult fires as each job reaches its Outcome, so the UI can push the
	// status event without waiting for the whole batch. Serialised with Log.
	OnResult func(result Result[T])
	// Messages are the log lines; see AuthMessages and LinkMessages.
	Messages Messages
	// RetryDelay is the pause between failed rounds. Zero means
	// DefaultRetryDelay.
	RetryDelay time.Duration
	// DisableRetryDelay removes the pause entirely (tests).
	DisableRetryDelay bool
	// FailureStatus derives the account status from a Fail error. nil uses
	// models.ExceptionStatus(err, StatusFailed), which is exception_status(exc,
	// "失败") at app.py:17712.
	FailureStatus func(err error) string
}

func (o Options[T, P]) delay() time.Duration {
	if o.DisableRetryDelay {
		return 0
	}
	if o.RetryDelay > 0 {
		return o.RetryDelay
	}
	return DefaultRetryDelay
}

func (o Options[T, P]) classify(err error) Disposition {
	if o.Classify == nil {
		return Fail
	}
	return o.Classify(err)
}

func (o Options[T, P]) failureStatus(err error) string {
	if o.FailureStatus != nil {
		return o.FailureStatus(err)
	}
	return models.ExceptionStatus(err, StatusFailed)
}

// Run executes jobs through runner with a bounded worker pool and returns one
// Result per job, in input order.
//
// It ports _run_accounts (app.py:17609-17665) for the pool and
// _generate_opll_link_retry_worker (app.py:23602-23715) for the per-account
// retry, racing and attempt accounting; with Race=1 and UnlimitedAttempts the
// per-account loop degenerates into _run_account_thread (app.py:17667-17714).
func Run[T, P any](ctx context.Context, jobs []Job[T], runner Runner[T, P], opts Options[T, P]) Report[T] {
	report := Report[T]{Results: make([]Result[T], len(jobs))}
	for i, job := range jobs {
		report.Results[i] = Result[T]{Index: i, Key: job.Key, Payload: job.Payload, Outcome: OutcomeSkipped}
	}

	// app.py:17622-17629, in that order: clamp, then cap by the pool, logging
	// the cap before the window.
	concurrency := ClampAuthConcurrency(opts.Concurrency, len(jobs))
	if opts.Proxies != nil {
		if capped := CapConcurrencyByProxyPool(concurrency, opts.ProxyPoolSize); capped != concurrency {
			logf(&opts, nil, "", opts.Messages.ConcurrencyCapped, capped, concurrency)
			concurrency = capped
		}
	}
	report.Concurrency = concurrency
	if len(jobs) == 0 {
		return report
	}
	if concurrency > 1 {
		logf(&opts, nil, "", opts.Messages.ConcurrencyWindow, concurrency)
	}

	st := &state[T, P]{
		runner:    runner,
		opts:      opts,
		attempts:  map[string]int{},
		race:      ClampRaceConcurrency(opts.Race),
		limit:     ClampAttemptLimit(opts.AttemptLimit),
		unlimited: opts.UnlimitedAttempts,
		usePool:   opts.Proxies != nil,
		delay:     opts.delay(),
	}
	if !st.usePool {
		// app.py:17689-17692: no pool means one shot per account, no racing.
		st.race = 1
		st.limit = 1
		st.unlimited = false
	}

	// A shared cursor is queue.Queue.get_nowait (app.py:17638-17653): each
	// worker takes the next index until the selection is used up.
	var cursor atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// app.py:17643-17645 checks the stop event before dequeuing,
				// so a cancelled batch leaves the rest of the queue untouched.
				if ctx.Err() != nil {
					return
				}
				i := int(cursor.Add(1)) - 1
				if i >= len(jobs) {
					return
				}
				res := st.runJob(ctx, jobs[i], i)
				report.Results[i] = res
				st.emitResult(res)
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		report.Stopped = true
		// app.py:17662-17663 / 23715.
		logLine(&opts, &st.mu, "", opts.Messages.Stopped)
	}
	return report
}

type state[T, P any] struct {
	runner Runner[T, P]
	opts   Options[T, P]

	// mu serialises the attempt counter and every user callback, standing in
	// for the single Tk event queue.
	mu       sync.Mutex
	attempts map[string]int

	race      int
	limit     int
	unlimited bool
	usePool   bool
	delay     time.Duration
}

type attempt[P any] struct {
	proxy P
	err   error
}

// runJob is one account's retry loop: _generate_opll_link_retry_worker
// (app.py:23602-23715), which collapses to _run_account_thread
// (app.py:17667-17714) when race == 1.
func (s *state[T, P]) runJob(ctx context.Context, job Job[T], index int) Result[T] {
	res := Result[T]{Index: index, Key: job.Key, Payload: job.Payload}
	used := 0

	for {
		if ctx.Err() != nil {
			// The loop head is `while not self.stop_event.is_set()`
			// (app.py:23622, 23651, 17680).
			return s.cancelled(res, used, ctx.Err())
		}
		if !s.unlimited && used >= s.limit {
			break
		}

		// app.py:23653: min(race_concurrency, max_attempts - attempt).
		want := s.race
		if !s.unlimited && s.limit-used < want {
			want = s.limit - used
		}
		batch := s.takeProxies(ctx, want)
		if len(batch) == 0 {
			// 代理耗尽 — app.py:17683-17688 / 23658-23661. A partially filled
			// batch is fine (app.py:23656 just breaks); the next round is the
			// one that reports exhaustion.
			s.log(job.Key, s.opts.Messages.ProxyExhausted)
			res.Attempts = used
			res.Outcome = OutcomeProxyExhausted
			res.Err = ErrProxyExhausted
			res.Status = StatusProxyExhausted
			return res
		}

		used += len(batch) // app.py:23663
		res.Attempts = used
		outcomes := s.race1(ctx, job, batch)

		success := false
		var fatal error
		var retryable []P
		for _, o := range outcomes {
			switch {
			case o.err == nil:
				success = true
			case s.opts.classify(o.err) == Retry:
				retryable = append(retryable, o.proxy)
			default:
				if fatal == nil {
					fatal = o.err
				}
			}
		}

		switch {
		case success:
			// app.py:23696-23697: only the losing lanes go back; the winner is
			// consumed.
			s.recycle(retryable)
		case fatal != nil:
			// app.py:23701-23702: a non-retryable round returns everything.
			s.recycle(proxiesOf(outcomes))
		default:
			// app.py:23705-23706 / 23640.
			s.recycle(retryable)
		}

		if success {
			res.Outcome = OutcomeSucceeded
			return res
		}
		// DIVERGENCE: app.py:23700-23704 reports 不可自动重试 even when the user
		// hit 停止 during the round. UI_SPEC G7 needs cancelled and failed to be
		// distinguishable in the report, so a done context wins over a
		// classified failure here. Statuses match either way: cancellation
		// pushes none.
		if ctx.Err() != nil {
			return s.cancelled(res, used, ctx.Err())
		}
		if fatal != nil {
			res.Outcome = OutcomeFailed
			res.Err = fatal
			res.Status = s.opts.failureStatus(fatal)
			return res
		}
		if len(batch) > 1 {
			s.logf(job.Key, s.opts.Messages.RaceRoundFailed, len(batch))
		} else {
			s.log(job.Key, s.opts.Messages.RoundFailed)
		}
		if !sleepCtx(ctx, s.delay) { // time.sleep(1) at app.py:23644 / 23710
			return s.cancelled(res, used, ctx.Err())
		}
	}

	// app.py:23711-23713 / 23543-23545.
	s.logf(job.Key, s.opts.Messages.AttemptLimit, s.limit)
	res.Attempts = used
	res.Outcome = OutcomeAttemptsExhausted
	res.Err = ErrAttemptsExhausted
	res.Status = StatusAttemptsExhausted
	return res
}

func (s *state[T, P]) cancelled(res Result[T], used int, err error) Result[T] {
	res.Attempts = used
	res.Outcome = OutcomeCancelled
	res.Err = err
	res.Status = ""
	return res
}

// takeProxies fills one round. Without a source every attempt runs on the zero
// proxy (app.py:17692 register_dynamic_proxy = "").
func (s *state[T, P]) takeProxies(ctx context.Context, want int) []P {
	if want <= 0 {
		return nil
	}
	if !s.usePool {
		return make([]P, 1)
	}
	batch := make([]P, 0, want)
	for len(batch) < want {
		proxy, ok := s.opts.Proxies.Take(ctx)
		if !ok {
			break // queue.Empty at app.py:23656
		}
		batch = append(batch, proxy)
	}
	return batch
}

// race1 runs one round: len(batch) simultaneous attempts, joined before the
// round is judged (app.py:23670-23694, run_one + join).
func (s *state[T, P]) race1(ctx context.Context, job Job[T], batch []P) []attempt[P] {
	outcomes := make([]attempt[P], len(batch))
	var wg sync.WaitGroup
	for i, proxy := range batch {
		s.bumpAttempt(job.Key)
		wg.Add(1)
		go func(i int, proxy P) {
			defer wg.Done()
			outcomes[i] = attempt[P]{proxy: proxy, err: s.runner.RunOnce(ctx, job, proxy)}
		}(i, proxy)
	}
	wg.Wait()
	return outcomes
}

func proxiesOf[P any](outcomes []attempt[P]) []P {
	all := make([]P, 0, len(outcomes))
	for _, o := range outcomes {
		all = append(all, o.proxy)
	}
	return all
}

func (s *state[T, P]) recycle(proxies []P) {
	if len(proxies) == 0 {
		return
	}
	recycler, ok := s.opts.Proxies.(ProxyRecycler[P])
	if !ok {
		return
	}
	for _, proxy := range proxies {
		recycler.Recycle(proxy)
	}
}

// bumpAttempt is the ("link-attempt", email) event: app.py:23749 emits it,
// app.py:18610 folds it into link_attempt_counts with max(0, prev)+1.
func (s *state[T, P]) bumpAttempt(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := s.attempts[key]
	if count < 0 {
		count = 0
	}
	count++
	s.attempts[key] = count
	if s.opts.OnAttempt != nil {
		s.opts.OnAttempt(key, count)
	}
}

func (s *state[T, P]) emitResult(res Result[T]) {
	if s.opts.OnResult == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.OnResult(res)
}

func (s *state[T, P]) log(key, message string) { logLine(&s.opts, &s.mu, key, message) }

func (s *state[T, P]) logf(key, format string, args ...any) {
	logf(&s.opts, &s.mu, key, format, args...)
}

// logLine emits a Messages entry as-is. An empty entry means "Tk logs nothing
// here", and a nil Options.Log means the caller wants no logs at all.
func logLine[T, P any](opts *Options[T, P], mu *sync.Mutex, key, message string) {
	if opts.Log == nil || message == "" {
		return
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	opts.Log(key, message)
}

// logf is logLine for the Messages entries that carry %d verbs.
func logf[T, P any](opts *Options[T, P], mu *sync.Mutex, key, format string, args ...any) {
	if opts.Log == nil || format == "" {
		return
	}
	logLine(opts, mu, key, fmt.Sprintf(format, args...))
}

// sleepCtx reports false when the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
