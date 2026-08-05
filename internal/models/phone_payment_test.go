package models

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeUSPhoneForForm(t *testing.T) {
	cases := map[string]string{
		"+1 (415) 555-0132": "4155550132",
		"14155550132":       "4155550132",
		"4155550132":        "4155550132",
		"1-202-555-0100":    "2025550100",
		"":                  "",
		"1":                 "1", // not 11 digits -> unchanged
	}
	for in, want := range cases {
		if got := NormalizeUSPhoneForForm(in); got != want {
			t.Fatalf("NormalizeUSPhoneForForm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyPhoneRejection(t *testing.T) {
	cases := []struct {
		in     string
		status string
	}{
		{"This phone number has been used   already", "手机号已使用"},
		{"手机号已被使用，请更换", "手机号已使用"},
		{"VoIP numbers are not supported", "手机号不支持"},
		{"Please enter a valid phone number", "手机号无效"},
		{"Too many requests, try again later", "手机号频率限制"},
		{"This phone number is blocked", "手机号被拒绝"},
		{"something totally unrelated", ""},
	}
	for _, c := range cases {
		status, text := ClassifyPhoneRejection(c.in)
		if status != c.status {
			t.Fatalf("ClassifyPhoneRejection(%q) status = %q, want %q", c.in, status, c.status)
		}
		if text == "" && c.in != "" {
			t.Fatalf("text should be non-empty for %q", c.in)
		}
	}
	// whitespace collapse in returned text
	if _, text := ClassifyPhoneRejection("a\n\n  b\tc"); text != "a b c" {
		t.Fatalf("whitespace not collapsed: %q", text)
	}

	// Python's re.sub(r"\s+") is Unicode-aware; Go's RE2 \s is not. Rendered page
	// text routinely carries NBSP, and an ASCII-only collapse would leave the
	// marker phrase unmatched, silently downgrading the status.
	status, text := ClassifyPhoneRejection("This phone number has been used already")
	if status != "手机号已使用" {
		t.Fatalf("NBSP-separated marker not matched: status = %q", status)
	}
	if strings.ContainsAny(text, "  ") {
		t.Fatalf("unicode spaces not collapsed: %q", text)
	}
}

func TestExceptionStatus(t *testing.T) {
	if got := ExceptionStatus(NewPhoneRejectedError("bad"), "失败"); got != "手机号不可用" {
		t.Fatalf("PhoneRejected status = %q", got)
	}
	if got := ExceptionStatus(&PhoneRejectedError{Msg: "x", Status: "手机号已使用"}, "失败"); got != "手机号已使用" {
		t.Fatalf("custom status = %q", got)
	}
	if got := ExceptionStatus(NewAccountDeactivatedError(), "失败"); got != "账号已停用" {
		t.Fatalf("deactivated status = %q", got)
	}
	if got := ExceptionStatus(errors.New("plain"), "失败"); got != "失败" {
		t.Fatalf("plain error should yield default, got %q", got)
	}
	// wrapped typed error resolves through errors.As
	wrapped := errors.Join(errors.New("ctx"), NewProxyExitCheckError("no jp exit"))
	if got := ExceptionStatus(wrapped, "失败"); got != "代理检测失败" {
		t.Fatalf("wrapped status = %q", got)
	}
}

func TestCurrencyForCountry(t *testing.T) {
	if CurrencyForCountry("jp") != "JPY" {
		t.Fatalf("jp -> %q", CurrencyForCountry("jp"))
	}
	if CurrencyForCountry(" us ") != "USD" {
		t.Fatalf("us -> %q", CurrencyForCountry(" us "))
	}
	if CurrencyForCountry("ZZ") != "USD" {
		t.Fatalf("unknown -> %q, want USD", CurrencyForCountry("ZZ"))
	}
	// Both COUNTRY_CURRENCY.update() passes (app.py:560-565) must be present:
	// the EUR fill-in for EUR_COUNTRIES, and the explicit extras. Omitting them
	// silently billed these countries in USD.
	for country, want := range map[string]string{
		"GR": "EUR", "CY": "EUR", "HR": "EUR", "LV": "EUR", "LT": "EUR",
		"LU": "EUR", "MT": "EUR", "MC": "EUR", "ME": "EUR", "SM": "EUR",
		"SK": "EUR", "SI": "EUR", "AD": "EUR", "EE": "EUR",
		"AE": "AED", "AR": "ARS", "BH": "BHD", "BM": "BMD", "BO": "BOB",
		"BQ": "USD", "CL": "CLP", "CO": "COP", "GU": "USD", "IL": "ILS",
		"PR": "USD", "TR": "TRY", "UA": "UAH", "UM": "USD", "ZA": "ZAR",
	} {
		if got := CurrencyForCountry(country); got != want {
			t.Fatalf("CurrencyForCountry(%q) = %q, want %q", country, got, want)
		}
	}
}

func TestPaymentModes(t *testing.T) {
	if len(PaymentModes) != len(PaymentModeOrder) {
		t.Fatalf("PaymentModes(%d) and PaymentModeOrder(%d) length mismatch", len(PaymentModes), len(PaymentModeOrder))
	}
	for _, k := range PaymentModeOrder {
		if _, ok := PaymentModes[k]; !ok {
			t.Fatalf("order key %q not in PaymentModes", k)
		}
	}
	if m := PaymentModes["试用短链 PayPal US/USD"]; !m.TrialShortLink || m.Country != "US" {
		t.Fatalf("trial mode wrong: %+v", m)
	}
	if m := PaymentModes["GoPay 长链接 ID/IDR"]; m.PaymentProvider != "gopay" {
		t.Fatalf("gopay mode wrong: %+v", m)
	}
	if m := PaymentModes["Apple Pay 支付页 JP/JPY"]; !m.ApplePayHosted || m.Currency != "JPY" {
		t.Fatalf("apple mode wrong: %+v", m)
	}
}

// TestNormalizeUSPhoneForFormPythonParity pins normalize_us_phone_for_form
// (app.py:1940-1944) on Unicode digits.
//
// Python's \D is "not category Nd", so a fullwidth or Arabic-Indic digit is
// KEPT; Go's RE2 \D is [^0-9] and would delete it. Every expectation below was
// computed by running app.py:1940-1944 verbatim under CPython 3.12, and every
// one of them is an input where the ASCII-only version answered differently.
//
// This is not academic: deleting digits shortens the number, which changes
// whether the 11-digit country-code strip fires, which changes what gets typed
// into OpenAI's phone field for a number the operator is being billed for.
func TestNormalizeUSPhoneForFormPythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"\uff10\uff11\uff12\uff13\uff14\uff15\uff16\uff17\uff18\uff19", "\uff10\uff11\uff12\uff13\uff14\uff15\uff16\uff17\uff18\uff19"},
		{"\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u0660", "\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u0660"},
		{"\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u0660\u2000+1\u00a02025550123\u000c", "\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u066012025550123"},
		{"+1(202)555-0123\u00a0\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u0660\u001e", "12025550123\u0660\u0661\u0662\u0663\u0664\u0665\u0666\u0667\u0668\u0669\u0660"},
		{"+1 (202) 555-0123", "2025550123"},
		{"\uff11\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", "\uff11\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13"},
		{"\u0661\u0662\u0660\u0662\u0665\u0665\u0665\u0660\u0661\u0662\u0663", "\u0661\u0662\u0660\u0662\u0665\u0665\u0665\u0660\u0661\u0662\u0663"},
		{"1\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13", "\uff12\uff10\uff12\uff15\uff15\uff15\uff10\uff11\uff12\uff13"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeUSPhoneForForm(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
}

// TestClassifyPhoneRejectionCollapsePythonParity pins the
// re.sub(r"\s+", " ", ...).strip() half of classify_phone_rejection
// (app.py:3879).
//
// Go's RE2 \s is [\t\n\f\r ]: no VT and none of U+001C-U+001F, all five of
// which Python's \s matches. Leaving one uncollapsed splits a marker phrase in
// two, so "already used" stops matching and the rejection degrades to the
// generic status — which is what decides whether the number is retried.
func TestClassifyPhoneRejectionCollapsePythonParity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"SMS\u000bcode", "SMS code"},
		{"Route\u001fError", "Route Error"},
		{"\u001cAlready\u001dUsed\u001e", "Already Used"},
		{"a\u001cb", "a b"},
		{"\u001f\u001c ", ""},
		{"already\u000bused", "already used"},
		{"phone \u001e already used", "phone already used"},
	}
	for _, tt := range tests {
		if _, got := ClassifyPhoneRejection(tt.in); got != tt.want {
			t.Errorf("%+v: got %+v, python says %+v", tt.in, got, tt.want)
		}
	}
	// The separators are inside the marker in every case above; this one proves
	// the status follows the collapsed text, not just the text.
	if status, _ := ClassifyPhoneRejection("phone \u001e already used"); status != "\u624b\u673a\u53f7\u5df2\u4f7f\u7528" {
		t.Errorf("separator-split marker not matched: status = %q", status)
	}
}
