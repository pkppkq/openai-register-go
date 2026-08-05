package export

import (
	"archive/zip"
	"bytes"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/sessionconv"
)

// SessionResults is self.session_results: email -> payload dict. It is the same
// loose shape accounts.LinkView.SessionResults carries, so the UI can hand its
// map straight over.
type SessionResults map[string]any

// payload is `self.session_results.get(email, {})` narrowed to a dict.
//
// DIVERGENCE: at app.py:24295 Python does NOT isinstance-check the payload, so
// a non-dict entry raises AttributeError and kills the export; at 24146 it
// does. Go cannot raise, so a non-dict payload is treated as {} in both places
// — a strictly more forgiving result for state that only the Python app could
// have produced.
func (r SessionResults) payload(email string) map[string]any {
	if r == nil {
		return map[string]any{}
	}
	if p, ok := r[email].(map[string]any); ok {
		return p
	}
	return map[string]any{}
}

// Sessions ports export_selected_sessions (app.py:24138-24166) — the
// 选中 Session button.
//
// Output is a JSON array of {"email", "session_json"} objects in selection
// order, where session_json is the raw stripped STRING, not embedded JSON
// (app.py:24148). Accounts with no session are collected into Skipped, which is
// the `missing` list of app.py:24150 — Python logs their count but still
// exports the rest.
func Sessions(accounts []models.MailAccount, results SessionResults) (Document, error) {
	if len(accounts) == 0 {
		return Document{}, ErrNoSelection
	}
	rows := []any{}
	var missing []string
	for _, account := range accounts {
		sessionJSON := pyStrip(pyStrOr(results.payload(account.Email)["session_json"]))
		if sessionJSON != "" {
			rows = append(rows, sessionconv.NewOrderedMap().
				Set("email", account.Email).
				Set("session_json", sessionJSON))
			continue
		}
		missing = append(missing, account.Email)
	}
	if len(rows) == 0 {
		return Document{}, ErrNoSessionJSON
	}
	text, err := dumpJSON(rows)
	if err != nil {
		return Document{}, err
	}
	return newJSONDocument("导出选中Session", text, len(rows), missing), nil
}

// ConversionEntry is one (email, converted) pair of app.py:24325.
type ConversionEntry struct {
	Email     string
	Converted sessionconv.Converted
}

// ConversionSet is the 5-tuple returned by _selected_session_conversions
// (app.py:24283-24328).
type ConversionSet struct {
	// Format is the normalized session_convert_format key.
	Format string
	// Label is SESSION_CONVERT_FORMATS[Format].
	Label string
	// Entries are the successfully converted accounts, in selection order.
	Entries []ConversionEntry
	// Skipped holds "email" (no access token) or "email: err" (conversion
	// failure) in selection order.
	Skipped []string
	// Now is the single UTC instant stamped into every conversion.
	Now time.Time
}

