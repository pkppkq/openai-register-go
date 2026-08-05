package ui

// export_test.go covers the BOUNDARY in export.go, not internal/export — the
// byte-for-byte export content already has 626 lines of tests next door. What is
// only testable here is what the boundary can get wrong:
//
//   - resolving a frontend selection against state.json (an unknown address must
//     be an error, a repeated one must not export twice, and order must be the
//     order the frontend sent);
//   - handing the WRITE the Document's File and not its Text, which on Windows
//     is a different byte count and is what makes the produced file identical to
//     the Tk app's;
//   - the log lines and skip notes, whose wording and ORDER are per-button;
//   - the two gates (ExportMissingRT, PrepareSub2APIExport) that exist so a
//     button can validate a selection without starting a job.
//
// Nothing here touches a dialog: saveExportDialog needs a live window, so the
// tests drive writeExport directly with a path in t.TempDir().

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/export"
)

// rtAccountMap is accountMap plus an openai_rt, the field every "已授权" export
// filters on.
func rtAccountMap(email, rt string) map[string]any {
	m := accountMap(email, "plus", "", "未分组")
	m["openai_rt"] = rt
	return m
}

func exportApp(t *testing.T) *App {
	t.Helper()
	return newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts": []any{
			rtAccountMap("a@example.com", "rt_aaa"),
			rtAccountMap("b@example.com", "rt_bbb"),
			accountMap("c@example.com", "free", "", "未分组"), // no openai_rt
		},
	})
}

func TestSelectedAccountsKeepsFrontendOrder(t *testing.T) {
	app := exportApp(t)
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got, err := selectedAccounts(snapshot, []string{"c@example.com", "a@example.com"})
	if err != nil {
		t.Fatalf("selectedAccounts: %v", err)
	}
	if len(got) != 2 || got[0].Email != "c@example.com" || got[1].Email != "a@example.com" {
		t.Fatalf("selection order not preserved: %v", accountEmails(got))
	}
}

// A Treeview selection is a SET, so Python can never hand the same account to an
// export twice; a webview can send the same key twice, and writing the RT line
// out twice would be silent data corruption in the exported file.
func TestSelectedAccountsCollapsesARepeatedAddress(t *testing.T) {
	app := exportApp(t)
	snapshot, _ := app.snapshot()
	got, err := selectedAccounts(snapshot, []string{"a@example.com", "A@Example.com", "a@example.com"})
	if err != nil {
		t.Fatalf("selectedAccounts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("repeated address exported %d times: %v", len(got), accountEmails(got))
	}
}

// Exporting 4 of the 5 mailboxes the user selected, with nothing saying which
// one vanished, is exactly the quiet loss Python cannot produce.
func TestSelectedAccountsRefusesAnUnknownAddress(t *testing.T) {
	app := exportApp(t)
	snapshot, _ := app.snapshot()
	if _, err := selectedAccounts(snapshot, []string{"a@example.com", "ghost@example.com"}); err == nil {
		t.Fatal("an unknown address was silently dropped")
	}
	if _, err := selectedAccounts(snapshot, nil); !errors.Is(err, export.ErrNoSelection) {
		t.Fatalf("empty selection: err = %v, want ErrNoSelection", err)
	}
}

// Every typed error from internal/export must reach the frontend UNWRAPPED, so
// err.Error() is the verbatim messagebox body app.py shows.
func TestExportPlanPassesTypedErrorsThrough(t *testing.T) {
	app := exportApp(t)
	tests := []struct {
		kind ExportKind
		sel  []string
		want error
	}{
		{ExportAuthorized, []string{"c@example.com"}, export.ErrNoAuthorizedRT},
		{ExportEmailRT, []string{"c@example.com"}, export.ErrNoAuthorizedRT},
		{ExportSessions, []string{"a@example.com"}, export.ErrNoSessionJSON},
		{ExportConversion, []string{"a@example.com"}, export.ErrNoConvertibleToken},
		{ExportConversionZIP, []string{"a@example.com"}, export.ErrNoConvertibleToken},
		{ExportRaw, nil, export.ErrNoSelection},
	}
	for _, tt := range tests {
		if _, err := app.exportPlan(tt.kind, tt.sel); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.kind, err, tt.want)
		}
	}
	if _, err := app.exportPlan("nope", []string{"a@example.com"}); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

func TestPreviewExportRaw(t *testing.T) {
	app := exportApp(t)
	preview, err := app.PreviewExport(string(ExportRaw), []string{"a@example.com", "c@example.com"})
	if err != nil {
		t.Fatalf("PreviewExport: %v", err)
	}
	if preview.Title != "导出选中Raw" {
		t.Errorf("Title = %q", preview.Title)
	}
	if preview.Count != 2 {
		t.Errorf("Count = %d, want 2 (Raw exports every selected account)", preview.Count)
	}
	if !strings.HasPrefix(preview.Text, "a@example.com----pw----cid----rt") {
		t.Errorf("Text = %q", preview.Text)
	}
	// exportStrings, so the JSON carries [] rather than null.
	if preview.Skipped == nil || preview.Entries == nil {
		t.Errorf("nil slice reached the wire: %+v", preview)
	}
}

