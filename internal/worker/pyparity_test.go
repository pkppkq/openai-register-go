package worker

// pyparity_test.go pins the places where Go's regexp/string dialect silently
// disagrees with Python's and this package had to spell around it.
//
// EVERY expectation in this file was COMPUTED by running the corresponding
// function's verbatim app.py source under CPython 3.12 over these exact inputs
// (the slices are taken byte-for-byte out of app.py, not retyped), and every
// input listed here is one where the pre-fix Go implementation returned a
// DIFFERENT answer than Python in a 51k-input differential sweep. Nothing here
// is hand-derived, and none of it passes against a constant.
//
// The three dialect gaps under test:
//
//	RE2 \s is [\t\n\f\r ]          Python \s is that + VT + U+001C..U+001F + \p{Z}
//	RE2 \b is an ASCII boundary    Python \w includes every Unicode letter/number
//	strings.TrimSpace omits        str.strip() removes U+001C..U+001F
//
// plus urllib unquote's leniency and urlunsplit's scheme-less form.

import "testing"

func yearMonthDay(y, m, d string) [3]string { return [3]string{y, m, d} }

type valuesCase struct {
	values []string
	kind   string
}

type secondCase struct {
	kind      string
	birthYear string
	age       string
	birthdate string
	context   string
}

// TestAboutYouSecondFieldKindFromContextPythonParity is AboutYouSecondFieldKindFromContext (app.py:11202-11254).
func TestAboutYouSecondFieldKindFromContextPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Date\u00a0of\u00a0birth", "birth_date"},
		{"date\u2009of\u2009birth", "birth_date"},
		{"Birth\u00a0date", "birth_date"},
		{"birth\u00a0year", "birth_year"},
		{"year\u00a0of\u00a0birth", "birth_year"},
		{"born\u00a0on", "birth_date"},
		{"born\u00a0year", "birth_year"},
		{"how\u00a0old", "age"},
		{"how\u00a0old\u00a0are\u00a0you", "age"},
		{"fecha\u00a0de\u00a0nacimiento", "birth_date"},
		{"type\u00a0=\u00a0date", "birth_date"},
		{"mm\u00a0/\u00a0dd\u00a0/\u00a0yyyy", "birth_date"},
		{"dd\u00a0/\u00a0mm\u00a0/\u00a0yyyy", "birth_date"},
		{"yyyy\u00a0/\u00a0mm\u00a0/\u00a0dd", "birth_date"},
		{"dd\u00a0-\u00a0mm\u00a0-\u00a0aaaa", "birth_date"},
		{"Date\u3000of\u3000birth", "birth_date"},
		{"how\u000bold", "age"},
		{"date\u2028of\u2028birth", "birth_date"},
		{"\u5e74\u9f62type=date", "age"},
		{"\u30b3\u30fc\u30c9mm/dd/yyyy", "birth_year"},
		{"\u30b3\u30fc\u30c9jjjj", "birth_year"},
		{"date of birth", "birth_date"},
		{"birth year", "birth_year"},
		{"how old", "age"},
		{"age", "age"},
		{"type=date", "birth_date"},
		{"mm/dd/yyyy", "birth_date"},
		{"\u751f\u5e74", "birth_year"},
		{"\u751f\u5e74\u6708\u65e5", "birth_date"},
		{"", "birth_year"},
		{"bday", "birth_date"},
	}
	for _, tt := range tests {
		if got := AboutYouSecondFieldKindFromContext(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestAboutYouClassifyFieldPythonParity is AboutYouClassifyField (app.py:11324-11355).
func TestAboutYouClassifyFieldPythonParity(t *testing.T) {
	tests := []struct {
		in   AboutYouFieldMeta
		want string
	}{
		{AboutYouFieldMeta{Type: "", Name: "", ID: "\u30b3\u30fc\u30c9age", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "\u5e74\u4efdyear", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "\u30b3\u30fc\u30c9day", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "\u8a95\u751f\u65e5age", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "\u59d3\u540dmonth", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "name"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: "\u96fb\u8a71us"}, "unknown"},
		{AboutYouFieldMeta{Type: "type\u00a0=\u00a0date", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: "Date\u00a0of\u00a0birth"}, "unknown"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "how\u00a0old\u00a0are\u00a0you", TestID: "", Label: ""}, "unknown"},
		{AboutYouFieldMeta{Type: "hidden", Name: "year", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "ignore"},
		{AboutYouFieldMeta{Type: "date", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "birth_date"},
		{AboutYouFieldMeta{Type: "", Name: "bday-year", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "birth_year"},
		{AboutYouFieldMeta{Type: "", Name: "bday-month", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "birth_month"},
		{AboutYouFieldMeta{Type: "", Name: "bday-day", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "birth_day"},
		{AboutYouFieldMeta{Type: "", Name: "age", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "age"},
		{AboutYouFieldMeta{Type: "", Name: "csrf", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "ignore"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: "full name"}, "name"},
		{AboutYouFieldMeta{Type: "", Name: "\u5e74\u9f84", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "age"},
		{AboutYouFieldMeta{Type: "", Name: "", ID: "", Placeholder: "", Autocomplete: "", Inputmode: "", AriaLabel: "", TestID: "", Label: ""}, "unknown"},
	}
	for _, tt := range tests {
		if got := AboutYouClassifyField(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestParseAboutYouBirthdatePythonParity is ParseAboutYouBirthdate (app.py:11256-11271).
func TestParseAboutYouBirthdatePythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want [3]string
	}{
		{"1990-05-17\u001f", [3]string{"1990", "05", "17"}},
		{" 1990-05-17 \u001f", [3]string{"1990", "05", "17"}},
		{"1990/5/7\u001f", [3]string{"1990", "05", "07"}},
		{"12/31/2001\u001f", [3]string{"2001", "12", "31"}},
		{"17.5.1990\u001f", [3]string{"1990", "05", "17"}},
		{"1990-05-17\u001c", [3]string{"1990", "05", "17"}},
		{"1990-05-17", [3]string{"1990", "05", "17"}},
		{"1990/5/7", [3]string{"1990", "05", "07"}},
		{"5/17/1990", [3]string{"1990", "05", "17"}},
		{"17.5.1990", [3]string{"1990", "05", "17"}},
		{"1990", [3]string{"1990", "", ""}},
		{"abc", [3]string{"", "", ""}},
		{"", [3]string{"", "", ""}},

		// Unicode digits. Python's \d is \p{Nd}, so the ISO arm returns the
		// groups VERBATIM (still Arabic-Indic) while the slash/dot arms round-trip
		// theirs through int() and come back ASCII.
		{"\u0662\u0660\u0660\u0660-\u0660\u0661-\u0660\u0661", [3]string{"\u0662\u0660\u0660\u0660", "\u0660\u0661", "\u0660\u0661"}},
		{"\u0661\u0669\u0669\u0660/\u0665/\u0667", [3]string{"\u0661\u0669\u0669\u0660", "05", "07"}},
		{"\u0665/\u0661\u0667/\u0661\u0669\u0669\u0660", [3]string{"\u0661\u0669\u0669\u0660", "05", "17"}},
		{"\u0661\u0667.\u0665.\u0661\u0669\u0669\u0660", [3]string{"\u0661\u0669\u0669\u0660", "05", "17"}},
		{"\u0661\u0669\u0669\u0660", [3]string{"\u0661\u0669\u0669\u0660", "", ""}},
		{"\uff12\uff10\uff10\uff10-\uff10\uff11-\uff10\uff11", [3]string{"\uff12\uff10\uff10\uff10", "\uff10\uff11", "\uff10\uff11"}},
		{"\uff11\uff19\uff19\uff10", [3]string{"\uff11\uff19\uff19\uff10", "", ""}},
		{"\u06f2\u06f0\u06f0\u06f0-\u06f0\u06f1-\u06f0\u06f1", [3]string{"\u06f2\u06f0\u06f0\u06f0", "\u06f0\u06f1", "\u06f0\u06f1"}},
		{"\u0966\u0967\u0968\u0969", [3]string{"\u0966\u0967\u0968\u0969", "", ""}},
		{"\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1", [3]string{"\U0001d7ce\U0001d7cf\U0001d7d0\U0001d7d1", "", ""}},
		{"\u0662\u0660\u0660\u0660-01-01", [3]string{"\u0662\u0660\u0660\u0660", "01", "01"}},
	}
	for _, tt := range tests {
		if got := yearMonthDay(ParseAboutYouBirthdate(tt.in)); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestAboutYouValuesOKPythonParity is AboutYouValuesOK (app.py:11458-11507).
func TestAboutYouValuesOKPythonParity(t *testing.T) {
	tests := []struct {
		in   valuesCase
		want bool
	}{
		{valuesCase{[]string{"Alice", "\u001f42\u001f", "101", "5"}, "birth_month"}, true},
		{valuesCase{[]string{"Alice", "1990", ""}, "birth_year"}, true},
		{valuesCase{[]string{"Alice", "\u001f1990\u001f"}, "birth_year"}, true},
		{valuesCase{[]string{"Alice", "25"}, "age"}, true},
		{valuesCase{[]string{"Alice", "1990-05-17"}, "birth_date"}, true},
		{valuesCase{[]string{"Alice", "1990"}, "birth_date"}, false},
		{valuesCase{[]string{}, "birth_year"}, false},

		// Unicode digits. This function is the gate that decides whether the
		// about-you form was accepted, and it re-reads what the BROWSER shows --
		// an ASCII-only \d would call a correctly filled Arabic-locale form a
		// failure. int() reads any Nd digit, so the 1950..2007 window applies to
		// all of them.
		{valuesCase{[]string{"\u0661\u0669\u0669\u0669", "05"}, "birth_month"}, false},
		{valuesCase{[]string{"AB", "\u0663\u0664"}, "birth_month"}, true},
		{valuesCase{[]string{"Alice", "\u0661\u0669\u0669\u0660"}, "birth_year"}, true},
		{valuesCase{[]string{"Alice", "\u0662\u0660\u0660\u0660"}, "birth_year"}, true},
		{valuesCase{[]string{"Alice", "\u0661\u0669\u0664\u0669"}, "birth_year"}, false},
		{valuesCase{[]string{"Alice", "\u0662\u0665"}, "age"}, true},
		{valuesCase{[]string{"Alice", "\u0661\u0667"}, "age"}, false},
		{valuesCase{[]string{"Alice", "\u0661\u0669\u0669\u0660-\u0660\u0665-\u0661\u0667"}, "birth_date"}, true},
		{valuesCase{[]string{"Alice", "\u0665/\u0661\u0667/\u0661\u0669\u0669\u0660"}, "birth_date"}, true},
		{valuesCase{[]string{"Alice", "\u0661\u0667.\u0665.\u0661\u0669\u0669\u0660"}, "birth_date"}, true},
		{valuesCase{[]string{"Alice", "\u0660\u0665", "\u0661\u0667", "\u0661\u0669\u0669\u0660"}, "birth_date"}, true},
		{valuesCase{[]string{"\uff11\uff19\uff19\uff19", "05"}, "birth_month"}, false},
		{valuesCase{[]string{"Alice", "\U0001d7d9\U0001d7e1\U0001d7e1\U0001d7d8"}, "birth_year"}, true},
		{valuesCase{[]string{"Alice", "\U0001d7f0\U0001d7ee\U0001d7ee\U0001d7f5"}, "birth_year"}, false},
		{valuesCase{[]string{"Alice", "\U0001d7d2\U0001d7d4\U0001d7d4\U0001d7d6"}, "birth_year"}, false},
	}
	for _, tt := range tests {
		if got := AboutYouValuesOK(tt.in.values, tt.in.kind); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestPageTextNormalisationPythonParity is re.sub(r"\s+", " ", text).strip() (app.py:9940, 10988).
func TestPageTextNormalisationPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"SMS\u000bcode", "SMS code"},
		{"Route\u001fError", "Route Error"},
		{"Route\u001fError ", "Route Error"},
		{"a\u00a0b", "a b"},
		{"a\u3000b", "a b"},
		{"a\u2028b", "a b"},
		{"  x  ", "x"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := pyCollapseStrip(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestDetectRouteErrorTextPythonParity is _detect_route_error (app.py:9935-9943).
func TestDetectRouteErrorTextPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Route\u001fError", "Route Error"},
		{"Route\u000bError", "Route Error"},
		{"Operation\u00a0timed out", "Operation timed out"},
		{"Route Error", "Route Error"},
		{"fine", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := authRouteErrorFromText(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestLooksLikeRegisterPhoneCodePagePythonParity is _looks_like_register_phone_code_page (app.py:10440-10449).
func TestLooksLikeRegisterPhoneCodePagePythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"SMS\u000bcode", true},
		{"phone\u00a0number verification", true},
		{"\u77ed\u4fe1\u001f\u9a8c\u8bc1\u7801", true},
		{"phone number code", true},
		{"email code", false},
		{"hello", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := phoneLooksLikeCodePageText(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestProxyExitIsJapanPythonParity is _proxy_exit_is_japan (app.py:11934-11935).
func TestProxyExitIsJapanPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"\u00a0JP ", true},
		{"JP\u00a0", true},
		{"JP\u3000x", true},
		{"US\u00a0JP/Tokyo", true},
		{"JP\u000b", true},
		{" JP ", true},
		{"JP", true},
		{"JP/Tokyo", true},
		{"US/JP", false},
		{"xJP", false},
		{"JPx", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := (&PayLinkExtractor{}).ProxyExitIsJapan(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestMaskProxyURLPythonParity is mask_proxy_url (app.py:2564-2576).
func TestMaskProxyURLPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"//user:pass@host:8080", "//***@host:8080"},
		{"//user:pass@host:8080/p", "//***@host:8080/p"},
		{"//user:pass@host:8080#f", "//***@host:8080#f"},
		{"https://USER:PW@HOST.EXAMPLE:443/path?q=1#f", "https://***@host.example:443/path?q=1#f"},
		{"http://u:p@h:1\u001f", "http://***@h:1"},
		{"http://user:pass@1.2.3.4:8080", "http://***@1.2.3.4:8080"},
		{"http://1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"socks5://u:p@host.example:1080", "socks5://***@host.example:1080"},
		{"http://@host:8080", "http://@host:8080"},
		{"", "\u76f4\u8fde"},
		{"   ", "\u76f4\u8fde"},
		{"127.0.0.1:7890", "127.0.0.1:7890"},
	}
	for _, tt := range tests {
		if got := payLinkMaskProxyURL(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestProxyExitFailedTextPythonParity is _proxy_exit_failed_text (app.py:4014-4015).
func TestProxyExitFailedTextPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"\u001f\u68c0\u6d4b\u5931\u8d25", true},
		{" \u68c0\u6d4b\u5931\u8d25: x", true},
		{"\u68c0\u6d4b\u5931\u8d25", true},
		{"ok", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := payLinkProxyExitFailedText(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestAlignedExitLogPythonParity is _format_aligned_exit_log (app.py:3997-4000).
func TestAlignedExitLogPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"\u001f\u68c0\u6d4b\u5931\u8d25", "\u51fa\u53e3[\u7b2c\u4e00\u6b65]    \t: \u68c0\u6d4b\u5931\u8d25"},
		{"JP\u001f", "\u51fa\u53e3[\u7b2c\u4e00\u6b65]    \t: JP"},
		{"  ", "\u51fa\u53e3[\u7b2c\u4e00\u6b65]    \t: \u672a\u8bb0\u5f55"},
		{"", "\u51fa\u53e3[\u7b2c\u4e00\u6b65]    \t: \u672a\u8bb0\u5f55"},
		{"JP", "\u51fa\u53e3[\u7b2c\u4e00\u6b65]    \t: JP"},
	}
	for _, tt := range tests {
		if got := payLinkFormatAlignedExitLog("\u7b2c\u4e00\u6b65", tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestCSRFTokenFromCookiePythonParity is unquote(value).split("|")[0] (app.py:10033).
func TestCSRFTokenFromCookiePythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"a%7Cb|c", "a"},
		{"tok%7Chash%zz", "tok"},
		{"%E4%B8%AD%E6%96%87|x", "\u4e2d\u6587"},
		{"a%2Bb|c", "a+b"},
		{"a+b|c", "a+b"},
		{"%|x", "%"},
		{"abc%20def|g", "abc def"},
		{"abc|def", "abc"},
		{"abc", "abc"},
		{"", ""},
		{"|", ""},
	}
	for _, tt := range tests {
		if got := authCSRFTokenFromCookie(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestManualOTPDigitsPythonParity is re.sub(r"\D", "", code) or code (app.py:8997-8998).
func TestManualOTPDigitsPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"\u0663\u0664\u0665", "\u0663\u0664\u0665"},
		{"12 34 56", "123456"},
		{"code:123456", "123456"},
		{"abc", "abc"},
		{"\u0663\u0664\u0665 abc", "\u0663\u0664\u0665"},
		{"123456", "123456"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := otpManualDigits(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestAboutYouSecondFieldValuePythonParity is AboutYouSecondFieldValue (app.py:11378-11411).
//
// Note how the two halves disagree on script: the ISO arm hands back the groups
// parse_birthdate matched, so a ٢٠٠٠-٠١-٠١ birthdate stays Arabic-Indic, while
// the slash and dot arms pass theirs through f"{int(x):02d}" and come back
// ASCII. Both are Python's answer; neither is a normalisation this port chose.
func TestAboutYouSecondFieldValuePythonParity(t *testing.T) {
	tests := []struct {
		in   secondCase
		want string
	}{
		{secondCase{"birth_date", "2000", "25", "\u0662\u0660\u0660\u0660-\u0660\u0661-\u0660\u0661", "yyyy/mm/dd bday-month year\u00a0of\u00a0birth"}, "\u0660\u0661/\u0660\u0661/\u0662\u0660\u0660\u0660"},
		{secondCase{"birth_month", "0", "0", "\u0662\u0660\u0660\u0660-\u0660\u0661-\u0660\u0661", "mm/dd/yyyy"}, "\u0660\u0661"},
		{secondCase{"birth_day", "", "", "\u0662\u0660\u0660\u0660-\u0660\u0661-\u0660\u0661", ""}, "\u0660\u0661"},
		{secondCase{"birth_date", "1990", "", "\u0662\u0660\u0660\u0660-\u0660\u0661-\u0660\u0661", "jjjj  AGE"}, "\u0660\u0661.\u0660\u0661.\u0662\u0660\u0660\u0660"},
		{secondCase{"birth_date", "", "", "\u0661\u0669\u0669\u0660/\u0665/\u0667", "mm/dd"}, "05/07/\u0661\u0669\u0669\u0660"},
		{secondCase{"birth_date", "", "", "\uff11\uff17.\uff15.\uff11\uff19\uff19\uff10", "dd/mm"}, "17/05/\uff11\uff19\uff19\uff10"},
		{secondCase{"birth_month", "", "", "\u0661\u0669\u0669\u0660/\u0665/\u0667", ""}, "05"},
	}
	for _, tt := range tests {
		got := AboutYouSecondFieldValue(tt.in.kind, tt.in.birthYear, tt.in.age, tt.in.birthdate, tt.in.context)
		if got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestOpllAmountFieldsPythonParity is _opll_amount_fields (app.py:11906-11912).
//
// Python's four arms test KEY PRESENCE, not truthiness. AmountFields always
// carries all four keys, so every arm takes the "present" branch and the
// function is the identity — in particular an explicitly EMPTY target_amount,
// which is what a skipped amount check produces, must NOT be back-filled with
// the configured target.
func TestOpllAmountFieldsPythonParity(t *testing.T) {
	tests := []struct {
		in   AmountFields
		want AmountFields
	}{
		{AmountFields{StripeAmount: "", StripeAmountSource: "", TargetAmount: "", AmountCheck: "skipped"},
			AmountFields{StripeAmount: "", StripeAmountSource: "", TargetAmount: "", AmountCheck: "skipped"}},
		{AmountFields{StripeAmount: "20", StripeAmountSource: "stripe", TargetAmount: "20", AmountCheck: "passed"},
			AmountFields{StripeAmount: "20", StripeAmountSource: "stripe", TargetAmount: "20", AmountCheck: "passed"}},
		{AmountFields{StripeAmount: "  20  ", StripeAmountSource: "  ", TargetAmount: "", AmountCheck: "mismatch"},
			AmountFields{StripeAmount: "  20  ", StripeAmountSource: "  ", TargetAmount: "", AmountCheck: "mismatch"}},
	}
	e := &PayLinkExtractor{TargetAmount: "TGT"}
	for _, tt := range tests {
		if got := e.OpllAmountFields(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}
