// Package ui holds the Wails-bound application object: the boundary between the
// webview frontend and the already-ported Go backend (internal/state,
// internal/worker, internal/mail, ...).
//
// Everything exported on App becomes callable from TypeScript, so the method set
// here IS the UI's API. Keep it small and explicit; do not expose internal
// structs whose shape we would then be unable to change.
package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	applogs "github.com/pkppkq/openai-register-go/internal/logs"
	"github.com/pkppkq/openai-register-go/internal/phoneprovider"
	"github.com/pkppkq/openai-register-go/internal/providerproxy"
	"github.com/pkppkq/openai-register-go/internal/state"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// EventLog is the Wails event name every backend log line is emitted on. The Tk
// app pumped log lines through a queue drained by root.after; the webview
// equivalent is an event stream, so nothing blocks the UI thread.
const EventLog = "log"

// EventLogRecord 携带已完成路由、模块分类和严重级别标记的结构化日志。
const EventLogRecord = "log-record"

// App is the object bound into the frontend.
type App struct {
	ctx context.Context

	mu        sync.Mutex
	store     *state.Store
	stateFile string
	dataDir   string

	jobs *jobRegistry
	logs *applogs.Store

	// phonePool 是所有注册任务共享的手工号码池。每个任务仍拥有独立的
	// SMSBower 租号生命周期，但手工号码的占用、次数和授权账号绑定必须在
	// App 范围内串行，否则批量任务会从各自快照重复拿到同一个号码。
	phonePoolOnce sync.Once
	phonePool     *phoneprovider.MemoryPool

	providerCtx     context.Context
	providerCancel  context.CancelFunc
	providerManager *providerproxy.Manager

	// paymentWindows 保存仍在运行的支付窗口代理链，供“切换支付代理”
	// 原地替换动态上游。与 state 写锁分离，避免窗口监控期间阻塞普通设置写入。
	paymentWindowsMu sync.Mutex
	paymentWindows   map[string]*activePaymentWindow

	// dynamicProxyIndex is _next_dynamic_proxy's cursor (app.py:17600-17607).
	//
	// Deliberately NOT the persisted pool rotation, and deliberately not reset
	// when the pool is edited: Python keeps this as a plain instance attribute,
	// takes `dynamic_proxies[index % len(dynamic_proxies)]` against whatever the
	// pool holds right now, and only ever increments. So it survives an edit, it
	// restarts from the head when the app restarts, and it never writes anything.
	// 重新获取 is its only reader.
	dynamicProxyIndex atomic.Uint64

	// logSink diverts Log away from the event bus. Nil in production; tests set
	// it because several ported behaviours ARE their log output — app.py:24164
	// logs a Session export's skip note BEFORE the export line while the two
	// conversion exports log theirs after, and that ordering is only observable
	// here.
	logSink func(string)
}

// New 构造 Wails 后端。默认状态写入当前用户的配置目录，避免发布后的 EXE
// 依赖开发机盘符；STATE_FILE / STATE_DATA_DIR 仍可用于迁移旧数据和隔离测试。
func New() *App {
	stateFile := os.Getenv("STATE_FILE")
	if stateFile == "" {
		stateFile = filepath.Join(defaultStorageRoot(), "state.json")
	}
	dataDir := os.Getenv("STATE_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(stateFile), "state_data")
	}
	app := &App{
		stateFile:      stateFile,
		dataDir:        dataDir,
		store:          state.New(stateFile, dataDir),
		jobs:           newJobRegistry(),
		logs:           applogs.NewStore(),
		paymentWindows: map[string]*activePaymentWindow{},
	}
	app.initProviderManager()
	return app
}

// defaultStorageRoot 返回可写、与源码位置无关的用户数据目录。
// OPENAI_REGISTER_HOME 主要用于便携部署和测试；Windows 默认落在
// %APPDATA%\OpenAIRegister。
func defaultStorageRoot() string {
	if root := os.Getenv("OPENAI_REGISTER_HOME"); root != "" {
		return root
	}
	if root, err := os.UserConfigDir(); err == nil && root != "" {
		return filepath.Join(root, "OpenAIRegister")
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), "data")
	}
	return filepath.Join(".", "OpenAIRegister")
}

