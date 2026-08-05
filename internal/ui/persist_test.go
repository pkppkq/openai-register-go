package ui

// MONEY SAFETY: nothing in this file may reach a worker. Every test drives the
// pure outcome/merge functions or App.persistRunOutcome directly, against a
// temp state.json — never startJob, never StartRegister, never GenerateLinks.

import (
	"errors"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// mergeSessionResult reproduces a 33-key dict literal built out of nested
// Python `or` chains (app.py:18640-18676). The expectations below are not
// reasoned out — each one was produced by exec()ing that literal verbatim under
// CPython 3.12. A scratch harness ran the same comparison over 6,008 randomised
// payload/old pairs; these are the cases that discriminate, kept because the
// harness is not.
//
// Two of them cost real money if they regress:
//
//   - the falsy-fallthrough group. Stringifying each `or` term before testing it
//     is the natural Go translation and it is wrong: str(None)/str(False)/str(0)
//     are all non-empty, so a JSON null in the payload would win the chain and
//     overwrite a good access_token with the literal text "None".
//   - the amount-key group, which is keyed on PRESENCE, not truthiness. A relink
//     that produced no amount check must clear the previous run's 金额一致 rather
//     than inherit it.
func TestMergeSessionResultPinnedAgainstCPython(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		old     map[string]any
		want    map[string]string
	}{{
		name:    "a JSON null in the payload falls through to the old value",
		payload: map[string]any{"access_token": nil},
		old:     map[string]any{"access_token": "OLD"},
		want:    map[string]string{"access_token": "OLD"},
	}, {
		name:    "so does false",
		payload: map[string]any{"access_token": false},
		old:     map[string]any{"access_token": "OLD"},
		want:    map[string]string{"access_token": "OLD"},
	}, {
		name:    "so does 0",
		payload: map[string]any{"access_token": 0},
		old:     map[string]any{"access_token": "OLD"},
		want:    map[string]string{"access_token": "OLD"},
	}, {
		name:    `with nothing behind it a null becomes empty, not "None"`,
		payload: map[string]any{"access_token": nil},
		old:     map[string]any{},
		want:    map[string]string{"access_token": ""},
	}, {
		name:    "a non-string that IS truthy is str()ed",
		payload: map[string]any{"access_token": 1.5},
		old:     map[string]any{},
		want:    map[string]string{"access_token": "1.5"},
	}, {
		name:    "followup falls back to the payload's generic link_proxy",
		payload: map[string]any{"link_proxy": "P"},
		old:     map[string]any{},
		want:    map[string]string{"link_proxy": "P", "link_followup_proxy": "P"},
	}, {
		// app.py's fourth `or` term repeats old["link_followup_proxy"] instead of
		// reading old["link_proxy"]. Reproduced deliberately; see mergeSessionResult.
		name:    "but NOT to the old entry's generic link_proxy",
		payload: map[string]any{},
		old:     map[string]any{"link_proxy": "P"},
		want:    map[string]string{"link_proxy": "P", "link_followup_proxy": ""},
	}, {
		name:    "the exit variant DOES read the old generic link_proxy_exit",
		payload: map[string]any{},
		old:     map[string]any{"link_proxy_exit": "JP"},
		want:    map[string]string{"link_proxy_exit": "JP", "link_followup_proxy_exit": "JP"},
	}, {
		name:    "an amount key present-but-empty overwrites the old value",
		payload: map[string]any{"amount_check": ""},
		old:     map[string]any{"amount_check": "OLD"},
		want:    map[string]string{"amount_check": ""},
	}, {
		name:    "an amount key absent keeps it",
		payload: map[string]any{},
		old:     map[string]any{"amount_check": "OLD"},
		want:    map[string]string{"amount_check": "OLD"},
	}, {
		name:    "an amount key present-and-null persists the literal None",
		payload: map[string]any{"amount_check": nil},
		old:     map[string]any{"amount_check": "OLD"},
		want:    map[string]string{"amount_check": "None"},
	}, {
		name:    "deactivation fields survive a run that cannot set them",
		payload: map[string]any{"access_token": "AT"},
		old: map[string]any{
			"openai_deactivation_found":  "true",
			"openai_deactivation_status": "已停用",
		},
		want: map[string]string{
			"access_token":               "AT",
			"openai_deactivation_found":  "true",
			"openai_deactivation_status": "已停用",
		},
	}, {
		name:    "Worker.Run's SessionInfo over a populated entry",
		payload: map[string]any{"url": "", "access_token": "AT", "session_json": "{}", "storage_state_json": "SS"},
		old: map[string]any{
			"access_token": "OLD", "openai_rt": "RT", "amount_check": "金额一致", "link_proxy": "P",
		},
		want: map[string]string{
			"access_token": "AT", "session_json": "{}", "storage_state_json": "SS",
			// Not in SessionInfo at all, so all three survive the run.
			"openai_rt": "RT", "amount_check": "金额一致", "link_proxy": "P",
		},
	}, {
		name:    "RunTeam's AuthResult",
		payload: map[string]any{"url": "", "access_token": "AT", "session_json": "{}", "storage_state_json": "SS", "openai_rt": "NEW"},
		old:     map[string]any{"openai_rt": "OLD"},
		want:    map[string]string{"openai_rt": "NEW"},
	}} {
		got := mergeSessionResult(tc.old, tc.payload)
		// Python rebuilds the entry from exactly these keys; anything else on the
		// old entry is dropped, and a missing key here would silently read as "".
		// 33, and "url" is deliberately not among them: the link lives in the
		// separate top-level `results` table, not in the session entry.
		if len(got) != 33 {
			t.Errorf("%s: %d keys, want 33", tc.name, len(got))
		}
		for k, want := range tc.want {
			if g, _ := got[k].(string); g != want {
				t.Errorf("%s: [%s] = %q, want %q", tc.name, k, g, want)
			}
		}
	}

	// The unknown-key case, checked separately because it is about absence.
	if got := mergeSessionResult(map[string]any{"totally_unknown": "gone"}, map[string]any{}); got["totally_unknown"] != nil {
		t.Error("an unmodelled key survived the rebuild; Python drops it")
	}
}

