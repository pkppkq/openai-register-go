package logs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Expected values were produced by running the verbatim Python bodies of
// _normalize_log_message / _log_record_tag / _infer_log_email (app.py:18485,
// 18562, 18407) on CPython 3.12 over these inputs.
func TestClassifyMatchesPython(t *testing.T) {
	cases := []struct {
		in_   string
		norm  string
		tag   string
		email string
	}{
		{in_: "登录成功", norm: "[认证] 登录成功", tag: "log_success", email: ""},
		// leading [email] is stripped from the text but the surrounding blanks
		// are stripped first, so the bracket still matches.
		{in_: "  [a@b.com]  代理检测失败  ", norm: "[代理] 代理检测失败", tag: "log_error", email: ""},
		{in_: "[a@b.com] 已保存", norm: "[系统] 已保存", tag: "log_success", email: "a@b.com"},
		// _infer_log_email does NOT strip first: one leading space and the line
		// silently becomes a global line, though the prefix is still deleted.
		{in_: " [a@b.com] 已保存", norm: "[系统] 已保存", tag: "log_success", email: ""},
		{in_: "[系统]已启动", norm: "[系统]已启动", tag: "", email: ""},
		{in_: "[Session] token", norm: "[Session] token", tag: "", email: ""},
		{in_: "proxy 出口 JP", norm: "[代理] proxy 出口 JP", tag: "", email: ""},
		// "paypal 扩展" (支付窗口) is checked before plain "paypal" (支付链接).
		{in_: "paypal 扩展已加载", norm: "[支付窗口] paypal 扩展已加载", tag: "", email: ""},
		{in_: "paypal 长链获取中", norm: "[支付链接] paypal 长链获取中", tag: "", email: ""},
		// "session" outranks "paypal": ladder order, not map order.
		{in_: "Session accesstoken paypal", norm: "[Session] Session accesstoken paypal", tag: "", email: ""},
		{in_: "支付窗口 paypal checkout", norm: "[支付窗口] 支付窗口 paypal checkout", tag: "", email: ""},
		{in_: "HTTP 502 Bad gateway", norm: "[系统] HTTP 502 Bad gateway", tag: "log_attention", email: ""},
		{in_: "1502 字节", norm: "[系统] 1502 字节", tag: "", email: ""},
		{in_: "5025", norm: "[系统] 5025", tag: "", email: ""},
		// Python's \d is Unicode Nd, so a full-width digit also suppresses 502.
		{in_: "５502", norm: "[系统] ５502", tag: "", email: ""},
		{in_: "502", norm: "[系统] 502", tag: "log_attention", email: ""},
		// error beats attention: both 错误 and 等待/超时 are present.
		{in_: "错误：等待超时", norm: "[系统] 错误：等待超时", tag: "log_error", email: ""},
		{in_: "等待重试", norm: "[系统] 等待重试", tag: "log_attention", email: ""},
		{in_: "Call log:\n  - waiting for locator\n  - retry", norm: "[系统] Call log: - waiting for locator - retry", tag: "", email: ""},
		// NBSP + ideographic space: Go's RE2 \s would match none of these.
		{in_: " 　[a@b.com] 代理异常 ", norm: "[代理] 代理异常", tag: "log_error", email: ""},
		{in_: "Call log: abc　def", norm: "[系统] Call log: abc def", tag: "", email: ""},
		// a space inside the bracket disqualifies it as an address prefix.
		{in_: "[a b@c] hello", norm: "[系统] [a b@c] hello", tag: "", email: ""},
		{in_: "[a@b] [c@d] x", norm: "[系统] [c@d] x", tag: "", email: "a@b"},
		// empty input still gets a module prefix, trailing space included.
		{in_: "", norm: "[系统] ", tag: "", email: ""},
		{in_: "   ", norm: "[系统] ", tag: "", email: ""},
		{in_: "导出 sub2api 完成", norm: "[导出] 导出 sub2api 完成", tag: "log_success", email: ""},
		{in_: "oauth 登录中", norm: "[认证] oauth 登录中", tag: "", email: ""},
		// 代理 (proxy) is checked before 认证 (注册), so this is 代理.
		{in_: "注册中 proxy", norm: "[代理] 注册中 proxy", tag: "", email: ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in_); got != c.norm {
			t.Errorf("Normalize(%q) = %q, want %q", c.in_, got, c.norm)
		}
		if got := Tag(Normalize(c.in_)).TkTag(); got != c.tag {
			t.Errorf("Tag(Normalize(%q)) = %q, want %q", c.in_, got, c.tag)
		}
		if got := InferEmail(c.in_); got != c.email {
			t.Errorf("InferEmail(%q) = %q, want %q", c.in_, got, c.email)
		}
	}
}

