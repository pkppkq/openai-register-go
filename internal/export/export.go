// Package export ports the export / conversion actions of app.py — gap G23 in
// docs/UI_SPEC.md §5.3, the half that turns loaded state into a file.
//
// Every function here is pure: it takes the already-loaded accounts, the
// session_results payloads and (for sub2api) the already-refreshed auth
// records, and returns the exact bytes plus the dialog metadata Python passes
// to filedialog. No file I/O, no dialogs, no clock reads that the caller cannot
// pin — that all belongs to the UI layer.
//
// The seven conversion formats themselves are NOT re-implemented here;
// internal/sessionconv owns convert_chatgpt_session_record,
// build_session_conversion_document, build_sub2api_export, account_export_line
// and session_conversion_zip_entry_name, and this package composes them.
//
// # Byte fidelity
//
// Three Python behaviours decide the output bytes and each is reproduced
// explicitly:
//
//   - json.dumps(..., ensure_ascii=False, indent=2) + "\n" — non-ASCII stays
//     literal and <, > & are NOT escaped. All JSON here goes through
//     sessionconv.DumpJSON, which wraps an Encoder with SetEscapeHTML(false);
//     a plain json.Marshal would emit < / & and corrupt an exported
//     session that Python writes as `a&b`.
//   - Python dicts are insertion-ordered and encoding/json sorts map keys, so
//     every JSON object whose key order is visible in the file is built with
//     sessionconv.OrderedMap and every ordered list is a slice.
//   - Path.write_text(text, encoding="utf-8") opens the file in TEXT mode with
//     newline=None, so CPython translates every "\n" to os.linesep — CRLF on
//     Windows. Document.Text is the in-memory / clipboard / preview form (LF);
//     Document.File is what actually lands on disk. zipfile.writestr does NOT
//     translate, so ZIP entries stay LF.
package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/sessionconv"
)

// FileType is one entry of a Tk `filetypes=[(label, pattern)]` list. The UI
// layer maps it onto whatever its own save dialog wants.
type FileType struct {
	Label   string
	Pattern string
}

// Text/All is the default filetypes list of _preview_and_save_text
// (app.py:24079: `filetypes or [("Text", "*.txt"), ("All", "*.*")]`).
var textFileTypes = []FileType{{"Text", "*.txt"}, {"All", "*.*"}}

// JSON/All is what the .json exports pass (app.py:24159, 24367).
var jsonFileTypes = []FileType{{"JSON", "*.json"}, {"All", "*.*"}}

// app.py:24503.
var sub2apiFileTypes = []FileType{{"sub2api JSON", "*.sub2api.json"}, {"JSON", "*.json"}, {"All", "*.*"}}

// app.py:24391.
var zipFileTypes = []FileType{{"ZIP", "*.zip"}, {"All", "*.*"}}

// Document is one finished export: the text Python previews, the bytes Python
// writes, and the save-dialog arguments it uses.
type Document struct {
	// Title is the dialog title, verbatim from app.py.
	Title string
	// Text is the in-memory string: what the preview pane shows and what
	// copy_selected_session_conversion puts on the clipboard. Always LF.
	Text string
	// File is Text as Path.write_text encodes it on this OS — UTF-8, no BOM,
	// with "\n" translated to os.linesep. Write these bytes, not Text.
	File []byte
	// DefaultExtension is filedialog's defaultextension.
	DefaultExtension string
	// FileTypes is filedialog's filetypes.
	FileTypes []FileType
	// SuggestedName is filedialog's initialfile. Python only sets one for the
	// ZIP export (app.py:24390); it is "" everywhere else.
	SuggestedName string
	// Count is the N of Python's "已导出 {N} 个 ..." log line.
	Count int
	// Skipped holds the per-account skip reasons, in account order.
	Skipped []string
}

// Empty-selection / nothing-to-write outcomes. Python shows each of these with
// messagebox.showwarning and returns; the strings are the warning bodies,
// verbatim, so the UI can surface them unchanged.
var (
	// app.py:24421 — _selected_accounts_for_export with an empty tree selection.
	ErrNoSelection = errors.New("请先在左侧选择要导出的邮箱，可多选")
	// app.py:24109 / 24129 / 24495 — every selected account lacks openai_rt.
	ErrNoAuthorizedRT = errors.New("没有可导出的已授权 RT")
	// app.py:24152.
	ErrNoSessionJSON = errors.New("选中的邮箱没有可导出的 Session JSON")
	// app.py:24336 / 24384.
	ErrNoConvertibleToken = errors.New("选中的邮箱没有可转换的 Access Token")
	// app.py:24532 — raised as RuntimeError inside the sub2api worker, not shown
	// as a dialog; it reaches the log as "导出 sub2api 失败: ...".
	ErrNoSub2APIRecords = errors.New("没有可导出的 sub2api 记录")
	// app.py:24436 — _selected_authorized_accounts, the shared gate other
	// RT-consuming actions use before they reach an export.
	ErrNoAuthorizedSelection = errors.New("选中的邮箱里没有已授权 RT")
)

