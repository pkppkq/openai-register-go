package ui

// UI_SPEC gap G7: batch orchestration.
//
// The single-account bindings (StartRegister, GenerateLinks) each run one
// account with the FIRST entry of a pool. That is a deliberate simplification,
// and it is not what the Tk app does: _run_accounts (app.py:17609-17665) runs a
// bounded pool over the whole selection, and _run_account_thread
// (app.py:17667-17714) takes a FRESH proxy for every attempt of every account,
// drops one that fails its exit precheck, and stops that account with 代理耗尽
// once the pool is dry.
//
// internal/batch is that orchestrator, already ported and tested. It is generic
// and does no I/O — it holds no clients and opens no sockets — so everything
// that can actually spend money is injected from here:
//
//	Runner       -> one account, one proxy: exactly the single-account path
//	ProxySource  -> proxypool.Set, which rotates the taken entry to the tail
//	Classify     -> which failures deserve a fresh proxy rather than ending
//	OnResult     -> the per-account status write
//
// SPENDS MONEY, N TIMES OVER. A batch is the single most expensive thing this
// app can do: every account can rent a billable phone number, and every relink
// creates a real payment link. The frontend must confirm the COUNT.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/batch"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// StartBatchRequest is one 批量 run over a selection of accounts.
type StartBatchRequest struct {
	// Emails is the selection, in the order the table shows it. Duplicates are
	// rejected rather than silently collapsed: two rows for one address would
	// double the money spent on it.
	Emails []string `json:"emails"`
	// Confirmed 必须来自展示过账号数量的确认框。
	Confirmed bool `json:"confirmed"`
	// CollectSession 对应 _start_worker 的 collect_session（app.py:16683）。
	CollectSession bool `json:"collectSession"`
}

// BatchSummary is what the caller gets back immediately: the parent job, and
// the accounts that were refused before anything started.
type BatchSummary struct {
	Job JobView `json:"job"`
	// Skipped names the accounts preflight refused (Cloud Mail token missing,
	// 邮箱锁定). They are reported rather than dropped, because Python logs each
	// one and writes it into the row's 状态.
	Skipped []string `json:"skipped"`
}

// StartBatchRegister is 批量注册/登录 — _start_worker over a selection
// (app.py:16683 -> _run_accounts 17609).
//
// 每个账号都可能产生付费副作用；确认后才允许读取状态或创建父任务。
func (a *App) StartBatchRegister(req StartBatchRequest) (BatchSummary, error) {
	if !req.Confirmed {
		return BatchSummary{}, errors.New("批量注册或登录前必须确认")
	}
	return a.startBatch(JobBatchRegister, req)
}

func (a *App) startBatch(parentKind JobKind, req StartBatchRequest) (BatchSummary, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return BatchSummary{}, err
	}
	st := settings.FromSnapshot(snapshot)

	accounts, skipped, err := a.resolveBatchSelection(snapshot, req.Emails)
	if err != nil {
		return BatchSummary{}, err
	}
	if len(accounts) == 0 {
		// app.py:16683's callers all guard on an empty selection first.
		return BatchSummary{Skipped: skipped}, errors.New("没有可执行的账号")
	}

	if parentKind != JobBatchRegister {
		return BatchSummary{}, fmt.Errorf("未支持的批量任务类型: %s", parentKind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The parent carries no email, so registerJob's duplicate check does not
	// apply to it — only to the children it dispatches.
	parentID, err := a.registerJob(parentKind, "", "", cancel)
	if err != nil {
		cancel()
		return BatchSummary{}, err
	}
	a.setBatchProgress(parentID, len(accounts), 0)
	log := a.jobLogger(parentID)
	for _, email := range skipped {
		log(fmt.Sprintf("跳过 %s：未通过启动前检查", email))
	}

	view, _ := a.jobView(parentID)
	go func() {
		defer cancel()
		a.runBatch(ctx, parentID, req.CollectSession, accounts, st, log)
	}()
	return BatchSummary{Job: view, Skipped: skipped}, nil
}

// resolveBatchSelection turns the requested addresses into accounts, applying
// the same per-account preflight the single-account path uses. An address that
// does not exist is an error for the WHOLE call — a typo must not silently
// shrink a batch the user is about to pay for — while an account that merely
// fails preflight is reported as skipped, which is what app.py:16690-16696 does.
func (a *App) resolveBatchSelection(snapshot map[string]any, emails []string) ([]models.MailAccount, []string, error) {
	all := accountsFromSnapshot(snapshot)
	byKey := make(map[string]models.MailAccount, len(all))
	for _, acc := range all {
		byKey[strings.ToLower(models.NormalizeEmailAddress(acc.Email))] = acc
	}

	var (
		out     []models.MailAccount
		skipped []string
	)
	seen := map[string]bool{}
	for _, raw := range emails {
		key := strings.ToLower(models.NormalizeEmailAddress(raw))
		if key == "" {
			return nil, nil, errors.New("选择中包含空邮箱")
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("选择中有重复账号: %s", raw)
		}
		seen[key] = true
		account, ok := byKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("账号不存在: %s", raw)
		}
		if err := preflight(snapshot, account); err != nil {
			skipped = append(skipped, account.Email)
			continue
		}
		out = append(out, account)
	}
	return out, skipped, nil
}

