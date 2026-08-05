// Package models is a faithful Go port of the Python data models, their
// JSON (dict) serialization, and the account/phone/card line parsers from app.py.
//
// Serialization uses map[string]any to preserve exact round-trip compatibility
// with the existing state.json written by the Python app, so old and new can
// coexist during migration.
package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	AccountDefaultGroup     = "未分组"
	AccountAllGroup         = "全部"
	DefaultDomainMailDomain = "mail.example.com"
	TeamEmailDomain         = "students.example.edu"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type MailAccount struct {
	Email              string
	Password           string
	ClientID           string
	RefreshToken       string
	Raw                string
	AccountType        string // default "free"
	Status             string
	OpenaiRT           string
	AuthPhoneNumber    string
	AuthPhoneSMSURL    string
	ReceiveMailbox     string
	MailProvider       string
	CloudMailBase      string
	CloudMailToken     string
	Group              string
	BrowserFingerprint *DeviceFingerprint
}

type PhoneEntry struct {
	Number       string
	SMSURL       string
	Status       string // default "可用"
	LastCode     string
	LastError    string
	ReceiveCount int
}

type PaymentCard struct {
	Card   string
	Month  string
	Year   string
	CVV    string
	Status string // default "未用"
}

type ProxyConfig struct {
	LocalProxy   string
	DynamicProxy string
	ChainURL     string
}

type ProxyHealthResult struct {
	Success       bool
	IP            string
	Country       string
	Region        string
	City          string
	Timezone      string
	Org           string
	ChatGPTStatus int
	StripeStatus  int
	FailedStage   string
	Error         string
}

func (r ProxyHealthResult) Location() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{r.Country, r.Region, r.City} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

// Summary mirrors the Python ProxyHealthResult.summary property: a failure
// descriptor, or a space-joined success line (ip/location/tz/org + status codes).
func (r ProxyHealthResult) Summary() string {
	if !r.Success {
		stage := r.FailedStage
		if stage == "" {
			stage = "unknown"
		}
		if r.Error != "" {
			return "检测失败[" + stage + "]: " + r.Error
		}
		return "检测失败[" + stage + "]"
	}
	parts := make([]string, 0, 6)
	for _, p := range []string{r.IP, r.Location(), r.Timezone, r.Org} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	parts = append(parts, fmt.Sprintf("ChatGPT=%d", r.ChatGPTStatus), fmt.Sprintf("Stripe=%d", r.StripeStatus))
	return strings.Join(parts, " ")
}

type DeviceFingerprint struct {
	UserAgent           string
	Locale              string
	Languages           []string
	Timezone            string
	ViewportWidth       int
	ViewportHeight      int
	ScreenWidth         int
	ScreenHeight        int
	OuterWidth          int
	OuterHeight         int
	DeviceScaleFactor   float64
	HardwareConcurrency int
	DeviceMemory        int
	Platform            string
	Vendor              string // default "Google Inc."
	MaxTouchPoints      int
}

var reChromeMajor = regexp.MustCompile(`Chrome/(\d+)`)
var reChromeFull = regexp.MustCompile(`Chrome/([\d.]+)`)

func (f DeviceFingerprint) ChromeMajor() string {
	if m := reChromeMajor.FindStringSubmatch(f.UserAgent); m != nil {
		return m[1]
	}
	return "146"
}

func (f DeviceFingerprint) ChromeFull() string {
	if m := reChromeFull.FindStringSubmatch(f.UserAgent); m != nil {
		return m[1]
	}
	return "146.0.0.0"
}

type LogRecord struct {
	Seq      int
	TimeText string
	Message  string
	Email    string
	Scope    string // default "global"
}

// ---------------------------------------------------------------------------
// map helpers (mimic Python str()/int()/float() coercion of dict values)
// ---------------------------------------------------------------------------

func mStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "True"
		}
		return "False"
	default:
		return ""
	}
}

func mStrOr(m map[string]any, key, def string) string {
	if s := mStr(m, key); s != "" {
		return s
	}
	return def
}

