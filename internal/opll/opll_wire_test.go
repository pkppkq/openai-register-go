package opll

// Wire-format and Python-semantics regression tests added by the differential
// sweep of internal/opll against app.py.
//
// EVERY expected value below was produced by EXECUTING the verbatim app.py
// source slice under CPython 3.12 — the request builders were driven through a
// fake requests.Session that captured the exact `data=` / `json=` payload and
// re-encoded it the way requests does. Nothing here was written by reading the
// Python and guessing.
//
// These tests exist because this package's output is a REAL PAYMENT LINK and
// the bytes it puts on the wire go to chatgpt.com and api.stripe.com, both of
// which fingerprint their clients.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// JSON request bodies (app.py 3519-3525, 4523)
// ---------------------------------------------------------------------------

// TestCreateCheckoutBodyBytes pins the checkout-create body byte-for-byte
// against json.dumps of the app.py 3519-3525 dict literal. Key order, the
// ", "/": " separators and the `false` spelling are all part of the
// fingerprint; a Go struct + encoding/json would sort nothing but WOULD escape
// differently and drop the spaces.
func TestCreateCheckoutBodyBytes(t *testing.T) {
	cases := []struct{ country, currency, want string }{
		{"US", "USD", `{"entry_point": "all_plans_pricing_modal", "plan_name": "chatgptplusplan", "billing_details": {"country": "US", "currency": "USD"}, "promo_campaign": {"promo_campaign_id": "plus-1-month-free", "is_coupon_from_query_param": false}, "checkout_ui_mode": "custom"}`},
		{"DE", "EUR", `{"entry_point": "all_plans_pricing_modal", "plan_name": "chatgptplusplan", "billing_details": {"country": "DE", "currency": "EUR"}, "promo_campaign": {"promo_campaign_id": "plus-1-month-free", "is_coupon_from_query_param": false}, "checkout_ui_mode": "custom"}`},
		{"JP", "JPY", `{"entry_point": "all_plans_pricing_modal", "plan_name": "chatgptplusplan", "billing_details": {"country": "JP", "currency": "JPY"}, "promo_campaign": {"promo_campaign_id": "plus-1-month-free", "is_coupon_from_query_param": false}, "checkout_ui_mode": "custom"}`},
	}
	for _, c := range cases {
		if got := createCheckoutBody(c.country, c.currency); got != c.want {
			t.Errorf("createCheckoutBody(%q,%q) =\n %s\nPython says\n %s", c.country, c.currency, got, c.want)
		}
	}
}

// TestApproveBodyIsPythonJSONDumps pins the approve body (app.py 4523). Both
// fields come STRAIGHT OFF THE WIRE (cs_id and processor_entity are echoed
// from the checkout response), so the escaping rules matter:
//
//	json.dumps       leaves & < > literal and escapes every non-ASCII rune
//	encoding/json    escapes & < > and emits non-ASCII raw
//
// Using encoding/json here sent chatgpt.com a body Python would never send.
// Expectations recomputed under CPython 3.12 via opll_chatgpt_approve itself.
func TestApproveBodyIsPythonJSONDumps(t *testing.T) {
	cases := []struct{ csID, entity, want string }{
		{"cs_live_1", "openai_llc", `{"checkout_session_id": "cs_live_1", "processor_entity": "openai_llc"}`},
		// & < > stay literal — encoding/json wrote & < >.
		{"cs&<>_1", "ent中", `{"checkout_session_id": "cs&<>_1", "processor_entity": "ent\u4e2d"}`},
		{"cs\"x", `a\b`, `{"checkout_session_id": "cs\"x", "processor_entity": "a\\b"}`},
		// U+00A0 and DEL are escaped; encoding/json emitted both raw.
		{"cs ", "", `{"checkout_session_id": "cs\u00a0", "processor_entity": "\u007f"}`},
		// Astral planes become a SURROGATE PAIR.
		{"cs\U0001f600", "e", `{"checkout_session_id": "cs\ud83d\ude00", "processor_entity": "e"}`},
	}
	for _, c := range cases {
		if got := approveBody(c.csID, c.entity); got != c.want {
			t.Errorf("approveBody(%q,%q) =\n %s\nPython says\n %s", c.csID, c.entity, got, c.want)
		}
	}
}