// batchOptions is the pure half of the orchestration: everything except the
// Runner, which is the only part that can spend money.
//
// Split out so a test can drive the whole pool / rotation / classification /
// concurrency-clamp path with a fake runner. There is no other honest way to
// test this — the real Runner opens a browser and logs into a real account.
func batchOptions(st settings.Settings, pool *proxypool.Set, poolSize int, log func(string), onResult func(batch.Result[models.MailAccount])) batch.Options[models.MailAccount, string] {
	opts := batch.Options[models.MailAccount, string]{
		Concurrency:   st.AuthConcurrency,
		ProxyPoolSize: poolSize,
		// Race is 提链-only (app.py:23651). The 认证 loop tries one proxy at a
		// time, and _refetch_links_batch_worker does too — the racing variant is
		// _generate_opll_link_retry_worker, which is 批量提链 from SESSIONS and is a
		// separate screen.
		Race: 1,
		// _run_account_thread's loop has no attempt cap: it retries until the pool
		// is dry or the user stops (app.py:17680).
		UnlimitedAttempts: true,
		Messages:          batch.AuthMessages(),
		Classify:          batchClassify,
		OnResult:          onResult,
		Log: func(key, message string) {
			if key == "" {
				log(message)
				return
			}
			log(fmt.Sprintf("[%s] %s", key, message))
		},
	}
	if pool != nil {
		opts.Proxies = batch.SourceFunc[string](func(context.Context) (string, bool) {
			proxy := pool.TakeAuth(st.RegisterWithPaymentProxy)
			return proxy, proxy != ""
		})
	}
	return opts
}

// runBatch is the orchestration proper. It blocks until every account is done,
// on the goroutine startBatch spawned.
func (a *App) runBatch(ctx context.Context, parentID string, collectSession bool, accounts []models.MailAccount, st settings.Settings, log func(string)) {
	pool, poolSize := a.authProxyPool(st)
	runner := batch.RunnerFunc[models.MailAccount, string](func(ctx context.Context, job batch.Job[models.MailAccount], proxy string) error {
		childKind := batchRegisterChildKind(job.Payload, collectSession)
		return a.runBatchAccount(ctx, parentID, childKind, job.Payload, proxy, pool, st)
	})
	a.runBatchWith(ctx, parentID, accounts, st, pool, poolSize, log, runner)
}

// batchRegisterChildKind 与单账号 StartRegister 使用同一入口分派。Team
// 账号在 collectSession=true 时必须走 RunTeam；把整个批次固定为
// JobRegister 会进入普通注册及租号流程，既不兼容 Team SSO，也可能产生错误
// 的付费副作用。只登录不取 Session 时仍统一走 RunAuthOnly。
func batchRegisterChildKind(account models.MailAccount, collectSession bool) JobKind {
	if !collectSession {
		return JobAuthOnly
	}
	if strings.EqualFold(strings.TrimSpace(account.AccountType), "team") {
		return JobTeam
	}
	return JobRegister
}

func (a *App) runBatchWith(ctx context.Context, parentID string, accounts []models.MailAccount, st settings.Settings, pool *proxypool.Set, poolSize int, log func(string), runner batch.Runner[models.MailAccount, string]) batch.Report[models.MailAccount] {
	jobs := make([]batch.Job[models.MailAccount], 0, len(accounts))
	for _, acc := range accounts {
		jobs = append(jobs, batch.Job[models.MailAccount]{Key: acc.Email, Payload: acc})
	}
	done := 0
	opts := batchOptions(st, pool, poolSize, log, func(batch.Result[models.MailAccount]) {
		done++
		a.setBatchProgress(parentID, len(accounts), done)
	})

	report := batch.Run(ctx, jobs, runner, opts)
	counts := report.Counts()
	log(fmt.Sprintf("批量任务结束：成功 %d，失败 %d，代理耗尽 %d，已停止 %d",
		counts.Succeeded, counts.Failed, counts.ProxyExhausted, counts.Cancelled))
	a.markJobFinished(parentID, report, nil, ctx.Err() != nil)
	return report
}

