package alias

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
		if got := pyNormalizeEmailAddress(c.in); got != c.want {
			t.Errorf("pyNormalizeEmailAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyParityPlusAliasHelpers(t *testing.T) {
	cases := []struct {
		in      string
		mailbox string
		isAlias bool
	}{
		{"a@b.com", "a@b.com", false},
		{"a+1@b.com", "a@b.com", true},
		{"A+TAG@B.COM", "A@B.COM", true},
		{"a+b+c@d.com", "a@d.com", true},
		{"+lead@b.com", "@b.com", true},
		{"a+@b.com", "a@b.com", true},
		{"noat", "noat", false},
		{"a@b", "a@b", false},
		{" a+x@b.com ", "a@b.com", true},
		{"<a+x@b.com>", "a@b.com", true},
		{"\x1fnoat\x1f", "noat", false},
		{"\u0130+1@b.com", "@b.com", true},
		{"\u017fam+2@b.com", "am@b.com", true},
		{"a@b.com+x", "a@b.com", false},
	}
	for _, c := range cases {
		if got := MailboxEmailForPlusAlias(c.in); got != c.mailbox {
			t.Errorf("MailboxEmailForPlusAlias(%q) = %q, want %q", c.in, got, c.mailbox)
		}
		if got := IsPlusAliasEmail(c.in); got != c.isAlias {
			t.Errorf("IsPlusAliasEmail(%q) = %v, want %v", c.in, got, c.isAlias)
		}
	}
}

// app.py:1732 strips with `\D+`, i.e. everything that is not a Unicode decimal
// digit — an Arabic-Indic suffix survives, a superscript two does not.
func TestPyParityPlusAliasEmail(t *testing.T) {
	cases := []struct {
		email, suffix, want, err string
	}{
		{"a@b.com", "1", "a+1@b.com", ""},
		{"a@b.com", "123", "a+123@b.com", ""},
		{"a@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com", "a1b2", "a+12@b.com", ""},
		{"a@b.com", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{"a@b.com", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{"a@b.com", "12 34", "a+1234@b.com", ""},
		{"a@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com", "-12", "a+12@b.com", ""},
		{"a@b.com", "1.5", "a+15@b.com", ""},
		{"a+1@b.com", "1", "a+1@b.com", ""},
		{"a+1@b.com", "123", "a+123@b.com", ""},
		{"a+1@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+1@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+1@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+1@b.com", "a1b2", "a+12@b.com", ""},
		{"a+1@b.com", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{"a+1@b.com", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{"a+1@b.com", "12 34", "a+1234@b.com", ""},
		{"a+1@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+1@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+1@b.com", "-12", "a+12@b.com", ""},
		{"a+1@b.com", "1.5", "a+15@b.com", ""},
		{"A+TAG@B.COM", "1", "A+1@B.COM", ""},
		{"A+TAG@B.COM", "123", "A+123@B.COM", ""},
		{"A+TAG@B.COM", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"A+TAG@B.COM", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"A+TAG@B.COM", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"A+TAG@B.COM", "a1b2", "A+12@B.COM", ""},
		{"A+TAG@B.COM", "\u0661\u0662\u0663", "A+\u0661\u0662\u0663@B.COM", ""},
		{"A+TAG@B.COM", "\uff11\uff12", "A+\uff11\uff12@B.COM", ""},
		{"A+TAG@B.COM", "12 34", "A+1234@B.COM", ""},
		{"A+TAG@B.COM", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"A+TAG@B.COM", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"A+TAG@B.COM", "-12", "A+12@B.COM", ""},
		{"A+TAG@B.COM", "1.5", "A+15@B.COM", ""},
		{"a+b+c@d.com", "1", "a+1@d.com", ""},
		{"a+b+c@d.com", "123", "a+123@d.com", ""},
		{"a+b+c@d.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+b+c@d.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+b+c@d.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+b+c@d.com", "a1b2", "a+12@d.com", ""},
		{"a+b+c@d.com", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@d.com", ""},
		{"a+b+c@d.com", "\uff11\uff12", "a+\uff11\uff12@d.com", ""},
		{"a+b+c@d.com", "12 34", "a+1234@d.com", ""},
		{"a+b+c@d.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+b+c@d.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+b+c@d.com", "-12", "a+12@d.com", ""},
		{"a+b+c@d.com", "1.5", "a+15@d.com", ""},
		{"+lead@b.com", "1", "+1@b.com", ""},
		{"+lead@b.com", "123", "+123@b.com", ""},
		{"+lead@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"+lead@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"+lead@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"+lead@b.com", "a1b2", "+12@b.com", ""},
		{"+lead@b.com", "\u0661\u0662\u0663", "+\u0661\u0662\u0663@b.com", ""},
		{"+lead@b.com", "\uff11\uff12", "+\uff11\uff12@b.com", ""},
		{"+lead@b.com", "12 34", "+1234@b.com", ""},
		{"+lead@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"+lead@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"+lead@b.com", "-12", "+12@b.com", ""},
		{"+lead@b.com", "1.5", "+15@b.com", ""},
		{"a+@b.com", "1", "a+1@b.com", ""},
		{"a+@b.com", "123", "a+123@b.com", ""},
		{"a+@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+@b.com", "a1b2", "a+12@b.com", ""},
		{"a+@b.com", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{"a+@b.com", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{"a+@b.com", "12 34", "a+1234@b.com", ""},
		{"a+@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a+@b.com", "-12", "a+12@b.com", ""},
		{"a+@b.com", "1.5", "a+15@b.com", ""},
		{"noat", "1", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "123", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "abc", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "  ", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "a1b2", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "\u0661\u0662\u0663", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "\uff11\uff12", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "12 34", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "\u00b2\u00b3", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "\u2460", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "-12", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"noat", "1.5", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"a@b", "1", "a+1@b", ""},
		{"a@b", "123", "a+123@b", ""},
		{"a@b", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b", "a1b2", "a+12@b", ""},
		{"a@b", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b", ""},
		{"a@b", "\uff11\uff12", "a+\uff11\uff12@b", ""},
		{"a@b", "12 34", "a+1234@b", ""},
		{"a@b", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b", "-12", "a+12@b", ""},
		{"a@b", "1.5", "a+15@b", ""},
		{" a+x@b.com ", "1", "a+1@b.com", ""},
		{" a+x@b.com ", "123", "a+123@b.com", ""},
		{" a+x@b.com ", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{" a+x@b.com ", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{" a+x@b.com ", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{" a+x@b.com ", "a1b2", "a+12@b.com", ""},
		{" a+x@b.com ", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{" a+x@b.com ", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{" a+x@b.com ", "12 34", "a+1234@b.com", ""},
		{" a+x@b.com ", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{" a+x@b.com ", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{" a+x@b.com ", "-12", "a+12@b.com", ""},
		{" a+x@b.com ", "1.5", "a+15@b.com", ""},
		{"<a+x@b.com>", "1", "a+1@b.com", ""},
		{"<a+x@b.com>", "123", "a+123@b.com", ""},
		{"<a+x@b.com>", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"<a+x@b.com>", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"<a+x@b.com>", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"<a+x@b.com>", "a1b2", "a+12@b.com", ""},
		{"<a+x@b.com>", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{"<a+x@b.com>", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{"<a+x@b.com>", "12 34", "a+1234@b.com", ""},
		{"<a+x@b.com>", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"<a+x@b.com>", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"<a+x@b.com>", "-12", "a+12@b.com", ""},
		{"<a+x@b.com>", "1.5", "a+15@b.com", ""},
		{"\x1fnoat\x1f", "1", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "123", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "abc", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "  ", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "a1b2", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "\u0661\u0662\u0663", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "\uff11\uff12", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "12 34", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "\u00b2\u00b3", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "\u2460", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "-12", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\x1fnoat\x1f", "1.5", "", "\u90ae\u7bb1\u683c\u5f0f\u9519\u8bef\uff0c\u65e0\u6cd5\u751f\u6210 + \u522b\u540d"},
		{"\u0130+1@b.com", "1", "+1@b.com", ""},
		{"\u0130+1@b.com", "123", "+123@b.com", ""},
		{"\u0130+1@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u0130+1@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u0130+1@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u0130+1@b.com", "a1b2", "+12@b.com", ""},
		{"\u0130+1@b.com", "\u0661\u0662\u0663", "+\u0661\u0662\u0663@b.com", ""},
		{"\u0130+1@b.com", "\uff11\uff12", "+\uff11\uff12@b.com", ""},
		{"\u0130+1@b.com", "12 34", "+1234@b.com", ""},
		{"\u0130+1@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u0130+1@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u0130+1@b.com", "-12", "+12@b.com", ""},
		{"\u0130+1@b.com", "1.5", "+15@b.com", ""},
		{"\u017fam+2@b.com", "1", "am+1@b.com", ""},
		{"\u017fam+2@b.com", "123", "am+123@b.com", ""},
		{"\u017fam+2@b.com", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u017fam+2@b.com", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u017fam+2@b.com", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u017fam+2@b.com", "a1b2", "am+12@b.com", ""},
		{"\u017fam+2@b.com", "\u0661\u0662\u0663", "am+\u0661\u0662\u0663@b.com", ""},
		{"\u017fam+2@b.com", "\uff11\uff12", "am+\uff11\uff12@b.com", ""},
		{"\u017fam+2@b.com", "12 34", "am+1234@b.com", ""},
		{"\u017fam+2@b.com", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u017fam+2@b.com", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"\u017fam+2@b.com", "-12", "am+12@b.com", ""},
		{"\u017fam+2@b.com", "1.5", "am+15@b.com", ""},
		{"a@b.com+x", "1", "a+1@b.com", ""},
		{"a@b.com+x", "123", "a+123@b.com", ""},
		{"a@b.com+x", "abc", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com+x", "", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com+x", "  ", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com+x", "a1b2", "a+12@b.com", ""},
		{"a@b.com+x", "\u0661\u0662\u0663", "a+\u0661\u0662\u0663@b.com", ""},
		{"a@b.com+x", "\uff11\uff12", "a+\uff11\uff12@b.com", ""},
		{"a@b.com+x", "12 34", "a+1234@b.com", ""},
		{"a@b.com+x", "\u00b2\u00b3", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com+x", "\u2460", "", "\u522b\u540d\u540e\u7f00\u5fc5\u987b\u5305\u542b\u6570\u5b57"},
		{"a@b.com+x", "-12", "a+12@b.com", ""},
		{"a@b.com+x", "1.5", "a+15@b.com", ""},
	}
	for _, c := range cases {
		got, err := PlusAliasEmail(c.email, c.suffix)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("PlusAliasEmail(%q, %q) error = %v, want %q", c.email, c.suffix, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("PlusAliasEmail(%q, %q) = (%q, %v), want %q", c.email, c.suffix, got, err, c.want)
		}
	}
}

// normalize_domain_mail_domain CASEFOLDS before it validates, so "example\u00df.com"
// becomes the perfectly legal "exampless.com" — strings.ToLower leaves the ß and
// the hostname regex then rejects a domain Python accepts.
func TestPyParityNormalizeDomainMailDomain(t *testing.T) {
	cases := []struct{ in, want, err string }{
		{"mail.example.com", "mail.example.com", ""},
		{" MAIL.EXAMPLE.COM ", "mail.example.com", ""},
		{"user@mail.example.com", "mail.example.com", ""},
		{"a@b@mail.example.com", "mail.example.com", ""},
		{".mail.example.com.", "mail.example.com", ""},
		{"example\u00df.com", "exampless.com", ""},
		{"EXAMPLE\u00df.COM", "exampless.com", ""},
		{"\u1e9eexample.com", "ssexample.com", ""},
		{"\u0130example.com", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"\u017fam.com", "sam.com", ""},
		{"", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"  ", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"\x1fmail.example.com\x1f", "mail.example.com", ""},
		{"a", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"a.b", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"-a.com", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"a-.com", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"xn--80ak6aa92e.com", "xn--80ak6aa92e.com", ""},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com", "", "\u57df\u540d\u683c\u5f0f\u9519\u8bef\uff0c\u4f8b\u5982 mail.example.com"},
		{"1.com", "1.com", ""},
		{"@mail.example.com", "mail.example.com", ""},
		{"mail.example.com\n", "mail.example.com", ""},
		{"MAIL.EXAMPLE.COM\u3000", "mail.example.com", ""},
	}
	for _, c := range cases {
		got, err := NormalizeDomainMailDomain(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("NormalizeDomainMailDomain(%q) error = %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NormalizeDomainMailDomain(%q) = (%q, %v), want %q", c.in, got, err, c.want)
		}
	}
}
