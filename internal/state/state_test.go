package state

// state_test.go is the first test coverage this package has had, and it is the
// package that reads and WRITES the user's real state.json and every per-account
// session file. Everything pinned here was checked against the Python StateStore
// (app.py:1961-2172), and the four behaviours in the "Python semantics" section
// were computed by running the verbatim Python idiom under CPython 3.12 — each
// of them was a live divergence before these tests existed.
//
// No test touches the real state file. Every path is under t.TempDir().

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "state.json"), filepath.Join(dir, "state_data"))
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
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

func account(email string) map[string]any { return map[string]any{"email": email} }

// seed writes a v2 state.json plus one session file per payload, the way the
// store itself would, and returns the store.
func seed(t *testing.T, accounts []any, payloads map[string]map[string]any) *Store {
	t.Helper()
	s := newStore(t)
	index := map[string]any{}
	for email, payload := range payloads {
		rel := sessionRelPath(email)
		index[email] = map[string]any{"session_file": rel}
		writeJSON(t, filepath.Join(s.DataDir, rel), map[string]any{
			"email": email, "updated_at": "2026-07-27", "payload": payload,
		})
	}
	snapshot := map[string]any{"schema_version": 2, "session_results": index}
	if accounts != nil {
		snapshot["accounts"] = accounts
	}
	writeJSON(t, s.StateFile, snapshot)
	return s
}

func sessionOf(t *testing.T, data map[string]any, email string) map[string]any {
	t.Helper()
	sessions, ok := data["session_results"].(map[string]any)
	if !ok {
		t.Fatalf("session_results is %T, not an object", data["session_results"])
	}
	payload, _ := sessions[email].(map[string]any)
	return payload
}

// ---------------------------------------------------------------------------
// Python semantics — each of these was a real divergence
// ---------------------------------------------------------------------------

// TestLoadWithNoAccountsStillLoadsEverySession pins app.py:2064's
// `if active_email_keys and ...`: an EMPTY SET IS FALSY, so "no active
// accounts" means "defer nothing".
//
// Go's activeEmailKeys returns a non-nil empty map for a state.json whose
// `accounts` key is missing, null, or not a list. Testing it with `!= nil`
// deferred EVERY session, so the UI came up with an empty session_results — and
// the merge in persist.go reads that as "this account had no prior session" and
// drops fields on the next write.
func TestLoadWithNoAccountsStillLoadsEverySession(t *testing.T) {
	payloads := map[string]map[string]any{"a@b.com": {"session_json": "tok"}}

	for _, tt := range []struct {
		name     string
		accounts []any
	}{
		{"key absent", nil},
		{"empty list", []any{}},
		{"only blank emails", []any{account("   "), account("")}},
		{"entries that are not objects", []any{"a@b.com", 42}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := seed(t, tt.accounts, payloads)
			data, err := s.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := sessionOf(t, data, "a@b.com"); got == nil {
				t.Fatalf("session was deferred; python loads it (accounts=%v)", tt.accounts)
			}
			if len(s.deferredSessionIndex) != 0 {
				t.Errorf("deferred %v, want nothing deferred", s.deferredSessionIndex)
			}
		})
	}
}