// nativeLineSeparator is Python's os.linesep for the running platform. Go has
// no os.linesep, and CPython hard-codes "\r\n" on Windows and "\n" elsewhere.
var nativeLineSeparator = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

// NativeNewlineBytes is Path.write_text(text, encoding="utf-8") — UTF-8 with no
// BOM, and every "\n" translated to os.linesep because write_text opens the
// file in text mode with newline=None (app.py:24115, 24135, 24163, 24371,
// 24415, 24533).
//
// The translation is unconditional, exactly like io.TextIOWrapper's: a "\r\n"
// already present in the text becomes "\r\r\n" on Windows. No export input
// carries a bare CR today (JSON escapes it as \r), so this only matters for
// hand-crafted account fields.
func NativeNewlineBytes(text string) []byte {
	if nativeLineSeparator == "\n" {
		return []byte(text)
	}
	return []byte(strings.ReplaceAll(text, "\n", nativeLineSeparator))
}

// newTextDocument builds the .txt shape of _preview_and_save_text
// (app.py:24055: default_extension=".txt", filetypes defaulted at 24079).
func newTextDocument(title, text string, count int) Document {
	return Document{
		Title:            title,
		Text:             text,
		File:             NativeNewlineBytes(text),
		DefaultExtension: ".txt",
		FileTypes:        textFileTypes,
		Count:            count,
	}
}

// newJSONDocument builds the .json shape (app.py:24158-24159, 24366-24367).
func newJSONDocument(title, text string, count int, skipped []string) Document {
	return Document{
		Title:            title,
		Text:             text,
		File:             NativeNewlineBytes(text),
		DefaultExtension: ".json",
		FileTypes:        jsonFileTypes,
		Count:            count,
		Skipped:          skipped,
	}
}

// dumpJSON is `json.dumps(value, ensure_ascii=False, indent=2) + "\n"`.
//
// sessionconv.DumpJSON already handles ensure_ascii=False and turns off Go's
// HTML escaping of <, > and &, but encoding/json ALSO escapes U+2028 and U+2029
// unconditionally (there is no encoder flag for it) and json.dumps does not.
// Those two code points reach an export whenever a ChatGPT session note or an
// account label carries one, and the difference is visible in the file, so the
// escapes are undone here.
func dumpJSON(value any) (string, error) {
	text, err := sessionconv.DumpJSON(value)
	if err != nil {
		return "", err
	}
	return restoreLineSeparators(text), nil
}

// restoreLineSeparators rewrites the six-character escapes \u2028 / \u2029
// that encoding/json emits back into the literal code points.
//
// It walks escape pairs rather than doing a blind ReplaceAll: in the encoder's
// output a literal backslash is written as `\\`, so the naive replacement would
// corrupt the JSON string `\\u2028` — an escaped backslash followed by
// five ordinary characters — into a backslash plus a real line separator.
func restoreLineSeparators(text string) string {
	if !strings.Contains(text, `\u202`) {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '\\' {
			out.WriteByte(text[i])
			i++
			continue
		}
		if i+6 <= len(text) && text[i+1] == 'u' && text[i+2] == '2' && text[i+3] == '0' && text[i+4] == '2' {
			if text[i+5] == '8' {
				out.WriteRune('\u2028')
				i += 6
				continue
			}
			if text[i+5] == '9' {
				out.WriteRune('\u2029')
				i += 6
				continue
			}
		}
		// Any other escape: copy the backslash and the character it escapes as a
		// unit, so a `\\` cannot be re-read as the start of an escape.
		out.WriteByte(text[i])
		if i+1 < len(text) {
			out.WriteByte(text[i+1])
		}
		i += 2
	}
	return out.String()
}

// pyIsSpace mirrors Python's str.isspace() for one rune: unicode.IsSpace plus
// the C0 information separators U+001C..U+001F, which Python counts as
// whitespace and Go does not.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip mirrors str.strip() (app.py:24047, 24146, 24296).
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyFalsy mirrors Python truthiness over the shapes json.Unmarshal produces:
// None / "" / 0 / False / [] / {} are falsy, everything else is truthy.
func pyFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	case json.Number:
		f, err := t.Float64()
		return err == nil && f == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// pyStrOr mirrors `str(value or "")` (app.py:24047, 24146, 24296).
func pyStrOr(v any) string {
	if pyFalsy(v) {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return t.String()
	case float64:
		// json.loads yields an int for an integral JSON number, so Python's str()
		// prints neither ".0" nor an exponent.
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}
