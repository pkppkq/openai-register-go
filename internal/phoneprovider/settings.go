// Package phoneprovider implements worker.PhoneProvider: the SMSBower-backed
// (plus manual-pool) phone supply used by the registration flow.
//
// It is a port of the phone cluster of the Tk app: _smsbower_settings
// (app.py:14366), _smsbower_next_phone (app.py:16408),
// _smsbower_set_activation_status (app.py:16523), _phone_provider
// (app.py:16535), _wait_for_phone_code (app.py:16639), _extract_phone_code
// (app.py:16669), _phone_receive_limit (app.py:14620) and _phone_is_frozen
// (app.py:14626).
//
// MONEY WARNING: Provider.Next rents a billable SMSBower number on every
// successful call, and the status transitions are what release or burn it —
// 1 = "SMS requested" (app.py:16594), 6 = "activation finished, charge it"
// (app.py:16598), 8 = "cancel the activation" (app.py:16609/16612). Sending 6
// for a number that never delivered a code pays for nothing; skipping 8 leaves
// the rental hanging. Never reorder or "simplify" those calls.
package phoneprovider

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
)

// Settings is the validated result of _smsbower_settings (app.py:14366-14386).
type Settings struct {
	Enabled bool
	APIKey  string
	Service string
	Country string
	// MaxPrice stays a string: Python passes the raw text straight to the API
	// as maxPrice (app.py:16467), so re-formatting it could change the price cap
	// actually sent. "" means "no cap".
	MaxPrice string
}

// SettingsSource is read live on every provider action, exactly like the Tk
// variables were. The three accessors are deliberately separate because Python
// reads them from different places:
//   - SMSBowerEnabled is checked BEFORE validation (app.py:16409-16411), so a
//     malformed service code on a disabled provider is silent.
//   - SMSBowerSettings is the validating read (app.py:16413).
//   - SMSBowerAPIKey is the RAW key var, read without validation by
//     _smsbower_set_activation_status (app.py:16525-16526) and by the "code"
//     action (app.py:16586-16587). Consequence: sent/good/bad/code keep working
//     even when the service code or country id is invalid.
type SettingsSource interface {
	SMSBowerEnabled() bool
	SMSBowerSettings() (Settings, error)
	SMSBowerAPIKey() string
	// PhoneReceiveLimit is _phone_receive_limit (app.py:14620).
	PhoneReceiveLimit() int
}

// Raw mirrors the S15 widget variables as the UI holds them (all strings, like
// the Tk StringVar/IntVar text) and implements SettingsSource by validating on
// each read — matching the Python, which re-read and re-validated the vars on
// every single call rather than caching a struct.
type Raw struct {
	Enabled              bool
	APIKey               string
	Service              string
	Country              string
	MaxPrice             string
	PhoneMaxReceiveCount string
}

func (r Raw) SMSBowerEnabled() bool { return r.Enabled }

func (r Raw) SMSBowerSettings() (Settings, error) { return NormalizeSettings(r) }

// SMSBowerAPIKey mirrors `str(api_key_var.get() or "").strip()` (app.py:16526).
func (r Raw) SMSBowerAPIKey() string { return strings.TrimSpace(r.APIKey) }

func (r Raw) PhoneReceiveLimit() int { return ParseReceiveLimit(r.PhoneMaxReceiveCount) }

var _ SettingsSource = Raw{}

// serviceRe is Python's re.fullmatch(r"[A-Za-z0-9_]+") (app.py:14370). Go's `$`
// (without the m flag) is end-of-text, unlike Python's `$`, so ^...$ here is a
// true fullmatch.
var serviceRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Validation errors carry the exact Python ValueError texts because they are
// user-visible: the message is logged verbatim as
// "SMSBower 设置无效，改用手工手机号池: {exc}" (app.py:16415).
var (
	ErrBadService  = errors.New("SMSBower 服务代码格式不正确")
	ErrBadCountry  = errors.New("SMSBower 国家 ID 必须是数字")
	ErrBadMaxPrice = errors.New("SMSBower 最高单价必须是大于 0 的数字，或留空")
)

// NormalizeSettings ports _smsbower_settings (app.py:14366-14386).
//
// The "or default" chains are Python truthiness on the STRIPPED value, which is
// not the same as Go's TrimSpace alone: "   " strips to "" and therefore falls
// back to the default rather than staying whitespace.
func NormalizeSettings(r Raw) (Settings, error) {
	service := strings.TrimSpace(r.Service)
	if service == "" {
		service = smsbower.DefaultService
	}
	country := strings.TrimSpace(r.Country)
	if country == "" {
		country = smsbower.DefaultCountry
	}
	maxPrice := strings.TrimSpace(r.MaxPrice)

	if !serviceRe.MatchString(service) {
		return Settings{}, ErrBadService
	}
	if !isPyDigitString(country) {
		return Settings{}, ErrBadCountry
	}
	if maxPrice != "" {
		// Python: `float(max_price) <= 0` inside try/except ValueError, so a parse
		// failure and a non-positive value give the SAME message. NaN is left
		// alone on purpose: float("nan") <= 0 is False in Python, so "nan" passes
		// validation here and later makes every price tier ineligible
		// (nan comparisons are all False) — reproduced, not fixed.
		value, err := strconv.ParseFloat(maxPrice, 64)
		if err != nil || value <= 0 {
			return Settings{}, ErrBadMaxPrice
		}
	}

	return Settings{
		Enabled:  r.Enabled,
		APIKey:   strings.TrimSpace(r.APIKey),
		Service:  service,
		Country:  country,
		MaxPrice: maxPrice,
	}, nil
}

// isPyDigitString is `country.isdigit()` (app.py:14372). Python's str.isdigit()
// is Unicode-aware and also true for Numeric_Type=Digit characters outside Nd
// (superscript "²"); Go's unicode.IsDigit is Nd only. The gap is deliberate and
// harmless: a non-Nd "digit" country id is rejected here instead of being
// rejected by the API with BAD_COUNTRY one round-trip later. Empty is false,
// same as Python.
func isPyDigitString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// ParseReceiveLimit ports _phone_receive_limit (app.py:14620-14624):
// max(0, int(value or 0)) with every failure collapsing to 0.
//
// Python's int() strips surrounding whitespace and rejects "5.5"/"0x10"; Atoi on
// a TrimSpace'd string behaves the same way, and the bare `except` maps any
// failure to 0 (unlimited).
func ParseReceiveLimit(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// IsFrozen ports _phone_is_frozen (app.py:14626-14628): limit 0 means unlimited,
// and the comparison is >= so a phone AT the cap is already frozen.
func IsFrozen(phone models.PhoneEntry, receiveLimit int) bool {
	return receiveLimit > 0 && phone.ReceiveCount >= receiveLimit
}
