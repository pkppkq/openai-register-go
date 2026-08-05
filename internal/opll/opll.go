// Package opll ports app.py's OPLL ("OpenAI payment long link") HTTP synthesis
// layer: the pure-HTTP pipeline that turns an OpenAI access token into a
// ChatGPT Plus payment long link (PayPal BA approve link, GoPay redirect, or a
// Stripe/pay.openai.com hosted page) without driving a browser.
//
// The pipeline is a three-stage proxy chain, exactly as in Python:
//
//	create   proxy -> POST chatgpt.com/backend-api/payments/checkout
//	followup proxy -> POST api.stripe.com/v1/payment_pages/{cs}/init
//	                  POST api.stripe.com/v1/payment_methods
//	                  POST api.stripe.com/v1/payment_pages/{cs}/confirm
//	                  GET  api.stripe.com/v1/payment_pages/{cs}   (poll)
//	approve  proxy -> POST chatgpt.com/backend-api/payments/checkout/approve
//
// Everything runs over internal/tlsclient (Chrome TLS impersonation), the Go
// replacement for Python's curl_cffi(impersonate="chrome136") sessions.
//
// Python reference: app.py lines 2681-2845, 3451-3663, 4054-4174, 4179-4494,
// 4507-4918.
package opll

import (
	"bytes"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/rand/v2"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	http "github.com/bogdanfinn/fhttp"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// ---------------------------------------------------------------------------
// Constants (app.py 270-273)
// ---------------------------------------------------------------------------

const (
	defaultStripePK             = "pk_live_51HOrSwC6h1nxGoI3lTAgRjYVrz4dU3fVOabyCcKR3pbEJguCVAlqCxdxCUvoRh1XWwRacViovU3kLKvpkjh7IqkW00iXQsjo3n"
	stripeVersionFull           = "2025-03-31.basil; checkout_server_update_beta=v1; checkout_manual_approval_preview=v1"
	defaultStripeRuntimeVersion = "6f8494a281"
	payLongLinkTimeout          = 30 // seconds, app.py PAY_LONG_LINK_TIMEOUT
)

// opllApproveBurstResults mirrors OPLL_APPROVE_BURST_RESULTS (app.py 4504):
// approve results that mean "retry the whole approve", not "hard failure".
var opllApproveBurstResults = map[string]bool{"blocked": true, "exception": true}

// ---------------------------------------------------------------------------
// Country / locale / billing tables (app.py 523-666)
// ---------------------------------------------------------------------------

// openAISupportedCountryCodes mirrors OPENAI_SUPPORTED_COUNTRY_CODES (app.py 530-554).
var openAISupportedCountryCodes = map[string]bool{
	"AX": true, "AL": true, "DZ": true, "AS": true, "AD": true, "AO": true, "AI": true, "AQ": true, "AG": true, "AR": true,
	"AM": true, "AW": true, "AU": true, "AT": true, "AZ": true, "BS": true, "BH": true, "BD": true, "BB": true, "BE": true,
	"BZ": true, "BJ": true, "BM": true, "BT": true, "BO": true, "BQ": true, "BA": true, "BW": true, "BV": true, "BR": true,
	"IO": true, "BN": true, "BG": true, "BF": true, "BI": true, "CV": true, "KH": true, "CM": true, "CA": true, "KY": true,
	"CF": true, "TD": true, "CL": true, "CX": true, "CC": true, "CO": true, "KM": true, "CG": true, "CK": true, "CR": true,
	"CI": true, "HR": true, "CW": true, "CY": true, "CZ": true, "DK": true, "DJ": true, "DM": true, "DO": true, "EC": true,
	"SV": true, "GQ": true, "ER": true, "EE": true, "SZ": true, "FK": true, "FO": true, "FJ": true, "FI": true, "FR": true,
	"GF": true, "PF": true, "TF": true, "GA": true, "GM": true, "GE": true, "DE": true, "GH": true, "GI": true, "GR": true,
	"GL": true, "GD": true, "GP": true, "GU": true, "GT": true, "GG": true, "GN": true, "GW": true, "GY": true, "HT": true,
	"HM": true, "VA": true, "HN": true, "HU": true, "IS": true, "IN": true, "ID": true, "IQ": true, "IE": true, "IM": true,
	"IL": true, "IT": true, "JM": true, "JP": true, "JE": true, "JO": true, "KZ": true, "KE": true, "KI": true, "KW": true,
	"KG": true, "LA": true, "LV": true, "LB": true, "LS": true, "LR": true, "LI": true, "LT": true, "LU": true, "MG": true,
	"MW": true, "MY": true, "MV": true, "ML": true, "MT": true, "MH": true, "MQ": true, "MR": true, "MU": true, "YT": true,
	"MX": true, "FM": true, "MD": true, "MC": true, "MN": true, "ME": true, "MS": true, "MA": true, "MZ": true, "MM": true,
	"NA": true, "NR": true, "NP": true, "NL": true, "NC": true, "NZ": true, "NI": true, "NE": true, "NG": true, "NU": true,
	"NF": true, "MK": true, "MP": true, "NO": true, "OM": true, "PK": true, "PW": true, "PS": true, "PA": true, "PG": true,
	"PE": true, "PH": true, "PN": true, "PL": true, "PT": true, "PR": true, "QA": true, "RE": true, "RO": true, "RW": true,
	"BL": true, "SH": true, "KN": true, "LC": true, "MF": true, "PM": true, "VC": true, "WS": true, "SM": true, "ST": true,
	"SN": true, "RS": true, "SC": true, "SL": true, "SG": true, "SX": true, "SK": true, "SI": true, "SB": true, "SO": true,
	"ZA": true, "GS": true, "KR": true, "SS": true, "ES": true, "LK": true, "SR": true, "SJ": true, "SE": true, "CH": true,
	"TW": true, "TZ": true, "TH": true, "TL": true, "TG": true, "TK": true, "TO": true, "TT": true, "TN": true, "TR": true,
	"TM": true, "TC": true, "TV": true, "UG": true, "UA": true, "AE": true, "GB": true, "UM": true, "US": true, "UY": true,
	"UZ": true, "VU": true, "WF": true, "EH": true, "ZM": true,
}

// countryPhonePrefix mirrors COUNTRY_PHONE_PREFIX (app.py 566-580).
var countryPhonePrefix = map[string]string{
	"AU": "+61", "CA": "+1", "DE": "+49", "GB": "+44", "IE": "+353", "JP": "+81",
	"NZ": "+64", "SG": "+65", "TH": "+66", "US": "+1",
	"AD": "+376", "AE": "+971", "AL": "+355", "AR": "+54", "AT": "+43", "BE": "+32",
	"BG": "+359", "BH": "+973", "BM": "+1", "BO": "+591", "BR": "+55", "CH": "+41",
	"CL": "+56", "CO": "+57", "CR": "+506", "CY": "+357", "CZ": "+420", "DK": "+45",
	"EE": "+372", "ES": "+34", "FI": "+358", "FR": "+33", "GI": "+350", "GR": "+30",
	"HK": "+852", "HU": "+36", "ID": "+62", "IL": "+972", "IN": "+91", "IS": "+354",
	"IT": "+39", "KR": "+82", "KZ": "+7", "LI": "+423", "LT": "+370", "LU": "+352",
	"LV": "+371", "MC": "+377", "MD": "+373", "ME": "+382", "MK": "+389", "MT": "+356",
	"MX": "+52", "MY": "+60", "NL": "+31", "NO": "+47", "PH": "+63", "PL": "+48",
	"PT": "+351", "QA": "+974", "RO": "+40", "RS": "+381", "SA": "+966", "SE": "+46",
	"SI": "+386", "SK": "+421", "SM": "+378", "TR": "+90", "TW": "+886", "UA": "+380",
	"UY": "+598", "ZA": "+27",
}

// billingName is one (first, last) tuple from the *_BILLING_NAMES tables.
type billingName struct{ First, Last string }

// billingStreet is one (line1, city, state, postal) tuple from *_BILLING_STREETS.
type billingStreet struct{ Line1, City, State, Postal string }

// app.py 581-588
var usBillingNames = []billingName{{"James", "Smith"}, {"John", "Brown"}, {"Michael", "Johnson"}, {"Robert", "Miller"}, {"David", "Davis"}, {"William", "Wilson"}}

var usBillingStreets = []billingStreet{
	{"3110 Sunset Boulevard", "Los Angeles", "CA", "90026"},
	{"1200 Market Street", "San Francisco", "CA", "94102"},
	{"500 Main Street", "Austin", "TX", "78701"},
	{"88 Broadway", "New York", "NY", "10007"},
	{"1200 Peachtree St", "Atlanta", "GA", "30309"},
}

// app.py 589-603
var deBillingNames = []billingName{{"Lukas", "Schneider"}, {"Felix", "Muller"}, {"Jonas", "Weber"}, {"Leon", "Fischer"}, {"Marie", "Wagner"}, {"Laura", "Becker"}, {"Maximilian", "Hoffmann"}, {"Paul", "Schulz"}, {"Emma", "Koch"}, {"Hannah", "Bauer"}, {"Sophie", "Richter"}, {"Noah", "Klein"}}

var deBillingStreets = []billingStreet{
	{"Friedrichstrasse 123", "Berlin", "BE", "10117"},
	{"Leopoldstrasse 50", "Munich", "BY", "80802"},
	{"Zeil 85", "Frankfurt am Main", "HE", "60313"},
	{"Konigsallee 60", "Dusseldorf", "NW", "40212"},
	{"Moenckebergstrasse 7", "Hamburg", "HH", "20095"},
	{"Hohenzollernring 72", "Cologne", "NW", "50672"},
	{"Kaiserstrasse 44", "Stuttgart", "BW", "70173"},
	{"Kaufingerstrasse 15", "Munich", "BY", "80331"},
	{"Georgstrasse 24", "Hanover", "NI", "30159"},
	{"Prager Strasse 9", "Dresden", "SN", "01069"},
	{"Schadowstrasse 36", "Dusseldorf", "NW", "40212"},
	{"Breite Strasse 18", "Bonn", "NW", "53111"},
}

// app.py 604-616
var gbBillingNames = []billingName{{"Oliver", "Smith"}, {"George", "Taylor"}, {"Harry", "Brown"}, {"Noah", "Wilson"}, {"Jack", "Davies"}, {"Arthur", "Evans"}, {"Olivia", "Johnson"}, {"Amelia", "Roberts"}, {"Isla", "Walker"}, {"Ava", "Thompson"}, {"Mia", "White"}, {"Grace", "Hughes"}}

var gbBillingStreets = []billingStreet{
	{"221B Baker Street", "London", "England", "NW1 6XE"},
	{"10 Downing Street", "London", "England", "SW1A 2AA"},
	{"45 Deansgate", "Manchester", "England", "M3 2AY"},
	{"18 Park Row", "Leeds", "England", "LS1 5JA"},
	{"77 Queen Street", "Cardiff", "Wales", "CF10 2GR"},
	{"9 Princes Street", "Edinburgh", "Scotland", "EH2 2ER"},
	{"33 Broad Street", "Birmingham", "England", "B1 2HF"},
	{"14 Castle Street", "Liverpool", "England", "L2 0NE"},
	{"52 College Green", "Bristol", "England", "BS1 5SH"},
	{"6 Royal Avenue", "Belfast", "Northern Ireland", "BT1 1DA"},
}

// app.py 617-625
var auBillingNames = []billingName{{"Jack", "Wilson"}, {"Oliver", "Taylor"}, {"Noah", "Brown"}, {"Charlotte", "Smith"}, {"Amelia", "Jones"}, {"Isla", "Williams"}}

var auBillingStreets = []billingStreet{
	{"120 Collins Street", "Melbourne", "Victoria", "3000"},
	{"88 George Street", "Sydney", "New South Wales", "2000"},
	{"45 Queen Street", "Brisbane", "Queensland", "4000"},
	{"22 King William Street", "Adelaide", "South Australia", "5000"},
	{"60 St Georges Terrace", "Perth", "Western Australia", "6000"},
	{"18 Elizabeth Street", "Hobart", "Tasmania", "7000"},
}

// app.py 626-634
var extraBillingNames = []billingName{{"Alex", "Tan"}, {"Daniel", "Lee"}, {"Emma", "Wong"}, {"Mia", "Chen"}, {"Noah", "Martin"}, {"Olivia", "Nguyen"}}

var extraBillingStreets = map[string][]billingStreet{
	"TH": {{"999 Rama I Road", "Bangkok", "Bangkok", "10330"}, {"88 Sukhumvit Road", "Bangkok", "Bangkok", "10110"}, {"45 Nimman Road", "Chiang Mai", "Chiang Mai", "50200"}},
	"JP": {{"1-1 Marunouchi", "Chiyoda-ku", "Tokyo", "100-0005"}, {"2-2-1 Yaesu", "Chuo-ku", "Tokyo", "104-0028"}, {"3-1 Umeda", "Osaka", "Osaka", "530-0001"}},
	"SG": {{"10 Anson Road", "Singapore", "Singapore", "079903"}, {"1 Raffles Place", "Singapore", "Singapore", "048616"}, {"80 Robinson Road", "Singapore", "Singapore", "068898"}},
	"NZ": {{"22 Queen Street", "Auckland", "Auckland", "1010"}, {"50 Lambton Quay", "Wellington", "Wellington", "6011"}, {"120 Hereford Street", "Christchurch", "Canterbury", "8011"}},
	"CA": {{"100 King Street West", "Toronto", "ON", "M5X 1A9"}, {"555 West Hastings Street", "Vancouver", "BC", "V6B 4N6"}, {"1250 Rene-Levesque Blvd", "Montreal", "QC", "H3B 4W8"}},
	"IE": {{"1 Grand Canal Square", "Dublin", "Dublin", "D02 P820"}, {"10 South Mall", "Cork", "Cork", "T12 RD43"}, {"5 Eyre Square", "Galway", "Galway", "H91 FPK2"}},
}

// billingProfileCityByCountry mirrors BILLING_PROFILE_CITY_BY_COUNTRY (app.py 635-642).
var billingProfileCityByCountry = map[string][]string{
	"AT": {"Vienna", "Graz", "Linz"}, "BE": {"Brussels", "Antwerp", "Ghent"}, "BR": {"Sao Paulo", "Rio de Janeiro", "Brasilia"},
	"CH": {"Zurich", "Geneva", "Basel"}, "DK": {"Copenhagen", "Aarhus", "Odense"}, "ES": {"Madrid", "Barcelona", "Valencia"},
	"FI": {"Helsinki", "Espoo", "Tampere"}, "FR": {"Paris", "Lyon", "Marseille"}, "ID": {"Jakarta", "Surabaya", "Bandung"},
	"IT": {"Rome", "Milan", "Turin"}, "KR": {"Seoul", "Busan", "Incheon"}, "MX": {"Mexico City", "Guadalajara", "Monterrey"},
	"NL": {"Amsterdam", "Rotterdam", "Utrecht"}, "NO": {"Oslo", "Bergen", "Trondheim"}, "PL": {"Warsaw", "Krakow", "Gdansk"},
	"PT": {"Lisbon", "Porto", "Coimbra"}, "SE": {"Stockholm", "Gothenburg", "Malmo"}, "TW": {"Taipei", "Taichung", "Kaohsiung"},
}

// postalPatternByCountry mirrors POSTAL_PATTERN_BY_COUNTRY (app.py 643-650).
var postalPatternByCountry = map[string]string{
	"AD": "AD###", "AR": "C####", "AU": "####", "AT": "####", "BE": "####", "BR": "#####-###",
	"CA": "A#A #A#", "CH": "####", "CL": "#######", "CZ": "### ##", "DE": "#####", "DK": "####",
	"ES": "#####", "FI": "#####", "FR": "#####", "GB": "AA# #AA", "IE": "A## A###", "ID": "#####",
	"IN": "######", "IT": "#####", "JP": "###-####", "KR": "#####", "MX": "#####", "NL": "#### AA",
	"NO": "####", "NZ": "####", "PL": "##-###", "PT": "####-###", "SE": "### ##", "SG": "######",
	"TH": "#####", "US": "#####",
}

// billingStreetPool mirrors BILLING_STREET_POOL (app.py 651).
var billingStreetPool = []string{"Market Street", "Central Avenue", "Station Road", "Main Street", "High Street", "King Street"}

// defaultBillingCityPool is the BILLING_PROFILE_BY_COUNTRY city_pool fallback (app.py 656).
var defaultBillingCityPool = []string{"Capital City", "Central District", "Market Town"}

// localeMap mirrors LOCALE_MAP (app.py 662-666): locale -> (browser, elements).
var localeMap = map[string][2]string{
	"de": {"de-DE", "de"}, "en": {"en-US", "en"}, "en-US": {"en-US", "en"}, "es": {"es-ES", "es"},
	"fr": {"fr-FR", "fr"}, "id": {"id-ID", "id"}, "it": {"it-IT", "it"}, "ja": {"ja-JP", "ja"},
	"ko": {"ko-KR", "ko"}, "pt-BR": {"pt-BR", "pt-BR"}, "zh-CN": {"zh-CN", "zh-CN"}, "zh-TW": {"zh-TW", "zh-TW"},
}

// ---------------------------------------------------------------------------
// Exported types
// ---------------------------------------------------------------------------

// Checkout mirrors the dict returned by opll_create_checkout (app.py 3514-3546)
// and opll_checkout_from_url (app.py 2758-2784). JSON tags match the Python keys.
type Checkout struct {
	CSID                 string `json:"cs_id"`
	ProcessorEntity      string `json:"processor_entity"`
	StripePublishableKey string `json:"stripe_publishable_key"`
	BillingCountry       string `json:"billing_country"`
	Currency             string `json:"currency"`
	// CheckoutURL is only present when the checkout came from a browser URL
	// (opll_checkout_from_url); opll_create_checkout does not set it.
	CheckoutURL string `json:"checkout_url,omitempty"`
}

// LinkResult mirrors the dict returned by the generate_opll_*_long_link
// functions (app.py 4762-4918): the spread Checkout keys plus the link and
// amount-check keys added by opll_apply_amount_check (app.py 4054-4066).
// Field JSON tags match the Python dict keys one-for-one.
type LinkResult struct {
	CSID                 string `json:"cs_id"`
	ProcessorEntity      string `json:"processor_entity"`
	StripePublishableKey string `json:"stripe_publishable_key"`
	BillingCountry       string `json:"billing_country"`
	Currency             string `json:"currency"`
	CheckoutURL          string `json:"checkout_url,omitempty"`

	PaymentMethodCountry string `json:"payment_method_country,omitempty"`
	PaymentMethodID      string `json:"payment_method_id,omitempty"`
	PaymentMethodType    string `json:"payment_method_type,omitempty"`

	StripeHostedURL     string `json:"stripe_hosted_url"`
	StripeRedirectURL   string `json:"stripe_redirect_url,omitempty"`
	ProviderRedirectURL string `json:"provider_redirect_url,omitempty"`
	LongURL             string `json:"long_url"`

	// Fallback is true when the PayPal flow succeeded on a country combo other
	// than the requested one (generate_opll_paypal_long_link only).
	Fallback bool `json:"fallback"`
	// ProviderError is the "; "-joined list of earlier combo failures.
	ProviderError string `json:"provider_error"`

	StripeAmount       string `json:"stripe_amount"`
	StripeAmountSource string `json:"stripe_amount_source"`
	TargetAmount       string `json:"target_amount"`
	AmountCheck        string `json:"amount_check"`
}

// PaymentMethodNotSupportedError mirrors OpllPaymentMethodNotSupported
// (app.py 4482-4486): the Stripe checkout does not offer the wanted method.
// Never retryable — a different proxy will not make the method appear.
type PaymentMethodNotSupportedError struct {
	PaymentMethod  string
	MethodsSummary string
}

func (e *PaymentMethodNotSupportedError) Error() string {
	return fmt.Sprintf("当前 checkout 不支持 %s; 可用支付方式: %s", e.PaymentMethod, e.MethodsSummary)
}

// stripeRequiresApprovalError mirrors OpllStripeRequiresApproval (app.py 4496-4497):
// internal control-flow signal meaning "call the ChatGPT approve endpoint, then
// poll the Stripe payment page again".
type stripeRequiresApprovalError struct{ msg string }

func (e *stripeRequiresApprovalError) Error() string { return e.msg }

// chatgptApproveBlockedError mirrors OpllChatgptApproveBlocked (app.py 4500-4501):
// approve returned a burst-throttle result; retrying the same burst is pointless.
type chatgptApproveBlockedError struct{ msg string }

func (e *chatgptApproveBlockedError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// Small Python-semantics helpers
// ---------------------------------------------------------------------------

// truncRunes mirrors Python's text[:n] slicing, which counts characters
// (runes), not bytes. Every "[:500]" style truncation in the Python source is
// character-based, so byte slicing would corrupt the Chinese log strings.
func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func runeLen(s string) int { return len([]rune(s)) }

// pySpaceClass is the exact set Python treats as whitespace: what str.strip()
// removes and what the `re` module's \s matches for str patterns. It is Go's
// unicode.White_Space PLUS U+001C-U+001F (the ASCII separators), which Python
// counts as whitespace and Go does not. Verified rune-by-rune over the whole
// code-point space against CPython 3.12 (TestPythonWhitespaceSet).
const pySpaceClass = `\t\n\v\f\r\x{1c}-\x{1f} \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// pyIsSpace reports whether r is whitespace to Python.
func pyIsSpace(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

// pyStrip mirrors Python str.strip(). strings.TrimSpace is NOT a substitute: it
// leaves U+001C-U+001F in place, which silently turns an equal amount string
// into an unequal one in opll_apply_amount_check.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyFloatRepr mirrors CPython's repr() of a float, which is what str() of a
// json-decoded JSON float returns. Go's strconv defaults do NOT match: Python
// switches to exponent form at a different threshold, always writes ".0" on an
// integral value, and pads the exponent to two digits with a sign.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // shortest round-tripping digits
	sign := ""
	if strings.HasPrefix(e, "-") {
		sign, e = "-", e[1:]
	}
	mant, expPart, _ := strings.Cut(e, "e")
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return sign + e
	}
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1 // position of the decimal point within digits
	// CPython float_repr_style 'r': exponent form when decpt <= -4 || decpt > 16.
	if decpt <= -4 || decpt > 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		return fmt.Sprintf("%s%se%+03d", sign, out, decpt-1)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}

// pyNumStr mirrors Python str() of a number that came out of json.loads.
// json.Number keeps the wire literal, but Python has already turned it into an
// int (arbitrary precision) or a float (whose str() is repr()), so "2000.00"
// prints as "2000.0", "2e3" as "2000.0" and "-0" as "0". This is the amount
// that is compared against target_amount and sent back to Stripe as
// expected_amount, so the spelling has to be Python's.
func pyNumStr(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return s
	}
	return pyFloatRepr(f)
}

// pyStr mirrors Python str(v) for any json.loads value.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return pyNumStr(t)
	case []any, *jsonObject:
		// str() of a container is its repr().
		return pyReprValue(v)
	default:
		return fmt.Sprint(v)
	}
}

// pyReprStr mirrors Python repr() of a str: single quotes unless the value
// contains a single quote and no double quote.
func pyReprStr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case !unicode.IsPrint(r):
			// Python repr() escapes everything str.isprintable() rejects, which
			// includes NBSP and the Unicode spaces — not just the C0 controls.
			switch {
			case r < 0x100:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyReprValue mirrors Python repr() of a json.loads value.
func pyReprValue(v any) string {
	switch t := v.(type) {
	case string:
		return pyReprStr(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, pyReprValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *jsonObject:
		if t == nil {
			return "None"
		}
		parts := make([]string, 0, len(t.keys))
		for _, k := range t.keys {
			parts = append(parts, pyReprStr(k)+": "+pyReprValue(t.vals[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return pyStr(v)
}

// pyFalsy mirrors Python truthiness for JSON values (None/""/0/False/[]/{}).
func pyFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return false
		}
		return f == 0
	case []any:
		return len(t) == 0
	case *jsonObject:
		return t == nil || len(t.keys) == 0
	}
	return false
}

// pyStrOr mirrors the pervasive Python idiom str(value or "").
func pyStrOr(v any) string {
	if pyFalsy(v) {
		return ""
	}
	return pyStr(v)
}

// pyRepr mirrors Python repr() for the values that reach the approve-result
// error messages.
func pyRepr(v any) string { return pyReprValue(v) }

// pyListRepr renders a []string the way Python renders a list of str, used to
// keep the "keys=[...]" diagnostics identical to the Python message.
func pyListRepr(items []string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, pyReprStr(it))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// randomUUID returns a RFC-4122 v4 UUID string (Python uuid.uuid4()).
func randomUUID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		// Python's uuid4 cannot fail; degrade to the math/rand source rather
		// than aborting a flow Python would have continued.
		for i := range b {
			b[i] = byte(rand.IntN(256))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// randomUUIDHex returns uuid4().hex — 32 lowercase hex chars, no dashes.
func randomUUIDHex() string {
	return strings.ReplaceAll(randomUUID(), "-", "")
}

func randInt(lo, hi int) int { return lo + rand.IntN(hi-lo+1) }

// pyJSONString mirrors json.dumps() of a str, which is what requests' `json=`
// argument puts on the wire. encoding/json is NOT a substitute in either
// direction: Go escapes `<`, `>` and `&` as </>/& (Python emits
// them literally) and Go emits every non-ASCII rune raw (Python's default
// ensure_ascii=True escapes them, astral planes as a surrogate PAIR). Both
// bodies built with this go to chatgpt.com, whose processor_entity /
// checkout_session_id values come straight off the wire, so the byte layout has
// to be Python's.
func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				v := r - 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800|((v>>10)&0x3ff), 0xdc00|(v&0x3ff))
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---------------------------------------------------------------------------
// Order-preserving JSON
//
// Python dicts preserve insertion order, so json.loads() yields the wire order
// and opll_collect_urls / opll_find_submission_attempt / the payment-method
// walker all depend on it (they take the FIRST match). Go maps are unordered,
// so JSON objects are decoded into jsonObject, which keeps key order.
// ---------------------------------------------------------------------------

type jsonObject struct {
	keys []string
	vals map[string]any
}

func newJSONObject() *jsonObject {
	return &jsonObject{vals: map[string]any{}}
}

func (o *jsonObject) set(key string, value any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Get returns the value for key, or nil (matching dict.get default).
func (o *jsonObject) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Keys returns the keys in wire order.
func (o *jsonObject) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// asObject narrows any decoded value to a JSON object, or nil.
func asObject(v any) *jsonObject {
	if o, ok := v.(*jsonObject); ok {
		return o
	}
	return nil
}

// decodeOrderedJSON parses JSON preserving object key order and numeric
// literals (json.Number, so str(2000) stays "2000" and never "2000.000000").
//
// Trailing content is an ERROR, as in json.loads. json.Decoder stops after the
// first value, so without the EOF check `"accessToken": "tok"` would decode to
// the bare string "accessToken" and a truncated HTTP body would be accepted as
// a complete response where Python raised.
func decodeOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("extra data after top-level JSON value")
	}
	return v, nil
}

func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := newJSONObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := kt.(string)
			val, err := decodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			obj.set(key, val)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			val, err := decodeOrderedValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected json delimiter %v", delim)
}

// decodeOrderedObject decodes a response body that Python treated as
// "response.json() or {}" — a non-object or empty body becomes an empty object.
func decodeOrderedObject(data []byte) (*jsonObject, error) {
	v, err := decodeOrderedJSON(data)
	if err != nil {
		return nil, err
	}
	if o := asObject(v); o != nil {
		return o, nil
	}
	return newJSONObject(), nil
}

// ---------------------------------------------------------------------------
// Python case mapping
//
// str.lower()/str.upper() apply FULL case mapping; strings.ToLower/ToUpper apply
// the simple (1:1) mapping. U+0130 lowercases to TWO runes in Python and one in
// Go, and 102 code points uppercase to more than one rune (U+00DF -> "SS",
// U+FB01 -> "FI" — both of which are supported country codes, so a simple
// ToUpper silently maps them to the "US" fallback instead).
// ---------------------------------------------------------------------------

// pyMultiUpper is every code point whose Python .upper() is longer than one
// rune (generated from CPython 3.12; see TestPythonCaseMapping).
var pyMultiUpper = map[rune]string{
	0x00DF: "\u0053\u0053", 0x0149: "\u02bc\u004e", 0x01F0: "\u004a\u030c", 0x0390: "\u0399\u0308\u0301",
	0x03B0: "\u03a5\u0308\u0301", 0x0587: "\u0535\u0552", 0x1E96: "\u0048\u0331", 0x1E97: "\u0054\u0308",
	0x1E98: "\u0057\u030a", 0x1E99: "\u0059\u030a", 0x1E9A: "\u0041\u02be", 0x1F50: "\u03a5\u0313",
	0x1F52: "\u03a5\u0313\u0300", 0x1F54: "\u03a5\u0313\u0301", 0x1F56: "\u03a5\u0313\u0342", 0x1F80: "\u1f08\u0399",
	0x1F81: "\u1f09\u0399", 0x1F82: "\u1f0a\u0399", 0x1F83: "\u1f0b\u0399", 0x1F84: "\u1f0c\u0399",
	0x1F85: "\u1f0d\u0399", 0x1F86: "\u1f0e\u0399", 0x1F87: "\u1f0f\u0399", 0x1F88: "\u1f08\u0399",
	0x1F89: "\u1f09\u0399", 0x1F8A: "\u1f0a\u0399", 0x1F8B: "\u1f0b\u0399", 0x1F8C: "\u1f0c\u0399",
	0x1F8D: "\u1f0d\u0399", 0x1F8E: "\u1f0e\u0399", 0x1F8F: "\u1f0f\u0399", 0x1F90: "\u1f28\u0399",
	0x1F91: "\u1f29\u0399", 0x1F92: "\u1f2a\u0399", 0x1F93: "\u1f2b\u0399", 0x1F94: "\u1f2c\u0399",
	0x1F95: "\u1f2d\u0399", 0x1F96: "\u1f2e\u0399", 0x1F97: "\u1f2f\u0399", 0x1F98: "\u1f28\u0399",
	0x1F99: "\u1f29\u0399", 0x1F9A: "\u1f2a\u0399", 0x1F9B: "\u1f2b\u0399", 0x1F9C: "\u1f2c\u0399",
	0x1F9D: "\u1f2d\u0399", 0x1F9E: "\u1f2e\u0399", 0x1F9F: "\u1f2f\u0399", 0x1FA0: "\u1f68\u0399",
	0x1FA1: "\u1f69\u0399", 0x1FA2: "\u1f6a\u0399", 0x1FA3: "\u1f6b\u0399", 0x1FA4: "\u1f6c\u0399",
	0x1FA5: "\u1f6d\u0399", 0x1FA6: "\u1f6e\u0399", 0x1FA7: "\u1f6f\u0399", 0x1FA8: "\u1f68\u0399",
	0x1FA9: "\u1f69\u0399", 0x1FAA: "\u1f6a\u0399", 0x1FAB: "\u1f6b\u0399", 0x1FAC: "\u1f6c\u0399",
	0x1FAD: "\u1f6d\u0399", 0x1FAE: "\u1f6e\u0399", 0x1FAF: "\u1f6f\u0399", 0x1FB2: "\u1fba\u0399",
	0x1FB3: "\u0391\u0399", 0x1FB4: "\u0386\u0399", 0x1FB6: "\u0391\u0342", 0x1FB7: "\u0391\u0342\u0399",
	0x1FBC: "\u0391\u0399", 0x1FC2: "\u1fca\u0399", 0x1FC3: "\u0397\u0399", 0x1FC4: "\u0389\u0399",
	0x1FC6: "\u0397\u0342", 0x1FC7: "\u0397\u0342\u0399", 0x1FCC: "\u0397\u0399", 0x1FD2: "\u0399\u0308\u0300",
	0x1FD3: "\u0399\u0308\u0301", 0x1FD6: "\u0399\u0342", 0x1FD7: "\u0399\u0308\u0342", 0x1FE2: "\u03a5\u0308\u0300",
	0x1FE3: "\u03a5\u0308\u0301", 0x1FE4: "\u03a1\u0313", 0x1FE6: "\u03a5\u0342", 0x1FE7: "\u03a5\u0308\u0342",
	0x1FF2: "\u1ffa\u0399", 0x1FF3: "\u03a9\u0399", 0x1FF4: "\u038f\u0399", 0x1FF6: "\u03a9\u0342",
	0x1FF7: "\u03a9\u0342\u0399", 0x1FFC: "\u03a9\u0399", 0xFB00: "\u0046\u0046", 0xFB01: "\u0046\u0049",
	0xFB02: "\u0046\u004c", 0xFB03: "\u0046\u0046\u0049", 0xFB04: "\u0046\u0046\u004c", 0xFB05: "\u0053\u0054",
	0xFB06: "\u0053\u0054", 0xFB13: "\u0544\u0546", 0xFB14: "\u0544\u0535", 0xFB15: "\u0544\u053b",
	0xFB16: "\u054e\u0546", 0xFB17: "\u0544\u053d",
}

// pyUpper mirrors Python str.upper().
func pyUpper(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { _, ok := pyMultiUpper[r]; return ok }) {
		return strings.ToUpper(s)
	}
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := pyMultiUpper[r]; ok {
			b.WriteString(mapped)
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// wordBreakMidSet is Word_Break ∈ {MidLetter, MidNumLet, Single_Quote} — the
// one component of Derived_Case_Ignorable that Go does not ship as a table.
// Verified exhaustively against CPython 3.12 over all 0x110000 code points:
// with this set, Mn|Me|Cf|Lm|Sk reproduces Case_Ignorable with ZERO mismatches
// (TestPythonCaseIgnorableSet).
var wordBreakMidSet = map[rune]bool{
	0x0027: true, // Single_Quote APOSTROPHE
	0x002E: true, // MidNumLet FULL STOP
	0x003A: true, // MidLetter COLON
	0x00B7: true, // MidLetter MIDDLE DOT
	0x0387: true, // MidLetter GREEK ANO TELEIA
	0x055F: true, // MidLetter ARMENIAN ABBREVIATION MARK
	0x05F4: true, // MidLetter HEBREW PUNCTUATION GERSHAYIM
	0x2018: true, // MidNumLet LEFT SINGLE QUOTATION MARK
	0x2019: true, // MidNumLet RIGHT SINGLE QUOTATION MARK
	0x2024: true, // MidNumLet ONE DOT LEADER
	0x2027: true, // MidLetter HYPHENATION POINT
	0xFE13: true, // MidLetter PRESENTATION FORM FOR VERTICAL COLON
	0xFE52: true, // MidNumLet SMALL FULL STOP
	0xFE55: true, // MidLetter SMALL COLON
	0xFF07: true, // MidNumLet FULLWIDTH APOSTROPHE
	0xFF0E: true, // MidNumLet FULLWIDTH FULL STOP
	0xFF1A: true, // MidLetter FULLWIDTH COLON
}

// pyCased is the Unicode Cased derived property (Lowercase + Uppercase + Lt).
func pyCased(r rune) bool {
	return unicode.Is(unicode.Ll, r) || unicode.Is(unicode.Lu, r) || unicode.Is(unicode.Lt, r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

// pyCaseIgnorable is the Unicode Case_Ignorable derived property.
func pyCaseIgnorable(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Lm, r) || unicode.Is(unicode.Sk, r) || wordBreakMidSet[r]
}

// isFinalSigma implements SpecialCasing.txt's Final_Sigma condition:
//
//	Before C: \p{cased} (\p{case-ignorable})*
//	After  C: ! ( (\p{case-ignorable})* \p{cased} )
//
// Case_Ignorable is tested FIRST, so a code point that is both cased and
// case-ignorable (every Lm modifier letter, e.g. U+02B0 ʰ) counts as ignorable.
// That ordering was confirmed against CPython over the whole code-point space.
func isFinalSigma(runes []rune, i int) bool {
	precededByCased := false
	for j := i - 1; j >= 0; j-- {
		if pyCaseIgnorable(runes[j]) {
			continue
		}
		precededByCased = pyCased(runes[j])
		break
	}
	if !precededByCased {
		return false
	}
	for j := i + 1; j < len(runes); j++ {
		if pyCaseIgnorable(runes[j]) {
			continue
		}
		return !pyCased(runes[j])
	}
	return true
}

// pyLower mirrors Python str.lower(), a FULL case mapping. strings.ToLower is
// the SIMPLE mapping and differs on the two non-language-specific
// SpecialCasing rules:
//
//   - U+0130 (İ) lowercases to TWO runes, "i" + U+0307. ToLower drops the dot.
//   - U+03A3 (Σ) lowercases to FINAL sigma ς at the end of a word and to medial
//     σ elsewhere; ToLower always writes σ. "ΟΔΟΣ".lower() is "οδος".
//
// The sigma rule is unobservable in every path this package has today (the
// values that reach pyLower on a wire or comparison path — payment-method
// types, currencies, country codes, URL schemes and hosts — are ASCII, and the
// retryability markers it is substring-matched against are sigma-free), but
// normalizePaymentMethodType's output IS sent to Stripe, so the rule is
// implemented rather than documented away.
func pyLower(s string) string {
	if !strings.ContainsRune(s, 'İ') && !strings.ContainsRune(s, 'Σ') {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range runes {
		switch r {
		case 'İ':
			b.WriteString("i̇")
		case 'Σ':
			if isFinalSigma(runes, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// urlsplit / urlunsplit
//
// net/url is NOT a stand-in for urllib.parse here. url.Parse REJECTS inputs
// urlsplit happily returns ("https://x:notaport/y", "%2F" in the host, a URL
// whose first path segment holds a colon), and url.URL.Host DROPS the userinfo
// that urlsplit keeps inside netloc — which is what decides whether
// "https://u@paypal.com/agreements/approve?ba_token=..." counts as a PayPal
// success URL. Both differences change link acceptance, so urlsplit is
// reimplemented rather than approximated.
// ---------------------------------------------------------------------------

// c0AndSpace is WHATWG "C0 control or space" — U+0000..U+0020.
const c0AndSpace = "\x00\x01\x02\x03\x04\x05\x06\a\b\t\n\v\f\r\x0e\x0f" +
	"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "

var urlUnsafeStripper = strings.NewReplacer("\t", "", "\n", "", "\r", "")

// splitResult is urllib.parse.SplitResult. Path/Query/Fragment are raw (never
// percent-decoded), exactly as urlsplit leaves them.
type splitResult struct {
	Scheme   string
	Netloc   string
	Path     string
	Query    string
	Fragment string
}

var schemeCharRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*$`)

// pyURLSplit mirrors urllib.parse.urlsplit(), which never fails.
func pyURLSplit(value string) splitResult {
	// CPython: lstrip C0-control-or-space, then delete every tab/CR/LF.
	url := urlUnsafeStripper.Replace(strings.TrimLeft(value, c0AndSpace))
	var out splitResult
	if i := strings.IndexByte(url, ':'); i > 0 && schemeCharRe.MatchString(url[:i]) {
		out.Scheme = pyLower(url[:i])
		url = url[i+1:]
	}
	if strings.HasPrefix(url, "//") {
		rest := url[2:]
		end := len(rest)
		for _, c := range []byte{'/', '?', '#'} {
			if j := strings.IndexByte(rest, c); j >= 0 && j < end {
				end = j
			}
		}
		out.Netloc, url = rest[:end], rest[end:]
	}
	if i := strings.IndexByte(url, '#'); i >= 0 {
		url, out.Fragment = url[:i], url[i+1:]
	}
	if i := strings.IndexByte(url, '?'); i >= 0 {
		url, out.Query = url[:i], url[i+1:]
	}
	out.Path = url
	return out
}

// Hostname mirrors SplitResult.hostname: netloc minus userinfo and port
// ("" where Python returns None).
//
// Two CPython quirks are load-bearing and neither is obvious:
//
//   - lowercasing STOPS at the first '%'. The property lowercases only the part
//     before it so an IPv6 zone id ("[fe80::1%tESt]") keeps its case, which means
//     a percent-escape in the host keeps its case too ("ex%2Fample.com").
//   - the bracket branch is chosen by the presence of '[' ALONE. With '[' and no
//     ']' the hostname is everything after '[', NOT the pre-colon prefix.
func (s splitResult) Hostname() string {
	host := s.Netloc
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexByte(host, '['); i >= 0 {
		host = host[i+1:]
		if j := strings.IndexByte(host, ']'); j >= 0 {
			host = host[:j]
		}
	} else if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	name, rest, found := strings.Cut(host, "%")
	if !found {
		return pyLower(name)
	}
	return pyLower(name) + "%" + rest
}

// ---------------------------------------------------------------------------
// urlparse / urljoin
//
// urljoin is how opll_resolve_external_redirect follows a relative Location
// header, so its output IS the emitted payment long link. net/url's
// ResolveReference is not equivalent: url.Parse REJECTS a bad percent-escape
// ("%zz") and a bad port, and URL.String() re-encodes the result (a literal
// space becomes %20). Both changed the link that was handed to the operator.
// ---------------------------------------------------------------------------

// parseResult is urllib.parse.ParseResult — urlsplit plus the ";params" tail
// that urlparse splits off the last path segment.
type parseResult struct{ Scheme, Netloc, Path, Params, Query, Fragment string }

// usesRelative mirrors urllib.parse.uses_relative. It is NOT uses_netloc:
// "itms-services" and "nfs"/"git"/"rsync"/"snews"/"telnet" appear in one list
// and not the other, and urljoin returns the reference UNCHANGED for a scheme
// outside this set.
var usesRelative = map[string]bool{
	"": true, "ftp": true, "http": true, "gopher": true, "nntp": true, "imap": true,
	"wais": true, "file": true, "https": true, "shttp": true, "mms": true,
	"prospero": true, "rtsp": true, "rtsps": true, "rtspu": true, "sftp": true,
	"svn": true, "svn+ssh": true, "ws": true, "wss": true,
}

// usesParams mirrors urllib.parse.uses_params: schemes for which urlparse peels
// a ";params" tail off the last path segment. http/https are in it, so
// "https://x/a;b" has path "/a" and params "b" — and urljoin reassembles them.
var usesParams = map[string]bool{
	"": true, "ftp": true, "hdl": true, "prospero": true, "http": true, "imap": true,
	"https": true, "shttp": true, "rtsp": true, "rtsps": true, "rtspu": true, "sip": true,
	"sips": true, "mms": true, "sftp": true, "tel": true,
}

// pyURLSplitWithScheme is urlsplit(url, scheme): same as pyURLSplit except an
// absent scheme defaults to the (C0/space-stripped, tab/CR/LF-deleted) argument.
func pyURLSplitWithScheme(value, scheme string) splitResult {
	out := pyURLSplit(value)
	if out.Scheme == "" {
		out.Scheme = urlUnsafeStripper.Replace(strings.Trim(scheme, c0AndSpace))
	}
	return out
}

// pySplitParams mirrors urllib.parse._splitparams. Only ever called with a path
// that contains ';', which is what keeps its i<0 branch from misbehaving.
func pySplitParams(path string) (string, string) {
	var i int
	if strings.Contains(path, "/") {
		i = strings.Index(path[strings.LastIndex(path, "/"):], ";")
		if i < 0 {
			return path, ""
		}
		i += strings.LastIndex(path, "/")
	} else {
		i = strings.Index(path, ";")
	}
	return path[:i], path[i+1:]
}

// pyURLParse mirrors urllib.parse.urlparse(url, scheme).
func pyURLParse(value, scheme string) parseResult {
	s := pyURLSplitWithScheme(value, scheme)
	out := parseResult{Scheme: s.Scheme, Netloc: s.Netloc, Path: s.Path, Query: s.Query, Fragment: s.Fragment}
	if usesParams[s.Scheme] && strings.Contains(s.Path, ";") {
		out.Path, out.Params = pySplitParams(s.Path)
	}
	return out
}

// pyURLUnparse mirrors urllib.parse.urlunparse.
func pyURLUnparse(p parseResult) string {
	path := p.Path
	if p.Params != "" {
		path = path + ";" + p.Params
	}
	return pyURLUnsplit(splitResult{p.Scheme, p.Netloc, path, p.Query, p.Fragment})
}

// pyURLJoin mirrors urllib.parse.urljoin (CPython 3.12). It is pure string
// surgery — it never percent-encodes, never rejects a malformed escape, and
// never normalises a port — which is exactly why net/url cannot stand in.
func pyURLJoin(base, ref string) string {
	if base == "" {
		return ref
	}
	if ref == "" {
		return base
	}
	b := pyURLParse(base, "")
	r := pyURLParse(ref, b.Scheme)
	if r.Scheme != b.Scheme || !usesRelative[r.Scheme] {
		return ref
	}
	if usesNetloc[r.Scheme] {
		if r.Netloc != "" {
			return pyURLUnparse(r)
		}
		r.Netloc = b.Netloc
	}
	if r.Path == "" && r.Params == "" {
		r.Path, r.Params = b.Path, b.Params
		if r.Query == "" {
			r.Query = b.Query
		}
		return pyURLUnparse(r)
	}

	var segments []string
	if strings.HasPrefix(r.Path, "/") {
		// rfc3986: a root-relative reference ignores the base path entirely.
		segments = strings.Split(r.Path, "/")
	} else {
		baseParts := strings.Split(b.Path, "/")
		if baseParts[len(baseParts)-1] != "" {
			// The last base segment is a file, not a directory.
			baseParts = baseParts[:len(baseParts)-1]
		}
		segments = append(append([]string{}, baseParts...), strings.Split(r.Path, "/")...)
		// segments[1:-1] = filter(None, segments[1:-1]) — CPython drops EMPTY
		// interior segments, so "/a/b//" + "c" is "/a/b/c" and not "/a/b//c".
		if len(segments) >= 2 {
			kept := make([]string, 0, len(segments))
			kept = append(kept, segments[0])
			for _, seg := range segments[1 : len(segments)-1] {
				if seg != "" {
					kept = append(kept, seg)
				}
			}
			segments = append(kept, segments[len(segments)-1])
		}
	}

	resolved := []string{}
	for _, seg := range segments {
		switch seg {
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
		case ".":
		default:
			resolved = append(resolved, seg)
		}
	}
	if last := segments[len(segments)-1]; last == "." || last == ".." {
		resolved = append(resolved, "")
	}
	r.Path = strings.Join(resolved, "/")
	if r.Path == "" {
		r.Path = "/"
	}
	return pyURLUnparse(r)
}

// pyURLUnsplit mirrors urllib.parse.urlunsplit().
func pyURLUnsplit(s splitResult) string {
	url := s.Path
	if s.Netloc != "" || (s.Scheme != "" && usesNetloc[s.Scheme] && !strings.HasPrefix(url, "//")) {
		if url != "" && !strings.HasPrefix(url, "/") {
			url = "/" + url
		}
		url = "//" + s.Netloc + url
	}
	if s.Scheme != "" {
		url = s.Scheme + ":" + url
	}
	if s.Query != "" {
		url += "?" + s.Query
	}
	if s.Fragment != "" {
		url += "#" + s.Fragment
	}
	return url
}

// usesNetloc mirrors urllib.parse.uses_netloc.
var usesNetloc = map[string]bool{
	"": true, "ftp": true, "http": true, "gopher": true, "nntp": true, "telnet": true,
	"imap": true, "wais": true, "file": true, "mms": true, "https": true, "shttp": true,
	"snews": true, "prospero": true, "rtsp": true, "rtsps": true, "rtspu": true, "rsync": true,
	"svn": true, "svn+ssh": true, "sftp": true, "nfs": true, "git": true, "git+ssh": true,
	"ws": true, "wss": true, "itms-services": true,
}

// ---------------------------------------------------------------------------
// Ordered form / query encoding
//
// requests urlencodes dicts in insertion order; url.Values.Encode() sorts keys,
// which would reorder every Stripe form body. formPairs keeps Python's order.
// ---------------------------------------------------------------------------

type formPair struct{ K, V string }

type formPairs []formPair

// Encode matches urllib.parse.urlencode (quote_plus per component, "&" joined).
func (f formPairs) Encode() string {
	var b strings.Builder
	for i, p := range f {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.K))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.V))
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// utf8MaximalSubpart returns the length of the maximal valid prefix of an
// invalid UTF-8 sequence, which is how CPython's errors="replace" decoder
// decides how many bytes one U+FFFD stands for.
func utf8MaximalSubpart(b []byte) int {
	c := b[0]
	need := 0
	var lo, hi byte = 0x80, 0xBF
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c >= 0xEE && c <= 0xEF:
		need = 2
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(b); k++ {
		l, h := lo, hi
		if k > 0 {
			l, h = 0x80, 0xBF
		}
		if b[n] < l || b[n] > h {
			break
		}
		n++
	}
	return n
}

