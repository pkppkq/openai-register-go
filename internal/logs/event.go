package logs

// The producer half of the log path: how a line is addressed to an account, how
// the ("log", ...) event is coerced back into (message, email), and the two pane
// titles the classifier's output is drawn under.
//
// Source of truth: app.py
//
//	prefix convention f"[{email}] " 11922, 12667
//	_emit_log                        18434
//	_account_logger                  18437
//	_coerce_log_event                18444
//	log_label / 全局日志 titles       13996, 13998, 18582
//	_render_log_view label text      18584

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// Prefix is the per-account addressing convention of app.py:11922-11923 and
// 12667-12668: producers that cannot pass an address alongside the message
// (app.py:17711, 17725-17726, 21989 put a bare string on the queue) glue it onto
// the front instead, and _infer_log_email digs it back out at drain time.
//
// The address is embedded verbatim — Python does not strip or lower-case it
// here, and _log_email_key normalises only on the way out.
func Prefix(email, message string) string {
	if email == "" {
		return message
	}
	return "[" + email + "] " + message
}

// SplitPrefix undoes Prefix the way the drain path does: the address comes from
// _infer_log_email on the RAW message (app.py:18408) while the bracket is cut
// from the STRIPPED message (app.py:18487). The asymmetry is Python's, and it is
// why " [a@b] x" loses its prefix but still lands in the global pane.
//
// The returned text is not yet module-prefixed; Classify does that.
func SplitPrefix(message string) (email, text string) {
	return InferEmail(message), leadingEmailRE.ReplaceAllString(pyStrip(message), "")
}

// Emit is the ("log", {...}) sink, i.e. self.events.put in _emit_log
// (app.py:18434-18435). The email it receives is already EmailKey-normalised.
type Emit func(message, email string)

// LogFunc is the `log_account` / `log` closure every worker is handed.
type LogFunc func(message string)

// Printf is the f-string spelling of the Python call sites (e.g. app.py:16037).
func (f LogFunc) Printf(format string, args ...any) {
	if f == nil {
		return
	}
	f(fmt.Sprintf(format, args...))
}

// EmitLog is _emit_log (app.py:18434-18435). str(message) happens at the call
// site in Go; the _log_email_key normalisation happens here, so the event
// payload always carries a key and never a raw address.
func EmitLog(emit Emit, message, email string) {
	if emit == nil {
		// Python's self.events is constructed with the App and is never None; a
		// Go Emit is nil until it is wired, and a nil call in a worker goroutine
		// would take the process down.
		return
	}
	emit(message, EmailKey(email))
}

// AccountLogger is _account_logger (app.py:18437-18442).
//
// DIVERGENCE: Python accepted either a MailAccount or a raw address and read
// .email off the former. Go callers pass the address; taking models.MailAccount
// here would make the classifier depend on the model layer for one field read.
func AccountLogger(emit Emit, email string) LogFunc {
	return func(message string) { EmitLog(emit, message, email) }
}

// Payload is the dict form of the log event, app.py:18435 and 13323.
type Payload struct {
	Message string `json:"message,omitempty"`
	Msg     string `json:"msg,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Entry resolves the payload to (message, email) exactly as _coerce_log_event
// does for its dict branch (app.py:18448-18455): "message" first, "msg" as the
// `or` fallback, then the leading-bracket inference when no address was given.
func (p Payload) Entry() Entry {
	message := p.Message
	if message == "" {
		message = p.Msg
	}
	return Entry{Message: message, Email: Route(message, p.Email)}
}

// CoerceMap is the same branch for a payload that arrived as an untyped map
// (a JSON round-trip through the Wails bridge).
//
// It cannot go through Payload.Entry: Python evaluates the `or` chain on the RAW
// values and only then calls str() (app.py:18448), so a payload of
// {"message": 0, "msg": "x"} logs "x". Stringifying each field first and then
// testing for "" — which is all Payload.Entry can do with three string fields —
// answers "0", because str(0) is not empty. The same trap decides routing:
// {"email": 0} is a GLOBAL line in Python and was an account line called "0"
// here.
func CoerceMap(payload map[string]any) Entry {
	message := pyStrOr(orChain(payload["message"], payload["msg"]))
	return Entry{Message: message, Email: Route(message, pyStrOr(payload["email"]))}
}

// pyFalsy reports whether Python would treat v as falsy in an `or` chain. Note
// this is not "empty after strip": " " is truthy, "" is falsy.
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
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

// orChain mirrors `a or b`: the first truthy operand, nil when none is.
func orChain(vals ...any) any {
	for _, v := range vals {
		if !pyFalsy(v) {
			return v
		}
	}
	return nil
}

// pyStr mirrors str(v) for a JSON-decoded value.
//
// DIVERGENCE, deliberate: Python's str() of a dict or list is its repr
// ("{'a': 1}", "[1]"), which needs a full repr port — quoting rules, nested
// containers, float formatting — to reproduce. A stand-in is used instead,
// exactly as internal/accounts does, because no producer puts a container on
// the log queue: every call site stringifies first (app.py:18435, 18442). What
// matters at this call site and IS exact is whether the value is falsy, since
// that decides which field wins the `or` and whether the line is routed at all.
//
// json.Unmarshal collapses Python's int/float distinction into float64, so an
// integral float prints as "200" where Python's str(200.0) is "200.0". Decode
// with json.Decoder.UseNumber and the json.Number branch below is exact.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
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
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case map[string]any:
		return "{...}"
	case []any:
		return "[...]"
	}
	return "?"
}

// pyStrOr mirrors the `str(x or "")` idiom of app.py:18405/18448.
func pyStrOr(v any) string {
	if pyFalsy(v) {
		return ""
	}
	return pyStr(v)
}

// CoerceText is the non-dict branch, app.py:18450-18455: ("log", message) and
// ("log", message, email) collapse to the same call because an empty address
// falls through to _infer_log_email either way.
func CoerceText(message, email string) Entry {
	return Entry{Message: message, Email: Route(message, email)}
}

// Pane titles, app.py:13996 / 13998 / 18584-18586. Verbatim, full-width colon
// included.
const (
	GlobalPaneTitle       = "全局日志"
	AccountPaneTitleEmpty = "选中账户日志：未选择账户"
)

// AccountPaneTitle is the log_label text of app.py:18583-18586.
//
// displayEmail is _selected_log_email_text (app.py:18425-18432): the account
// row's original-cased address, falling back to the lower-cased key when no row
// matches, and "" when nothing is selected. Resolving that needs the account
// list, which this package deliberately does not know about — the caller passes
// the resolved text.
func AccountPaneTitle(displayEmail string) string {
	if displayEmail == "" {
		return AccountPaneTitleEmpty
	}
	return "选中账户日志：" + displayEmail
}

// SelectedEmailText is the lookup half of _selected_log_email_text
// (app.py:18428-18432): find the account whose key matches and show its address
// as typed, otherwise show the key itself.
func SelectedEmailText(key string, accountEmails []string) string {
	if key == "" {
		return ""
	}
	for _, email := range accountEmails {
		if EmailKey(email) == key {
			return email
		}
	}
	return key
}
