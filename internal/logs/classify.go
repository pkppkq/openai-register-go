// Package logs ports the Tk log classifier/router (UI_SPEC G25).
//
// Source of truth: app.py
//
//	_log_email_key        18404
//	_infer_log_email      18407
//	_coerce_log_event     18443
//	_store_log_record     18458
//	_normalize_log_message 18485
//	_append_log_record    18516
//	_append_log_records   18523
//	_log_record_tag       18562
//	_format_log_record    18575
//	tag_configure colours 14006
package logs

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ring buffer bounds, app.py:58-59.
const (
	MaxLogRecordsPerView = 2000
	MaxTotalLogRecords   = 10000
)

// callLogMaxRunes / callLogKeepRunes: app.py:18490-18491 truncates at 360 with
// text[:357] + "..." — Python slices by code point, so this MUST be rune-based;
// a byte slice would split the Chinese text these messages are full of.
const (
	callLogMaxRunes  = 360
	callLogKeepRunes = 357
)

// pySpaceClass is Python's whitespace set, which is what both str.strip() and
// re's \s match for str objects (Py_UNICODE_ISSPACE). Go differs twice:
// RE2's \s is ASCII-only [\t\n\f\r ] (no \v, no NBSP, no ideographic space) and
// Go's unicode.IsSpace omits U+001C-U+001F. Rendered page text carries NBSP and
// U+3000, so using bare \s here would silently stop collapsing "Call log:" dumps.
const pySpaceClass = `\t\n\v\f\r\x{001C}-\x{001F}\x{0085}\p{Z}`

var (
	// app.py:18408 — anchored at the RAW message, deliberately NOT stripped first,
	// so a message with a leading space routes to the global pane even though
	// _normalize_log_message still deletes its [email] prefix. Reproduced as-is.
	inferEmailRE = regexp.MustCompile(`^\[([^\]` + pySpaceClass + `]+@[^\]` + pySpaceClass + `]+)\]`)

	// app.py:18487
	leadingEmailRE = regexp.MustCompile(`^\[[^\]` + pySpaceClass + `]+@[^\]` + pySpaceClass + `]+\][` + pySpaceClass + `]*`)

	// app.py:18489 re.sub(r"\s+", " ", text)
	spaceRunRE = regexp.MustCompile(`[` + pySpaceClass + `]+`)
)

// KnownModules are the module tags recognised as an already-present prefix,
// app.py:18492. A slice, not a map: order is irrelevant for membership here but
// the caller-visible list is shown in this order in the UI spec.
var KnownModules = []string{"系统", "代理", "认证", "邮箱", "手机", "Session", "支付链接", "支付窗口", "导出"}

// moduleRules is the keyword ladder of app.py:18496-18512. It is a SLICE because
// the ladder is first-match-wins: "支付窗口" must beat "支付链接" ("paypal" hits
// both lists) and "session" must beat "支付链接". A Go map would randomise that.
var moduleRules = []struct {
	module string
	words  []string
}{
	{"代理", []string{"proxy", "代理", "出口", "ipinfo", "stripe="}},
	{"邮箱", []string{"imap", "邮箱", "邮件", "验证码邮件"}},
	{"手机", []string{"手机号", "电话验证", "短信"}},
	{"Session", []string{"session", "access token", "accesstoken"}},
	{"支付窗口", []string{"支付窗口", "chromium 窗口", "paypal 扩展"}},
	{"支付链接", []string{"长链", "支付链接", "checkout", "paypal", "gopay", "apple pay"}},
	{"导出", []string{"导出", "sub2api"}},
	{"认证", []string{"注册", "登录", "认证", "oauth", "rt 获取"}},
}

// DefaultModule is the fallback of app.py:18513.
const DefaultModule = "系统"

// Level is the severity tag of _log_record_tag (app.py:18562). The Tk names are
// kept available via TkTag; the wire names match the `log` event contract in
// UI_SPEC §4.2 (error/success/attention/normal).
type Level string

const (
	LevelNormal    Level = "normal"
	LevelError     Level = "error"
	LevelSuccess   Level = "success"
	LevelAttention Level = "attention"
)

// TkTag returns the Tk text tag the Python used; "" for normal (app.py:18573).
func (l Level) TkTag() string {
	switch l {
	case LevelError:
		return "log_error"
	case LevelSuccess:
		return "log_success"
	case LevelAttention:
		return "log_attention"
	default:
		return ""
	}
}

// Foreground colours of the three Tk tags, app.py:14006-14008 (also pinned in
// UI_SPEC S13). Normal lines carry no tag and keep the widget default.
const (
	ColorError     = "#b91c1c"
	ColorSuccess   = "#15803d"
	ColorAttention = "#1d4ed8"
)

// Color is the foreground app.py:14006-14008 configured for this severity; ""
// for normal.
func (l Level) Color() string {
	switch l {
	case LevelError:
		return ColorError
	case LevelSuccess:
		return ColorSuccess
	case LevelAttention:
		return ColorAttention
	default:
		return ""
	}
}

// LevelStyle is one row of the Tk tag table.
type LevelStyle struct {
	Level Level  `json:"level"`
	TkTag string `json:"tag"`
	Color string `json:"color"`
}

