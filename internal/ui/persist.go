package ui

// Persisting the outcome of a run.
//
// The Tk app never wrote a run result directly. The worker thread pushed
// ("account-updated", …), ("result", …) and ("status", …) onto self.events, and
// the drain loop at app.py:18605-18695 translated those into mutations of
// self.results / self.session_results / the MailAccount, then called
// save_state(). Wails has no such loop — a job finishes on its own goroutine —
// so the translation lives here and runs once, at the end of the job.
//
// Doing it once rather than per event is deliberate and is the ONLY divergence:
// Python's intermediate saves leave a partially-updated file behind if the app
// dies mid-run, and its status handler runs after an already-completed
// save_state() so the final status is only persisted by whatever saves next.
// One write at the end reaches the same end state without either hazard.
//
// Everything else is reproduced exactly, because the money is already spent by
// the time this runs: a mistake here silently discards the result of a paid run.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// Statuses the run outcome writes. Kept as named constants because several are
// also read back (the failure set below, and the 邮箱锁定 sibling scan).
const (
	statusSessionCollected = "Session已获取" // app.py:17750
	statusLoggedIn         = "已登录"        // app.py:17752
	statusTeamRTCollected  = "Team RT已获取" // app.py:17854
	statusRTCollected      = "RT已获取"      // app.py:17812 fallback
	// Set by the result handler and then immediately overwritten by the status
	// event that follows it on every path, so it never survives a completed run.
	// Kept because it IS a persisted value — 批量提链 (G7) writes it and leaves it.
	statusLinkExtracted  = "长链已提取" // app.py:18688
	statusRelinkOK       = "成功"    // app.py:17913
	statusCloudMailUnset = "Cloud Mail未配置"
	statusEmailLocked    = alias.AccountEmailLockedStatus
)

// rtStatusByPlan is app.py:17805-17811.
var rtStatusByPlan = map[string]string{
	"free": "Free RT已获取",
	"plus": "Plus RT已获取",
	"team": "Team RT已获取",
	"k12":  "K12 RT已获取",
	"pro":  "Pro RT已获取",
}

// planTypesAdopted is app.py:17804 — only these five overwrite account_type.
var planTypesAdopted = map[string]bool{
	"free": true, "plus": true, "team": true, "k12": true, "pro": true,
}

// linkFailureStatuses is the set app.py:18619 refuses to overwrite a successful
// link with. An account that already has a long link keeps 长链已提取 rather than
// being downgraded by a later retry's failure.
var linkFailureStatuses = map[string]bool{
	"提取长链失败": true,
	"代理耗尽":   true,
	"代理非日本":  true,
	"代理检测失败": true,
	"不可自动重试": true,
}

// ---------------------------------------------------------------------------
// The result payload
// ---------------------------------------------------------------------------

