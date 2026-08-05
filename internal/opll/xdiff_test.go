package opll

// SCRATCH differential driver — deleted at the end of the sweep.
// Run: go test ./internal/opll -run TestXDiff -count=1
// Reads $OPLL_DIFF_IN (JSON case list), writes $OPLL_DIFF_OUT (JSON answers).

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

type xcase struct {
	Fn   string            `json:"fn"`
	Args []json.RawMessage `json:"args"`
}

type xres struct {
	OK bool `json:"ok"`
	V  any  `json:"v"`
}

func xs(t *testing.T, raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("arg not a string: %s", raw)
	}
	return s
}

func xjson(t *testing.T, raw json.RawMessage) any {
	v, err := decodeOrderedJSON([]byte(xs(t, raw)))
	if err != nil {
		t.Fatalf("bad fixture json: %v", err)
	}
	return v
}

func xobjToMap(o *jsonObject) map[string]string {
	m := map[string]string{}
	if o == nil {
		return m
	}
	for _, k := range o.Keys() {
		m[k] = pyStrOr(o.Get(k))
	}
	return m
}

func TestXDiff(t *testing.T) {
	in := os.Getenv("OPLL_DIFF_IN")
	if in == "" {
		t.Skip("no OPLL_DIFF_IN")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var cases []xcase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	out := make([]xres, 0, len(cases))
	for _, c := range cases {
		a := c.Args
		var v any
		ok := true
		switch c.Fn {
		case "currency_for_country":
			v = currencyForCountry(xs(t, a[0]))
		case "normalize_opll_country":
			v = normalizeOpllCountry(xs(t, a[0]))
		case "locale_parts":
			b, e := localeParts(xs(t, a[0]))
			v = []string{b, e}
		case "opll_processor_entity_for_country":
			v = processorEntityForCountry(xs(t, a[0]), xs(t, a[1]))
		case "opll_chatgpt_success_return_url":
			v = chatgptSuccessReturnURL(xs(t, a[0]), xs(t, a[1]), xs(t, a[2]))
		case "opll_to_openai_pay_url":
			v = toOpenAIPayURL(xs(t, a[0]))
		case "opll_stripe_checkout_long_url":
			v = stripeCheckoutLongURL(xs(t, a[0]), xs(t, a[1]), xs(t, a[2]))
		case "opll_checkout_from_url":
			c2, err := OpllCheckoutFromURL(xs(t, a[0]), xs(t, a[1]), xs(t, a[2]))
			if err != nil {
				ok = false
				v = "RuntimeError: " + err.Error()
			} else {
				v = map[string]string{
					"cs_id": c2.CSID, "processor_entity": c2.ProcessorEntity,
					"stripe_publishable_key": c2.StripePublishableKey,
					"billing_country":        c2.BillingCountry, "currency": c2.Currency,
					"checkout_url": c2.CheckoutURL,
				}
			}
		case "opll_stripe_confirm_return_url":
			var ck struct {
				BillingCountry  string `json:"billing_country"`
				ProcessorEntity string `json:"processor_entity"`
			}
			_ = json.Unmarshal(a[1], &ck)
			v = stripeConfirmReturnURL(xs(t, a[0]), Checkout{BillingCountry: ck.BillingCountry, ProcessorEntity: ck.ProcessorEntity}, xs(t, a[2]))
		case "opll_is_external_url":
			v = isExternalURL(xs(t, a[0]))
		case "opll_is_paypal_url":
			v = isPaypalURL(xs(t, a[0]))
		case "opll_is_paypal_ba_approve_url":
			v = isPaypalBAApproveURL(xs(t, a[0]))
		case "opll_is_paypal_success_url":
			v = OpllIsPaypalSuccessURL(xs(t, a[0]))
		case "opll_is_ignored_resource_url":
			v = isIgnoredResourceURL(xs(t, a[0]))
		case "opll_collect_urls":
			got := collectURLs(xjson(t, a[0]), nil)
			if got == nil {
				got = []string{}
			}
			v = got
		case "opll_extract_redirect_to_url":
			v = extractRedirectToURL(xjson(t, a[0]))
		case "opll_extract_provider_redirect_url":
			v = extractProviderRedirectURL(xjson(t, a[0]))
		case "opll_stripe_amount_info":
			amt, src := stripeAmountInfo(xjson(t, a[0]))
			v = []string{amt, src}
		case "opll_collect_payment_method_names":
			v = sortedNames(collectPaymentMethodNames(xjson(t, a[0])))
		case "opll_require_payment_method":
			err := requirePaymentMethod(xjson(t, a[0]), xs(t, a[1]))
			if err == nil {
				v = ""
			} else {
				v = err.Error()
			}
		case "opll_submission_attempt_failure_fields":
			v = submissionAttemptFailureFields(xjson(t, a[0]))
		case "opll_find_submission_attempt":
			v = pyRepr(findSubmissionAttempt(xjson(t, a[0])))
		case "opll_stripe_payload_diagnostics":
			cobj := asObject(xjson(t, a[1]))
			v = stripePayloadDiagnostics(xjson(t, a[0]), &stripeCtx{ElementsSessionID: pyStrOr(cobj.Get("elements_session_id"))})
		case "opll_short_error":
			if len(a) > 1 {
				var lim int
				_ = json.Unmarshal(a[1], &lim)
				v = OpllShortError(xs(t, a[0]), lim)
			} else {
				v = OpllShortErrorDefault(xs(t, a[0]))
			}
		case "opll_stripe_error_summary":
			v = stripeErrorSummary(xs(t, a[0]), []byte(xs(t, a[1])))
		case "opll_first_non_empty":
			var m map[string]string
			_ = json.Unmarshal(a[0], &m)
			var keys []string
			_ = json.Unmarshal(a[1], &keys)
			v = firstNonEmptyField(m, keys...)
		case "opll_is_non_retryable_link_error":
			v = OpllIsNonRetryableLinkError(xerr(xs(t, a[0])))
		case "opll_non_retryable_status":
			v = OpllNonRetryableStatus(xerr(xs(t, a[0])))
		case "opll_non_retryable_hint":
			v = OpllNonRetryableHint(xerr(xs(t, a[0])))
		case "opll_combo_attempt_order":
			pairs := comboAttemptOrder(xs(t, a[0]))
			outp := [][]string{}
			for _, p := range pairs {
				outp = append(outp, []string{p[0], p[1]})
			}
			v = outp
		case "opll_apply_amount_check":
			r := &LinkResult{StripeAmount: xs(t, a[0]), StripeAmountSource: xs(t, a[1])}
			err := applyAmountCheck(r, xs(t, a[2]))
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			v = []string{r.TargetAmount, r.AmountCheck, msg}
		case "opll_stripe_key_for_checkout":
			o := asObject(xjson(t, a[0]))
			v = stripeKeyForCheckout(&Checkout{StripePublishableKey: pyStrOr(o.Get("stripe_publishable_key"))})
		case "opll_stripe_context":
			ip := asObject(xjson(t, a[0]))
			base := asObject(xjson(t, a[2]))
			b := &stripeCtx{RuntimeVersion: pyStrOr(base.Get("runtime_version"))}
			ctx := newStripeContext(ip, xs(t, a[1]), b)
			v = map[string]string{
				"config_id": ctx.ConfigID, "init_checksum": ctx.InitChecksum,
				"checkout_amount": ctx.CheckoutAmount, "currency": ctx.Currency,
				"locale": ctx.Locale, "runtime_version": ctx.RuntimeVersion,
			}
		case "opll_random_postal_code":
			v = randomPostalCode(xs(t, a[0]))
		case "extract_access_token_from_session_text":
			v = extractAccessTokenFromSessionText(xs(t, a[0]))
		case "urlsplit":
			p := pyURLSplit(xs(t, a[0]))
			v = []string{p.Scheme, p.Netloc, p.Path, p.Query, p.Fragment, p.Hostname()}
		case "urlunsplit":
			var parts []string
			_ = json.Unmarshal(a[0], &parts)
			v = pyURLUnsplit(splitResult{parts[0], parts[1], parts[2], parts[3], parts[4]})
		case "urljoin":
			v = pyURLJoin(xs(t, a[0]), xs(t, a[1]))
		case "quote_safe_empty":
			v = pyQuote(xs(t, a[0]))
		case "urlencode_pairs":
			var pairs [][]string
			_ = json.Unmarshal(a[0], &pairs)
			f := formPairs{}
			for _, p := range pairs {
				f = append(f, formPair{p[0], p[1]})
			}
			v = f.Encode()
		case "parse_qsl_dict":
			_, m := queryDict(xs(t, a[0]))
			v = m
		case "json_dumps_str":
			v = pyJSONString(xs(t, a[0]))
		case "str_of_json":
			v = pyStr(xjson(t, a[0]))
		case "repr_of_json":
			v = pyRepr(xjson(t, a[0]))
		case "detect_currency_text":
			ip := asObject(xjson(t, a[0]))
			ck := asObject(xjson(t, a[1]))
			v = detectCurrencyText(ip, pyStrOr(ck.Get("currency")), xs(t, a[2]))
		case "pm_type_normalize":
			v = normalizePaymentMethodType(xs(t, a[0]))
		case "country_currency_table":
			v = models.CountryCurrency
		case "supported_countries":
			names := []string{}
			for k := range openAISupportedCountryCodes {
				names = append(names, k)
			}
			sort.Strings(names)
			v = names
		case "billing_profile_phone":
			cc := xs(t, a[0])
			p := countryPhonePrefix[cc]
			if p == "" {
				p = "+1"
			}
			v = p
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		out = append(out, xres{OK: ok, V: v})
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("OPLL_DIFF_OUT"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type xerrT string

func (e xerrT) Error() string { return string(e) }

func xerr(s string) error { return xerrT(s) }
