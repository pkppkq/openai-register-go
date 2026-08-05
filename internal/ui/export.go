package ui

// UI_SPEC gap G23: the export buttons of the 账户 screen (app.py:24055-24533).
//
// internal/export owns every byte of every export — the seven conversion
// formats, the newline translation, the skip bookkeeping and the verbatim
// messagebox bodies. Nothing here recomputes any of it. This file is only the
// boundary: resolve the frontend's selection against state.json, hand the
// accounts to internal/export, and put the result through a save dialog.
//
// Three Tk behaviours have no direct Wails equivalent and are reproduced here
// rather than in internal/export, which is deliberately pure:
//
//   - _preview_and_save_text (app.py:24055-24095) is a modal Toplevel that shows
//     the text, offers 复制内容, and only then opens the file dialog. A webview
//     renders its own preview, so the two halves are separate bound methods:
//     PreviewExport builds the text, SaveExport writes it, CopyExportPreview is
//     the 复制内容 button.
//   - Tk's filedialog takes `defaultextension`; wails SaveDialogOptions has only
//     Filters and DefaultFilename, so applyDefaultExtension reproduces it.
//   - Tk owns the clipboard through the root window; here it is
//     wailsruntime.ClipboardSetText.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pkppkq/openai-register-go/internal/accounts"
	"github.com/pkppkq/openai-register-go/internal/export"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/sessionconv"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// ---------------------------------------------------------------------------
// Kinds
// ---------------------------------------------------------------------------

// ExportKind names one export button. The values are the wire strings the
// frontend passes to PreviewExport / SaveExport.
type ExportKind string

const (
	// ExportRaw is 导出选中Raw — export_selected_raw (app.py:24406).
	ExportRaw ExportKind = "raw"
	// ExportAuthorized is 已授权 — export_authorized (app.py:24098).
	ExportAuthorized ExportKind = "authorized"
	// ExportEmailRT is 邮箱----RT — export_authorized_email_rt (app.py:24118).
	ExportEmailRT ExportKind = "email_rt"
	// ExportSessions is 选中 Session — export_selected_sessions (app.py:24138).
	ExportSessions ExportKind = "sessions"
	// ExportConversion is 导出转换 — export_selected_session_conversion
	// (app.py:24355).
	ExportConversion ExportKind = "conversion"
	// ExportConversionZIP is 导出ZIP — export_selected_session_conversion_zip
	// (app.py:24377).
	ExportConversionZIP ExportKind = "conversion_zip"
)

// exportKinds is the accepted set, checked before any disk read so a typo does
// not cost a state.json load.
var exportKinds = map[ExportKind]bool{
	ExportRaw:           true,
	ExportAuthorized:    true,
	ExportEmailRT:       true,
	ExportSessions:      true,
	ExportConversion:    true,
	ExportConversionZIP: true,
}

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// ExportPreview is what the preview pane renders: the contents of the Toplevel
// _preview_and_save_text raises (app.py:24062-24065) plus the bookkeeping the
// Tk version only ever puts in the log.
type ExportPreview struct {
	Kind ExportKind `json:"kind"`
	// Title is the dialog title, verbatim from app.py.
	Title string `json:"title"`
	// Text is the LF form: what the preview shows and what the clipboard gets.
	// It is NOT what lands on disk — see exportPlan.data.
	Text string `json:"text"`
	// SuggestedName is filedialog's initialfile. Python only sets one for the
	// ZIP export (app.py:24386); it is "" everywhere else.
	SuggestedName string `json:"suggestedName"`
	// Count is the N of Python's "已导出 {N} 个 ..." line.
	Count int `json:"count"`
	// Skipped are the accounts that produced no output, in selection order.
	Skipped []string `json:"skipped"`
	// SkippedNote is the log line Python writes for Skipped, verbatim, or "".
	SkippedNote string `json:"skippedNote"`
	// Entries are the ZIP member names, ExportConversionZIP only.
	Entries []string `json:"entries"`
}

// ExportResult is what a save reports back.
type ExportResult struct {
	Kind ExportKind `json:"kind"`
	// Path is the file written, or "" when the user cancelled the dialog.
	Path string `json:"path"`
	// Cancelled distinguishes "user closed the save dialog" from a failure.
	// app.py:24085/24393 `if not path: return` — cancelling is not an error.
	Cancelled bool `json:"cancelled"`
	// Bytes is the size actually written: the CRLF-translated length on Windows,
	// so it deliberately differs from len(preview.Text).
	Bytes       int      `json:"bytes"`
	Count       int      `json:"count"`
	Skipped     []string `json:"skipped"`
	SkippedNote string   `json:"skippedNote"`
	// Message is the log line app.py writes on success, verbatim.
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

// exportInputs is everything the internal/export entry points read, gathered
// from one state.json load so a single export cannot see two different files.
type exportInputs struct {
	accounts []models.MailAccount
	sessions export.SessionResults
	prefix   string
	format   string
	label    string
	settings settings.Settings
}

// selectedAccounts resolves the frontend's selection to accounts, in the order
// the frontend sent them.
//
// The Tk version reads Treeview item ids and indexes self.accounts
// (app.py:24418-24431), which a webview must not do: UI_SPEC §0.3 forbids
// addressing a row by index because a filtered or sorted table renumbers on
// every render. The address is the row identity instead (AccountRow.Key), so
// the match runs through accounts.KeyOf and agrees with the table the user
// actually clicked.
//
// An unknown address is an ERROR, not a silent skip. Exporting 4 of the 5
// mailboxes a user selected, with nothing saying which one vanished, is exactly
// the quiet data loss the Python app cannot produce — its indices always
// resolve against the list it just rendered.
func selectedAccounts(snapshot map[string]any, emails []string) ([]models.MailAccount, error) {
	if len(emails) == 0 {
		// app.py:24421, the verbatim showwarning body.
		return nil, export.ErrNoSelection
	}
	all := accountsFromSnapshot(snapshot)
	byKey := make(map[string]models.MailAccount, len(all))
	for _, acc := range all {
		key := accounts.Key(acc)
		// state.json can technically hold the same address twice. The table shows
		// one row per key, so the first row is the one the user could have
		// selected.
		if _, seen := byKey[key]; !seen {
			byKey[key] = acc
		}
	}
	out := make([]models.MailAccount, 0, len(emails))
	taken := make(map[string]bool, len(emails))
	for _, email := range emails {
		key := accounts.KeyOf(email)
		acc, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("账号不存在: %s", email)
		}
		// A Treeview selection is a SET of item ids (app.py:24419), so Python can
		// never hand the same account to an export twice. Collapse a repeated
		// address rather than writing its RT line out twice.
		if taken[key] {
			continue
		}
		taken[key] = true
		out = append(out, acc)
	}
	return out, nil
}

