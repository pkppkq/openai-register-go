// Package worker ports OpenAIRegisterPayLinkWorker (app.py:8846-12298) to Go.
//
// This file is the phone/SMS verification cluster (app.py:10209-10542).
package worker

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
)

// PhoneProvider mirrors the Python `self.phone_provider(action, email, payload)`
// callable (implementation: app.py:16535 `_phone_provider`). The five actions are
// split into methods so the cluster stays decoupled from SMSBower / the phone pool.
//
// Ordering is money-critical and enforced by HandlePhoneContinueIfVisible:
//   - Next  — reserve the next usable number ("next"). An empty map means the pool
//     is exhausted; an error aborts the flow (Python called it OUTSIDE the try).
//   - Sent  — fires ONLY after OpenAI actually showed the SMS-code form ("sent",
//     SMSBower status 1). Firing it earlier bills a number that never got an SMS.
//   - Code  — blocks for the inbound code ("code").
//   - Good  — fires ONLY after the code was submitted and accepted ("good",
//     SMSBower status 6 = finish).
//   - Bad   — fires ONLY for POST-submit failures ("bad"); the payload carries the
//     extra "error" and "status" keys the pool uses to mark/burn the number.
type PhoneProvider interface {
	// Next reserves the next candidate number. opts carries {"country": "US"}.
	Next(email string, opts map[string]string) (map[string]string, error)
	// Sent marks the activation as "SMS requested" (SMSBower status 1).
	Sent(email string, phone map[string]string) error
	// Code waits for and returns the received verification code.
	Code(email string, phone map[string]string) (string, error)
	// Good marks the activation complete (SMSBower status 6).
	Good(email string, phone map[string]string) error
	// Bad reports a post-submit failure; phone contains "error" and "status".
	Bad(email string, phone map[string]string) error
}

// phoneMaxAttempts mirrors `for _ in range(30)` in _handle_phone_continue_if_visible
// (app.py:10226) — at most 30 numbers are burned before giving up.
const phoneMaxAttempts = 30

// PhoneHandler owns the phone-verification cluster of
// OpenAIRegisterPayLinkWorker (app.py:10209-10542). One instance per
// registration attempt; it mutates ActiveRegisterPhone on success.
type PhoneHandler struct {
	page     *browser.Page
	browser  *browser.Browser
	provider PhoneProvider
	account  *models.MailAccount
	log      func(string)

	// ActiveRegisterPhone mirrors `self.active_register_phone` (app.py:10253):
	// a copy of the phone map that passed verification. nil until success.
	ActiveRegisterPhone map[string]string

	// HasAboutYouForm is an optional hook for _has_about_you_form
	// (app.py:10991), which lives in the about-you cluster. When nil, the
	// built-in equivalent below is used. Only WaitAfterRegisterPhoneCodeSubmit
	// consults it.
	HasAboutYouForm func() bool
}

// NewPhoneHandler builds a PhoneHandler bound to one page/browser/account. It
// mirrors the subset of OpenAIRegisterPayLinkWorker.__init__ (app.py:8847-8856)
// the phone cluster reads. provider may be nil, which makes
// HandlePhoneContinueIfVisible a no-op exactly like `if not self.phone_provider`
// (app.py:10210).
func NewPhoneHandler(page *browser.Page, br *browser.Browser, provider PhoneProvider, account *models.MailAccount, log func(string)) *PhoneHandler {
	return &PhoneHandler{page: page, browser: br, provider: provider, account: account, log: log}
}

func (h *PhoneHandler) logf(format string, args ...any) {
	if h.log == nil {
		return
	}
	if len(args) == 0 {
		h.log(format)
		return
	}
	h.log(fmt.Sprintf(format, args...))
}

func (h *PhoneHandler) email() string {
	if h.account == nil {
		return ""
	}
	return h.account.Email
}

// ---------------------------------------------------------------------------
// _handle_phone_continue_if_visible (app.py:10209-10276)
// ---------------------------------------------------------------------------