// The status each entry point ends on. Getting these wrong is not cosmetic: the
// account table's filters and every batch/export screen select on 状态, so a run
// that finished is invisible to the rest of the app until this is right.
func TestOutcomeStatusPerEntryPoint(t *testing.T) {
	free := models.MailAccount{Email: "a@x.com", AccountType: "free"}

	for _, tc := range []struct {
		name    string
		kind    JobKind
		account models.MailAccount
		result  any
		err     error
		cancel  bool

		wantStatus  string
		wantType    string
		wantRT      string
		wantPayload bool
	}{{
		name: "注册取 Session", kind: JobRegister, account: free,
		result:      &worker.SessionInfo{AccessToken: "AT"},
		wantStatus:  statusSessionCollected,
		wantPayload: true,
	}, {
		// app.py:17752 puts no ("result", …) event at all on this path.
		name: "注册或登录 persists a status and nothing else", kind: JobAuthOnly, account: free,
		wantStatus: statusLoggedIn,
	}, {
		name: "Team", kind: JobTeam, account: free,
		result:      &worker.AuthResult{SessionInfo: worker.SessionInfo{AccessToken: "AT"}, OpenAIRT: "RT"},
		wantStatus:  statusTeamRTCollected,
		wantType:    "team",
		wantRT:      "RT",
		wantPayload: true,
	}, {
		// app.py:17851 raises when run_team() came back without one, so the seat is
		// recorded as a failure rather than as a Team account with no token.
		name: "Team without an RT is a failure", kind: JobTeam, account: free,
		result:     &worker.AuthResult{SessionInfo: worker.SessionInfo{AccessToken: "AT"}},
		wantStatus: "Team失败",
	}, {
		name: "域名邮箱取RT adopts the plan from the token", kind: JobRegisterAndRT, account: free,
		result:      &worker.AuthResult{OpenAIRT: "RT"},
		wantStatus:  "Free RT已获取", // account_type stays free, no token to reclassify
		wantType:    "free",
		wantRT:      "RT",
		wantPayload: true,
	}, {
		name: "重新获取长链", kind: JobRelink, account: free,
		result:      &worker.PayLinkResult{URL: "https://pay/x"},
		wantStatus:  statusRelinkOK,
		wantPayload: true,
	}, {
		// _run_account_thread returns without any status event when stop_event is
		// set (app.py:16699), so a stopped run must not stamp 失败 over whatever the
		// account already said.
		name: "a cancelled run writes nothing", kind: JobRegister, account: free,
		result: &worker.SessionInfo{AccessToken: "AT"}, cancel: true,
	}, {
		name: "a typed error keeps its own status", kind: JobRegister, account: free,
		err:        &models.PhoneRequiredError{Msg: "需要验证", Status: "需要手机号"},
		wantStatus: "需要手机号",
	}, {
		// app.py:17926/17957 — plain 失败, NOT 提取长链失败. That one belongs to the
		// OPLL/session link screens and, being in linkFailureStatuses, would start
		// suppressing later failures on this account.
		name: "an untyped error takes the per-entry-point default", kind: JobRelink, account: free,
		err:        errors.New("boom"),
		wantStatus: "失败",
	}} {
		got := outcomeFor(tc.kind, tc.account, tc.result, tc.err, tc.cancel)
		if got.Status != tc.wantStatus {
			t.Errorf("%s: status = %q, want %q", tc.name, got.Status, tc.wantStatus)
		}
		if got.AccountType != tc.wantType {
			t.Errorf("%s: account_type = %q, want %q", tc.name, got.AccountType, tc.wantType)
		}
		if got.OpenAIRT != tc.wantRT {
			t.Errorf("%s: openai_rt = %q, want %q", tc.name, got.OpenAIRT, tc.wantRT)
		}
		if (got.Payload != nil) != tc.wantPayload {
			t.Errorf("%s: payload present = %v, want %v", tc.name, got.Payload != nil, tc.wantPayload)
		}
	}
}

