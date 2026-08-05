// Package state is a faithful Go port of the Python StateStore: debounced,
// atomic JSON persistence with per-account session files split out under
// state_data/sessions/<sha256[:24]>.json.
//
// The snapshot is a generic map[string]any (like the Python dict) so it stays
// byte-compatible with the existing state.json and higher layers own the typed
// conversion (see internal/models).
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	SchemaVersion          = 2
	debounce               = 1500 * time.Millisecond
	sessionLoadConcurrency = 8
)

type pendingWrite struct {
	version  int64
	snapshot map[string]any
	dirty    map[string]bool // nil => all sessions dirty
	dirtyAll bool
}

type Store struct {
	StateFile  string
	DataDir    string
	SessionDir string

	LoadedLegacy        bool
	MissingSessionFiles bool
	Warnings            []string

	pendingMu            sync.Mutex
	writeMu              sync.Mutex
	pending              *pendingWrite
	saving               bool
	version              int64
	latestWritten        int64
	legacyBackupDone     bool
	deferredSessionIndex map[string]map[string]any
}

// New builds a Store. dataDir defaults to <dir(stateFile)>/state_data.
func New(stateFile, dataDir string) *Store {
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(stateFile), "state_data")
	}
	return &Store{
		StateFile:            stateFile,
		DataDir:              dataDir,
		SessionDir:           filepath.Join(dataDir, "sessions"),
		deferredSessionIndex: map[string]map[string]any{},
	}
}

func (s *Store) Load() (map[string]any, error) {
	s.LoadedLegacy = false
	s.MissingSessionFiles = false
	s.Warnings = nil

	raw, err := os.ReadFile(s.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	// KNOWN GAP, measured and deliberately not closed here: this decodes numbers
	// as float64, so a JSON number does not survive the round trip verbatim.
	// json.Decoder.UseNumber() would fix it — json.Number keeps the literal text
	// and re-encodes it byte for byte.
	//
	// It is not switched on because the type escapes this package: every
	// `case float64:` in accounts / export / logs / models / settings /
	// sessionconv reads values that came out of here, and each would have to
	// grow a json.Number branch on the same commit or start silently taking its
	// default. Against THIS user's real state.json the whole observable
	// difference is that the twelve `"device_scale_factor": 1.0` entries are
	// rewritten as `1` — Python re-reads that as int 1 and both sides then
	// float() it, so nothing downstream can tell. What it would cost is an
	// integer above 2^53 (none present) or a float whose shortest Go rendering
	// differs from Python's repr (none present: 0.4972375690607735 and the other
	// ratios round-trip exactly).
	//
	// Revisit as one atomic change across those packages, not piecemeal.
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}

	// app.py:1985 is `int(data.get("schema_version") or 1)`, and int() RAISES on
	// a string it cannot parse — so a corrupt schema_version makes load() fail
	// and the Tk app reads nothing. Refusing is the safe answer and Go must copy
	// it: guessing 2 for a legacy file would send loadSplitSessions a payload map
	// where it expects an index, it would return {}, and the next save would
	// write that emptiness over every session the user has.
	version, err := pyIntOr(data["schema_version"], 1)
	if err != nil {
		return nil, fmt.Errorf("state.json 的 schema_version 无法解析: %w", err)
	}
	if version >= SchemaVersion {
		active := activeEmailKeys(data["accounts"])
		data["session_results"] = s.loadSplitSessions(data["session_results"], active)
		return data, nil
	}

	s.LoadedLegacy = true
	if _, ok := data["session_results"].(map[string]any); !ok {
		data["session_results"] = map[string]any{}
	}
	return data, nil
}

// Save writes the snapshot. If flush is true it writes synchronously; otherwise
// it debounces. dirtySessionEmails==nil means "all sessions dirty".
func (s *Store) Save(snapshot map[string]any, dirtySessionEmails map[string]bool, flush bool) {
	s.pendingMu.Lock()
	s.version++
	version := s.version

	if flush {
		s.pending = nil
		s.pendingMu.Unlock()
		s.writeIfCurrent(version, snapshot, dirtySessionEmails, dirtySessionEmails == nil)
		return
	}

	dirtyAll := dirtySessionEmails == nil
	pendingDirty := dirtySessionEmails
	if s.pending != nil {
		if s.pending.dirtyAll || dirtySessionEmails == nil {
			dirtyAll = true
			pendingDirty = nil
		} else {
			merged := map[string]bool{}
			for k := range s.pending.dirty {
				merged[k] = true
			}
			for k := range dirtySessionEmails {
				merged[k] = true
			}
			pendingDirty = merged
		}
	}
	s.pending = &pendingWrite{version: version, snapshot: snapshot, dirty: pendingDirty, dirtyAll: dirtyAll}
	if !s.saving {
		s.saving = true
		go s.saveWorker()
	}
	s.pendingMu.Unlock()
}

