package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// MONEY SAFETY. Nothing in this file may reach a paid API — not smsbower
// (GetNumber rents a real billable number), not the OpenAI endpoints, not the
// payment flow, not a Team invite.
//
// workerConfig used to refuse a multi-hop proxy chain, and several tests leaned on
// that refusal to keep worker.New from ever being constructed. It no longer
// refuses — the chain is wired now — so that guard is GONE. The rule is therefore
// absolute: a test may call StartRegister / GenerateLinks only with an account the
// state file does not contain (which fails synchronously in startJob). Anything
// that needs a running job uses startStubJob from jobs_test.go, and anything that
// needs the real config calls App.workerConfig directly.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func realStatePaths(t *testing.T) (string, string) {
	t.Helper()
	stateFile := strings.TrimSpace(os.Getenv("OPENAI_REGISTER_REAL_STATE_FILE"))
	dataDir := strings.TrimSpace(os.Getenv("OPENAI_REGISTER_REAL_STATE_DATA_DIR"))
	if stateFile == "" || dataDir == "" {
		t.Skip("未同时设置 OPENAI_REGISTER_REAL_STATE_FILE 和 OPENAI_REGISTER_REAL_STATE_DATA_DIR，跳过外部状态对照测试")
	}
	return filepath.Clean(stateFile), filepath.Clean(dataDir)
}

// newTempApp builds an App over a throwaway state.json. Every test that WRITES
// uses this: the real state.json is shared with the still-running Python app
// and a test must never touch it.
func newTempApp(t *testing.T, snapshot map[string]any) *App {
	t.Helper()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	writeJSONFile(t, stateFile, snapshot)
	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", filepath.Join(dir, "state_data"))
	return New()
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return out
}

// sessionRelPath mirrors state.sessionRelPath (state.go:343), which is
// unexported. Duplicated rather than promoted: a test that computes the path
// independently also asserts the layout has not drifted.
func sessionRelPath(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "sessions/" + hex.EncodeToString(sum[:])[:24] + ".json"
}

func writeSessionFile(t *testing.T, dataDir, email string, payload map[string]any) {
	t.Helper()
	writeJSONFile(t, filepath.Join(dataDir, filepath.FromSlash(sessionRelPath(email))),
		map[string]any{"email": email, "updated_at": "", "payload": payload})
}

func sessionIndexEntry(email string) map[string]any {
	return map[string]any{"session_file": sessionRelPath(email)}
}

func accountMap(email, accountType, status, group string) map[string]any {
	return map[string]any{
		"email":         email,
		"password":      "pw",
		"client_id":     "cid",
		"refresh_token": "rt",
		"account_type":  accountType,
		"status":        status,
		"group":         group,
	}
}

func rowByEmail(t *testing.T, page AccountPage, email string) AccountRow {
	t.Helper()
	for _, row := range page.Rows {
		if row.Email == email {
			return row
		}
	}
	t.Fatalf("no row for %s in %v", email, emailsOf(page))
	return AccountRow{}
}

func emailsOf(page AccountPage) []string {
	out := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		out = append(out, row.Email)
	}
	return out
}