// Startup is wired to Wails' OnStartup; it captures the context the runtime
// helpers (events, dialogs, window control) need.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.repairState()
}

// Shutdown 在 Wails 窗口退出时停止任务，并统一回收跨任务保留的浏览器。
func (a *App) Shutdown(context.Context) {
	_ = a.StopAll()
	if a.providerCancel != nil {
		a.providerCancel()
	}
	if a.providerManager != nil {
		a.providerManager.Stop()
	}
	worker.CloseAllParkedBrowsers()
}

// repairState is app.py:14216-14222, the one write the Tk app performs at startup
// without the user asking.
//
// Two conditions trigger it, and Store reports both from Load:
//
//   - LoadedLegacy: an old monolithic state.json, which must be rewritten as the
//     lightweight index plus split per-account session files;
//   - MissingSessionFiles: a session index entry whose file under
//     state_data/sessions/ is gone. Load already dropped it from memory, so without
//     the rewrite the stale entry sits in the index warning about itself on every
//     single start.
//
// Both rewrite EVERY session file — that is what Python's _dirty_all_sessions means,
// and why Store.Save reads a nil dirty set that way. This is the only place where
// passing nil is correct; everywhere else it would needlessly rewrite all of them.
func (a *App) repairState() {
	a.mu.Lock()
	defer a.mu.Unlock()

	snapshot, err := a.store.Load()
	if err != nil {
		a.Log("读取 state.json 失败: " + err.Error())
		return
	}
	for _, warning := range a.store.Warnings {
		a.Log(warning)
	}
	// Python reaches this through save_state() -> _build_state_snapshot(), which
	// stamps updated_at first (app.py:14227). Saving the snapshot exactly as loaded
	// would stamp Python's OLD timestamp into the index entry and into every one of
	// the ~150 rewritten session files.
	snapshot["updated_at"] = time.Now().Format(stateTimeFormat)
	switch {
	case a.store.LoadedLegacy:
		a.store.Save(snapshot, nil, true)
		a.Log("检测到旧版 state.json，已排队迁移为轻量索引 + 拆分 Session 文件")
	case a.store.MissingSessionFiles:
		// Python repairs this silently; say so instead, because it rewrites the
		// user's whole session directory.
		a.store.Save(snapshot, nil, true)
		a.Log("检测到 Session 文件缺失，已重写索引与全部 Session 文件")
	}
}

// OpenAccountFile is 从文件导入 (app.py:14348): pick a text file and return its
// contents for the import textarea. It deliberately returns the TEXT rather than a
// path, because the frontend's job is to show it in the box for review — Python
// does not import on open either, it only fills the widget.
//
// An empty return means the user cancelled, which is not an error.
func (a *App) OpenAccountFile() (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("窗口尚未就绪")
	}
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择邮箱文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Text", Pattern: "*.txt"},
			{DisplayName: "All", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	if path == "" {
		return "", nil
	}
	// Python reads with encoding="utf-8" and lets a decode error surface.
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("读取 %s 失败: 文件不是 UTF-8 编码", path)
	}
	return string(raw), nil
}

// Log 保存并发送一行日志。启动前仍会进入环形缓冲区，只是不发送 Wails 事件。
func (a *App) Log(line string) {
	a.log(line, "")
}

// log 是带显式账户路由的内部日志入口。原始文本仍按原样发送到旧 log 事件；
// 只有结构化记录使用 email 路由，因此不会为了修复账户日志而改写用户可见文本。
func (a *App) log(line, email string) {
	if a.logSink != nil {
		a.logSink(line)
		return
	}
	record := a.logs.Append(line, email)
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, EventLog, line)
	wailsruntime.EventsEmit(a.ctx, EventLogRecord, record)
}