// The end-to-end write. Before this existed, a finished run persisted nothing at
// all: the money was spent and the result was discarded when the process exited.
func TestPersistRunOutcomeWritesTheRunResult(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "注册中", "未分组")},
		"results":        map[string]any{},
	})
	// Seeded through the store rather than inline in the fixture: at
	// schema_version 2 the "session_results" object in state.json is an INDEX, and
	// the payload lives in its own file under state_data/sessions/. A fixture that
	// writes the payload inline loads back as an empty entry.
	seedSession(t, app, "a@x.com", map[string]any{
		// A prior run's deactivation scan. A new run cannot set these and must not
		// erase them.
		"openai_deactivation_found": "true",
		"amount_check":              "金额一致",
	})
	account, err := app.accountByEmail("a@x.com")
	if err != nil {
		t.Fatalf("accountByEmail: %v", err)
	}

	link := &worker.PayLinkResult{URL: "  https://pay.example/abc  ", AccessToken: "AT"}
	if err := app.persistRunOutcome(JobRelink, account, link, nil, false); err != nil {
		t.Fatalf("persistRunOutcome: %v", err)
	}

	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, _ := resultsFromSnapshot(snapshot)["a@x.com"].(string); got != "https://pay.example/abc" {
		t.Errorf("results = %q, want the stripped url", got)
	}
	session, _ := sessionResultsFromSnapshot(snapshot)["a@x.com"].(map[string]any)
	if session == nil {
		t.Fatal("no session_results entry was written")
	}
	if got, _ := session["access_token"].(string); got != "AT" {
		t.Errorf("access_token = %q, want AT", got)
	}
	if got, _ := session["openai_deactivation_found"].(string); got != "true" {
		t.Errorf("openai_deactivation_found = %q — the prior scan was erased", got)
	}
	// PayLinkResult carries amount_check, so the empty one overwrites (presence,
	// not truthiness — see mergeSessionResult).
	if got, _ := session["amount_check"].(string); got != "" {
		t.Errorf("amount_check = %q, want it cleared by the new run", got)
	}
	if got := accountStatus(t, snapshot, "a@x.com"); got != statusRelinkOK {
		t.Errorf("status = %q, want %q", got, statusRelinkOK)
	}

	// A later failure whose status is in app.py:18619's set must NOT downgrade an
	// account that already has a link.
	proxyFailure := &models.ProxyExitCheckError{Msg: "not JP", Status: "代理非日本"}
	if err := app.persistRunOutcome(JobRelink, account, nil, proxyFailure, false); err != nil {
		t.Fatalf("persistRunOutcome(suppressed failure): %v", err)
	}
	snapshot, _ = app.snapshot()
	if got := accountStatus(t, snapshot, "a@x.com"); got != statusRelinkOK {
		t.Errorf("status = %q after a 代理非日本 retry; 已有长链 must suppress it", got)
	}

	// A failure OUTSIDE that set still lands, even with a link on file — the guard
	// is a five-item list, not "never overwrite a success".
	if err := app.persistRunOutcome(JobRelink, account, nil, errors.New("boom"), false); err != nil {
		t.Fatalf("persistRunOutcome(plain failure): %v", err)
	}
	snapshot, _ = app.snapshot()
	if got := accountStatus(t, snapshot, "a@x.com"); got != "失败" {
		t.Errorf("status = %q, want 失败 — 失败 is not in app.py:18619's suppression set", got)
	}
}