// resultPayload turns a worker return value into the dict the Python event
// carried. The JSON tags on SessionInfo / AuthResult / PayLinkResult ARE the
// keys of the dicts app.py:12151, 9035 and relink() build, so a marshal/unmarshal
// round trip reproduces the payload exactly — including WHICH KEYS ARE PRESENT,
// which the merge below depends on.
//
// A nil pointer in a non-nil interface (what `w.Run(ctx)` returns on the
// keep-window-open path, app.py:8964) marshals to `null`; that is "no payload",
// same as Python returning None.
func resultPayload(result any) map[string]any {
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// pick is `str(payload.get(k) or old.get(k) or "")`.
//
// The `or` chain runs on the RAW values and str() is applied ONCE, to whichever
// one wins. Stringifying each candidate first and testing it for "" is the
// obvious-looking translation and it is wrong in three ways that a real
// state.json hits: str(None) is "None", str(False) is "False" and str(0) is "0",
// all of which are non-empty, so a JSON null / false / 0 in the payload would
// win the chain and be persisted as that literal text instead of falling
// through to the previous run's value. Found by differential execution against
// the app.py slice over 6,008 payload/old pairs.
func pick(payload, old map[string]any, keys ...string) string {
	terms := make([]kv, 0, 2*len(keys))
	for _, key := range keys {
		terms = append(terms, kv{payload, key}, kv{old, key})
	}
	return pickFrom(terms...)
}

// kv is one term of an `or` chain: a map and the key to read from it.
type kv struct {
	m   map[string]any
	key string
}

// pickFrom is the general `str(a or b or c or "")`, for the chains whose terms
// are not a regular payload/old alternation.
func pickFrom(terms ...kv) string {
	for _, t := range terms {
		if v := t.m[t.key]; settings.PyTruthy(v) {
			return settings.PyStr(v)
		}
	}
	return ""
}

// pickOverride is the amount keys' rule (app.py:18661-18664):
//
//	str(payload[k] if k in payload else (old.get(k) or ""))
//
// Presence, not truthiness. A relink whose amount check produced nothing returns
// the key as "" and that "" MUST overwrite the previous run's value — otherwise
// a stale 金额一致 from an earlier link is reported for a link that was never
// checked. Run/RunTeam/RunRegisterAndAuthorizeRT do not carry these keys at all,
// so for them the old value survives.
// Note the asymmetry, which is Python's: the payload branch has NO `or ""`, so a
// null there really does persist as the string "None"; the old branch has one,
// so a null there becomes "".
func pickOverride(payload, old map[string]any, key string) string {
	if v, present := payload[key]; present {
		return settings.PyStr(v)
	}
	return settings.PyStr(pyOrEmpty(old[key]))
}

// mergeSessionResult is app.py:18640-18676 verbatim.
//
// It REBUILDS the entry rather than patching it, exactly as Python does: a key
// not listed here is dropped from session_results[email]. The eight
// openai_deactivation_* keys are carried over from the old entry only — a run
// result can never set them, but it must never erase them either.
func mergeSessionResult(old, payload map[string]any) map[string]any {
	if old == nil {
		old = map[string]any{}
	}
	out := map[string]any{
		"access_token":       pick(payload, old, "access_token"),
		"session_json":       pick(payload, old, "session_json"),
		"checkout_url":       pick(payload, old, "checkout_url"),
		"storage_state_json": pick(payload, old, "storage_state_json"),
		"openai_rt":          pick(payload, old, "openai_rt"),

		"link_proxy":       pick(payload, old, "link_proxy"),
		"link_proxy_label": pick(payload, old, "link_proxy_label"),
		"link_proxy_exit":  pick(payload, old, "link_proxy_exit"),

		"link_create_proxy":       pick(payload, old, "link_create_proxy"),
		"link_create_proxy_label": pick(payload, old, "link_create_proxy_label"),
		"link_create_proxy_exit":  pick(payload, old, "link_create_proxy_exit"),

		// The followup trio falls back to the generic link_proxy fields, because a
		// single-chain run records only those (app.py:18653-18655).
		//
		// Read those three lines carefully before "simplifying" this: the fourth
		// `or` term of the first two REPEATS old_session["link_followup_proxy"(_label)]
		// instead of reading old_session["link_proxy"(_label)], while the third one
		// really does read old_session["link_proxy_exit"]. So the OLD entry's
		// generic link_proxy is consulted for the exit and for nothing else.
		//
		// That asymmetry is near-certainly a copy-paste slip in app.py, and it is
		// still the behaviour of the app that shares this state file, so it is
		// reproduced rather than corrected. Differential execution flagged the
		// tidied-up version on 1,042 fields.
		"link_followup_proxy":       pickFrom(kv{payload, "link_followup_proxy"}, kv{old, "link_followup_proxy"}, kv{payload, "link_proxy"}),
		"link_followup_proxy_label": pickFrom(kv{payload, "link_followup_proxy_label"}, kv{old, "link_followup_proxy_label"}, kv{payload, "link_proxy_label"}),
		"link_followup_proxy_exit":  pickFrom(kv{payload, "link_followup_proxy_exit"}, kv{old, "link_followup_proxy_exit"}, kv{payload, "link_proxy_exit"}, kv{old, "link_proxy_exit"}),

		"link_approve_proxy":       pick(payload, old, "link_approve_proxy"),
		"link_approve_proxy_label": pick(payload, old, "link_approve_proxy_label"),
		"link_approve_proxy_exit":  pick(payload, old, "link_approve_proxy_exit"),

		"payment_link_type": pick(payload, old, "payment_link_type"),

		"stripe_amount":        pickOverride(payload, old, "stripe_amount"),
		"stripe_amount_source": pickOverride(payload, old, "stripe_amount_source"),
		"target_amount":        pickOverride(payload, old, "target_amount"),
		"amount_check":         pickOverride(payload, old, "amount_check"),

		"k12_workspace_id": pick(payload, old, "k12_workspace_id"),
		"k12_status":       pick(payload, old, "k12_status"),
		"k12_response":     pick(payload, old, "k12_response"),

		"openai_deactivation_found":      settings.PyStr(pyOrEmpty(old["openai_deactivation_found"])),
		"openai_deactivation_status":     settings.PyStr(pyOrEmpty(old["openai_deactivation_status"])),
		"openai_deactivation_checked_at": settings.PyStr(pyOrEmpty(old["openai_deactivation_checked_at"])),
		"openai_deactivation_subject":    settings.PyStr(pyOrEmpty(old["openai_deactivation_subject"])),
		"openai_deactivation_date":       settings.PyStr(pyOrEmpty(old["openai_deactivation_date"])),
		"openai_deactivation_folder":     settings.PyStr(pyOrEmpty(old["openai_deactivation_folder"])),
		"openai_deactivation_to":         settings.PyStr(pyOrEmpty(old["openai_deactivation_to"])),
		"openai_deactivation_snippet":    settings.PyStr(pyOrEmpty(old["openai_deactivation_snippet"])),
	}
	return out
}

// pyOrEmpty is Python's `x or ""` — needed because str(None) is "None", and these
// eight go through `str(old.get(k) or "")`.
func pyOrEmpty(v any) any {
	if !settings.PyTruthy(v) {
		return ""
	}
	return v
}

// ---------------------------------------------------------------------------
// Applying an outcome to the snapshot
// ---------------------------------------------------------------------------

// runOutcome is what one finished job wants written.
type runOutcome struct {
	// Status is the account's final 状态, or "" to leave it alone.
	Status string
	// Payload is the result dict, or nil for a run that produced none.
	Payload map[string]any
	// AccountType, when non-empty, overwrites account.account_type.
	AccountType string
	// OpenAIRT, when non-empty, overwrites account.openai_rt.
	OpenAIRT string
	// LockMailbox marks every sibling of this mother mailbox 邮箱锁定
	// (_mark_mailbox_email_locked, app.py:19834).
	LockMailbox bool
	LockDetail  string
}

// outcomeFor derives the outcome of a finished job, mirroring the events each
// Python entry point puts on success and on failure.
//
// cancelled is Python's stop_event: _run_account_thread returns without any
// status event when the user stopped the task, so a cancelled job must not
// stamp a failure over whatever the account already said.
func outcomeFor(kind JobKind, account models.MailAccount, result any, runErr error, cancelled bool) runOutcome {
	if cancelled {
		return runOutcome{}
	}
	if runErr != nil {
		return failureOutcome(kind, runErr)
	}

	payload := resultPayload(result)
	switch kind {
	case JobAuthOnly:
		// app.py:17752 — no result event at all, only the status.
		return runOutcome{Status: statusLoggedIn}

	case JobRegister:
		// app.py:17749-17750: result, then 状态=Session已获取 (which lands after the
		// result handler's 长链已提取 and therefore wins).
		return runOutcome{Status: statusSessionCollected, Payload: payload}

	case JobTeam:
		// app.py:17850-17856.
		rt := settings.PyStr(pyOrEmpty(payload["openai_rt"]))
		if rt == "" {
			return runOutcome{Status: models.ExceptionStatus(errTeamNoRT, "Team失败")}
		}
		return runOutcome{
			Status:      statusTeamRTCollected,
			Payload:     payload,
			AccountType: "team",
			OpenAIRT:    rt,
		}

	case JobRegisterAndRT:
		// app.py:17796-17813.
		rt := settings.PyStr(pyOrEmpty(payload["openai_rt"]))
		if rt == "" {
			return runOutcome{Status: models.ExceptionStatus(errDomainNoRT, "取RT失败")}
		}
		summary := openai.SummarizeChatGPTAccessToken(settings.PyStr(pyOrEmpty(payload["access_token"])))
		plan := openai.ClassifyChatGPTPlanText(openai.FirstNonEmpty(summary["plan_type"], account.AccountType))
		accountType := account.AccountType
		if planTypesAdopted[plan] {
			accountType = plan
		}
		status, ok := rtStatusByPlan[accountType]
		if !ok {
			status = statusRTCollected
		}
		return runOutcome{
			Status:      status,
			Payload:     payload,
			AccountType: accountType,
			OpenAIRT:    rt,
		}

	case JobRelink:
		// app.py:17912-17913.
		return runOutcome{Status: statusRelinkOK, Payload: payload}
	}
	return runOutcome{}
}

// The two RT checks Python performs in the GUI layer rather than the worker
// (app.py:17851 / 17797). They are raised as errors there, so they get the same
// exception_status treatment here.
var (
	errTeamNoRT   = errors.New("Team 注册成功但未获取到 refresh_token")
	errDomainNoRT = errors.New("域名邮箱注册成功但未获取到 refresh_token")
)

// failureOutcome maps a run error onto the status Python's except-blocks set.
//
// The 邮箱锁定 case is the one that also mutates: app.py:17696-17698 puts an
// ("email-locked", …) event, whose handler moves EVERY +alias of the same mother
// mailbox into the 邮箱锁定 group. Skipping it would let the next run spend money
// on a sibling of a mailbox already known to be locked.
func failureOutcome(kind JobKind, runErr error) runOutcome {
	var locked *mail.EmailSecurityInterruptError
	if errors.As(runErr, &locked) {
		return runOutcome{
			Status:      statusEmailLocked,
			LockMailbox: true,
			LockDetail:  locked.Error(),
		}
	}
	def := "失败"
	switch kind {
	case JobTeam:
		def = "Team失败" // app.py:17864
	case JobRegisterAndRT:
		def = "取RT失败" // app.py:17820
	case JobRelink:
		// app.py:17926/17957 is exception_status(exc, "失败") — plain 失败.
		// 提取长链失败 belongs to the OPLL/session link paths (app.py:23146, 23545,
		// 23647, 23713), which are a different screen; using it here would put a
		// status into linkFailureStatuses and start suppressing later failures.
		def = "失败"
	}
	return runOutcome{Status: models.ExceptionStatus(runErr, def)}
}

// applyOutcome writes one outcome into the snapshot and returns the session
// emails whose split file must be rewritten.
func applyOutcome(snapshot map[string]any, email string, out runOutcome) map[string]bool {
	dirty := map[string]bool{}

	if out.Payload != nil {
		// app.py:18634-18637 — results[email] is only touched when the payload
		// actually carries a url. A run that produced no link must not erase the
		// link an earlier run did produce.
		if url := strings.TrimSpace(settings.PyStr(pyOrEmpty(out.Payload["url"]))); url != "" {
			results := subMap(snapshot, "results")
			if results == nil {
				results = map[string]any{}
			}
			results[email] = url
			snapshot["results"] = results
		}
		sessions := subMap(snapshot, "session_results")
		if sessions == nil {
			sessions = map[string]any{}
		}
		old, _ := sessions[email].(map[string]any)
		sessions[email] = mergeSessionResult(old, out.Payload)
		snapshot["session_results"] = sessions
		// _mark_session_dirty (app.py:14308) keys off the stripped email exactly
		// as given, which is the same key session_results uses.
		dirty[email] = true
	}

	rows, _ := snapshot["accounts"].([]any)
	if out.LockMailbox {
		for _, e := range lockMailboxSiblings(snapshot, rows, email, out.LockDetail) {
			dirty[e] = true
		}
		return dirty
	}

	// _set_account_status / the direct account.* writes: first row whose email
	// matches case-insensitively (app.py:19814), by plain lower(), not the
	// alias-folded key.
	want := strings.ToLower(email)
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if strings.ToLower(settings.PyStr(pyOrEmpty(m["email"]))) != want {
			continue
		}
		if out.OpenAIRT != "" {
			m["openai_rt"] = out.OpenAIRT
		}
		if out.AccountType != "" {
			m["account_type"] = out.AccountType
		}
		if out.Status != "" && !suppressedStatus(snapshot, email, out.Status) {
			m["status"] = out.Status
		}
		break
	}
	return dirty
}

