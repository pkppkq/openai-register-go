package ui

// UI_SPEC gap G2: the bound methods behind the first-slice screens (UI_SPEC §6).
// Each one mirrors a Tk handler and cites it.
//
// Two rules shape every method in this file:
//
//   - A bound method runs on the Wails call thread. Anything that can block for
//     longer than a disk write goes through jobRegistry and returns a JobView;
//     the frontend then follows the run over the `log` / `job` event streams.
//   - state.json is shared with the still-running Python app, so every write
//     re-reads the file first and hands the FULL prior snapshot to
//     settings.ToSnapshot. See mutateState.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pkppkq/openai-register-go/internal/accounts"
	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/importer"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// EventPrompt carries a PromptRequest when a worker blocks on human input.
const EventPrompt = "prompt"

// stateTimeFormat is datetime.now().isoformat(timespec="seconds")
// (app.py:14227): local time, second precision, no offset.
const stateTimeFormat = "2006-01-02T15:04:05"

// promptTimeout bounds a manual-input round-trip. UI_SPEC §4 flags the Python
// version's *untimed* prompt as a defect: a worker that raises a dialog nobody
// answers holds its browser and its proxy forever. The value matches the
// longest human wait the Tk app allows elsewhere (手动登录取码, 600 s).
const promptTimeout = 10 * time.Minute

// ---------------------------------------------------------------------------
// Snapshot helpers
// ---------------------------------------------------------------------------

// mutateState is the one write path into state.json.
//
// It re-reads the file inside a.mu rather than accepting a caller-held
// snapshot: the Python app writes the same file, so a snapshot carried across a
// user's editing session would silently revert whatever Python wrote meanwhile.
// fn returns the snapshot to persist, the session emails whose split file must
// be rewritten, and any refusal.
//
// The dirty set is never passed through as nil — state.Store.Save reads nil as
// "every session is dirty" and would rewrite all ~150 files under
// state_data/sessions/ on a settings save.
func (a *App) mutateState(flush bool, fn func(map[string]any) (map[string]any, map[string]bool, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	prior, err := a.store.Load()
	if err != nil {
		return fmt.Errorf("读取 state.json 失败: %w", err)
	}
	// Load nils Warnings, so the baseline has to be taken after it.
	before := len(a.store.Warnings)
	next, dirty, err := fn(prior)
	if errors.Is(err, errNoStateChange) {
		// app.py:21008 returns WITHOUT calling save_state(). Stamping updated_at and
		// writing anyway would rewrite the file on every no-op — and this path fires
		// on every proxy handshake, against a file the Python app also holds.
		return nil
	}
	if err != nil {
		return err
	}
	if dirty == nil {
		dirty = map[string]bool{}
	}
	// Python stamps this in _build_state_snapshot; settings.ToSnapshot
	// deliberately leaves it to whoever owns the clock, which is here.
	next["updated_at"] = time.Now().Format(stateTimeFormat)
	a.store.Save(next, dirty, flush)
	// Store.Save swallows a write failure into Warnings, and the next Load clears
	// them — so without this a locked or full disk is reported to the user as a
	// successful save.
	return a.storeWriteError(before)
}

// errNoStateChange tells mutateState the callback decided nothing needs writing.
var errNoStateChange = errors.New("state unchanged")

// storeWriteError surfaces warnings Store accumulated during this write. Store
// reports failures by appending to Warnings and returns nothing (state.go:164), and
// Load nils the slice, so they have to be read immediately or they are lost.
func (a *App) storeWriteError(before int) error {
	if len(a.store.Warnings) <= before {
		return nil
	}
	return fmt.Errorf("写入 state.json 失败: %s", strings.Join(a.store.Warnings[before:], "; "))
}

// accountsFromSnapshot decodes state.json's "accounts" array (app.py:14031).
// A row that is not an object is skipped rather than failing the whole load.
func accountsFromSnapshot(snapshot map[string]any) []models.MailAccount {
	rows, _ := snapshot["accounts"].([]any)
	out := make([]models.MailAccount, 0, len(rows))
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			out = append(out, models.AccountFromMap(m))
		}
	}
	// Python 在每次加载 state 后立即把全局 Cloud Mail 设置应用到内存账号，
	// 但 account_to_dict 有意不持久化 base/token。Go 每次操作都重新解码
	// snapshot，因此必须在这个统一入口恢复同一份运行时字段；否则域名邮箱
	// 保存后会在下一次 startJob 的预检中变成“Cloud Mail未配置”。
	st := settings.FromSnapshot(snapshot)
	pointers := make([]*models.MailAccount, 0, len(out))
	for index := range out {
		pointers = append(pointers, &out[index])
	}
	alias.ApplyCloudMailRuntimeConfig(
		pointers,
		st.CloudMailBase,
		st.CloudMailToken,
		st.CloudMailEnabled,
	)
	return out
}