// A run that produced no link must not erase the link a previous run did
// produce — app.py:18634 only assigns results[email] when the payload has a url.
func TestPersistRunOutcomeKeepsAnEarlierLink(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
		"results":        map[string]any{"a@x.com": "https://pay.example/old"},
	})
	account, _ := app.accountByEmail("a@x.com")
	if err := app.persistRunOutcome(JobRegister, account, &worker.SessionInfo{AccessToken: "AT"}, nil, false); err != nil {
		t.Fatalf("persistRunOutcome: %v", err)
	}
	snapshot, _ := app.snapshot()
	if got, _ := resultsFromSnapshot(snapshot)["a@x.com"].(string); got != "https://pay.example/old" {
		t.Errorf("results = %q, want the earlier link untouched", got)
	}
}

// 邮箱锁定 locks the MAILBOX, not the address: every +alias of the same mother
// mailbox is moved and marked (app.py:19834-19856). Skipping the siblings would
// let the next run spend money on an address already known to be refused.
func TestPersistRunOutcomeLocksEveryAlias(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts": []any{
			accountMap("box@example.com", "free", "", "未分组"),
			accountMap("box+one@example.com", "free", "", "未分组"),
			accountMap("other@example.com", "free", "", "未分组"),
		},
	})
	account, _ := app.accountByEmail("box+one@example.com")
	locked := &mail.EmailSecurityInterruptError{Message: "被微软锁定", Status: alias.AccountEmailLockedStatus}
	if err := app.persistRunOutcome(JobRegister, account, nil, locked, false); err != nil {
		t.Fatalf("persistRunOutcome: %v", err)
	}

	snapshot, _ := app.snapshot()
	for _, email := range []string{"box@example.com", "box+one@example.com"} {
		if got := accountStatus(t, snapshot, email); got != statusEmailLocked {
			t.Errorf("%s: status = %q, want %q", email, got, statusEmailLocked)
		}
		session, _ := sessionResultsFromSnapshot(snapshot)[email].(map[string]any)
		if session == nil || session["email_locked"] != "true" {
			t.Errorf("%s: session entry does not record the lock: %v", email, session)
		}
		if detail, _ := session["email_locked_detail"].(string); detail != "被微软锁定" {
			t.Errorf("%s: email_locked_detail = %q", email, detail)
		}
	}
	if got := accountStatus(t, snapshot, "other@example.com"); got != "" {
		t.Errorf("an unrelated mailbox was locked too: %q", got)
	}
	// The 邮箱锁定 group has to exist for the moved rows to be reachable in the
	// 分组 filter.
	groups := settingsGroups(t, snapshot)
	if !groups[alias.AccountEmailLockedGroup] {
		t.Errorf("account_groups = %v, missing %q", groups, alias.AccountEmailLockedGroup)
	}
}

// seedSession writes one account's session payload the way the app itself does,
// so it comes back through Store.Load's split-file path.
func seedSession(t *testing.T, app *App, email string, payload map[string]any) {
	t.Helper()
	err := app.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		sessions := subMap(snapshot, "session_results")
		if sessions == nil {
			sessions = map[string]any{}
		}
		sessions[email] = payload
		snapshot["session_results"] = sessions
		return snapshot, map[string]bool{email: true}, nil
	})
	if err != nil {
		t.Fatalf("seedSession(%s): %v", email, err)
	}
}

func accountStatus(t *testing.T, snapshot map[string]any, email string) string {
	t.Helper()
	for _, acc := range accountsFromSnapshot(snapshot) {
		if acc.Email == email {
			return acc.Status
		}
	}
	t.Fatalf("account %s vanished from the snapshot", email)
	return ""
}

func settingsGroups(t *testing.T, snapshot map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, g := range settings.FromSnapshot(snapshot).AccountGroups {
		out[g] = true
	}
	return out
}