// decodeUTF8Replace mirrors bytes.decode("utf-8", errors="replace").
func decodeUTF8Replace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			out.Write(b[i : i+size])
			i += size
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += utf8MaximalSubpart(b[i:])
	}
	return out.String()
}

// pyUnquote mirrors urllib.parse.unquote(s, errors="replace"). Unlike
// url.QueryUnescape it never fails: an invalid "%zz" is emitted verbatim.
// QueryUnescape's all-or-nothing error made parseQSL fall back to the RAW
// segment on any bad escape, so "+" was left unconverted in exactly the values
// (ba_token, success_return_url) the link checks read.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	// CPython decodes each maximal ASCII run independently and passes
	// non-ASCII runs through untouched.
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(s[i:])
			out.WriteString(s[i : i+size])
			i += size
			continue
		}
		j := i
		for j < len(s) && s[j] < utf8.RuneSelf {
			j++
		}
		out.WriteString(decodeUTF8Replace(unquoteToBytes(s[i:j])))
		i = j
	}
	return out.String()
}

// unquoteToBytes mirrors urllib.parse.unquote_to_bytes.
func unquoteToBytes(s string) []byte {
	parts := strings.Split(s, "%")
	buf := make([]byte, 0, len(s))
	buf = append(buf, parts[0]...)
	for _, item := range parts[1:] {
		if len(item) >= 2 && isHexDigit(item[0]) && isHexDigit(item[1]) {
			buf = append(buf, unhexDigit(item[0])<<4|unhexDigit(item[1]))
			buf = append(buf, item[2:]...)
			continue
		}
		buf = append(buf, '%')
		buf = append(buf, item...)
	}
	return buf
}