// LogSnapshot 是日志页一次读取所需的两个视图。
type LogSnapshot struct {
	AccountTitle string           `json:"accountTitle"`
	Account      []applogs.Record `json:"account"`
	Global       []applogs.Record `json:"global"`
}

// SelectLogAccount 设置日志页当前选中的账号，并返回对应环形缓冲区。
func (a *App) SelectLogAccount(email string) LogSnapshot {
	a.logs.SetSelected(email)
	display := ""
	if email != "" {
		snapshot, err := a.snapshot()
		if err == nil {
			accountEmails := make([]string, 0)
			for _, account := range accountsFromSnapshot(snapshot) {
				accountEmails = append(accountEmails, account.Email)
			}
			display = applogs.SelectedEmailText(applogs.EmailKey(email), accountEmails)
		}
	}
	return LogSnapshot{
		AccountTitle: applogs.AccountPaneTitle(display),
		Account:      a.logs.AccountRecords(email),
		Global:       a.logs.GlobalRecords(),
	}
}

// LoadLogs 返回当前账号与全局日志，不改变当前选择。
func (a *App) LoadLogs() LogSnapshot {
	key := a.logs.Selected()
	display := ""
	if key != "" {
		snapshot, err := a.snapshot()
		if err == nil {
			accountEmails := make([]string, 0)
			for _, account := range accountsFromSnapshot(snapshot) {
				accountEmails = append(accountEmails, account.Email)
			}
			display = applogs.SelectedEmailText(key, accountEmails)
		}
	}
	return LogSnapshot{
		AccountTitle: applogs.AccountPaneTitle(display),
		Account:      a.logs.AccountRecords(key),
		Global:       a.logs.GlobalRecords(),
	}
}

// Env describes the running build. The frontend shows this in the title bar so a
// mis-targeted state file is obvious at a glance rather than after a bad run.
type Env struct {
	GoVersion string `json:"goVersion"`
	StateFile string `json:"stateFile"`
	DataDir   string `json:"dataDir"`
	StateOK   bool   `json:"stateOK"`
}

// Environment returns build/runtime info and whether the state file is readable.
func (a *App) Environment() Env {
	_, err := os.Stat(a.stateFile)
	return Env{
		GoVersion: runtime.Version(),
		StateFile: a.stateFile,
		DataDir:   a.dataDir,
		StateOK:   err == nil,
	}
}

// StateSummary is a cheap, read-only overview used to prove the backend wiring
// end-to-end before any of the real screens exist.
type StateSummary struct {
	Accounts     int      `json:"accounts"`
	Sessions     int      `json:"sessions"`
	SettingsKeys []string `json:"settingsKeys"`
	SchemaFile   string   `json:"schemaFile"`
}

// LoadSummary reads the real state.json through the ported store.
func (a *App) LoadSummary() (StateSummary, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	snapshot, err := a.store.Load()
	if err != nil {
		return StateSummary{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	out := StateSummary{SchemaFile: filepath.Base(a.stateFile), SettingsKeys: []string{}}
	if accounts, ok := snapshot["accounts"].([]any); ok {
		out.Accounts = len(accounts)
	}
	// NOTE: the key is "session_results", not "sessions" — Store.Load rebuilds it
	// from the split per-account files under state_data/sessions/ (state.go:91).
	// Reading "sessions" silently yields 0, which is indistinguishable from an
	// account set that genuinely has none.
	if sessions, ok := snapshot["session_results"].(map[string]any); ok {
		out.Sessions = len(sessions)
	}
	if settings, ok := snapshot["settings"].(map[string]any); ok {
		for k := range settings {
			out.SettingsKeys = append(out.SettingsKeys, k)
		}
		// Go randomises map iteration, so without this the same unchanged
		// state.json returns a differently-ordered list on every call — which the
		// frontend cannot diff, and which makes any test over this field flaky.
		sort.Strings(out.SettingsKeys)
	}
	return out, nil
}