// ORDER IS LOAD-BEARING. app.py:18496-18512 is an if/elif chain, so for every
// pair of rules the EARLIER one must win no matter which keyword of each is
// present. Exhaustive over the cross product of the whole vocabulary; the same
// sweep run against the verbatim Python bodies produces zero mismatches, so a
// failure here means the ladder was reordered (or turned into a map).
func TestModuleLadderOrderIsLoadBearing(t *testing.T) {
	for i, early := range moduleRules {
		for _, late := range moduleRules[i+1:] {
			for _, a := range early.words {
				for _, b := range late.words {
					text := a + " " + b
					if got := Normalize(text); got != "["+early.module+"] "+text {
						t.Errorf("%q: %q must beat %q, got %q", text, early.module, late.module, got)
					}
				}
			}
		}
	}
	// Nothing matched at all -> the else branch of app.py:18513.
	if got := Normalize("nothing here"); got != "[系统] nothing here" {
		t.Errorf("fallback = %q", got)
	}
}

// Same exhaustive sweep for the severity chain, app.py:18567-18572, in both word
// orders (the ladder is over word lists, not positions in the sentence). The
// bare-502 test is last and loses to every keyword.
func TestTagLadderOrderIsLoadBearing(t *testing.T) {
	ladder := []struct {
		level Level
		words []string
	}{
		{LevelError, errorWords},
		{LevelSuccess, successWords},
		{LevelAttention, attentionWords},
	}
	for i, early := range ladder {
		for _, late := range ladder[i+1:] {
			for _, a := range early.words {
				for _, b := range late.words {
					for _, text := range []string{a + b, b + a} {
						if got := Tag(text); got != early.level {
							t.Errorf("%q: want %q, got %q", text, early.level, got)
						}
					}
				}
			}
		}
	}
	for _, l := range ladder {
		for _, w := range l.words {
			for _, text := range []string{w + " 502", "502 " + w} {
				if got := Tag(text); got != l.level {
					t.Errorf("%q: 502 must not outrank %q, got %q", text, l.level, got)
				}
			}
		}
	}
	if got := Tag("平平无奇"); got != LevelNormal {
		t.Errorf("fallback = %q", got)
	}
}

