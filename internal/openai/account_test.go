package openai

import (
	"strings"
	"testing"
)

// Note: the `jwt(payload)` helper lives in auth_test.go (same package) — reused here.

func TestClassifyChatGPTPlanTextBranchOrder(t *testing.T) {
	cases := []struct{ in, want string }{
		// Branch 1 wins over every later branch.
		{"team", "team"},
		{"enterprise", "team"},
		{"Business Plan", "team"},
		{"school team", "team"}, // contains both "school" (k12) and "team" -> team first
		{"chatgpt_team_plan", "team"},
		{"free team", "team"}, // team beats free
		// Branch 2.
		{"k12", "k12"},
		{"K-12 Teacher", "k12"},
		{"school", "k12"},
		{"teacher plus", "k12"}, // k12 checked before plus
		// Branch 3 — "pro" deliberately folds into plus, and it is a SUBSTRING test.
		{"plus", "plus"},
		{"ChatGPT Pro", "plus"},
		{"chatgptplusplan", "plus"},
		{"Professional", "plus"},
		{"product", "plus"},  // substring trap, reproduced on purpose
		{"pro free", "plus"}, // plus checked before free
		// Branch 4.
		{"free", "free"},
		{"chatgpt_free_plan", "free"},
		{"no-plan", "free"},
		{"None", "free"},
		// No match.
		{"", ""},
		{"   ", ""},
		{"unknown", ""},
		{"gpt-4", ""},
	}
	for _, c := range cases {
		if got := ClassifyChatGPTPlanText(c.in); got != c.want {
			t.Errorf("ClassifyChatGPTPlanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSummarizeChatGPTAccessToken(t *testing.T) {
	token := jwt(map[string]any{
		"exp": 1700000000,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc_1234567890",
			"chatgpt_user_id":    "user_abcdefghij",
			"chatgpt_plan_type":  "plus",
		},
	})
	got := SummarizeChatGPTAccessToken(token)
	want := map[string]string{
		"plan_type":       "plus",
		"account_id":      "acc_1234567890",
		"account_id_tail": "34567890",
		"user_id":         "user_abcdefghij",
		"user_id_tail":    "cdefghij",
		"expires_at":      "2023-11-14T22:13:20.000Z",
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Errorf("summary[%q] = %v, want %q", key, got[key], wantValue)
		}
	}

	// No claims at all: plan_type falls back to "unknown", tails stay empty.
	empty := SummarizeChatGPTAccessToken("not-a-jwt")
	if empty["plan_type"] != "unknown" || empty["account_id"] != "" || empty["expires_at"] != "" {
		t.Fatalf("empty summary = %#v", empty)
	}

	// exp in milliseconds (>1e11) must be divided by 1000.
	ms := SummarizeChatGPTAccessToken(jwt(map[string]any{"exp": 1700000000000}))
	if ms["expires_at"] != "2023-11-14T22:13:20.000Z" {
		t.Fatalf("ms expires_at = %v", ms["expires_at"])
	}
	// exp <= 0 or unparsable -> "".
	for _, bad := range []any{0, -5, "abc", nil} {
		if got := accountTimestampFromUnixSeconds(bad); got != "" {
			t.Fatalf("accountTimestampFromUnixSeconds(%v) = %q, want empty", bad, got)
		}
	}
	// Python float() accepts numeric strings.
	if got := accountTimestampFromUnixSeconds("1700000000"); got != "2023-11-14T22:13:20.000Z" {
		t.Fatalf("string exp = %q", got)
	}
}

func TestAccountTailIsRuneBased(t *testing.T) {
	if got := accountTail("abc", 8); got != "abc" {
		t.Fatalf("short tail = %q, want abc (Python str[-8:] does not pad)", got)
	}
	if got := accountTail("邮箱账号标识ABCDEFGH", 8); got != "ABCDEFGH" {
		t.Fatalf("rune tail = %q", got)
	}
}

func TestApplyInferredPlanToSummary(t *testing.T) {
	// A paid plan overrides a cached "free" from the access token.
	summary := map[string]any{"plan_type": "free"}
	ApplyInferredPlanToSummary(summary, "plus", "endpoint -> x", "backend")
	if summary["plan_type"] != "plus" {
		t.Fatalf("plan_type = %v, want plus", summary["plan_type"])
	}
	if summary["backend_plan_type"] != "plus" || summary["backend_plan_detail"] != "endpoint -> x" {
		t.Fatalf("source keys = %#v", summary)
	}

	// A free plan must NOT demote an existing paid plan, but is still recorded.
	summary = map[string]any{"plan_type": "team"}
	ApplyInferredPlanToSummary(summary, "free", "d", "session")
	if summary["plan_type"] != "team" {
		t.Fatalf("plan_type = %v, want team (free must not demote)", summary["plan_type"])
	}
	if summary["session_plan_type"] != "free" {
		t.Fatalf("session_plan_type = %v", summary["session_plan_type"])
	}

	// "unknown" counts as no plan and is overridden even by free.
	summary = map[string]any{"plan_type": "unknown"}
	ApplyInferredPlanToSummary(summary, "  Free ", "d", "session")
	if summary["plan_type"] != "free" {
		t.Fatalf("plan_type = %v, want free (trim+lower of the raw plan)", summary["plan_type"])
	}

	// The plan is NOT re-classified: a manual "pro" stays "pro" (it is a paid type).
	summary = map[string]any{"plan_type": "free"}
	ApplyInferredPlanToSummary(summary, "pro", "用户手动指定", "manual")
	if summary["plan_type"] != "pro" || summary["manual_plan_type"] != "pro" {
		t.Fatalf("manual override = %#v", summary)
	}

	// Source key normalization + the "payload" default.
	summary = map[string]any{}
	ApplyInferredPlanToSummary(summary, "plus", "d", "Back-End 2")
	if _, ok := summary["backend2_plan_type"]; !ok {
		t.Fatalf("normalized source key missing: %#v", summary)
	}
	summary = map[string]any{}
	ApplyInferredPlanToSummary(summary, "plus", "d", "!!!")
	if _, ok := summary["payload_plan_type"]; !ok {
		t.Fatalf("default source key missing: %#v", summary)
	}

	// An empty plan is a no-op.
	summary = map[string]any{"plan_type": "free"}
	ApplyInferredPlanToSummary(summary, "   ", "d", "backend")
	if len(summary) != 1 || summary["plan_type"] != "free" {
		t.Fatalf("empty plan must not touch the summary: %#v", summary)
	}

	// A nil map is tolerated (Python would raise).
	if out := ApplyInferredPlanToSummary(nil, "plus", "d", "backend"); out["plan_type"] != "plus" {
		t.Fatalf("nil summary = %#v", out)
	}
}

func TestInferAccountTypeFromPayload(t *testing.T) {
	// Highest priority paid plan wins regardless of where it sits (team>k12>plus>free).
	plan, detail := InferAccountTypeFromPayload(map[string]any{
		"plan": "free",
		"tier": "ChatGPT Plus",
		"sku":  "enterprise",
	})
	if plan != "team" || detail != "payload.sku=enterprise" {
		t.Fatalf("priority = (%q, %q), want (team, payload.sku=enterprise)", plan, detail)
	}

	// Boolean paid flags collapse to "plus" only when no plan text was found.
	plan, detail = InferAccountTypeFromPayload(map[string]any{"is_paid_subscription_active": true})
	if plan != "plus" || detail != "payload.is_paid_subscription_active=true" {
		t.Fatalf("bool paid = (%q, %q)", plan, detail)
	}
	plan, detail = InferAccountTypeFromPayload(map[string]any{"has_active_subscription": false})
	if plan != "free" || detail != "payload.has_active_subscription=false" {
		t.Fatalf("bool free = (%q, %q)", plan, detail)
	}
	// Only real booleans count (Python uses `is True` / `is False`).
	if plan, _ := InferAccountTypeFromPayload(map[string]any{"is_plus": "true"}); plan != "" {
		t.Fatalf("string true must not count as a paid flag, got %q", plan)
	}

	// Bare "name" is ignored unless the path is plan/product/subscription/billing/sku/account.
	if plan, _ := InferAccountTypeFromPayload(map[string]any{
		"user": map[string]any{"name": "Team Alpha"},
	}); plan != "" {
		t.Fatalf("user.name must not be read as a plan, got %q", plan)
	}
	plan, detail = InferAccountTypeFromPayload(map[string]any{
		"subscription": map[string]any{"name": "Team Alpha"},
	})
	if plan != "team" || detail != "payload.subscription.name=Team Alpha" {
		t.Fatalf("subscription.name = (%q, %q)", plan, detail)
	}

	// Lists are walked with an [index] path segment.
	plan, detail = InferAccountTypeFromPayload(map[string]any{
		"accounts": []any{map[string]any{"plan": "chatgpt-k12-plan"}},
	})
	if plan != "k12" || detail != "payload.accounts[0].plan=chatgpt-k12-plan" {
		t.Fatalf("list walk = (%q, %q)", plan, detail)
	}

	// Nothing recognisable.
	plan, detail = InferAccountTypeFromPayload(map[string]any{"foo": "bar"})
	if plan != "" || detail != "未发现明确套餐字段" {
		t.Fatalf("empty payload = (%q, %q)", plan, detail)
	}
	if plan, _ := InferAccountTypeFromPayload(nil); plan != "" {
		t.Fatalf("nil payload must infer nothing")
	}
}

func teamBackendPayload(accountID, role string) map[string]any {
	return map[string]any{
		"accounts": map[string]any{
			accountID: map[string]any{
				"account": map[string]any{"account_id": accountID, "account_user_role": role},
				"plan":    "team",
			},
		},
	}
}

func TestMergeChatGPTBackendPlanSummaryFirstPaidWins(t *testing.T) {
	summary := map[string]any{"plan_type": "free"}
	results := []any{
		map[string]any{"endpoint": "/backend-api/me", "status": float64(200), "payload": map[string]any{"plan": "plus"}},
		map[string]any{"endpoint": "/backend-api/check", "status": float64(200), "payload": map[string]any{"plan": "team"}},
	}
	MergeChatGPTBackendPlanSummary(summary, results)
	if summary["plan_type"] != "plus" {
		t.Fatalf("plan_type = %v, want plus (the FIRST paid result returns immediately)", summary["plan_type"])
	}
	// JS numbers arrive as float64 in Go; the detail must still read "HTTP 200".
	detail, _ := summary["backend_plan_detail"].(string)
	if detail != "/backend-api/me HTTP 200 -> payload.plan=plus" {
		t.Fatalf("backend_plan_detail = %q", detail)
	}
}

func TestMergeChatGPTBackendPlanSummaryFreeFallback(t *testing.T) {
	// Free results are only a fallback, and only the first one is kept.
	summary := map[string]any{"plan_type": "unknown"}
	results := []any{
		map[string]any{"endpoint": "a", "status": 401, "payload": map[string]any{"plan": "free"}},
		map[string]any{"endpoint": "b", "status": 200, "payload": map[string]any{"plan": "chatgpt free plan"}},
	}
	MergeChatGPTBackendPlanSummary(summary, results)
	if summary["plan_type"] != "free" {
		t.Fatalf("plan_type = %v, want free", summary["plan_type"])
	}
	if got := summary["backend_plan_detail"]; got != "a HTTP 401 -> payload.plan=free" {
		t.Fatalf("backend_plan_detail = %v (first free wins)", got)
	}

	// A free backend result must not demote a paid summary.
	summary = map[string]any{"plan_type": "plus"}
	MergeChatGPTBackendPlanSummary(summary, results)
	if summary["plan_type"] != "plus" {
		t.Fatalf("plan_type = %v, want plus", summary["plan_type"])
	}

	// Non-list / non-dict / payload-less inputs are skipped, not fatal.
	summary = map[string]any{"plan_type": "unknown"}
	MergeChatGPTBackendPlanSummary(summary, nil)
	MergeChatGPTBackendPlanSummary(summary, []any{"junk", map[string]any{"endpoint": "x", "payload": "text"}})
	if summary["plan_type"] != "unknown" || len(summary) != 1 {
		t.Fatalf("junk input changed the summary: %#v", summary)
	}
}

func TestMergeChatGPTBackendPlanSummaryTeamWorkspace(t *testing.T) {
	summary := map[string]any{"plan_type": "unknown"}
	results := []map[string]any{{
		"endpoint": "/backend-api/accounts/check/v4-2023-04-27",
		"status":   float64(200),
		"payload":  teamBackendPayload("acc_team_1", "owner"),
	}}
	MergeChatGPTBackendPlanSummary(summary, results)
	if summary["plan_type"] != "team" {
		t.Fatalf("plan_type = %v, want team", summary["plan_type"])
	}
	if summary["backend_workspace_id"] != "acc_team_1" || summary["team_workspace_id"] != "acc_team_1" {
		t.Fatalf("workspace ids = %#v", summary)
	}
	if summary["backend_workspace_role"] != "owner" {
		t.Fatalf("workspace role = %v", summary["backend_workspace_role"])
	}
}

func TestTeamWorkspaceFromBackendPayload(t *testing.T) {
	// Owner (score 3) beats a plain member (score 1) even when listed later.
	payload := map[string]any{
		"accounts": map[string]any{
			"aaa_member": map[string]any{
				"account": map[string]any{"account_id": "aaa_member", "role": "standard-user"},
				"plan":    "team",
			},
			"zzz_owner": map[string]any{
				"account": map[string]any{"account_id": "zzz_owner", "role": "account-owner"},
				"plan":    "team",
			},
		},
	}
	got := accountTeamWorkspaceFromBackendPayload(payload)
	if got.AccountID != "zzz_owner" || got.Role != "account-owner" || got.PlanType != "team" {
		t.Fatalf("workspace = %#v", got)
	}

	// Non-team entries are skipped entirely.
	if got := accountTeamWorkspaceFromBackendPayload(map[string]any{
		"accounts": map[string]any{"a": map[string]any{"plan": "plus", "account": map[string]any{"account_id": "a"}}},
	}); got.AccountID != "" {
		t.Fatalf("plus workspace must be ignored: %#v", got)
	}

	// List form + account_id falling back to the entry key is only for the dict form.
	if got := accountTeamWorkspaceFromBackendPayload(map[string]any{
		"accounts": []any{map[string]any{"plan": "team", "id": "acc_list"}},
	}); got.AccountID != "acc_list" {
		t.Fatalf("list workspace = %#v", got)
	}
	if got := accountTeamWorkspaceFromBackendPayload("not a dict"); got.AccountID != "" {
		t.Fatalf("non-dict payload = %#v", got)
	}
}

// When two team workspaces share a role score the tie is broken by JSON document
// order, and the winner is the workspace a billable invite seat is created in — so
// an ordered decode is not cosmetic. Sorting the keys instead would answer
// "aaa_second" here; Python answers "zzz_first".
func TestTeamWorkspaceTieBreakFollowsDocumentOrder(t *testing.T) {
	const raw = `{"accounts":{
		"zzz_first":{"account":{"account_id":"zzz_first","role":"owner"},"plan":"team"},
		"aaa_second":{"account":{"account_id":"aaa_second","role":"owner"},"plan":"team"}}}`

	payload, err := DecodeOrderedJSON([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeOrderedJSON: %v", err)
	}
	if got := accountTeamWorkspaceFromBackendPayload(payload); got.AccountID != "zzz_first" {
		t.Fatalf("ordered payload picked %q, want the first entry zzz_first", got.AccountID)
	}

	// Reversing the document must reverse the answer — otherwise the test would
	// still pass against a sorted walk that happened to agree.
	const reversed = `{"accounts":{
		"aaa_second":{"account":{"account_id":"aaa_second","role":"owner"},"plan":"team"},
		"zzz_first":{"account":{"account_id":"zzz_first","role":"owner"},"plan":"team"}}}`
	payload, err = DecodeOrderedJSON([]byte(reversed))
	if err != nil {
		t.Fatalf("DecodeOrderedJSON: %v", err)
	}
	if got := accountTeamWorkspaceFromBackendPayload(payload); got.AccountID != "aaa_second" {
		t.Fatalf("reversed payload picked %q, want aaa_second", got.AccountID)
	}

	// A lower-scored entry earlier in the document still loses to a later owner.
	const mixed = `{"accounts":{
		"aaa_member":{"account":{"account_id":"aaa_member","role":"standard-user"},"plan":"team"},
		"zzz_owner":{"account":{"account_id":"zzz_owner","role":"account-owner"},"plan":"team"}}}`
	payload, err = DecodeOrderedJSON([]byte(mixed))
	if err != nil {
		t.Fatalf("DecodeOrderedJSON: %v", err)
	}
	if got := accountTeamWorkspaceFromBackendPayload(payload); got.AccountID != "zzz_owner" {
		t.Fatalf("role score must outrank document order, got %q", got.AccountID)
	}
}

// accountTimestampFromUnixSeconds must land on the microsecond grid the way
// datetime.fromtimestamp does before isoformat truncates to milliseconds.
func TestTimestampMicrosecondGrid(t *testing.T) {
	// Every expectation below was computed by running the real thing on this
	// machine's CPython 3.12:
	//   datetime.fromtimestamp(v, utc).isoformat(timespec="milliseconds")
	// The four marked cases are ones where rounding straight to nanoseconds and
	// truncating — what this function used to do — answers one millisecond low.
	for _, tc := range []struct{ in, want string }{
		{"1.0009995", "1970-01-01T00:00:01.001Z"},          // ns rounding: 000
		{"1.0029995", "1970-01-01T00:00:01.003Z"},          // ns rounding: 002
		{"1.9999996", "1970-01-01T00:00:02.000Z"},          // ns rounding: 001.999
		{"1700000000.0019995", "2023-11-14T22:13:20.002Z"}, // ns rounding: 001
		// Unaffected cases, pinned so the µs step cannot introduce a new drift.
		{"1.5", "1970-01-01T00:00:01.500Z"},
		{"1.9994", "1970-01-01T00:00:01.999Z"},    // truncated, not rounded up
		{"1.0019995", "1970-01-01T00:00:01.001Z"}, // the double lands below the .5 µs tie
	} {
		if got := accountTimestampFromUnixSeconds(tc.in); got != tc.want {
			t.Errorf("accountTimestampFromUnixSeconds(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestAccountIsOpenAIRefreshToken(t *testing.T) {
	for _, ok := range []string{"rt_abc", "rt.abc", "  rt_abc  "} {
		if !AccountIsOpenAIRefreshToken(ok) {
			t.Errorf("AccountIsOpenAIRefreshToken(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "   ", "sess_abc", "eyJhbGciOi", "RT_abc"} {
		if AccountIsOpenAIRefreshToken(bad) {
			t.Errorf("AccountIsOpenAIRefreshToken(%q) = true, want false", bad)
		}
	}
}

// The refresh grant must reject a non-RT locally, before any network call is made —
// this test therefore never touches a live endpoint.
func TestRefreshOpenAIAccessTokenRejectsNonRefreshToken(t *testing.T) {
	payload, err := RefreshOpenAIAccessToken("sess_not_an_rt", "")
	if err == nil {
		t.Fatalf("expected an error, got payload %#v", payload)
	}
	if !strings.Contains(err.Error(), "不是有效 OpenAI refresh_token") {
		t.Fatalf("error = %v", err)
	}
	if _, _, _, err := DetectOpenAIAccountType("", ""); err == nil {
		t.Fatalf("DetectOpenAIAccountType must fail fast on an empty RT")
	}
}

func TestAccountPyStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"", ""},
		{"200", "200"},
		{float64(200), "200"}, // JSON number from the browser bridge
		{float64(0), ""},      // Python: str(0 or "") == ""
		{200, "200"},
		{0, ""},
		{false, ""},
		{true, "True"},
	}
	for _, c := range cases {
		if got := accountPyStr(c.in); got != c.want {
			t.Errorf("accountPyStr(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAccountIsTransientHTTPError(t *testing.T) {
	transient := []string{
		"Connection reset by peer",
		"curl: (52) Empty reply from server",
		"context deadline exceeded (Client.Timeout exceeded)",
		"tls: handshake failure",
		"远程主机强迫关闭了一个现有的连接",
	}
	for _, text := range transient {
		if !accountIsTransientHTTPError(text) {
			t.Errorf("accountIsTransientHTTPError(%q) = false, want true", text)
		}
	}
	for _, text := range []string{"", "HTTP 401 unauthorized", "invalid_grant"} {
		if accountIsTransientHTTPError(text) {
			t.Errorf("accountIsTransientHTTPError(%q) = true, want false", text)
		}
	}
}

func TestAccountFormEncodeKeepsPythonOrder(t *testing.T) {
	got := accountFormEncode([][2]string{
		{"grant_type", "refresh_token"},
		{"client_id", DefaultClientID},
		{"refresh_token", "rt_a+b/c"},
	})
	want := "grant_type=refresh_token&client_id=" + DefaultClientID + "&refresh_token=rt_a%2Bb%2Fc"
	if got != want {
		t.Fatalf("accountFormEncode = %q, want %q", got, want)
	}
}

func TestAccountTruncateAndLastN(t *testing.T) {
	if got := accountTruncate("邮箱账号标识", 3); got != "邮箱账" {
		t.Fatalf("accountTruncate = %q (must slice runes, not bytes)", got)
	}
	if got := accountTruncate("ab", 5); got != "ab" {
		t.Fatalf("accountTruncate short = %q", got)
	}
	if got := strings.Join(accountLastN([]string{"a", "b", "c", "d"}, 3), "|"); got != "b|c|d" {
		t.Fatalf("accountLastN = %q", got)
	}
	if got := strings.Join(accountLastN([]string{"a"}, 3), "|"); got != "a" {
		t.Fatalf("accountLastN short = %q", got)
	}
}