// 已授权 is Raw filtered to accounts that hold an RT; the unauthorized one has to
// disappear from the document AND from the count.
func TestPreviewExportAuthorizedDropsAccountsWithoutRT(t *testing.T) {
	app := exportApp(t)
	preview, err := app.PreviewExport(string(ExportAuthorized), []string{"a@example.com", "c@example.com"})
	if err != nil {
		t.Fatalf("PreviewExport: %v", err)
	}
	if preview.Count != 1 {
		t.Errorf("Count = %d, want 1", preview.Count)
	}
	if strings.Contains(preview.Text, "c@example.com") {
		t.Errorf("an account with no RT reached the 已授权 export: %q", preview.Text)
	}
}

func TestPreviewExportEmailRT(t *testing.T) {
	app := exportApp(t)
	preview, err := app.PreviewExport(string(ExportEmailRT), []string{"b@example.com", "a@example.com"})
	if err != nil {
		t.Fatalf("PreviewExport: %v", err)
	}
	if preview.Text != "b@example.com----rt_bbb\na@example.com----rt_aaa\n" {
		t.Errorf("Text = %q", preview.Text)
	}
}

// The bytes on disk are Document.File, never Document.Text: Python writes with
// Path.write_text(), which translates "\n" to os.linesep, so on Windows the Tk
// app's exports are CRLF. Writing Text would make every file this app produces
// differ from the Python one for the same accounts.
func TestWriteExportWritesTheOSNewlineForm(t *testing.T) {
	app := exportApp(t)
	plan, err := app.exportPlan(ExportEmailRT, []string{"a@example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("exportPlan: %v", err)
	}
	path := filepath.Join(t.TempDir(), "rt.txt")
	res, err := app.writeExport(plan, path)
	if err != nil {
		t.Fatalf("writeExport: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if res.Bytes != len(raw) {
		t.Errorf("Bytes = %d, file holds %d", res.Bytes, len(raw))
	}
	if res.Cancelled {
		t.Error("a completed write reported Cancelled")
	}
	if res.Message != "已导出 2 个邮箱----RT TXT: "+path {
		t.Errorf("Message = %q", res.Message)
	}
	wantCRLF := runtime.GOOS == "windows"
	if got := strings.Contains(string(raw), "\r\n"); got != wantCRLF {
		t.Errorf("file has CRLF = %v, want %v on %s", got, wantCRLF, runtime.GOOS)
	}
	if wantCRLF && res.Bytes == len(plan.text) {
		t.Errorf("Text was written instead of File: %d bytes either way", res.Bytes)
	}
	// The text form the preview showed must be unchanged by the write.
	if strings.Contains(plan.text, "\r") {
		t.Errorf("plan.text is not LF: %q", plan.text)
	}
}

// app.py:24164 logs the Session skip line BEFORE the export line; the two
// conversion exports log theirs after (24375, 24404). Getting the order wrong
// makes the log read as though the skip belonged to the previous action.
func TestWriteExportLogsTheSkipNoteOnTheRightSide(t *testing.T) {
	app := exportApp(t)
	seedSession(t, app, "a@example.com", map[string]any{"session_json": `{"accessToken":"tok"}`})

	var lines []string
	app.logSink = func(line string) { lines = append(lines, line) }

	plan, err := app.exportPlan(ExportSessions, []string{"a@example.com", "c@example.com"})
	if err != nil {
		t.Fatalf("exportPlan: %v", err)
	}
	if len(plan.skipped) != 1 {
		t.Fatalf("skipped = %v, want the one account with no session", plan.skipped)
	}
	lines = nil
	if _, err := app.writeExport(plan, filepath.Join(t.TempDir(), "s.json")); err != nil {
		t.Fatalf("writeExport: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("logged %d lines, want the skip note then the export line: %v", len(lines), lines)
	}
	if lines[0] != "导出 Session 跳过 1 个无 Session 邮箱" {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "已导出 1 个选中邮箱 Session JSON: ") {
		t.Errorf("second line = %q", lines[1])
	}
}

// CopyExportPreview is Tk's 复制内容, which copies
// preview.get("1.0", END).rstrip("\n") — so the clipboard text is the document
// MINUS its trailing newline, while CopySessionConversion keeps it.
func TestCopyExportPreviewStripsTheTrailingNewline(t *testing.T) {
	app := exportApp(t)
	full, err := app.PreviewExport(string(ExportEmailRT), []string{"a@example.com"})
	if err != nil {
		t.Fatalf("PreviewExport: %v", err)
	}
	copied, err := app.CopyExportPreview(string(ExportEmailRT), []string{"a@example.com"})
	if err != nil {
		t.Fatalf("CopyExportPreview: %v", err)
	}
	if !strings.HasSuffix(full.Text, "\n") {
		t.Fatalf("the document should end in a newline: %q", full.Text)
	}
	if copied.Text != strings.TrimSuffix(full.Text, "\n") {
		t.Errorf("copied = %q, want the document without its trailing newline", copied.Text)
	}
}

func TestTrimTrailingNewlines(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\n", "a"},
		{"a\n\n\n", "a"},   // rstrip("\n") removes EVERY trailing newline
		{"a\n\t", "a\n\t"}, // and no other whitespace
		{"\n", ""},
		{"", ""},
		{"a\r\n", "a\r"}, // "\n" only; the \r is not in the strip set
	}
	for _, tt := range tests {
		if got := trimTrailingNewlines(tt.in); got != tt.want {
			t.Errorf("trimTrailingNewlines(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Tk's filedialog takes defaultextension; wails SaveDialogOptions has no
// equivalent, so a user who picks the All/*.* filter and types "accounts" would
// otherwise get an extensionless file where Tk wrote "accounts.txt".
func TestApplyDefaultExtension(t *testing.T) {
	tests := []struct {
		path, ext, want string
	}{
		{`C:\x\accounts`, ".txt", `C:\x\accounts.txt`},
		{`C:\x\accounts.txt`, ".txt", `C:\x\accounts.txt`},
		{`C:\x\accounts.json`, ".txt", `C:\x\accounts.json`}, // an explicit one wins
		{`C:\x\accounts`, "", `C:\x\accounts`},
		{`C:\my.dir\accounts`, ".zip", `C:\my.dir\accounts.zip`}, // a dot in a PARENT is not an extension
	}
	for _, tt := range tests {
		if got := applyDefaultExtension(tt.path, tt.ext); got != tt.want {
			t.Errorf("applyDefaultExtension(%q, %q) = %q, want %q", tt.path, tt.ext, got, tt.want)
		}
	}
}

func TestExportMissingRT(t *testing.T) {
	app := exportApp(t)
	view, err := app.ExportMissingRT([]string{"a@example.com", "c@example.com"})
	if err != nil {
		t.Fatalf("ExportMissingRT: %v", err)
	}
	if len(view.Emails) != 1 || view.Emails[0] != "c@example.com" {
		t.Errorf("Emails = %v, want just the account with no RT", view.Emails)
	}
	if view.Prompt == "" {
		t.Error("Prompt is empty; the frontend has nothing to confirm with")
	}

	// Nothing missing: Python takes the early `return False` and shows NO dialog,
	// so an empty Prompt is the signal to skip the confirmation entirely.
	view, err = app.ExportMissingRT([]string{"a@example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("ExportMissingRT: %v", err)
	}
	if len(view.Emails) != 0 || view.Prompt != "" {
		t.Errorf("nothing is missing, yet a dialog was raised: %+v", view)
	}
}

func TestPrepareSub2APIExport(t *testing.T) {
	app := exportApp(t)
	plan, err := app.PrepareSub2APIExport([]string{"a@example.com", "c@example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("PrepareSub2APIExport: %v", err)
	}
	if len(plan.Emails) != 2 || plan.Emails[0] != "a@example.com" || plan.Emails[1] != "b@example.com" {
		t.Errorf("Emails = %v, want the two authorized ones in selection order", plan.Emails)
	}
	if len(plan.ExportEmails) != len(plan.Emails) {
		t.Errorf("ExportEmails = %v, does not line up with Emails = %v", plan.ExportEmails, plan.Emails)
	}
	// 全部缺少 RT 时也必须返回显式授权计划，前端据此先执行
	// register_and_rt；直接报 ErrNoAuthorizedRT 会丢失待授权账号清单。
	plan, err = app.PrepareSub2APIExport([]string{"c@example.com"})
	if err != nil {
		t.Fatalf("PrepareSub2APIExport missing-only: %v", err)
	}
	if len(plan.Emails) != 0 || len(plan.ExportEmails) != 0 {
		t.Errorf("missing-only authorized fields = %+v, want empty", plan)
	}
	if len(plan.MissingEmails) != 1 || plan.MissingEmails[0] != "c@example.com" {
		t.Errorf("MissingEmails = %v, want c@example.com", plan.MissingEmails)
	}
	if plan.AuthorizationPrompt == "" {
		t.Error("AuthorizationPrompt is empty; the frontend cannot confirm authorization")
	}
}
