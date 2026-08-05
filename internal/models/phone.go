package models

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Python's `\D` on str is "not a Unicode DECIMAL digit" (category Nd); Go's RE2
// `\D` is the ASCII-only [^0-9]. The gap is not theoretical here: the number
// text can arrive from a rendered page or from a hand-typed 手动输入手机号 box,
// and Go would silently DELETE a fullwidth １２３ or an Arabic-Indic ٠١٢ that
// Python keeps — turning an 11-digit number into a short one and dialling the
// wrong thing. Keep them, exactly as Python does, so the length test below sees
// the same string.
var reNonDigit = regexp.MustCompile(`[^\p{Nd}]+`)

// Python's re.sub(r"\s+") on str is Unicode-aware; Go's RE2 \s is ASCII-only.
// ClassifyPhoneRejection reads rendered page text, which routinely contains
// NBSP — an ASCII-only collapse leaves a marker phrase unmatched, so a "number
// already used" banner degrades to the generic 手机号不可用 status and the pool
// marks the number differently.
//
// Go's own `\s` is [\t\n\f\r ]: no VT and none of the four information
// separators U+001C-U+001F, all five of which Python's `\s` matches. innerText
// on a Stripe/OpenAI error banner does carry them (they survive a copy/paste
// through a spreadsheet), and leaving one uncollapsed splits "already used"
// across the separator so the marker never matches.
var reWS = regexp.MustCompile(`[\s\x{000B}\x{001C}-\x{001F}\x{0085}\p{Z}]+`)

// pyStripCutset is exactly the 29 code points Python's str.strip() removes.
// unicode.IsSpace (hence strings.TrimSpace) covers 25 and omits the four
// information separators U+001C-U+001F.
//
// The collapse above already rewrote every whitespace run — edges included — to
// a single ASCII space, so in practice only ' ' is ever trimmed. The cutset is
// spelled in full anyway so the two halves of `re.sub(...).strip()` cannot drift
// apart if the collapse is ever narrowed.
const pyStripCutset = "\t\n\v\f\r\u001C\u001D\u001E\u001F\u0020\u0085\u00A0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200A\u2028\u2029\u202F\u205F\u3000"

// NormalizeUSPhoneForForm mirrors normalize_us_phone_for_form: strip to digits,
// and drop a leading US country code (11 digits starting with '1' -> 10 digits).
//
// The length test counts CODE POINTS, as Python's len() does — a fullwidth or
// Arabic-Indic digit is three bytes, so a byte count would never reach exactly
// 11 for them and the country-code strip would silently stop firing.
func NormalizeUSPhoneForForm(phoneNumber string) string {
	digits := reNonDigit.ReplaceAllString(phoneNumber, "")
	if utf8.RuneCountInString(digits) == 11 && strings.HasPrefix(digits, "1") {
		return digits[1:]
	}
	return digits
}

type phoneRejectionRule struct {
	status  string
	markers []string
}

// phoneRejectionRules mirrors classify_phone_rejection's ordered pattern table;
// first matching status wins.
var phoneRejectionRules = []phoneRejectionRule{
	{"手机号已使用", []string{
		"already used", "has been used", "already linked", "already associated",
		"maximum number of accounts", "too many accounts", "phone_number_already_used",
		"phone_number_in_use", "phone number is already in use",
		"手机号已被使用", "手机号已经使用", "号码已被使用", "号码已使用", "已绑定其他账户",
	}},
	{"手机号不支持", []string{
		"not supported", "unsupported phone", "unsupported country",
		"phone_number_not_supported", "voip", "virtual phone",
		"手机号不受支持", "不支持该手机号", "不支持此号码", "虚拟号码",
	}},
	{"手机号无效", []string{
		"invalid phone", "phone_number_invalid", "not a valid phone", "enter a valid phone",
		"手机号无效", "号码无效", "请输入有效的手机号",
	}},
	{"手机号频率限制", []string{
		"too many requests", "too many attempts", "rate limit", "rate_limit", "try again later",
		"请求过于频繁", "尝试次数过多", "稍后再试",
	}},
	{"手机号被拒绝", []string{
		"phone number is blocked", "phone_number_blocked", "cannot use this phone",
		"unable to use this phone", "手机号被拒绝", "号码被阻止", "无法使用此手机号",
	}},
}

// ClassifyPhoneRejection mirrors classify_phone_rejection: collapse whitespace,
// match markers case-insensitively, return (status, collapsed-text). status is ""
// when nothing matches.
func ClassifyPhoneRejection(message string) (status, text string) {
	text = strings.Trim(reWS.ReplaceAllString(message, " "), pyStripCutset)
	lowered := strings.ToLower(text)
	for _, rule := range phoneRejectionRules {
		for _, marker := range rule.markers {
			if strings.Contains(lowered, marker) {
				return rule.status, text
			}
		}
	}
	return "", text
}

// OpenAIPhoneErrorReasons mirrors OPENAI_PHONE_ERROR_REASONS.
var OpenAIPhoneErrorReasons = map[string]string{
	"phone_number_in_use":        "手机号已被 OpenAI 使用/绑定过，不能再用于此账号",
	"phone_number_already_used":  "手机号已被 OpenAI 使用/绑定过，不能再用于此账号",
	"phone_number_not_supported": "OpenAI 不支持这个手机号或国家/号码类型",
	"phone_number_invalid":       "手机号格式或号码本身无效",
	"phone_number_blocked":       "手机号被 OpenAI 风控拦截",
	"rate_limit_exceeded":        "请求过于频繁，需稍后重试",
}

// OpenAIPhoneErrorReason mirrors openai_phone_error_reason.
func OpenAIPhoneErrorReason(code string) string {
	return OpenAIPhoneErrorReasons[strings.ToLower(strings.TrimSpace(code))]
}