func accountsToSnapshot(accs []models.MailAccount) []any {
	rows := make([]any, 0, len(accs))
	for _, acc := range accs {
		rows = append(rows, models.AccountToMap(acc))
	}
	return rows
}

// lookupsFromSnapshot wires the three side tables the derived 状态 column reads
// (app.py:14034-14038). The keys stay exactly as they are on disk, because
// accounts.Lookups is documented to key off the RAW account email.
func lookupsFromSnapshot(snapshot map[string]any) accounts.Lookups {
	return accounts.Lookups{
		Results:        resultsFromSnapshot(snapshot),
		SessionResults: sessionResultsFromSnapshot(snapshot),
		LinkAttempts:   subMap(snapshot, "link_attempt_counts"),
	}
}

func subMap(snapshot map[string]any, key string) map[string]any {
	m, _ := snapshot[key].(map[string]any)
	return m
}

// resultsFromSnapshot is app.py:14034:
//
//	self.results = {str(k): str(v) for k, v in data.get("results", {}).items() if v}
//
// The `if v` is load-bearing in both directions and is NOT a tidy-up:
//
//   - It drops falsy values, so a row whose link was cleared to "" (or to a JSON
//     null, or 0) is treated as HAVING NO LINK. Keeping it would make
//     Lookups.Results[email] a present-but-empty entry, and the status event's
//     `has_link` guard (app.py:18618) would then suppress every failure status
//     for that account — the account would sit at a stale 长链已提取 while its
//     runs kept failing.
//   - str(v) is Python's str(), so a non-string survives as text rather than
//     being dropped by a Go type assertion.
func resultsFromSnapshot(snapshot map[string]any) map[string]any {
	raw := subMap(snapshot, "results")
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if !settings.PyTruthy(v) {
			continue
		}
		out[k] = settings.PyStr(v)
	}
	return out
}