// pyUnquotePlus mirrors urllib.parse.unquote_plus / parse_qsl's decoding.
func pyUnquotePlus(s string) string { return pyUnquote(strings.ReplaceAll(s, "+", " ")) }

// parseQSL mirrors urllib.parse.parse_qsl(query, keep_blank_values=True).
func parseQSL(raw string) formPairs {
	out := formPairs{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v := part, ""
		if i := strings.Index(part, "="); i >= 0 {
			k, v = part[:i], part[i+1:]
		}
		out = append(out, formPair{pyUnquotePlus(k), pyUnquotePlus(v)})
	}
	return out
}

// queryDict mirrors dict(parse_qsl(...)): last value wins, first position kept.
func queryDict(raw string) (formPairs, map[string]string) {
	pairs := parseQSL(raw)
	order := formPairs{}
	seen := map[string]int{}
	for _, p := range pairs {
		if idx, ok := seen[p.K]; ok {
			order[idx].V = p.V
			continue
		}
		seen[p.K] = len(order)
		order = append(order, p)
	}
	vals := map[string]string{}
	for _, p := range order {
		vals[p.K] = p.V
	}
	return order, vals
}

// ---------------------------------------------------------------------------
// Country / locale helpers
// ---------------------------------------------------------------------------

// normalizeOpllCountry mirrors normalize_opll_country (app.py 2681-2683).
func normalizeOpllCountry(country string) string {
	c := pyUpper(pyStrip(country))
	if openAISupportedCountryCodes[c] {
		return c
	}
	return "US"
}

// currencyForCountry mirrors currency_for_country (app.py 2677-2678).
// models.CountryCurrency now carries the full map (base literal + both
// update() passes), so this simply delegates.
func currencyForCountry(country string) string {
	// NOT models.CurrencyForCountry: that helper adds a TrimSpace Python does
	// not do, so it answers JPY for " jp " where currency_for_country answers
	// USD — a wrong-currency checkout for any unnormalised input.
	if c, ok := models.CountryCurrency[pyUpper(country)]; ok {
		return c
	}
	return "USD"
}

// localeParts mirrors locale_parts (app.py 2686-2687).
func localeParts(locale string) (string, string) {
	if v, ok := localeMap[pyStrip(locale)]; ok {
		return v[0], v[1]
	}
	v := localeMap["en"]
	return v[0], v[1]
}

// ---------------------------------------------------------------------------
// Checkout payload scraping (app.py 2690-2755)
// ---------------------------------------------------------------------------

// extractProcessorEntity mirrors opll_extract_processor_entity (app.py 2690-2702).
func extractProcessorEntity(data any) string {
	obj := asObject(data)
	if obj == nil {
		return ""
	}
	direct := obj.Get("processor_entity")
	if pyFalsy(direct) {
		direct = obj.Get("processorEntity")
	}
	if !pyFalsy(direct) {
		return pyStrip(pyStr(direct))
	}
	for _, key := range []string{"checkout_session", "session", "checkout", "data"} {
		if nested := asObject(obj.Get(key)); nested != nil {
			if found := extractProcessorEntity(nested); found != "" {
				return found
			}
		}
	}
	return ""
}

var stripePKRe = regexp.MustCompile(`pk_live_[A-Za-z0-9]+`)

