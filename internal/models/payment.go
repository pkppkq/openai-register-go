package models

import "strings"

// PaymentMode describes one payment-link mode (app.py PAYMENT_MODES value).
type PaymentMode struct {
	Country         string
	Currency        string
	PaymentProvider string // "" or "gopay"
	TrialShortLink  bool
	ApplePayHosted  bool
}

// PaymentModeOrder is the display order of PaymentModes (map iteration is
// unordered; the UI shows modes in this sequence). Keys match PaymentModes.
var PaymentModeOrder = []string{
	"无卡长链接 US/USD", "无卡长链接 BR/BRL", "无卡长链接 DE/EUR", "无卡长链接 FR/EUR",
	"无卡长链接 GB/GBP", "无卡长链接 CA/CAD", "无卡长链接 AU/AUD", "无卡长链接 JP/JPY",
	"GoPay 长链接 ID/IDR", "PayPal 长链接 US/USD", "试用短链 PayPal US/USD",
	"PayPal 长链接 FR/EUR", "Apple Pay 支付页 US/USD", "Apple Pay 支付页 JP/JPY",
}

// PaymentModes mirrors app.py PAYMENT_MODES.
var PaymentModes = map[string]PaymentMode{
	"无卡长链接 US/USD":         {Country: "US", Currency: "USD"},
	"无卡长链接 BR/BRL":         {Country: "BR", Currency: "BRL"},
	"无卡长链接 DE/EUR":         {Country: "DE", Currency: "EUR"},
	"无卡长链接 FR/EUR":         {Country: "FR", Currency: "EUR"},
	"无卡长链接 GB/GBP":         {Country: "GB", Currency: "GBP"},
	"无卡长链接 CA/CAD":         {Country: "CA", Currency: "CAD"},
	"无卡长链接 AU/AUD":         {Country: "AU", Currency: "AUD"},
	"无卡长链接 JP/JPY":         {Country: "JP", Currency: "JPY"},
	"GoPay 长链接 ID/IDR":     {Country: "ID", Currency: "IDR", PaymentProvider: "gopay"},
	"PayPal 长链接 US/USD":    {Country: "US", Currency: "USD"},
	"试用短链 PayPal US/USD":   {Country: "US", Currency: "USD", TrialShortLink: true},
	"PayPal 长链接 FR/EUR":    {Country: "FR", Currency: "EUR"},
	"Apple Pay 支付页 US/USD": {Country: "US", Currency: "USD", ApplePayHosted: true},
	"Apple Pay 支付页 JP/JPY": {Country: "JP", Currency: "JPY", ApplePayHosted: true},
}

// CountryCurrency mirrors app.py COUNTRY_CURRENCY *after* both update()
// passes (base literal 523-529, then the EUR fill-in for EUR_COUNTRIES and the
// explicit extras at 560-565). Omitting those passes made CurrencyForCountry
// return USD for GR/CY/HR/... and for AE/AR/IL/TR/ZA/... — a wrong-currency
// checkout.
var CountryCurrency = map[string]string{
	"AD": "EUR", "AE": "AED", "AR": "ARS", "AT": "EUR", "AU": "AUD", "BE": "EUR", "BH": "BHD",
	"BM": "BMD", "BO": "BOB", "BQ": "USD", "BR": "BRL", "CA": "CAD", "CH": "CHF", "CL": "CLP",
	"CO": "COP", "CY": "EUR", "CZ": "CZK", "DE": "EUR", "DK": "DKK", "EE": "EUR", "ES": "EUR",
	"FI": "EUR", "FR": "EUR", "GB": "GBP", "GR": "EUR", "GU": "USD", "HK": "HKD", "HR": "EUR",
	"ID": "IDR", "IE": "EUR", "IL": "ILS", "IN": "INR", "IT": "EUR", "JP": "JPY", "KR": "KRW",
	"LT": "EUR", "LU": "EUR", "LV": "EUR", "MC": "EUR", "ME": "EUR", "MT": "EUR", "MX": "MXN",
	"MY": "MYR", "NL": "EUR", "NO": "NOK", "NZ": "NZD", "PH": "PHP", "PL": "PLN", "PR": "USD",
	"PT": "EUR", "SE": "SEK", "SG": "SGD", "SI": "EUR", "SK": "EUR", "SM": "EUR", "TH": "THB",
	"TR": "TRY", "TW": "TWD", "UA": "UAH", "UM": "USD", "US": "USD", "VN": "VND", "ZA": "ZAR",
}

// upperFullReplacer covers the code points whose Python str.upper() EXPANDS to
// exactly two ASCII letters. str.upper() is FULL case mapping; strings.ToUpper
// is simple case mapping and leaves all six unchanged. Only "ﬁ" (U+FB01, which
// a PDF or an autocorrecting editor produces routinely) can actually change a
// lookup here — it upper-cases to "FI", a real key — but the table is kept whole
// so the helper stays a faithful str.upper() for two-letter input.
// Enumerated by scanning all 1,112,064 code points for len(chr(cp).upper()) == 2
// with both halves in A-Z; longer expansions (ﬃ→FFI, ŉ→ʼN …) cannot be a
// two-letter country code either way.
var upperFullReplacer = strings.NewReplacer(
	"ß", "SS",
	"ﬀ", "FF",
	"ﬁ", "FI",
	"ﬂ", "FL",
	"ﬅ", "ST",
	"ﬆ", "ST",
)

// CurrencyForCountry mirrors currency_for_country (app.py:2677-2678):
//
//	COUNTRY_CURRENCY.get(str(country or "").upper(), "USD")
//
// NOTE: there is NO .strip(). This used to call strings.TrimSpace, so Go
// answered JPY for " JP " where Python answers USD — a different checkout
// currency for the same stored value. Adjacent helpers such as
// normalize_opll_country DO strip (app.py:2682); this one does not, and the
// difference is deliberate on the Python side, so it is reproduced exactly.
func CurrencyForCountry(country string) string {
	if c, ok := CountryCurrency[strings.ToUpper(upperFullReplacer.Replace(country))]; ok {
		return c
	}
	return "USD"
}