// With at least one real account the filter DOES apply — the deferral is a load
// optimisation for a state.json holding sessions for accounts the user deleted.
func TestLoadDefersSessionsForInactiveAccounts(t *testing.T) {
	s := seed(t, []any{account("keep@b.com")}, map[string]map[string]any{
		"keep@b.com": {"session_json": "kept"},
		"gone@b.com": {"session_json": "deferred"},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sessionOf(t, data, "keep@b.com"); got["session_json"] != "kept" {
		t.Errorf("active session = %v", got)
	}
	if got := sessionOf(t, data, "gone@b.com"); got != nil {
		t.Errorf("inactive session was loaded eagerly: %v", got)
	}

	// It is still reachable, and reading it removes it from the deferred index
	// (app.py:2124 `deferred_session_index.pop`) so the next write does not
	// resurrect the stale index entry over the live one.
	got := s.LoadDeferredSession("GONE@B.com") // case-insensitive lookup
	if got == nil || got["session_json"] != "deferred" {
		t.Fatalf("LoadDeferredSession = %v", got)
	}
	if _, still := s.deferredSessionIndex["gone@b.com"]; still {
		t.Error("deferred index still holds the entry after it was loaded")
	}
	if again := s.LoadDeferredSession("gone@b.com"); again != nil {
		t.Errorf("second LoadDeferredSession = %v, want nil", again)
	}
	if miss := s.LoadDeferredSession("never@b.com"); miss != nil {
		t.Errorf("unknown address = %v, want nil", miss)
	}
}

// TestSessionIndexFlagsUsePythonTruthiness pins app.py:2136-2138, which are
// `str(payload.get(k) or "")` — the `or` runs on the RAW value, so a JSON false
// or 0 collapses to "". Stringifying first gives "false" / "0" and flips
// has_session_json to true for an account that has no session; the 有 Session
// column and the export gates read these flags.
//
// Expectations computed under CPython 3.12:
//
//	'tok' -> 'tok' True | '' -> '' False | None -> '' False | 0 -> '' False
//	False -> '' False | True -> 'True' True | 5 -> '5' True | [] -> '' False
//	{} -> '' False | '0' -> '0' True | 'false' -> 'false' True
func TestSessionIndexFlagsUsePythonTruthiness(t *testing.T) {
	tests := []struct {
		value    any
		wantFlag bool
		wantText string
	}{
		{"tok", true, "tok"},
		{"", false, ""},
		{nil, false, ""},
		{float64(0), false, ""},
		{false, false, ""},
		{true, true, "true"}, // see the note below on str(True)
		{float64(5), true, "5"},
		{[]any{}, false, ""},
		{map[string]any{}, false, ""},
		{"0", true, "0"},
		{"false", true, "false"},
	}
	for _, tt := range tests {
		if got := pyTruthy(tt.value); got != tt.wantFlag {
			t.Errorf("pyTruthy(%#v) = %v, python says %v", tt.value, got, tt.wantFlag)
		}
		if got := pyStrOrEmpty(tt.value); got != tt.wantText {
			t.Errorf("pyStrOrEmpty(%#v) = %q, want %q", tt.value, got, tt.wantText)
		}
		if got := pyStrOrEmpty(tt.value) != ""; got != tt.wantFlag {
			t.Errorf("pyStrOrEmpty(%#v) != \"\" = %v, python says %v", tt.value, got, tt.wantFlag)
		}
	}

	// KNOWN, BOUNDED DIVERGENCE: Python's str(True) is "True"; Go's %v is "true".
	// It only reaches the file through payment_link_type, which every producer
	// writes as a string, so a bool there is already corrupt data. Recorded here
	// rather than papered over, because the truthiness above — which is what the
	// flags depend on — DOES match.
	if got := pyStrOrEmpty(true); got != "true" {
		t.Errorf("pyStrOrEmpty(true) = %q, want the documented Go spelling %q", got, "true")
	}
}

func TestSessionIndexFlagsEndToEnd(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at": "2026-07-27",
		"accounts":   []any{account("a@b.com"), account("c@d.com")},
		"session_results": map[string]any{
			"a@b.com": map[string]any{"session_json": "tok", "storage_state_json": ""},
			// A JSON false is what a hand-edited or half-migrated file can hold.
			"c@d.com": map[string]any{"session_json": false, "storage_state_json": float64(0)},
		},
	}, nil, true)

	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	a, _ := index["a@b.com"].(map[string]any)
	c, _ := index["c@d.com"].(map[string]any)
	if a["has_session_json"] != true || a["has_storage_state_json"] != false {
		t.Errorf("a@b.com index = %v", a)
	}
	if c["has_session_json"] != false || c["has_storage_state_json"] != false {
		t.Errorf("c@d.com index = %v, python collapses false/0 to \"\"", c)
	}
}