// extractStripePublishableKey mirrors opll_extract_stripe_publishable_key
// (app.py 2705-2723).
func extractStripePublishableKey(data any) string {
	switch t := data.(type) {
	case string:
		return stripePKRe.FindString(t)
	case *jsonObject:
		for _, key := range []string{"stripe_publishable_key", "publishable_key", "publishableKey", "stripePublishableKey", "key"} {
			if found := extractStripePublishableKey(t.Get(key)); found != "" {
				return found
			}
		}
		for _, k := range t.Keys() {
			if found := extractStripePublishableKey(t.Get(k)); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range t {
			if found := extractStripePublishableKey(item); found != "" {
				return found
			}
		}
	}
	return ""
}

// processorEntityForCountry mirrors opll_processor_entity_for_country (app.py 2726-2730).
func processorEntityForCountry(country, processorEntity string) string {
	if entity := pyStrip(processorEntity); entity != "" {
		return entity
	}
	if pyUpper(country) == "US" {
		return "openai_llc"
	}
	return "openai_ie"
}

// chatgptSuccessReturnURL mirrors opll_chatgpt_success_return_url (app.py 2733-2735).
func chatgptSuccessReturnURL(csID, country, processorEntity string) string {
	entity := processorEntityForCountry(country, processorEntity)
	return fmt.Sprintf("https://chatgpt.com/checkout/verify?stripe_session_id=%s&processor_entity=%s&plan_type=plus", csID, entity)
}

// toOpenAIPayURL mirrors opll_to_openai_pay_url (app.py 2738-2747): rehost a
// checkout.stripe.com URL on pay.openai.com.
func toOpenAIPayURL(stripeHostedURL string) string {
	raw := pyStrip(stripeHostedURL)
	if raw == "" {
		return ""
	}
	const prefix = "https://checkout.stripe.com"
	if strings.HasPrefix(raw, prefix) {
		return "https://pay.openai.com" + raw[len(prefix):]
	}
	parsed := pyURLSplit(raw)
	// Python compares netloc (userinfo included), so a URL carrying credentials
	// is NOT rehosted; rewriting it would also silently drop the credentials.
	if pyLower(parsed.Netloc) == "checkout.stripe.com" {
		out := parsed
		if out.Scheme == "" {
			out.Scheme = "https"
		}
		out.Netloc = "pay.openai.com"
		return pyURLUnsplit(out)
	}
	return raw
}

// stripeCheckoutLongURL mirrors opll_stripe_checkout_long_url (app.py 2750-2755).
func stripeCheckoutLongURL(csID, country, processorEntity string) string {
	return fmt.Sprintf(
		"https://checkout.stripe.com/c/pay/%s?returned_from_redirect=true&ui_mode=custom&return_url=%s",
		csID,
		pyQuote(chatgptSuccessReturnURL(csID, country, processorEntity)),
	)
}

// pyQuote mirrors urllib.parse.quote(value, safe=”): percent-encode every byte
// except the unreserved set. url.QueryEscape is quote_plus, which writes a
// literal space as '+' instead of %20.
func pyQuote(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

var csIDRe = regexp.MustCompile(`cs_(?:live|test)_[A-Za-z0-9_]+`)

// OpllCheckoutFromURL mirrors opll_checkout_from_url (app.py 2758-2784):
// reconstruct a Checkout from a browser-visible pay.openai.com /
// checkout.stripe.com URL, so the PayPal pipeline can continue from a trial
// short-link that was claimed in the browser.
func OpllCheckoutFromURL(rawURL, country, currency string) (Checkout, error) {
	text := pyStrip(rawURL)
	processorEntity := ""
	csID := ""
	_, query := queryDict(pyURLSplit(text).Query)
	for _, key := range []string{"stripe_session_id", "checkout_session_id", "session_id", "cs_id"} {
		if v := query[key]; v != "" {
			csID = v
			break
		}
	}
	csID = pyStrip(csID)
	if csID == "" {
		csID = csIDRe.FindString(text)
	}
	if csID == "" {
		return Checkout{}, fmt.Errorf("未从试用 checkout URL 提取到 Stripe Session ID: %s", truncRunes(text, 180))
	}
	processorEntity = pyStrip(query["processor_entity"])
	// Python: normalize_opll_country(country or "US") — "" (not whitespace) falls back.
	if country == "" {
		country = "US"
	}
	checkoutCountry := normalizeOpllCountry(country)
	checkoutCurrency := currencyForCountry(checkoutCountry)
	if currency != "" {
		checkoutCurrency = pyUpper(currency)
	}
	return Checkout{
		CSID:                 csID,
		ProcessorEntity:      processorEntity,
		StripePublishableKey: "",
		BillingCountry:       checkoutCountry,
		Currency:             checkoutCurrency,
		CheckoutURL:          text,
	}, nil
}

// stripeConfirmReturnURL mirrors opll_stripe_confirm_return_url (app.py 2787-2805).
func stripeConfirmReturnURL(csID string, checkout Checkout, stripeHostedURL string) string {
	hostedURL := toOpenAIPayURL(stripeHostedURL)
	if hostedURL == "" {
		hostedURL = stripeCheckoutLongURL(csID, checkout.BillingCountry, checkout.ProcessorEntity)
	}
	if !strings.Contains(hostedURL, "pay.openai.com/") && !strings.Contains(hostedURL, "checkout.stripe.com/") {
		return hostedURL
	}
	parsed := pyURLSplit(hostedURL)
	order, vals := queryDict(parsed.Query)
	if _, ok := vals["success_return_url"]; !ok {
		order = append(order, formPair{
			K: "success_return_url",
			V: chatgptSuccessReturnURL(csID, checkout.BillingCountry, checkout.ProcessorEntity),
		})
	}
	parsed.Query = order.Encode()
	return pyURLUnsplit(parsed)
}

// ---------------------------------------------------------------------------
// HTTP sessions over internal/tlsclient
// ---------------------------------------------------------------------------

// session is the Go stand-in for a requests.Session: a Chrome-impersonating TLS
// client plus persistent headers. Header keys MUST be lowercase so they replace
// (rather than duplicate) tlsclient.ChromeHeaders' lowercase defaults, and they
// are held as an ORDERED list, not a map: these endpoints fingerprint clients
// and fhttp emits any header missing from HeaderOrderKey in Go map-iteration
// order, i.e. reshuffled on every single request.
type session struct {
	c       *tlsclient.Client
	headers formPairs
}

func newSession(proxyURL string, headers formPairs) (*session, error) {
	c, err := tlsclient.New(proxyURL, payLongLinkTimeout)
	if err != nil {
		return nil, err
	}
	return &session{c: c, headers: append(formPairs(nil), headers...)}, nil
}

// mergeHeaders layers the session and per-request headers over the Chrome
// defaults and rebuilds HeaderOrderKey so EVERY name has a fixed position:
// Chrome's own order first (it is part of the fingerprint), then the session
// headers in Python's dict-insertion order, then the per-request ones.
//
// Without this, fhttp emits any header absent from HeaderOrderKey in Go
// map-iteration order — the sixteen ChatGPT session headers would be reshuffled
// on every single request to endpoints picked precisely because they
// fingerprint clients.
func mergeHeaders(base http.Header, sets ...formPairs) http.Header {
	order := append([]string(nil), base[http.HeaderOrderKey]...)
	named := make(map[string]bool, len(order)+8)
	for _, k := range order {
		named[strings.ToLower(k)] = true
	}
	for _, set := range sets {
		for _, p := range set {
			key := strings.ToLower(p.K)
			base[key] = []string{p.V}
			if !named[key] {
				named[key] = true
				order = append(order, key)
			}
		}
	}
	base[http.HeaderOrderKey] = order
	return base
}

// request issues one HTTP call and returns the response (body already drained
// and closed — only status/headers remain valid) plus the body bytes.
func (s *session) request(method, rawURL string, body []byte, extra formPairs) (*http.Response, []byte, error) {
	var r io.Reader
	if len(body) > 0 {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, r)
	if err != nil {
		return nil, nil, err
	}
	req.Header = mergeHeaders(s.c.ChromeHeaders(), s.headers, extra)
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b, nil
}

func (s *session) postJSON(rawURL, body string, extra formPairs) (int, []byte, error) {
	h := append(formPairs{{"content-type", "application/json"}}, extra...)
	resp, b, err := s.request("POST", rawURL, []byte(body), h)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, b, nil
}

func (s *session) postForm(rawURL string, form formPairs) (int, []byte, error) {
	resp, b, err := s.request("POST", rawURL, []byte(form.Encode()),
		formPairs{{"content-type", "application/x-www-form-urlencoded"}})
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, b, nil
}

func (s *session) getWithParams(rawURL string, params formPairs) (int, []byte, error) {
	full := rawURL
	if len(params) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		full = rawURL + sep + params.Encode()
	}
	resp, b, err := s.request("GET", full, nil, nil)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, b, nil
}

// findAccessToken mirrors find_access_token (app.py 2605-2620).
func findAccessToken(value any) string {
	switch t := value.(type) {
	case *jsonObject:
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if token := pyStrip(pyStrOr(t.Get(key))); token != "" {
				return token
			}
		}
		for _, k := range t.Keys() {
			if token := findAccessToken(t.Get(k)); token != "" {
				return token
			}
		}
	case []any:
		for _, item := range t {
			if token := findAccessToken(item); token != "" {
				return token
			}
		}
	}
	return ""
}

// sessionTokenRe mirrors the fallback regex at app.py 2633.
var sessionTokenRe = regexp.MustCompile(`"(?:accessToken|access_token|token)"\s*:\s*"([^"]+)"`)

// extractAccessTokenFromSessionText mirrors extract_access_token_from_session_text
// (app.py 2623-2636): accept a raw JWT, a "Bearer x" header, or a session JSON blob.
func extractAccessTokenFromSessionText(text string) string {
	raw := pyStrip(text)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "Bearer ") {
		// Python raw.split(None, 1)[1].strip().
		return pyStrip(raw[len("Bearer"):])
	}
	// Python: `try: return find_access_token(json.loads(raw)) except: pass`.
	// The return is unconditional — a document that parses as JSON but carries
	// no token yields "" and NEVER reaches the regex / bare-JWT fallbacks.
	if v, err := decodeOrderedJSON([]byte(raw)); err == nil {
		return findAccessToken(v)
	}
	if m := sessionTokenRe.FindStringSubmatch(raw); m != nil {
		return pyStrip(m[1])
	}
	if strings.Count(raw, ".") >= 2 && len(raw) > 80 {
		return raw
	}
	return ""
}

// newChatGPTSession mirrors opll_build_chatgpt_session (app.py 2818-2844).
func newChatGPTSession(accessToken, proxyURL string) (*session, error) {
	token := extractAccessTokenFromSessionText(accessToken)
	if token == "" {
		token = pyStrip(accessToken)
	}
	if token == "" {
		return nil, errors.New("当前账号没有 Access Token，请先注册并获取 Session 信息")
	}
	deviceID := randomUUID()
	// Order is app.py 2824-2841's dict order (requests preserves it).
	return newSession(proxyURL, formPairs{
		{"user-agent", openai.DefaultUserAgent},
		{"accept", "*/*"},
		{"accept-language", "en-US,en;q=0.9"},
		{"authorization", "Bearer " + token},
		{"origin", "https://chatgpt.com"},
		{"referer", "https://chatgpt.com/"},
		{"content-type", "application/json"},
		{"oai-device-id", deviceID},
		{"oai-language", "en-US"},
		{"sec-ch-ua", `"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"`},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", `"Windows"`},
		{"sec-fetch-dest", "empty"},
		{"sec-fetch-mode", "cors"},
		{"sec-fetch-site", "same-origin"},
		{"cookie", "oai-did=" + deviceID},
	})
}

// newStripeSession mirrors opll_build_stripe_session (app.py 3613-3618).
func newStripeSession(proxyURL string) (*session, error) {
	return newSession(proxyURL, formPairs{
		{"user-agent", openai.DefaultUserAgent},
		{"accept-language", "en-US,en;q=0.9"},
	})
}

// ---------------------------------------------------------------------------
// Checkout creation (app.py 3514-3546)
// ---------------------------------------------------------------------------

// OpllCreateCheckout mirrors opll_create_checkout (app.py 3514-3546): create a
// ChatGPT Plus checkout session over the CREATE-stage proxy.
//
// The currency argument is accepted for signature fidelity but ignored — the
// Python function immediately overwrites it with currency_for_country(country).
func OpllCreateCheckout(accessToken, country, currency, proxyURL string) (Checkout, error) {
	_ = currency
	country = normalizeOpllCountry(country)
	currency = currencyForCountry(country)
	sess, err := newChatGPTSession(accessToken, proxyURL)
	if err != nil {
		return Checkout{}, err
	}
	body := createCheckoutBody(country, currency)
	status, raw, err := sess.postJSON("https://chatgpt.com/backend-api/payments/checkout", body, formPairs{
		{"referer", "https://chatgpt.com/"},
		{"x-openai-target-path", "/backend-api/payments/checkout"},
		{"x-openai-target-route", "/backend-api/payments/checkout"},
	})
	if err != nil {
		return Checkout{}, err
	}
	if status >= 400 {
		return Checkout{}, fmt.Errorf("checkout create failed: HTTP %d %s", status, truncRunes(string(raw), 500))
	}
	data, err := decodeOrderedObject(raw)
	if err != nil {
		return Checkout{}, err
	}
	csID := ""
	for _, key := range []string{"checkout_session_id", "session_id", "id"} {
		if v := data.Get(key); !pyFalsy(v) {
			csID = pyStr(v)
			break
		}
	}
	if csID == "" || !strings.HasPrefix(csID, "cs_") {
		// Python interpolates str(data) — the repr of the PARSED dict, not the
		// raw body. The two differ in ways the retryability classifier reads:
		// json.dumps escapes non-ASCII as \uXXXX on the wire while str(dict)
		// prints it literally, so a Chinese "token_invalidated"-class marker in
		// the body only matches after the round-trip through repr.
		return Checkout{}, fmt.Errorf("checkout response missing cs_id: %s", truncRunes(pyStr(data), 500))
	}
	return Checkout{
		CSID:                 csID,
		ProcessorEntity:      extractProcessorEntity(data),
		StripePublishableKey: extractStripePublishableKey(data),
		BillingCountry:       country,
		Currency:             currency,
	}, nil
}

// createCheckoutBody is the `json=` dict of opll_create_checkout (app.py
// 3519-3525), hand-serialized so the byte layout matches requests' json.dumps:
// key order is the literal's, the separators are ", " and ": ", and every
// string goes through pyJSONString (ensure_ascii, no HTML escaping).
func createCheckoutBody(country, currency string) string {
	return fmt.Sprintf(
		`{"entry_point": "all_plans_pricing_modal", "plan_name": "chatgptplusplan", `+
			`"billing_details": {"country": %s, "currency": %s}, `+
			`"promo_campaign": {"promo_campaign_id": "plus-1-month-free", "is_coupon_from_query_param": false}, `+
			`"checkout_ui_mode": "custom"}`,
		pyJSONString(country), pyJSONString(currency),
	)
}

// approveBody is the `json=` dict of opll_chatgpt_approve (app.py 4523).
func approveBody(csID, entity string) string {
	return fmt.Sprintf(`{"checkout_session_id": %s, "processor_entity": %s}`,
		pyJSONString(csID), pyJSONString(entity))
}

// stripeKeyForCheckout mirrors opll_stripe_key_for_checkout (app.py 3575-3576).
func stripeKeyForCheckout(checkout *Checkout) string {
	if checkout != nil {
		if k := pyStrip(checkout.StripePublishableKey); k != "" {
			return k
		}
	}
	return defaultStripePK
}

// stripeInitForm is the `data=` dict of opll_stripe_init (app.py 3591-3605),
// in Python insertion order (requests urlencodes a dict in that order).
func stripeInitForm(browserLocale, elementsLocale, stripeJSID, stripePK string) formPairs {
	return formPairs{
		{"browser_locale", browserLocale},
		{"browser_timezone", "Asia/Shanghai"},
		{"elements_session_client[client_betas][0]", "custom_checkout_server_updates_1"},
		{"elements_session_client[client_betas][1]", "custom_checkout_manual_approval_1"},
		{"elements_session_client[elements_init_source]", "custom_checkout"},
		{"elements_session_client[referrer_host]", "chatgpt.com"},
		{"elements_session_client[stripe_js_id]", stripeJSID},
		{"elements_session_client[locale]", elementsLocale},
		{"elements_session_client[is_aggregation_expected]", "false"},
		{"elements_options_client[saved_payment_method][enable_save]", "never"},
		{"elements_options_client[saved_payment_method][enable_redisplay]", "never"},
		{"key", stripePK},
		{"_stripe_version", stripeVersionFull},
	}
}

// stripeInit mirrors opll_stripe_init (app.py 3579-3610). country/currency are
// accepted for signature fidelity but unused in the request body, exactly as in
// Python. A nil st creates a throwaway session bound to proxyURL.
func stripeInit(csID, country, currency, proxyURL, paymentLocale string, st *session, ctx *stripeCtx, checkout *Checkout) (*jsonObject, error) {
	_, _ = country, currency
	browserLocale, elementsLocale := localeParts(paymentLocale)
	stripePK := stripeKeyForCheckout(checkout)
	sess := st
	if sess == nil {
		// Python built a plain requests.Session here (no curl_cffi); Go always
		// impersonates Chrome, which is a superset of that behaviour.
		var err error
		sess, err = newStripeSession(proxyURL)
		if err != nil {
			return nil, err
		}
	}
	stripeJSID := ""
	if ctx != nil {
		stripeJSID = ctx.StripeJSID
	}
	if stripeJSID == "" {
		stripeJSID = randomUUID()
	}
	form := stripeInitForm(browserLocale, elementsLocale, stripeJSID, stripePK)
	status, raw, err := sess.postForm("https://api.stripe.com/v1/payment_pages/"+csID+"/init", form)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("stripe init failed: HTTP %d %s", status, truncRunes(string(raw), 500))
	}
	return decodeOrderedObject(raw)
}

// ---------------------------------------------------------------------------
// Stripe context + amount (app.py 3621-3663, 4054-4066)
// ---------------------------------------------------------------------------

// stripeCtx mirrors the dict built by opll_stripe_context (app.py 3621-3634).
type stripeCtx struct {
	StripeJSID              string
	ElementsSessionID       string
	ElementsSessionConfigID string
	ConfigID                string
	InitChecksum            string
	CheckoutAmount          string
	Currency                string
	Locale                  string
	RuntimeVersion          string
}