// runBatchAccount is one attempt for one account: the single-account path with
// the batch's proxy substituted for the pool's first entry.
//
// A child registry entry is created per ATTEMPT rather than per account, so the
// job pane shows a retry as a new run — which is what it is, on a different
// exit. The duplicate-account guard in registerJob still holds, because the
// previous attempt's child is already finished by the time the next one starts.
func (a *App) runBatchAccount(ctx context.Context, parentID string, kind JobKind, account models.MailAccount, proxy string, pool *proxypool.Set, st settings.Settings) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	childID, err := a.registerJob(kind, account.Email, parentID, cancel)
	if err != nil {
		return err
	}
	log := a.jobLogger(childID)

	// The exit is already known here — it came from the batch's pool — so the
	// value runJobWithProxy reports back is redundant for this caller.
	result, _, runErr := a.runJobWithProxy(childCtx, childID, kind, account, &proxy, log)
	a.finishJob(childID, kind, account, result, runErr, childCtx.Err() != nil && ctx.Err() != nil)

	dropFailedAuthProxy(pool, authPoolRole(st), proxy, runErr, log)
	return runErr
}

// dropFailedAuthProxy is app.py's _remove_failed_auth_proxy call sites, split out
// of runBatchAccount because everything around it in that function launches a
// browser and spends money — this is the only part a test can reach.
//
// A proxy that failed its exit precheck is REMOVED from the pool, not merely
// rotated, and for ANY ProxyExitCheckError — not only the retry-worthy
// 代理检测失败 one. app.py has TWO removal sites:
//
//	17705  if isinstance(exc, ProxyExitCheckError) and exc.status == "代理检测失败" ...
//	17706      self._remove_failed_auth_proxy(...)   <- and retry this account
//	17709  if isinstance(exc, ProxyExitCheckError):
//	17710      self._remove_failed_auth_proxy(...)   <- and give up on this account
//
// Removing only in the first case leaves a dead exit in the pool for every
// remaining account to be handed in turn, and each of those handoffs is another
// browser launch on an exit already known to be broken.
func dropFailedAuthProxy(pool *proxypool.Set, role proxypool.Role, proxy string, runErr error, log func(string)) {
	if pool == nil {
		return
	}
	var exit *models.ProxyExitCheckError
	if !errors.As(runErr, &exit) {
		return
	}
	// Remove normalizes and reports whether a line actually went, so an empty or
	// already-removed proxy logs nothing — app.py:17404's `if not dynamic_proxy`
	// and 17354's `if not removed`.
	if !pool.Remove(role, proxy) {
		return
	}
	// 17707's per-account line, which only the retry branch writes.
	if batchClassify(runErr) == batch.Retry {
		log(fmt.Sprintf("认证代理预检失败，自动换下一个代理重试: %v", runErr))
	}
	log(failedAuthProxyRemovedLine(role, proxy))
}

// dropFailedStandaloneProxy is _remove_failed_auth_proxy for a run that owns no
// batch pool — every single-account entry point calls it (app.py:17824, 17862,
// 17923, 17954), so a dead exit is pruned from the SAVED pool whichever button
// hit it. Without this, 重新获取 on a dead exit retries it on every click
// forever, and each retry is a browser launch.
//
// It re-reads the pool inside the write rather than taking a *proxypool.Set,
// because a standalone run has none: the pool it must edit is the persisted
// settings text itself.
func (a *App) dropFailedStandaloneProxy(proxy string, runErr error, log func(string)) {
	var exit *models.ProxyExitCheckError
	if !errors.As(runErr, &exit) {
		return
	}
	var removedFrom proxypool.Role
	removed := false
	_ = a.mutateState(true, func(prior map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(prior)
		role := authPoolRole(st)
		text := st.DynamicProxies
		if role == proxypool.RoleCreate {
			text = st.PaymentDynamicProxy
		}
		pool := proxypool.NewPool(text)
		if !pool.Remove(proxy) {
			// app.py:17354's `if not removed: return False` — no write, no log.
			return nil, nil, errNoStateChange
		}
		removed, removedFrom = true, role
		if role == proxypool.RoleCreate {
			st.PaymentDynamicProxy = pool.Text()
		} else {
			st.DynamicProxies = pool.Text()
		}
		return settings.ToSnapshot(st, prior), map[string]bool{}, nil
	})
	if removed {
		log(failedAuthProxyRemovedLine(removedFrom, proxy))
	}
}

// failedAuthProxyRemovedLine is the removal notice app.py:17357 / 17513 write.
//
// DIVERGENCE (deliberate): the 注册 pool's line is
// `失败注册代理已移除: {target}` — the RAW proxy, credentials and all — while
// the 支付 pool's is `失败支付代理已移除: {mask_proxy_url(target)}`. The pair
// makes the intent obvious and the register-side omission a slip; this log is
// shown in the UI and routinely pasted when asking for help, so both are masked.
func failedAuthProxyRemovedLine(role proxypool.Role, proxy string) string {
	what := "注册"
	if role == proxypool.RoleCreate {
		what = "支付"
	}
	return fmt.Sprintf("失败%s代理已移除: %s", what, proxypool.MaskProxyURL(proxy))
}