// suppressedStatus is app.py:18618-18623: once an account has a long link, a
// later failure status is logged and dropped rather than replacing 长链已提取.
func suppressedStatus(snapshot map[string]any, email, status string) bool {
	if !linkFailureStatuses[status] {
		return false
	}
	link, _ := resultsFromSnapshot(snapshot)[email].(string)
	return strings.TrimSpace(link) != ""
}

// lockMailboxSiblings is _mark_mailbox_email_locked (app.py:19834-19856).
func lockMailboxSiblings(snapshot map[string]any, rows []any, email, detail string) []string {
	mailboxKey := alias.AccountMailboxKey(email)
	if mailboxKey == "" {
		return nil
	}
	group := ensureAccountGroup(snapshot, alias.AccountEmailLockedGroup)
	sessions := subMap(snapshot, "session_results")
	if sessions == nil {
		sessions = map[string]any{}
	}
	// isoformat(timespec="seconds") on an aware UTC datetime, with +00:00 folded
	// to Z (app.py:19852).
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05") + "Z"

	var touched []string
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		addr := settings.PyStr(pyOrEmpty(m["email"]))
		if alias.AccountMailboxKey(addr) != mailboxKey {
			continue
		}
		m["status"] = statusEmailLocked
		m["group"] = group
		payload, ok := sessions[addr].(map[string]any)
		if !ok {
			payload = map[string]any{}
			sessions[addr] = payload
		}
		payload["email_locked"] = "true"
		payload["email_locked_detail"] = detail
		payload["email_locked_at"] = stamp
		touched = append(touched, addr)
	}
	snapshot["session_results"] = sessions
	return touched
}

