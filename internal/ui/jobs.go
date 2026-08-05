package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	applogs "github.com/pkppkq/openai-register-go/internal/logs"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// Tk 版本把长任务放在线程中，再由 root.after 把队列进度送回 UI。Wails 的
// WebView 是独立进程边界，因此 Go 端用 goroutine 执行任务，并通过事件流发送
// 日志和状态。公开入口完成参数校验和风险确认后，再调用包内 startJob；
// 任何 Wails 绑定方法都不能等待长任务结束。

// Event names the frontend subscribes to.
const (
	// EventJob carries a JobView whenever a job is created or changes state.
	EventJob = "job"
	// EventLinkSuccess 通知前端播放成功提示音。暂停其他任务由 Go 端完成，
	// 不依赖 WebView 是否仍在前台或事件监听是否及时安装。
	EventLinkSuccess = "link-success"
)

// LinkSuccessEvent 是前端提示音所需的最小数据。AudioDevice 保留 Python
// 保存的设备标签；WebView 不支持该设备时应明确回退到系统默认输出。
type LinkSuccessEvent struct {
	Email       string `json:"email"`
	AudioDevice string `json:"audioDevice"`
}

// JobKind is which backend entry point a job runs. These map 1:1 onto
// internal/worker's five entry points.
type JobKind string

const (
	JobRegister      JobKind = "register"        // Worker.Run
	JobAuthOnly      JobKind = "auth_only"       // Worker.RunAuthOnly
	JobTeam          JobKind = "team"            // Worker.RunTeam
	JobRegisterAndRT JobKind = "register_and_rt" // Worker.RunRegisterAndAuthorizeRT
	JobRelink        JobKind = "relink"          // Worker.Relink

	// 批量父任务自身不运行单账号 worker，而是持有并发窗口和代理轮转；
	// 认证批次派发的每个子任务通过 BatchID 指回父任务。
	JobBatchRegister JobKind = "batch_register" // _run_accounts, app.py:17609
	// 批量提链从已保存的 Access Token 直接生成支付链接；父任务统一持有
	// 三段代理队列、撞链并发和每账号尝试上限。
	JobBatchLink JobKind = "batch_link"
	// 批量重新获取会为每个账号重新登录并生成一条长链；每个账号只尝试一次。
	JobBatchRelink JobKind = "batch_relink"
)

// JobStatus is the lifecycle of one job.
type JobStatus string

const (
	StatusRunning   JobStatus = "running"
	StatusSucceeded JobStatus = "succeeded"
	StatusFailed    JobStatus = "failed"
	StatusCancelled JobStatus = "cancelled"
)

// JobView is the frontend-facing snapshot of a job. Deliberately flat and
// JSON-friendly; the rich result stays on the Go side until asked for.
type JobView struct {
	ID       string    `json:"id"`
	Kind     JobKind   `json:"kind"`
	Email    string    `json:"email"`
	Status   JobStatus `json:"status"`
	Error    string    `json:"error"`
	Started  string    `json:"started"`
	Finished string    `json:"finished"`

	// BatchID is the parent batch's job id, or "" for a standalone run. Set on
	// the CHILD; a parent's own BatchID is always "".
	BatchID string `json:"batchId"`
	// Total and Done are the batch parent's progress. Both are 0 on a child and
	// on a standalone run.
	Total int `json:"total"`
	Done  int `json:"done"`
}

type job struct {
	// seq is the creation order, used to sort deterministically: two jobs
	// started in the same second have identical RFC3339 timestamps.
	seq    int
	view   JobView
	cancel context.CancelFunc
	// logEmail 是结构化日志的显式账户路由。通常等于 view.Email；需要用
	// view.Email 保存非邮箱任务身份（例如手机号）的任务可单独留空。
	logEmail string
	// result holds whatever the entry point returned, for a later fetch.
	result any
	// prompt is the reply channel of the manual-input request this job is
	// currently blocked on, or nil. Guarded by jobRegistry.mu like every other
	// field here; see App.inputCallback / App.AnswerPrompt.
	prompt chan string
}

type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*job
	seq  int
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{jobs: map[string]*job{}}
}