// TestPyJSONStringMatchesJSONDumps pins json.dumps() of a str for the shapes
// encoding/json disagrees on.
func TestPyJSONStringMatchesJSONDumps(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"a&b<c>d", `"a&b<c>d"`},
		{"", `"\u0085"`},
		{" ", `"\u00a0"`},
		{" ", `"\u1680"`},
		{" ", `"\u2028"`},
		{" ", `"\u202f"`},
		{"", `"\u007f"`},
		{"", `"\u001f"`},
		{"\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"中", `"\u4e2d"`},
		{"\U0001f600", `"\ud83d\ude00"`},
		{`"\`, `"\"\\"`},
		{" ~", `" ~"`},
	}
	for _, c := range cases {
		if got := pyJSONString(c.in); got != c.want {
			t.Errorf("pyJSONString(%q) = %s, Python json.dumps says %s", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Form bodies (app.py 3591-3605, 4137-4164, 4670-4705, 4561-4574)
// ---------------------------------------------------------------------------

const stripeVersionEncoded = "2025-03-31.basil%3B+checkout_server_update_beta%3Dv1%3B+checkout_manual_approval_preview%3Dv1"

// TestStripeInitFormBytes pins the /init body (app.py 3591-3605). requests
// urlencodes a dict in INSERTION order; url.Values.Encode() sorts keys, which
// would reorder every field of every Stripe request.
func TestStripeInitFormBytes(t *testing.T) {
	const jsID = "11111111-1111-4111-8111-111111111111"
	want := "browser_locale=en-US&browser_timezone=Asia%2FShanghai" +
		"&elements_session_client%5Bclient_betas%5D%5B0%5D=custom_checkout_server_updates_1" +
		"&elements_session_client%5Bclient_betas%5D%5B1%5D=custom_checkout_manual_approval_1" +
		"&elements_session_client%5Belements_init_source%5D=custom_checkout" +
		"&elements_session_client%5Breferrer_host%5D=chatgpt.com" +
		"&elements_session_client%5Bstripe_js_id%5D=" + jsID +
		"&elements_session_client%5Blocale%5D=en" +
		"&elements_session_client%5Bis_aggregation_expected%5D=false" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_save%5D=never" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_redisplay%5D=never" +
		"&key=pk_live_x&_stripe_version=" + stripeVersionEncoded
	if got := stripeInitForm("en-US", "en", jsID, "pk_live_x").Encode(); got != want {
		t.Errorf("init form =\n %s\nPython says\n %s", got, want)
	}
	// locale_parts drives both locale fields; an unknown locale falls back to en.
	bl, el := localeParts("zh-CN")
	got := stripeInitForm(bl, el, jsID, "pk_live_x").Encode()
	if !strings.Contains(got, "browser_locale=zh-CN&") ||
		!strings.Contains(got, "elements_session_client%5Blocale%5D=zh-CN&") {
		t.Errorf("zh-CN init form = %s", got)
	}
	bl, el = localeParts("xx")
	if bl != "en-US" || el != "en" {
		t.Errorf("localeParts(xx) = %q/%q, Python says en-US/en", bl, el)
	}
}

// TestPaymentMethodFormBytes pins the /v1/payment_methods body (app.py 4137-4164).
func TestPaymentMethodFormBytes(t *testing.T) {
	ctx := &stripeCtx{
		StripeJSID: "JS-ID", ElementsSessionID: "elements_session_abc",
		ElementsSessionConfigID: "ES-CFG", ConfigID: "CFG", RuntimeVersion: "RV",
	}
	billing := billingDetails{Name: "A B", Email: "a.b@example.com", Phone: "+1123",
		Country: "US", Line1: "L1", City: "C", State: "S", PostalCode: "P"}
	const tail = "&payment_user_agent=stripe.js%2FRV%3B+stripe-js-v3%2FRV%3B+payment-element%3B+deferred-intent" +
		"&referrer=https%3A%2F%2Fchatgpt.com&time_on_page=25000" +
		"&client_attribution_metadata%5Bcheckout_session_id%5D=cs_live_1" +
		"&client_attribution_metadata%5Bclient_session_id%5D=JS-ID" +
		"&client_attribution_metadata%5Bcheckout_config_id%5D=CFG" +
		"&client_attribution_metadata%5Belements_session_id%5D=elements_session_abc" +
		"&client_attribution_metadata%5Belements_session_config_id%5D=ES-CFG" +
		"&client_attribution_metadata%5Bmerchant_integration_source%5D=elements" +
		"&client_attribution_metadata%5Bmerchant_integration_subtype%5D=payment-element" +
		"&client_attribution_metadata%5Bmerchant_integration_version%5D=2021" +
		"&client_attribution_metadata%5Bpayment_intent_creation_flow%5D=deferred" +
		"&client_attribution_metadata%5Bpayment_method_selection_flow%5D=automatic" +
		"&client_attribution_metadata%5Bmerchant_integration_additional_elements%5D%5B0%5D=payment" +
		"&client_attribution_metadata%5Bmerchant_integration_additional_elements%5D%5B1%5D=address"

	want := "billing_details%5Bname%5D=A+B&billing_details%5Bemail%5D=a.b%40example.com" +
		"&billing_details%5Bphone%5D=%2B1123&billing_details%5Baddress%5D%5Bcountry%5D=US" +
		"&billing_details%5Baddress%5D%5Bline1%5D=L1&billing_details%5Baddress%5D%5Bcity%5D=C" +
		"&billing_details%5Baddress%5D%5Bpostal_code%5D=P&billing_details%5Baddress%5D%5Bstate%5D=S" +
		"&type=paypal" + tail + "&key=pk_live_x&_stripe_version=" + stripeVersionEncoded
	if got := paymentMethodForm("cs_live_1", ctx, billing, "pk_live_x", "paypal", "RV", "25000").Encode(); got != want {
		t.Errorf("pm form =\n %s\nPython says\n %s", got, want)
	}

	// Every `or` default in the Python dict fires on an empty billing profile,
	// EXCEPT phone, whose default is the empty string.
	wantEmpty := "billing_details%5Bname%5D=John+Doe&billing_details%5Bemail%5D=buyer%40example.com" +
		"&billing_details%5Bphone%5D=&billing_details%5Baddress%5D%5Bcountry%5D=US" +
		"&billing_details%5Baddress%5D%5Bline1%5D=3110+Sunset+Boulevard" +
		"&billing_details%5Baddress%5D%5Bcity%5D=Los+Angeles" +
		"&billing_details%5Baddress%5D%5Bpostal_code%5D=90026" +
		"&billing_details%5Baddress%5D%5Bstate%5D=CA&type=paypal" + tail +
		"&key=" + defaultStripePK + "&_stripe_version=" + stripeVersionEncoded
	if got := paymentMethodForm("cs_live_1", ctx, billingDetails{}, "", "paypal", "RV", "25000").Encode(); got != wantEmpty {
		t.Errorf("pm form (empty billing) =\n %s\nPython says\n %s", got, wantEmpty)
	}
}

// TestConfirmFormBytes pins the /confirm body (app.py 4670-4705). This is the
// request that actually creates the PayPal billing agreement, so a reordered
// or differently-escaped field is a failed (or wrong) charge setup.
func TestConfirmFormBytes(t *testing.T) {
	ctx := &stripeCtx{
		StripeJSID: "JS-ID", ElementsSessionID: "elements_session_abc",
		ElementsSessionConfigID: "ES-CFG", ConfigID: "CFG", Locale: "en", RuntimeVersion: "RV",
	}
	returnURL := stripeConfirmReturnURL("cs_live_1",
		Checkout{BillingCountry: "US", ProcessorEntity: "openai_llc"},
		"https://checkout.stripe.com/c/pay/cs_live_1")
	const wantReturnURL = "https://pay.openai.com/c/pay/cs_live_1?success_return_url=" +
		"https%3A%2F%2Fchatgpt.com%2Fcheckout%2Fverify%3Fstripe_session_id%3Dcs_live_1" +
		"%26processor_entity%3Dopenai_llc%26plan_type%3Dplus"
	if returnURL != wantReturnURL {
		t.Fatalf("return_url = %q, Python says %q", returnURL, wantReturnURL)
	}
	want := "guid=11111111111141118111111111111111&muid=22222222222242228222222222222222" +
		"&sid=33333333333343338333333333333333&payment_method=pm_1&init_checksum=IPCHK" +
		"&version=RV&expected_amount=2000&expected_payment_method_type=paypal" +
		"&return_url=https%3A%2F%2Fpay.openai.com%2Fc%2Fpay%2Fcs_live_1%3Fsuccess_return_url%3D" +
		"https%253A%252F%252Fchatgpt.com%252Fcheckout%252Fverify%253Fstripe_session_id%253Dcs_live_1" +
		"%2526processor_entity%253Dopenai_llc%2526plan_type%253Dplus" +
		"&elements_session_client%5Bsession_id%5D=elements_session_abc" +
		"&elements_session_client%5Blocale%5D=en" +
		"&elements_session_client%5Breferrer_host%5D=chatgpt.com" +
		"&elements_session_client%5Bis_aggregation_expected%5D=false" +
		"&elements_session_client%5Belements_init_source%5D=custom_checkout" +
		"&elements_session_client%5Bstripe_js_id%5D=JS-ID" +
		"&elements_session_client%5Bclient_betas%5D%5B0%5D=custom_checkout_server_updates_1" +
		"&elements_session_client%5Bclient_betas%5D%5B1%5D=custom_checkout_manual_approval_1" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_save%5D=never" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_redisplay%5D=never" +
		"&client_attribution_metadata%5Bclient_session_id%5D=JS-ID" +
		"&client_attribution_metadata%5Bcheckout_session_id%5D=cs_live_1" +
		"&client_attribution_metadata%5Bcheckout_config_id%5D=CFG" +
		"&client_attribution_metadata%5Belements_session_id%5D=elements_session_abc" +
		"&client_attribution_metadata%5Belements_session_config_id%5D=ES-CFG" +
		"&client_attribution_metadata%5Bmerchant_integration_source%5D=checkout" +
		"&client_attribution_metadata%5Bmerchant_integration_subtype%5D=payment-element" +
		"&client_attribution_metadata%5Bmerchant_integration_version%5D=custom" +
		"&client_attribution_metadata%5Bpayment_intent_creation_flow%5D=deferred" +
		"&client_attribution_metadata%5Bpayment_method_selection_flow%5D=automatic" +
		"&client_attribution_metadata%5Bmerchant_integration_additional_elements%5D%5B0%5D=payment" +
		"&client_attribution_metadata%5Bmerchant_integration_additional_elements%5D%5B1%5D=address" +
		"&consent%5Bterms_of_service%5D=accepted&key=pk_live_x&_stripe_version=" + stripeVersionEncoded
	got := confirmForm("cs_live_1", "pm_1", "pk_live_x", "IPCHK", "RV", "2000", "paypal", returnURL, ctx,
		"11111111111141118111111111111111", "22222222222242228222222222222222", "33333333333343338333333333333333").Encode()
	if got != want {
		t.Errorf("confirm form =\n %s\nPython says\n %s", got, want)
	}
}

// TestPaymentPageParamsBytes pins the /payment_pages poll query (app.py 4561-4574).
func TestPaymentPageParamsBytes(t *testing.T) {
	ctx := &stripeCtx{StripeJSID: "JS-ID", ElementsSessionID: "elements_session_abc"}
	want := "elements_session_client%5Bclient_betas%5D%5B0%5D=custom_checkout_server_updates_1" +
		"&elements_session_client%5Bclient_betas%5D%5B1%5D=custom_checkout_manual_approval_1" +
		"&elements_session_client%5Belements_init_source%5D=custom_checkout" +
		"&elements_session_client%5Breferrer_host%5D=chatgpt.com" +
		"&elements_session_client%5Bsession_id%5D=elements_session_abc" +
		"&elements_session_client%5Bstripe_js_id%5D=JS-ID" +
		"&elements_session_client%5Blocale%5D=en" +
		"&elements_session_client%5Bis_aggregation_expected%5D=false" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_save%5D=never" +
		"&elements_options_client%5Bsaved_payment_method%5D%5Benable_redisplay%5D=never" +
		"&key=pk_live_x&_stripe_version=" + stripeVersionEncoded
	if got := paymentPageParams(ctx, "pk_live_x", "en").Encode(); got != want {
		t.Errorf("poll params =\n %s\nPython says\n %s", got, want)
	}
	// A nil ctx synthesises both ids rather than sending blanks.
	got := paymentPageParams(nil, "pk_live_x", "en").Encode()
	if !strings.Contains(got, "elements_session_client%5Bsession_id%5D=elements_session_") ||
		strings.Contains(got, "elements_session_client%5Bstripe_js_id%5D=&") {
		t.Errorf("nil-ctx poll params = %s", got)
	}
}

// TestChatGPTSessionHeaderOrder pins the sixteen session headers of
// opll_build_chatgpt_session (app.py 2824-2841) in Python's dict-insertion
// order. requests emits them in that order and these endpoints fingerprint the
// client, so the sequence is part of the request identity.
func TestChatGPTSessionHeaderOrder(t *testing.T) {
	want := []string{
		"user-agent", "accept", "accept-language", "authorization", "origin", "referer",
		"content-type", "oai-device-id", "oai-language", "sec-ch-ua", "sec-ch-ua-mobile",
		"sec-ch-ua-platform", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "cookie",
	}
	sess, err := newChatGPTSession("tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.headers) != len(want) {
		t.Fatalf("header count = %d, Python sets %d", len(sess.headers), len(want))
	}
	for i, k := range want {
		if sess.headers[i].K != k {
			t.Fatalf("header %d = %q, Python has %q", i, sess.headers[i].K, k)
		}
	}
	// oai-device-id and the cookie must be the SAME uuid4.
	var deviceID, cookie string
	for _, p := range sess.headers {
		switch p.K {
		case "oai-device-id":
			deviceID = p.V
		case "cookie":
			cookie = p.V
		}
	}
	if cookie != "oai-did="+deviceID {
		t.Fatalf("cookie %q does not carry oai-device-id %q", cookie, deviceID)
	}
	if _, err := newChatGPTSession("   ", ""); err == nil {
		t.Fatal("a blank access token must raise, as in Python")
	}
	stripe, err := newStripeSession("")
	if err != nil {
		t.Fatal(err)
	}
	if len(stripe.headers) != 2 || stripe.headers[0].K != "user-agent" || stripe.headers[1].K != "accept-language" {
		t.Fatalf("stripe session headers = %+v, Python sets User-Agent then Accept-Language", stripe.headers)
	}
}