func (s *Store) saveWorker() {
	for {
		time.Sleep(debounce)
		s.pendingMu.Lock()
		pending := s.pending
		s.pending = nil
		if pending == nil {
			s.saving = false
			s.pendingMu.Unlock()
			return
		}
		s.pendingMu.Unlock()

		s.writeIfCurrent(pending.version, pending.snapshot, pending.dirty, pending.dirtyAll)
	}
}

func (s *Store) writeIfCurrent(version int64, snapshot map[string]any, dirty map[string]bool, dirtyAll bool) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if version < s.latestWritten {
		return
	}
	if err := s.writeSnapshot(snapshot, dirty, dirtyAll); err != nil {
		s.Warnings = append(s.Warnings, "写入状态失败: "+err.Error())
		return
	}
	s.latestWritten = version
}

func (s *Store) loadSplitSessions(rawIndex any, activeKeys map[string]bool) map[string]any {
	index, ok := rawIndex.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	s.deferredSessionIndex = map[string]map[string]any{}
	sessions := map[string]any{}

	type task struct{ emailKey, path string }
	var tasks []task
	dataDirAbs, _ := filepath.Abs(s.DataDir)

	// app.py:2064 guards on `if active_email_keys and ...`, and an EMPTY SET IS
	// FALSY — so "no active accounts" means "defer nothing", not "defer
	// everything". Go's activeEmailKeys returns a non-nil empty map for a
	// state.json whose `accounts` key is missing, null, or not a list, and a
	// `!= nil` test would then defer EVERY session: the UI would come up with an
	// empty session_results, and the merge in persist.go would treat that as
	// "this account has no prior session" and drop fields on the next write.
	filterActive := len(activeKeys) > 0

	// Sorted so the WARNING ORDER is deterministic. Python's ThreadPoolExecutor
	// .map yields results in INPUT order, and its input is dict-insertion order;
	// Go's map range is random and the loads finish out of order, so without
	// this the same file produces a different warning list every run.
	for _, emailAddr := range sortedKeys(index) {
		item := index[emailAddr]
		emailKey := strings.TrimSpace(emailAddr)
		if emailKey == "" {
			continue
		}
		relPath := ""
		if m, ok := item.(map[string]any); ok {
			relPath = strings.TrimSpace(asStr(m["session_file"]))
		}
		if relPath == "" {
			relPath = sessionRelPath(emailKey)
		}
		if filterActive && !activeKeys[strings.ToLower(emailKey)] {
			s.deferredSessionIndex[emailKey] = map[string]any{"session_file": relPath}
			continue
		}
		sessionPath := filepath.Join(s.DataDir, relPath)
		abs, _ := filepath.Abs(sessionPath)
		if !within(dataDirAbs, abs) {
			s.Warnings = append(s.Warnings, "Session 文件路径越界，已跳过: "+emailKey)
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			s.MissingSessionFiles = true
			s.Warnings = append(s.Warnings, "Session 文件缺失，已跳过: "+emailKey)
			continue
		}
		tasks = append(tasks, task{emailKey, abs})
	}

	// One result slot per task, filled in place, so the collection below runs in
	// TASK order however the goroutines finish — see the sort above.
	type result struct {
		payload map[string]any
		err     error
	}
	results := make([]result, len(tasks))
	sem := make(chan struct{}, sessionLoadConcurrency)
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t task) {
			defer wg.Done()
			defer func() { <-sem }()
			payload, err := readSessionPayload(t.path)
			results[i] = result{payload, err}
		}(i, t)
	}
	wg.Wait()
	for i, t := range tasks {
		if results[i].err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("Session 文件读取失败，已跳过 %s: %v", t.emailKey, results[i].err))
			continue
		}
		sessions[t.emailKey] = results[i].payload
	}
	return sessions
}