// startJob 启动单账号后端入口并立即返回任务 ID。它只允许由已经完成风险确认的
// 公开方法调用；保持未导出可防止 WebView 绕过这些确认门禁。
func (a *App) startJob(kind string, email string) (string, error) {
	account, err := a.accountByEmail(email)
	if err != nil {
		return "", err
	}
	k := JobKind(kind)
	if !k.isEntryPoint() {
		return "", fmt.Errorf("未知的任务类型: %s", kind)
	}
	// app.py:16690-16696 refuses these two BEFORE the worker exists. Both are
	// money-safety checks — see preflight.
	snapshot, err := a.snapshot()
	if err != nil {
		return "", err
	}
	if err := preflight(snapshot, account); err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancel(context.Background())
	id, err := a.registerJob(k, account.Email, "", cancel)
	if err != nil {
		cancel()
		return "", err
	}

	go func() {
		defer cancel()
		log := a.jobLogger(id)
		result, proxy, runErr := a.runJob(ctx, id, k, account, log)
		a.finishJob(id, k, account, result, runErr, ctx.Err() != nil)
		// Every single-account entry point in app.py prunes the exit that failed
		// its precheck from the SAVED pool (17824 / 17862 / 17923 / 17954), so the
		// next click does not walk into the same dead proxy.
		a.dropFailedStandaloneProxy(proxy, runErr, log)
	}()

	return id, nil
}

func (k JobKind) isEntryPoint() bool {
	switch k {
	case JobRegister, JobAuthOnly, JobTeam, JobRegisterAndRT, JobRelink:
		return true
	}
	return false
}

// isBatchParent is "this job owns a bounded pool and dispatches children".
// Separate from isEntryPoint because a parent runs no worker of its own.
func (k JobKind) isBatchParent() bool {
	return k == JobBatchRegister || k == JobBatchLink ||
		k == JobBatchRelink ||
		k == JobProtocolRegisterBatch || k == JobOAuthAuthorizeBatch ||
		k == JobTeamInviteScanJoinBatch || k == JobK12AcceptRefreshBatch ||
		k == JobK12RegisterJoinBatch
}

// jobLogger 在保留可见 [job-N] 前缀的同时，用任务登记时保存的邮箱显式路由
// 结构化记录。若只把 job 前缀拼到文本前面，logs.InferEmail 无法越过它读取
// 后面的 [account@example.com]，账户日志就会静默落入全局缓冲区。
func (a *App) jobLogger(id string) func(string) {
	registeredEmail := ""
	if a.jobs != nil {
		a.jobs.mu.Lock()
		if registered := a.jobs.jobs[id]; registered != nil {
			registeredEmail = registered.logEmail
		}
		a.jobs.mu.Unlock()
	}
	return func(line string) {
		routeEmail := registeredEmail
		// 批量父任务本身没有单一邮箱，但其逐账号消息仍沿用
		// "[account@example.com] ..." 约定。先剥离该内部路由前缀，再把
		// job id 放到可见文本前；账户子任务则始终以登记邮箱为准。
		if inferred, text := applogs.SplitPrefix(line); inferred != "" {
			if routeEmail == "" {
				routeEmail = inferred
			}
			line = text
		}
		a.log(fmt.Sprintf("[%s] %s", id, line), routeEmail)
	}
}

// registerJob creates the registry entry and emits it. batchID is "" for a
// standalone run and the parent's id for one account of a batch.
//
// Two refusals live here rather than in the callers, so the batch orchestrator
// gets both:
//
//   - The same ADDRESS twice. Python cannot produce that collision — its fan-out
//     iterates a selection, so an address appears once — but a webview can, and
//     two runs on one account means two browsers logging it in from two exits at
//     once, each able to rent its own billable number.
//   - A second BATCH while one is running: app.py:16684's `if self.running`.
//     That guard is batch-level, not account-level — inside it Python immediately
//     fans out across _auth_concurrency() threads (16696-16708) — so it forbids a
//     second 批量 run and nothing else. Without it two batches over disjoint
//     selections each open their own 认证 window and the machine runs 2 ×
//     AuthConcurrency browsers, which is the bound's whole purpose.
//
// Both are checked under the registry mutex so two concurrent callers cannot
// both pass. A standalone run is deliberately NOT serialised against a batch:
// Python's guard only gates the 批量 buttons, and refusing here would be
// strictly more restrictive than the Tk app.
func (a *App) registerJob(kind JobKind, email, batchID string, cancel context.CancelFunc) (string, error) {
	return a.registerJobWithLogEmail(kind, email, email, batchID, cancel)
}

// registerJobWithLogEmail 将任务冲突身份/view.Email 与日志账户路由分开。
// 大多数任务二者相同；非账户任务仍可利用 Email 字段做稳定冲突检测，而不会
// 在账户日志存储中创建手机号等伪邮箱分组。
func (a *App) registerJobWithLogEmail(kind JobKind, email, logEmail, batchID string, cancel context.CancelFunc) (string, error) {
	a.jobs.mu.Lock()
	for _, existing := range a.jobs.jobs {
		if existing.view.Status != StatusRunning {
			continue
		}
		if email != "" && strings.EqualFold(existing.view.Email, email) {
			a.jobs.mu.Unlock()
			return "", fmt.Errorf("该账号已有任务在运行: %s", email)
		}
		if kind.isBatchParent() && existing.view.Kind.isBatchParent() {
			a.jobs.mu.Unlock()
			return "", fmt.Errorf("已有批量任务在运行，请先等待其结束或点击停止")
		}
	}
	a.jobs.seq++
	id := fmt.Sprintf("job-%d", a.jobs.seq)
	j := &job{
		seq:      a.jobs.seq,
		logEmail: logEmail,
		view: JobView{
			ID:      id,
			Kind:    kind,
			Email:   email,
			BatchID: batchID,
			Status:  StatusRunning,
			Started: time.Now().Format(time.RFC3339),
		},
		cancel: cancel,
	}
	a.jobs.jobs[id] = j
	a.jobs.mu.Unlock()

	a.emitJob(j)
	return id, nil
}