// HandlePhoneContinueIfVisible mirrors _handle_phone_continue_if_visible
// (app.py:10209-10276): when OpenAI demands phone verification, rotate through
// the phone pool (max 30) until one number passes pre-validation AND the SMS
// code is accepted.
//
// Returns (false, nil) when there is no phone provider or no phone form on the
// page — i.e. "nothing to do", not a failure.
//
// The provider-callback ordering is deliberate and must not be reordered:
// Sent only after the code form appears, Good only after the code submission
// succeeded, Bad only for POST-submit failures. A PRE-submit failure aborts the
// whole flow (Python re-raised), a POST-submit failure rotates to the next number.
func (h *PhoneHandler) HandlePhoneContinueIfVisible() (bool, error) {
	if h.provider == nil {
		return false, nil
	}
	currentURL := ""
	if h.page != nil {
		currentURL = h.page.URL()
	}
	requiredRoute := strings.Contains(currentURL, "add-phone") || strings.Contains(currentURL, "phone-verification")
	hasPhoneForm := h.HasRegisterPhoneNumberForm()
	if requiredRoute && !hasPhoneForm {
		if h.ClickUsePhoneNumberContinue() {
			time.Sleep(1 * time.Second)
			hasPhoneForm = h.HasRegisterPhoneNumberForm()
		}
	}
	if !hasPhoneForm {
		return false, nil
	}

	h.logf("[手机] 服务要求电话验证，开始使用手机号池")
	lastError := ""
	for i := 0; i < phoneMaxAttempts; i++ {
		// Python called phone_provider("next", ...) OUTSIDE the try block, so a
		// provider error here propagates immediately instead of rotating.
		phone, err := h.provider.Next(h.email(), map[string]string{"country": "US"})
		if err != nil {
			return false, err
		}
		if len(phone) == 0 {
			detail := ""
			if lastError != "" {
				detail = "，最后错误: " + lastError
			}
			return false, fmt.Errorf("手机号池没有可用的美国 +1 手机号，无法继续电话验证%s", detail)
		}
		phoneNumber := pyStrip(phone["number"])
		localNumber := models.NormalizeUSPhoneForForm(phoneNumber)

		// numberSubmitted mirrors Python's `number_submitted` flag: it flips to
		// true the moment the number has been POSTed, which is what decides
		// abort-vs-rotate below.
		numberSubmitted := false

		attemptErr := func() error {
			if !strings.HasPrefix(phoneNumber, "+1") || phoneLocalTooShort(localNumber) {
				return errors.New("当前电话验证流程要求美国 +1 手机号")
			}
			if !h.SelectUSPhoneCountry() {
				return errors.New("未能将手机号国家切换为美国 +1")
			}
			h.logf("[手机] 填写并预验证美国电话手机号: %s", phoneNumber)
			if err := h.FillRegisterPhoneNumber(phoneLastTenRunes(localNumber)); err != nil {
				return err
			}
			if !h.clickContinue() {
				return errors.New("手机号已填写，但未找到继续按钮")
			}
			numberSubmitted = true
			if err := h.WaitForRegisterPhoneCodeForm(45 * time.Second); err != nil {
				return err
			}
			if err := h.provider.Sent(h.email(), phone); err != nil {
				return err
			}
			h.logf("[手机] 手机号预验证通过，OpenAI 已进入短信验证码步骤: %s", phoneNumber)
			code, err := h.provider.Code(h.email(), phone)
			if err != nil {
				return err
			}
			if code == "" {
				return errors.New("短信链接未读取到验证码")
			}
			h.logf("[手机] 读取到电话验证码: %s", code)
			if err := h.SubmitRegisterPhoneCode(code); err != nil {
				return err
			}
			if err := h.provider.Good(h.email(), phone); err != nil {
				return err
			}
			h.ActiveRegisterPhone = phoneCopyMap(phone)
			h.logf("[手机] 已提交电话验证码，继续认证流程")
			time.Sleep(3 * time.Second)
			return nil
		}()
		if attemptErr == nil {
			return true, nil
		}

		lastError = attemptErr.Error()
		if !numberSubmitted {
			// Python: `raise RuntimeError(last_error)` — the original exception
			// type is intentionally dropped, so a pre-submit failure surfaces as
			// a plain error and aborts the entire registration.
			return false, errors.New(lastError)
		}

		// Rejection-classification cascade, in Python's exact order.
		rejectionStatus, _ := models.ClassifyPhoneRejection(lastError)
		if rejectionStatus == "" {
			// "接码网络抖动" means the SMS *network* wobbled, not the number —
			// the pool must NOT burn the number for this status.
			if smsbower.IsTransientError(attemptErr) || strings.Contains(strings.ToLower(lastError), "smsbower 请求失败") {
				rejectionStatus = "接码网络抖动"
			} else {
				rejectionStatus = models.ExceptionStatus(attemptErr, "手机号不可用")
			}
		}
		badPayload := phoneCopyMap(phone)
		badPayload["error"] = lastError
		badPayload["status"] = rejectionStatus
		// Python ignored the return of phone_provider("bad", ...); an error here
		// must not abort the rotation.
		_ = h.provider.Bad(h.email(), badPayload)
		h.logf("[手机] 手机号预验证/接码失败 [%s]，切换下一个: %s %s", rejectionStatus, phoneNumber, lastError)
		if !h.ResetPhoneRegistrationForNextNumber() {
			return false, fmt.Errorf("手机号验证失败且无法回到号码输入页: %s", lastError)
		}
		time.Sleep(1 * time.Second)
	}
	if lastError == "" {
		lastError = "unknown"
	}
	return false, fmt.Errorf("手机号验证失败次数过多: %s", lastError)
}