// newStripeContext mirrors opll_stripe_context (app.py 3621-3634).
func newStripeContext(initPayload *jsonObject, paymentLocale string, base *stripeCtx) *stripeCtx {
	_, elementsLocale := localeParts(paymentLocale)
	out := &stripeCtx{}
	if base != nil {
		*out = *base
	}
	if out.StripeJSID == "" {
		out.StripeJSID = randomUUID()
	}
	if out.ElementsSessionID == "" {
		out.ElementsSessionID = "elements_session_" + randomUUIDHex()[:11]
	}
	configID := pyStrOr(initPayload.Get("config_id"))
	if configID != "" {
		out.ElementsSessionConfigID = configID
	} else if out.ElementsSessionConfigID == "" {
		out.ElementsSessionConfigID = randomUUID()
	}
	out.ConfigID = configID
	out.InitChecksum = pyStrOr(initPayload.Get("init_checksum"))
	out.CheckoutAmount = expectedAmount(initPayload)
	out.Currency = pyLower(pyStrOr(initPayload.Get("currency")))
	out.Locale = elementsLocale
	if out.RuntimeVersion == "" {
		out.RuntimeVersion = defaultStripeRuntimeVersion
	}
	return out
}

// expectedAmount mirrors opll_expected_amount (app.py 3637-3638).
func expectedAmount(initPayload *jsonObject) string {
	amount, _ := stripeAmountInfo(initPayload)
	return amount
}

// stripeAmountInfo mirrors opll_stripe_amount_info (app.py 3641-3663): the due
// amount in minor units plus the payload field it came from.
func stripeAmountInfo(initPayload any) (string, string) {
	obj := asObject(initPayload)
	if obj == nil {
		return "0", "missing_payload"
	}
	if totalSummary := asObject(obj.Get("total_summary")); totalSummary != nil {
		if due := totalSummary.Get("due"); due != nil {
			return pyStr(due), "total_summary.due"
		}
	}
	if invoice := asObject(obj.Get("invoice")); invoice != nil {
		if amountDue := invoice.Get("amount_due"); amountDue != nil {
			return pyStr(amountDue), "invoice.amount_due"
		}
	}
	if lineItems, ok := obj.Get("line_items").([]any); ok {
		// Python ints are arbitrary precision: an int64 accumulator wraps to a
		// NEGATIVE amount on overflow, which would then be compared against
		// target_amount and shipped to Stripe as expected_amount.
		total := new(big.Int)
		found := false
		for _, item := range lineItems {
			io := asObject(item)
			if io == nil {
				continue
			}
			amount := io.Get("amount")
			if amount == nil {
				continue
			}
			v, ok := pyInt(amount)
			if !ok {
				// Python: except Exception -> pass (found stays unchanged).
				continue
			}
			total.Add(total, v)
			found = true
		}
		if found {
			return total.String(), "line_items.amount"
		}
	}
	return "0", "fallback_zero"
}

// pyInt mirrors Python int(value or 0) for JSON scalars (truncation toward zero
// for floats, False/None/"" -> 0), at Python's arbitrary precision.
func pyInt(v any) (*big.Int, bool) {
	zero := new(big.Int)
	if pyFalsy(v) {
		return zero, true
	}
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if !strings.ContainsAny(s, ".eE") {
			if b, ok := new(big.Int).SetString(s, 10); ok {
				return b, true
			}
			return zero, false
		}
		// Round-trip through float64 first: json.loads already did, and
		// big.Float.SetString would keep more precision than the float Python
		// actually holds, changing the truncated integer.
		f64, err := strconv.ParseFloat(s, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return zero, false
		}
		if math.IsInf(f64, 0) || math.IsNaN(f64) {
			return zero, false // Python: int(inf) raises OverflowError -> except: pass
		}
		i, _ := big.NewFloat(f64).Int(nil) // truncates toward zero, like int(float)
		return i, true
	case bool:
		if t {
			return big.NewInt(1), true
		}
		return zero, true
	case string:
		if b, ok := pyIntFromString(t); ok {
			return b, true
		}
	}
	return zero, false
}

// decimalDigitZeros lists the first code point of every Unicode decimal-digit
// run (Numeric_Type=Decimal, 68 runs of ten covering 680 code points),
// generated from CPython 3.12's unicodedata. A run cannot be found by walking
// backwards: U+1D7CE..U+1D7FF packs five runs with no gap between them.
var decimalDigitZeros = []rune{
	0x0030, 0x0660, 0x06F0, 0x07C0, 0x0966, 0x09E6, 0x0A66, 0x0AE6, 0x0B66, 0x0BE6,
	0x0C66, 0x0CE6, 0x0D66, 0x0DE6, 0x0E50, 0x0ED0, 0x0F20, 0x1040, 0x1090, 0x17E0,
	0x1810, 0x1946, 0x19D0, 0x1A80, 0x1A90, 0x1B50, 0x1BB0, 0x1C40, 0x1C50, 0xA620,
	0xA8D0, 0xA900, 0xA9D0, 0xA9F0, 0xAA50, 0xABF0, 0xFF10, 0x104A0, 0x10D30, 0x11066,
	0x110F0, 0x11136, 0x111D0, 0x112F0, 0x11450, 0x114D0, 0x11650, 0x116C0, 0x11730, 0x118E0,
	0x11950, 0x11C50, 0x11D50, 0x11DA0, 0x11F50, 0x16A60, 0x16AC0, 0x16B50, 0x1D7CE, 0x1D7D8,
	0x1D7E2, 0x1D7EC, 0x1D7F6, 0x1E140, 0x1E2F0, 0x1E4F0, 0x1E950, 0x1FBF0,
}

// pyDecimalDigit mirrors Py_UNICODE_TODECIMAL: the 0-9 value of a Unicode
// decimal digit, or -1.
func pyDecimalDigit(r rune) int {
	i := sort.Search(len(decimalDigitZeros), func(i int) bool { return decimalDigitZeros[i] > r })
	if i == 0 {
		return -1
	}
	if d := r - decimalDigitZeros[i-1]; d >= 0 && d <= 9 {
		return int(d)
	}
	return -1
}

// pyIntFromString mirrors CPython int(str) at base 10, in two stages exactly as
// CPython does it. strconv/big.Int.SetString is NOT equivalent and this is the
// AMOUNT path (line_items[].amount), so the difference is money:
//
//	int("3_0")   == 30  — underscores are legal between digits at base 10
//	int("٢")     == 2   — every Unicode decimal digit converts
//	int("\x1c1") raises — U+001C is whitespace to str.strip() but NOT to the
//	                      C-level parser, which only skips "\t\n\v\f\r ".
func pyIntFromString(s string) (*big.Int, bool) {
	// Stage 1: _PyUnicode_TransformDecimalAndSpaceToASCII. Code points below
	// 127 pass through UNCHANGED (so U+001C stays a control character), other
	// whitespace becomes a space, other decimal digits become their ASCII
	// digit, and anything else becomes '?' which fails stage 2.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 127:
			b.WriteRune(r)
		case pyIsSpace(r):
			b.WriteByte(' ')
		default:
			d := pyDecimalDigit(r)
			if d < 0 {
				return nil, false
			}
			b.WriteByte(byte('0' + d))
		}
	}
	// Stage 2: PyLong_FromString.
	t := b.String()
	isCSpace := func(c byte) bool {
		return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
	}
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	i := 0
	for i < len(t) && isCSpace(t[i]) {
		i++
	}
	neg := false
	if i < len(t) && (t[i] == '+' || t[i] == '-') {
		neg = t[i] == '-'
		i++
	}
	if i >= len(t) || !isDigit(t[i]) {
		return nil, false
	}
	digits := make([]byte, 0, len(t)-i)
	for i < len(t) {
		if t[i] == '_' {
			// An underscore must be followed by a digit (and, since we only get
			// here after one, preceded by one too).
			i++
			if i >= len(t) || !isDigit(t[i]) {
				return nil, false
			}
		}
		if !isDigit(t[i]) {
			break
		}
		digits = append(digits, t[i])
		i++
	}
	for i < len(t) && isCSpace(t[i]) {
		i++
	}
	if i != len(t) {
		return nil, false
	}
	n, ok := new(big.Int).SetString(string(digits), 10)
	if !ok {
		return nil, false
	}
	if neg {
		n.Neg(n)
	}
	return n, true
}

// applyAmountCheck mirrors opll_apply_amount_check (app.py 4054-4066): stamp
// target_amount/amount_check onto the result and reject amount drift.
func applyAmountCheck(result *LinkResult, targetAmount string) error {
	// Python .strip(), not TrimSpace: a U+001C-U+001F byte around the operator's
	// target amount is whitespace to Python and a character to Go, so TrimSpace
	// would turn an equal amount into a spurious 金额不匹配 (or, in the other
	// direction, let a differing amount through).
	target := pyStrip(targetAmount)
	actual := pyStrip(result.StripeAmount)
	source := pyStrip(result.StripeAmountSource)
	result.TargetAmount = target
	if target == "" {
		result.AmountCheck = "skipped"
		return nil
	}
	if actual != target {
		result.AmountCheck = "failed"
		return &models.AmountMismatchError{TargetAmount: target, ActualAmount: actual, StripeAmountSource: source}
	}
	result.AmountCheck = "passed"
	return nil
}

// ---------------------------------------------------------------------------
// Billing profile synthesis (app.py 4069-4118)
// ---------------------------------------------------------------------------

// billingDetails mirrors the dict returned by opll_billing_for_country.
type billingDetails struct {
	Name       string
	Email      string
	Phone      string
	Country    string
	Line1      string
	City       string
	State      string
	PostalCode string
}

// randomPostalCode mirrors opll_random_postal_code (app.py 4069-4078):
// '#' -> digit, 'A' -> uppercase letter, anything else literal.
func randomPostalCode(pattern string) string {
	if pattern == "" {
		pattern = "#####"
	}
	var b strings.Builder
	for _, char := range pattern {
		switch char {
		case '#':
			b.WriteString(strconv.Itoa(randInt(0, 9)))
		case 'A':
			b.WriteRune(rune(randInt('A', 'Z')))
		default:
			b.WriteRune(char)
		}
	}
	return b.String()
}

// billingForCountry mirrors opll_billing_for_country (app.py 4081-4118).
func billingForCountry(country string) (billingDetails, error) {
	country = normalizeOpllCountry(country)
	var first, last, line1, city, state, postal string
	pick := func(names []billingName, streets []billingStreet) {
		n := names[rand.IntN(len(names))]
		s := streets[rand.IntN(len(streets))]
		first, last = n.First, n.Last
		line1, city, state, postal = s.Line1, s.City, s.State, s.Postal
	}
	switch {
	case country == "DE":
		pick(deBillingNames, deBillingStreets)
	case country == "GB":
		pick(gbBillingNames, gbBillingStreets)
	case country == "AU":
		pick(auBillingNames, auBillingStreets)
	case country == "US":
		pick(usBillingNames, usBillingStreets)
	case extraBillingStreets[country] != nil:
		pick(extraBillingNames, extraBillingStreets[country])
	case openAISupportedCountryCodes[country]:
		n := extraBillingNames[rand.IntN(len(extraBillingNames))]
		first, last = n.First, n.Last
		line1 = fmt.Sprintf("%d %s", randInt(10, 999), billingStreetPool[rand.IntN(len(billingStreetPool))])
		cityPool := billingProfileCityByCountry[country]
		if cityPool == nil {
			cityPool = defaultBillingCityPool
		}
		city = cityPool[rand.IntN(len(cityPool))]
		state = country
		pattern := postalPatternByCountry[country]
		if pattern == "" {
			pattern = "#####"
		}
		postal = randomPostalCode(pattern)
	default:
		// Unreachable: normalizeOpllCountry always returns a supported code.
		return billingDetails{}, fmt.Errorf("不支持的账单资料地区: %s", country)
	}
	suffix := randInt(1000, 9999)
	phonePrefix := countryPhonePrefix[country]
	if phonePrefix == "" {
		phonePrefix = "+1"
	}
	return billingDetails{
		Name:       first + " " + last,
		Email:      fmt.Sprintf("%s.%s%d@example.com", pyLower(first), pyLower(last), suffix),
		Phone:      fmt.Sprintf("%s%d", phonePrefix, randInt(100000000, 999999999)),
		Country:    country,
		Line1:      line1,
		City:       city,
		State:      state,
		PostalCode: postal,
	}, nil
}

// ---------------------------------------------------------------------------
// Stripe payment method (app.py 4134-4171)
// ---------------------------------------------------------------------------

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// normalizePaymentMethodType mirrors str(payment_method_type or "paypal").strip().lower()
// (app.py 4136 and 4667). The `or` fires BEFORE the strip, so a whitespace-only
// argument keeps itself, strips to "" and is sent to Stripe as an EMPTY
// type/expected_payment_method_type — it does NOT fall back to "paypal".
func normalizePaymentMethodType(value string) string {
	if value == "" {
		value = "paypal"
	}
	return pyLower(pyStrip(value))
}

// paymentMethodForm is the `data=` dict of opll_stripe_create_paypal_method
// (app.py 4137-4164), in Python insertion order.
func paymentMethodForm(csID string, ctx *stripeCtx, billing billingDetails, stripePK, paymentMethodType, runtimeVersion, timeOnPage string) formPairs {
	return formPairs{
		{"billing_details[name]", orDefault(billing.Name, "John Doe")},
		{"billing_details[email]", orDefault(billing.Email, "buyer@example.com")},
		{"billing_details[phone]", billing.Phone},
		{"billing_details[address][country]", orDefault(billing.Country, "US")},
		{"billing_details[address][line1]", orDefault(billing.Line1, "3110 Sunset Boulevard")},
		{"billing_details[address][city]", orDefault(billing.City, "Los Angeles")},
		{"billing_details[address][postal_code]", orDefault(billing.PostalCode, "90026")},
		{"billing_details[address][state]", orDefault(billing.State, "CA")},
		{"type", paymentMethodType},
		{"payment_user_agent", fmt.Sprintf("stripe.js/%s; stripe-js-v3/%s; payment-element; deferred-intent", runtimeVersion, runtimeVersion)},
		{"referrer", "https://chatgpt.com"},
		{"time_on_page", timeOnPage},
		{"client_attribution_metadata[checkout_session_id]", csID},
		{"client_attribution_metadata[client_session_id]", ctx.StripeJSID},
		{"client_attribution_metadata[checkout_config_id]", ctx.ConfigID},
		{"client_attribution_metadata[elements_session_id]", ctx.ElementsSessionID},
		{"client_attribution_metadata[elements_session_config_id]", ctx.ElementsSessionConfigID},
		{"client_attribution_metadata[merchant_integration_source]", "elements"},
		{"client_attribution_metadata[merchant_integration_subtype]", "payment-element"},
		{"client_attribution_metadata[merchant_integration_version]", "2021"},
		{"client_attribution_metadata[payment_intent_creation_flow]", "deferred"},
		{"client_attribution_metadata[payment_method_selection_flow]", "automatic"},
		{"client_attribution_metadata[merchant_integration_additional_elements][0]", "payment"},
		{"client_attribution_metadata[merchant_integration_additional_elements][1]", "address"},
		{"key", orDefault(stripePK, defaultStripePK)},
		{"_stripe_version", stripeVersionFull},
	}
}

// stripeCreatePaymentMethod mirrors opll_stripe_create_paypal_method
// (app.py 4134-4171). Despite the Python name it also creates gopay methods.
func stripeCreatePaymentMethod(st *session, csID string, ctx *stripeCtx, billing billingDetails, stripePK, paymentMethodType string) (string, error) {
	runtimeVersion := orDefault(ctx.RuntimeVersion, defaultStripeRuntimeVersion)
	paymentMethodType = normalizePaymentMethodType(paymentMethodType)
	form := paymentMethodForm(csID, ctx, billing, stripePK, paymentMethodType, runtimeVersion, strconv.Itoa(randInt(25000, 55000)))
	status, raw, err := st.postForm("https://api.stripe.com/v1/payment_methods", form)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("stripe payment_methods failed: HTTP %d %s", status, truncRunes(string(raw), 500))
	}
	data, err := decodeOrderedObject(raw)
	if err != nil {
		return "", fmt.Errorf("stripe payment_methods bad response: %s", truncRunes(string(raw), 300))
	}
	pmID := pyStrOr(data.Get("id"))
	if !strings.HasPrefix(pmID, "pm_") {
		return "", fmt.Errorf("stripe payment_methods bad response: %s", truncRunes(string(raw), 300))
	}
	return pmID, nil
}

// ---------------------------------------------------------------------------
// Error formatting (app.py 4174-4203)
// ---------------------------------------------------------------------------

// whitespaceRe is Python's `\s+` for str patterns, which is Unicode-aware.
// Go's RE2 `\s` is [\t\n\f\r ] — it matches neither \v nor NBSP nor the ASCII
// separators, so an error message carrying any of those would keep them and
// stop matching the non-retryable marker substrings below, turning a permanent
// failure into a retried one.
var whitespaceRe = regexp.MustCompile("[" + pySpaceClass + "]+")

// OpllShortError mirrors opll_short_error (app.py 4174-4176) for an EXPLICIT
// limit: collapse whitespace and clip to limit characters with an ellipsis.
// Use OpllShortErrorDefault for the Python default of 260 — 0 is a real limit
// here, not a sentinel, and Python answers "..." for it.
func OpllShortError(detail string, limit int) string {
	text := pyStrip(whitespaceRe.ReplaceAllString(detail, " "))
	if runeLen(text) <= limit {
		return text
	}
	// Python text[:limit-3] with limit < 3 is a NEGATIVE slice bound: it drops
	// the last 3-limit characters rather than returning nothing.
	keep := limit - 3
	if keep < 0 {
		keep = runeLen(text) + keep
		if keep < 0 {
			keep = 0
		}
	}
	return truncRunes(text, keep) + "..."
}