// runJob dispatches to the ported entry point. Each returns a different shape,
// which is why the result is kept as `any` and fetched separately.
//
// The middle return is the dynamic exit the run actually left through — the
// caller needs it to prune a dead proxy from the pool, and it cannot recompute
// it, because entryDynamicProxy ROTATES.
func (a *App) runJob(ctx context.Context, id string, kind JobKind, account models.MailAccount, log func(string)) (any, string, error) {
	return a.runJobWithProxy(ctx, id, kind, account, nil, log)
}

// runJobWithProxy is runJob with the run's exit forced, which is what a batch
// attempt needs. proxy is nil for a standalone run.
func (a *App) runJobWithProxy(ctx context.Context, id string, kind JobKind, account models.MailAccount, proxy *string, log func(string)) (any, string, error) {
	return a.runJobWithProxyRoutes(ctx, id, kind, account, proxy, nil, log)
}

// runJobWithProxyRoutes 是批量重新获取长链使用的显式路由版本。
// linkTriple 只对 JobRelink 有意义；其他入口保持 nil。
func (a *App) runJobWithProxyRoutes(
	ctx context.Context,
	id string,
	kind JobKind,
	account models.MailAccount,
	proxy *string,
	linkTriple *[3]string,
	log func(string),
) (any, string, error) {
	cfg, res, err := a.workerConfigProxyRoutes(ctx, kind, account, proxy, linkTriple, log)
	if err != nil {
		return nil, "", err
	}
	used := cfg.RegisterProxy.DynamicProxy
	parkedBefore := worker.ParkedBrowserGeneration(account.Email)
	// 普通任务结束即回收资源；若 worker 把浏览器保留给用户，则必须把
	// chain_url 对应的本地代理链一并移交，否则窗口仍在但所有网络请求会断。
	defer func() {
		if worker.AttachParkedCleanupSince(account.Email, parkedBefore, res.Close) {
			return
		}
		res.Close()
	}()
	// Bound to the job id so AnswerPrompt can find the waiting worker, and to
	// ctx so cancelling the job releases a prompt nobody answered.
	//
	// Per entry point, like every other capability: Python passes input_callback
	// only at 17739/17797, so a Team or relink run that needs manual input must
	// fail fast rather than park for the full prompt timeout with no UI behind it.
	if entryPointCaps(kind).inputCallback {
		cfg.InputCallback = a.inputCallback(ctx, id)
	}
	w := worker.New(cfg)

	switch kind {
	case JobRegister:
		result, err := w.Run(ctx)
		return result, used, err
	case JobAuthOnly:
		return nil, used, w.RunAuthOnly(ctx)
	case JobTeam:
		result, err := w.RunTeam(ctx)
		return result, used, err
	case JobRegisterAndRT:
		result, err := w.RunRegisterAndAuthorizeRT(ctx)
		return result, used, err
	case JobRelink:
		result, err := w.Relink(ctx)
		return result, used, err
	}
	return nil, used, fmt.Errorf("未处理的任务类型: %s", kind)
}

func (a *App) finishJob(id string, kind JobKind, account models.MailAccount, result any, runErr error, cancelled bool) {
	// Persist FIRST, and before the job is marked finished: the money is already
	// spent, and a frontend that sees 成功 then reloads the account table must not
	// find the old row. A write failure is reported as the job's error rather
	// than swallowed — a run whose result never reached disk did not succeed in
	// any sense the user cares about.
	if err := a.persistRunOutcome(kind, account, result, runErr, cancelled); err != nil {
		a.jobLogger(id)(fmt.Sprintf("保存运行结果失败: %v", err))
		if runErr == nil && !cancelled {
			runErr = err
		}
	}
	if runErr == nil && !cancelled && resultCarriesLink(result) {
		a.handleLinkSuccess(id, account.Email)
	}
	a.markJobFinished(id, result, runErr, cancelled)
}