// phoneLocalTooShort is `len(local_number) < 10` (app.py:10235) and
// phoneLastTenRunes is `local_number[-10:]` (app.py:10240). BOTH count CODE
// POINTS, and both must, because normalize_us_phone_for_form keeps every
// Unicode decimal digit (`re.sub(r"\D+", ...)`, app.py:1941 — Python's `\D` is
// [^\p{Nd}]), so the string it returns is not necessarily ASCII.
//
// Counting bytes broke this twice over: a 5-digit Devanagari number is 13 bytes
// and sailed past the >= 10 guard Python rejects, and the byte slice then cut
// the last 10 BYTES out of a run of 3-byte digits, feeding a mid-rune fragment
// into the phone form. Either way the number is POSTed, number_submitted flips
// true, and the rotate branch burns the rented number and rents another.
func phoneLocalTooShort(local string) bool {
	return utf8.RuneCountInString(local) < 10
}

func phoneLastTenRunes(local string) string {
	r := []rune(local)
	if len(r) <= 10 {
		return local
	}
	return string(r[len(r)-10:])
}

// phoneClickedLabel is `str(box.get('text', ”)).strip()[:40]` (app.py:11186):
// str.strip() (not TrimSpace — it leaves U+001C..U+001F) then 40 CODE POINTS.
func phoneClickedLabel(label string) string {
	return phoneTruncateRunes(pyStrip(label), 40)
}

func phoneCopyMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+2)
	for k, v := range src {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// _has_register_phone_number_form (app.py:10278-10308)
// ---------------------------------------------------------------------------

// phoneNumberFormSelectors mirrors the selector list in
// _has_register_phone_number_form (app.py:10279-10288). Order is a priority
// ladder — do not sort.
var phoneNumberFormSelectors = []string{
	`input[type="tel"]`,
	`input[inputmode="tel"]`,
	`input[name*="phone" i]`,
	`input[autocomplete*="tel" i]`,
	`input[aria-label*="phone" i]`,
	`input[aria-label*="手机" i]`,
	`input[placeholder*="phone" i]`,
	`input[placeholder*="手机" i]`,
}

// phoneInputMetaJS is the per-element probe from _has_register_phone_number_form
// (app.py:10294-10299), rewritten as a `function` so go-rod binds `this` to the
// element (an arrow function would capture the wrong `this`).
const phoneInputMetaJS = `function(){
    const el = this;
    const meta = [el.type, el.inputMode, el.name, el.id, el.placeholder, el.autocomplete, el.getAttribute('aria-label')]
        .join(' ')
        .toLowerCase();
    return /phone|tel|手机|手機|電話|\+1|\+81/.test(meta);
}`

// rePhoneFormBodyText mirrors the body-text fallback regex at app.py:10308.
var rePhoneFormBodyText = regexp.MustCompile(`(?i)country|国家|國家|日本|美国|美國|United States`)

// HasRegisterPhoneNumberForm mirrors _has_register_phone_number_form
// (app.py:10278-10308): a visible phone-ish input plus, as a fallback, a body
// text that mentions a country selector. Keeps the multilingual matchers verbatim.
func (h *PhoneHandler) HasRegisterPhoneNumberForm() bool {
	if h.page == nil {
		return false
	}
	inputs := h.page.VisibleInputs(phoneNumberFormSelectors)
	if len(inputs) == 0 {
		return false
	}
	for _, in := range inputs {
		v, err := in.Eval(phoneInputMetaJS)
		if err != nil || v == nil {
			// Python: `except Exception: pass` — keep probing the other inputs.
			continue
		}
		if v.Value.Bool() {
			return true
		}
	}
	text, _ := h.bodyInnerText(1000 * time.Millisecond)
	return rePhoneFormBodyText.MatchString(text)
}

// ---------------------------------------------------------------------------
// _click_use_phone_number_continue (app.py:10310-10335)
// ---------------------------------------------------------------------------

// phoneClickUsePhoneNumberContinueJS is app.py:10313-10332 verbatim.
const phoneClickUsePhoneNumberContinueJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
    const candidates = Array.from(document.querySelectorAll('button, a, [role="button"]')).filter(el => visible(el) && enabled(el));
    const target = candidates.find(el => {
        const text = ` + "`${el.textContent || ''} ${el.getAttribute('aria-label') || ''}`" + `.replace(/\s+/g, ' ').trim();
        const hasPhone = /使用电话号码|使用電話號碼|電話番号|phone number/i.test(text);
        const hasContinue = /继续|繼續|続行|continue/i.test(text);
        return hasPhone && hasContinue;
    });
    if (!target) return false;
    target.scrollIntoView({ block: 'center', inline: 'center' });
    target.click();
    return true;
}`

// ClickUsePhoneNumberContinue mirrors _click_use_phone_number_continue
// (app.py:10310-10335): on the "verify with email or phone" chooser, click the
// button whose label contains BOTH a phone-number phrase and a continue phrase
// (EN/ZH-Hans/ZH-Hant/JA). Any failure yields false, as in Python.
func (h *PhoneHandler) ClickUsePhoneNumberContinue() bool {
	if h.page == nil {
		return false
	}
	v, err := h.page.Rod.Eval(phoneClickUsePhoneNumberContinueJS)
	if err != nil || v == nil {
		return false
	}
	return v.Value.Bool()
}

// ---------------------------------------------------------------------------
// _select_us_phone_country (app.py:10337-10420)
// ---------------------------------------------------------------------------

// phoneSelectUSCountryJS is app.py:10339-10384 verbatim. It returns 'select'
// when a native <select> was switched, 'opened' when a custom dropdown was
// opened (a second pass then picks the option), or ” on no match.
//
// NOTE the US-territory exclusion: American Samoa / Guam / U.S. Virgin Islands /
// Northern Mariana Islands / Puerto Rico all share +1 but are NOT the US for
// OpenAI's purposes, so they are filtered out before the positive match.
// This <select> phase has NO flag-emoji tolerance (see the option phase below).
const phoneSelectUSCountryJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const setNativeValue = (el, value) => {
        const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
        const desc = Object.getOwnPropertyDescriptor(proto, 'value');
        if (desc && desc.set) desc.set.call(el, value); else el.value = value;
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    const isUnitedStates = text => {
        const value = String(text || '').replace(/\s+/g, ' ').trim();
        if (/美属|美屬|萨摩亚|薩摩亞|维尔京|維爾京|关岛|關島|波多黎各/.test(value)) return false;
        if (/Samoa|Virgin|Guam|Mariana|Puerto Rico/i.test(value)) return false;
        return /(^|\s)美国\s*(\(\+?1\)|\+?1)?$/i.test(value)
            || /(^|\s)美國\s*(\(\+?1\)|\+?1)?$/i.test(value)
            || /United States\s*(\(\+?1\)|\+?1)?/i.test(value);
    };

    for (const select of Array.from(document.querySelectorAll('select')).filter(visible)) {
        const matched = Array.from(select.options || []).find(opt => {
            const value = String(opt.value || '').trim().toUpperCase();
            return value === 'US' || isUnitedStates(opt.textContent || '');
        });
        if (matched) {
            setNativeValue(select, matched.value);
            return 'select';
        }
    }

    const buttons = Array.from(document.querySelectorAll('button, [role="button"], [role="combobox"], [aria-haspopup]')).filter(visible);
    const current = buttons.find(el => {
        const text = ` + "`${el.textContent || ''} ${el.getAttribute('aria-label') || ''}`" + `.replace(/\s+/g, ' ');
        return /\+81|日本|Japan|country|region|国家|國家/i.test(text);
    });
    if (current) {
        current.scrollIntoView({ block: 'center', inline: 'center' });
        current.click();
        return 'opened';
    }
    return '';
}`