// Conversions ports _selected_session_conversions (app.py:24283-24328): it
// merges each account's stored session_json with the live session_results
// payload and runs the record through sessionconv.
//
// The merge order at app.py:24310-24323 matters — the payload's values override
// the parsed session_json, and each field falls back through first_non_empty in
// the exact order Python lists. Pass the zero time for
// `datetime.now(timezone.utc)`.
func Conversions(accounts []models.MailAccount, results SessionResults, outputFormat string, now time.Time) (ConversionSet, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if len(accounts) == 0 {
		// app.py:24286 returns ("", "", [], [], now) and every caller bails out
		// silently — the warning already came from _selected_accounts_for_export.
		return ConversionSet{Now: now}, ErrNoSelection
	}
	format := sessionconv.NormalizeFormat(outputFormat)
	set := ConversionSet{Format: format, Label: sessionconv.FormatLabel(format), Now: now}
	for _, account := range accounts {
		payload := results.payload(account.Email)
		sessionJSON := pyStrip(pyStrOr(payload["session_json"]))
		accessSummary, _ := payload["access_summary"].(map[string]any)
		if accessSummary == nil {
			accessSummary = map[string]any{}
		}
		sessionRecord := map[string]any{}
		if sessionJSON != "" {
			// A non-object or malformed body is swallowed (app.py:24304).
			if parsed, err := sessionconv.ParseSessionRecord([]byte(sessionJSON)); err == nil {
				for k, v := range parsed {
					sessionRecord[k] = v
				}
			}
		}
		accessToken := openai.FirstNonEmpty(payload["access_token"], sessionRecord["accessToken"], sessionRecord["access_token"])
		if accessToken == "" {
			set.Skipped = append(set.Skipped, account.Email)
			continue
		}
		// app.py:24310-24323 — dict.update, so these always win over the parsed
		// session_json and each one collapses to "" when its whole chain is empty.
		sessionRecord["accessToken"] = accessToken
		sessionRecord["access_token"] = accessToken
		sessionRecord["idToken"] = openai.FirstNonEmpty(payload["id_token"], sessionRecord["idToken"], sessionRecord["id_token"])
		sessionRecord["id_token"] = openai.FirstNonEmpty(payload["id_token"], sessionRecord["id_token"], sessionRecord["idToken"])
		sessionRecord["refresh_token"] = openai.FirstNonEmpty(payload["openai_rt"], account.OpenaiRT)
		sessionRecord["openai_rt"] = openai.FirstNonEmpty(payload["openai_rt"], account.OpenaiRT)
		sessionRecord["email"] = openai.FirstNonEmpty(account.Email, sessionRecord["email"])
		sessionRecord["label"] = openai.FirstNonEmpty(account.Email, sessionRecord["label"])
		sessionRecord["plan_type"] = openai.FirstNonEmpty(payload["plan_type"], payload["chatgpt_plan_type"], accessSummary["plan_type"], accessSummary["backend_plan_type"], sessionRecord["plan_type"])
		sessionRecord["chatgpt_plan_type"] = openai.FirstNonEmpty(payload["chatgpt_plan_type"], payload["plan_type"], accessSummary["plan_type"], accessSummary["backend_plan_type"], sessionRecord["chatgpt_plan_type"])
		sessionRecord["account_id"] = openai.FirstNonEmpty(payload["account_id"], payload["chatgpt_account_id"], sessionRecord["account_id"])
		sessionRecord["chatgpt_account_id"] = openai.FirstNonEmpty(payload["chatgpt_account_id"], payload["account_id"], sessionRecord["chatgpt_account_id"])

		converted, err := sessionconv.ConvertChatGPTSessionRecord(sessionRecord, account.Email, now)
		if err != nil {
			set.Skipped = append(set.Skipped, account.Email+": "+err.Error())
			continue
		}
		set.Entries = append(set.Entries, ConversionEntry{Email: account.Email, Converted: converted})
	}
	return set, nil
}

// converted flattens Entries for build_session_conversion_document.
func (s ConversionSet) converted() []sessionconv.Converted {
	out := make([]sessionconv.Converted, 0, len(s.Entries))
	for _, entry := range s.Entries {
		out = append(out, entry.Converted)
	}
	return out
}

// SessionConversionText ports _selected_session_conversion_text
// (app.py:24330-24340) — the string shared by the 复制/导出转换 buttons and by
// copy_selected_session_conversion's clipboard write (app.py:24349).
func (s ConversionSet) SessionConversionText() (string, error) {
	if len(s.Entries) == 0 {
		return "", ErrNoConvertibleToken
	}
	return dumpJSON(sessionconv.BuildSessionConversionDocument(s.converted(), s.Format, s.Now))
}

// SessionConversion ports export_selected_session_conversion
// (app.py:24355-24375) — the 导出转换 button.
//
// The dialog title is f"导出 Session 转换 {label}" (app.py:24364), so it varies
// with the selected format.
func SessionConversion(accounts []models.MailAccount, results SessionResults, outputFormat string, now time.Time) (Document, error) {
	set, err := Conversions(accounts, results, outputFormat, now)
	if err != nil {
		return Document{}, err
	}
	text, err := set.SessionConversionText()
	if err != nil {
		return Document{}, err
	}
	return newJSONDocument("导出 Session 转换 "+set.Label, text, len(set.Entries), set.Skipped), nil
}