// sessionResultsFromSnapshot is app.py:14035-14036 — only dict values survive.
func sessionResultsFromSnapshot(snapshot map[string]any) map[string]any {
	raw := subMap(snapshot, "session_results")
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out[k] = m
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ListAccounts — S9 account table
// ---------------------------------------------------------------------------

// AccountFilter is the account-table filter bar plus paging. The zero value
// means "everything, in list order": an empty Group/Status/Search filters
// nothing and an empty SortColumn/SortDirection is accounts.SortCustom, which
// preserves the manual drag order (app.py:19725-19731).
//
// The persisted filter/sort values live in settings (account_group_filter,
// account_status_filter, account_sort_column, account_sort_direction); the
// frontend reads them from LoadSettings and passes them back here rather than
// this method silently defaulting to them, so a deliberate "show everything"
// stays distinguishable from "no preference".
type AccountFilter struct {
	Group         string `json:"group"`
	Status        string `json:"status"`
	Search        string `json:"search"`
	SortColumn    string `json:"sortColumn"`
	SortDirection string `json:"sortDirection"`
	Offset        int    `json:"offset"`
	// Limit <= 0 returns every matching row.
	Limit int `json:"limit"`
}

// AccountRow is one rendered table row.
//
// The persisted half carries the account's state.json field names unchanged
// (models.AccountToMap) so the frontend and the Python app talk about the same
// keys. browser_fingerprint is deliberately not included: it is a ~15-field
// spoofing blob no screen renders, and shipping it on every row would dominate
// the payload.
//
// The derived half is UI_SPEC §1.6 / §7.1 and is NOT persisted anywhere —
// StatusText in particular is computed per render, so `status` (stored) and
// `statusText` (displayed) routinely differ.
type AccountRow struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	ClientID        string `json:"client_id"`
	RefreshToken    string `json:"refresh_token"`
	Raw             string `json:"raw"`
	AccountType     string `json:"account_type"`
	Status          string `json:"status"`
	OpenaiRT        string `json:"openai_rt"`
	AuthPhoneNumber string `json:"auth_phone_number"`
	AuthPhoneSMSURL string `json:"auth_phone_sms_url"`
	ReceiveMailbox  string `json:"receive_mailbox"`
	MailProvider    string `json:"mail_provider"`
	Group           string `json:"group"`

	// Key is the stable row identity: the lowercased email. UI_SPEC §0.3 —
	// never address a row by its index.
	Key        string `json:"key"`
	StatusText string `json:"statusText"`
	Attempts   int    `json:"attempts"`
	HasSession bool   `json:"hasSession"`
	Link       string `json:"link"`
}

// AccountPage is one page of the table plus the counts the header needs
// (account_summary_var, `账户 N · 显示 M`, app.py:12352).
type AccountPage struct {
	Rows    []AccountRow `json:"rows"`
	Total   int          `json:"total"`
	Matched int          `json:"matched"`
	Offset  int          `json:"offset"`
	// Groups is the 分组 combo's contents, folded from settings.account_groups
	// and every group an account references (app.py:14052-14064).
	Groups []string `json:"groups"`
}