// phoneSelectUSOptionJS is app.py:10391-10414 verbatim — the open-dropdown pass.
//
// The isUnitedStates matcher here differs from the <select> one on purpose: the
// Chinese branches are anchored with ^ and tolerate a leading 🇺🇸 flag emoji
// (`^(?:🇺🇸\s*)?美国...`) because rendered dropdown rows carry the flag, whereas
// <option> text does not. Keep both variants.
const phoneSelectUSOptionJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const isUnitedStates = text => {
        const value = String(text || '').replace(/\s+/g, ' ').trim();
        if (/美属|美屬|萨摩亚|薩摩亞|维尔京|維爾京|关岛|關島|波多黎各/.test(value)) return false;
        if (/Samoa|Virgin|Guam|Mariana|Puerto Rico/i.test(value)) return false;
        return /^(?:🇺🇸\s*)?美国\s*(\(\+?1\)|\+?1)?$/i.test(value)
            || /^(?:🇺🇸\s*)?美國\s*(\(\+?1\)|\+?1)?$/i.test(value)
            || /United States\s*(\(\+?1\)|\+?1)?/i.test(value);
    };
    const options = Array.from(document.querySelectorAll('[role="option"], [role="menuitem"], li, button, div'))
        .filter(visible)
        .filter(el => isUnitedStates(el.textContent || el.getAttribute('aria-label') || ''));
    const target = options[0];
    if (!target) return false;
    target.scrollIntoView({ block: 'center', inline: 'center' });
    target.click();
    return true;
}`

// SelectUSPhoneCountry mirrors _select_us_phone_country (app.py:10337-10420):
// switch the phone country picker to United States (+1), handling both a native
// <select> and a custom dropdown (open, wait 0.8s, click the US row, wait 0.5s).
// Logs "未能确认手机号国家已切换为美国" and returns false when neither worked.
func (h *PhoneHandler) SelectUSPhoneCountry() bool {
	if h.page == nil {
		return false
	}
	result := ""
	// Python let a page.evaluate failure propagate here (it is inside the caller's
	// try); in Go an eval error is reported as "not switched", which the caller
	// turns into the same abort via "未能将手机号国家切换为美国 +1".
	if v, err := h.page.Rod.Eval(phoneSelectUSCountryJS); err == nil && v != nil {
		result = v.Value.Str()
	}
	if result == "select" {
		return true
	}
	if result == "opened" {
		time.Sleep(800 * time.Millisecond)
		selected := false
		if v, err := h.page.Rod.Eval(phoneSelectUSOptionJS); err == nil && v != nil {
			selected = v.Value.Bool()
		}
		if selected {
			time.Sleep(500 * time.Millisecond)
			return true
		}
	}
	h.logf("未能确认手机号国家已切换为美国")
	return false
}

// ---------------------------------------------------------------------------
// _fill_register_phone_number (app.py:10422-10438)
// ---------------------------------------------------------------------------

// phoneFillSelectors mirrors the selector ladder at app.py:10423-10433. It is
// the phone-form ladder plus 'input[inputmode="numeric"]' in third position.
var phoneFillSelectors = []string{
	`input[type="tel"]`,
	`input[inputmode="tel"]`,
	`input[inputmode="numeric"]`,
	`input[name*="phone" i]`,
	`input[autocomplete*="tel" i]`,
	`input[aria-label*="phone" i]`,
	`input[aria-label*="手机" i]`,
	`input[placeholder*="phone" i]`,
	`input[placeholder*="手机" i]`,
}

// FillRegisterPhoneNumber mirrors _fill_register_phone_number
// (app.py:10422-10438): fill the first visible phone input with the 10-digit
// local number using the React-safe native-setter path (Page.ForceFill).
func (h *PhoneHandler) FillRegisterPhoneNumber(localNumber string) error {
	if h.page == nil {
		return errors.New("未找到手机号输入框")
	}
	inputs := h.page.VisibleInputs(phoneFillSelectors)
	if len(inputs) == 0 {
		return errors.New("未找到手机号输入框")
	}
	if !h.page.ForceFill(inputs[0], localNumber) {
		return errors.New("手机号输入框填写失败")
	}
	return nil
}

// ---------------------------------------------------------------------------
// _looks_like_register_phone_code_page (app.py:10440-10449)
// ---------------------------------------------------------------------------

var (
	// The trailing alternative is Python's `\+\d` (app.py:10446). RE2's `\d` is
	// [0-9]; Python's is \p{Nd}, so an ASCII spelling misses a rendered "+１" /
	// "+١" dial-code and _has_otp_input then treats the SMS-code screen as an
	// EMAIL-OTP screen — the wrong code gets typed and the rented number burns.
	rePhoneCodeHasPhone = regexp.MustCompile(`(?i)短信|SMS|text message|手机号|手機|電話|phone number|\+` + pyDigit)
	rePhoneCodeHasCode  = regexp.MustCompile(`(?i)验证码|驗證碼|コード|code|6[- ]?digit|verification`)
	rePhoneCodeHasEmail = regexp.MustCompile(`(?i)email|邮件|郵件|邮箱|電子メール`)
	rePhoneCodeSMSWord  = regexp.MustCompile(`(?i)短信|SMS|text message|phone`)
)

// LooksLikeRegisterPhoneCodePage mirrors _looks_like_register_phone_code_page
// (app.py:10440-10449): the body reads like an SMS-code step (phone marker AND
// code marker) and is not an email-only OTP page. All multilingual markers kept
// verbatim.
func (h *PhoneHandler) LooksLikeRegisterPhoneCodePage() bool {
	text, _ := h.bodyInnerText(1000 * time.Millisecond)
	return phoneLooksLikeCodePageText(text)
}

// phoneLooksLikeCodePageText is the pure predicate of
// _looks_like_register_phone_code_page (app.py:10444-10449), split out of the
// page read so it can be differentially tested against the Python source.
func phoneLooksLikeCodePageText(text string) bool {
	normalized := pyWhitespaceRun.ReplaceAllString(text, " ")
	hasPhone := rePhoneCodeHasPhone.MatchString(normalized)
	hasCode := rePhoneCodeHasCode.MatchString(normalized)
	hasEmailOnly := rePhoneCodeHasEmail.MatchString(normalized)
	return hasPhone && hasCode && !(hasEmailOnly && !rePhoneCodeSMSWord.MatchString(normalized))
}

// ---------------------------------------------------------------------------
// _register_phone_code_inputs (app.py:10451-10483)
// ---------------------------------------------------------------------------

// phoneCodeStrictSelectors mirrors app.py:10452-10459 (priority order).
var phoneCodeStrictSelectors = []string{
	`input[autocomplete="one-time-code"]`,
	`input[name="code"]`,
	`input[aria-label*="code" i]`,
	`input[placeholder*="code" i]`,
	`input[aria-label*="验证码" i]`,
	`input[placeholder*="验证码" i]`,
}

// phoneCodeInputMetaJS is app.py:10470-10477, rewritten as a `function` so
// go-rod binds `this` to the element. It rejects phone-ish inputs and keeps only
// short (maxLength 1..8) numeric boxes.
const phoneCodeInputMetaJS = `function(){
    const el = this;
    const meta = [el.type, el.inputMode, el.name, el.id, el.placeholder, el.autocomplete, el.getAttribute('aria-label')]
        .join(' ')
        .toLowerCase();
    const maxLength = Number(el.maxLength || 0);
    if (/phone|tel|手机|手機|電話|\+1|\+81/.test(meta)) return false;
    return maxLength > 0 && maxLength <= 8;
}`

// RegisterPhoneCodeInputs mirrors _register_phone_code_inputs
// (app.py:10451-10483): strict OTP selectors first; otherwise, only if the page
// looks like an SMS-code page, fall back to numeric boxes (>=6 of them means a
// split OTP widget, else filter by the short-maxlength probe).
func (h *PhoneHandler) RegisterPhoneCodeInputs() []*rod.Element {
	if h.page == nil {
		return nil
	}
	if strict := h.page.VisibleInputs(phoneCodeStrictSelectors); len(strict) > 0 {
		return strict
	}
	if !h.LooksLikeRegisterPhoneCodePage() {
		return nil
	}
	numeric := h.page.VisibleInputs([]string{`input[inputmode="numeric"]`})
	if len(numeric) >= 6 {
		return numeric
	}
	var codeInputs []*rod.Element
	for _, in := range numeric {
		v, err := in.Eval(phoneCodeInputMetaJS)
		if err != nil || v == nil {
			// Python: `except Exception: pass` — skip this input, keep going.
			continue
		}
		if v.Value.Bool() {
			codeInputs = append(codeInputs, in)
		}
	}
	return codeInputs
}

// ---------------------------------------------------------------------------
// _wait_after_register_phone_code_submit (app.py:10485-10497)
// ---------------------------------------------------------------------------

// WaitAfterRegisterPhoneCodeSubmit mirrors
// _wait_after_register_phone_code_submit (app.py:10485-10497): poll for 30s at
// 1s cadence until we left the SMS step. ANY of these counts as done — a live
// ChatGPT session, the about-you route/form, the password step, or simply the
// code inputs disappearing. Still on the SMS page after the timeout is an error.
func (h *PhoneHandler) WaitAfterRegisterPhoneCodeSubmit(timeout time.Duration) error {
	started := time.Now()
	for time.Since(started) < timeout {
		if h.browser != nil && h.browser.HasChatGPTSession() {
			return nil
		}
		url := ""
		if h.page != nil {
			url = h.page.URL()
		}
		if strings.Contains(url, "about-you") || h.hasAboutYouForm() {
			return nil
		}
		if strings.Contains(url, "password") && h.hasVisiblePassword() {
			return nil
		}
		if len(h.RegisterPhoneCodeInputs()) == 0 {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("手机验证码提交后仍停留在短信验证页: %s", h.pageTextSummary(300))
}

// ---------------------------------------------------------------------------
// _wait_for_register_phone_code_form (app.py:10499-10514)
// ---------------------------------------------------------------------------

// WaitForRegisterPhoneCodeForm mirrors _wait_for_register_phone_code_form
// (app.py:10499-10514): after POSTing the number, poll for 45s at 1s cadence
// until the SMS-code form appears.
//
// Two early exits matter: a ChatGPT session means OpenAI auto-advanced and the
// code box never appears, and a classified rejection in the page text raises a
// PhoneRejectedError carrying that status so the caller burns the right number.
// This is the gate that must pass BEFORE PhoneProvider.Sent is called.
func (h *PhoneHandler) WaitForRegisterPhoneCodeForm(timeout time.Duration) error {
	started := time.Now()
	for time.Since(started) < timeout {
		if h.browser != nil && h.browser.HasChatGPTSession() {
			return nil
		}
		if len(h.RegisterPhoneCodeInputs()) > 0 {
			return nil
		}
		pageSummary := h.pageTextSummary(700)
		rejectionStatus, detail := models.ClassifyPhoneRejection(pageSummary)
		if rejectionStatus != "" {
			return &models.PhoneRejectedError{
				Msg:    fmt.Sprintf("%s: %s", rejectionStatus, detail),
				Status: rejectionStatus,
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("提交手机号后未进入短信验证码页: %s", h.pageTextSummary(300))
}

// ---------------------------------------------------------------------------
// _submit_register_phone_code (app.py:10516-10528)
// ---------------------------------------------------------------------------

// SubmitRegisterPhoneCode mirrors _submit_register_phone_code
// (app.py:10516-10528): type the code (one char per box when the widget is split
// into >=6 boxes, otherwise the whole code into the first box), press continue,
// then wait for the SMS step to clear.
//
// Missing code inputs are NOT fatal when a ChatGPT session already exists —
// OpenAI can auto-advance without ever showing a code box.
func (h *PhoneHandler) SubmitRegisterPhoneCode(code string) error {
	inputs := h.RegisterPhoneCodeInputs()
	if len(inputs) == 0 {
		if h.browser != nil && h.browser.HasChatGPTSession() {
			return nil
		}
		return errors.New("页面未找到手机验证码输入框")
	}
	// Python uses plain locator.fill() here (app.py:10524/10526), NOT the
	// _force_fill_locator it uses for the phone-NUMBER field. That distinction is
	// load-bearing for the split 6-box widget: each box advances focus on `input`,
	// and ForceFill's extra change/blur dispatch makes it drop characters. A
	// garbled code wastes an SMS that already arrived — the pool then reports the
	// number Bad and rents another. Errors propagate because .fill() has no
	// try/except on the Python side either.
	if len(inputs) >= 6 {
		runes := []rune(code)
		if len(runes) > 6 {
			runes = runes[:6]
		}
		for index, char := range runes {
			if err := otpPlainFill(inputs[index], string(char)); err != nil {
				return err
			}
		}
	} else if err := otpPlainFill(inputs[0], code); err != nil {
		return err
	}
	h.clickContinue()
	return h.WaitAfterRegisterPhoneCodeSubmit(30 * time.Second)
}

// ---------------------------------------------------------------------------
// _reset_phone_registration_for_next_number (app.py:10530-10541)
// ---------------------------------------------------------------------------

// phoneResetButtonTexts mirrors app.py:10533 verbatim (EN + ZH + JA).
var phoneResetButtonTexts = []string{"Change phone", "Edit", "Back", "更改", "编辑", "返回", "戻る"}

// ResetPhoneRegistrationForNextNumber mirrors
// _reset_phone_registration_for_next_number (app.py:10530-10541): get back to
// the phone-number input so the next pool number can be tried — already there,
// or via a change/edit/back button, or finally via browser history back.
func (h *PhoneHandler) ResetPhoneRegistrationForNextNumber() bool {
	if h.HasRegisterPhoneNumberForm() {
		return true
	}
	if h.page != nil {
		if clicked, label := h.page.ClickButtonByText(phoneResetButtonTexts); clicked {
			h.logf("已点击按钮: %s", phoneClickedLabel(label))
			time.Sleep(1 * time.Second)
			return h.HasRegisterPhoneNumberForm()
		}
	}
	if h.page == nil {
		return false
	}
	// Python: page.go_back(wait_until="domcontentloaded", timeout=15000).
	if err := h.page.Rod.NavigateBack(); err != nil {
		return false
	}
	if err := h.page.WaitDOMContentLoaded(15 * time.Second); err != nil {
		return false
	}
	time.Sleep(1 * time.Second)
	return h.HasRegisterPhoneNumberForm()
}

// ---------------------------------------------------------------------------
// Cross-cluster helpers pulled in from outside 10209-10542.
// They are unexported methods on PhoneHandler so the other worker clusters can
// define their own copies without symbol collisions.
// ---------------------------------------------------------------------------

// phoneClickContinueLadderJS reimplements _click_continue's selector ladder
// (app.py:10118-10142). go-rod has no ':has-text()', so each text step becomes a
// textContent substring match (Playwright's :has-text() is case-insensitive and
// matches descendants — hence toLowerCase + textContent).
//
// ORDER IS A PRIORITY LADDER and must be preserved. Playwright used
// locator(sel).first, i.e. only the FIRST DOM match of a step is considered; if
// that one is invisible the step is skipped rather than falling through to the
// second match — replicated here.
const phoneClickContinueLadderJS = `() => {
    const visible = (el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const ladder = [
        ['button', 'Finish creating account'],
        ['button', 'Finalizar la creación de la cuenta'],
        ['button', 'Finalizar la creacion de la cuenta'],
        ['button[data-dd-action-name="Continue"][type="submit"]', null],
        ['button', 'Continue'],
        ['button', 'Continuar'],
        ['button', 'アカウントの作成を完了する'],
        ['button', '作成を完了'],
        ['button', '继续'],
        ['button', '完成帐户创建'],
        ['button', '完成账户创建'],
        ['button', 'Next'],
        ['button', '下一步'],
        ['button', 'Create'],
        ['button', '完成'],
        ['button[type="submit"]', null],
        ['[role="button"]', 'Finish creating account'],
        ['[role="button"]', 'Finalizar la creación de la cuenta'],
        ['[role="button"]', 'Finalizar la creacion de la cuenta'],
        ['[role="button"]', 'Continue'],
        ['[role="button"]', 'Continuar'],
        ['[role="button"]', 'アカウントの作成を完了する'],
        ['[role="button"]', '作成を完了']
    ];
    // Playwright's click() auto-waits for the element to become ENABLED and then
    // times out, so Python falls through to the next rung on a disabled button
    // (and _click_submit_button_by_dom rejects disabled too). Without this check
    // a disabled Continue counts as a successful click, HandlePhoneContinueIfVisible
    // sets numberSubmitted with nothing ever POSTed, and the rotate branch burns
    // up to 30 rented numbers instead of aborting.
    const enabled = (el) => !el.disabled && el.getAttribute('aria-disabled') !== 'true';
    // :has-text() normalizes whitespace and case on BOTH sides, over rendered text.
    const norm = (s) => (s || '').replace(/\s+/g, ' ').trim().toLowerCase();
    for (const [css, text] of ladder) {
        let nodes;
        try { nodes = Array.from(document.querySelectorAll(css)); } catch (e) { continue; }
        if (text) {
            const needle = norm(text);
            nodes = nodes.filter(el => norm(el.innerText || el.textContent).includes(needle));
        }
        // Python takes .first (first DOM match) and skips the whole rung if it is
        // not usable, rather than hunting for a later sibling.
        const el = nodes[0];
        if (!el || !visible(el) || !enabled(el)) continue;
        el.scrollIntoView({ block: 'center', inline: 'center' });
        const r = el.getBoundingClientRect();
        return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    }
    return null;
}`

// clickContinue mirrors _click_continue (app.py:10117-10155): walk the localized
// continue/finish button ladder, click the winner with a real mouse click, and
// wait for DOMContentLoaded; fall back to the DOM submit path.
func (h *PhoneHandler) clickContinue() bool {
	if h.page == nil {
		return false
	}
	if v, err := h.page.Rod.Eval(phoneClickContinueLadderJS); err == nil && v != nil && !v.Value.Nil() {
		x := v.Value.Get("x").Num()
		y := v.Value.Get("y").Num()
		if phoneClickPoint(h.page, x, y) {
			// Python re-raised a wait timeout into the next ladder step; here the
			// click already landed, so a slow readyState is downgraded to success.
			_ = h.page.WaitDOMContentLoaded(10 * time.Second)
			return true
		}
	}
	if h.page.ClickSubmitButtonByDOM() {
		_ = h.page.WaitDOMContentLoaded(10 * time.Second)
		return true
	}
	return false
}

// phoneClickPoint performs a real mouse move+click (anti-detection), mirroring
// browser.Page.ClickButtonByText's coordinate path.
func phoneClickPoint(p *browser.Page, x, y float64) bool {
	if err := p.Rod.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return false
	}
	return p.Rod.Mouse.Click(proto.InputMouseButtonLeft, 1) == nil
}

// hasVisiblePassword mirrors _has_visible_password (app.py:10543-10544).
func (h *PhoneHandler) hasVisiblePassword() bool {
	if h.page == nil {
		return false
	}
	return len(h.page.VisibleInputs([]string{`input[type="password"]`, `input[name="password"]`})) > 0
}

// phoneAboutYouMarkers mirrors the text markers in _has_about_you_form
// (app.py:10994-11012), all lowercased, kept verbatim across EN/ES/JA/ZH.
var phoneAboutYouMarkers = []string{
	"tell us about you",
	"about you",
	"birth",
	"how old are you",
	"full name",
	"finish creating account",
	"confirmemos tu edad",
	"fecha de nacimiento",
	"nombre y apellidos",
	"finalizar la creación de la cuenta",
	"finalizar la creacion de la cuenta",
	"生まれた年",
	"生年",
	"年齢",
	"アカウントの作成を完了する",
	"出生年",
	"年龄",
}

// hasAboutYouForm mirrors _has_about_you_form (app.py:10991-11020). The
// about-you cluster owns the canonical implementation; when HasAboutYouForm is
// wired the hook wins, otherwise this equivalent runs.
func (h *PhoneHandler) hasAboutYouForm() bool {
	if h.HasAboutYouForm != nil {
		return h.HasAboutYouForm()
	}
	if h.page == nil {
		return false
	}
	raw, ok := h.bodyInnerText(1000 * time.Millisecond)
	if !ok {
		return false
	}
	text := strings.ToLower(raw)
	hasAboutText := false
	for _, marker := range phoneAboutYouMarkers {
		if strings.Contains(text, marker) {
			hasAboutText = true
			break
		}
	}
	if !hasAboutText {
		return false
	}
	return len(h.page.VisibleInputs([]string{`input`, `textarea`, `[contenteditable="true"]`})) >= 2
}

// pageTextSummary mirrors _page_text_summary (app.py:10983-10989): collapsed
// body text truncated to maxLength, falling back to the URL.
func (h *PhoneHandler) pageTextSummary(maxLength int) string {
	if h.page == nil {
		return ""
	}
	raw, ok := h.bodyInnerText(1500 * time.Millisecond)
	if !ok {
		return h.page.URL()
	}
	text := pyCollapseStrip(raw)
	text = phoneTruncateRunes(text, maxLength)
	if text == "" {
		return h.page.URL()
	}
	return text
}

// bodyInnerText mirrors page.locator("body").inner_text(timeout=...). The bool
// distinguishes "read failed" (Python's except branch) from "body is empty".
func (h *PhoneHandler) bodyInnerText(timeout time.Duration) (string, bool) {
	if h.page == nil || h.page.Rod == nil {
		return "", false
	}
	v, err := h.page.Rod.Timeout(timeout).Eval(`() => (document.body && document.body.innerText) || ''`)
	if err != nil || v == nil {
		return "", false
	}
	return v.Value.Str(), true
}

func phoneTruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