func assertEmails(t *testing.T, page AccountPage, want ...string) {
	t.Helper()
	got := emailsOf(page)
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// ListAccounts
// ---------------------------------------------------------------------------

// TestListAccountsFilterSortAndPage exercises the whole derived-table path
// against a real on-disk state dir, including the split session file — a
// session that lives in state_data/sessions/ rather than inline is exactly the
// case a naive loader reads as "no session".
func TestListAccountsFilterSortAndPage(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "state_data")
	writeSessionFile(t, dataDir, "a@x.com", map[string]any{"access_token": "tok-a"})

	snapshot := map[string]any{
		"schema_version": 2,
		"accounts": []any{
			accountMap("b@x.com", "free", "", "组A"),
			accountMap("a@x.com", "plus", "", "组B"),
			accountMap("c@x.com", "team", "登录失败", "组A"),
		},
		"results":             map[string]any{"a@x.com": "https://link/a"},
		"link_attempt_counts": map[string]any{"a@x.com": 3},
		"session_results":     map[string]any{"a@x.com": sessionIndexEntry("a@x.com")},
		"settings":            map[string]any{"account_groups": []any{"组A", "组B"}},
	}
	stateFile := filepath.Join(dir, "state.json")
	writeJSONFile(t, stateFile, snapshot)
	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", dataDir)
	app := New()

	// Unfiltered: list order, because the default direction is SortCustom.
	page, err := app.ListAccounts(AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	assertEmails(t, page, "b@x.com", "a@x.com", "c@x.com")
	if page.Total != 3 || page.Matched != 3 {
		t.Errorf("total=%d matched=%d, want 3/3", page.Total, page.Matched)
	}
	// 分组 combo: 未分组 is always first, then the saved groups.
	if len(page.Groups) != 3 || page.Groups[0] != "未分组" {
		t.Errorf("groups = %v, want [未分组 组A 组B]", page.Groups)
	}

	// The derived 状态 column, UI_SPEC §1.6.
	rowA := rowByEmail(t, page, "a@x.com")
	if rowA.StatusText != "长链已提取" {
		t.Errorf("a statusText = %q, want 长链已提取 (a non-blank results entry outranks everything)", rowA.StatusText)
	}
	if !rowA.HasSession {
		t.Error("a hasSession = false — the split session file under state_data/sessions/ was not read")
	}
	if rowA.Attempts != 3 || rowA.Link != "https://link/a" {
		t.Errorf("a attempts=%d link=%q, want 3 / https://link/a", rowA.Attempts, rowA.Link)
	}
	if rowA.AccountType != "plus" || rowA.Status != "" {
		t.Errorf("a account_type=%q status=%q — the persisted fields must pass through unchanged",
			rowA.AccountType, rowA.Status)
	}
	if rowA.Key != "a@x.com" {
		t.Errorf("a key = %q, want the lowercased email", rowA.Key)
	}
	if got := rowByEmail(t, page, "b@x.com").StatusText; got != "待处理" {
		t.Errorf("b statusText = %q, want 待处理", got)
	}
	if got := rowByEmail(t, page, "c@x.com").StatusText; got != "登录失败" {
		t.Errorf("c statusText = %q, want the stored worker status", got)
	}

	// Sorting.
	page, _ = app.ListAccounts(AccountFilter{SortColumn: "email", SortDirection: "asc"})
	assertEmails(t, page, "a@x.com", "b@x.com", "c@x.com")
	page, _ = app.ListAccounts(AccountFilter{SortColumn: "email", SortDirection: "desc"})
	assertEmails(t, page, "c@x.com", "b@x.com", "a@x.com")
	page, _ = app.ListAccounts(AccountFilter{SortColumn: "attempts", SortDirection: "desc"})
	if page.Rows[0].Email != "a@x.com" {
		t.Errorf("attempts desc put %q first, want a@x.com", page.Rows[0].Email)
	}

	// Filters.
	for _, tc := range []struct {
		name   string
		filter AccountFilter
		want   []string
	}{
		{"group", AccountFilter{Group: "组A", SortColumn: "email", SortDirection: "asc"}, []string{"b@x.com", "c@x.com"}},
		{"group-all", AccountFilter{Group: "全部"}, []string{"b@x.com", "a@x.com", "c@x.com"}},
		{"status-session", AccountFilter{Status: "有 Session"}, []string{"a@x.com"}},
		{"status-linked", AccountFilter{Status: "提链成功"}, []string{"a@x.com"}},
		{"status-failed", AccountFilter{Status: "失败"}, []string{"c@x.com"}},
		{"status-pending", AccountFilter{Status: "待处理"}, []string{"b@x.com"}},
		{"status-plus", AccountFilter{Status: "Plus"}, []string{"a@x.com"}},
		{"search-type", AccountFilter{Search: "team"}, []string{"c@x.com"}},
		// Multi-term search is AND-ed over email+type+status+group.
		{"search-and", AccountFilter{Search: "组A free"}, []string{"b@x.com"}},
		{"search-none", AccountFilter{Search: "组A 组B"}, nil},
	} {
		page, err := app.ListAccounts(tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		assertEmails(t, page, tc.want...)
		if page.Total != 3 {
			t.Errorf("%s: total = %d, want 3 (total is the account count, not the match count)", tc.name, page.Total)
		}
	}

	// Paging.
	page, err = app.ListAccounts(AccountFilter{SortColumn: "email", SortDirection: "asc", Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListAccounts paged: %v", err)
	}
	assertEmails(t, page, "b@x.com", "c@x.com")
	if page.Matched != 3 || page.Offset != 1 {
		t.Errorf("matched=%d offset=%d, want 3/1", page.Matched, page.Offset)
	}
	// An offset past the end is empty, not an error and not a panic.
	page, err = app.ListAccounts(AccountFilter{Offset: 99, Limit: 5})
	if err != nil || len(page.Rows) != 0 {
		t.Errorf("offset past end: rows=%d err=%v, want 0/nil", len(page.Rows), err)
	}
}

// TestListAccountsAgainstRealState is the same contract as
// TestLoadSummaryAgainstRealState: a binding that silently returns a zero value
// looks identical to a working one in a screenshot. Read-only — ListAccounts
// never writes, so this may point at the user's actual file.
func TestListAccountsAgainstRealState(t *testing.T) {
	requireRealStateTests(t)
	realStateFile, realDataDir := realStatePaths(t)
	if _, err := os.Stat(realStateFile); err != nil {
		t.Skipf("configured state file is unavailable (%s); skipping", realStateFile)
	}
	t.Setenv("STATE_FILE", realStateFile)
	t.Setenv("STATE_DATA_DIR", realDataDir)
	app := New()
	page, err := app.ListAccounts(AccountFilter{SortColumn: "email", SortDirection: "asc"})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if page.Total == 0 {
		t.Fatalf("read 0 accounts from %s — the loader is not seeing real data", realStateFile)
	}
	if page.Matched != page.Total {
		t.Errorf("unfiltered matched=%d total=%d — an empty AccountFilter must filter nothing",
			page.Matched, page.Total)
	}
	sessions := 0
	for _, row := range page.Rows {
		if row.Key == "" || row.Key != strings.ToLower(strings.TrimSpace(row.Email)) {
			t.Fatalf("row %q has key %q — row identity must be the lowercased email", row.Email, row.Key)
		}
		if row.StatusText == "" {
			t.Errorf("row %q has an empty derived 状态", row.Email)
		}
		if row.HasSession {
			sessions++
		}
	}
	// Sessions live in the split files under state_data/sessions/; if none load,
	// the whole derived-status layer is reading the wrong thing.
	if sessions == 0 {
		t.Error("no account reports a Session — expected the split session files to be read")
	}
	if len(page.Groups) == 0 {
		t.Error("no groups — 未分组 is always present")
	}
	t.Logf("accounts=%d withSession=%d groups=%d", page.Total, sessions, len(page.Groups))
}

// ---------------------------------------------------------------------------
// ImportAccounts
// ---------------------------------------------------------------------------

// TestImportAccountsPreservesWorkerFields is the merge rule at
// app.py:14701-14717: re-pasting an export must refresh credentials without
// resetting a registration that already happened.
func TestImportAccountsPreservesWorkerFields(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "state_data")
	// new@x.com is in the session index but NOT in accounts, so Store.Load
	// defers it — importing the mailbox back has to pull the payload in again.
	writeSessionFile(t, dataDir, "new@x.com", map[string]any{"access_token": "tok-new"})

	keep := accountMap("keep@x.com", "plus", "Session已获取", "组A")
	keep["openai_rt"] = "RT-OLD"
	snapshot := map[string]any{
		"schema_version":  2,
		"accounts":        []any{keep},
		"session_results": map[string]any{"new@x.com": sessionIndexEntry("new@x.com")},
		"settings": map[string]any{
			"account_groups":       []any{"组A"},
			"account_group_filter": "组A",
		},
	}
	stateFile := filepath.Join(dir, "state.json")
	writeJSONFile(t, stateFile, snapshot)
	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", dataDir)
	app := New()

	result, err := app.ImportAccounts(
		"keep@x.com----newpw----newcid----newrt\n" +
			"new@x.com----np----nc----nr\n" +
			"   \n" +
			"broken-line\n")
	if err != nil {
		t.Fatalf("ImportAccounts: %v", err)
	}
	if result.Imported != 2 || result.Added != 1 || result.Updated != 1 || result.Total != 2 {
		t.Errorf("imported=%d added=%d updated=%d total=%d, want 2/1/1/2",
			result.Imported, result.Added, result.Updated, result.Total)
	}
	if result.Group != "组A" {
		t.Errorf("import group = %q, want 组A (the active 分组 filter)", result.Group)
	}
	// Blank lines are skipped but still counted out of the line numbering, so
	// the malformed 4th line reports as 第 3 行.
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "第 3 行") {
		t.Errorf("errors = %v, want one 第 3 行 entry", result.Errors)
	}
	if !strings.HasPrefix(result.Message, "已导入 2 个邮箱；失败: ") {
		t.Errorf("message = %q", result.Message)
	}

	page, err := app.ListAccounts(AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	got := rowByEmail(t, page, "keep@x.com")
	if got.AccountType != "plus" || got.Status != "Session已获取" || got.OpenaiRT != "RT-OLD" || got.Group != "组A" {
		t.Errorf("import clobbered worker-owned fields: type=%q status=%q rt=%q group=%q",
			got.AccountType, got.Status, got.OpenaiRT, got.Group)
	}
	if got.Password != "newpw" || got.ClientID != "newcid" || got.RefreshToken != "newrt" {
		t.Errorf("import did not refresh credentials: pw=%q cid=%q rt=%q",
			got.Password, got.ClientID, got.RefreshToken)
	}
	added := rowByEmail(t, page, "new@x.com")
	if added.Group != "组A" {
		t.Errorf("new account group = %q, want 组A", added.Group)
	}
	if !added.HasSession {
		t.Error("the deferred split session was not pulled back in (app.py:14715-14717)")
	}

	// And it is on disk, not just in memory.
	persisted := readJSONFile(t, stateFile)
	if rows, _ := persisted["accounts"].([]any); len(rows) != 2 {
		t.Fatalf("persisted accounts = %d, want 2", len(rows))
	}
	index, _ := persisted["session_results"].(map[string]any)
	if _, ok := index["new@x.com"]; !ok {
		t.Error("new@x.com missing from the persisted session index")
	}
	if persisted["updated_at"] == "" || persisted["updated_at"] == nil {
		t.Error("updated_at was not stamped")
	}
}

func TestImportAccountsRejectsEmptyText(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	if _, err := app.ImportAccounts("   \n\n"); err == nil {
		t.Fatal("expected 请先粘贴邮箱账户")
	} else if !strings.Contains(err.Error(), "请先粘贴邮箱账户") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// TestSaveSettingsPreservesUnmodelledKeys: losing a key out of the user's
// state.json is a real regression — the Python app still reads this file.
func TestSaveSettingsPreservesUnmodelledKeys(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	writeJSONFile(t, stateFile, map[string]any{
		"schema_version":       2,
		"updated_at":           "2020-01-01T00:00:00",
		"accounts":             []any{},
		"phones":               []any{map[string]any{"number": "+15550000"}},
		"payment_cards":        []any{},
		"results":              map[string]any{"x@x.com": "http://link"},
		"link_attempt_counts":  map[string]any{"x@x.com": 2},
		"session_results":      map[string]any{},
		"future_top_level_key": "keep-me",
		"settings": map[string]any{
			"target_amount":       "1.00",
			"smsbower_api_key":    "SECRET",
			"future_settings_key": "keep-me-too",
			"provider_proxy_configs": map[string]any{
				"create":       map[string]any{"enabled": true, "future_role_field": "keep-me-three"},
				"future_role":  map[string]any{"enabled": false},
				"__unmodelled": "keep-me-four",
			},
		},
	})
	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", filepath.Join(filepath.Dir(stateFile), "state_data"))
	app := New()

	loaded, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if loaded.TargetAmount != "1.00" || loaded.SMSBowerAPIKey != "SECRET" {
		t.Fatalf("LoadSettings did not decode: %+v", loaded)
	}
	loaded.TargetAmount = "9.99"
	if err := app.SaveSettings(loaded); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	persisted := readJSONFile(t, stateFile)
	if persisted["future_top_level_key"] != "keep-me" {
		t.Error("an unmodelled TOP-LEVEL key was dropped")
	}
	if phones, _ := persisted["phones"].([]any); len(phones) != 1 {
		t.Error("phones was dropped — settings.ToSnapshot must copy the whole prior snapshot")
	}
	if results, _ := persisted["results"].(map[string]any); len(results) != 1 {
		t.Error("results was dropped")
	}
	ps, _ := persisted["settings"].(map[string]any)
	if ps["future_settings_key"] != "keep-me-too" {
		t.Error("an unmodelled SETTINGS key was dropped")
	}
	if ps["smsbower_api_key"] != "SECRET" {
		t.Errorf("smsbower_api_key = %v, want SECRET", ps["smsbower_api_key"])
	}
	if ps["target_amount"] != "9.99" {
		t.Errorf("target_amount = %v, want the edited 9.99", ps["target_amount"])
	}
	providers, _ := ps["provider_proxy_configs"].(map[string]any)
	create, _ := providers["create"].(map[string]any)
	if create["future_role_field"] != "keep-me-three" {
		t.Error("an unmodelled field inside a known provider role was dropped")
	}
	if _, ok := providers["future_role"]; !ok {
		t.Error("an unknown provider role was dropped")
	}
	if providers["__unmodelled"] != "keep-me-four" {
		t.Error("a non-object entry in provider_proxy_configs was dropped")
	}
	if persisted["updated_at"] == "2020-01-01T00:00:00" {
		t.Error("updated_at was not re-stamped")
	}
}

// TestSaveSettingsRoundTripsRealState is the regression the synthetic fixture
// cannot be: the user's actual 60-key settings object, 21 accounts and 153-entry
// session index, through a full load/save with no edit. The file is COPIED to a
// temp dir first — the real one is never written.
func TestSaveSettingsRoundTripsRealState(t *testing.T) {
	requireRealStateTests(t)
	stateFile, before := copyRealState(t)

	app := New()
	loaded, err := app.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if err := app.SaveSettings(loaded); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	after := readJSONFile(t, stateFile)

	for key := range before {
		if _, ok := after[key]; !ok {
			t.Errorf("top-level key %q was lost", key)
		}
	}
	beforeSettings, _ := before["settings"].(map[string]any)
	afterSettings, _ := after["settings"].(map[string]any)
	for key := range beforeSettings {
		if _, ok := afterSettings[key]; !ok {
			t.Errorf("settings key %q was lost", key)
		}
	}
	beforeAccounts, _ := before["accounts"].([]any)
	afterAccounts, _ := after["accounts"].([]any)
	if len(beforeAccounts) != len(afterAccounts) {
		t.Errorf("accounts %d -> %d", len(beforeAccounts), len(afterAccounts))
	}
	// The session index is rebuilt from scratch on every write (deferred
	// entries + loaded ones), so an off-by-one here silently orphans sessions.
	beforeIndex, _ := before["session_results"].(map[string]any)
	afterIndex, _ := after["session_results"].(map[string]any)
	if len(beforeIndex) != len(afterIndex) {
		t.Errorf("session index %d -> %d entries", len(beforeIndex), len(afterIndex))
	}
	t.Logf("settings=%d accounts=%d sessions=%d survived the round trip",
		len(afterSettings), len(afterAccounts), len(afterIndex))
}

// copyRealState copies the user's state.json plus the session files of the
// ACTIVE accounts into a temp dir and points the App at it. Only active
// sessions need copying: Store.Load parks every other index entry in its
// deferred map without touching the disk, which keeps the fixture ~12 MB
// instead of ~110 MB.
func copyRealState(t *testing.T) (stateFile string, snapshot map[string]any) {
	t.Helper()
	realStateFile, realDataDir := realStatePaths(t)
	raw, err := os.ReadFile(realStateFile)
	if err != nil {
		t.Skipf("configured state file is unavailable (%s); skipping", realStateFile)
	}
	dir := t.TempDir()
	stateFile = filepath.Join(dir, "state.json")
	dataDir := filepath.Join(dir, "state_data")
	if err := os.WriteFile(stateFile, raw, 0o644); err != nil {
		t.Fatalf("copy state.json: %v", err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("unmarshal real state.json: %v", err)
	}

	active := map[string]bool{}
	rows, _ := snapshot["accounts"].([]any)
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		email, _ := m["email"].(string)
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			active[email] = true
		}
	}
	index, _ := snapshot["session_results"].(map[string]any)
	for email, item := range index {
		if !active[strings.ToLower(strings.TrimSpace(email))] {
			continue
		}
		rel := sessionRelPath(email)
		if m, ok := item.(map[string]any); ok {
			if p, _ := m["session_file"].(string); strings.TrimSpace(p) != "" {
				rel = strings.TrimSpace(p)
			}
		}
		src := filepath.Join(realDataDir, filepath.FromSlash(rel))
		payload, err := os.ReadFile(src)
		if err != nil {
			continue // Store.Load tolerates a missing file; so does this.
		}
		dst := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, payload, 0o644); err != nil {
			t.Fatalf("copy %s: %v", rel, err)
		}
	}

	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", dataDir)
	return stateFile, snapshot
}

// ---------------------------------------------------------------------------
// Job entry points
// ---------------------------------------------------------------------------

// TestStartRegisterRejectsUnknownAccount proves the refusal happens BEFORE a
// job exists — a bound method that spawns a goroutine and then discovers the
// account is missing has already spent the caller's confirmation.
func TestStartRegisterRejectsUnknownAccount(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	if _, err := app.StartRegister(StartRegisterRequest{
		Email: "nobody@example.com", CollectSession: true, Confirmed: true,
	}); err == nil {
		t.Fatal("expected 账号不存在")
	} else if !strings.Contains(err.Error(), "账号不存在") {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Fatalf("a rejected StartRegister created %d job(s)", len(jobs))
	}
	if _, err := app.GenerateLinks(GenerateLinksRequest{
		Email: "nobody@example.com", Confirmed: true,
	}); err == nil {
		t.Fatal("GenerateLinks: expected 账号不存在")
	}
}

// MONEY SAFETY: these exercise workerConfig DIRECTLY. They must never go through
// StartRegister/GenerateLinks with a resolvable account, because workerConfig no
// longer refuses anything — a successful config now proceeds straight to
// worker.New and launches a real browser against real endpoints. Every other job
// test in this file uses startStubJob for exactly that reason.

// The register/session run chains through settings.dynamic_proxies, NOT
// payment_dynamic_proxy: app.py:15343 only swaps to the payment pool when the
// "注册时使用支付链接动态代理（特殊情况勾选）" box is ticked. Reading the wrong key
// silently registers from this machine's own address instead of the residential
// exit, which is how accounts get flagged.
func TestRegisterDynamicProxyPoolSelection(t *testing.T) {
	const registerPool = "1.1.1.1:8080:u:p\n2.2.2.2:8080:u:p"
	const paymentPool = "9.9.9.9:9090:u:p"

	for _, tc := range []struct {
		name, routeMode, want string
		usePayment            bool
	}{
		{name: "default route takes the register pool", want: "http://u:p@1.1.1.1:8080"},
		{name: "the checkbox swaps in the payment pool", usePayment: true, want: "http://u:p@9.9.9.9:9090"},
		// _read_dynamic_proxies returns [] in this mode (app.py:16725), so the chain
		// degenerates to the local proxy alone.
		{name: "全走本地代理 empties every pool", routeMode: "全走本地代理", want: ""},
		{name: "and does so even with the checkbox on", routeMode: "全走本地代理", usePayment: true, want: ""},
	} {
		// Deliberately routed through the real decoder rather than a hand-built
		// Settings: the pool choice depends on load-time coercion (an unrecognised
		// proxy_route_mode falls back to 照旧, register_with_payment_proxy goes
		// through Python's bool()), so a literal struct would test the picker
		// while skipping the half that has historically been wrong.
		got := registerDynamicProxy(settings.FromSnapshot(map[string]any{
			"settings": map[string]any{
				"proxy_route_mode":            tc.routeMode,
				"dynamic_proxies":             registerPool,
				"payment_dynamic_proxy":       paymentPool,
				"register_with_payment_proxy": tc.usePayment,
			},
		}))
		if got != tc.want {
			t.Errorf("%s: registerDynamicProxy = %q, want %q", tc.name, got, tc.want)
		}
	}

	// An empty pool is not an error — it just means no dynamic hop.
	blank := settings.FromSnapshot(map[string]any{
		"settings": map[string]any{"dynamic_proxies": "   \n  "},
	})
	if got := registerDynamicProxy(blank); got != "" {
		t.Errorf("blank pool text = %q, want empty", got)
	}
}

// Only _refetch_account_once passes link_create_proxy / link_followup_proxy /
// link_approve_proxy (app.py:17903-17905). Every other constructor leaves them
// None, and worker.New then runs the ENTIRE payment pipeline through
// ExtractProxy — i.e. creates, follows up and approves a real payment link
// through the exit the account just logged in from, which is exactly what the
// three separate stages exist to avoid.
func TestWorkerConfigWiresTheLinkProxyCascadeForRelinkOnly(t *testing.T) {
	const registerPool = "1.1.1.1:8080:u:p"
	const createPool = "2.2.2.2:8080:u:p"
	const followupPool = "3.3.3.3:8080:u:p"
	const approvePool = "4.4.4.4:8080:u:p"

	newApp := func(t *testing.T, extra map[string]any) *App {
		s := map[string]any{
			"local_proxy":            "http://127.0.0.1:7897",
			"dynamic_proxies":        registerPool,
			"payment_dynamic_proxy":  createPool,
			"followup_dynamic_proxy": followupPool,
			"approve_dynamic_proxy":  approvePool,
		}
		for k, v := range extra {
			s[k] = v
		}
		return newTempApp(t, map[string]any{
			"schema_version": 2,
			"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
			"settings":       s,
		})
	}

	t.Run("relink gets one exit per stage", func(t *testing.T) {
		app := newApp(t, nil)
		account, err := app.accountByEmail("a@x.com")
		if err != nil {
			t.Fatalf("accountByEmail: %v", err)
		}
		cfg, res, err := app.workerConfig(context.Background(), JobRelink, account, func(string) {})
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		defer res.Close()

		for _, tc := range []struct {
			stage string
			got   *models.ProxyConfig
			want  string
		}{
			{"create", cfg.LinkCreateProxy, "http://u:p@2.2.2.2:8080"},
			{"followup", cfg.LinkFollowupProxy, "http://u:p@3.3.3.3:8080"},
			{"approve", cfg.LinkApproveProxy, "http://u:p@4.4.4.4:8080"},
		} {
			if tc.got == nil {
				t.Fatalf("%s stage is nil — the whole pipeline would fall back to the register exit", tc.stage)
			}
			if tc.got.DynamicProxy != tc.want {
				t.Errorf("%s exit = %q, want %q", tc.stage, tc.got.DynamicProxy, tc.want)
			}
			if !strings.HasPrefix(tc.got.ChainURL, "http://127.0.0.1:") {
				t.Errorf("%s has no live chain listener: %q", tc.stage, tc.got.ChainURL)
			}
		}
		// The browser half still leaves through the register pool.
		if cfg.ExtractProxy.DynamicProxy != "http://u:p@1.1.1.1:8080" {
			t.Errorf("extract exit = %q, want the register pool", cfg.ExtractProxy.DynamicProxy)
		}

		// Every listener dies with the job.
		urls := []string{cfg.LinkCreateProxy.ChainURL, cfg.LinkFollowupProxy.ChainURL, cfg.LinkApproveProxy.ChainURL}
		res.Close()
		for _, u := range urls {
			if conn, err := net.Dial("tcp", strings.TrimPrefix(u, "http://")); err == nil {
				_ = conn.Close()
				t.Errorf("link chain %s still accepting after Close", u)
			}
		}
	})

	// app.py:17918: the 特殊情况 checkbox redirects the LOGIN hop to the create
	// stage. It does not move the browser extraction, which keeps using the
	// register pool, and it does not point the login at payment_dynamic_proxy[0]
	// — that value is only one input to the triple.
	t.Run("特殊情况 moves only the login hop, and to the create stage", func(t *testing.T) {
		app := newApp(t, map[string]any{"register_with_payment_proxy": true})
		account, _ := app.accountByEmail("a@x.com")
		cfg, res, err := app.workerConfig(context.Background(), JobRelink, account, func(string) {})
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		defer res.Close()

		if cfg.RegisterProxy.DynamicProxy != "http://u:p@2.2.2.2:8080" {
			t.Errorf("register exit = %q, want the create stage", cfg.RegisterProxy.DynamicProxy)
		}
		if cfg.ExtractProxy.DynamicProxy != "http://u:p@1.1.1.1:8080" {
			t.Errorf("extract exit = %q, want the register pool regardless of the checkbox",
				cfg.ExtractProxy.DynamicProxy)
		}
	})

	for _, kind := range []JobKind{JobRegister, JobAuthOnly, JobTeam, JobRegisterAndRT} {
		t.Run(string(kind)+" leaves the cascade unset", func(t *testing.T) {
			app := newApp(t, nil)
			account, _ := app.accountByEmail("a@x.com")
			cfg, res, err := app.workerConfig(context.Background(), kind, account, func(string) {})
			if err != nil {
				t.Fatalf("workerConfig: %v", err)
			}
			defer res.Close()
			if cfg.LinkCreateProxy != nil || cfg.LinkFollowupProxy != nil || cfg.LinkApproveProxy != nil {
				t.Errorf("%s wired the link cascade; only _refetch_account_once passes it", kind)
			}
		})
	}
}

// app.py:17722 is `extract_proxy = register_proxy`. If the Session fetch left
// through a different exit than the registration, OpenAI would see the login move
// addresses mid-flow.
func TestWorkerConfigChainsRegisterAndExtractIdentically(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
		"settings": map[string]any{
			"local_proxy":     "http://127.0.0.1:7897",
			"dynamic_proxies": "1.1.1.1:8080:u:p",
		},
	})
	account, err := app.accountByEmail("a@x.com")
	if err != nil {
		t.Fatalf("accountByEmail: %v", err)
	}

	cfg, res, err := app.workerConfig(context.Background(), JobRegister, account, func(string) {})
	if err != nil {
		t.Fatalf("workerConfig: %v", err)
	}
	defer res.Close()

	if cfg.RegisterProxy != cfg.ExtractProxy {
		t.Errorf("register %+v != extract %+v; app.py assigns the same object",
			cfg.RegisterProxy, cfg.ExtractProxy)
	}
	if cfg.RegisterProxy.DynamicProxy != "http://u:p@1.1.1.1:8080" {
		t.Errorf("dynamic hop = %q, want the register pool entry", cfg.RegisterProxy.DynamicProxy)
	}
	if !strings.HasPrefix(cfg.RegisterProxy.ChainURL, "http://127.0.0.1:") {
		t.Errorf("chain URL = %q, want a live local chain listener", cfg.RegisterProxy.ChainURL)
	}
	// G3: without a provider a run that reaches phone verification cannot finish.
	// It is always constructed; smsbower_enabled (off in this fixture) is what
	// decides whether it may rent, so building it here cannot spend anything.
	if cfg.PhoneProvider == nil {
		t.Error("PhoneProvider is nil — a registration hitting phone verification would stall")
	}
	if n := len(res.phone.OutstandingActivations()); n != 0 {
		t.Errorf("%d rentals outstanding before the run even started", n)
	}

	// The listener is the job's, and must die with it.
	url := strings.TrimPrefix(cfg.RegisterProxy.ChainURL, "http://")
	res.Close()
	if conn, err := net.Dial("tcp", url); err == nil {
		_ = conn.Close()
		t.Error("chain listener still accepting after the session closed")
	}
}