// DELIBERATE FALSE POSITIVES. Every case below is a real producer in app.py
// whose line is mis-coloured or mis-filed by the first-match-wins ladders, and
// every expectation was produced by running the verbatim Python bodies. They are
// pinned so nobody "fixes" the port: fixing them here would make the Go UI
// disagree with the Tk one it is diffed against.
func TestClassifyDeliberateFalsePositives(t *testing.T) {
	cases := []struct {
		why  string
		in_  string
		norm string
		tag  string
	}{
		{
			// app.py:10879. "未完成" contains the success word "完成", which is
			// tested before the attention word "手动" — a manual-intervention
			// prompt is painted green.
			why:  "app.py:10879 未完成 -> log_success",
			in_:  "[CF] 自动过盾未完成，请在浏览器中手动通过人机验证（最多 90s）…",
			norm: "[系统] [CF] 自动过盾未完成，请在浏览器中手动通过人机验证（最多 90s）…",
			tag:  "log_success",
		},
		{
			// app.py:16037. Same 未完成 trap, and the module is 认证 because
			// "登录" hits the last rule.
			why:  "app.py:16037 未完成 -> log_success",
			in_:  "自动登录未完成，浏览器窗口已保留；请在窗口里手动继续/输入验证码: boom",
			norm: "[认证] 自动登录未完成，浏览器窗口已保留；请在窗口里手动继续/输入验证码: boom",
			tag:  "log_success",
		},
		{
			// app.py:18621. A benign "we already have the link, ignoring the
			// failure" notice is painted as an error because it quotes 失败.
			why:  "app.py:18621 忽略后续失败 -> log_error",
			in_:  "已有长链结果，忽略后续失败状态: 提取长链失败",
			norm: "[支付链接] 已有长链结果，忽略后续失败状态: 提取长链失败",
			tag:  "log_error",
		},
		{
			// Same line with a different interpolated status: 代理 is the FIRST
			// module rule, so quoting "代理耗尽" moves a payment-link message
			// into the proxy module.
			why:  "app.py:18621 with 代理耗尽 -> module 代理, not 支付链接",
			in_:  "已有长链结果，忽略后续失败状态: 代理耗尽",
			norm: "[代理] 已有长链结果，忽略后续失败状态: 代理耗尽",
			tag:  "log_error",
		},
		{
			// app.py:17634. Double hit: an informational start-up line is filed
			// under 代理 rather than 认证 and coloured as an error, both because
			// of words inside a parenthetical.
			why:  "app.py:17634 预检失败 in an info line -> 代理 + log_error",
			in_:  "认证代理池已启用（来源: 注册动态代理池；按账号尝试时取用，预检失败会自动换下一个）",
			norm: "[代理] 认证代理池已启用（来源: 注册动态代理池；按账号尝试时取用，预检失败会自动换下一个）",
			tag:  "log_error",
		},
		{
			// app.py:13323. 失败 outranks 成功 even though 成功 comes first in
			// the sentence — the ladder is over word lists, not positions.
			why:  "app.py:13323 播放成功...失败 -> log_error",
			in_:  "播放成功提示音失败: boom",
			norm: "[系统] 播放成功提示音失败: boom",
			tag:  "log_error",
		},
		{
			// Sibling of the 未见封禁邮件 case pinned in internal/accounts: the
			// substring 邮件 files a ban-scan result under the mailbox module.
			why:  "封禁邮件 -> module 邮箱",
			in_:  "未见封禁邮件",
			norm: "[邮箱] 未见封禁邮件",
			tag:  "",
		},
		{
			// app.py:18493 tests startswith on the ORIGINAL text while the rules
			// test the lower-cased copy, so a lower-case "[session]" is not
			// recognised as a prefix and gets a second one stacked on it.
			why:  "app.py:18493 known-prefix check is case sensitive",
			in_:  "[session] token",
			norm: "[Session] [session] token",
			tag:  "",
		},
	}
	for _, c := range cases {
		if got := Normalize(c.in_); got != c.norm {
			t.Errorf("%s: Normalize(%q) = %q, want %q", c.why, c.in_, got, c.norm)
		}
		if got := Tag(Normalize(c.in_)).TkTag(); got != c.tag {
			t.Errorf("%s: Tag = %q, want %q", c.why, got, c.tag)
		}
	}
}

// app.py:18495 lowers with str.lower(), whose FULL case mapping turns U+0130
// into "i" + U+0307. strings.ToLower produces a bare "i" and would therefore
// complete the "ipinfo" keyword that Python leaves broken.
func TestPythonLowerFullMapping(t *testing.T) {
	if got := pyLower("IPİNFO 出错"); got != "ipi̇nfo 出错" {
		t.Fatalf("pyLower = %q", got)
	}
	if got := strings.ToLower("IPİNFO 出错"); got != "ipinfo 出错" {
		t.Fatalf("fixture assumes strings.ToLower collapses the dot, got %q", got)
	}
	if got := Normalize("IPİNFO 出错"); got != "[系统] IPİNFO 出错" {
		t.Errorf("dotted capital I must NOT match ipinfo: %q", got)
	}
	if got := Normalize("IPINFO 出错"); got != "[代理] IPINFO 出错" {
		t.Errorf("plain IPINFO must match ipinfo: %q", got)
	}
	if got := EmailKey(" İ@X.COM "); got != "i̇@x.com" {
		t.Errorf("EmailKey = %q", got)
	}
}