// TestSchemaVersionParsing pins app.py:1985 `int(data.get("schema_version") or 1)`.
// int() RAISES on a string it cannot parse, and Python's load() lets that
// escape — the Tk app reads NOTHING rather than guessing.
//
// Guessing is the dangerous answer: read a legacy file as v2 and
// loadSplitSessions gets a payload map where it expects an index, returns {},
// and the next save writes that emptiness over every session on disk.
//
// Expectations computed under CPython 3.12. Note two places where int() is
// WIDER than strconv.Atoi ("3_0" is 30, "٢" is 2) and two where it is
// NARROWER than fmt.Sscanf("%d") ("2abc" and "0x10" raise where Sscanf returns
// 2 and 0).
func TestSchemaVersionParsing(t *testing.T) {
	tests := []struct {
		in    any
		want  int
		raise bool
	}{
		{nil, 1, false},
		{float64(1), 1, false},
		{float64(2), 2, false},
		{"2", 2, false},
		{" 2 ", 2, false},
		{float64(2.9), 2, false},   // int() truncates toward zero
		{float64(-2.9), -2, false}, // ... in both directions
		{true, 1, false},           // int(True) == 1
		{false, 1, false},          // False is falsy -> the default
		{float64(0), 1, false},
		{"", 1, false},
		{[]any{}, 1, false},
		{map[string]any{}, 1, false},
		{"3_0", 30, false},   // underscores may separate digits
		{"\u0662", 2, false}, // ٢ ARABIC-INDIC DIGIT TWO
		{"+7", 7, false},
		{"-1", -1, false},
		{"2abc", 0, true},
		{"0x10", 0, true},
		{"_1", 0, true},
		{"1_", 0, true},
		{"1__2", 0, true},
		{"2.0", 0, true}, // int("2.0") raises; only float(2.0) truncates
		// A whitespace-only string is TRUTHY (it is non-empty), so `or 1` does
		// not fire and int(" ") raises — the one case where "looks blank" and
		// "is falsy" disagree.
		{" ", 0, true},
	}
	for _, tt := range tests {
		got, err := pyIntOr(tt.in, 1)
		if tt.raise {
			if err == nil {
				t.Errorf("pyIntOr(%#v) = %d, python raises ValueError", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("pyIntOr(%#v): %v, python says %d", tt.in, err, tt.want)
		} else if got != tt.want {
			t.Errorf("pyIntOr(%#v) = %d, python says %d", tt.in, got, tt.want)
		}
	}
}

func TestLoadRefusesACorruptSchemaVersion(t *testing.T) {
	s := newStore(t)
	writeJSON(t, s.StateFile, map[string]any{
		"schema_version":  "2abc",
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "tok"}},
	})
	if _, err := s.Load(); err == nil {
		t.Fatal("a corrupt schema_version loaded silently; python refuses the whole file")
	}
}

// TestUpdatedAtIsNotStringified pins app.py:2139/2143, which read
// `data.get("updated_at", "")` with NO str() — a numeric updated_at stays a
// JSON number in the index and the session file. Stringifying would change its
// type in a file the Python app also reads.
func TestUpdatedAtIsNotStringified(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at":      float64(1753560000),
		"accounts":        []any{account("a@b.com")},
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "tok"}},
	}, nil, true)

	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	entry, _ := index["a@b.com"].(map[string]any)
	if got, ok := entry["updated_at"].(float64); !ok || got != 1753560000 {
		t.Errorf("index updated_at = %#v, want the number unchanged", entry["updated_at"])
	}
	file := readJSON(t, filepath.Join(s.DataDir, sessionRelPath("a@b.com")))
	if got, ok := file["updated_at"].(float64); !ok || got != 1753560000 {
		t.Errorf("session file updated_at = %#v, want the number unchanged", file["updated_at"])
	}

	// An ABSENT key is "" in Python (`.get(k, "")`), not null.
	s2 := newStore(t)
	s2.Save(map[string]any{
		"accounts":        []any{account("a@b.com")},
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "tok"}},
	}, nil, true)
	index2, _ := readJSON(t, s2.StateFile)["session_results"].(map[string]any)
	entry2, _ := index2["a@b.com"].(map[string]any)
	if entry2["updated_at"] != "" {
		t.Errorf("absent updated_at = %#v, want \"\"", entry2["updated_at"])
	}
}