// ensureAccountGroup is _ensure_account_group: append the group to
// settings.account_groups if missing, and return its name.
func ensureAccountGroup(snapshot map[string]any, group string) string {
	st := settings.FromSnapshot(snapshot)
	for _, existing := range st.AccountGroups {
		if existing == group {
			return group
		}
	}
	st.AccountGroups = append(st.AccountGroups, group)
	// ToSnapshot rewrites the settings object in place on the snapshot we are
	// about to persist, so the new group survives with everything else.
	next := settings.ToSnapshot(st, snapshot)
	for k, v := range next {
		snapshot[k] = v
	}
	return group
}

// ---------------------------------------------------------------------------
// The write
// ---------------------------------------------------------------------------

// persistRunOutcome is called once, from finishJob, after the job goroutine has
// released the proxy chain and any phone rental.
//
// flush=true rather than the 1.5 s debounce: this is the record of a run that
// already cost money, and a debounced write sits in Store.pending where the next
// flush=true save from anywhere else discards it.
func (a *App) persistRunOutcome(kind JobKind, account models.MailAccount, result any, runErr error, cancelled bool) error {
	out := outcomeFor(kind, account, result, runErr, cancelled)
	if out.Status == "" && out.Payload == nil && !out.LockMailbox {
		return nil
	}
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		dirty := applyOutcome(snapshot, account.Email, out)
		return snapshot, dirty, nil
	})
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// preflight is _run_account_thread's two refusals (app.py:16690-16696), checked
// BEFORE a job exists so the caller gets an error instead of a job that spends
// money and then fails.
//
// Both are money-safety checks, not validation niceties:
//
//   - a Cloud Mail account with no program token cannot read its OTP, so the run
//     is guaranteed to reach phone verification and rent a billable number for a
//     registration that then cannot complete;
//   - a mailbox already marked 邮箱锁定 stays locked, so every sibling +alias run
//     is money spent on a login that Microsoft will refuse.
//
// Python also writes the refusal into the account's 状态; that write is left to
// the caller, which reports the error to the user directly instead.
//
// Not here, deliberately: _start_worker's `if self.running` guard
// (app.py:16684). That is a BATCH-level lock — inside it Python immediately fans
// out across _auth_concurrency() threads (app.py:16696-16708), so it forbids a
// second 批量 run, not a second account, and this is the per-ACCOUNT check. Both
// that guard and the per-account collision Python cannot produce — the same
// address started twice — are refused in registerJob instead, which is the one
// place every path (standalone and batch child alike) goes through.
func preflight(snapshot map[string]any, account models.MailAccount) error {
	if strings.EqualFold(strings.TrimSpace(account.MailProvider), "cloudmail") &&
		strings.TrimSpace(account.CloudMailToken) == "" {
		return fmt.Errorf("%s: Cloud Mail 程序 Token 为空；请在导入页的 Cloud Mail API 设置中点击“生成Token”或填入已有 Token 后保存", statusCloudMailUnset)
	}
	if alias.IsAccountEmailLocked(accountsFromSnapshot(snapshot), account) {
		return fmt.Errorf("%s: 账号邮箱已标记锁定，跳过本次任务", statusEmailLocked)
	}
	return nil
}