// app.py:14006-14008. Colours are copied verbatim and the table stays ordered.
func TestLevelStyles(t *testing.T) {
	want := []LevelStyle{
		{LevelError, "log_error", "#b91c1c"},
		{LevelSuccess, "log_success", "#15803d"},
		{LevelAttention, "log_attention", "#1d4ed8"},
	}
	if len(LevelStyles) != len(want) {
		t.Fatalf("LevelStyles len = %d", len(LevelStyles))
	}
	for i, w := range want {
		if LevelStyles[i] != w {
			t.Errorf("LevelStyles[%d] = %+v, want %+v", i, LevelStyles[i], w)
		}
		if got := w.Level.Color(); got != w.Color {
			t.Errorf("%s.Color() = %q, want %q", w.Level, got, w.Color)
		}
		if got := w.Level.TkTag(); got != w.TkTag {
			t.Errorf("%s.TkTag() = %q, want %q", w.Level, got, w.TkTag)
		}
	}
	// Normal lines are inserted without a tag (app.py:18553-18554).
	if LevelNormal.Color() != "" || LevelNormal.TkTag() != "" {
		t.Errorf("normal must carry no tag/colour")
	}
}

// The module returned alongside the text must equal the bracket that is on it,
// including when the caller already supplied a known prefix.
func TestClassifyModule(t *testing.T) {
	cases := map[string]string{
		"登录成功":            "认证",
		"[Session] token": "Session",
		"[系统]已启动":         "系统",
		"随便":              "系统",
		"imap 连接":         "邮箱",
		"短信验证码":           "手机",
	}
	for in, want := range cases {
		if _, got := Classify(in); got != want {
			t.Errorf("Classify(%q) module = %q, want %q", in, got, want)
		}
	}
}

// app.py:18490-18491 — the cut is len()/slice on a str, i.e. code points.
// Cutting bytes would split a 3-byte CJK rune and emit U+FFFD.
func TestCallLogTruncationIsRuneBased(t *testing.T) {
	long := "Call log:" + strings.Repeat("中文", 400)
	got := Normalize(long)
	want := "[系统] " + "Call log:" + strings.Repeat("中文", 174) + "..."
	if got != want {
		t.Fatalf("truncated text mismatch:\n got %q\nwant %q", got, want)
	}
	// 5 runes of module prefix + the 360-rune body.
	if n := utf8.RuneCountInString(got); n != 365 {
		t.Fatalf("rune length = %d, want 365", n)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("truncation split a rune: %q", got)
	}

	// Exactly 360 runes is NOT truncated (the test is `> 360`).
	exact := "Call log:" + strings.Repeat("中", 351)
	if utf8.RuneCountInString(exact) != 360 {
		t.Fatalf("bad fixture length %d", utf8.RuneCountInString(exact))
	}
	if got := Normalize(exact); got != "[系统] "+exact {
		t.Fatalf("360 runes must survive untouched, got %q", got)
	}
}

// The whitespace collapse fires only for "Call log:" or a multi-line message;
// an ordinary long single-line message keeps its runs and its full length.
func TestCollapseOnlyForCallLogOrNewline(t *testing.T) {
	plain := "普通消息   带   空格 " + strings.Repeat("尾", 400)
	got := Normalize(plain)
	if !strings.Contains(got, "普通消息   带   空格") {
		t.Errorf("plain message must not be collapsed: %q", got[:40])
	}
	if utf8.RuneCountInString(got) <= 365 {
		t.Errorf("plain message must not be truncated, len=%d", utf8.RuneCountInString(got))
	}

	multi := strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 100)
	if got := Normalize(multi); strings.Contains(got, "\n") {
		t.Errorf("newline must be collapsed: %q", got)
	}
}

func TestEmailKeyAndRoute(t *testing.T) {
	if got := EmailKey("  A@B.COM  "); got != "a@b.com" {
		t.Errorf("EmailKey = %q", got)
	}
	// U+001C..U+001F are whitespace to Python's str.strip() but not to
	// Go's unicode.IsSpace, so strings.TrimSpace would leave them behind.
	if got := EmailKey("\x1ca@b.com\x1f"); got != "a@b.com" {
		t.Errorf("EmailKey with C0 separators = %q", got)
	}
	// explicit address wins over the bracket
	if got := Route("[x@y] hi", "A@B"); got != "a@b" {
		t.Errorf("Route explicit = %q", got)
	}
	if got := Route("[x@y] hi", "  "); got != "x@y" {
		t.Errorf("Route inferred = %q", got)
	}
	if got := Route("hi", ""); got != "" {
		t.Errorf("Route global = %q", got)
	}
}
