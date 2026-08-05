package export

import (
	"strconv"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/sessionconv"
)

// Raw ports export_selected_raw (app.py:24406-24416) — the 导出选中Raw button.
//
// Every selected account is emitted, authorized or not; the line shape is
// account_export_line, which sessionconv already owns. namePrefix is
// self.export_name_prefix.get().strip() (app.py:24410); AccountExportLine
// strips it again, so an unstripped value is harmless.
func Raw(accounts []models.MailAccount, namePrefix string) (Document, error) {
	if len(accounts) == 0 {
		return Document{}, ErrNoSelection
	}
	text := sessionconv.AccountExportText(accounts, namePrefix)
	return newTextDocument("导出选中Raw", text, len(accounts)), nil
}

// Authorized ports export_authorized / _finish_export_authorized
// (app.py:24098-24116) — the 已授权 button.
//
// It is Raw filtered to `if account.openai_rt` (app.py:24107). The RT
// back-fill that export_authorized runs first
// (_ensure_export_accounts_have_rt, app.py:24102) is a network task; the UI
// layer runs it and calls back in here with the refreshed accounts, exactly
// like the export-authorized-ready event (app.py:24475).
func Authorized(accounts []models.MailAccount, namePrefix string) (Document, error) {
	if len(accounts) == 0 {
		return Document{}, ErrNoSelection
	}
	authorized := withRefreshToken(accounts)
	if len(authorized) == 0 {
		return Document{}, ErrNoAuthorizedRT
	}
	text := sessionconv.AccountExportText(authorized, namePrefix)
	return newTextDocument("导出已授权邮箱", text, len(authorized)), nil
}

// AuthorizedEmailRT ports export_authorized_email_rt /
// _finish_export_authorized_email_rt (app.py:24118-24136) — the 邮箱 RT button.
//
// One "email----openai_rt" line per authorized account (app.py:24131), plus the
// trailing newline. No name prefix: this format has no raw line to graft onto.
func AuthorizedEmailRT(accounts []models.MailAccount) (Document, error) {
	if len(accounts) == 0 {
		return Document{}, ErrNoSelection
	}
	authorized := withRefreshToken(accounts)
	if len(authorized) == 0 {
		return Document{}, ErrNoAuthorizedRT
	}
	lines := make([]string, 0, len(authorized))
	for _, account := range authorized {
		lines = append(lines, account.Email+"----"+account.OpenaiRT)
	}
	text := strings.Join(lines, "\n") + "\n"
	return newTextDocument("导出邮箱----RT", text, len(authorized)), nil
}

// withRefreshToken is `[a for a in accounts if a.openai_rt]` (app.py:24107,
// 24127, 24493). Python's truthiness on a str field is just "non-empty" — no
// strip, so a whitespace-only RT passes here and is written out as-is.
func withRefreshToken(accounts []models.MailAccount) []models.MailAccount {
	out := make([]models.MailAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.OpenaiRT != "" {
			out = append(out, account)
		}
	}
	return out
}

// MissingRTPrompt ports the askyesno body of _ensure_export_accounts_have_rt
// (app.py:24446-24449): the confirmation shown before an export silently
// authorizes the accounts that have no RT yet.
//
// Returns "" when nothing is missing, i.e. when Python takes the early
// `return False` at app.py:24442 and no dialog appears at all.
func MissingRTPrompt(missing []models.MailAccount) string {
	if len(missing) == 0 {
		return ""
	}
	head := missing
	if len(head) > 12 {
		head = head[:12]
	}
	emails := make([]string, 0, len(head))
	for _, account := range head {
		emails = append(emails, account.Email)
	}
	preview := strings.Join(emails, "\n")
	if len(missing) > 12 {
		preview += "\n... 另有 " + strconv.Itoa(len(missing)-12) + " 个"
	}
	return "选中邮箱中有 " + strconv.Itoa(len(missing)) + " 个没有 RT，将先自动授权获取 RT 后再导出。\n" +
		preview + "\n\n是否继续？"
}

// MissingRT is `[a for a in accounts if not a.openai_rt]` (app.py:24440), the
// input MissingRTPrompt describes and the list the UI must authorize first.
func MissingRT(accounts []models.MailAccount) []models.MailAccount {
	var out []models.MailAccount
	for _, account := range accounts {
		if account.OpenaiRT == "" {
			out = append(out, account)
		}
	}
	return out
}

// SkippedNote ports the skip summary appended to three log lines
// (app.py:24353, 24375, 24404):
//
//	f"{prefix}跳过 {n} 个: {', '.join(skipped[:5])}" + (f" 等 {n} 个" if n > 5 else "")
//
// prefix is "Session 转换" (copy/export) or "Session 转换 ZIP". Returns "" for
// an empty list, matching Python's `if skipped:` guard.
func SkippedNote(prefix string, skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	head := skipped
	if len(head) > 5 {
		head = head[:5]
	}
	note := prefix + "跳过 " + strconv.Itoa(len(skipped)) + " 个: " + strings.Join(head, ", ")
	if len(skipped) > 5 {
		note += " 等 " + strconv.Itoa(len(skipped)) + " 个"
	}
	return note
}