// ListAccounts renders the account table: _account_display_indices
// (app.py:19725-19731) over _account_visible_indices (app.py:19108-19134).
func (a *App) ListAccounts(filter AccountFilter) (AccountPage, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return AccountPage{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	all := accountsFromSnapshot(snapshot)
	lk := lookupsFromSnapshot(snapshot)

	display := accounts.Display(all, accounts.Filter{
		Group:  filter.Group,
		Status: filter.Status,
		Search: filter.Search,
	}, filter.SortColumn, filter.SortDirection, lk)

	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(display) {
		offset = len(display)
	}
	end := len(display)
	if filter.Limit > 0 && offset+filter.Limit < end {
		end = offset + filter.Limit
	}

	page := AccountPage{
		Rows:    make([]AccountRow, 0, end-offset),
		Total:   len(all),
		Matched: len(display),
		Offset:  offset,
		Groups:  settings.FromSnapshot(snapshot).AccountGroups,
	}
	for _, acc := range display[offset:end] {
		page.Rows = append(page.Rows, accountRow(acc, lk))
	}
	return page, nil
}

func accountRow(acc models.MailAccount, lk accounts.Lookups) AccountRow {
	row := accounts.RowOf(acc, lk)
	link, _ := lk.Results[acc.Email].(string)
	return AccountRow{
		Email:           acc.Email,
		Password:        acc.Password,
		ClientID:        acc.ClientID,
		RefreshToken:    acc.RefreshToken,
		Raw:             acc.Raw,
		AccountType:     acc.AccountType,
		Status:          acc.Status,
		OpenaiRT:        acc.OpenaiRT,
		AuthPhoneNumber: acc.AuthPhoneNumber,
		AuthPhoneSMSURL: acc.AuthPhoneSMSURL,
		ReceiveMailbox:  acc.ReceiveMailbox,
		MailProvider:    acc.MailProvider,
		Group:           acc.Group,

		Key:        row.Key,
		StatusText: row.Status,
		Attempts:   row.Attempts,
		HasSession: lk.HasSession(acc.Email),
		Link:       link,
	}
}

// ---------------------------------------------------------------------------
// ImportAccounts — S14 导入账号
// ---------------------------------------------------------------------------

// ImportResult is what the 导入账号 button reports back.
type ImportResult struct {
	// Imported counts successfully parsed lines, Added/Updated split them by
	// whether the email was already known.
	Imported int `json:"imported"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	// Total is the account count after the merge.
	Total int `json:"total"`
	// Group is the group new accounts landed in.
	Group  string   `json:"group"`
	Errors []string `json:"errors"`
	// Message is the log line app.py:14721 writes, verbatim.
	Message string `json:"message"`
}

// ImportAccounts ports import_accounts (app.py:14686-14721).
//
// The merge itself is importer.MergeInto (app.py:14701-14717): an existing row
// keeps its worker- and user-owned fields (account_type, status, group) and
// only gains credentials, so re-pasting an export never resets a registration.
func (a *App) ImportAccounts(text string) (ImportResult, error) {
	parsed, lineErrs := importer.ParseText(text)

	out := ImportResult{Imported: len(parsed), Errors: []string{}}
	for _, e := range lineErrs {
		out.Errors = append(out.Errors, e.Error())
	}
	if len(parsed) == 0 && len(lineErrs) == 0 {
		return out, errors.New("请先粘贴邮箱账户") // app.py:14689
	}

	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		existing := accountsFromSnapshot(snapshot)
		// importer.MergeKey, NOT accounts.Key: the two normalise differently and
		// the bookkeeping below has to agree with the merge it is reporting on.
		known := make(map[string]bool, len(existing))
		for _, acc := range existing {
			known[importer.MergeKey(acc.Email)] = true
		}

		// app.py:14693-14694 — new rows land in the group currently being
		// filtered on, unless that is one of the two pseudo-groups.
		group := settings.FromSnapshot(snapshot).AccountGroupFilter
		if group == accounts.GroupAll || group == accounts.GroupDefault {
			group = accounts.GroupDefault
		}
		out.Group = group

		sessions, ok := snapshot["session_results"].(map[string]any)
		if !ok {
			sessions = map[string]any{}
		}
		dirty := map[string]bool{}
		for _, acc := range parsed {
			key := importer.MergeKey(acc.Email)
			if known[key] {
				out.Updated++
				continue
			}
			// A paste may name the same mailbox twice; Python re-scans the live
			// list every iteration, so only the first occurrence is an add.
			known[key] = true
			out.Added++
			// app.py:14715-14717 — Store.Load skips the split session file of
			// an account that is not in the list, so re-importing a mailbox has
			// to pull its payload back in explicitly or its Session is lost.
			if payload := a.store.LoadDeferredSession(acc.Email); payload != nil {
				sessions[acc.Email] = payload
				dirty[acc.Email] = true
			}
		}

		merged := importer.MergeInto(existing, parsed, group)
		snapshot["accounts"] = accountsToSnapshot(merged)
		snapshot["session_results"] = sessions
		out.Total = len(merged)
		return snapshot, dirty, nil
	})
	if err != nil {
		return out, err
	}

	out.Message = fmt.Sprintf("已导入 %d 个邮箱", out.Imported) // app.py:14721
	if len(out.Errors) > 0 {
		out.Message += "；失败: " + strings.Join(out.Errors, "; ")
	}
	a.Log(out.Message)
	return out, nil
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// LoadSettings decodes the 60 persisted keys (GUI.load_state, app.py:14039-14213).
func (a *App) LoadSettings() (settings.Settings, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return settings.Settings{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	return settings.FromSnapshot(snapshot), nil
}

// SaveSettings persists the settings object (GUI._build_state_snapshot,
// app.py:14225-14299).
//
// The write is flushed rather than debounced: the 1.5 s debounce exists for the
// per-status event storm (UI_SPEC §4.2), not for a button the user pressed once.
//
// `prior` is the snapshot just read off disk, so every key this package does not
// model — at the top level, inside "settings" and inside each
// provider_proxy_configs role — survives the round trip.
func (a *App) SaveSettings(s settings.Settings) error {
	err := a.mutateState(true, func(prior map[string]any) (map[string]any, map[string]bool, error) {
		return settings.ToSnapshot(s, prior), map[string]bool{}, nil
	})
	if err == nil && s.ProxyRouteMode == settings.ProxyRouteModeLocalOnly && a.providerManager != nil {
		a.providerManager.Stop()
		a.Log("当前为“全走本地代理”，已停止并忽略提供商代理池")
	}
	return err
}

// ---------------------------------------------------------------------------
// Job entry points
// ---------------------------------------------------------------------------

// StartRegisterRequest drives _start_worker (app.py:16683).
//
// One account per call, and one browser. This is NOT the binding a multi-account
// selection should use: batch orchestration — the bounded 认证 window, the
// per-attempt proxy and 代理耗尽 on exhaustion — is StartBatchRegister
// (batch.go). Looping this one over a selection launches every browser at once,
// which is exactly what _run_accounts (app.py:17609) exists to prevent.
type StartRegisterRequest struct {
	Email     string `json:"email"`
	Confirmed bool   `json:"confirmed"`
	// CollectSession is _start_worker's collect_session: true reads the Session
	// back (注册取 Session, app.py:15115), false stops after login and keeps the
	// window open (注册或登录, app.py:15131).
	CollectSession bool `json:"collectSession"`
}

// StartRegister starts a registration job and returns immediately.
//
// 该流程可能租用付费手机号，后端必须收到明确确认。
func (a *App) StartRegister(req StartRegisterRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("启动注册或登录前必须确认")
	}
	kind := JobAuthOnly
	if req.CollectSession {
		kind = JobRegister
		// app.py:17718 branches on the account BEFORE running:
		//   if account.account_type == "team" and collect_session:
		//       return self._run_team_account_once(...)
		// A Team account put through the plain register flow runs the wrong
		// money-spending flow entirely — Worker.Run registers and reads a session
		// where RunTeam does Team SSO signup and takes the RT. Resolving the
		// account here costs one state read that startJob is about to do anyway.
		account, err := a.accountByEmail(req.Email)
		if err != nil {
			return JobView{}, err
		}
		if strings.EqualFold(strings.TrimSpace(account.AccountType), "team") {
			kind = JobTeam
		}
	}
	return a.startJobView(kind, req.Email)
}

// GenerateLinksRequest drives refetch_selected_link (app.py:15205-15239).
//
// 每次只处理一个账号。这里是“重新获取”（§2 #51），不是“批量提链”
// （§2 #50）；后者由 StartBatchGenerateLinks 统一重置撞链次数并调度三段代理。
type GenerateLinksRequest struct {
	Email     string `json:"email"`
	Confirmed bool   `json:"confirmed"`
}

// GenerateLinks starts a payment-link job and returns immediately.
//
// 该操作会创建真实支付链接，后端必须收到明确确认。
func (a *App) GenerateLinks(req GenerateLinksRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("重新获取支付链接前必须确认")
	}
	return a.startJobView(JobRelink, req.Email)
}

func (a *App) startJobView(kind JobKind, email string) (JobView, error) {
	id, err := a.startJob(string(kind), email)
	if err != nil {
		return JobView{}, err
	}
	view, ok := a.jobView(id)
	if !ok {
		// Unreachable: nothing removes a job from the registry.
		return JobView{}, fmt.Errorf("任务已丢失: %s", id)
	}
	return view, nil
}

// StopAll ports stop_current_task (app.py:15091-15113): cancel everything in
// flight and release every worker blocked on a prompt.
//
// Safe to call with nothing running — the Tk version is too, and 停止 is the
// button a user mashes when they are not sure what is running.
//
// UI_SPEC §4.2 flags Python's single global stop_event as a real bug: the first
// task to finish clears it for everyone. Cancelling per-job contexts has no
// such shared state.
func (a *App) StopAll() error {
	a.jobs.mu.Lock()
	var (
		cancels []context.CancelFunc
		prompts []chan string
	)
	for _, j := range a.jobs.jobs {
		// view.Status is written by finishJob under this mutex, so it is read
		// under it too.
		if j.view.Status == StatusRunning {
			cancels = append(cancels, j.cancel)
		}
		if j.prompt != nil {
			prompts = append(prompts, j.prompt)
			j.prompt = nil
		}
	}
	a.jobs.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	// app.py:15103-15108 — an unanswered prompt gets "", which every caller
	// reads as "user cancelled". The channels are buffered, so this cannot
	// block even if the worker already gave up on its ctx.
	for _, ch := range prompts {
		select {
		case ch <- "":
		default:
		}
	}
	if len(cancels) > 0 {
		a.Log("已请求停止当前任务") // app.py:15110
	}
	return nil
}

// ---------------------------------------------------------------------------
// Manual input round-trip (UI_SPEC §4.2, S26)
// ---------------------------------------------------------------------------

// PromptRequest is emitted on EventPrompt when a worker needs human input:
// _request_user_input (app.py:16401-16406) plus its render at app.py:19000-19015.
//
// Keyed by job rather than by a separate prompt id: InputCallback blocks the
// job's single goroutine, so a job can never have two prompts outstanding, and
// one key means the frontend cannot answer a prompt that has already timed out
// with a stale id.
type PromptRequest struct {
	JobID string `json:"jobId"`
	// Kind is the prompt_type: "email-code", "phone", "phone-code", ...
	Kind   string `json:"kind"`
	Email  string `json:"email"`
	Prompt string `json:"prompt"`
}

// inputCallback builds the worker.InputFunc for one job. It blocks the worker
// goroutine — never the Wails call thread.
func (a *App) inputCallback(ctx context.Context, id string) worker.InputFunc {
	return func(kind, email, prompt string) string {
		// Buffered so AnswerPrompt never blocks on a worker that has already
		// given up.
		ch := make(chan string, 1)

		a.jobs.mu.Lock()
		j := a.jobs.jobs[id]
		if j != nil {
			j.prompt = ch
		}
		a.jobs.mu.Unlock()
		if j == nil {
			return ""
		}

		a.emitPrompt(PromptRequest{JobID: id, Kind: kind, Email: email, Prompt: prompt})

		timer := time.NewTimer(promptTimeout)
		defer timer.Stop()

		var answer string
		select {
		case answer = <-ch:
		case <-ctx.Done():
		case <-timer.C:
			a.jobLogger(id)("等待人工输入超时，已按取消处理")
		}

		a.jobs.mu.Lock()
		// Only clear our own channel: StopAll may already have detached it and
		// a later prompt may already have installed its own.
		if j.prompt == ch {
			j.prompt = nil
		}
		a.jobs.mu.Unlock()
		return answer
	}
}

// AnswerPrompt delivers the user's reply, unblocking the worker (§2 #114, the
// result_queue.put at app.py:19013-19015).
func (a *App) AnswerPrompt(jobID string, answer string) error {
	a.jobs.mu.Lock()
	j := a.jobs.jobs[jobID]
	var ch chan string
	if j != nil {
		ch = j.prompt
		j.prompt = nil
	}
	a.jobs.mu.Unlock()

	if j == nil {
		return fmt.Errorf("任务不存在: %s", jobID)
	}
	if ch == nil {
		return fmt.Errorf("任务当前没有等待输入的请求: %s", jobID)
	}
	select {
	case ch <- answer:
	default:
	}
	return nil
}

func (a *App) emitPrompt(req PromptRequest) {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, EventPrompt, req)
}