// batchClassify is app.py:17705-17714: only a 代理检测失败 exit precheck failure
// earns a fresh proxy. Everything else — a phone requirement, a locked mailbox,
// a deactivated account — ends that account, because retrying it would spend
// money on a condition that cannot change between attempts.
func batchClassify(err error) batch.Disposition {
	if err == nil {
		return batch.Fail
	}
	var exit *models.ProxyExitCheckError
	if errors.As(err, &exit) && exit.ErrorStatus() == "代理检测失败" {
		return batch.Retry
	}
	return batch.Fail
}

// authProxyPool builds the rotating 认证 pool for one batch, or nil when there
// is none (app.py:16694-16695).
//
// nil is meaningful, not an error: batch.Options treats a nil ProxySource as
// "no pool", giving every job the zero proxy and exactly ONE attempt — which is
// app.py:17689-17692, where without a pool `attempts > 0` returns immediately.
func (a *App) authProxyPool(st settings.Settings) (*proxypool.Set, int) {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		// 全走本地代理 empties every pool (app.py:16719), so the chain degenerates
		// to the local proxy alone and there is nothing to rotate.
		return nil, 0
	}
	text := st.DynamicProxies
	if st.RegisterWithPaymentProxy {
		text = st.PaymentDynamicProxy
	}
	entries := proxypool.ParseProxyPoolText(text)
	if len(entries) == 0 {
		return nil, 0
	}
	set := proxypool.NewSet()
	role := authPoolRole(st)
	set.SetText(role, text)
	// Installed AFTER SetText so seeding the pool does not itself trigger a write.
	set.SetOnChange(func() { a.persistAuthPool(role, set) })
	return set, len(entries)
}

// persistAuthPool writes the pool back to state.json after a take or a removal.
//
// THE POOL IS PERSISTED STATE IN THE TK APP, not batch-local scratch. Both
// mutations go through _rotate_proxy_pool_values (app.py:17316) or
// _remove_*_dynamic_proxy_value (17342/17492), and EVERY one of them ends with
// `self.save_state()` — the textarea IS the pool, so rotating it to the tail and
// deleting a dead line are both edits to the user's saved configuration.
//
// Without this the Set is built from settings at the top of every batch and
// thrown away at the bottom, so:
//
//   - a dead exit removed during a batch comes back for the next one, and is
//     retried from scratch — one wasted browser launch per account, forever;
//   - the rotation never advances, so two consecutive runs leave through the
//     same first entry where Tk would have moved on.
//
// flush=true, not the debounced path, for the reason spelled out on
// saveFingerprint: a debounced write is dropped by the next flush and its
// background writer would touch Store.deferredSessionIndex concurrently with a
// Load from a UI call, which is a fatal concurrent map access rather than a
// tolerable race. Best-effort — a failed pool write must not fail the batch.
func (a *App) persistAuthPool(role proxypool.Role, set *proxypool.Set) {
	_ = a.mutateState(true, func(prior map[string]any) (map[string]any, map[string]bool, error) {
		// Read the text inside the write lock so concurrent attempts cannot
		// persist a pool state older than one already written.
		text := set.Text(role)
		st := settings.FromSnapshot(prior)
		switch role {
		case proxypool.RoleCreate:
			if st.PaymentDynamicProxy == text {
				return nil, nil, errNoStateChange
			}
			st.PaymentDynamicProxy = text
		default:
			if st.DynamicProxies == text {
				return nil, nil, errNoStateChange
			}
			st.DynamicProxies = text
		}
		return settings.ToSnapshot(st, prior), map[string]bool{}, nil
	})
}

// authPoolRole is the pool TakeAuth reads for this settings state
// (app.py:17412/17424): the 注册 pool normally, the 第一步 pool when the
// 特殊情况 checkbox redirects the login hop.
func authPoolRole(st settings.Settings) proxypool.Role {
	if st.RegisterWithPaymentProxy {
		return proxypool.RoleCreate
	}
	return proxypool.RoleRegister
}

// setBatchProgress updates the parent's counters and re-emits it.
func (a *App) setBatchProgress(parentID string, total, done int) {
	a.jobs.mu.Lock()
	j := a.jobs.jobs[parentID]
	if j == nil {
		a.jobs.mu.Unlock()
		return
	}
	j.view.Total = total
	j.view.Done = done
	a.jobs.mu.Unlock()
	a.emitJob(j)
}
