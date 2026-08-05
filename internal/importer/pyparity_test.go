package importer

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

import (
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// A pasted phone in fullwidth or Arabic-Indic digits is a phone to Python, and
// a URL is cut short by a non-breaking space — both of which RE2's ASCII `\d`
// and `\S` got backwards.
func TestPyParityExtractExtras(t *testing.T) {
	cases := []struct {
		in                                                                 []string
		openaiRT, phone, smsURL, receiveMailbox, mailProvider, accountType string
	}{
		{[]string{}, "", "", "", "", "", ""},
		{[]string{""}, "", "", "", "", "", ""},
		{[]string{"   "}, "", "", "", "", "", ""},
		{[]string{"\x1f"}, "", "", "", "", "", ""},
		{[]string{"rt_token=rt_abc"}, "rt_abc", "", "", "", "", ""},
		{[]string{"RT_TOKEN=  rt_x  "}, "rt_x", "", "", "", "", ""},
		{[]string{"openai_rt=rt.zz"}, "rt.zz", "", "", "", "", ""},
		{[]string{"auth_phone=+8613800000000"}, "", "+8613800000000", "", "", "", ""},
		{[]string{"phone= +33 1 23 45 67 89 "}, "", "+33 1 23 45 67 89", "", "", "", ""},
		{[]string{"sms_url=https://s/1"}, "", "", "https://s/1", "", "", ""},
		{[]string{"receive_mailbox= <Main@Example.COM> "}, "", "", "", "Main@Example.COM", "", ""},
		{[]string{"mail_provider=CloudMail"}, "", "", "", "", "cloudmail", ""},
		{[]string{"mail_type=OUTLOOK"}, "", "", "", "", "outlook", ""},
		{[]string{"mail_provider=gmail"}, "", "", "", "", "", ""},
		{[]string{"mail_provider=CLOUDMA\u0130L"}, "", "", "", "", "", ""},
		{[]string{"account_type=PLUS"}, "", "", "", "", "", "plus"},
		{[]string{"type=team"}, "", "", "", "", "", "team"},
		{[]string{"type=pro"}, "", "", "", "", "", ""},
		{[]string{"account_type= free "}, "", "", "", "", "", "free"},
		{[]string{"\u0130NBOX=x@y.com"}, "", "", "", "", "", ""},
		{[]string{"+123456789https://sms.example/x"}, "", "+123456789", "https://sms.example/x", "", "", ""},
		{[]string{"+1 (555) 010-9999https://s/2"}, "", "+1 (555) 010-9999", "https://s/2", "", "", ""},
		{[]string{"0123456"}, "", "0123456", "", "", "", ""},
		{[]string{"+12345"}, "", "+12345", "", "", "", ""},
		{[]string{"+123456"}, "", "+123456", "", "", "", ""},
		{[]string{"https://bare.example/x"}, "", "", "https://bare.example/x", "", "", ""},
		{[]string{"https://x.example/a\u00a0b"}, "", "", "", "", "", ""},
		{[]string{"\u0665\u0661\u0660\u0666\u0669\u0664"}, "", "\u0665\u0661\u0660\u0666\u0669\u0664", "", "", "", ""},
		{[]string{"+\uff18\uff11\uff15\uff18\uff10\uff11\uff13\uff10\uff15"}, "", "+\uff18\uff11\uff15\uff18\uff10\uff11\uff13\uff10\uff15", "", "", "", ""},
		{[]string{"+\u0661\u0661\u0663\u0662\u0660\u0667https://s/3"}, "", "+\u0661\u0661\u0663\u0662\u0660\u0667", "https://s/3", "", "", ""},
		{[]string{"phone=\uff10\uff11\uff12\uff13\uff14\uff15\uff16\uff17"}, "", "\uff10\uff11\uff12\uff13\uff14\uff15\uff16\uff17", "", "", "", ""},
		{[]string{"auth_phone=+15550001111", "sms_url=https://s/x"}, "", "+15550001111", "https://s/x", "", "", ""},
		{[]string{"+15550001111", "https://s/x"}, "", "+15550001111", "https://s/x", "", "", ""},
		{[]string{"rt_token=rt_1", "type=free", "inbox= z@y.com "}, "rt_1", "", "", "z@y.com", "", "free"},
	}
	for _, c := range cases {
		got := ExtractExtras(c.in)
		want := Extras{
			OpenAIRT: c.openaiRT, AuthPhoneNumber: c.phone, AuthPhoneSMSURL: c.smsURL,
			ReceiveMailbox: c.receiveMailbox, MailProvider: c.mailProvider, AccountType: c.accountType,
		}
		if got != want {
			t.Errorf("ExtractExtras(%q) = %+v, want %+v", c.in, got, want)
		}
	}
}

func TestPyParityParseLine(t *testing.T) {
	cases := []struct {
		in          string
		err         string
		email       string
		password    string
		clientID    string
		refresh     string
		raw         string
		accountType string
		status      string
		openaiRT    string
		phone       string
		smsURL      string
		receiveBox  string
		provider    string
	}{
		{"", "\u683c\u5f0f\u9519\u8bef\uff0c\u5e94\u4e3a email----password----client_id----refresh_token", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"   ", "\u683c\u5f0f\u9519\u8bef\uff0c\u5e94\u4e3a email----password----client_id----refresh_token", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"a@b.com", "\u683c\u5f0f\u9519\u8bef\uff0c\u5e94\u4e3a email----password----client_id----refresh_token", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"a@b.com----p----c", "\u683c\u5f0f\u9519\u8bef\uff0c\u5e94\u4e3a email----password----client_id----refresh_token", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"a@b.com----p----c----r", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----r----rt_token=rt_1", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "plus", "\u5df2\u7ed1\u5b9a\u624b\u673a\u53f7", "rt_1", "", "", "", ""},
		{" <A@B.COM> ---- pw ---- cid ---- rt ", "", "A@B.COM", "pw", "cid", "rt", "A@B.COM----pw----cid----rt", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----r----mail_provider=cloudmail", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", "cloudmail"},
		{"----p----c----r----mail_provider=cloudmail", "email \u4e0d\u80fd\u4e3a\u7a7a", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"\x1f----p----c----r", "email \u4e0d\u80fd\u4e3a\u7a7a", "", "", "", "", "", "", "", "", "", "", "", ""},
		{"notanemail----p----c----r", "", "notanemail", "p", "c", "r", "notanemail----p----c----r", "free", "", "", "", "", "", ""},
		{"\x1fa@b.com\x1f----p----c----r", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----r----+15550001111----https://s/x", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "\u5f85\u83b7\u53d6RT", "", "+15550001111", "https://s/x", "", ""},
		{"a@b.com----p----c----r----+15550001111https://s/x", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "\u5f85\u83b7\u53d6RT", "", "+15550001111", "https://s/x", "", ""},
		{"a@b.com----p----c----r----+\u0661\u0661\u0663\u0662\u0660\u0667https://s/3", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "\u5f85\u83b7\u53d6RT", "", "+\u0661\u0661\u0663\u0662\u0660\u0667", "https://s/3", "", ""},
		{"a@b.com----p----c----r----\uff15\uff18\uff19\uff10\uff12\uff10", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "\uff15\uff18\uff19\uff10\uff12\uff10", "", "", ""},
		{"a@b.com----p----c----r----https://x.example/a\u00a0b", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----r----type=k12", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----r----account_type=PRO", "", "a@b.com", "p", "c", "r", "a@b.com----p----c----r", "free", "", "", "", "", "", ""},
		{"a@b.com--------c----r", "", "a@b.com", "", "c", "r", "a@b.com--------c----r", "free", "", "", "", "", "", ""},
		{"a@b.com----p----c----", "\u975e Cloud Mail \u90ae\u7bb1\u7684 client_id / refresh_token \u4e0d\u80fd\u4e3a\u7a7a", "", "", "", "", "", "", "", "", "", "", "", ""},
	}
	for _, c := range cases {
		account, err := ParseLine(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("ParseLine(%q) error = %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseLine(%q) unexpected error %v", c.in, err)
			continue
		}
		if account.Email != c.email || account.Password != c.password ||
			account.ClientID != c.clientID || account.RefreshToken != c.refresh ||
			account.Raw != c.raw || account.AccountType != c.accountType ||
			account.Status != c.status || account.OpenaiRT != c.openaiRT ||
			account.AuthPhoneNumber != c.phone || account.AuthPhoneSMSURL != c.smsURL ||
			account.ReceiveMailbox != c.receiveBox || account.MailProvider != c.provider {
			t.Errorf("ParseLine(%q) = %+v, want email=%q password=%q client=%q refresh=%q raw=%q type=%q status=%q rt=%q phone=%q sms=%q box=%q provider=%q",
				c.in, account, c.email, c.password, c.clientID, c.refresh, c.raw,
				c.accountType, c.status, c.openaiRT, c.phone, c.smsURL, c.receiveBox, c.provider)
		}
	}
}

// app.py:14687 splits the paste box with str.splitlines(), which breaks on
// eleven boundaries where strings.Split(s, "\n") sees one.
func TestPyParitySplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a\nb", []string{"a", "b"}},
		{"a\r\nb", []string{"a", "b"}},
		{"a\rb", []string{"a", "b"}},
		{"a\u2028b", []string{"a", "b"}},
		{"a\x0bb", []string{"a", "b"}},
		{"a\x1cb", []string{"a", "b"}},
		{"a\u0085b", []string{"a", "b"}},
		{"a\n", []string{"a"}},
		{"a\r\n", []string{"a"}},
		{"a\n\nb", []string{"a", "", "b"}},
		{"x----y----z----w\r\nq----w----e----r", []string{"x----y----z----w", "q----w----e----r"}},
	}
	for _, c := range cases {
		got := pySplitLines(c.in)
		if len(got) != len(c.want) {
			t.Errorf("pySplitLines(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("pySplitLines(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}

// app.py:14701 resolves an existing row with
// `next((i for i, item in enumerate(self.accounts) if item.email.lower() == ...), -1)`
// — the FIRST match. MergeKey is that bare .lower(), so two stored rows spelled
// with different case collide, and an index map that keeps the LAST occurrence
// updated the wrong row: the import looked like it applied while the row the
// rest of the app resolves to stayed stale.
func TestPyParityMergeIntoUpdatesFirstMatch(t *testing.T) {
	existing := []models.MailAccount{
		{Email: "A@B.COM", Password: "first", ClientID: "c0", RefreshToken: "r0", Group: "G1"},
		{Email: "a@b.com", Password: "second", ClientID: "c1", RefreshToken: "r1", Group: "G2"},
	}
	imported, errs := ParseText("a@b.com----new----c2----r2")
	if len(errs) != 0 || len(imported) != 1 {
		t.Fatalf("ParseText = %v, %v", imported, errs)
	}
	out := MergeInto(existing, imported, "GX")
	if len(out) != 2 {
		t.Fatalf("MergeInto appended instead of updating: %d rows", len(out))
	}
	if out[0].Password != "new" {
		t.Errorf("first match not updated: out[0] = %+v", out[0])
	}
	if out[1].Password != "second" {
		t.Errorf("later duplicate must be left alone: out[1] = %+v", out[1])
	}
	// account_type / status / group stay worker- and user-owned on an update.
	if out[0].Group != "G1" {
		t.Errorf("group must survive the import: %q", out[0].Group)
	}
}
