package sessionconv

import (
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// AccountExportLine ports account_export_line (app.py:1878-1898) — the
// 导出选中Raw / 导出已授权邮箱 line format.
//
// Base is the account's stored raw import line; when there is none it is
// rebuilt as email----password----client_id----refresh_token with the trailing
// separators trimmed. An optional display prefix is wrapped in parentheses and
// glued to the FIRST field only. Each of the five extras is appended as
// ----key=value, and only when that key is not already present anywhere in the
// line (so re-exporting an imported line never duplicates it).
func AccountExportLine(account models.MailAccount, namePrefix string) string {
	line := account.Raw
	if line == "" {
		// Python: "----".join([...]).rstrip("-") — rstrip removes every
		// trailing '-', which collapses the empty trailing fields.
		joined := strings.Join([]string{account.Email, account.Password, account.ClientID, account.RefreshToken}, "----")
		line = strings.TrimRight(joined, "-")
	}
	if line == "" {
		line = account.Email
	}
	// pyStrip, not TrimSpace: str(name_prefix or "").strip() also removes
	// U+001C-U+001F, so a prefix of "\x1cpfx\x1f" writes "(pfx)" in Python.
	prefix := pyStrip(namePrefix)
	if prefix != "" {
		parts := strings.SplitN(line, "----", 2)
		parts[0] = "(" + prefix + ")" + parts[0]
		line = strings.Join(parts, "----")
	}
	appendExtra := func(key, value string) {
		if value == "" {
			return
		}
		marker := "----" + key + "="
		if strings.Contains(line, marker) {
			return
		}
		line = line + marker + value
	}
	appendExtra("rt_token", account.OpenaiRT)
	appendExtra("auth_phone", account.AuthPhoneNumber)
	appendExtra("auth_phone_sms_url", account.AuthPhoneSMSURL)
	appendExtra("receive_mailbox", account.ReceiveMailbox)
	appendExtra("mail_provider", account.MailProvider)
	return line
}

// AccountExportText joins the export lines and adds the trailing newline, the
// shape written by 导出选中Raw (app.py:24411) and 导出已授权邮箱
// (app.py:24111). An EMPTY account list still yields "\n" in Python
// ("".join(...) + "\n"); callers gate on a non-empty selection first.
func AccountExportText(accounts []models.MailAccount, namePrefix string) string {
	lines := make([]string, 0, len(accounts))
	for _, account := range accounts {
		lines = append(lines, AccountExportLine(account, namePrefix))
	}
	return strings.Join(lines, "\n") + "\n"
}

// IsOpenAIRefreshToken ports is_openai_refresh_token (app.py:5449-5451).
func IsOpenAIRefreshToken(value string) bool {
	text := pyStrip(value)
	return strings.HasPrefix(text, "rt_") || strings.HasPrefix(text, "rt.")
}

// CPAGate is the decision half of _start_cpa_rt_refresh_for_conversion
// (app.py:24168-24197).
type CPAGate struct {
	// Refresh is true when the caller must run an RT refresh pass BEFORE
	// building the document, and re-enter the copy/export/zip action afterwards.
	Refresh bool
	// Refreshable lists the accounts that hold a usable OpenAI refresh token.
	Refreshable []models.MailAccount
	// Note is the log line Python emits when the format is CPA but no account
	// has a usable RT ("...将继续使用现有 Access Token"). Empty otherwise.
	Note string
}

// CPARefreshGate ports the pure part of _start_cpa_rt_refresh_for_conversion
// (app.py:24168-24182): only the CPA format pre-refreshes, and only when at
// least one selected account has an `rt_`/`rt.` token. sessionRT supplies
// session_results[email]["openai_rt"]; the account's own OpenaiRT wins.
//
// The impure half (the running-task guard, the proxy chain, the worker thread)
// belongs to the task registry, not here.
func CPARefreshGate(outputFormat string, accounts []models.MailAccount, sessionRT map[string]string) CPAGate {
	if pyLower(pyStrip(outputFormat)) != "cpa" {
		return CPAGate{}
	}
	if len(accounts) == 0 {
		// Python returns True here (nothing selected -> abort the action).
		return CPAGate{Refresh: true}
	}
	var refreshable []models.MailAccount
	for _, account := range accounts {
		token := pyFirstNonEmpty(account.OpenaiRT, sessionRT[account.Email])
		if IsOpenAIRefreshToken(token) {
			refreshable = append(refreshable, account)
		}
	}
	if len(refreshable) == 0 {
		return CPAGate{Note: "选中账号没有有效 OpenAI RT，CPA 将继续使用现有 Access Token"}
	}
	return CPAGate{Refresh: true, Refreshable: refreshable}
}