// TestNumbersRoundTripThroughFloat64 pins the known float64 gap Load documents,
// so the day someone turns on UseNumber this test tells them what changed rather
// than the user's state.json telling them.
//
// The first row is the only one that actually moves against the real file: its
// twelve device_scale_factor entries come back as `1`. The rest prove the gap is
// bounded — the sash ratios and 1.25 are exact, and the >2^53 row is what would
// genuinely corrupt data if such a value ever appeared.
func TestNumbersRoundTripThroughFloat64(t *testing.T) {
	for _, tt := range []struct{ stored, want string }{
		{"1.0", "1"}, // GAP: Python writes 1.0
		{"1.25", "1.25"},
		{"0.4972375690607735", "0.4972375690607735"},
		{"0.27", "0.27"},
		{"2", "2"},
		{"-3", "-3"},
		{"1753560000", "1753560000"},
		{"9007199254740993", "9007199254740992"}, // GAP: 2^53+1 is not representable
	} {
		s := newStore(t)
		if err := os.WriteFile(s.StateFile,
			[]byte(`{"schema_version":2,"n":`+tt.stored+`,"session_results":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		data, err := s.Load()
		if err != nil {
			t.Fatalf("%s: Load: %v", tt.stored, err)
		}
		s.Save(data, nil, true)
		raw, err := os.ReadFile(s.StateFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := `"n": ` + tt.want; !strings.Contains(string(raw), got) {
			t.Errorf("%s round-tripped to something other than %q:\n%s", tt.stored, tt.want, raw)
		}
	}
}

// TestSessionRelPath pins the file name: sha256(email.strip().lower())[:24].
// Change it and every existing session file on disk becomes unreachable.
// Digests computed under CPython 3.12.
func TestSessionRelPath(t *testing.T) {
	for _, tt := range []struct{ email, want string }{
		{"a@b.com", "sessions/fb98d44ad7501a959f3f4f4a.json"},
		{"  A@B.COM  ", "sessions/fb98d44ad7501a959f3f4f4a.json"}, // trim then lower
		{"用户@例子.测试", "sessions/fb290d904dc1fad349bc69f4.json"},
		{"x", "sessions/2d711642b726b04401627ca9.json"},
	} {
		if got := sessionRelPath(tt.email); got != tt.want {
			t.Errorf("sessionRelPath(%q) = %q, python says %q", tt.email, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	s := newStore(t)
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Load of a missing file = %v, want an empty map", data)
	}
}

func TestLoadLegacyKeepsThePayloadsInline(t *testing.T) {
	s := newStore(t)
	writeJSON(t, s.StateFile, map[string]any{
		"schema_version":  1,
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "inline"}},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.LoadedLegacy {
		t.Error("LoadedLegacy is false for a schema_version 1 file")
	}
	if got := sessionOf(t, data, "a@b.com"); got["session_json"] != "inline" {
		t.Errorf("legacy payload = %v, want it read in place", got)
	}

	// A legacy file whose session_results is not an object gets an empty one
	// rather than being left as a string the callers would type-assert on.
	s2 := newStore(t)
	writeJSON(t, s2.StateFile, map[string]any{"schema_version": 1, "session_results": "nope"})
	data2, err := s2.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := data2["session_results"].(map[string]any); !ok || len(got) != 0 {
		t.Errorf("session_results = %#v, want an empty object", data2["session_results"])
	}
}

func TestLoadReportsAMissingSessionFile(t *testing.T) {
	s := seed(t, []any{account("a@b.com"), account("b@b.com")}, map[string]map[string]any{
		"a@b.com": {"session_json": "tok"},
	})
	// Index an account whose file was never written.
	raw := readJSON(t, s.StateFile)
	index, _ := raw["session_results"].(map[string]any)
	index["b@b.com"] = map[string]any{"session_file": sessionRelPath("b@b.com")}
	writeJSON(t, s.StateFile, raw)

	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.MissingSessionFiles {
		t.Error("MissingSessionFiles is false; repairState will not rewrite the index")
	}
	if got := sessionOf(t, data, "a@b.com"); got == nil {
		t.Error("the healthy session was dropped along with the missing one")
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "b@b.com") {
		t.Errorf("Warnings = %v", s.Warnings)
	}
}

// The index holds a caller-supplied relative path. A "../" in it would let a
// hand-edited state.json make the app read an arbitrary file.
func TestLoadRefusesAPathOutsideTheDataDir(t *testing.T) {
	s := newStore(t)
	writeJSON(t, s.StateFile, map[string]any{
		"schema_version": 2,
		"accounts":       []any{account("a@b.com")},
		"session_results": map[string]any{
			"a@b.com": map[string]any{"session_file": "../../secrets.json"},
		},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sessionOf(t, data, "a@b.com"); got != nil {
		t.Errorf("an out-of-tree path was read: %v", got)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "越界") {
		t.Errorf("Warnings = %v, want the 越界 refusal", s.Warnings)
	}
}

// Python's ThreadPoolExecutor.map yields in INPUT order; Go's map range is
// random and the loads finish out of order, so the warning list has to be
// sorted into a stable order or the same file reports differently every run.
func TestLoadWarningOrderIsDeterministic(t *testing.T) {
	accounts := []any{}
	index := map[string]any{}
	for i := 0; i < 12; i++ {
		email := fmt.Sprintf("m%02d@b.com", i)
		accounts = append(accounts, account(email))
		index[email] = map[string]any{"session_file": sessionRelPath(email)}
	}
	var first []string
	for run := 0; run < 5; run++ {
		s := newStore(t)
		writeJSON(t, s.StateFile, map[string]any{
			"schema_version": 2, "accounts": accounts, "session_results": index,
		})
		if _, err := s.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if run == 0 {
			first = append([]string(nil), s.Warnings...)
			if len(first) != 12 {
				t.Fatalf("got %d warnings, want 12", len(first))
			}
			continue
		}
		if !reflect.DeepEqual(first, s.Warnings) {
			t.Fatalf("run %d warnings differ from run 0:\n  %v\n  %v", run, first, s.Warnings)
		}
	}
}

// A session file may be the bare payload or the {email, updated_at, payload}
// envelope the store itself writes (app.py:2093-2094).
func TestReadSessionPayloadAcceptsBothShapes(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.json")
	writeJSON(t, bare, map[string]any{"session_json": "bare"})
	if got, err := readSessionPayload(bare); err != nil || got["session_json"] != "bare" {
		t.Errorf("bare = %v, %v", got, err)
	}
	wrapped := filepath.Join(dir, "wrapped.json")
	writeJSON(t, wrapped, map[string]any{"email": "a@b.com", "payload": map[string]any{"session_json": "inner"}})
	if got, err := readSessionPayload(wrapped); err != nil || got["session_json"] != "inner" {
		t.Errorf("wrapped = %v, %v", got, err)
	}
	// A top-level null is "not an object", not a crash.
	nullFile := filepath.Join(dir, "null.json")
	if err := os.WriteFile(nullFile, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionPayload(nullFile); err == nil {
		t.Error("a null session file was accepted")
	}
}

// ---------------------------------------------------------------------------
// Save
// ---------------------------------------------------------------------------

func TestSaveThenLoadRoundTrips(t *testing.T) {
	s := newStore(t)
	snapshot := map[string]any{
		"updated_at": "2026-07-27",
		"accounts":   []any{account("a@b.com"), account("c@d.com")},
		"settings":   map[string]any{"local_proxy": "http://127.0.0.1:7890"},
		"session_results": map[string]any{
			"a@b.com": map[string]any{"session_json": "tokA", "openai_rt": "rt_a"},
			"c@d.com": map[string]any{"session_json": "tokC"},
		},
	}
	s.Save(snapshot, nil, true)

	// The caller's own map must not have been mutated — writeSnapshot pops
	// session_results out of a COPY.
	if _, ok := snapshot["session_results"]; !ok {
		t.Fatal("Save removed session_results from the caller's snapshot")
	}

	back := New(s.StateFile, s.DataDir)
	data, err := back.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sessionOf(t, data, "a@b.com"); got["session_json"] != "tokA" || got["openai_rt"] != "rt_a" {
		t.Errorf("a@b.com round-tripped as %v", got)
	}
	if got := sessionOf(t, data, "c@d.com"); got["session_json"] != "tokC" {
		t.Errorf("c@d.com round-tripped as %v", got)
	}
	settings, _ := data["settings"].(map[string]any)
	if settings["local_proxy"] != "http://127.0.0.1:7890" {
		t.Errorf("settings round-tripped as %v", data["settings"])
	}
	if data["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v", data["schema_version"])
	}
}

// The index is the only thing state.json holds for a session; the payload must
// NOT be inlined there, or the file grows without bound and Load reads the
// index as if it were a payload.
func TestSaveWritesAnIndexNotThePayload(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at":      "2026-07-27",
		"accounts":        []any{account("a@b.com")},
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "tok", "secret": "s"}},
	}, nil, true)

	raw, err := os.ReadFile(s.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"secret"`) {
		t.Error("the payload was inlined into state.json")
	}
	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	entry, _ := index["a@b.com"].(map[string]any)
	if entry["session_file"] != sessionRelPath("a@b.com") {
		t.Errorf("index entry = %v", entry)
	}
}

// dirty is the "which session files changed" set. A session already on disk and
// NOT in the set must not be rewritten (that is the whole point of the set);
// one whose file is MISSING is rewritten regardless, because the index would
// otherwise point at nothing (app.py:2141).
func TestSaveRewritesOnlyDirtySessions(t *testing.T) {
	s := newStore(t)
	snapshot := map[string]any{
		"updated_at": "1",
		"accounts":   []any{account("a@b.com"), account("c@d.com")},
		"session_results": map[string]any{
			"a@b.com": map[string]any{"session_json": "v1"},
			"c@d.com": map[string]any{"session_json": "v1"},
		},
	}
	s.Save(snapshot, nil, true) // nil == all dirty, so both files exist now

	pathA := filepath.Join(s.DataDir, sessionRelPath("a@b.com"))
	pathC := filepath.Join(s.DataDir, sessionRelPath("c@d.com"))
	statA, _ := os.Stat(pathA)

	// Change both payloads but declare only c@d.com dirty.
	sessions, _ := snapshot["session_results"].(map[string]any)
	sessions["a@b.com"] = map[string]any{"session_json": "v2"}
	sessions["c@d.com"] = map[string]any{"session_json": "v2"}
	time.Sleep(10 * time.Millisecond) // so a rewrite is visible in ModTime
	s.Save(snapshot, map[string]bool{"C@D.com": true}, true)

	if got := readJSON(t, pathA)["payload"].(map[string]any)["session_json"]; got != "v1" {
		t.Errorf("a@b.com was rewritten though it was not dirty: %v", got)
	}
	if statA2, _ := os.Stat(pathA); !statA2.ModTime().Equal(statA.ModTime()) {
		t.Error("a@b.com's file was touched though it was not dirty")
	}
	// The dirty key is matched case-insensitively (app.py:2133 `.lower()`).
	if got := readJSON(t, pathC)["payload"].(map[string]any)["session_json"]; got != "v2" {
		t.Errorf("c@d.com was not rewritten: %v", got)
	}

	// Delete a file and save with an empty dirty set: it must come back, or the
	// index points at nothing and the next Load warns 文件缺失.
	if err := os.Remove(pathA); err != nil {
		t.Fatal(err)
	}
	s.Save(snapshot, map[string]bool{}, true)
	if got := readJSON(t, pathA)["payload"].(map[string]any)["session_json"]; got != "v2" {
		t.Errorf("a missing session file was not restored: %v", got)
	}
}

// A non-flush Save must NOT have written by the time it returns, and a later
// flush must win — that is the whole debounce contract (app.py:2001-2019).
func TestSaveDebouncesAndTheFlushWins(t *testing.T) {
	s := newStore(t)
	base := func(v string) map[string]any {
		return map[string]any{
			"updated_at": v,
			"accounts":   []any{account("a@b.com")},
			"session_results": map[string]any{
				"a@b.com": map[string]any{"session_json": v},
			},
		}
	}
	s.Save(base("debounced"), nil, false)
	if _, err := os.Stat(s.StateFile); err == nil {
		t.Fatal("a debounced Save wrote immediately")
	}
	s.Save(base("flushed"), nil, true)
	if got := readJSON(t, s.StateFile)["updated_at"]; got != "flushed" {
		t.Fatalf("updated_at = %v, want the flushed value", got)
	}

	// The debounced write must not land on top of the flushed one afterwards:
	// its version is older, so writeIfCurrent drops it.
	time.Sleep(debounce + 500*time.Millisecond)
	if got := readJSON(t, s.StateFile)["updated_at"]; got != "flushed" {
		t.Fatalf("the stale debounced write landed later: updated_at = %v", got)
	}
}

// A crash mid-write must never leave a truncated state.json, so the write goes
// to a .tmp and is renamed — and the .tmp must not survive a successful write.
func TestWritesAreAtomicAndLeaveNoTempFile(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at":      "2026-07-27",
		"accounts":        []any{account("a@b.com")},
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "tok"}},
	}, nil, true)

	for _, path := range []string{s.StateFile + ".tmp", filepath.Join(s.DataDir, sessionRelPath("a@b.com")) + ".tmp"} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s survived a successful write", path)
		}
	}
	// The written JSON must be valid and un-HTML-escaped (Python's
	// ensure_ascii=False + Go's SetEscapeHTML(false)).
	s.Save(map[string]any{"note": "a<b&c 中文", "session_results": map[string]any{}}, nil, true)
	raw, err := os.ReadFile(s.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "a<b&c 中文") {
		t.Errorf("state.json escaped its content: %s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("state.json does not end in a newline; python writes dumps(...) + \"\\n\"")
	}
}