// With nothing configured at all the run is direct — Python's ProxyChainServer
// returns without binding (app.py:5937) rather than failing.
func TestWorkerConfigAllowsDirectWithNoProxies(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
		"settings":       map[string]any{"local_proxy": "", "dynamic_proxies": ""},
	})
	account, _ := app.accountByEmail("a@x.com")

	var logged []string
	cfg, res, err := app.workerConfig(context.Background(), JobRegister, account, func(s string) { logged = append(logged, s) })
	if err != nil {
		t.Fatalf("workerConfig: %v", err)
	}
	defer res.Close()

	if cfg.RegisterProxy.ChainURL != "" {
		t.Errorf("chain URL = %q, want none", cfg.RegisterProxy.ChainURL)
	}
	// Registering from the operator's own address is legal but must not be silent.
	if !slices.ContainsFunc(logged, func(s string) bool { return strings.Contains(s, "直连") }) {
		t.Errorf("a direct run was not announced; logged %q", logged)
	}
}

// An account the state file does not know about fails synchronously, before any
// job — and therefore before any browser — exists.
func TestBoundEntryPointsRejectUnknownAccountBeforeStarting(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
	})
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"StartRegister", func() error {
			_, err := app.StartRegister(StartRegisterRequest{
				Email: "nobody@x.com", CollectSession: true, Confirmed: true,
			})
			return err
		}},
		{"GenerateLinks", func() error {
			_, err := app.GenerateLinks(GenerateLinksRequest{
				Email: "nobody@x.com", Confirmed: true,
			})
			return err
		}},
	} {
		if err := call.fn(); err == nil {
			t.Errorf("%s accepted an unknown account", call.name)
		} else if !strings.Contains(err.Error(), "账号不存在") {
			t.Errorf("%s error = %v, want 账号不存在", call.name, err)
		}
	}
	if jobs := app.ListJobs(); len(jobs) != 0 {
		t.Errorf("a rejected request still created %d job(s)", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// StopAll
// ---------------------------------------------------------------------------

// TestStopAllOnEmptyRegistry: 停止 is the button a user mashes when they are not
// sure what is running.
func TestStopAllOnEmptyRegistry(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	for i := 0; i < 3; i++ {
		if err := app.StopAll(); err != nil {
			t.Fatalf("StopAll on an empty registry: %v", err)
		}
	}
	// And again once every job has already finished.
	app.startStubJob("done", func(context.Context) error { return nil })
	waitStatus(t, app, "done", StatusSucceeded)
	if err := app.StopAll(); err != nil {
		t.Fatalf("StopAll with only finished jobs: %v", err)
	}
}

func TestStopAllCancelsEveryRunningJob(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	for _, id := range []string{"s1", "s2", "s3"} {
		app.startStubJob(id, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}
	if err := app.StopAll(); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		waitStatus(t, app, id, StatusCancelled)
	}
}

// ---------------------------------------------------------------------------
// Manual input round-trip
// ---------------------------------------------------------------------------

// answerable runs a stub job that parks on inputCallback, and returns the
// channel the answer eventually lands on.
func answerable(t *testing.T, app *App, id string) <-chan string {
	t.Helper()
	got := make(chan string, 1)
	app.startStubJob(id, func(ctx context.Context) error {
		got <- app.inputCallback(ctx, id)("email-code", "a@x.com", "请输入验证码")
		return nil
	})
	// The callback installs its channel on the job; wait for it rather than
	// racing the goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.jobs.mu.Lock()
		pending := app.jobs.jobs[id].prompt != nil
		app.jobs.mu.Unlock()
		if pending {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never registered a prompt", id)
	return got
}

func TestAnswerPromptRoundTrip(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	got := answerable(t, app, "p1")

	if err := app.AnswerPrompt("p1", "123456"); err != nil {
		t.Fatalf("AnswerPrompt: %v", err)
	}
	select {
	case answer := <-got:
		if answer != "123456" {
			t.Fatalf("worker received %q, want 123456", answer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the answer never reached the blocked worker")
	}
	// The prompt is consumed: answering twice must report, not double-deliver.
	if err := app.AnswerPrompt("p1", "999999"); err == nil {
		t.Error("expected an error answering a prompt that is no longer pending")
	}
}

// TestStopAllReleasesPendingPrompts is app.py:15103-15108 — a worker parked on
// a prompt must not survive 停止.
func TestStopAllReleasesPendingPrompts(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	got := answerable(t, app, "p2")

	if err := app.StopAll(); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	select {
	case answer := <-got:
		if answer != "" {
			t.Fatalf("worker received %q, want the empty cancel answer", answer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopAll left a worker blocked on a prompt")
	}
}

func TestAnswerPromptUnknownJob(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	if err := app.AnswerPrompt("no-such-job", "123456"); err == nil {
		t.Fatal("expected an error for an unknown job id")
	} else if !strings.Contains(err.Error(), "任务不存在") {
		t.Fatalf("unexpected error: %v", err)
	}

	// A job that exists but is not waiting on anything is a different failure.
	app.startStubJob("idle", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if err := app.AnswerPrompt("idle", "123456"); err == nil {
		t.Fatal("expected an error for a job with no pending prompt")
	} else if !strings.Contains(err.Error(), "没有等待输入") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = app.StopAll()
}

// app.py:14085 gates the overwrite on key PRESENCE, and the Tk default is
// http://127.0.0.1:7890 (app.py:12340). An absent local_proxy is therefore NOT a
// direct run — treating it as one would register from this machine's own address.
func TestAbsentLocalProxyFallsBackToTheTkDefault(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("a@x.com", "free", "", "未分组")},
		// No local_proxy key at all.
		"settings": map[string]any{"dynamic_proxies": ""},
	})
	account, _ := app.accountByEmail("a@x.com")
	cfg, res, err := app.workerConfig(context.Background(), JobRegister, account, func(string) {})
	if err != nil {
		t.Fatalf("workerConfig: %v", err)
	}
	defer res.Close()

	if cfg.RegisterProxy.LocalProxy != settings.DefaultLocalProxy {
		t.Errorf("local proxy = %q, want the Tk default %q",
			cfg.RegisterProxy.LocalProxy, settings.DefaultLocalProxy)
	}
	if cfg.RegisterProxy.ChainURL == "" {
		t.Error("no chain was started, so the run would go out direct")
	}
}
