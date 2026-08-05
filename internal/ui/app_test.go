package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func requireRealStateTests(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_REGISTER_REAL_STATE_TEST") != "1" {
		t.Skip("仅在显式设置 OPENAI_REGISTER_REAL_STATE_TEST=1 时读取真实状态")
	}
}

func TestNewUsesPortableDefaultPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENAI_REGISTER_HOME", root)
	t.Setenv("STATE_FILE", "")
	t.Setenv("STATE_DATA_DIR", "")

	app := New()
	if got, want := app.stateFile, filepath.Join(root, "state.json"); got != want {
		t.Fatalf("stateFile=%q，期望 %q", got, want)
	}
	if got, want := app.dataDir, filepath.Join(root, "state_data"); got != want {
		t.Fatalf("dataDir=%q，期望 %q", got, want)
	}
}

// TestLoadSummaryAgainstRealState exercises the Wails binding against the user's
// actual state.json. The GUI itself cannot be asserted on headlessly, so this is
// what proves the frontend's two startup calls return real data rather than a
// zero value — a binding that silently returns an empty struct looks identical
// to a working one in a screenshot.
func TestLoadSummaryAgainstRealState(t *testing.T) {
	requireRealStateTests(t)
	app := New()

	env := app.Environment()
	if env.GoVersion == "" {
		t.Fatal("Environment().GoVersion is empty")
	}
	if _, err := os.Stat(env.StateFile); err != nil {
		t.Skipf("state file not present on this machine (%s); skipping", env.StateFile)
	}
	if !env.StateOK {
		t.Fatalf("state file exists but StateOK is false: %s", env.StateFile)
	}

	summary, err := app.LoadSummary()
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if summary.Accounts == 0 {
		t.Errorf("read 0 accounts from %s — the loader is not seeing real data", env.StateFile)
	}
	if len(summary.SettingsKeys) == 0 {
		t.Errorf("read 0 settings keys — settings.* is where smsbower_api_key lives")
	}
	// Sessions live under "session_results", rebuilt from the split files under
	// state_data/sessions/. Reading the wrong key returns 0 and looks fine.
	if summary.Sessions == 0 {
		t.Errorf("read 0 sessions — expected the split session index to be rebuilt")
	}
	t.Logf("accounts=%d sessions=%d settingsKeys=%d",
		summary.Accounts, summary.Sessions, len(summary.SettingsKeys))
}

// TestLogBeforeStartupIsSafe: Wails calls OnStartup after construction, so any
// log emitted in between has a nil context. Dropping it must not panic.
func TestLogBeforeStartupIsSafe(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts": []any{
			accountMap("Person@example.com", "free", "", "未分组"),
		},
	})
	app.Log("[person@example.com] 提取长链失败")
	got := app.SelectLogAccount("PERSON@example.com")
	if len(got.Account) != 1 {
		t.Fatalf("启动前日志未进入账号缓冲区: %#v", got)
	}
	if got.Account[0].Email != "person@example.com" || got.Account[0].Level != "error" {
		t.Fatalf("日志路由或级别不正确: %#v", got.Account[0])
	}
	if got.AccountTitle != "选中账户日志：Person@example.com" {
		t.Fatalf("账号日志标题=%q", got.AccountTitle)
	}
}

// repairState is the only write the app performs without the user asking, so pin
// both when it fires and — more importantly — when it does NOT.
func TestRepairStateRewritesOnlyWhenNeeded(t *testing.T) {
	newAppWithSession := func(t *testing.T, writeSession bool) (*App, string) {
		t.Helper()
		dir := t.TempDir()
		dataDir := filepath.Join(dir, "state_data")
		if writeSession {
			writeSessionFile(t, dataDir, "a@x.com", map[string]any{"access_token": "tok"})
		}
		stateFile := filepath.Join(dir, "state.json")
		writeJSONFile(t, stateFile, map[string]any{
			"schema_version":  2,
			"accounts":        []any{accountMap("a@x.com", "free", "", "未分组")},
			"session_results": map[string]any{"a@x.com": sessionIndexEntry("a@x.com")},
		})
		t.Setenv("STATE_FILE", stateFile)
		t.Setenv("STATE_DATA_DIR", dataDir)
		return New(), stateFile
	}

	// Healthy state: nothing is missing, so the file must be left completely alone.
	app, stateFile := newAppWithSession(t, true)
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	app.repairState()
	if app.store.MissingSessionFiles {
		t.Fatal("a state dir with all its session files reported one missing")
	}
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Error("repairState rewrote a healthy state.json; startup must not touch user data for nothing")
	}

	// Session file gone: Load drops it, and the repair must take it out of the
	// persisted index too, or every later start warns about it again.
	app, stateFile = newAppWithSession(t, false)
	app.repairState()
	if !app.store.MissingSessionFiles {
		t.Fatal("a missing session file was not detected")
	}
	repaired := readJSONFile(t, stateFile)
	sessions, _ := repaired["session_results"].(map[string]any)
	if _, stillThere := sessions["a@x.com"]; stillThere {
		t.Errorf("the orphaned index entry survived the repair: %v", sessions)
	}
	// The account itself is untouched — only the session index was stale.
	if rows, _ := repaired["accounts"].([]any); len(rows) != 1 {
		t.Errorf("accounts = %v, want the one account preserved", repaired["accounts"])
	}
}