// resultCarriesLink 对齐 Python results 事件的 link_url 判断：只有本次结果
// 明确携带非空 URL 才播放提示音或暂停任务，旧状态中已有长链不算本次成功。
func resultCarriesLink(result any) bool {
	payload := resultPayload(result)
	return strings.TrimSpace(settings.PyStr(pyOrEmpty(payload["url"]))) != ""
}

// handleLinkSuccess 对齐 app.py `_handle_link_success`。提示音通过事件交给
// WebView 播放；“暂停其他账户”则必须在 Go 任务注册表中执行，避免前端卡顿
// 或窗口失焦时继续消耗代理、租号和 checkout。
func (a *App) handleLinkSuccess(currentJobID, email string) {
	snapshot, err := a.snapshot()
	if err != nil {
		a.jobLogger(currentJobID)(fmt.Sprintf("读取长链成功设置失败: %v", err))
		return
	}
	st := settings.FromSnapshot(snapshot)
	if st.SuccessSoundEnabled && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, EventLinkSuccess, LinkSuccessEvent{
			Email:       email,
			AudioDevice: st.SuccessAudioDevice,
		})
	}
	if !st.PauseOthersOnLinkSuccess {
		return
	}

	a.jobs.mu.Lock()
	current := a.jobs.jobs[currentJobID]
	cancelCurrentParent := current != nil && current.view.Kind.isBatchParent()
	var (
		cancels []context.CancelFunc
		prompts []chan string
	)
	for id, candidate := range a.jobs.jobs {
		if candidate.view.Status != StatusRunning {
			continue
		}
		shouldCancel := id != currentJobID
		// 批量提链没有逐账号注册项，父任务本身就是其他并发尝试的取消源。
		// 因此命中成功时也要取消父 context；父任务最终显示“已停止”，已成功
		// 账号的结果已经先持久化，不会丢失。
		if id == currentJobID && cancelCurrentParent {
			shouldCancel = true
		}
		if !shouldCancel {
			continue
		}
		cancels = append(cancels, candidate.cancel)
		if candidate.prompt != nil {
			prompts = append(prompts, candidate.prompt)
			candidate.prompt = nil
		}
	}
	a.jobs.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, prompt := range prompts {
		select {
		case prompt <- "":
		default:
		}
	}
	a.Log(fmt.Sprintf("账号 %s 长链已提取，已暂停其他账户继续尝试", email))
}

// markJobFinished is the registry half of finishJob: status, timestamps, event.
// Split out so the persistence step above has exactly one caller and cannot be
// reached by anything that did not actually run a worker.
func (a *App) markJobFinished(id string, result any, runErr error, cancelled bool) {
	a.jobs.mu.Lock()
	j := a.jobs.jobs[id]
	if j == nil {
		a.jobs.mu.Unlock()
		return
	}
	j.result = result
	j.view.Finished = time.Now().Format(time.RFC3339)
	switch {
	case cancelled:
		j.view.Status = StatusCancelled
	case runErr != nil:
		j.view.Status = StatusFailed
		j.view.Error = runErr.Error()
	default:
		j.view.Status = StatusSucceeded
	}
	a.jobs.mu.Unlock()

	a.emitJob(j)
}

// CancelJob cancels a running job's context. The worker entry points thread the
// context through, so this unwinds the run rather than orphaning a browser.
func (a *App) CancelJob(id string) error {
	// view.Status is written by finishJob under this same mutex, so it must be
	// READ under it too — otherwise this races the completing goroutine.
	a.jobs.mu.Lock()
	j := a.jobs.jobs[id]
	var (
		cancel  context.CancelFunc
		running bool
	)
	if j != nil {
		cancel = j.cancel
		running = j.view.Status == StatusRunning
	}
	a.jobs.mu.Unlock()

	if j == nil {
		return fmt.Errorf("任务不存在: %s", id)
	}
	if !running {
		return fmt.Errorf("任务已结束: %s", id)
	}
	cancel()
	return nil
}

// jobView snapshots one job under the registry mutex.
func (a *App) jobView(id string) (JobView, bool) {
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()
	j := a.jobs.jobs[id]
	if j == nil {
		return JobView{}, false
	}
	return j.view, true
}

// ListJobs returns every job this session, newest first.
func (a *App) ListJobs() []JobView {
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()

	type row struct {
		seq  int
		view JobView
	}
	rows := make([]row, 0, len(a.jobs.jobs))
	for _, j := range a.jobs.jobs {
		rows = append(rows, row{seq: j.seq, view: j.view})
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].seq > rows[k].seq })

	out := make([]JobView, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view)
	}
	return out
}

func (a *App) emitJob(j *job) {
	if a.ctx == nil {
		return
	}
	a.jobs.mu.Lock()
	view := j.view
	a.jobs.mu.Unlock()
	wailsruntime.EventsEmit(a.ctx, EventJob, view)
}