func mInt(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case float32:
		return int(t)
	// A map that came from encoding/json only ever holds float64, but one built
	// in-process holds native ints — and the fingerprint persistence payload is
	// exactly that (Python's is an in-memory dict, never JSON). Without these
	// cases FingerprintToMap -> FingerprintFromMap silently dropped every integer
	// field and substituted the defaults: a 1536 viewport read back as 1280.
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
		if f, err := t.Float64(); err == nil {
			return int(f)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return def
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---------------------------------------------------------------------------
// DeviceFingerprint <-> map  (fingerprint_to_dict / fingerprint_from_dict)
// ---------------------------------------------------------------------------

func FingerprintToMap(fp *DeviceFingerprint) map[string]any {
	if fp == nil {
		return map[string]any{}
	}
	langs := make([]any, len(fp.Languages))
	for i, l := range fp.Languages {
		langs[i] = l
	}
	return map[string]any{
		"user_agent":           fp.UserAgent,
		"locale":               fp.Locale,
		"languages":            langs,
		"timezone":             fp.Timezone,
		"viewport_width":       fp.ViewportWidth,
		"viewport_height":      fp.ViewportHeight,
		"screen_width":         fp.ScreenWidth,
		"screen_height":        fp.ScreenHeight,
		"outer_width":          fp.OuterWidth,
		"outer_height":         fp.OuterHeight,
		"device_scale_factor":  fp.DeviceScaleFactor,
		"hardware_concurrency": fp.HardwareConcurrency,
		"device_memory":        fp.DeviceMemory,
		"platform":             fp.Platform,
		"vendor":               fp.Vendor,
		"max_touch_points":     fp.MaxTouchPoints,
	}
}

// FingerprintFromMap is fingerprint_from_dict. Python has exactly one decoder
// and every caller goes through it, so this is a thin alias for
// ParseStoredFingerprint rather than a second, laxer implementation.
//
// It used to be that second implementation, and it diverged in ways that only
// showed up on a corrupt state.json: it passed the default to mInt/mFloat, which
// fires only when the key is MISSING, so a stored 0 survived as 0 and was then
// clamped to 1 instead of taking Python's `or` default of 1280/720/8/1; and it
// rendered a numeric user_agent as "0" where Python's `str(x or "")` makes it
// empty and throws the whole fingerprint away.
func FingerprintFromMap(v map[string]any) *DeviceFingerprint {
	return ParseStoredFingerprint(v)
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// MailAccount <-> map  (account_to_dict / account_from_dict)
// ---------------------------------------------------------------------------

func AccountToMap(a MailAccount) map[string]any {
	raw := a.Raw
	if raw == "" && a.ClientID != "" && a.RefreshToken != "" {
		raw = strings.Join([]string{a.Email, a.Password, a.ClientID, a.RefreshToken}, "----")
	}
	return map[string]any{
		"email":               a.Email,
		"password":            a.Password,
		"client_id":           a.ClientID,
		"refresh_token":       a.RefreshToken,
		"raw":                 raw,
		"account_type":        a.AccountType,
		"status":              a.Status,
		"openai_rt":           a.OpenaiRT,
		"auth_phone_number":   a.AuthPhoneNumber,
		"auth_phone_sms_url":  a.AuthPhoneSMSURL,
		"receive_mailbox":     a.ReceiveMailbox,
		"mail_provider":       a.MailProvider,
		"group":               orDefault(a.Group, AccountDefaultGroup),
		"browser_fingerprint": FingerprintToMap(a.BrowserFingerprint),
	}
}

func AccountFromMap(v map[string]any) MailAccount {
	rawValue := mStr(v, "raw")
	if rawValue != "" {
		if account, err := ParseAccountLine(rawValue); err == nil {
			account.AccountType = firstNonEmpty(mStr(v, "account_type"), account.AccountType, "free")
			account.Status = firstNonEmpty(mStr(v, "status"), account.Status)
			account.OpenaiRT = firstNonEmpty(mStr(v, "openai_rt"), account.OpenaiRT)
			account.AuthPhoneNumber = firstNonEmpty(mStr(v, "auth_phone_number"), account.AuthPhoneNumber)
			account.AuthPhoneSMSURL = firstNonEmpty(mStr(v, "auth_phone_sms_url"), account.AuthPhoneSMSURL)
			account.ReceiveMailbox = NormalizeEmailAddress(firstNonEmpty(mStr(v, "receive_mailbox"), account.ReceiveMailbox))
			account.MailProvider = firstNonEmpty(mStr(v, "mail_provider"), account.MailProvider)
			account.Group = mStrOr(v, "group", AccountDefaultGroup)
			if fpm, ok := v["browser_fingerprint"].(map[string]any); ok {
				account.BrowserFingerprint = FingerprintFromMap(fpm)
			}
			return account
		}
	}
	email := NormalizeEmailAddress(strings.TrimSpace(mStr(v, "email")))
	password := mStr(v, "password")
	clientID := strings.TrimSpace(mStr(v, "client_id"))
	refreshToken := strings.TrimSpace(mStr(v, "refresh_token"))
	raw := ""
	if clientID != "" && refreshToken != "" {
		raw = rawValue
	}
	if raw == "" && email != "" && clientID != "" && refreshToken != "" {
		raw = strings.Join([]string{email, password, clientID, refreshToken}, "----")
	}
	acc := MailAccount{
		Email:           email,
		Password:        password,
		ClientID:        clientID,
		RefreshToken:    refreshToken,
		Raw:             raw,
		AccountType:     mStrOr(v, "account_type", "free"),
		Status:          mStr(v, "status"),
		OpenaiRT:        mStr(v, "openai_rt"),
		AuthPhoneNumber: mStr(v, "auth_phone_number"),
		AuthPhoneSMSURL: mStr(v, "auth_phone_sms_url"),
		ReceiveMailbox:  NormalizeEmailAddress(mStr(v, "receive_mailbox")),
		MailProvider:    mStr(v, "mail_provider"),
		Group:           mStrOr(v, "group", AccountDefaultGroup),
	}
	if fpm, ok := v["browser_fingerprint"].(map[string]any); ok {
		acc.BrowserFingerprint = FingerprintFromMap(fpm)
	}
	return acc
}

func firstNonEmpty(vals ...string) string {
	for _, s := range vals {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// PhoneEntry / PaymentCard <-> map
// ---------------------------------------------------------------------------

func PhoneToMap(p PhoneEntry) map[string]any {
	return map[string]any{
		"number": p.Number, "sms_url": p.SMSURL, "status": p.Status,
		"last_code": p.LastCode, "last_error": p.LastError, "receive_count": p.ReceiveCount,
	}
}

func PhoneFromMap(v map[string]any) PhoneEntry {
	return PhoneEntry{
		Number:       strings.TrimSpace(mStr(v, "number")),
		SMSURL:       strings.TrimSpace(mStr(v, "sms_url")),
		Status:       mStrOr(v, "status", "可用"),
		LastCode:     mStr(v, "last_code"),
		LastError:    mStr(v, "last_error"),
		ReceiveCount: maxi(0, mInt(v, "receive_count", 0)),
	}
}

func CardToMap(c PaymentCard) map[string]any {
	return map[string]any{
		"card": c.Card, "month": c.Month, "year": c.Year, "cvv": c.CVV, "status": c.Status,
	}
}

func CardFromMap(v map[string]any) PaymentCard {
	return PaymentCard{
		Card:   strings.TrimSpace(mStr(v, "card")),
		Month:  strings.TrimSpace(mStr(v, "month")),
		Year:   strings.TrimSpace(mStr(v, "year")),
		CVV:    strings.TrimSpace(mStr(v, "cvv")),
		Status: mStrOr(v, "status", "未用"),
	}
}

// ---------------------------------------------------------------------------
// Parsers
// ---------------------------------------------------------------------------

var (
	reEmail        = regexp.MustCompile("[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}")
	rePlusDigits   = regexp.MustCompile(`^\+\d+$`)
	reHTTPURLEnd   = regexp.MustCompile(`^https?://\S+$`)
	rePhoneURLLine = regexp.MustCompile(`^(\+\d+)\s*(https?://\S+)\s*$`)
	rePayPalLine   = regexp.MustCompile(`^([+\d][\d\s().-]*)\s*(https?://\S+)\s*$`)
	reInlinePhone  = regexp.MustCompile(`^([+\d][\d\s().-]*)(https?://\S+)$`)
	reLoosePhone   = regexp.MustCompile(`^[+\d][\d\s().-]{5,}$`)
	reCardNum      = regexp.MustCompile(`^\d{12,19}$`)
	reCardMonth    = regexp.MustCompile(`^\d{1,2}$`)
	reCardYear     = regexp.MustCompile(`^\d{4}$`)
	reCardCVV      = regexp.MustCompile(`^\d{3,4}$`)
	reTwoDigits    = regexp.MustCompile(`^\d{2}$`)
)

// NormalizeEmailAddress mirrors normalize_email_address.
func NormalizeEmailAddress(value string) string {
	text := strings.TrimSpace(value)
	text = strings.Trim(text, " \t\r\n\"'“”‘’<>()[]{}，,;；")
	if m := reEmail.FindString(text); m != "" {
		return m
	}
	return text
}

// ParseAccountLine mirrors parse_account_line.
func ParseAccountLine(line string) (MailAccount, error) {
	rawParts := strings.Split(strings.TrimSpace(line), "----")
	parts := make([]string, len(rawParts))
	for i, p := range rawParts {
		parts[i] = strings.TrimSpace(p)
	}
	if len(parts) < 4 {
		return MailAccount{}, errFormat("格式错误，应为 email----password----client_id----refresh_token")
	}
	email := NormalizeEmailAddress(parts[0])
	password, clientID, refreshToken := parts[1], parts[2], parts[3]
	extras := extractAccountExtras(parts[4:])
	if email == "" {
		return MailAccount{}, errFormat("email 不能为空")
	}
	if (clientID == "" || refreshToken == "") && extras["mail_provider"] != "cloudmail" {
		return MailAccount{}, errFormat("非 Cloud Mail 邮箱的 client_id / refresh_token 不能为空")
	}
	openaiRT := extras["openai_rt"]
	accountType := extras["account_type"]
	if accountType == "" {
		if openaiRT != "" {
			accountType = "plus"
		} else {
			accountType = "free"
		}
	}
	status := ""
	if openaiRT != "" {
		status = "已绑定手机号"
	} else if extras["auth_phone_number"] != "" && extras["auth_phone_sms_url"] != "" {
		status = "待获取RT"
	}
	return MailAccount{
		Email:           email,
		Password:        password,
		ClientID:        clientID,
		RefreshToken:    refreshToken,
		Raw:             strings.Join([]string{email, password, clientID, refreshToken}, "----"),
		AccountType:     accountType,
		Status:          status,
		OpenaiRT:        openaiRT,
		AuthPhoneNumber: extras["auth_phone_number"],
		AuthPhoneSMSURL: extras["auth_phone_sms_url"],
		ReceiveMailbox:  extras["receive_mailbox"],
		MailProvider:    extras["mail_provider"],
	}, nil
}

func extractAccountExtras(extraParts []string) map[string]string {
	result := map[string]string{
		"openai_rt": "", "auth_phone_number": "", "auth_phone_sms_url": "",
		"receive_mailbox": "", "mail_provider": "", "account_type": "",
	}
	val := func(part string) string {
		if i := strings.Index(part, "="); i >= 0 {
			return strings.TrimSpace(part[i+1:])
		}
		return ""
	}
	for _, rawPart := range extraParts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		switch {
		case hasAnyPrefix(lower, "rt_token=", "openai_rt="):
			result["openai_rt"] = val(part)
		case hasAnyPrefix(lower, "auth_phone=", "auth_phone_number=", "phone="):
			result["auth_phone_number"] = val(part)
		case hasAnyPrefix(lower, "auth_phone_sms_url=", "auth_sms_url=", "phone_sms_url=", "sms_url="):
			result["auth_phone_sms_url"] = val(part)
		case hasAnyPrefix(lower, "receive_mailbox=", "mailbox_email=", "receive_email=", "inbox="):
			result["receive_mailbox"] = NormalizeEmailAddress(val(part))
		case hasAnyPrefix(lower, "mail_provider=", "mail_type="):
			provider := strings.ToLower(val(part))
			if provider == "cloudmail" || provider == "outlook" {
				result["mail_provider"] = provider
			}
		case hasAnyPrefix(lower, "account_type=", "type="):
			at := strings.ToLower(val(part))
			if at == "free" || at == "plus" || at == "team" {
				result["account_type"] = at
			}
		default:
			if m := reInlinePhone.FindStringSubmatch(part); m != nil {
				if result["auth_phone_number"] == "" {
					result["auth_phone_number"] = strings.TrimSpace(m[1])
				}
				if result["auth_phone_sms_url"] == "" {
					result["auth_phone_sms_url"] = strings.TrimSpace(m[2])
				}
			} else if result["auth_phone_number"] == "" && reLoosePhone.MatchString(part) {
				result["auth_phone_number"] = part
			} else if result["auth_phone_sms_url"] == "" && reHTTPURLEnd.MatchString(part) {
				result["auth_phone_sms_url"] = part
			}
		}
	}
	return result
}

// ParsePhoneLine mirrors parse_phone_line.
func ParsePhoneLine(line string) (PhoneEntry, error) {
	text := strings.TrimSpace(line)
	if strings.Contains(text, "----") {
		rawParts := strings.Split(text, "----")
		parts := make([]string, len(rawParts))
		for i, p := range rawParts {
			parts[i] = strings.TrimSpace(p)
		}
		if len(parts) >= 2 && rePlusDigits.MatchString(parts[0]) && reHTTPURLEnd.MatchString(parts[1]) {
			return PhoneEntry{Number: parts[0], SMSURL: parts[1], Status: "可用"}, nil
		}
	}
	if m := rePhoneURLLine.FindStringSubmatch(text); m != nil {
		return PhoneEntry{Number: m[1], SMSURL: m[2], Status: "可用"}, nil
	}
	return PhoneEntry{}, errFormat("格式错误，应为 +手机号https://短信链接 或 +手机号----https://短信链接")
}

// ParsePayPalPhoneLine mirrors parse_paypal_phone_line.
func ParsePayPalPhoneLine(line string) (PhoneEntry, error) {
	text := strings.TrimSpace(line)
	if strings.Contains(text, "----") {
		number, smsURL, _ := strings.Cut(text, "----")
		number, smsURL = strings.TrimSpace(number), strings.TrimSpace(smsURL)
		if number != "" && reHTTPURLEnd.MatchString(smsURL) {
			return PhoneEntry{Number: number, SMSURL: smsURL, Status: "可用"}, nil
		}
	}
	if m := rePayPalLine.FindStringSubmatch(text); m != nil {
		return PhoneEntry{Number: strings.TrimSpace(m[1]), SMSURL: strings.TrimSpace(m[2]), Status: "可用"}, nil
	}
	return PhoneEntry{}, errFormat("格式错误，应为 手机号----https://接码链接")
}

// ParsePaymentCardLine mirrors parse_payment_card_line.
func ParsePaymentCardLine(line string) (PaymentCard, error) {
	rawParts := strings.Split(strings.TrimSpace(line), "|")
	parts := make([]string, len(rawParts))
	for i, p := range rawParts {
		parts[i] = strings.TrimSpace(p)
	}
	if len(parts) != 4 {
		return PaymentCard{}, errFormat("格式错误，应为 卡号|月|年|CVV")
	}
	card, month, year, cvv := parts[0], parts[1], parts[2], parts[3]
	if reTwoDigits.MatchString(year) {
		year = "20" + year
	}
	if !reCardNum.MatchString(card) || !reCardMonth.MatchString(month) || !reCardYear.MatchString(year) || !reCardCVV.MatchString(cvv) {
		return PaymentCard{}, errFormat("卡号/月/年/CVV 格式不正确")
	}
	if n, err := strconv.Atoi(month); err == nil {
		month = strconv.Itoa(n)
	}
	return PaymentCard{Card: card, Month: month, Year: year, CVV: cvv, Status: "未用"}, nil
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

type formatError string

func (e formatError) Error() string { return string(e) }
func errFormat(msg string) error    { return formatError(msg) }