// Saving a v2 file must not create the legacy backup; loading a v1 file and
// then saving must, exactly once (app.py:2126-2132).
func TestLegacyBackupIsWrittenOnceAndOnlyForALegacyLoad(t *testing.T) {
	backups := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "state.backup-before-state-split-") {
				out = append(out, e.Name())
			}
		}
		return out
	}

	fresh := newStore(t)
	fresh.Save(map[string]any{"session_results": map[string]any{}}, nil, true)
	if got := backups(filepath.Dir(fresh.StateFile)); len(got) != 0 {
		t.Errorf("a v2 save made a legacy backup: %v", got)
	}

	legacy := newStore(t)
	writeJSON(t, legacy.StateFile, map[string]any{
		"schema_version":  1,
		"session_results": map[string]any{"a@b.com": map[string]any{"session_json": "inline"}},
	})
	data, err := legacy.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	legacy.Save(data, nil, true)
	if got := backups(filepath.Dir(legacy.StateFile)); len(got) != 1 {
		t.Fatalf("legacy backups = %v, want exactly one", got)
	}
	legacy.Save(data, nil, true)
	if got := backups(filepath.Dir(legacy.StateFile)); len(got) != 1 {
		t.Errorf("legacy backups after a second save = %v, want still one", got)
	}
	// The migration must have split the inline payload out to its own file.
	if got := readJSON(t, filepath.Join(legacy.DataDir, sessionRelPath("a@b.com"))); got["payload"] == nil {
		t.Errorf("the legacy payload was not split out: %v", got)
	}
}

