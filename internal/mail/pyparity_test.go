package mail

// pyparity_test.go — differential parity with app.py.
//
// EVERY expectation in this file was COMPUTED by executing the verbatim app.py
// line slice for the function under test over the input beside it (CPython
// 3.12, Unicode 15.0.0); none of it is hand-derived. The inputs are the ones
// that separated CPython from a natural Go spelling: non-ASCII decimal digits,
// NBSP and the other 24 Unicode spaces, the C0 information separators
// U+001C..U+001F, CJK text abutting ASCII (Python's `\b` sees no boundary
// there), Turkish dotted I, long s, sharp s, and percent-escapes net/url
// rejects.
//
// Regenerate by re-running the slices, not by editing an expectation: a
// "wrong-looking" value here is app.py's answer and the port must match it.

import "testing"

func TestPyParityNormalizeEmailAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{" ", ""},
		{"\x1f", ""},
		{"\x1f\x1f\x1fno-at-here\x1f\x1f\x1f", "no-at-here"},
		{"  a@b.com  ", "a@b.com"},
		{"<A@B.COM>", "A@B.COM"},
		{"\u201ca@b.com\u201d", "a@b.com"},
		{"Name <a@b.com>,", "a@b.com"},
		{"\x1ca@b.com\x1c", "a@b.com"},
		{"\u00a0a@b.com\u00a0", "a@b.com"},
		{"\u3000a@b.com\u3000", "a@b.com"},
		{"notanemail", "notanemail"},
		{"a@b", "a@b"},
		{"a@b.c", "a@b.c"},
		{"a@b.co", "a@b.co"},
		{"text a@b.com more c@d.org", "a@b.com"},
		{"....a@b.com....", "....a@b.com"},
		{"a@b.com...", "a@b.com"},
		{"<<a@b.com>>", "a@b.com"},
		{"mailto:a@b.com", "a@b.com"},
	}
	for _, c := range cases {
		if got := normalizeEmailAddress(c.in); got != c.want {
			t.Errorf("normalizeEmailAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The OTP path is where a non-ASCII digit actually arrives: an operator pastes
// a code, or the mail itself is rendered in a locale that uses them. RE2's `\d`
// is [0-9] and its `\b` is ASCII, so both halves of extract_openai_code had to
// be respelled.
func TestPyParityExtractOpenAICode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{" ", ""},
		{"Your OpenAI code is 123456", "123456"},
		{"verification code: 654321", "654321"},
		{"\u9a8c\u8bc1\u7801 135790", "135790"},
		{"\u767b\u5f55\u7801\uff1a246810", "246810"},
		{"code 123456 and 999999", "123456"},
		{"code\u00a0123456", "123456"},
		{"Your account code:\u00a0123456", "123456"},
		{"code \u0661\u0662\u0663\u0664\u0665\u0666", "\u0661\u0662\u0663\u0664\u0665\u0666"},
		{"code \uff11\uff12\uff13\uff14\uff15\uff16", "\uff11\uff12\uff13\uff14\uff15\uff16"},
		{"\u9a8c\u8bc1\u7801\uff11\uff12\uff13\uff14\uff15\uff16", "\uff11\uff12\uff13\uff14\uff15\uff16"},
		{"code \U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1\U0001d7d2\U0001d7d3", "\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1\U0001d7d2\U0001d7d3"},
		{"\u0661\u0662\u0663\u0664\u0665\u0666", "\u0661\u0662\u0663\u0664\u0665\u0666"},
		{"code \u0660\u0661\u0662\u0663\u0664\u0665 123456", "\u0660\u0661\u0662\u0663\u0664\u0665"},
		{"code\u0660\u0660\u0660123456", "\u0660\u0660\u0660123"},
		{"\u4e2d\u6587123456\u4e2d\u6587", ""},
		{"abc\u4e2d123456\u4e2ddef", ""},
		{"\u4e2d\u6587 123456 \u4e2d\u6587", "123456"},
		{"1234567", ""},
		{"12345", ""},
		{"abc123456def", ""},
		{"a123456", ""},
		{"123456a", ""},
		{"codexxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx123456", "123456"},
		{"codexxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx123456", ""},
		{"code\x0b123456", "123456"},
		{"code\x1f123456", "123456"},
		{"code\u2028123456", "123456"},
		{"code\u3000123456", "123456"},
		{"OpenAI 111111 ChatGPT 222222", "111111"},
		{"(123456)", "123456"},
		{"code=123456", "123456"},
		{"code-123456", "123456"},
		{"code \u2460\u2461\u2462\u2463\u2464\u2465", ""},
		{"no digits here", ""},
		{" 123456 ", "123456"},
		{"Verify\x0bx -x\x0b\u0667\u0662\u0661\u0662\u0668\u0660", "\u0667\u0662\u0661\u0662\u0668\u0660"},
		{"\u767b\u5f55\u7801 \n \x0b\u3000\u0664\u0663\u0666\u0669\u0665\u0668", "\u0664\u0663\u0666\u0669\u0665\u0668"},
	}
	for _, c := range cases {
		if got := extractOpenAICode(c.in); got != c.want {
			t.Errorf("extractOpenAICode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyParityHTMLToText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"<p>hi</p>", " hi "},
		{"<script>var a=1;</script>body", " body"},
		{"<SCRIPT>\nx\n</SCRIPT>tail", " tail"},
		{"<style>a{}</style>text", " text"},
		{"a&nbsp;b", "a\u00a0b"},
		{"a&amp;b", "a&b"},
		{"&#65;&#x42;", "AB"},
		{"&notarealentity;", "\u00acarealentity;"},
		{"a\u00a0b", "a b"},
		{"a\u2028b", "a b"},
		{"a\x0bb", "a b"},
		{"a\x1cb", "a b"},
		{"a\u3000b", "a b"},
		{"a \t\n b", "a b"},
		{"<div>a</div><div>b</div>", " a b "},
		{"<a href='http://x/'>l</a>", " l "},
		{"text&nbsp;&nbsp;text", "text\u00a0\u00a0text"},
		{"a\u0085b", "a b"},
		{"  ", " "},
		{"<p>&nbsp;</p>", " \u00a0 "},
		{"&lt;script&gt;", "<script>"},
	}
	for _, c := range cases {
		if got := htmlToText(c.in); got != c.want {
			t.Errorf("htmlToText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyParityExtractEmailAddressesForMatching(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"to: A@B.COM", []string{"a@b.com"}},
		{"<a@b.com>, <c@d.org>;", []string{"a@b.com", "c@d.org"}},
		{"a@b.com. ", []string{"a@b.com"}},
		{"\u017fam@b.com", []string{"sam@b.com"}},
		{"\u212a@b.com", []string{"k@b.com"}},
		{"\u0130@b.com", []string{"i\u0307@b.com"}},
		{"a@B\u00df.com", []string{}},
		{"a@b.com a@B.COM A@b.com", []string{"a@b.com"}},
		{"no emails", []string{}},
		{"user+tag@sub.domain.co.uk", []string{"user+tag@sub.domain.co.uk"}},
		{"\u90ae\u7bb1 zhang@x.com \u7ed3\u675f", []string{"zhang@x.com"}},
	}
	for _, c := range cases {
		got := extractEmailAddressesForMatching(c.in)
		if len(got) != len(c.want) {
			t.Errorf("extractEmailAddressesForMatching(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for _, want := range c.want {
			if !got[want] {
				t.Errorf("extractEmailAddressesForMatching(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// A rendered HTML mail carries `&nbsp;`, which html_to_text unescapes AFTER it
// collapses whitespace (app.py:6283) — so the ban markers reach this function
// with U+00A0 inside them and only a Unicode `\s+` puts them back together.
func TestPyParityIsOpenAIDeactivationNotice(t *testing.T) {
	cases := []struct {
		subject, from, body string
		want                bool
	}{
		{"", "noreply@openai.com", "account\u00a0has\u00a0been\u00a0deactivated", true},
		{"", "noreply@openai.com", "account has been deactivated", true},
		{"\u4e2d\u6587deactivated", "support@chatgpt.com", "openai stuff", false},
		{"openai\u4e2d\u6587suspended", "", "nothing relevant", false},
		{"Account deactivated", "noreply@openai.com", "", true},
		{"Account_deactivated", "support@chatgpt.com", "openai", false},
		{"ACCESS SUSPENDED", "noreply@openai.com", "", true},
		{"\u8d26\u53f7\u5df2\u505c\u7528", "support@chatgpt.com", "openai \u8d26\u53f7\u5df2\u505c\u7528", true},
		{"", "a@b.com", "account has been deactivated", false},
		{"Account   Terminated", "noreply@openai.com", "", true},
		{"openai", "", "you have violated our usage policies", true},
		{"chatgpt", "", "start\u00a0an\u00a0appeal", true},
	}
	for _, c := range cases {
		if got := isOpenAIDeactivationNotice(c.subject, c.from, c.body); got != c.want {
			t.Errorf("isOpenAIDeactivationNotice(%q, %q, %q) = %v, want %v", c.subject, c.from, c.body, got, c.want)
		}
	}
}

func TestPyParityOpenAIDeactivationNoticeMatchesAccount(t *testing.T) {
	cases := []struct {
		target, to, body string
		want             bool
	}{
		{"a@b.com", "", "contact z@b.com", false},
		{"\x1fa@b.com\x1f", "other@b.com", "contact z@b.com", false},
		{"\x1fa@b.com\x1f", "", "", true},
		{"a+1@b.com", "a+1@b.com", "", true},
		{"a+1@b.com", "other@b.com", "", false},
		{"A@B.COM", "<a@b.com>", "", true},
		{"noat", "a@b.com", "", false},
		{"a@b.com", "", "contact z@q.org", true},
		{"\u017fam@b.com", "sam@b.com", "", true},
	}
	for _, c := range cases {
		if got := openaiDeactivationNoticeMatchesAccount(c.target, c.to, c.body); got != c.want {
			t.Errorf("openaiDeactivationNoticeMatchesAccount(%q, %q, %q) = %v, want %v", c.target, c.to, c.body, got, c.want)
		}
	}
}

func TestPyParityOpenAIDeactivationNoticeSnippet(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"", 260, ""},
		{"short", 260, "short"},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 10, "aaaaaaa..."},
		{"a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0", 10, "a a a a..."},
		{"a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0a\u00a0", 260, "a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a a..."},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx   yyyyyyyyyy", 260, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx..."},
		{"                                                                                                                                                                                                                                                                                                            tail", 10, "tail"},
		{"\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587", 10, "\u4e2d\u6587\u4e2d\u6587\u4e2d\u6587\u4e2d..."},
		{"a  b\tc\nd", 260, "a b c d"},
		{"a\u2028b", 260, "a b"},
	}
	for _, c := range cases {
		if got := openaiDeactivationNoticeSnippet(c.in, c.limit); got != c.want {
			t.Errorf("openaiDeactivationNoticeSnippet(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestPyParityExtractLinksFromText(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"http://a/", []string{"http://a/"}},
		{"visit https://x.example/path?q=1 now", []string{"https://x.example/path?q=1"}},
		{"<a href=\"https://x.example/a\">x</a>", []string{"https://x.example/a"}},
		{"<a href='http://y/z'>y</a>", []string{"http://y/z"}},
		{"href=\"ftp://a/\"", []string{}},
		{"href=\"/rel\"", []string{}},
		{"https://a/b.", []string{"https://a/b"}},
		{"https://a/b,", []string{"https://a/b"}},
		{"https://a/b.,;)]}>", []string{"https://a/b"}},
		{"https://a/b\u00a0c", []string{"https://a/b"}},
		{"https://a/b\x0bc", []string{"https://a/b"}},
		{"https://a/b\x1cc", []string{"https://a/b"}},
		{"https://a/b\u2028c", []string{"https://a/b"}},
		{"https://a/b\u3000c", []string{"https://a/b"}},
		{"https://a/b&amp;c=1", []string{"https://a/b&c=1"}},
		{"&lt;https://a/b&gt;", []string{"https://a/b"}},
		{"HTTPS://A/B https://a/b", []string{"HTTPS://A/B"}},
		{"href=\"  https://a/x  \"", []string{"https://a/x"}},
		{"text https://a https://b https://a", []string{"https://a", "https://b"}},
	}
	for _, c := range cases {
		got := extractLinksFromText(c.in)
		if len(got) != len(c.want) {
			t.Errorf("extractLinksFromText(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("extractLinksFromText(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}

// urlsplit never raises and unquote decodes what it can; net/url refuses the
// whole URL on one bad escape and QueryUnescape turns "+" into a space. Both
// differences dropped invites Python clicks.
func TestPyParityIsChatGPTTeamInviteURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"https://chatgpt.com/invite/abc", true},
		{"https://chatgpt.com/team/join/x", true},
		{"https://admin.chatgpt.com/invitation?x=1", false},
		{"https://openai.com/join-team", true},
		{"https://evil.com/team/invite", false},
		{"https://chatgpt.com/k12-invite/x", false},
		{"https://chatgpt.com/teacher/invite", false},
		{"https://chatgpt.com/%zz/invite/team", true},
		{"https://chatgpt.com/invite%25team", true},
		{"https://chatgpt.com/invite%2Fteam", true},
		{"https://chatgpt.com/x?a=invite+team", true},
		{"https://chatgpt.com/x?a=invite%20team", true},
		{"https://chatgpt.com/%2525invite%2525team", true},
		{"https://chatgpt.com/invite/team#frag", true},
		{"https://CHATGPT.COM/INVITE/TEAM", true},
		{"https://chatgpt.com:443/invite/team", true},
		{"https://user:pw@chatgpt.com/invite/team", true},
		{"https://chatgpt.com/\u0130nvite/team", false},
		{"https://chatgpt.com/invite\u00df/team", true},
		{"https://chatgpt.comx/invite/team", false},
		{"https://chatgpt.com/join", true},
		{"https://chatgpt.com/a?b=%", false},
		{" https://chatgpt.com/invite/team ", true},
	}
	for _, c := range cases {
		if got := isChatGPTTeamInviteURL(c.in); got != c.want {
			t.Errorf("isChatGPTTeamInviteURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPyParityIsMicrosoftAccountSecurityInterrupt(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"Account Security Interrupt", true},
		{"ACCOUNT SECURITY INTERRUPT", true},
		{"collecting proof", true},
		{"found as compromised", true},
		{"nothing", false},
		{"FOUND AS COMPROM\u0130SED", false},
		{"found as compromi\u017fed", true},
	}
	for _, c := range cases {
		if got := isMicrosoftAccountSecurityInterrupt(c.in); got != c.want {
			t.Errorf("isMicrosoftAccountSecurityInterrupt(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The kind decides IMAP vs Graph. app.py:6511 is
// `str(claims.get("scp") or claims.get("scope") or "").strip()`, and the `or`
// chain tests the RAW value: a whitespace-only scp is TRUTHY, wins the chain and
// strips to "" — it does NOT fall through to scope.
func TestPyParityMicrosoftAccessTokenKind(t *testing.T) {
	cases := []struct {
		name                  string
		token                 string
		kind, audience, scope string
	}{
		{"", "", "unknown", "", ""},
		{"", "abc", "unknown", "", ""},
		{"", "a.b", "unknown", "", ""},
		{"{}", "eyJhbGciOiJub25lIn0.e30.sig", "unknown", "", ""},
		{"outlook", "eyJhbGciOiJub25lIn0.eyJhdWQiOiJodHRwczovL291dGxvb2sub2ZmaWNlLmNvbSJ9.sig", "imap", "https://outlook.office.com", ""},
		{"outlook upper + slashes", "eyJhbGciOiJub25lIn0.eyJhdWQiOiJIVFRQUzovL09VVExPT0suT0ZGSUNFLkNPTS8vIn0.sig", "imap", "HTTPS://OUTLOOK.OFFICE.COM//", ""},
		{"graph", "eyJhbGciOiJub25lIn0.eyJhdWQiOiJodHRwczovL2dyYXBoLm1pY3Jvc29mdC5jb20ifQ.sig", "graph", "https://graph.microsoft.com", ""},
		{"imap scope", "eyJhbGciOiJub25lIn0.eyJzY3AiOiJJTUFQLkFjY2Vzc0FzVXNlci5BbGwifQ.sig", "imap", "", "IMAP.AccessAsUser.All"},
		{"whitespace scp wins the or-chain", "eyJhbGciOiJub25lIn0.eyJzY3AiOiIgICAiLCJzY29wZSI6Ik1haWwuUmVhZCJ9.sig", "unknown", "", ""},
		{"falsy 0 scp falls through", "eyJhbGciOiJub25lIn0.eyJzY3AiOjAsInNjb3BlIjoiTWFpbC5SZWFkIn0.sig", "graph", "", "Mail.Read"},
		{"falsy False scp falls through", "eyJhbGciOiJub25lIn0.eyJzY3AiOmZhbHNlLCJzY29wZSI6Ik1haWwuUmVhZFdyaXRlIn0.sig", "graph", "", "Mail.ReadWrite"},
		{"list aud stringifies as a repr", "eyJhbGciOiJub25lIn0.eyJhdWQiOlsiaHR0cHM6Ly9ncmFwaC5taWNyb3NvZnQuY29tIl19.sig", "graph", "['https://graph.microsoft.com']", ""},
		{"list scope repr", "eyJhbGciOiJub25lIn0.eyJhdWQiOiJ4Iiwic2NwIjpbImEiLCJiIl19.sig", "unknown", "x", "['a', 'b']"},
		{"C0 separators are stripped", "eyJhbGciOiJub25lIn0.eyJhdWQiOiJcdTAwMWZodHRwczovL2dyYXBoLm1pY3Jvc29mdC5jb21cdTAwMWYifQ.sig", "graph", "https://graph.microsoft.com", ""},
		{"int aud", "eyJhbGciOiJub25lIn0.eyJhdWQiOjEyMzR9.sig", "unknown", "1234", ""},
		{"bool aud", "eyJhbGciOiJub25lIn0.eyJhdWQiOnRydWV9.sig", "unknown", "True", ""},
	}
	for _, c := range cases {
		kind, audience, scope := microsoftAccessTokenKind(c.token)
		if kind != c.kind || audience != c.audience || scope != c.scope {
			t.Errorf("microsoftAccessTokenKind(%s) = (%q, %q, %q), want (%q, %q, %q)",
				c.name, kind, audience, scope, c.kind, c.audience, c.scope)
		}
	}
}

func TestPyParityCloudMailTimestamp(t *testing.T) {
	cases := []struct {
		in   interface{}
		want float64
	}{
		{float64(0), 0.0},
		{float64(1700000000), 1700000000.0},
		{float64(1700000000000), 1700000000.0},
		{float64(1.5), 1.5},
		{"", 0.0},
		{"  ", 0.0},
		{"1700000000", 1700000000.0},
		{"\uff11\uff17\uff10\uff10\uff10\uff10\uff10\uff10\uff10\uff10", 1700000000.0},
		{"\x1f1700000000\x1f", 1700000000.0},
		{"2024-05-06T07:08:09Z", 1714979289.0},
		{"2024-05-06T07:08:09+02:00", 1714972089.0},
		{"2024-05-06T07:08:09", 1714979289.0},
		{"2024-05-06 07:08:09", 1714979289.0},
		{"2024/05/06 07:08:09", 1714979289.0},
		{"2024-05-06", 1714953600.0},
		{"2024-05-06T07:08", 1714979280.0},
		{"not a date", 0.0},
		{"2024-05-06T07:08:09.123456Z", 1714979289.123456},
	}
	for _, c := range cases {
		if got := cloudMailTimestamp(c.in); got != c.want {
			t.Errorf("cloudMailTimestamp(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// pyCaseFold / pyLower were verified against CPython over every code point in
// U+0000..U+10FFFF; these are the representatives of each rule.
func TestPyParityCaseFolds(t *testing.T) {
	cases := []struct{ in, fold, lower string }{
		{"", "", ""},
		{"ABC", "abc", "abc"},
		{"Stra\u00dfe", "strasse", "stra\u00dfe"},
		{"STRA\u1e9eE", "strasse", "stra\u00dfe"},
		{"\u017fam", "sam", "\u017fam"},
		{"\u0130stanbul", "i\u0307stanbul", "i\u0307stanbul"},
		{"\u00b5", "\u03bc", "\u00b5"},
		{"\u03c2", "\u03c3", "\u03c2"},
		{"\u13a0", "\u13a0", "\uab70"},
		{"\uab70", "\u13a0", "\uab70"},
		{"\ufb01n", "fin", "\ufb01n"},
		{"\u1f88", "\u1f00\u03b9", "\u1f80"},
		{"\u0587", "\u0565\u0582", "\u0587"},
		{"\u4e2d\u6587", "\u4e2d\u6587", "\u4e2d\u6587"},
	}
	for _, c := range cases {
		if got := pyCaseFold(c.in); got != c.fold {
			t.Errorf("pyCaseFold(%q) = %q, want %q", c.in, got, c.fold)
		}
		if got := pyLower(c.in); got != c.lower {
			t.Errorf("pyLower(%q) = %q, want %q", c.in, got, c.lower)
		}
	}
}

func TestPyParityStrip(t *testing.T) {
	cases := []struct{ in, strip, rstrip string }{
		{"", "", ""},
		{" x ", "x", " x"},
		{"\x1cx\x1f", "x", "\x1cx"},
		{"\x0bx\x0b", "x", "\x0bx"},
		{"\u0085x\u0085", "x", "\u0085x"},
		{"\u00a0x\u00a0", "x", "\u00a0x"},
		{"\u3000x\u3000", "x", "\u3000x"},
		{"\u2028x\u2029", "x", "\u2028x"},
		{"x\x1c\x1d", "x", "x"},
	}
	for _, c := range cases {
		if got := pyStrip(c.in); got != c.strip {
			t.Errorf("pyStrip(%q) = %q, want %q", c.in, got, c.strip)
		}
		if got := pyRStrip(c.in); got != c.rstrip {
			t.Errorf("pyRStrip(%q) = %q, want %q", c.in, got, c.rstrip)
		}
	}
}

// Nd is laid out as aligned runs of ten, and U+1D7CE..U+1D7FF packs FIVE of them
// into one range — walking back to the nearest non-digit answers 9 for the
// double-struck zero.
func TestPyParityDigitValue(t *testing.T) {
	cases := []struct {
		r     rune
		value int
		isNd  bool
	}{
		{0x0030, 0, true},
		{0x0039, 9, true},
		{0x0660, 0, true},
		{0x0669, 9, true},
		{0xFF10, 0, true},
		{0xFF19, 9, true},
		{0x0966, 0, true},
		{0x1D7CE, 0, true},
		{0x1D7D8, 0, true},
		{0x1D7FF, 9, true},
		{0x2460, 0, false},
		{0x0041, 0, false},
		{0x4E2D, 0, false},
	}
	for _, c := range cases {
		value, isNd := pyDigitValue(c.r)
		if isNd != c.isNd || (isNd && value != c.value) {
			t.Errorf("pyDigitValue(U+%04X) = (%d, %v), want (%d, %v)", c.r, value, isNd, c.value, c.isNd)
		}
	}
}