// LoadDeferredSession lazily loads a session skipped during Load (inactive account).
func (s *Store) LoadDeferredSession(emailAddr string) map[string]any {
	target := strings.ToLower(strings.TrimSpace(emailAddr))
	var emailKey string
	for k := range s.deferredSessionIndex {
		if strings.ToLower(k) == target {
			emailKey = k
			break
		}
	}
	if emailKey == "" {
		return nil
	}
	item := s.deferredSessionIndex[emailKey]
	relPath := strings.TrimSpace(asStr(item["session_file"]))
	if relPath == "" {
		relPath = sessionRelPath(emailKey)
	}
	abs, _ := filepath.Abs(filepath.Join(s.DataDir, relPath))
	dataDirAbs, _ := filepath.Abs(s.DataDir)
	if !within(dataDirAbs, abs) {
		return nil
	}
	payload, err := readSessionPayload(abs)
	if err != nil {
		return nil
	}
	delete(s.deferredSessionIndex, emailKey)
	return payload
}

func (s *Store) writeSnapshot(snapshot map[string]any, dirty map[string]bool, dirtyAll bool) error {
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.SessionDir, 0o755); err != nil {
		return err
	}

	if s.LoadedLegacy && !s.legacyBackupDone {
		if _, err := os.Stat(s.StateFile); err == nil {
			backup := filepath.Join(filepath.Dir(s.StateFile),
				"state.backup-before-state-split-"+time.Now().Format("20060102-150405")+".json")
			if copyFile(s.StateFile, backup) == nil {
				s.legacyBackupDone = true
			}
		}
	}

	// shallow copy, pull out session_results
	data := make(map[string]any, len(snapshot))
	for k, v := range snapshot {
		data[k] = v
	}
	sessions, _ := data["session_results"].(map[string]any)
	delete(data, "session_results")
	// app.py:2139/2143 read `data.get("updated_at", "")` and write it into the
	// index and the session file WITHOUT str() — so a numeric updated_at stays a
	// JSON number on both sides. asStr here would quietly change its type in a
	// file the Python app also reads.
	updatedAt := any("")
	if v, ok := data["updated_at"]; ok {
		updatedAt = v
	}

	dirtyKeys := map[string]bool{}
	for k := range dirty {
		if kk := strings.ToLower(strings.TrimSpace(k)); kk != "" {
			dirtyKeys[kk] = true
		}
	}

	sessionIndex := map[string]any{}
	for k, v := range s.deferredSessionIndex {
		sessionIndex[k] = v
	}

	for emailAddr, payloadAny := range sessions {
		emailKey := strings.TrimSpace(emailAddr)
		payload, ok := payloadAny.(map[string]any)
		if emailKey == "" || !ok {
			continue
		}
		relPath := sessionRelPath(emailKey)
		sessionPath := filepath.Join(s.DataDir, relPath)
		// All three are `str(payload.get(k) or "")` (app.py:2136-2138), NOT
		// str(payload[k]). The `or` runs FIRST on the raw value, so a JSON false
		// or 0 collapses to "" — where asStr would produce "false" / "0" and
		// flip has_session_json to true for an account that has none. The 有
		// Session column and the export gates read these flags.
		sessionIndex[emailKey] = map[string]any{
			"session_file":           relPath,
			"has_session_json":       pyStrOrEmpty(payload["session_json"]) != "",
			"has_storage_state_json": pyStrOrEmpty(payload["storage_state_json"]) != "",
			"payment_link_type":      pyStrOrEmpty(payload["payment_link_type"]),
			"updated_at":             updatedAt,
		}
		_, statErr := os.Stat(sessionPath)
		if dirtyAll || dirtyKeys[strings.ToLower(emailKey)] || statErr != nil {
			if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
				return err
			}
			if err := writeJSONAtomic(sessionPath, map[string]any{
				"email": emailKey, "updated_at": updatedAt, "payload": payload,
			}); err != nil {
				return err
			}
		}
	}

	data["schema_version"] = SchemaVersion
	data["session_results"] = sessionIndex
	return writeJSONAtomic(s.StateFile, data)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func sessionRelPath(emailAddr string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(emailAddr))))
	return "sessions/" + hex.EncodeToString(sum[:])[:24] + ".json"
}

func readSessionPayload(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if inner, ok := payload["payload"].(map[string]any); ok {
		return inner, nil
	}
	if payload == nil {
		return nil, errors.New("Session 文件内容不是对象")
	}
	return payload, nil
}