// LevelStyles is that table in the order app.py:14006-14008 configures it. A
// SLICE, not a map: the frontend renders these into an ordered stylesheet and Go
// map iteration would reshuffle them on every run.
var LevelStyles = []LevelStyle{
	{LevelError, "log_error", ColorError},
	{LevelSuccess, "log_success", ColorSuccess},
	{LevelAttention, "log_attention", ColorAttention},
}

// Ordered ladders of _log_record_tag, app.py:18564-18566.
var (
	errorWords     = []string{"失败", "异常", "错误", "超时", "不可用", "拒绝", "耗尽"}
	successWords   = []string{"成功", "完成", "已获得", "已保存", "已提取", "已复制"}
	attentionWords = []string{"等待", "重试", "手动", "暂停", "风控"}
)

// pyIsSpace mirrors Py_UNICODE_ISSPACE. strings.TrimSpace is NOT equivalent:
// it uses unicode.IsSpace, which does not treat U+001C-U+001F as space.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1C && r <= 0x1F)
}

// pyStrip is Python's str.strip() with no argument (app.py:18486).
func pyStrip(s string) string {
	return strings.TrimFunc(s, pyIsSpace)
}

// pyLower is Python's str.lower() (app.py:18405, 18495). strings.ToLower applies
// Unicode *simple* case mapping while Python applies the *full* mapping, and the
// two disagree on U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE: Python yields the
// two-rune "i̇", Go yields a bare "i". That is load-bearing here because the
// bare "i" can complete a keyword — "IPİNFO" would match the "ipinfo" rule in Go
// but not in Python — so the expansion is done by hand.
//
// DIVERGENCE: str.lower() also applies the Final_Sigma rule ("ΑΣ".lower() ==
// "ας") where Go always produces σ. Not reproduced: no module keyword and no
// e-mail key routed through here contains a sigma, so no outcome can differ.
func pyLower(s string) string {
	if strings.ContainsRune(s, 'İ') {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

// EmailKey is _log_email_key (app.py:18404-18405): strip then lower-case.
func EmailKey(email string) string {
	return pyLower(pyStrip(email))
}

// InferEmail is _infer_log_email (app.py:18407-18409): pull the routing address
// out of a leading "[user@host]" bracket of the raw, unstripped message.
func InferEmail(message string) string {
	m := inferEmailRE.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return EmailKey(m[1])
}

// Route is _coerce_log_event's address resolution (app.py:18453-18455): an
// explicit email wins, otherwise the leading bracket decides; "" means the line
// belongs to the global pane.
func Route(message, email string) string {
	key := EmailKey(email)
	if key == "" {
		key = InferEmail(message)
	}
	return key
}

// Normalize is _normalize_log_message (app.py:18485-18514).
func Normalize(message string) string {
	text, _ := Classify(message)
	return text
}

// Classify returns the normalized (module-prefixed) message plus the module tag
// that ended up on it, so the `log` event can carry `module` without re-parsing.
func Classify(message string) (text, module string) {
	text = pyStrip(message)
	text = leadingEmailRE.ReplaceAllString(text, "")

	// app.py:18488 — the collapse is gated on the RAW (post-strip) text still
	// containing a newline, so it must be tested before any collapsing happens.
	if strings.Contains(text, "Call log:") || strings.Contains(text, "\n") {
		text = spaceRunRE.ReplaceAllString(text, " ")
		if utf8.RuneCountInString(text) > callLogMaxRunes {
			text = string([]rune(text)[:callLogKeepRunes]) + "..."
		}
	}

	for _, known := range KnownModules {
		if strings.HasPrefix(text, "["+known+"]") {
			return text, known
		}
	}

	// app.py:18495 — the keyword ladder tests the lower-cased text, but the known
	// prefix check above tests the original, so "[Session] x" is recognised as an
	// existing prefix and "[session] x" is not (it gets a second "[Session] ").
	lowered := pyLower(text)
	module = DefaultModule
	for _, rule := range moduleRules {
		if containsAny(lowered, rule.words) {
			module = rule.module
			break
		}
	}
	// app.py:18514 — note the empty-message case still yields "[系统] ".
	return "[" + module + "] " + text, module
}

func containsAny(haystack string, words []string) bool {
	for _, w := range words {
		if strings.Contains(haystack, w) {
			return true
		}
	}
	return false
}

// Tag is _log_record_tag (app.py:18562-18574). It runs on the NORMALIZED
// message, i.e. after the module prefix has been attached.
func Tag(message string) Level {
	if containsAny(message, errorWords) {
		return LevelError
	}
	if containsAny(message, successWords) {
		return LevelSuccess
	}
	if containsAny(message, attentionWords) || hasBare502(message) {
		return LevelAttention
	}
	return LevelNormal
}

// hasBare502 is re.search(r"(?<!\d)502(?!\d)", message). RE2 has no lookaround,
// so the neighbour test is manual. Python's \d is the Unicode Nd category (so
// "５502" does NOT match) — unicode.IsDigit is the same category.
func hasBare502(message string) bool {
	for offset := 0; ; {
		idx := strings.Index(message[offset:], "502")
		if idx < 0 {
			return false
		}
		start := offset + idx
		end := start + 3
		before, _ := utf8.DecodeLastRuneInString(message[:start])
		after, _ := utf8.DecodeRuneInString(message[end:])
		leftOK := start == 0 || !unicode.IsDigit(before)
		rightOK := end == len(message) || !unicode.IsDigit(after)
		if leftOK && rightOK {
			return true
		}
		offset = start + 1
	}
}