// OpllShortErrorDefault is opll_short_error(detail) with its default limit=260.
func OpllShortErrorDefault(detail string) string { return OpllShortError(detail, 260) }

// stripeErrorSummary mirrors opll_stripe_error_summary (app.py 4179-4203).
func stripeErrorSummary(prefix string, raw []byte) string {
	payload, err := decodeOrderedObject(raw)
	if err != nil {
		payload = newJSONObject()
	}
	errObj := asObject(payload.Get("error"))
	if errObj == nil {
		errObj = newJSONObject()
	}
	extraFields := asObject(errObj.Get("extra_fields"))
	if extraFields == nil {
		extraFields = newJSONObject()
	}
	type labelled struct {
		label string
		value any
	}
	fields := []labelled{
		{"code", errObj.Get("code")},
		{"decline_code", errObj.Get("decline_code")},
		{"type", errObj.Get("type")},
		{"message", errObj.Get("message")},
		{"payment_method_type", extraFields.Get("payment_method_type")},
		{"confirm_error_reason", extraFields.Get("confirm_error_reason")},
		{"confirm_error_code", extraFields.Get("confirm_error_code")},
		{"confirm_error_message", extraFields.Get("confirm_error_message")},
	}
	parts := []string{}
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		if s, ok := f.value.(string); ok && s == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", f.label, OpllShortError(pyStr(f.value), 180)))
	}
	if len(parts) > 0 {
		return prefix + ": " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%s: %s", prefix, OpllShortError(string(raw), 500))
}

// ---------------------------------------------------------------------------
// URL classification (app.py 4206-4253)
// ---------------------------------------------------------------------------

// isExternalURL mirrors opll_is_external_url (app.py 4206-4211).
func isExternalURL(value string) bool {
	parsed := pyURLSplit(value)
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Netloc != ""
}

// netloc returns Python urlsplit().netloc lowercased (userinfo and port kept).
func netloc(value string) (string, bool) {
	return pyLower(pyURLSplit(value).Netloc), true
}

// isPaypalURL mirrors opll_is_paypal_url (app.py 4214-4216).
func isPaypalURL(value string) bool {
	host, _ := netloc(value)
	return host == "paypal.com" || strings.HasSuffix(host, ".paypal.com") ||
		host == "paypalobjects.com" || strings.HasSuffix(host, ".paypalobjects.com")
}

// isPaypalBAApproveURL mirrors opll_is_paypal_ba_approve_url (app.py 4219-4229):
// a real PayPal billing-agreement approval link (the thing we are hunting for).
func isPaypalBAApproveURL(value string) bool {
	parsed := pyURLSplit(value)
	host := pyLower(parsed.Netloc)
	if !(host == "paypal.com" || strings.HasSuffix(host, ".paypal.com")) {
		return false
	}
	if pyLower(strings.TrimRight(parsed.Path, "/")) != "/agreements/approve" {
		return false
	}
	_, query := queryDict(parsed.Query)
	return pyStrip(query["ba_token"]) != ""
}

// OpllIsPaypalSuccessURL mirrors opll_is_paypal_success_url (app.py 4232-4239):
// true for a PayPal BA approve link, or a pm-redirects.stripe.com hand-off.
func OpllIsPaypalSuccessURL(value string) bool {
	if isPaypalBAApproveURL(value) {
		return true
	}
	parsed := pyURLSplit(value)
	return pyLower(parsed.Scheme) == "https" && parsed.Hostname() == "pm-redirects.stripe.com"
}

var ignoredResourceHosts = []string{
	"stripe-camo.global.ssl.fastly.net", "files.stripe.com", "q.stripe.com", "js.stripe.com", "m.stripe.network",
}

var ignoredResourceSuffixes = []string{
	".png", ".jpg", ".jpeg", ".svg", ".webp", ".gif", ".ico", ".css", ".js", ".woff", ".woff2",
}