func activeEmailKeys(accounts any) map[string]bool {
	arr, ok := accounts.([]any)
	if !ok {
		return map[string]bool{}
	}
	keys := map[string]bool{}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if e := strings.TrimSpace(asStr(m["email"])); e != "" {
			keys[strings.ToLower(e)] = true
		}
	}
	return keys
}

func within(dirAbs, pathAbs string) bool {
	rel, err := filepath.Rel(dirAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeJSONAtomic(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil { // Encode appends a trailing newline
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// sortedKeys is used wherever Python iterates a dict and the ORDER reaches the
// user (a warning list, a file written in sequence). Python's order is dict
// insertion order, which a JSON round-trip through Go's map has already thrown
// away; sorting at least makes the same input produce the same output twice.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pyTruthy is Python's bool(v) for a json.loads value: "" / 0 / 0.0 / false /
// nil / [] / {} are falsy, everything else is truthy.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		return len(t) != 0
	case map[string]any:
		return len(t) != 0
	default:
		return true
	}
}

// pyStrOrEmpty is Python's `str(v or "")`. The `or` runs on the RAW value, so
// this is not the same as stringifying and then testing for "": str(False) is
// "False", but `False or ""` is "".
func pyStrOrEmpty(v any) string {
	if !pyTruthy(v) {
		return ""
	}
	return asStr(v)
}

// pyIntOr is Python's `int(v or def)`, including the ValueError that a
// malformed string raises — see the comment at its call site for why a silent
// fallback is the wrong answer there.
//
// Python's int(str) grammar is wider than strconv.Atoi's: surrounding
// whitespace is allowed, single underscores may separate digits ("3_0" is 30),
// and the digits may be any Unicode decimal (int("٢") is 2). It is also
// NARROWER in the place that matters: "2abc" and "0x10" raise, where
// fmt.Sscanf("%d") would happily return 2 and 0.
func pyIntOr(v any, def int) (int, error) {
	if !pyTruthy(v) {
		return def, nil
	}
	switch t := v.(type) {
	case bool: // truthy, so true; int(True) == 1
		return 1, nil
	case float64:
		return int(t), nil // int() truncates toward zero, as Go's conversion does
	case int:
		return t, nil
	case string:
		return pyIntString(t)
	}
	return 0, fmt.Errorf("int() argument must be a string or a number, not %T", v)
}

// pyIntString is int(s) for a str: [ws] [+|-] digit (['_'] digit)* [ws].
func pyIntString(s string) (int, error) {
	runes := []rune(strings.Trim(s, pyStripCutset))
	i := 0
	neg := false
	if i < len(runes) && (runes[i] == '+' || runes[i] == '-') {
		neg = runes[i] == '-'
		i++
	}
	n, digits := 0, 0
	prevUnderscore := false
	for ; i < len(runes); i++ {
		if runes[i] == '_' {
			// An underscore must sit BETWEEN digits: "_1", "1_" and "1__2" all raise.
			if digits == 0 || prevUnderscore {
				return 0, invalidInt(s)
			}
			prevUnderscore = true
			continue
		}
		v, ok := pyDigitValue(runes[i])
		if !ok {
			return 0, invalidInt(s)
		}
		prevUnderscore = false
		digits++
		n = n*10 + v
		if n > 1<<30 { // a schema version; anything this large is already nonsense
			return 0, invalidInt(s)
		}
	}
	if digits == 0 || prevUnderscore {
		return 0, invalidInt(s)
	}
	if neg {
		n = -n
	}
	return n, nil
}

func invalidInt(s string) error {
	return fmt.Errorf("invalid literal for int() with base 10: %q", s)
}

// pyDigitValue is int(ch) for one character: any Unicode decimal digit (Nd).
// Nd is laid out as complete, aligned runs of ten, so the offset from the start
// of the containing range modulo ten is the value.
func pyDigitValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if r <= unicode.MaxLatin1 {
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int((r - rune(rg.Lo)) % 10), true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) && rg.Stride == 1 {
			return int((r - rune(rg.Lo)) % 10), true
		}
	}
	return 0, false
}

// pyStripCutset is exactly the 29 code points Python's str.strip() removes;
// strings.TrimSpace covers 25 and omits U+001C-U+001F.
const pyStripCutset = "\t\n\v\f\r\u001C\u001D\u001E\u001F\u0020\u0085\u00A0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200A\u2028\u2029\u202F\u205F\u3000"