// ZipEntry is one member of the conversion ZIP.
type ZipEntry struct {
	// Name is session_conversion_zip_entry_name's result.
	Name string
	// Text is the entry body: one single-account document, LF, trailing "\n".
	Text string
	// Data is Text as UTF-8. zipfile.writestr does NOT translate newlines, so
	// unlike Document.File this stays LF on every platform.
	Data []byte
}

// Archive is the ZIP export of export_selected_session_conversion_zip
// (app.py:24377-24404).
type Archive struct {
	Title            string
	SuggestedName    string
	DefaultExtension string
	FileTypes        []FileType
	Entries          []ZipEntry
	Count            int
	Skipped          []string
}

// ZipSuggestedName ports the initialfile of app.py:24386:
//
//	f"session-conversion-{output_format}-{datetime.now().strftime('%Y%m%d-%H%M%S')}.zip"
//
// datetime.now() there is naive LOCAL time, a second call distinct from the
// tz-aware `now` stamped into the documents — but the same instant, so the UTC
// `now` is converted to local here rather than taking a second parameter.
func ZipSuggestedName(outputFormat string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return "session-conversion-" + outputFormat + "-" + now.Local().Format("20060102-150405") + ".zip"
}

// SessionConversionZIP ports export_selected_session_conversion_zip
// (app.py:24377-24404) — the 导出ZIP button: one document per account, each
// built as a single-element conversion (app.py:24398) so non-sub2api formats
// take build_session_conversion_document's `values[0]` branch and emit a bare
// object rather than a one-element array.
//
// Entry names are de-duplicated across the archive by the shared used_names set
// (app.py:24395).
func SessionConversionZIP(accounts []models.MailAccount, results SessionResults, outputFormat string, now time.Time) (Archive, error) {
	set, err := Conversions(accounts, results, outputFormat, now)
	if err != nil {
		return Archive{}, err
	}
	if len(set.Entries) == 0 {
		return Archive{}, ErrNoConvertibleToken
	}
	usedNames := map[string]bool{}
	entries := make([]ZipEntry, 0, len(set.Entries))
	for _, entry := range set.Entries {
		document := sessionconv.BuildSessionConversionDocument([]sessionconv.Converted{entry.Converted}, set.Format, set.Now)
		text, err := dumpJSON(document)
		if err != nil {
			return Archive{}, err
		}
		entries = append(entries, ZipEntry{
			Name: sessionconv.SessionConversionZipEntryName(entry.Email, set.Format, usedNames),
			Text: text,
			Data: []byte(text),
		})
	}
	return Archive{
		Title:            "导出 Session 转换 ZIP " + set.Label,
		SuggestedName:    ZipSuggestedName(set.Format, set.Now),
		DefaultExtension: ".zip",
		FileTypes:        zipFileTypes,
		Entries:          entries,
		Count:            len(entries),
		Skipped:          set.Skipped,
	}, nil
}

// Bytes assembles the archive in memory: DEFLATE, one entry per account, in the
// order Python writes them (app.py:24396-24400).
//
// DIVERGENCE: the container is byte-comparable to Python's only in structure.
// CPython's zipfile compresses with zlib and stamps external_attr 0o600<<16,
// while Go uses compress/flate and its own attribute defaults, so the archive
// bytes differ even though every extracted entry is byte-identical. `modified`
// fills zipfile's `date_time = time.localtime()[:6]`; pass the zero time to use
// the current local time, as Python does.
func (a Archive) Bytes(modified time.Time) ([]byte, error) {
	if modified.IsZero() {
		modified = time.Now()
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range a.Entries {
		w, err := writer.CreateHeader(&zip.FileHeader{
			Name:     entry.Name,
			Method:   zip.Deflate,
			Modified: modified,
		})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(entry.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