// isIgnoredResourceURL mirrors opll_is_ignored_resource_url (app.py 4242-4253):
// Stripe static assets that must never be mistaken for a provider redirect.
func isIgnoredResourceURL(value string) bool {
	parsed := pyURLSplit(value)
	host := pyLower(parsed.Netloc)
	path := pyLower(parsed.Path)
	for _, item := range ignoredResourceHosts {
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	for _, suffix := range ignoredResourceSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// urlInTextRe mirrors Python's r"https?://[^\s\"'<>]+". The character class must
// exclude Python's whitespace set, not RE2's: with the narrower ASCII \s a URL
// followed by a non-breaking space swallows the space and everything after it,
// and that concatenated string becomes the emitted payment long link.
var urlInTextRe = regexp.MustCompile(`https?://[^` + pySpaceClass + `"'<>]+`)

// collectURLs mirrors opll_collect_urls (app.py 4256-4270): every URL reachable
// in the payload, in wire order (order decides which redirect we take).
func collectURLs(payload any, found *[]string) []string {
	if found == nil {
		found = &[]string{}
	}
	switch t := payload.(type) {
	case string:
		for _, match := range urlInTextRe.FindAllString(t, -1) {
			*found = append(*found, strings.TrimRight(match, "),.;]"))
		}
	case *jsonObject:
		for _, key := range t.Keys() {
			value := t.Get(key)
			isURLKey := key == "url" || key == "return_url" || key == "redirect_url" || key == "redirect_to_url"
			if s, ok := value.(string); ok && isURLKey && isExternalURL(s) {
				*found = append(*found, s)
				continue
			}
			collectURLs(value, found)
		}
	case []any:
		for _, item := range t {
			collectURLs(item, found)
		}
	}
	return *found
}

// firstPaypalPreferred mirrors the nested next(...) at app.py 4276-4279:
// prefer a BA approve link, else any non-asset PayPal URL, else "".
func firstPaypalPreferred(urls []string) string {
	for _, item := range urls {
		if isPaypalBAApproveURL(item) {
			return item
		}
	}
	for _, item := range urls {
		if isPaypalURL(item) && !isIgnoredResourceURL(item) {
			return item
		}
	}
	return ""
}

// extractRedirectToURL mirrors opll_extract_redirect_to_url (app.py 4273-4297).
func extractRedirectToURL(payload any) string {
	obj := asObject(payload)
	if obj == nil {
		return firstPaypalPreferred(collectURLs(payload, nil))
	}
	if nextAction := asObject(obj.Get("next_action")); nextAction != nil {
		if pyStrOr(nextAction.Get("type")) == "redirect_to_url" {
			if redirectTo := asObject(nextAction.Get("redirect_to_url")); redirectTo != nil {
				if u := pyStrip(pyStrOr(redirectTo.Get("url"))); u != "" {
					return u
				}
			}
		}
	}
	for _, key := range []string{"setup_intent", "payment_intent"} {
		if nested := asObject(obj.Get(key)); nested != nil {
			if found := extractRedirectToURL(nested); found != "" {
				return found
			}
		}
	}
	return firstPaypalPreferred(collectURLs(payload, nil))
}

// firstExternalNonResource mirrors the next(...) at app.py 4303/4318.
func firstExternalNonResource(urls []string) string {
	for _, item := range urls {
		if isExternalURL(item) && !isIgnoredResourceURL(item) {
			return item
		}
	}
	return ""
}

// extractProviderRedirectURL mirrors opll_extract_provider_redirect_url
// (app.py 4300-4318): the generic (non-PayPal) provider hand-off, e.g. GoPay.
func extractProviderRedirectURL(payload any) string {
	obj := asObject(payload)
	if obj == nil {
		return firstExternalNonResource(collectURLs(payload, nil))
	}
	if nextAction := asObject(obj.Get("next_action")); nextAction != nil {
		if pyStrOr(nextAction.Get("type")) == "redirect_to_url" {
			if redirectTo := asObject(nextAction.Get("redirect_to_url")); redirectTo != nil {
				if u := pyStrip(pyStrOr(redirectTo.Get("url"))); u != "" {
					return u
				}
			}
		}
	}
	for _, key := range []string{"setup_intent", "payment_intent"} {
		if nested := asObject(obj.Get(key)); nested != nil {
			if found := extractProviderRedirectURL(nested); found != "" {
				return found
			}
		}
	}
	return firstExternalNonResource(collectURLs(payload, nil))
}

// ---------------------------------------------------------------------------
// Submission-attempt diagnostics (app.py 4321-4410)
// ---------------------------------------------------------------------------

// firstNonEmptyField mirrors opll_first_non_empty (app.py 4321-4326).
func firstNonEmptyField(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := pyStrip(values[key]); v != "" {
			return v
		}
	}
	return ""
}

var submissionWantedFields = map[string]bool{
	"error": true, "code": true, "message": true, "reason": true,
	"failure_reason": true, "decline_code": true, "failure_code": true, "failure_message": true,
}

// submissionAttemptFailureFields mirrors opll_submission_attempt_failure_fields
// (app.py 4329-4353): first-seen wins, so wire order matters.
func submissionAttemptFailureFields(submission any) map[string]string {
	found := map[string]string{}
	var walk func(value any)
	walk = func(value any) {
		switch t := value.(type) {
		case *jsonObject:
			for _, key := range t.Keys() {
				item := t.Get(key)
				normalized := pyStrip(key)
				if submissionWantedFields[normalized] {
					if _, exists := found[normalized]; !exists {
						text := ""
						switch iv := item.(type) {
						case string, json.Number, bool:
							text = pyStrip(pyStr(iv))
						case *jsonObject:
							v := iv.Get("message")
							if pyFalsy(v) {
								v = iv.Get("code")
							}
							if pyFalsy(v) {
								v = iv.Get("reason")
							}
							if pyFalsy(v) {
								v = iv.Get("type")
							}
							text = pyStrip(pyStrOr(v))
						}
						if text != "" {
							found[normalized] = truncRunes(text, 240)
						}
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	if asObject(submission) != nil {
		walk(submission)
	}
	return found
}

// findSubmissionAttempt mirrors opll_find_submission_attempt (app.py 4356-4370).
// It never returns nil: "not found" is an EMPTY object, because Python's
// recursive `if found:` check treats an empty submission_attempt dict as a
// miss and keeps searching sibling values.
func findSubmissionAttempt(payload any) *jsonObject {
	switch t := payload.(type) {
	case *jsonObject:
		if item := asObject(t.Get("submission_attempt")); item != nil {
			return item
		}
		for _, key := range t.Keys() {
			if found := findSubmissionAttempt(t.Get(key)); len(found.keys) > 0 {
				return found
			}
		}
	case []any:
		for _, value := range t {
			if found := findSubmissionAttempt(value); len(found.keys) > 0 {
				return found
			}
		}
	}
	return newJSONObject()
}

// submissionState returns submission.get("state") as a string ("" when absent),
// mirroring the Python `submission.get("state") == "..."` comparisons. Python's
// {}.get("state") is None, which never equals a state string.
func submissionState(submission *jsonObject) string {
	if submission == nil {
		return ""
	}
	v := submission.Get("state")
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// stripePayloadDiagnostics mirrors opll_stripe_payload_diagnostics (app.py 4391-4410).
func stripePayloadDiagnostics(payload any, ctx *stripeCtx) string {
	obj := asObject(payload)
	if obj == nil {
		return fmt.Sprintf("payload_type=%s", pythonTypeName(payload))
	}
	keys := append([]string(nil), obj.Keys()...)
	sort.Strings(keys)
	if len(keys) > 12 {
		keys = keys[:12]
	}
	urls := collectURLs(payload, nil)
	paypalCount, baCount, ignoredCount := 0, 0, 0
	for _, item := range urls {
		if isPaypalURL(item) {
			paypalCount++
		}
		if isPaypalBAApproveURL(item) {
			baCount++
		}
		if isIgnoredResourceURL(item) {
			ignoredCount++
		}
	}
	submission := findSubmissionAttempt(payload)
	state := pyStrOr(submission.Get("state"))
	fields := submissionAttemptFailureFields(submission)
	reason := firstNonEmptyField(fields, "reason", "failure_reason", "decline_code", "failure_code", "code")
	code := firstNonEmptyField(fields, "code", "decline_code", "failure_code")
	message := firstNonEmptyField(fields, "message", "failure_message", "error")
	sessionID := ""
	if ctx != nil {
		sessionID = ctx.ElementsSessionID
	}
	return fmt.Sprintf(
		"keys=[%s], urls=%d, paypal_urls=%d, ba_approve_urls=%d, "+
			"ignored_resource_urls=%d, submission_attempt=%s, submission_state=%s, "+
			"submission_reason=%s, submission_code=%s, "+
			"submission_message=%s, ctx_session=%s",
		strings.Join(keys, ","), len(urls), paypalCount, baCount,
		ignoredCount, pyBool(len(submission.Keys()) > 0), orDefault(state, "未知"),
		orDefault(reason, "无"), orDefault(code, "无"),
		orDefault(message, "无"), sessionID,
	)
}

func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func pythonTypeName(v any) string {
	switch t := v.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case json.Number:
		// json.loads yields int for an integer literal and float otherwise.
		if strings.ContainsAny(t.String(), ".eE") {
			return "float"
		}
		return "int"
	case []any:
		return "list"
	}
	return "object"
}

// ---------------------------------------------------------------------------
// Payment-method availability (app.py 4413-4493)
// ---------------------------------------------------------------------------

var knownPaymentMethods = map[string]bool{
	"paypal": true, "card": true, "apple_pay": true, "google_pay": true, "link": true,
	"cashapp": true, "klarna": true, "afterpay_clearpay": true, "amazon_pay": true,
	"gopay": true, "pix": true, "boleto": true,
}

// collectPaymentMethodNames mirrors opll_collect_payment_method_names
// (app.py 4413-4474): scrape the init payload for the offered payment methods.
func collectPaymentMethodNames(payload any) map[string]bool {
	names := map[string]bool{}
	add := func(value any) {
		text := pyLower(pyStrip(pyStrOr(value)))
		if text == "" {
			return
		}
		normalized := strings.ReplaceAll(text, "-", "_")
		switch {
		case knownPaymentMethods[normalized]:
			names[normalized] = true
		case strings.Contains(normalized, "paypal"):
			names["paypal"] = true
		case strings.Contains(normalized, "apple") && strings.Contains(normalized, "pay"):
			names["apple_pay"] = true
		case strings.Contains(normalized, "google") && strings.Contains(normalized, "pay"):
			names["google_pay"] = true
		}
	}
	isScalar := func(v any) bool {
		switch v.(type) {
		case string, json.Number, bool:
			return true
		}
		return false
	}
	listKeys := map[string]bool{
		"payment_method_types":           true,
		"automatic_payment_method_types": true,
		"payment_methods":                true,
		"available_payment_method_types": true,
		"ordered_payment_method_types":   true,
	}
	dictKeys := map[string]bool{"type": true, "code": true, "name": true, "id": true}
	var walk func(value any, key string)
	walk = func(value any, key string) {
		keyText := pyLower(key)
		switch t := value.(type) {
		case *jsonObject:
			if strings.Contains(keyText, "payment_method") || dictKeys[keyText] {
				for _, k := range t.Keys() {
					if item := t.Get(k); isScalar(item) {
						add(item)
					}
				}
			}
			for _, childKey := range t.Keys() {
				walk(t.Get(childKey), childKey)
			}
		case []any:
			if strings.Contains(keyText, "payment_method") || listKeys[keyText] {
				for _, item := range t {
					if isScalar(item) {
						add(item)
					} else {
						walk(item, keyText)
					}
				}
				return
			}
			for _, item := range t {
				walk(item, keyText)
			}
		default:
			if isScalar(value) && strings.Contains(keyText, "payment_method") {
				add(value)
			}
		}
	}
	walk(payload, "")
	return names
}

func sortedNames(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// requirePaymentMethod mirrors opll_require_payment_method (app.py 4489-4493).
func requirePaymentMethod(initPayload any, paymentMethod string) error {
	method := pyLower(pyStrip(paymentMethod))
	methods := collectPaymentMethodNames(initPayload)
	if len(methods) > 0 && !methods[method] {
		return &PaymentMethodNotSupportedError{
			PaymentMethod:  method,
			MethodsSummary: strings.Join(sortedNames(methods), ","),
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ChatGPT approve (app.py 4507-4554)
// ---------------------------------------------------------------------------

// chatgptApprove mirrors opll_chatgpt_approve (app.py 4507-4538).
func chatgptApprove(chatgpt *session, csID string, checkout Checkout) error {
	entity := processorEntityForCountry(checkout.BillingCountry, checkout.ProcessorEntity)
	// Python wrapped the sentinel ping in a bare try/except: a failure here is
	// deliberately ignored and must never abort the approve.
	_, _, _ = chatgpt.postJSON("https://chatgpt.com/backend-api/sentinel/ping", "{}", formPairs{
		{"referer", "https://chatgpt.com/"},
		{"x-openai-target-path", "/backend-api/sentinel/ping"},
		{"x-openai-target-route", "/backend-api/sentinel/ping"},
	})
	status, raw, err := chatgpt.postJSON("https://chatgpt.com/backend-api/payments/checkout/approve", approveBody(csID, entity), formPairs{
		{"referer", fmt.Sprintf("https://chatgpt.com/checkout/%s/%s", entity, csID)},
		{"x-openai-target-path", "/backend-api/payments/checkout/approve"},
		{"x-openai-target-route", "/backend-api/payments/checkout/approve"},
	})
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("chatgpt approve failed: HTTP %d %s", status, truncRunes(string(raw), 500))
	}
	var result any
	if data, derr := decodeOrderedObject(raw); derr == nil {
		result = data.Get("result")
	} else {
		result = "" // Python: except -> result = ""
	}
	normalized := pyLower(pyStrip(pyStrOr(result)))
	if opllApproveBurstResults[normalized] {
		return &chatgptApproveBlockedError{msg: fmt.Sprintf("chatgpt approve retryable result: %s", pyRepr(normalized))}
	}
	if s, ok := result.(string); !ok || s != "approved" {
		return fmt.Errorf("chatgpt approve unexpected result: %s", pyRepr(result))
	}
	return nil
}

// chatgptApproveWithRetry mirrors opll_chatgpt_approve_with_retry
// (app.py 4541-4554): 3 attempts, 1s apart, aborting early on a burst block.
// Python returned the live session; no caller used it, so Go returns only error.
func chatgptApproveWithRetry(accessToken, csID string, checkout Checkout, proxyURL string) error {
	lastError := ""
	for i := 0; i < 3; i++ {
		chatgpt, err := newChatGPTSession(accessToken, proxyURL)
		if err == nil {
			err = chatgptApprove(chatgpt, csID, checkout)
			if err == nil {
				return nil
			}
		}
		var blocked *chatgptApproveBlockedError
		if errors.As(err, &blocked) {
			lastError = err.Error()
			break
		}
		lastError = err.Error()
		time.Sleep(time.Second)
	}
	return fmt.Errorf("ChatGPT approve 连续失败: %s", lastError)
}

// ---------------------------------------------------------------------------
// Stripe payment-page polling (app.py 4557-4661)
// ---------------------------------------------------------------------------

// paymentPageParams builds the shared GET query for the payment_pages poll
// (app.py 4561-4574 / 4603-4616).
func paymentPageParams(ctx *stripeCtx, stripePK, elementsLocale string) formPairs {
	sessionID := ""
	stripeJSID := ""
	if ctx != nil {
		sessionID = ctx.ElementsSessionID
		stripeJSID = ctx.StripeJSID
	}
	if sessionID == "" {
		sessionID = "elements_session_" + randomUUIDHex()[:11]
	}
	if stripeJSID == "" {
		stripeJSID = randomUUID()
	}
	return formPairs{
		{"elements_session_client[client_betas][0]", "custom_checkout_server_updates_1"},
		{"elements_session_client[client_betas][1]", "custom_checkout_manual_approval_1"},
		{"elements_session_client[elements_init_source]", "custom_checkout"},
		{"elements_session_client[referrer_host]", "chatgpt.com"},
		{"elements_session_client[session_id]", sessionID},
		{"elements_session_client[stripe_js_id]", stripeJSID},
		{"elements_session_client[locale]", elementsLocale},
		{"elements_session_client[is_aggregation_expected]", "false"},
		{"elements_options_client[saved_payment_method][enable_save]", "never"},
		{"elements_options_client[saved_payment_method][enable_redisplay]", "never"},
		{"key", stripePK},
		{"_stripe_version", stripeVersionFull},
	}
}

// pollPaymentPage is the shared body of opll_stripe_payment_page_redirect_url
// (app.py 4557-4596) and opll_stripe_payment_page_provider_redirect_url
// (app.py 4599-4638) — identical except for the URL extractor and the timeout
// message. Poll cadence (1s) and deadline arithmetic are preserved exactly.
func pollPaymentPage(st *session, csID, stripePK, paymentLocale string, timeoutSeconds int, ctx *stripeCtx, extract func(any) string, timeoutLabel string) (string, error) {
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	_, elementsLocale := localeParts(paymentLocale)
	params := paymentPageParams(ctx, stripePK, elementsLocale)
	lastErr := ""
	for time.Now().Before(deadline) {
		status, raw, err := st.getWithParams("https://api.stripe.com/v1/payment_pages/"+csID, params)
		if err != nil {
			// Python let a transport error propagate out of the poll loop.
			return "", err
		}
		if status == 200 {
			payload, derr := decodeOrderedObject(raw)
			if derr != nil {
				return "", derr
			}
			if redirectURL := extract(payload); redirectURL != "" {
				return redirectURL, nil
			}
			submission := findSubmissionAttempt(payload)
			switch submissionState(submission) {
			case "requires_approval":
				return "", &stripeRequiresApprovalError{msg: "payment page requires ChatGPT approval"}
			case "failed":
				return "", fmt.Errorf("stripe submission failed: %s", stripePayloadDiagnostics(payload, ctx))
			}
			lastErr = stripePayloadDiagnostics(payload, ctx)
		} else {
			lastErr = fmt.Sprintf("HTTP %d %s", status, truncRunes(string(raw), 120))
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("%s: %s", timeoutLabel, lastErr)
}

// stripePaymentPageRedirectURL mirrors opll_stripe_payment_page_redirect_url
// (app.py 4557-4596).
func stripePaymentPageRedirectURL(st *session, csID, stripePK string, timeoutSeconds int, ctx *stripeCtx) (string, error) {
	return pollPaymentPage(st, csID, stripePK, "en", timeoutSeconds, ctx, extractRedirectToURL, "redirect url resolution timeout")
}

// stripePaymentPageProviderRedirectURL mirrors
// opll_stripe_payment_page_provider_redirect_url (app.py 4599-4638).
func stripePaymentPageProviderRedirectURL(st *session, csID, stripePK string, timeoutSeconds int, ctx *stripeCtx) (string, error) {
	return pollPaymentPage(st, csID, stripePK, "en", timeoutSeconds, ctx, extractProviderRedirectURL, "provider redirect url resolution timeout")
}

// resolveExternalRedirect mirrors opll_resolve_external_redirect (app.py 4641-4661):
// walk up to 5 non-following redirect hops until a PayPal success URL or a
// preferred host is reached. preferredHosts nil == Python's empty tuple.
func resolveExternalRedirect(st *session, redirectURL string, preferredHosts []string) string {
	current := pyStrip(redirectURL)
	// tls-client follows redirects by default; Python used allow_redirects=False.
	st.c.HTTP.SetFollowRedirect(false)
	defer st.c.HTTP.SetFollowRedirect(true)
	for i := 0; i < 5; i++ {
		if current == "" {
			return ""
		}
		if OpllIsPaypalSuccessURL(current) {
			return current
		}
		host, _ := netloc(current)
		for _, item := range preferredHosts {
			if host == item || strings.HasSuffix(host, "."+item) {
				return current
			}
		}
		resp, _, err := st.request("GET", current, nil, nil)
		if err != nil {
			// Python: except Exception -> return current.
			return current
		}
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
		default:
			return current
		}
		location := pyStrip(resp.Header.Get("Location"))
		if location == "" {
			return current
		}
		// urljoin, NOT url.Parse+ResolveReference: net/url errors out on a bad
		// percent-escape or a non-numeric port (which made the walk return the
		// PREVIOUS hop as the final link) and re-encodes what it does accept.
		current = pyURLJoin(current, location)
	}
	return current
}

// ---------------------------------------------------------------------------
// Stripe confirm (app.py 4664-4744)
// ---------------------------------------------------------------------------

// stripeConfirm mirrors opll_stripe_confirm (app.py 4664-4710).
func stripeConfirm(st *session, csID, pmID, stripePK string, initPayload *jsonObject, ctx *stripeCtx, checkout Checkout, stripeHostedURL, paymentMethodType string) (*jsonObject, error) {
	returnURL := stripeConfirmReturnURL(csID, checkout, stripeHostedURL)
	runtimeVersion := orDefault(ctx.RuntimeVersion, defaultStripeRuntimeVersion)
	paymentMethodType = normalizePaymentMethodType(paymentMethodType)
	initChecksum := pyStrOr(initPayload.Get("init_checksum"))
	if initChecksum == "" {
		initChecksum = ctx.InitChecksum
	}
	expected := ctx.CheckoutAmount
	if expected == "" {
		expected = expectedAmount(initPayload)
	}
	form := confirmForm(csID, pmID, stripePK, initChecksum, runtimeVersion, expected, paymentMethodType, returnURL, ctx,
		randomUUIDHex(), randomUUIDHex(), randomUUIDHex())
	status, raw, err := st.postForm("https://api.stripe.com/v1/payment_pages/"+csID+"/confirm", form)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, errors.New(stripeErrorSummary("stripe confirm failed", raw))
	}
	return decodeOrderedObject(raw)
}

// confirmForm is the `data=` dict of opll_stripe_confirm (app.py 4670-4705),
// in Python insertion order.
func confirmForm(csID, pmID, stripePK, initChecksum, runtimeVersion, expected, paymentMethodType, returnURL string, ctx *stripeCtx, guid, muid, sid string) formPairs {
	return formPairs{
		{"guid", guid},
		{"muid", muid},
		{"sid", sid},
		{"payment_method", pmID},
		{"init_checksum", initChecksum},
		{"version", runtimeVersion},
		{"expected_amount", expected},
		{"expected_payment_method_type", paymentMethodType},
		{"return_url", returnURL},
		{"elements_session_client[session_id]", ctx.ElementsSessionID},
		{"elements_session_client[locale]", orDefault(ctx.Locale, "en")},
		{"elements_session_client[referrer_host]", "chatgpt.com"},
		{"elements_session_client[is_aggregation_expected]", "false"},
		{"elements_session_client[elements_init_source]", "custom_checkout"},
		{"elements_session_client[stripe_js_id]", ctx.StripeJSID},
		{"elements_session_client[client_betas][0]", "custom_checkout_server_updates_1"},
		{"elements_session_client[client_betas][1]", "custom_checkout_manual_approval_1"},
		{"elements_options_client[saved_payment_method][enable_save]", "never"},
		{"elements_options_client[saved_payment_method][enable_redisplay]", "never"},
		{"client_attribution_metadata[client_session_id]", ctx.StripeJSID},
		{"client_attribution_metadata[checkout_session_id]", csID},
		{"client_attribution_metadata[checkout_config_id]", ctx.ConfigID},
		{"client_attribution_metadata[elements_session_id]", ctx.ElementsSessionID},
		{"client_attribution_metadata[elements_session_config_id]", ctx.ElementsSessionConfigID},
		{"client_attribution_metadata[merchant_integration_source]", "checkout"},
		{"client_attribution_metadata[merchant_integration_subtype]", "payment-element"},
		{"client_attribution_metadata[merchant_integration_version]", "custom"},
		{"client_attribution_metadata[payment_intent_creation_flow]", "deferred"},
		{"client_attribution_metadata[payment_method_selection_flow]", "automatic"},
		{"client_attribution_metadata[merchant_integration_additional_elements][0]", "payment"},
		{"client_attribution_metadata[merchant_integration_additional_elements][1]", "address"},
		{"consent[terms_of_service]", "accepted"},
		{"key", stripePK},
		{"_stripe_version", stripeVersionFull},
	}
}

// redirectURLAfterConfirm mirrors opll_redirect_url_after_confirm (app.py 4713-4727).
func redirectURLAfterConfirm(accessToken string, st *session, confirmPayload *jsonObject, csID, stripePK string, ctx *stripeCtx, checkout Checkout, approveProxyURL string) (string, error) {
	if redirectURL := extractRedirectToURL(confirmPayload); redirectURL != "" {
		return redirectURL, nil
	}
	submission := findSubmissionAttempt(confirmPayload)
	switch submissionState(submission) {
	case "requires_approval":
		if err := chatgptApproveWithRetry(accessToken, csID, checkout, approveProxyURL); err != nil {
			return "", err
		}
		return stripePaymentPageRedirectURL(st, csID, stripePK, 45, ctx)
	case "failed":
		return "", fmt.Errorf("stripe submission failed: %s", stripePayloadDiagnostics(confirmPayload, ctx))
	}
	url30, err := stripePaymentPageRedirectURL(st, csID, stripePK, 30, ctx)
	if err == nil {
		return url30, nil
	}
	var needsApproval *stripeRequiresApprovalError
	if !errors.As(err, &needsApproval) {
		return "", err
	}
	if aerr := chatgptApproveWithRetry(accessToken, csID, checkout, approveProxyURL); aerr != nil {
		return "", aerr
	}
	return stripePaymentPageRedirectURL(st, csID, stripePK, 45, ctx)
}

// providerRedirectURLAfterConfirm mirrors opll_provider_redirect_url_after_confirm
// (app.py 4730-4744).
func providerRedirectURLAfterConfirm(accessToken string, st *session, confirmPayload *jsonObject, csID, stripePK string, ctx *stripeCtx, checkout Checkout, approveProxyURL string) (string, error) {
	if redirectURL := extractProviderRedirectURL(confirmPayload); redirectURL != "" {
		return redirectURL, nil
	}
	submission := findSubmissionAttempt(confirmPayload)
	switch submissionState(submission) {
	case "requires_approval":
		if err := chatgptApproveWithRetry(accessToken, csID, checkout, approveProxyURL); err != nil {
			return "", err
		}
		return stripePaymentPageProviderRedirectURL(st, csID, stripePK, 45, ctx)
	case "failed":
		return "", fmt.Errorf("stripe submission failed: %s", stripePayloadDiagnostics(confirmPayload, ctx))
	}
	url30, err := stripePaymentPageProviderRedirectURL(st, csID, stripePK, 30, ctx)
	if err == nil {
		return url30, nil
	}
	var needsApproval *stripeRequiresApprovalError
	if !errors.As(err, &needsApproval) {
		return "", err
	}
	if aerr := chatgptApproveWithRetry(accessToken, csID, checkout, approveProxyURL); aerr != nil {
		return "", aerr
	}
	return stripePaymentPageProviderRedirectURL(st, csID, stripePK, 45, ctx)
}

// ---------------------------------------------------------------------------
// Link generators (app.py 4747-4918)
// ---------------------------------------------------------------------------

// comboAttemptOrder mirrors opll_combo_attempt_order (app.py 4747-4759):
// (checkout_country, payment_method_country) pairs to try, in priority order.
func comboAttemptOrder(country string) [][2]string {
	requested := normalizeOpllCountry(country)
	ordered := [][2]string{{requested, requested}}
	if requested == "DE" {
		ordered = append(ordered, [2]string{"US", "US"}, [2]string{"DE", "US"}, [2]string{"US", "DE"})
	}
	result := [][2]string{}
	seen := map[[2]string]bool{}
	for _, item := range ordered {
		if seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

// linkResultFromCheckout spreads a Checkout into a LinkResult, mirroring the
// Python `{**checkout, ...}` dict spread in the generators.
func linkResultFromCheckout(c Checkout) *LinkResult {
	return &LinkResult{
		CSID:                 c.CSID,
		ProcessorEntity:      c.ProcessorEntity,
		StripePublishableKey: c.StripePublishableKey,
		BillingCountry:       c.BillingCountry,
		Currency:             c.Currency,
		CheckoutURL:          c.CheckoutURL,
	}
}

// paypalPipeline is the shared body of generate_opll_paypal_long_link
// (app.py 4762-4805, per combo) and generate_opll_paypal_long_link_from_checkout
// (app.py 4813-4858). The two Python functions are line-for-line identical
// after the checkout is in hand.
func paypalPipeline(accessToken string, checkout Checkout, followupProxyURL, approveProxyURL string, forceLegacyPaypal bool, pmCountry string, hostedFallbackURL string) (*LinkResult, error) {
	st, err := newStripeSession(followupProxyURL)
	if err != nil {
		return nil, err
	}
	initPayload, err := stripeInit(checkout.CSID, checkout.BillingCountry, checkout.Currency, followupProxyURL, "en", st, nil, &checkout)
	if err != nil {
		return nil, err
	}
	// Python: str(init.stripe_hosted_url or checkout.checkout_url or "").strip()
	// — the `or` chain runs BEFORE the strip, so an all-whitespace
	// stripe_hosted_url wins the chain and then strips to "", and the fallback
	// is never consulted.
	stripeHostedURL := pyStrOr(initPayload.Get("stripe_hosted_url"))
	if stripeHostedURL == "" {
		stripeHostedURL = hostedFallbackURL
	}
	stripeHostedURL = pyStrip(stripeHostedURL)
	if stripeHostedURL == "" {
		keys := append([]string(nil), initPayload.Keys()...)
		sort.Strings(keys)
		return nil, fmt.Errorf("stripe init response missing stripe_hosted_url, keys=%s", pyListRepr(keys))
	}
	if !forceLegacyPaypal {
		if err := requirePaymentMethod(initPayload, "paypal"); err != nil {
			return nil, err
		}
	}
	hostedLongURL := toOpenAIPayURL(stripeHostedURL)
	stripePK := stripeKeyForCheckout(&checkout)
	ctx := newStripeContext(initPayload, "en", nil)
	if ctx.Currency == "" {
		ctx.Currency = pyLower(checkout.Currency)
	}
	stripeAmount, stripeAmountSource := stripeAmountInfo(initPayload)
	billing, err := billingForCountry(pmCountry)
	if err != nil {
		return nil, err
	}
	pmID, err := stripeCreatePaymentMethod(st, checkout.CSID, ctx, billing, stripePK, "paypal")
	if err != nil {
		return nil, err
	}
	confirmPayload, err := stripeConfirm(st, checkout.CSID, pmID, stripePK, initPayload, ctx, checkout, stripeHostedURL, "paypal")
	if err != nil {
		return nil, err
	}
	stripeRedirectURL, err := redirectURLAfterConfirm(accessToken, st, confirmPayload, checkout.CSID, stripePK, ctx, checkout, approveProxyURL)
	if err != nil {
		return nil, err
	}
	providerURL := stripeRedirectURL
	if !OpllIsPaypalSuccessURL(stripeRedirectURL) {
		providerURL = resolveExternalRedirect(st, stripeRedirectURL, []string{"paypal.com"})
	}
	if !OpllIsPaypalSuccessURL(providerURL) {
		resourceHint := ""
		if isIgnoredResourceURL(providerURL) {
			resourceHint = "仅发现 Stripe 资源 URL，未发现 PayPal BA approve 链；"
		}
		shown := providerURL
		if shown == "" {
			shown = stripeRedirectURL
		}
		return nil, fmt.Errorf("%s未提取到可用的 PayPal 跳转链接；当前结果: %s", resourceHint, shown)
	}
	result := linkResultFromCheckout(checkout)
	result.PaymentMethodCountry = pmCountry
	result.PaymentMethodID = pmID
	result.StripeHostedURL = stripeHostedURL
	result.StripeRedirectURL = stripeRedirectURL
	result.ProviderRedirectURL = providerURL
	result.LongURL = providerURL
	if result.LongURL == "" {
		result.LongURL = hostedLongURL
	}
	result.StripeAmount = stripeAmount
	result.StripeAmountSource = stripeAmountSource
	return result, nil
}

// GenerateOpllPaypalLongLink mirrors generate_opll_paypal_long_link
// (app.py 4762-4810): create a fresh checkout and drive the Stripe PayPal flow
// until a PayPal billing-agreement approve link falls out.
//
// It walks comboAttemptOrder (DE additionally retries via US), collecting
// per-combo failures into ProviderError. An amount mismatch aborts immediately
// (never retried); every other error moves on to the next combo.
//
// Proxy roles: createProxyURL for the ChatGPT checkout call, followupProxyURL
// for all Stripe calls (defaults to create), approveProxyURL for the ChatGPT
// approve call (defaults to followup).
func GenerateOpllPaypalLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string, forceLegacyPaypal bool) (*LinkResult, error) {
	_ = currency // Python accepts and ignores it; the country decides the currency.
	createProxyURL = pyStrip(createProxyURL)
	followupProxyURL = pyStrip(followupProxyURL)
	if followupProxyURL == "" {
		followupProxyURL = createProxyURL
	}
	approveProxyURL = pyStrip(approveProxyURL)
	if approveProxyURL == "" {
		approveProxyURL = followupProxyURL
	}
	failures := []string{}
	requestedCountry := normalizeOpllCountry(country)
	for _, combo := range comboAttemptOrder(requestedCountry) {
		checkoutCountry, pmCountry := combo[0], combo[1]
		result, err := func() (*LinkResult, error) {
			checkout, err := OpllCreateCheckout(accessToken, checkoutCountry, currencyForCountry(checkoutCountry), createProxyURL)
			if err != nil {
				return nil, err
			}
			return paypalPipeline(accessToken, checkout, followupProxyURL, approveProxyURL, forceLegacyPaypal, pmCountry, "")
		}()
		if err != nil {
			var mismatch *models.AmountMismatchError
			if errors.As(err, &mismatch) {
				return nil, err
			}
			failures = append(failures, fmt.Sprintf("%s+%s: %s", checkoutCountry, pmCountry, OpllShortErrorDefault(err.Error())))
			continue
		}
		result.Fallback = checkoutCountry != requestedCountry || pmCountry != requestedCountry
		result.ProviderError = strings.Join(failures, "; ")
		if err := applyAmountCheck(result, targetAmount); err != nil {
			// Python raised AmountMismatchError out of the loop unchanged.
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("所有组合均未提取到 PayPal BA approve 链；%s", strings.Join(failures, "; "))
}

// GenerateOpllPaypalLongLinkFromCheckout mirrors
// generate_opll_paypal_long_link_from_checkout (app.py 4813-4858): continue the
// PayPal flow from a checkout that already exists — typically one produced by a
// browser-claimed trial short link and parsed with OpllCheckoutFromURL.
//
// Unlike the Python version this does not mutate the caller's checkout; the
// normalized billing country/currency live on the returned LinkResult.
func GenerateOpllPaypalLongLinkFromCheckout(accessToken string, checkout Checkout, followupProxyURL, approveProxyURL, targetAmount string, forceLegacyPaypal bool) (*LinkResult, error) {
	followupProxyURL = pyStrip(followupProxyURL)
	approveProxyURL = pyStrip(approveProxyURL)
	if approveProxyURL == "" {
		approveProxyURL = followupProxyURL
	}
	// Python: normalize_opll_country(str(checkout.get("billing_country") or "US")).
	billingCountry := checkout.BillingCountry
	if billingCountry == "" {
		billingCountry = "US"
	}
	checkoutCountry := normalizeOpllCountry(billingCountry)
	checkout.BillingCountry = checkoutCountry
	if checkout.Currency == "" {
		checkout.Currency = currencyForCountry(checkoutCountry)
	}
	checkout.Currency = pyUpper(checkout.Currency)
	result, err := paypalPipeline(accessToken, checkout, followupProxyURL, approveProxyURL, forceLegacyPaypal, checkoutCountry, checkout.CheckoutURL)
	if err != nil {
		return nil, err
	}
	result.Fallback = false
	result.ProviderError = ""
	if err := applyAmountCheck(result, targetAmount); err != nil {
		return nil, err
	}
	return result, nil
}

// GenerateOpllGopayLongLink mirrors generate_opll_gopay_long_link
// (app.py 4861-4896): the Indonesian GoPay variant. The payment method is
// always built with ID billing details and the provider redirect is resolved
// without a preferred-host shortcut.
func GenerateOpllGopayLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string) (*LinkResult, error) {
	_ = currency // Python accepts and ignores it.
	createProxyURL = pyStrip(createProxyURL)
	followupProxyURL = pyStrip(followupProxyURL)
	if followupProxyURL == "" {
		followupProxyURL = createProxyURL
	}
	approveProxyURL = pyStrip(approveProxyURL)
	if approveProxyURL == "" {
		approveProxyURL = followupProxyURL
	}
	// Python: normalize_opll_country(country or "ID").
	if country == "" {
		country = "ID"
	}
	checkoutCountry := normalizeOpllCountry(country)
	checkout, err := OpllCreateCheckout(accessToken, checkoutCountry, currencyForCountry(checkoutCountry), createProxyURL)
	if err != nil {
		return nil, err
	}
	st, err := newStripeSession(followupProxyURL)
	if err != nil {
		return nil, err
	}
	initPayload, err := stripeInit(checkout.CSID, checkout.BillingCountry, checkout.Currency, followupProxyURL, "en", st, nil, &checkout)
	if err != nil {
		return nil, err
	}
	stripeHostedURL := pyStrip(pyStrOr(initPayload.Get("stripe_hosted_url")))
	if stripeHostedURL == "" {
		keys := append([]string(nil), initPayload.Keys()...)
		sort.Strings(keys)
		return nil, fmt.Errorf("stripe init response missing stripe_hosted_url, keys=%s", pyListRepr(keys))
	}
	hostedLongURL := toOpenAIPayURL(stripeHostedURL)
	stripePK := stripeKeyForCheckout(&checkout)
	ctx := newStripeContext(initPayload, "en", nil)
	if ctx.Currency == "" {
		ctx.Currency = pyLower(checkout.Currency)
	}
	stripeAmount, stripeAmountSource := stripeAmountInfo(initPayload)
	billing, err := billingForCountry("ID")
	if err != nil {
		return nil, err
	}
	pmID, err := stripeCreatePaymentMethod(st, checkout.CSID, ctx, billing, stripePK, "gopay")
	if err != nil {
		return nil, err
	}
	confirmPayload, err := stripeConfirm(st, checkout.CSID, pmID, stripePK, initPayload, ctx, checkout, stripeHostedURL, "gopay")
	if err != nil {
		return nil, err
	}
	stripeRedirectURL, err := providerRedirectURLAfterConfirm(accessToken, st, confirmPayload, checkout.CSID, stripePK, ctx, checkout, approveProxyURL)
	if err != nil {
		return nil, err
	}
	providerURL := ""
	if stripeRedirectURL != "" {
		providerURL = resolveExternalRedirect(st, stripeRedirectURL, nil)
	}
	longURL := providerURL
	if longURL == "" {
		longURL = stripeRedirectURL
	}
	if longURL == "" {
		longURL = hostedLongURL
	}
	if longURL == "" || !isExternalURL(longURL) || isIgnoredResourceURL(longURL) {
		shown := longURL
		if shown == "" {
			shown = stripeRedirectURL
		}
		if shown == "" {
			shown = stripeHostedURL
		}
		return nil, fmt.Errorf("未提取到有效 GoPay 跳转长链；当前结果: %s", shown)
	}
	result := linkResultFromCheckout(checkout)
	result.PaymentMethodCountry = "ID"
	result.PaymentMethodID = pmID
	result.StripeHostedURL = stripeHostedURL
	result.StripeRedirectURL = stripeRedirectURL
	result.ProviderRedirectURL = longURL
	result.LongURL = longURL
	result.PaymentMethodType = "gopay"
	result.StripeAmount = stripeAmount
	result.StripeAmountSource = stripeAmountSource
	if err := applyAmountCheck(result, targetAmount); err != nil {
		return nil, err
	}
	return result, nil
}

// GenerateOpllHostedLongLink mirrors generate_opll_hosted_long_link
// (app.py 4899-4918): the cardless / Apple Pay variant. It stops after the
// Stripe init and just rehosts the hosted checkout page on pay.openai.com — no
// payment method, no confirm, no approve. approveProxyURL is accepted for
// signature parity and, exactly as in Python, never used.
func GenerateOpllHostedLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string) (*LinkResult, error) {
	createProxyURL = pyStrip(createProxyURL)
	followupProxyURL = pyStrip(followupProxyURL)
	if followupProxyURL == "" {
		followupProxyURL = createProxyURL
	}
	approveProxyURL = pyStrip(approveProxyURL)
	if approveProxyURL == "" {
		approveProxyURL = followupProxyURL
	}
	_ = approveProxyURL
	checkout, err := OpllCreateCheckout(accessToken, country, currency, createProxyURL)
	if err != nil {
		return nil, err
	}
	initPayload, err := stripeInit(checkout.CSID, checkout.BillingCountry, checkout.Currency, followupProxyURL, "en", nil, nil, &checkout)
	if err != nil {
		return nil, err
	}
	stripeHostedURL := pyStrip(pyStrOr(initPayload.Get("stripe_hosted_url")))
	if stripeHostedURL == "" {
		keys := append([]string(nil), initPayload.Keys()...)
		sort.Strings(keys)
		return nil, fmt.Errorf("stripe init response missing stripe_hosted_url, keys=%s", pyListRepr(keys))
	}
	stripeAmount, stripeAmountSource := stripeAmountInfo(initPayload)
	longURL := toOpenAIPayURL(stripeHostedURL)
	if longURL == "" {
		longURL = stripeCheckoutLongURL(checkout.CSID, checkout.BillingCountry, checkout.ProcessorEntity)
	}
	result := linkResultFromCheckout(checkout)
	result.StripeHostedURL = stripeHostedURL
	result.LongURL = longURL
	result.StripeAmount = stripeAmount
	result.StripeAmountSource = stripeAmountSource
	if err := applyAmountCheck(result, targetAmount); err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Retryability classification (app.py 3451-3511)
// ---------------------------------------------------------------------------

// nonRetryableMarkers mirrors the marker tuple at app.py 3457-3468.
var nonRetryableMarkers = []string{
	"金额不匹配",
	"amount mismatch",
	"试用短链必须通过浏览器",
	"billing country must match request country",
	"当前 checkout 不支持 paypal",
	"current checkout does not support paypal",
	"checkout does not support paypal",
	"confirm_error_reason=payment_method_types_mismatch",
	"token_invalidated",
	"authentication token has been invalidated",
}

// OpllIsNonRetryableLinkError mirrors opll_is_non_retryable_link_error
// (app.py 3451-3469): true when swapping proxies cannot possibly help.
func OpllIsNonRetryableLinkError(err error) bool {
	var notSupported *PaymentMethodNotSupportedError
	if errors.As(err, &notSupported) {
		return true
	}
	var mismatch *models.AmountMismatchError
	if errors.As(err, &mismatch) {
		return true
	}
	text := pyLower(errText(err))
	for _, marker := range nonRetryableMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// OpllNonRetryableStatus mirrors opll_non_retryable_status (app.py 3472-3490):
// the account-row status text for a non-retryable link failure.
func OpllNonRetryableStatus(err error) string {
	var notSupported *PaymentMethodNotSupportedError
	if errors.As(err, &notSupported) {
		return "支付方式不支持"
	}
	var mismatch *models.AmountMismatchError
	if errors.As(err, &mismatch) {
		return "金额不匹配"
	}
	text := pyLower(errText(err))
	switch {
	case strings.Contains(text, "金额不匹配") || strings.Contains(text, "amount mismatch"):
		return "金额不匹配"
	case strings.Contains(text, "试用短链必须通过浏览器"):
		return "短链需浏览器"
	case strings.Contains(text, "当前 checkout 不支持 paypal") || strings.Contains(text, "checkout does not support paypal"):
		return "支付方式不支持"
	case strings.Contains(text, "confirm_error_reason=payment_method_types_mismatch"):
		return "支付方式不匹配"
	case strings.Contains(text, "billing country must match request country"):
		return "地区不匹配"
	case strings.Contains(text, "token_invalidated") || strings.Contains(text, "authentication token has been invalidated"):
		return "Token失效"
	}
	return "不可自动重试"
}

// OpllNonRetryableHint mirrors opll_non_retryable_hint (app.py 3493-3511): the
// long operator-facing explanation shown under a non-retryable link failure.
func OpllNonRetryableHint(err error) string {
	var notSupported *PaymentMethodNotSupportedError
	if errors.As(err, &notSupported) {
		return fmt.Sprintf("这张 checkout 的可用支付方式是 %s，没有 %s；客户端不能强行开启。要 PayPal 长链只能换支持 PayPal 的 checkout/地区，或者改用可用支付方式。", notSupported.MethodsSummary, notSupported.PaymentMethod)
	}
	var mismatch *models.AmountMismatchError
	if errors.As(err, &mismatch) {
		return fmt.Sprintf("Stripe 返回金额 %s，但目标金额是 %s；100 通常代表 1.00，2000 通常代表 20.00。要提普通 Plus 长链就把目标金额改成 2000；只筛试用/低额资格就跳过这个账号。", mismatch.ActualAmount, mismatch.TargetAmount)
	}
	text := pyLower(errText(err))
	switch {
	case strings.Contains(text, "金额不匹配") || strings.Contains(text, "amount mismatch"):
		return "Stripe 返回金额和目标金额不一致；要提普通 Plus 长链就改目标金额，只筛试用/低额资格就跳过这个账号。"
	case strings.Contains(text, "试用短链必须通过浏览器"):
		return "试用短链不能用接口批量提链；请单选账号点 Session 生成，让程序打开试用页并点击领取按钮。"
	case strings.Contains(text, "当前 checkout 不支持 paypal") || strings.Contains(text, "checkout does not support paypal"):
		return "这张 checkout 的可用支付方式里没有 PayPal。继续换代理不会让 PayPal 出现；要 PayPal BA 请换支持 PayPal 的 checkout/地区，或者改用当前可用的 Apple Pay/Card/Google Pay/Link 模式。"
	case strings.Contains(text, "confirm_error_reason=payment_method_types_mismatch"):
		return "当前 checkout 不支持 PayPal BA 这种支付方式；这不是注册地不匹配，也不代表账号废了。BR/JP 等地区即使用对应代理和对应注册环境，也可能没有 PayPal BA。要提 PayPal BA 请切 PayPal US/USD；要保留 BR checkout 请改用非 PayPal 支付页模式。"
	case strings.Contains(text, "billing country must match request country"):
		return "账单国家和请求国家不一致；请切换匹配的支付模式或代理出口。"
	case strings.Contains(text, "token_invalidated") || strings.Contains(text, "authentication token has been invalidated"):
		return "Access Token 已失效；请刷新 Session 后再试。"
	}
	return ""
}

// errText mirrors Python str(exc or "") for a possibly-nil error.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// detect_plus_trial_eligibility (app.py:3549-3572)
// ---------------------------------------------------------------------------

// TrialEligibility is the dict returned by detect_plus_trial_eligibility.
type TrialEligibility struct {
	Eligible          bool   `json:"eligible"`
	Status            string `json:"status"`
	Amount            string `json:"amount"`
	AmountSource      string `json:"amount_source"`
	Currency          string `json:"currency"`
	Country           string `json:"country"`
	CheckoutSessionID string `json:"checkout_session_id"`
	ProcessorEntity   string `json:"processor_entity"`
}

// trialEligibleAmounts is the eligibility test from app.py:3562. NOTE this is a
// SET membership over three spellings of zero, deliberately unlike
// opll_apply_amount_check's exact-string compare — Stripe returns the amount as
// "0", "0.0" or "0.00" depending on the entity, and treating only one of them as
// free would misclassify a genuinely free trial as paid.
var trialEligibleAmounts = map[string]bool{"0": true, "0.0": true, "0.00": true}

// detectCurrencyText mirrors app.py:3560
//
//	str(init_payload.get("currency") or checkout.get("currency") or currency).upper()
//
// There is NO strip in that expression. Stripping first (which the port used to
// do) makes a whitespace-only init currency fall through to the checkout's
// currency, but Python keeps it: "  " is truthy, so it wins the `or` chain and
// is reported verbatim as the trial currency.
func detectCurrencyText(initPayload *jsonObject, checkoutCurrency, fallback string) string {
	if v := initPayload.Get("currency"); !pyFalsy(v) {
		return pyUpper(pyStr(v))
	}
	if checkoutCurrency != "" {
		return pyUpper(checkoutCurrency)
	}
	return pyUpper(fallback)
}

// DetectPlusTrialEligibility mirrors detect_plus_trial_eligibility: create a
// checkout, init Stripe, and decide from the resulting amount whether this
// account still qualifies for the free Plus trial.
//
// country defaults to "US" both when empty AND when it normalises away, matching
// the Python `country or "US"` guard being applied before normalisation.
func DetectPlusTrialEligibility(accessToken, proxyURL, country string) (TrialEligibility, error) {
	if pyStrip(country) == "" {
		country = "US"
	}
	country = normalizeOpllCountry(country)
	currency := currencyForCountry(country)

	checkout, err := OpllCreateCheckout(accessToken, country, currency, proxyURL)
	if err != nil {
		return TrialEligibility{}, err
	}
	// Python passes neither payment_locale nor a session/ctx here, so the
	// defaults must match the bare opll_stripe_init call.
	initPayload, err := stripeInit(checkout.CSID, checkout.BillingCountry, checkout.Currency,
		proxyURL, "en", nil, nil, &checkout)
	if err != nil {
		return TrialEligibility{}, err
	}

	amount, amountSource := stripeAmountInfo(initPayload)

	currencyText := detectCurrencyText(initPayload, checkout.Currency, currency)

	eligible := trialEligibleAmounts[pyStrip(amount)]
	status := "not_eligible"
	if eligible {
		status = "eligible"
	}
	return TrialEligibility{
		Eligible:          eligible,
		Status:            status,
		Amount:            amount,
		AmountSource:      amountSource,
		Currency:          currencyText,
		Country:           country,
		CheckoutSessionID: checkout.CSID,
		ProcessorEntity:   checkout.ProcessorEntity,
	}, nil
}
