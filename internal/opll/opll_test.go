package opll

import (
	"encoding/json"
	"errors"
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

// These cover only the pure (non-network) half of the port: the payload
// walkers, URL classifiers and encoders whose Python semantics are subtle.

func TestOrderedJSONAndRedirectExtraction(t *testing.T) {
	raw := []byte(`{"next_action":{"type":"redirect_to_url","redirect_to_url":{"url":"https://www.paypal.com/agreements/approve?ba_token=BA-123"}},"assets":["https://js.stripe.com/x.js"]}`)
	obj, err := decodeOrderedObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := extractRedirectToURL(obj); got != "https://www.paypal.com/agreements/approve?ba_token=BA-123" {
		t.Fatalf("redirect_to_url=%q", got)
	}

	// opll_extract_provider_redirect_url takes the FIRST external non-asset URL
	// in wire order, so JSON object order must survive decoding.
	obj2, _ := decodeOrderedObject([]byte(`{"a":{"url":"https://q.stripe.com/p.gif"},"b":{"url":"https://gopay.example/pay/1"},"c":{"url":"https://second.example/x"}}`))
	if got := extractProviderRedirectURL(obj2); got != "https://gopay.example/pay/1" {
		t.Fatalf("provider=%q", got)
	}
}

func TestURLClassifiers(t *testing.T) {
	if !OpllIsPaypalSuccessURL("https://www.paypal.com/agreements/approve?ba_token=BA-123") {
		t.Fatal("ba approve link must be a success url")
	}
	if OpllIsPaypalSuccessURL("https://www.paypal.com/agreements/approve") {
		t.Fatal("missing ba_token must not be a success url")
	}
	if !OpllIsPaypalSuccessURL("https://pm-redirects.stripe.com/foo") {
		t.Fatal("pm-redirects.stripe.com must be a success url")
	}
	if !isIgnoredResourceURL("https://js.stripe.com/x.js") || !isIgnoredResourceURL("https://cdn.example/a.woff2") {
		t.Fatal("stripe asset urls must be ignored")
	}
	if isIgnoredResourceURL("https://gopay.example/pay/1") {
		t.Fatal("provider url wrongly ignored")
	}
	if got := toOpenAIPayURL("https://checkout.stripe.com/c/pay/cs_live_1#x"); got != "https://pay.openai.com/c/pay/cs_live_1#x" {
		t.Fatalf("pay url=%q", got)
	}
}

func TestStripeAmountInfo(t *testing.T) {
	cases := []struct {
		body, amount, source string
	}{
		{`{"total_summary":{"due":2000}}`, "2000", "total_summary.due"},
		{`{"invoice":{"amount_due":0}}`, "0", "invoice.amount_due"},
		{`{"line_items":[{"amount":100},{"amount":25}]}`, "125", "line_items.amount"},
		{`{}`, "0", "fallback_zero"},
	}
	for _, c := range cases {
		obj, err := decodeOrderedObject([]byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		amount, source := stripeAmountInfo(obj)
		if amount != c.amount || source != c.source {
			t.Fatalf("%s -> %q/%q, want %q/%q", c.body, amount, source, c.amount, c.source)
		}
	}
}

func TestPaymentMethodDetection(t *testing.T) {
	obj, _ := decodeOrderedObject([]byte(`{"payment_method_types":["card","paypal","apple-pay"]}`))
	if err := requirePaymentMethod(obj, "paypal"); err != nil {
		t.Fatalf("paypal should be supported: %v", err)
	}
	err := requirePaymentMethod(obj, "gopay")
	if err == nil {
		t.Fatal("expected PaymentMethodNotSupportedError")
	}
	if err.Error() != "当前 checkout 不支持 gopay; 可用支付方式: apple_pay,card,paypal" {
		t.Fatalf("msg=%q", err.Error())
	}
	if !OpllIsNonRetryableLinkError(err) || OpllNonRetryableStatus(err) != "支付方式不支持" {
		t.Fatal("unsupported method must be non-retryable")
	}
}

func TestConfirmReturnURLAndCheckoutFromURL(t *testing.T) {
	got := stripeConfirmReturnURL("cs_live_1", Checkout{BillingCountry: "US"}, "https://checkout.stripe.com/c/pay/cs_live_1?a=b")
	want := "https://pay.openai.com/c/pay/cs_live_1?a=b&success_return_url=https%3A%2F%2Fchatgpt.com%2Fcheckout%2Fverify%3Fstripe_session_id%3Dcs_live_1%26processor_entity%3Dopenai_llc%26plan_type%3Dplus"
	if got != want {
		t.Fatalf("confirm return url=\n got %q\nwant %q", got, want)
	}

	c, err := OpllCheckoutFromURL("https://pay.openai.com/c/pay/cs_live_abc?processor_entity=openai_llc", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.CSID != "cs_live_abc" || c.ProcessorEntity != "openai_llc" || c.BillingCountry != "US" || c.Currency != "USD" {
		t.Fatalf("checkout=%+v", c)
	}
	if _, err := OpllCheckoutFromURL("https://example.com/none", "US", "USD"); err == nil {
		t.Fatal("expected a Stripe-session-id extraction error")
	}
}

func TestAmountCheckAndShortError(t *testing.T) {
	r := &LinkResult{StripeAmount: "0", StripeAmountSource: "total_summary.due"}
	err := applyAmountCheck(r, "2000")
	if err == nil || r.AmountCheck != "failed" {
		t.Fatalf("expected mismatch, got %v / %q", err, r.AmountCheck)
	}
	if err.Error() != "金额不匹配: 目标 2000, 实际 0" {
		t.Fatalf("mismatch msg=%q", err.Error())
	}
	if !OpllIsNonRetryableLinkError(err) || OpllNonRetryableStatus(err) != "金额不匹配" {
		t.Fatal("amount mismatch must be non-retryable")
	}

	ok := &LinkResult{StripeAmount: "2000"}
	if err := applyAmountCheck(ok, "2000"); err != nil || ok.AmountCheck != "passed" {
		t.Fatalf("passed check failed: %v %q", err, ok.AmountCheck)
	}
	skipped := &LinkResult{StripeAmount: "2000"}
	if err := applyAmountCheck(skipped, ""); err != nil || skipped.AmountCheck != "skipped" {
		t.Fatalf("skipped check failed: %v %q", err, skipped.AmountCheck)
	}

	if got := OpllShortErrorDefault("a   b\n c"); got != "a b c" {
		t.Fatalf("short=%q", got)
	}
	// Python clips by characters, not bytes.
	if got := OpllShortError("金额不匹配金额不匹配", 6); got != "金额不..." {
		t.Fatalf("rune clip=%q", got)
	}
}

func TestSubmissionAttemptSearchSkipsEmptyDict(t *testing.T) {
	obj, _ := decodeOrderedObject([]byte(`{"x":{"submission_attempt":{}},"y":{"submission_attempt":{"state":"failed","error":{"message":"boom"}}}}`))
	sub := findSubmissionAttempt(obj)
	if submissionState(sub) != "failed" {
		t.Fatalf("an empty submission_attempt must not stop the search: %v", sub.Keys())
	}
	if fields := submissionAttemptFailureFields(sub); fields["error"] != "boom" {
		t.Fatalf("fields=%v", fields)
	}
}

func TestCurrencyAndComboOrder(t *testing.T) {
	// COUNTRY_CURRENCY includes the two update() passes app.py applies after
	// the literal that internal/models carries.
	for country, want := range map[string]string{"GR": "EUR", "ZA": "ZAR", "JP": "JPY", "XX": "USD"} {
		if got := currencyForCountry(country); got != want {
			t.Fatalf("currency %s=%s want %s", country, got, want)
		}
	}
	if got := comboAttemptOrder("DE"); len(got) != 4 || got[0] != [2]string{"DE", "DE"} || got[3] != [2]string{"US", "DE"} {
		t.Fatalf("combo=%v", got)
	}
	if got := comboAttemptOrder("zz"); len(got) != 1 || got[0] != [2]string{"US", "US"} {
		t.Fatalf("combo=%v", got)
	}
}

func TestBillingForEverySupportedCountry(t *testing.T) {
	for code := range openAISupportedCountryCodes {
		b, err := billingForCountry(code)
		if err != nil {
			t.Fatalf("billing %s: %v", code, err)
		}
		if b.Country != code || b.Name == "" || b.Line1 == "" || b.City == "" || b.PostalCode == "" || b.Phone == "" {
			t.Fatalf("billing %s incomplete: %+v", code, b)
		}
	}
}

func TestFormEncodingKeepsInsertionOrder(t *testing.T) {
	f := formPairs{{"z", "1"}, {"a", "2"}, {"k[0]", "a b"}}
	if got := f.Encode(); got != "z=1&a=2&k%5B0%5D=a+b" {
		t.Fatalf("form=%q", got)
	}
}

func TestExtractAccessTokenFromSessionText(t *testing.T) {
	if got := extractAccessTokenFromSessionText(`{"session":{"accessToken":"tok123"}}`); got != "tok123" {
		t.Fatalf("token=%q", got)
	}
	if got := extractAccessTokenFromSessionText("Bearer  abc"); got != "abc" {
		t.Fatalf("bearer=%q", got)
	}
	if got := extractAccessTokenFromSessionText("not-a-token"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// TestTrialEligibleAmounts pins the eligibility predicate from app.py:3562.
// It is a SET over three spellings of zero, unlike opll_apply_amount_check's
// exact-string compare — Stripe reports the amount differently per entity, and
// accepting only "0" would bill a user who was in fact eligible for the free
// trial (or vice versa). Getting this wrong is a money bug, so it is pinned.
//
// It exercises the predicate as DetectPlusTrialEligibility applies it
// (pyStrip + set lookup), not the bare map, so a change to either half is
// caught. Expectations recomputed from app.py:3562 under CPython 3.12.
func trialEligible(amount string) bool { return trialEligibleAmounts[pyStrip(amount)] }

func TestTrialEligibleAmounts(t *testing.T) {
	for _, free := range []string{"0", "0.0", "0.00", " 0.00 ", "\x1c0\x1c", " 0 ", "　0　"} {
		if !trialEligible(free) {
			t.Errorf("%q should count as eligible (free)", free)
		}
	}
	for _, paid := range []string{"", "0.000", "00", "1", "0.01", "20.00", "zero", "-0", "０"} {
		if trialEligible(paid) {
			t.Errorf("%q must NOT count as eligible", paid)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression tests for the Python-semantics defects found by differentially
// executing the verbatim app.py slices against this package. Every expected
// value below was COMPUTED by running the corresponding app.py lines under
// CPython 3.12, not guessed.
// ---------------------------------------------------------------------------

// pythonWhitespace is the exact set CPython 3.12 treats as whitespace for both
// str.strip() and the re module's \s (enumerated over all 0x110000 code points).
var pythonWhitespace = []rune{
	0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x1C, 0x1D, 0x1E, 0x1F, 0x20, 0x85, 0xA0,
	0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007,
	0x2008, 0x2009, 0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000,
}

func TestPythonWhitespaceSet(t *testing.T) {
	want := map[rune]bool{}
	for _, r := range pythonWhitespace {
		want[r] = true
		if !pyIsSpace(r) {
			t.Errorf("pyIsSpace(%#04x) = false, Python says whitespace", r)
		}
		if got := pyStrip(string(r) + "x" + string(r)); got != "x" {
			t.Errorf("pyStrip does not strip %#04x: %q", r, got)
		}
		if got := whitespaceRe.ReplaceAllString("a"+string(r)+"b", " "); got != "a b" {
			t.Errorf("whitespaceRe does not collapse %#04x: %q", r, got)
		}
		if got := urlInTextRe.FindAllString("https://a/x"+string(r)+"https://b/y", -1); len(got) != 2 {
			t.Errorf("urlInTextRe does not break on %#04x: %q", r, got)
		}
	}
	// Nothing outside the set may be treated as whitespace.
	for r := rune(0); r < 0x3100; r++ {
		if pyIsSpace(r) != want[r] {
			t.Fatalf("pyIsSpace(%#04x) = %v, want %v", r, pyIsSpace(r), want[r])
		}
	}
	if pyIsSpace(0x180E) || pyIsSpace(0x200B) {
		t.Fatal("U+180E / U+200B are not whitespace in Python 3.12")
	}
}

// TestAmountStringIsPythonStr covers str() of the JSON number Stripe returns.
// json.Number keeps the wire literal; Python already converted it to an int or
// a float, so the spelling differs — and this string is compared against the
// operator's target amount and re-sent to Stripe as expected_amount.
func TestAmountStringIsPythonStr(t *testing.T) {
	cases := []struct{ body, amount string }{
		{`{"total_summary":{"due":2000}}`, "2000"},
		{`{"total_summary":{"due":2000.00}}`, "2000.0"},
		{`{"total_summary":{"due":0.00}}`, "0.0"},
		{`{"total_summary":{"due":2e3}}`, "2000.0"},
		{`{"total_summary":{"due":2E3}}`, "2000.0"},
		{`{"total_summary":{"due":1e21}}`, "1e+21"},
		{`{"total_summary":{"due":1e16}}`, "1e+16"},
		{`{"total_summary":{"due":1e15}}`, "1000000000000000.0"},
		{`{"total_summary":{"due":1e-5}}`, "1e-05"},
		{`{"total_summary":{"due":0.0001}}`, "0.0001"},
		{`{"total_summary":{"due":-0}}`, "0"},
		{`{"total_summary":{"due":-0.0}}`, "-0.0"},
		{`{"total_summary":{"due":1e400}}`, "inf"},
		{`{"total_summary":{"due":20000000000000000000000}}`, "20000000000000000000000"},
	}
	for _, c := range cases {
		obj, err := decodeOrderedObject([]byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := stripeAmountInfo(obj); got != c.amount {
			t.Errorf("%s -> %q, Python says %q", c.body, got, c.amount)
		}
	}
}

// TestLineItemsSumIsArbitraryPrecision: Python ints do not overflow. An int64
// accumulator wrapped to a NEGATIVE amount, which would then be compared with
// target_amount and shipped to Stripe as expected_amount.
func TestLineItemsSumIsArbitraryPrecision(t *testing.T) {
	obj, err := decodeOrderedObject([]byte(`{"line_items":[{"amount":9223372036854775807},{"amount":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	amount, source := stripeAmountInfo(obj)
	if amount != "9223372036854775808" || source != "line_items.amount" {
		t.Fatalf("overflowed sum = %q/%q, Python says 9223372036854775808/line_items.amount", amount, source)
	}
	// int(float) truncates toward zero at float64 precision, as json.loads left it.
	obj2, _ := decodeOrderedObject([]byte(`{"line_items":[{"amount":1.4804002272970578e+20},{"amount":1}]}`))
	if got, _ := stripeAmountInfo(obj2); got != "148040022729705783297" {
		t.Fatalf("float line item = %q, Python says 148040022729705783297", got)
	}
	// int("\x1c12\x1c") raises in Python even though str.strip() would remove
	// those bytes, so the item is skipped and the fallback wins.
	// The JSON escape must stay ESCAPED in the fixture: a raw U+001C inside a
	// JSON string is invalid in both languages (json.loads raises
	// JSONDecodeError), which would make this test assert the decode failure
	// rather than the int() failure it is about.
	obj3, err := decodeOrderedObject([]byte(`{"line_items":[{"amount":"\u001c12\u001c"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, src := stripeAmountInfo(obj3); got != "0" || src != "fallback_zero" {
		t.Fatalf("separator-wrapped amount = %q/%q, Python says 0/fallback_zero", got, src)
	}
}

// TestApplyAmountCheckUsesPythonStrip: str.strip() removes U+001C-U+001F and
// strings.TrimSpace does not, so a target amount carrying one would have been
// rejected as 金额不匹配 against an identical Stripe amount.
func TestApplyAmountCheckUsesPythonStrip(t *testing.T) {
	r := &LinkResult{StripeAmount: "2000", StripeAmountSource: "total_summary.due"}
	if err := applyAmountCheck(r, "\x1c2000"); err != nil {
		t.Fatalf("target \\x1c2000 vs actual 2000 must pass: %v", err)
	}
	if r.TargetAmount != "2000" || r.AmountCheck != "passed" {
		t.Fatalf("target=%q check=%q, Python says 2000/passed", r.TargetAmount, r.AmountCheck)
	}
	r2 := &LinkResult{StripeAmount: "　0　"}
	if err := applyAmountCheck(r2, "0"); err != nil || r2.AmountCheck != "passed" {
		t.Fatalf("ideographic-space amount: %v %q", err, r2.AmountCheck)
	}
}

// TestShortErrorCollapsesPythonWhitespace: RE2's \s is [\t\n\f\r ]. A message
// carrying an NBSP kept it, so the collapsed text no longer contained the
// literal marker and a permanent failure was classified as retryable.
func TestShortErrorCollapsesPythonWhitespace(t *testing.T) {
	if got := OpllShortErrorDefault("amount mismatch"); got != "amount mismatch" {
		t.Fatalf("short=%q, Python says %q", got, "amount mismatch")
	}
	if !OpllIsNonRetryableLinkError(errors.New(OpllShortErrorDefault("amount mismatch"))) {
		t.Fatal("NBSP-separated 'amount mismatch' must classify as non-retryable")
	}
	if got := OpllShortErrorDefault("a\vb\x1cc"); got != "a b c" {
		t.Fatalf("short=%q, Python says %q", got, "a b c")
	}
	if got := OpllShortErrorDefault("\x1c\x1dtrim\x1e\x1f"); got != "trim" {
		t.Fatalf("short=%q, Python says %q", got, "trim")
	}
	if got := OpllShortError("abcdefgh", 0); got != "abcde..." {
		t.Fatalf("limit=0 -> %q, Python says %q", got, "abcde...")
	}
	// text[:limit-3] with limit < 3 is a NEGATIVE Python slice bound.
	if got := OpllShortError("abcdefgh", 1); got != "abcdef..." {
		t.Fatalf("limit=1 -> %q, Python says %q", got, "abcdef...")
	}
	if got := OpllShortError("abcdefgh", 3); got != "..." {
		t.Fatalf("limit=3 -> %q, Python says %q", got, "...")
	}
}

// TestCollectURLsBreaksOnPythonWhitespace: with RE2's narrower \s the URL
// swallowed the separator and everything after it, and the resulting glued
// string was returned as the provider redirect — i.e. as the payment long link.
func TestCollectURLsBreaksOnPythonWhitespace(t *testing.T) {
	obj, err := decodeOrderedObject([]byte(`{"blob":"https://a.example/x https://b.example/y"}`))
	if err != nil {
		t.Fatal(err)
	}
	urls := collectURLs(obj, nil)
	want := []string{"https://a.example/x", "https://b.example/y"}
	if len(urls) != 2 || urls[0] != want[0] || urls[1] != want[1] {
		t.Fatalf("urls=%q, Python says %q", urls, want)
	}
	if got := extractProviderRedirectURL(obj); got != want[0] {
		t.Fatalf("provider redirect=%q, Python says %q", got, want[0])
	}
}

// TestURLSplitMatchesPython pins the urlsplit behaviours net/url does not share.
func TestURLSplitMatchesPython(t *testing.T) {
	// netloc INCLUDES the userinfo, so this is not a PayPal URL to Python.
	const withUser = "https://u@paypal.com/agreements/approve?ba_token=BA-1"
	if isPaypalURL(withUser) || isPaypalBAApproveURL(withUser) || OpllIsPaypalSuccessURL(withUser) {
		t.Fatal("userinfo in netloc must stop the PayPal host match")
	}
	// ...and it stops the pay.openai.com rehost, which would drop credentials.
	const cred = "https://user:pw@checkout.stripe.com/c/pay/cs_1"
	if got := toOpenAIPayURL(cred); got != cred {
		t.Fatalf("rehosted a credentialed URL: %q", got)
	}
	// urlsplit strips leading C0/space and deletes tab/CR/LF anywhere; url.Parse
	// rejects the result outright.
	for _, u := range []string{"  https://checkout.stripe.com/c/pay/cs_1  ", "\x1fhttps://x/y", "https://x\t/y"} {
		if !isExternalURL(u) {
			t.Errorf("isExternalURL(%q) = false, Python says true", u)
		}
	}
	// url.Parse errors on these; urlsplit does not.
	for _, u := range []string{"https://x:notaport/y", "https://ex%2Fample.com/a%2Eb.png"} {
		if !isExternalURL(u) {
			t.Errorf("isExternalURL(%q) = false, Python says true", u)
		}
	}
	if !isIgnoredResourceURL("https://ex%2Fample.com/a%2Eb.png") {
		t.Error("percent-escaped path must still match the .png suffix")
	}
	// A leading NBSP is NOT stripped by urlsplit: the whole string is the path.
	if got := pyURLSplit(" https://js.stripe.com/x.js"); got.Scheme != "" || got.Netloc != "" {
		t.Fatalf("NBSP-led URL parsed as absolute: %+v", got)
	}
	if !isIgnoredResourceURL(" https://js.stripe.com/x.js") {
		t.Error("NBSP-led asset URL is still an ignored resource in Python (path ends .js)")
	}
}

// TestDecodeOrderedJSONRejectsTrailingData: json.Decoder stops after the first
// value, json.loads does not. Without the EOF check a truncated or
// garbage-suffixed response body decoded "successfully".
func TestDecodeOrderedJSONRejectsTrailingData(t *testing.T) {
	for _, bad := range []string{`{"a":1} garbage`, `"accessToken": "tok"`, `{} {}`, `1 2`} {
		if _, err := decodeOrderedJSON([]byte(bad)); err == nil {
			t.Errorf("decodeOrderedJSON(%q) accepted trailing data", bad)
		}
	}
	if _, err := decodeOrderedJSON([]byte("  {\"a\":1}\n ")); err != nil {
		t.Errorf("surrounding whitespace must stay valid: %v", err)
	}
	// The token extractor then reaches its regex fallback, as in Python.
	if got := extractAccessTokenFromSessionText(`"accessToken": "tok9"`); got != "tok9" {
		t.Fatalf("token=%q, Python says tok9", got)
	}
	// A document that PARSES but has no token returns "" — Python's `return
	// find_access_token(json.loads(raw))` is unconditional, so the bare-JWT
	// fallback below it is never reached.
	long := `{"user":{"email":"a.b.c@x.com"},"expires":"2026-07-26T00:00:00.000Z","authProvider":"auth0-x"}`
	if got := extractAccessTokenFromSessionText(long); got != "" {
		t.Fatalf("token=%q, Python says \"\"", got)
	}
}

// TestParseQSLLenientUnquote: url.QueryUnescape is all-or-nothing, so one bad
// escape made the whole value fall back to its RAW form with "+" unconverted —
// in exactly the values the link checks read (ba_token, success_return_url).
func TestParseQSLLenientUnquote(t *testing.T) {
	cases := []struct{ raw, key, val string }{
		{"a=+%zz", "a", " %zz"},
		{"a=%zz+b", "a", "%zz b"},
		{"a=%E4%B8%AD", "a", "中"},
		{"a=%2", "a", "%2"},
		{"a=%", "a", "%"},
		{"a=x%FFy", "a", "x�y"},
		{"a=%C3", "a", "�"},
		{"a=%E2%80", "a", "�"},
	}
	for _, c := range cases {
		pairs := parseQSL(c.raw)
		if len(pairs) != 1 || pairs[0].K != c.key || pairs[0].V != c.val {
			t.Errorf("parseQSL(%q) = %+v, Python says {%q %q}", c.raw, pairs, c.key, c.val)
		}
	}
	if !isPaypalBAApproveURL("https://www.paypal.com/agreements/approve?ba_token=+%zz") {
		t.Error("a ba_token with a bad escape still decodes to a non-blank value in Python")
	}
}

// TestPythonCaseMapping: str.lower()/upper() are FULL case mappings.
func TestPythonCaseMapping(t *testing.T) {
	if got := pyLower("İSK"); got != "i̇sk" {
		t.Fatalf("pyLower(İSK) = %q, Python says %q", got, "i̇sk")
	}
	if got := pyUpper("ß"); got != "SS" {
		t.Fatalf("pyUpper(ß) = %q, Python says SS", got)
	}
	// ...and "SS"/"FI" are real country codes, so the mapping decides the answer.
	if got := normalizeOpllCountry("ß"); got != "SS" {
		t.Fatalf("normalizeOpllCountry(ß) = %q, Python says SS", got)
	}
	if got := normalizeOpllCountry("ﬁ"); got != "FI" {
		t.Fatalf("normalizeOpllCountry(ﬁ) = %q, Python says FI", got)
	}
	if got := currencyForCountry("ﬁ"); got != "EUR" {
		t.Fatalf("currencyForCountry(ﬁ) = %q, Python says EUR", got)
	}
}

// TestCurrencyForCountryDoesNotStrip: currency_for_country has no .strip(), so
// an unnormalised country falls through to USD. models.CurrencyForCountry adds
// a TrimSpace and would answer JPY — a wrong-currency checkout.
func TestCurrencyForCountryDoesNotStrip(t *testing.T) {
	if got := currencyForCountry(" jp "); got != "USD" {
		t.Fatalf("currencyForCountry(\" jp \") = %q, Python says USD", got)
	}
	if got := currencyForCountry("jp"); got != "JPY" {
		t.Fatalf("currencyForCountry(\"jp\") = %q, Python says JPY", got)
	}
}

// TestPyReprMatchesPython pins str()/repr() of the container and float shapes
// that reach the diagnostics and the approve-result error message.
func TestPyReprMatchesPython(t *testing.T) {
	obj, err := decodeOrderedJSON([]byte(`{"b":1,"a":"x'y","c":[true,null,1.5],"d":{"e":" "}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{'b': 1, 'a': "x'y", 'c': [True, None, 1.5], 'd': {'e': '\xa0'}}`
	if got := pyStr(obj); got != want {
		t.Fatalf("pyStr =\n %s\nPython says\n %s", got, want)
	}
	if got := pyRepr("it's"); got != `"it's"` {
		t.Fatalf("pyRepr(it's) = %s, Python says \"it's\"", got)
	}
	// repr() escapes a non-printable rather than emitting it. want is a RAW
	// string holding the six characters \u2028; a real U+2028 there
	// would assert the opposite of what CPython 3.12 does.
	if got := pyRepr("a\u2028b"); got != `'a\u2028b'` {
		t.Fatalf("pyRepr = %s, Python escapes it", got)
	}
	// type(payload).__name__ distinguishes int from float.
	if got := stripePayloadDiagnostics(json.Number("1.5"), nil); got != "payload_type=float" {
		t.Fatalf("diagnostics=%q, Python says payload_type=float", got)
	}
	if got := stripePayloadDiagnostics(json.Number("2"), nil); got != "payload_type=int" {
		t.Fatalf("diagnostics=%q, Python says payload_type=int", got)
	}
}

// TestHeaderOrderIsDeterministic: fhttp emits any header missing from
// HeaderOrderKey in Go map-iteration order, so the sixteen ChatGPT session
// headers were reshuffled per request on endpoints that fingerprint clients.
func TestHeaderOrderIsDeterministic(t *testing.T) {
	base := func() http.Header {
		return http.Header{
			"sec-ch-ua":         {"x"},
			"user-agent":        {"x"},
			http.HeaderOrderKey: {"sec-ch-ua", "user-agent"},
		}
	}
	session := formPairs{{"user-agent", "ua"}, {"authorization", "Bearer t"}, {"cookie", "c"}, {"origin", "o"}}
	extra := formPairs{{"content-type", "application/json"}, {"referer", "r"}}
	want := []string{"sec-ch-ua", "user-agent", "authorization", "cookie", "origin", "content-type", "referer"}

	for i := 0; i < 200; i++ {
		got := mergeHeaders(base(), session, extra)[http.HeaderOrderKey]
		if len(got) != len(want) {
			t.Fatalf("order=%v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iteration %d: order=%v, want %v", i, got, want)
			}
		}
	}
	// An overriding header must not be listed twice.
	h := mergeHeaders(base(), formPairs{{"User-Agent", "ua2"}})
	if len(h[http.HeaderOrderKey]) != 2 || h["user-agent"][0] != "ua2" {
		t.Fatalf("override produced %v / %v", h[http.HeaderOrderKey], h["user-agent"])
	}
}

// TestHostedURLFallbackOrder: Python is str(A or B or "").strip() — the `or`
// chain runs BEFORE the strip, so an all-whitespace stripe_hosted_url wins the
// chain and then strips to "", and the checkout_url fallback is never used.
func TestHostedURLFallbackOrder(t *testing.T) {
	pick := func(initValue, fallback string) string {
		hosted := initValue
		if hosted == "" {
			hosted = fallback
		}
		return pyStrip(hosted)
	}
	if got := pick("   ", "https://pay.openai.com/c/pay/cs_1"); got != "" {
		t.Fatalf("whitespace-only stripe_hosted_url resolved to %q, Python says \"\"", got)
	}
	if got := pick("", "https://pay.openai.com/c/pay/cs_1"); got != "https://pay.openai.com/c/pay/cs_1" {
		t.Fatalf("missing stripe_hosted_url resolved to %q", got)
	}
}

// TestNonRetryableLadderOrderIsLoadBearing: the substring ladders are
// first-match-wins, so a message containing two markers must resolve to the
// EARLIER branch. Expectations recomputed from app.py:3472-3511.
func TestNonRetryableLadderOrderIsLoadBearing(t *testing.T) {
	cases := []struct{ text, status string }{
		{"金额不匹配 and token_invalidated", "金额不匹配"},
		{"token_invalidated and 金额不匹配", "金额不匹配"},
		{"试用短链必须通过浏览器 token_invalidated", "短链需浏览器"},
		{"当前 checkout 不支持 paypal + billing country must match request country", "支付方式不支持"},
		{"billing country must match request country + 当前 checkout 不支持 paypal", "支付方式不支持"},
		{"confirm_error_reason=payment_method_types_mismatch 金额不匹配", "金额不匹配"},
		{"billing country must match request country token_invalidated", "地区不匹配"},
		{"token_invalidated checkout does not support paypal", "支付方式不支持"},
		{"CHECKOUT DOES NOT SUPPORT PAYPAL", "支付方式不支持"},
		{"当前 checkout 不支持 gopay", "不可自动重试"},
		{"boom", "不可自动重试"},
	}
	for _, c := range cases {
		if got := OpllNonRetryableStatus(errors.New(c.text)); got != c.status {
			t.Errorf("status(%q) = %q, Python says %q", c.text, got, c.status)
		}
	}
	if OpllIsNonRetryableLinkError(errors.New("当前 checkout 不支持 gopay")) {
		t.Error("only the paypal spelling is a non-retryable marker")
	}
}