// A deferred (inactive) session must survive a save it was not part of. If its
// index entry were dropped, the file would be orphaned and the session lost the
// moment the user re-adds the account.
func TestSaveKeepsDeferredIndexEntries(t *testing.T) {
	s := seed(t, []any{account("keep@b.com")}, map[string]map[string]any{
		"keep@b.com": {"session_json": "kept"},
		"gone@b.com": {"session_json": "deferred"},
	})
	data, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Save(data, nil, true)

	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	if _, ok := index["gone@b.com"]; !ok {
		t.Fatalf("the deferred entry was dropped from the index: %v", index)
	}
	if _, ok := index["keep@b.com"]; !ok {
		t.Fatalf("the live entry is missing: %v", index)
	}
	// And it is still readable afterwards.
	again := New(s.StateFile, s.DataDir)
	if _, err := again.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := again.LoadDeferredSession("gone@b.com"); got == nil || got["session_json"] != "deferred" {
		t.Errorf("deferred session after a save = %v", got)
	}
}

// A blank address, and a payload that is not an object, are both skipped rather
// than written under a garbage file name (app.py:2130-2131).
func TestSaveSkipsBlankKeysAndNonObjectPayloads(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at": "2026-07-27",
		"session_results": map[string]any{
			"":         map[string]any{"session_json": "blank"},
			"   ":      map[string]any{"session_json": "spaces"},
			"a@b.com":  "not an object",
			"ok@b.com": map[string]any{"session_json": "fine"},
		},
	}, nil, true)

	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	if len(index) != 1 {
		t.Fatalf("index = %v, want only ok@b.com", index)
	}
	if _, ok := index["ok@b.com"]; !ok {
		t.Errorf("index = %v", index)
	}
}

// The address is trimmed before it becomes a key, so "  a@b.com  " and
// "a@b.com" are the same session and cannot end up as two index entries
// pointing at one file.
func TestSaveTrimsTheIndexKey(t *testing.T) {
	s := newStore(t)
	s.Save(map[string]any{
		"updated_at":      "2026-07-27",
		"session_results": map[string]any{"  a@b.com  ": map[string]any{"session_json": "tok"}},
	}, nil, true)
	index, _ := readJSON(t, s.StateFile)["session_results"].(map[string]any)
	if _, ok := index["a@b.com"]; !ok {
		t.Errorf("index = %v, want the trimmed key", index)
	}
}
