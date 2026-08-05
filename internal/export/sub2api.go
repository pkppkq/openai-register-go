package export

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/sessionconv"
)

// Errors of openai_record_from_refresh_payload (app.py:5457, 5464, 5466).
var (
	ErrRefreshMissingAccessToken  = errors.New("刷新 RT 后缺少 access_token")
	ErrRefreshMissingRefreshToken = errors.New("刷新 RT 后缺少有效 refresh_token")
)

// Sub2APISelection ports the gate of export_sub2api /
// _start_sub2api_export_with_accounts (app.py:24484-24496): the selection must
// be non-empty, and only accounts that already hold an openai_rt are exported.
//
// The RT back-fill in between (_ensure_export_accounts_have_rt) is a network
// task owned by the UI; it re-enters here through the export-sub2api-ready
// event (app.py:24479).
func Sub2APISelection(accounts []models.MailAccount) ([]models.MailAccount, error) {
	if len(accounts) == 0 {
		return nil, ErrNoSelection
	}
	authorized := withRefreshToken(accounts)
	if len(authorized) == 0 {
		return nil, ErrNoAuthorizedRT
	}
	return authorized, nil
}

// Sub2APIExportEmail ports app.py:24527:
//
//	export_email = f"({prefix}){account.email}" if prefix else account.email
//
// Note the prefix is NOT re-stripped here — the caller already stripped it at
// app.py:24511 — and an all-whitespace prefix is therefore truthy and gets its
// own parentheses, unlike account_export_line which strips first.
func Sub2APIExportEmail(namePrefix, email string) string {
	if namePrefix == "" {
		return email
	}
	return "(" + namePrefix + ")" + email
}

// RecordFromRefreshPayload ports openai_record_from_refresh_payload
// (app.py:5454-5477): it turns one refresh-token response into the auth record
// BuildSub2APIAccount consumes.
//
// The caller must first overwrite payload["refresh_token"] with the account's
// stored RT (app.py:24526) and pass Sub2APIExportEmail's result as emailAddr
// (app.py:24527-24528). `now` fills datetime.now(timezone.utc) for
// last_refresh; pass the zero time for the real clock.
func RecordFromRefreshPayload(emailAddr string, payload map[string]any, now time.Time) (map[string]any, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	accessToken := pyStrOr(payload["access_token"])
	if accessToken == "" {
		return nil, ErrRefreshMissingAccessToken
	}
	accessClaims := openai.DecodeJWTPayload(accessToken)
	accessAuth := openai.GetNestedRecord(accessClaims, "https://api.openai.com/auth")
	accountID := openai.FirstNonEmpty(accessAuth["chatgpt_account_id"], accessAuth["account_id"])
	exp := pyIntOrZero(accessClaims["exp"])
	refreshToken := pyStrOr(payload["refresh_token"])
	if !sessionconv.IsOpenAIRefreshToken(refreshToken) {
		return nil, ErrRefreshMissingRefreshToken
	}
	if accountID == "" {
		// DIVERGENCE: Python interpolates the raw dict repr of access_claims
		// (app.py:5466). Go has no dict repr, so the claims are rendered as a
		// sorted key list — the message text differs, the failure does not.
		return nil, fmt.Errorf("access_token 中缺少 account_id: %s", claimsHint(accessClaims))
	}
	expired := ""
	if exp != 0 {
		expired = pyISOFormatZ(time.Unix(exp, 0).UTC())
	}
	return map[string]any{
		"access_token":  accessToken,
		"account_id":    accountID,
		"email":         emailAddr,
		"expired":       expired,
		"id_token":      pyStrOr(payload["id_token"]),
		"last_refresh":  pyISOFormatZ(now.UTC()),
		"plan_type":     openai.FirstNonEmpty(accessAuth["chatgpt_plan_type"]),
		"refresh_token": refreshToken,
		"type":          "codex",
	}, nil
}

// Sub2API ports the write of _export_sub2api_worker (app.py:24531-24533) — the
// sub2api button's payload — together with the save dialog of
// _start_sub2api_export_with_accounts (app.py:24500-24504).
//
// Unlike the other exports this one has NO preview dialog: Python picks the
// path up front and the worker writes straight to it. `records` are
// RecordFromRefreshPayload results, in account order; `now` is exported_at.
func Sub2API(records []map[string]any, now time.Time) (Document, error) {
	if len(records) == 0 {
		return Document{}, ErrNoSub2APIRecords
	}
	text, err := dumpJSON(sessionconv.BuildSub2APIExport(records, now))
	if err != nil {
		return Document{}, err
	}
	return Document{
		Title:            "导出 sub2api JSON",
		Text:             text,
		File:             NativeNewlineBytes(text),
		DefaultExtension: ".sub2api.json",
		FileTypes:        sub2apiFileTypes,
		Count:            len(records),
	}, nil
}

// pyISOFormatZ is datetime.isoformat().replace("+00:00", "Z") for a UTC
// datetime (app.py:5471, 5473): microseconds are printed only when non-zero,
// and then always with six digits.
func pyISOFormatZ(t time.Time) string {
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05") + "Z"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "Z"
}

// pyIntOrZero is `int(value or 0)` over JWT claim values.
func pyIntOrZero(v any) int64 {
	if pyFalsy(v) {
		return 0
	}
	text := pyStrOr(v)
	if text == "" {
		return 0
	}
	var whole int64
	if _, err := fmt.Sscanf(strings.SplitN(text, ".", 2)[0], "%d", &whole); err != nil {
		return 0
	}
	return whole
}

// claimsHint renders JWT claims deterministically for the account_id error.
func claimsHint(claims map[string]any) string {
	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return "{" + strings.Join(keys, ", ") + "}"
}