// exportInputs loads state.json once and pulls out the four things the export
// functions read.
func (a *App) exportInputs(emails []string) (exportInputs, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return exportInputs{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	selected, err := selectedAccounts(snapshot, emails)
	if err != nil {
		return exportInputs{}, err
	}
	st := settings.FromSnapshot(snapshot)
	return exportInputs{
		accounts: selected,
		// app.py:24146 / 24295 read self.session_results, which load_state built
		// with this same dict-values-only filter (app.py:14035-14036).
		sessions: export.SessionResults(sessionResultsFromSnapshot(snapshot)),
		// app.py:24104 / 24410 use self.export_name_prefix.get().strip(). The
		// PERSISTED value is already str.strip()'d — _build_state_snapshot writes
		// it that way (app.py:14266) and settings.ToSnapshot mirrors that — and
		// load copies it through unchanged (app.py:14166). So the stored string is
		// exactly what Python's .strip() would return, and re-stripping here with
		// a hand-rolled Python-compatible strip would only add a way to diverge.
		prefix: st.ExportNamePrefix,
		// FromSnapshot already applied .strip().lower() and the
		// SESSION_CONVERT_FORMATS fallback that app.py:24289-24291 applies.
		format:   st.SessionConvertFormat,
		label:    sessionconv.FormatLabel(st.SessionConvertFormat),
		settings: st,
	}, nil
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

// exportPlan is one finished export: internal/export's Document and Archive
// collapsed onto the fields the save dialog needs, plus the exact bytes to
// write.
type exportPlan struct {
	kind  ExportKind
	label string

	title            string
	text             string
	suggestedName    string
	defaultExtension string
	fileTypes        []export.FileType

	// data is what os.WriteFile gets. For a Document this is Document.File, NOT
	// Document.Text.
	data []byte

	count   int
	skipped []string
	entries []string
}

// exportPlan builds the document for one button press.
func (a *App) exportPlan(kind ExportKind, emails []string) (exportPlan, error) {
	if !exportKinds[kind] {
		return exportPlan{}, fmt.Errorf("未知的导出类型: %s", kind)
	}
	in, err := a.exportInputs(emails)
	if err != nil {
		return exportPlan{}, err
	}
	return buildExportPlan(kind, in)
}

func buildExportPlan(kind ExportKind, in exportInputs) (exportPlan, error) {
	if kind == ExportConversionZIP {
		// app.py:24377-24404. The archive is assembled in memory rather than
		// streamed into the chosen path, because the file dialog comes AFTER the
		// conversion in Python too (24384 refuses before 24386 asks for a path) —
		// a conversion that produces nothing must not have created an empty ZIP.
		archive, err := export.SessionConversionZIP(in.accounts, in.sessions, in.format, time.Time{})
		if err != nil {
			return exportPlan{}, err
		}
		data, err := archive.Bytes(time.Time{})
		if err != nil {
			return exportPlan{}, fmt.Errorf("打包 Session 转换 ZIP 失败: %w", err)
		}
		names := make([]string, 0, len(archive.Entries))
		for _, entry := range archive.Entries {
			names = append(names, entry.Name)
		}
		return exportPlan{
			kind:             kind,
			label:            in.label,
			title:            archive.Title,
			suggestedName:    archive.SuggestedName,
			defaultExtension: archive.DefaultExtension,
			fileTypes:        archive.FileTypes,
			data:             data,
			count:            archive.Count,
			skipped:          archive.Skipped,
			entries:          names,
		}, nil
	}

	doc, err := buildExportDocument(kind, in)
	if err != nil {
		return exportPlan{}, err
	}
	return exportPlan{
		kind:             kind,
		label:            in.label,
		title:            doc.Title,
		text:             doc.Text,
		suggestedName:    doc.SuggestedName,
		defaultExtension: doc.DefaultExtension,
		fileTypes:        doc.FileTypes,
		// Document.File, never Document.Text. Python writes with
		// Path.write_text(text, encoding="utf-8"), which opens the file in TEXT
		// mode with newline=None, so CPython translates every "\n" to os.linesep
		// and the Tk app's exports are CRLF on Windows. Writing Text would make
		// every file this app produces differ, byte for byte, from the one the
		// Python app produces for the same accounts.
		data:    doc.File,
		count:   doc.Count,
		skipped: doc.Skipped,
	}, nil
}

// buildExportDocument dispatches the five text/JSON exports. Every error it can
// return is one of internal/export's typed errors, whose text is the verbatim
// messagebox.showwarning body — so it is passed through UNWRAPPED and the
// frontend can show err.Error() as-is.
func buildExportDocument(kind ExportKind, in exportInputs) (export.Document, error) {
	switch kind {
	case ExportRaw:
		return export.Raw(in.accounts, in.prefix)
	case ExportAuthorized:
		// The RT back-fill export_authorized runs first
		// (_ensure_export_accounts_have_rt, app.py:24102) is a network task; see
		// ExportMissingRT.
		return export.Authorized(in.accounts, in.prefix)
	case ExportEmailRT:
		return export.AuthorizedEmailRT(in.accounts)
	case ExportSessions:
		return export.Sessions(in.accounts, in.sessions)
	case ExportConversion:
		return export.SessionConversion(in.accounts, in.sessions, in.format, time.Time{})
	}
	return export.Document{}, fmt.Errorf("未知的导出类型: %s", kind)
}

// skippedNote is the log line Python writes for the accounts an export dropped.
// The three exports that have one do not share a wording.
func skippedNote(kind ExportKind, skipped []string) string {
	switch kind {
	case ExportSessions:
		if len(skipped) == 0 {
			return ""
		}
		// app.py:24164 — a count only, no addresses.
		return fmt.Sprintf("导出 Session 跳过 %d 个无 Session 邮箱", len(skipped))
	case ExportConversion:
		return export.SkippedNote("Session 转换", skipped) // app.py:24351, 24375
	case ExportConversionZIP:
		return export.SkippedNote("Session 转换 ZIP", skipped) // app.py:24404
	}
	return ""
}

// exportLogLine is the success line app.py writes, verbatim per button.
func exportLogLine(plan exportPlan, path string) string {
	switch plan.kind {
	case ExportRaw:
		return fmt.Sprintf("已导出 %d 个选中邮箱 Raw TXT: %s", plan.count, path) // app.py:24416
	case ExportAuthorized:
		return fmt.Sprintf("已导出 %d 个已授权邮箱 TXT: %s", plan.count, path) // app.py:24116
	case ExportEmailRT:
		return fmt.Sprintf("已导出 %d 个邮箱----RT TXT: %s", plan.count, path) // app.py:24136
	case ExportSessions:
		return fmt.Sprintf("已导出 %d 个选中邮箱 Session JSON: %s", plan.count, path) // app.py:24166
	case ExportConversion:
		return fmt.Sprintf("已导出 %d 个 Session 转换结果 %s: %s", plan.count, plan.label, path) // app.py:24374
	case ExportConversionZIP:
		return fmt.Sprintf("已导出 %d 个独立 JSON 到 Session 转换 ZIP %s: %s", plan.count, plan.label, path) // app.py:24403
	}
	return ""
}

// ---------------------------------------------------------------------------
// Bound methods
// ---------------------------------------------------------------------------

// PreviewExport builds one export and returns it WITHOUT writing anything: the
// contents of the modal _preview_and_save_text raises (app.py:24062-24065),
// which is the step the user checks before committing to a file.
//
// For ExportConversionZIP there is no Tk preview at all — app.py:24386 goes
// straight to the file dialog — so Text is empty and Entries carries the member
// names instead, which is the only thing about a ZIP worth showing first.
func (a *App) PreviewExport(kind string, emails []string) (ExportPreview, error) {
	plan, err := a.exportPlan(ExportKind(kind), emails)
	if err != nil {
		return ExportPreview{}, err
	}
	return exportPreviewOf(plan, plan.text), nil
}

// SaveExport is 确定导出 (app.py:24081-24087) plus the write at app.py:24115 —
// build the document, ask for a path, write the bytes.
//
// The document is built BEFORE the dialog, exactly as Python does: an export
// that has nothing to write must refuse with its warning instead of making the
// user pick a filename first. The bytes written are the ones built here, not a
// second build after the dialog, so a state.json the Python app edits while the
// dialog is open cannot change what the user just previewed.
func (a *App) SaveExport(kind string, emails []string) (ExportResult, error) {
	plan, err := a.exportPlan(ExportKind(kind), emails)
	if err != nil {
		return ExportResult{}, err
	}
	path, err := a.saveExportDialog(plan)
	if err != nil {
		return ExportResult{}, err
	}
	if path == "" {
		// app.py:24085 / 24393 `if not path: return` — cancelling writes nothing,
		// logs nothing, and is not an error.
		return ExportResult{
			Kind:        plan.kind,
			Cancelled:   true,
			Count:       plan.count,
			Skipped:     exportStrings(plan.skipped),
			SkippedNote: skippedNote(plan.kind, plan.skipped),
		}, nil
	}
	return a.writeExport(plan, path)
}

// CopyExportPreview is the 复制内容 button of the preview dialog
// (app.py:24067-24071).
//
// The returned Text is what landed on the clipboard, and it is one byte shorter
// than PreviewExport's: Python copies `preview.get("1.0", END).rstrip("\n")`,
// so the document's trailing newline (and Tk's own appended one) is stripped.
func (a *App) CopyExportPreview(kind string, emails []string) (ExportPreview, error) {
	plan, err := a.exportPlan(ExportKind(kind), emails)
	if err != nil {
		return ExportPreview{}, err
	}
	text := trimTrailingNewlines(plan.text)
	a.setClipboard(text)
	a.Log("导出预览内容已复制到剪贴板") // app.py:24071
	return exportPreviewOf(plan, text), nil
}

// CopySessionConversion ports copy_selected_session_conversion
// (app.py:24342-24353) — the 复制转换 button, which is NOT the preview dialog's
// copy: it puts the document on the clipboard WITH its trailing newline
// (app.py:24349 clipboard_append(text)) and logs a different line.
func (a *App) CopySessionConversion(emails []string) (ExportPreview, error) {
	plan, err := a.exportPlan(ExportConversion, emails)
	if err != nil {
		return ExportPreview{}, err
	}
	a.setClipboard(plan.text)
	preview := exportPreviewOf(plan, plan.text)
	// app.py:24350 — the label, not the title, and no path.
	a.Log(fmt.Sprintf("已复制 %d 个 Session 转换结果: %s", plan.count, plan.label))
	if preview.SkippedNote != "" {
		a.Log(preview.SkippedNote) // app.py:24351
	}
	return preview, nil
}

// MissingRTView is the askyesno _ensure_export_accounts_have_rt raises before an
// export silently authorizes the accounts that have no RT yet
// (app.py:24439-24449).
type MissingRTView struct {
	// Emails are the selected accounts with no openai_rt, in selection order.
	Emails []string `json:"emails"`
	// Prompt is the confirmation body, verbatim. Empty when nothing is missing,
	// i.e. when Python takes the early `return False` at app.py:24442 and shows
	// no dialog at all.
	Prompt string `json:"prompt"`
}

// ExportMissingRT is the decision half of _ensure_export_accounts_have_rt
// (app.py:24439-24449): which selected accounts an export would have to
// authorize first, and the exact confirmation to show.
//
// RT 回填本身（app.py:24457-24483）是网络任务：逐账号授权后，再通过
// export-authorized-ready / export-email-rt-ready 事件重新进入导出。前端先用
// 本方法取得缺失列表，确认后调用带后端确认门禁的授权入口，全部完成后再调用
// SaveExport。本方法不会启动任务，也不会产生付费副作用。
func (a *App) ExportMissingRT(emails []string) (MissingRTView, error) {
	in, err := a.exportInputs(emails)
	if err != nil {
		return MissingRTView{}, err
	}
	missing := export.MissingRT(in.accounts) // app.py:24440
	out := MissingRTView{
		Emails: make([]string, 0, len(missing)),
		Prompt: export.MissingRTPrompt(missing),
	}
	for _, acc := range missing {
		out.Emails = append(out.Emails, acc.Email)
	}
	return out, nil
}

// Sub2APIPlan is the synchronous half of the sub2api button.
type Sub2APIPlan struct {
	// Emails are the selected accounts that already hold an openai_rt, in
	// selection order (app.py:24493).
	Emails []string `json:"emails"`
	// ExportEmails are the addresses that would be written into the file — the
	// name prefix applied per app.py:24527.
	ExportEmails []string `json:"exportEmails"`
	// MissingEmails 是必须先走 OAuth 授权入口的账号。只返回 Emails 而把
	// 这些账号静默过滤掉，会生成一个看似成功但不完整的导出。
	MissingEmails []string `json:"missingEmails"`
	// AuthorizationPrompt 是 Python 在自动授权前显示的确认正文；前端据此
	// 明确进入 register_and_rt，而不是写入占位 refresh_token。
	AuthorizationPrompt string `json:"authorizationPrompt"`
}

// PrepareSub2APIExport ports the gate of export_sub2api /
// _start_sub2api_export_with_accounts (app.py:24484-24496): a non-empty
// selection, the accounts that already hold an openai_rt, the addresses the
// file would carry, and an explicit authorization plan for every missing RT.
func (a *App) PrepareSub2APIExport(emails []string) (Sub2APIPlan, error) {
	in, err := a.exportInputs(emails)
	if err != nil {
		return Sub2APIPlan{}, err
	}
	return sub2APIPlanOf(in), nil
}

func sub2APIPlanOf(in exportInputs) Sub2APIPlan {
	missing := export.MissingRT(in.accounts)
	out := Sub2APIPlan{
		Emails:              make([]string, 0, len(in.accounts)-len(missing)),
		ExportEmails:        make([]string, 0, len(in.accounts)-len(missing)),
		MissingEmails:       make([]string, 0, len(missing)),
		AuthorizationPrompt: export.MissingRTPrompt(missing),
	}
	for _, acc := range in.accounts {
		if acc.OpenaiRT == "" {
			out.MissingEmails = append(out.MissingEmails, acc.Email)
			continue
		}
		out.Emails = append(out.Emails, acc.Email)
		out.ExportEmails = append(out.ExportEmails, export.Sub2APIExportEmail(in.prefix, acc.Email))
	}
	return out
}

// ExportRefreshFailure 是导出前刷新单个账号失败的可序列化结果。
type ExportRefreshFailure struct {
	Email string `json:"email"`
	Error string `json:"error"`
}

// CPAExportRefreshResult 记录 CPA 转换前刷新阶段的完整结果。单账号失败不会
// 阻断其他账号，调用方仍可按 Python 行为继续生成使用旧 Access Token 的转换。
type CPAExportRefreshResult struct {
	Format          string                 `json:"format"`
	Required        bool                   `json:"required"`
	Selected        int                    `json:"selected"`
	Requested       int                    `json:"requested"`
	RefreshedEmails []string               `json:"refreshedEmails"`
	RotatedEmails   []string               `json:"rotatedEmails"`
	Failures        []ExportRefreshFailure `json:"failures"`
	Note            string                 `json:"note"`
}

// Sub2APIExportResult 是后台刷新完成后可预览、可保存的可信文档。document
// 不暴露给 webview；SaveSub2APIExport 只写后端保留的原始字节。
type Sub2APIExportResult struct {
	Plan            Sub2APIPlan            `json:"plan"`
	Text            string                 `json:"text"`
	Count           int                    `json:"count"`
	RefreshedEmails []string               `json:"refreshedEmails"`
	RotatedEmails   []string               `json:"rotatedEmails"`
	Failures        []ExportRefreshFailure `json:"failures"`

	document export.Document
}

// Sub2APISaveResult 描述已完成 sub2api 任务的保存结果。
type Sub2APISaveResult struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"cancelled"`
	Bytes     int    `json:"bytes"`
	Count     int    `json:"count"`
	Message   string `json:"message"`
}

// exportRefreshUpdate 是一次成功刷新要原子写回 Account 与 Session 的字段。
type exportRefreshUpdate struct {
	email         string
	accessToken   string
	refreshToken  string
	idToken       string
	accessSummary map[string]any
	planType      string
	accountID     string
	record        map[string]any
}

const exportRefreshJobIdentity = "导出刷新"

// StartCPAExportRefresh 启动 CPA 转换前刷新。它只负责刷新与持久化；任务成功
// 后调用原有 PreviewExport、CopySessionConversion 或 SaveExport 构建文档。
func (a *App) StartCPAExportRefresh(emails []string) (JobView, error) {
	if _, err := a.exportInputs(emails); err != nil {
		return JobView{}, err
	}
	selection := append([]string(nil), emails...)
	return a.startNetworkJobWithLogEmail(
		JobCPAExportRefresh,
		exportRefreshJobIdentity,
		"",
		func(ctx context.Context, log func(string)) (any, error) {
			return a.refreshCPAExport(ctx, selection, log)
		},
	)
}

// StartSub2APIExport 启动刷新与文档构建。存在缺失 RT 时明确拒绝并要求调用方
// 使用 PrepareSub2APIExport 返回的授权计划，不会只导出剩余账号。
func (a *App) StartSub2APIExport(emails []string) (JobView, error) {
	plan, err := a.PrepareSub2APIExport(emails)
	if err != nil {
		return JobView{}, err
	}
	if len(plan.MissingEmails) > 0 {
		return JobView{}, fmt.Errorf("sub2api 导出前需要先授权获取 RT：%s", strings.Join(plan.MissingEmails, ", "))
	}
	if len(plan.Emails) == 0 {
		return JobView{}, export.ErrNoAuthorizedRT
	}
	selection := append([]string(nil), emails...)
	return a.startNetworkJobWithLogEmail(
		JobSub2APIExport,
		exportRefreshJobIdentity,
		"",
		func(ctx context.Context, log func(string)) (any, error) {
			return a.buildSub2APIExport(ctx, selection, log)
		},
	)
}

// SaveSub2APIExport 保存后台任务保留的 Document.File，避免把 webview 中可被
// 修改的预览文本重新作为导出源。
func (a *App) SaveSub2APIExport(jobID string) (Sub2APISaveResult, error) {
	a.jobs.mu.Lock()
	j := a.jobs.jobs[jobID]
	if j == nil {
		a.jobs.mu.Unlock()
		return Sub2APISaveResult{}, fmt.Errorf("任务不存在: %s", jobID)
	}
	view := j.view
	rawResult := j.result
	a.jobs.mu.Unlock()

	if view.Kind != JobSub2APIExport {
		return Sub2APISaveResult{}, fmt.Errorf("任务不是 sub2api 导出: %s", jobID)
	}
	if view.Status == StatusRunning {
		return Sub2APISaveResult{}, fmt.Errorf("任务尚未结束: %s", jobID)
	}
	if view.Status != StatusSucceeded {
		if view.Error != "" {
			return Sub2APISaveResult{}, errors.New(view.Error)
		}
		return Sub2APISaveResult{}, fmt.Errorf("sub2api 导出任务未成功: %s", view.Status)
	}
	result, ok := rawResult.(Sub2APIExportResult)
	if !ok || result.document.Count == 0 {
		return Sub2APISaveResult{}, fmt.Errorf("sub2api 导出任务缺少可保存文档: %s", jobID)
	}
	plan := exportPlan{
		title:            result.document.Title,
		suggestedName:    result.document.SuggestedName,
		defaultExtension: result.document.DefaultExtension,
		fileTypes:        result.document.FileTypes,
		data:             result.document.File,
		count:            result.document.Count,
	}
	path, err := a.saveExportDialog(plan)
	if err != nil {
		return Sub2APISaveResult{}, err
	}
	if path == "" {
		return Sub2APISaveResult{Cancelled: true, Count: result.Count}, nil
	}
	return a.writeSub2APIExport(result, path)
}

// refreshCPAExport 对齐 app.py:24168-24276：账号 RT 优先于 Session RT，
// 每个账号独立失败，所有成功更新在转换前一次性持久化。
func (a *App) refreshCPAExport(ctx context.Context, emails []string, log func(string)) (CPAExportRefreshResult, error) {
	in, err := a.exportInputs(emails)
	if err != nil {
		return CPAExportRefreshResult{}, err
	}
	sessionRT := make(map[string]string, len(in.accounts))
	for _, account := range in.accounts {
		sessionRT[account.Email] = openai.FirstNonEmpty(exportSessionPayload(in.sessions, account.Email)["openai_rt"])
	}
	gate := sessionconv.CPARefreshGate(in.format, in.accounts, sessionRT)
	result := CPAExportRefreshResult{
		Format:          in.format,
		Required:        gate.Refresh && len(gate.Refreshable) > 0,
		Selected:        len(in.accounts),
		Requested:       len(gate.Refreshable),
		RefreshedEmails: []string{},
		RotatedEmails:   []string{},
		Failures:        []ExportRefreshFailure{},
		Note:            gate.Note,
	}
	if gate.Note != "" {
		if log != nil {
			log(gate.Note)
		}
		return result, nil
	}
	if !result.Required {
		return result, nil
	}

	dynamicProxies := exportDynamicProxies(in.settings)
	if log != nil {
		log(fmt.Sprintf("CPA 导出前开始刷新 RT: %d/%d 个账号", result.Requested, result.Selected))
	}
	updates := make([]exportRefreshUpdate, 0, len(gate.Refreshable))
	for index, account := range gate.Refreshable {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		currentRT := openai.FirstNonEmpty(account.OpenaiRT, exportSessionPayload(in.sessions, account.Email)["openai_rt"])
		dynamic := ""
		if len(dynamicProxies) > 0 {
			dynamic = dynamicProxies[index%len(dynamicProxies)]
		}
		accountLog := func(line string) {
			if log != nil {
				log("[" + account.Email + "] " + line)
			}
		}
		proxySession, proxyURL, proxyErr := a.networkProxy(in.settings, dynamic, accountLog)
		if proxyErr != nil {
			result.Failures = append(result.Failures, exportRefreshFailure(account.Email, proxyErr))
			accountLog("CPA RT 刷新失败，将尝试使用现有 Access Token: " + proxyErr.Error())
			continue
		}
		accountLog("CPA 导出前刷新 RT 使用代理: " + exportProxyLabel(proxySession.Config))
		payload, callErr := networkRefreshAccessToken(ctx, currentRT, proxyURL)
		proxySession.Close()
		if callErr != nil {
			if networkCancelled(ctx, callErr) {
				return result, callErr
			}
			result.Failures = append(result.Failures, exportRefreshFailure(account.Email, callErr))
			accountLog("CPA RT 刷新失败，将尝试使用现有 Access Token: " + callErr.Error())
			continue
		}
		update, updateErr := exportRefreshUpdateFromPayload(account.Email, account.Email, currentRT, payload)
		if updateErr != nil {
			result.Failures = append(result.Failures, exportRefreshFailure(account.Email, updateErr))
			accountLog("CPA RT 刷新失败，将尝试使用现有 Access Token: " + updateErr.Error())
			continue
		}
		updates = append(updates, update)
		result.RefreshedEmails = append(result.RefreshedEmails, account.Email)
		if update.refreshToken != currentRT {
			result.RotatedEmails = append(result.RotatedEmails, account.Email)
		}
		plan := update.planType
		if plan == "" {
			plan = openai.FirstNonEmpty(update.accessSummary["plan_type"], "unknown")
		}
		accountLog(fmt.Sprintf(
			"CPA RT 刷新成功: plan=%s account尾号=%s",
			plan,
			exportTail(update.accountID, 8),
		))
	}
	if err := a.persistExportRefreshUpdates(updates); err != nil {
		return result, err
	}
	return result, nil
}

// buildSub2APIExport 对齐 app.py:24513-24533。每个 access token 都来自
// networkRefreshAccessToken seam；有效旋转 RT 先落盘，再生成最终文档。
func (a *App) buildSub2APIExport(ctx context.Context, emails []string, log func(string)) (Sub2APIExportResult, error) {
	in, err := a.exportInputs(emails)
	if err != nil {
		return Sub2APIExportResult{}, err
	}
	result := Sub2APIExportResult{
		Plan:            sub2APIPlanOf(in),
		RefreshedEmails: []string{},
		RotatedEmails:   []string{},
		Failures:        []ExportRefreshFailure{},
	}
	if len(result.Plan.MissingEmails) > 0 {
		return result, fmt.Errorf(
			"sub2api 导出前需要先授权获取 RT：%s",
			strings.Join(result.Plan.MissingEmails, ", "),
		)
	}
	accounts, err := export.Sub2APISelection(in.accounts)
	if err != nil {
		return result, err
	}

	dynamic := ""
	if proxies := exportDynamicProxies(in.settings); len(proxies) > 0 {
		dynamic = a.nextDynamicProxy(proxies)
	}
	proxySession, proxyURL, err := a.networkProxy(in.settings, dynamic, log)
	if err != nil {
		return result, err
	}
	defer proxySession.Close()
	if log != nil {
		log("导出 sub2api 使用代理: " + exportProxyLabel(proxySession.Config))
	}

	records := make([]map[string]any, 0, len(accounts))
	updates := make([]exportRefreshUpdate, 0, len(accounts))
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			persistErr := a.persistExportRefreshUpdates(updates)
			return result, networkJoin(err, persistErr)
		}
		currentRT := account.OpenaiRT
		payload, callErr := networkRefreshAccessToken(ctx, currentRT, proxyURL)
		if callErr != nil {
			result.Failures = append(result.Failures, exportRefreshFailure(account.Email, callErr))
			persistErr := a.persistExportRefreshUpdates(updates)
			return result, networkJoin(fmt.Errorf("%s: %w", account.Email, callErr), persistErr)
		}
		update, updateErr := exportRefreshUpdateFromPayload(
			account.Email,
			export.Sub2APIExportEmail(in.prefix, account.Email),
			currentRT,
			payload,
		)
		if updateErr != nil {
			result.Failures = append(result.Failures, exportRefreshFailure(account.Email, updateErr))
			persistErr := a.persistExportRefreshUpdates(updates)
			return result, networkJoin(fmt.Errorf("%s: %w", account.Email, updateErr), persistErr)
		}
		updates = append(updates, update)
		records = append(records, update.record)
		result.RefreshedEmails = append(result.RefreshedEmails, account.Email)
		if update.refreshToken != currentRT {
			result.RotatedEmails = append(result.RotatedEmails, account.Email)
		}
		if log != nil {
			log("已刷新 sub2api token: " + account.Email)
		}
	}
	if len(records) == 0 {
		return result, export.ErrNoSub2APIRecords
	}
	if err := a.persistExportRefreshUpdates(updates); err != nil {
		return result, err
	}
	document, err := export.Sub2API(records, time.Time{})
	if err != nil {
		return result, err
	}
	result.document = document
	result.Text = document.Text
	result.Count = document.Count
	return result, nil
}

// exportRefreshUpdateFromPayload 统一 CPA 与 sub2api 的响应解析。响应没有
// 有效新 RT 时保留旧 RT，并强制把最终 RT 写回 payload 后再建记录。
func exportRefreshUpdateFromPayload(
	accountEmail string,
	recordEmail string,
	currentRT string,
	payload map[string]any,
) (exportRefreshUpdate, error) {
	tokenPayload := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		tokenPayload[key] = value
	}
	refreshedRT := settings.PyStr(pyOrEmpty(tokenPayload["refresh_token"]))
	if !sessionconv.IsOpenAIRefreshToken(refreshedRT) {
		refreshedRT = currentRT
	}
	tokenPayload["refresh_token"] = refreshedRT
	record, err := export.RecordFromRefreshPayload(recordEmail, tokenPayload, time.Time{})
	if err != nil {
		return exportRefreshUpdate{}, err
	}
	accessToken := openai.FirstNonEmpty(record["access_token"])
	summary := openai.SummarizeChatGPTAccessToken(accessToken)
	planType := openai.ClassifyChatGPTPlanText(openai.FirstNonEmpty(record["plan_type"], summary["plan_type"]))
	if planType != "" {
		record["plan_type"] = planType
		summary["plan_type"] = planType
	}
	accountID := openai.FirstNonEmpty(record["account_id"], summary["account_id"])
	return exportRefreshUpdate{
		email:         accountEmail,
		accessToken:   accessToken,
		refreshToken:  refreshedRT,
		idToken:       openai.FirstNonEmpty(record["id_token"]),
		accessSummary: summary,
		planType:      planType,
		accountID:     accountID,
		record:        record,
	}, nil
}

// persistExportRefreshUpdates 在一个 mutateState 事务中同时更新账号和拆分
// Session 文件，保留刷新期间 Python 或其他任务写入的无关字段。
func (a *App) persistExportRefreshUpdates(updates []exportRefreshUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		rows, _ := snapshot["accounts"].([]any)
		accountRows := make(map[string]map[string]any, len(rows))
		actualEmails := make(map[string]string, len(rows))
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			email := networkText(m["email"])
			key := strings.ToLower(email)
			if key == "" || accountRows[key] != nil {
				continue
			}
			accountRows[key] = m
			actualEmails[key] = email
		}
		for _, update := range updates {
			if accountRows[strings.ToLower(update.email)] == nil {
				return snapshot, nil, fmt.Errorf("账号不存在: %s", update.email)
			}
		}

		sessions, _ := snapshot["session_results"].(map[string]any)
		if sessions == nil {
			sessions = map[string]any{}
		}
		dirty := make(map[string]bool, len(updates))
		for _, update := range updates {
			key := strings.ToLower(update.email)
			accountRow := accountRows[key]
			actualEmail := actualEmails[key]
			if sessionconv.IsOpenAIRefreshToken(update.refreshToken) {
				accountRow["openai_rt"] = update.refreshToken
			}
			if planTypesAdopted[update.planType] {
				accountRow["account_type"] = update.planType
			}

			sessionKey := actualEmail
			for existingKey := range sessions {
				if strings.EqualFold(existingKey, actualEmail) {
					sessionKey = existingKey
					break
				}
			}
			payload, _ := sessions[sessionKey].(map[string]any)
			if payload == nil {
				payload = map[string]any{}
			}
			payload["access_token"] = update.accessToken
			payload["openai_rt"] = update.refreshToken
			payload["id_token"] = openai.FirstNonEmpty(update.idToken, payload["id_token"])
			if len(update.accessSummary) > 0 {
				payload["access_summary"] = update.accessSummary
			}
			payload["plan_type"] = openai.FirstNonEmpty(update.planType, payload["plan_type"])
			payload["chatgpt_plan_type"] = openai.FirstNonEmpty(update.planType, payload["chatgpt_plan_type"])
			payload["account_id"] = openai.FirstNonEmpty(update.accountID, payload["account_id"])
			payload["chatgpt_account_id"] = openai.FirstNonEmpty(update.accountID, payload["chatgpt_account_id"])
			sessions[sessionKey] = payload
			dirty[sessionKey] = true
		}
		snapshot["session_results"] = sessions
		return snapshot, dirty, nil
	})
}

func exportSessionPayload(results export.SessionResults, email string) map[string]any {
	for key, value := range results {
		if strings.EqualFold(key, email) {
			if payload, ok := value.(map[string]any); ok {
				return payload
			}
			break
		}
	}
	return map[string]any{}
}

func exportDynamicProxies(st settings.Settings) []string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return nil
	}
	return proxypool.ParseProxyPoolText(st.DynamicProxies)
}

func exportRefreshFailure(email string, err error) ExportRefreshFailure {
	return ExportRefreshFailure{Email: email, Error: networkTruncate(err.Error(), 180)}
}

func exportProxyLabel(config models.ProxyConfig) string {
	local := proxypool.MaskProxyURL(config.LocalProxy)
	dynamic := proxypool.MaskProxyURL(config.DynamicProxy)
	switch {
	case local != "" && dynamic != "":
		return local + " -> " + dynamic
	case local != "":
		return local
	case dynamic != "":
		return dynamic
	default:
		return "直连"
	}
}

func exportTail(value string, length int) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return "-"
	}
	if len(runes) <= length {
		return value
	}
	return string(runes[len(runes)-length:])
}

func (a *App) writeSub2APIExport(result Sub2APIExportResult, path string) (Sub2APISaveResult, error) {
	if result.document.Count == 0 || len(result.document.File) == 0 {
		return Sub2APISaveResult{}, export.ErrNoSub2APIRecords
	}
	if err := os.WriteFile(path, result.document.File, 0o644); err != nil {
		return Sub2APISaveResult{}, fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	message := fmt.Sprintf("已导出 %d 个账号 sub2api JSON: %s", result.document.Count, path)
	a.Log(message)
	return Sub2APISaveResult{
		Path:    path,
		Bytes:   len(result.document.File),
		Count:   result.document.Count,
		Message: message,
	}, nil
}

// ---------------------------------------------------------------------------
// Dialog / clipboard / disk
// ---------------------------------------------------------------------------

// saveExportDialog is filedialog.asksaveasfilename (app.py:24081, 24387,
// 24500). It returns "" when the user cancelled, which every caller treats as
// "do nothing" rather than as a failure.
//
// It blocks the Wails call thread for as long as the dialog is open. That is the
// same contract the Tk version has (the dialog is modal), and it is why nothing
// slower than a disk write may share this path.
func (a *App) saveExportDialog(plan exportPlan) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("窗口尚未就绪")
	}
	filters := make([]wailsruntime.FileFilter, 0, len(plan.fileTypes))
	for _, ft := range plan.fileTypes {
		filters = append(filters, wailsruntime.FileFilter{DisplayName: ft.Label, Pattern: ft.Pattern})
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           plan.title,
		DefaultFilename: plan.suggestedName,
		Filters:         filters,
		// Tk's dialog lets the user create one; without this the ZIP export
		// cannot be dropped into a new folder.
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", fmt.Errorf("选择保存文件失败: %w", err)
	}
	if path == "" {
		return "", nil
	}
	return applyDefaultExtension(path, plan.defaultExtension), nil
}

// applyDefaultExtension reproduces Tk's `defaultextension` (app.py:24080).
//
// wails SaveDialogOptions has no equivalent field — it only takes Filters — so
// without this a user who selects the All/*.* filter and types "accounts" gets
// an extensionless file where Tk would have written "accounts.txt". Tk appends
// only when the typed name has no extension, and so does this; the Windows
// dialog usually appends one from the selected filter already, in which case
// filepath.Ext is non-empty and nothing more is added.
func applyDefaultExtension(path, ext string) string {
	if ext == "" || filepath.Ext(path) != "" {
		return path
	}
	return path + ext
}

// writeExport is Path(path).write_text(...) plus the log lines around it. Split
// out of SaveExport so the byte-fidelity half is reachable without a dialog.
func (a *App) writeExport(plan exportPlan, path string) (ExportResult, error) {
	// 0o644 rather than Python's 0o666&umask: Windows ignores the mode, and on
	// POSIX an export can hold refresh tokens, so group/other write is wrong.
	if err := os.WriteFile(path, plan.data, 0o644); err != nil {
		return ExportResult{}, fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	out := ExportResult{
		Kind:        plan.kind,
		Path:        path,
		Bytes:       len(plan.data),
		Count:       plan.count,
		Skipped:     exportStrings(plan.skipped),
		SkippedNote: skippedNote(plan.kind, plan.skipped),
		Message:     exportLogLine(plan, path),
	}
	// Order matters: app.py:24164 logs the Session skip line BEFORE the export
	// line, while the two conversion exports log theirs after (24375, 24404).
	if plan.kind == ExportSessions && out.SkippedNote != "" {
		a.Log(out.SkippedNote)
	}
	a.Log(out.Message)
	if plan.kind != ExportSessions && out.SkippedNote != "" {
		a.Log(out.SkippedNote)
	}
	// NOT PORTED, deliberately: app.py:24373 and 24402 call self.save_state()
	// after the conversion exports. That write exists to persist what the CPA
	// pre-refresh mutated in self.session_results; nothing here mutates state, so
	// saving would only stamp a new updated_at into a file the Python app shares.
	return out, nil
}

// setClipboard is the root.clipboard_clear/clipboard_append pair
// (app.py:24069-24070, 24348-24349).
//
// Like App.Log it drops when there is no window yet rather than failing: a
// clipboard write can only be triggered by a button in a window that exists.
func (a *App) setClipboard(text string) {
	if a.ctx == nil {
		return
	}
	if err := wailsruntime.ClipboardSetText(a.ctx, text); err != nil {
		a.Log("复制到剪贴板失败: " + err.Error())
	}
}

// exportPreviewOf projects a plan, with `text` as the body actually produced —
// the full document for PreviewExport, the rstrip'd one for CopyExportPreview.
func exportPreviewOf(plan exportPlan, text string) ExportPreview {
	return ExportPreview{
		Kind:          plan.kind,
		Title:         plan.title,
		Text:          text,
		SuggestedName: plan.suggestedName,
		Count:         plan.count,
		Skipped:       exportStrings(plan.skipped),
		SkippedNote:   skippedNote(plan.kind, plan.skipped),
		Entries:       exportStrings(plan.entries),
	}
}

// trimTrailingNewlines is str.rstrip("\n") (app.py:24069) — every trailing "\n",
// not just one, and no other whitespace.
func trimTrailingNewlines(text string) string {
	end := len(text)
	for end > 0 && text[end-1] == '\n' {
		end--
	}
	return text[:end]
}

// exportStrings keeps a nil slice out of the JSON, where it would become `null` and
// force every consumer to null-check a list.
func exportStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
