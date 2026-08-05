// Package worker ports the OpenAIRegisterPayLinkWorker browser flow (app.py
// 8846-12298) to go-rod.
//
// This file holds the password + email-OTP cluster: app.py 10543-10668
// (_has_visible_password, _fill_password_step, _openai_password_for_account,
// _generate_password, _has_otp_input, _submit_email_code,
// _validate_email_code_api) plus _read_email_otp_code (app.py 8982-9001).
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// OTPHandler mirrors the slice of OpenAIRegisterPayLinkWorker state that the
// password / email-verification steps touch (app.py 10543-10668 and
// _read_email_otp_code app.py 8982-9001).
//
// The four func fields at the bottom stand in for worker methods that live in
// OTHER port units; the orchestration layer must wire them. They are modelled as
// hooks (rather than re-implemented here) so the cross-cluster behaviour has a
// single definition.
type OTPHandler struct {
	// Page is the auth.openai.com page the flow is driving.
	Page *browser.Page
	// Browser is the owning context (needed by the WaitAfterOTPSubmit hook's
	// _has_chatgpt_session / _context_has_chatgpt_page probes).
	Browser *browser.Browser
	// Account is the worker's mail account; the EMPTY-password branch of
	// _openai_password_for_account writes a generated password back into it.
	Account *models.MailAccount
	// Reader mirrors self.otp_reader. May be nil: _read_email_otp_code lazily
	// creates one via mail.CreateMailReader(account, log, "").
	Reader mail.Reader
	// ManualEmailOTP mirrors self.manual_email_otp.
	ManualEmailOTP bool
	// InputCallback mirrors self.input_callback(kind, email, prompt) -> str.
	InputCallback func(kind, email, prompt string) string
	// Log mirrors self.log.
	Log func(string)

	// ClickContinue mirrors _click_continue (app.py:10117). When nil, falls back
	// to the DOM tail of that method (_click_submit_button_by_dom, app.py:10152).
	ClickContinue func() bool
	// HasAboutYouForm mirrors _has_about_you_form (app.py:10991). Required for
	// the first negative guard of _has_otp_input; nil == "not the about-you page".
	HasAboutYouForm func() bool
	// LooksLikeRegisterPhoneCodePage mirrors _looks_like_register_phone_code_page
	// (app.py:10440). Required for the second negative guard of _has_otp_input;
	// nil == "not the register phone-code page".
	LooksLikeRegisterPhoneCodePage func() bool
	// WaitAfterOTPSubmit mirrors _wait_after_otp_submit (app.py:10964).
	WaitAfterOTPSubmit func() error
	// HandleCloudflareChallenge mirrors _handle_cloudflare_challenge
	// (app.py:10912); it receives the raw challenge body text.
	HandleCloudflareChallenge func(challengeHTML string) error
}

func (h *OTPHandler) logf(msg string) {
	if h.Log != nil {
		h.Log(msg)
	}
}

// otpPasswordSelectors is the _has_visible_password / _fill_password_step ladder
// (app.py:10544, 10549). Order is a priority ladder — do not sort.
var otpPasswordSelectors = []string{
	`input[type="password"]`,
	`input[name="password"]`,
}

// otpDetectSelectors is the _has_otp_input ladder (app.py:10581-10588).
var otpDetectSelectors = []string{
	`input[autocomplete="one-time-code"]`,
	`input[name="code"]`,
	`input[aria-label*="code" i]`,
	`input[placeholder*="code" i]`,
	`input[aria-label*="验证码" i]`,
	`input[placeholder*="验证码" i]`,
}

// otpFillSelectors is the _submit_email_code ladder (app.py:10596-10601). It is
// deliberately WIDER than otpDetectSelectors (adds inputmode=numeric / type=tel)
// because the split 6-box widget exposes neither autocomplete nor name=code.
var otpFillSelectors = []string{
	`input[autocomplete="one-time-code"]`,
	`input[inputmode="numeric"]`,
	`input[type="tel"]`,
	`input[name="code"]`,
}

var (
	// otpStaleCodeRe is the retry trigger of _submit_email_code (app.py:10615).
	// RE2-safe: no lookaround, no backreference.
	otpStaleCodeRe = regexp.MustCompile(`(?i)wrong_email_otp_code|expired|invalid.*code|验证码.*(错误|过期)`)
	// otpNonDigitRe mirrors re.sub(r"\D", "", code) (app.py:8997). RE2's \D is
	// ASCII-only ([^0-9]); Python's is every Unicode decimal digit, which is
	// exactly \p{Nd} (verified exhaustively over U+0000..U+10FFFF). With the
	// ASCII spelling an operator pasting non-ASCII digits got them silently
	// deleted, and a code reduced to "" fell back to the raw pasted text.
	otpNonDigitRe = regexp.MustCompile(`[^\p{Nd}]`)
)

// HasVisiblePassword mirrors _has_visible_password (app.py:10543-10544).
func (h *OTPHandler) HasVisiblePassword() bool {
	return len(h.Page.VisibleInputs(otpPasswordSelectors)) > 0
}

// FillPasswordStep mirrors _fill_password_step (app.py:10546-10555): fill EVERY
// visible password box with the account's OpenAI-usable password (React-safe
// ForceFill), then press continue.
func (h *OTPHandler) FillPasswordStep() error {
	openaiPassword := h.OpenAIPasswordForAccount()

	inputs := h.Page.VisibleInputs(otpPasswordSelectors)
	if len(inputs) == 0 {
		return errors.New("进入密码步骤但未找到密码输入框")
	}
	for _, inputBox := range inputs {
		// Python ignores the _force_fill_locator return value.
		h.Page.ForceFill(inputBox, openaiPassword)
	}
	if !h.clickContinue() {
		return errors.New("密码已填写，但未找到继续按钮")
	}
	return nil
}

// clickContinue routes to the _click_continue hook, or degrades to the
// DOM-based tail of that method (app.py:10152-10154) when it is not wired.
func (h *OTPHandler) clickContinue() bool {
	if h.ClickContinue != nil {
		return h.ClickContinue()
	}
	if h.Page.ClickSubmitButtonByDOM() {
		_ = h.Page.WaitDOMContentLoaded(10 * time.Second)
		return true
	}
	return false
}

// OpenAIPasswordForAccount mirrors _openai_password_for_account
// (app.py:10557-10569).
//
// Only the EMPTY-password branch writes back to the account. A short imported
// password is NOT mutated — a longer OpenAI-usable variant is derived from
// sha256("email:password") and returned, while the import line keeps its
// original value.
func (h *OTPHandler) OpenAIPasswordForAccount() string {
	password := ""
	if h.Account != nil {
		password = h.Account.Password
	}
	if password == "" {
		generated := GeneratePassword()
		if h.Account != nil {
			h.Account.Password = generated
		}
		h.logf("账号需要密码步骤，已生成密码: " + generated)
		return generated
	}
	// Python len() counts characters, not bytes.
	if len([]rune(password)) >= 12 {
		h.logf("账号需要密码步骤，使用导入行已有密码继续")
		return password
	}
	email := ""
	if h.Account != nil {
		email = h.Account.Email
	}
	openaiPassword := otpDerivePassword(email, password)
	h.logf("账号需要密码步骤，导入密码不足 12 位，已自动补足为 OpenAI 可用密码（不改导入行）")
	return openaiPassword
}

// otpDerivePassword is the pure derivation of app.py:10565-10566, split out so
// it can be differentially tested against the Python source.
func otpDerivePassword(email, password string) string {
	sum := sha256.Sum256([]byte(email + ":" + password))
	digest := hex.EncodeToString(sum[:])
	return password + "A7!" + digest[:12]
}

// otpPasswordAlphabet is the ambiguity-free alphabet of _generate_password
// (app.py:10572) — no I/l/O/0/1.
const otpPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789" // gitleaks:allow，密码生成字符表不是凭据

// GeneratePassword mirrors _generate_password (app.py:10571-10574): 13 random
// characters from the ambiguity-free alphabet plus the fixed "!A7" suffix, which
// guarantees the OpenAI complexity rules (length >= 12, symbol, upper, digit).
func GeneratePassword() string {
	var b strings.Builder
	b.Grow(16)
	for i := 0; i < 13; i++ {
		b.WriteByte(otpPasswordAlphabet[rand.IntN(len(otpPasswordAlphabet))])
	}
	b.WriteString("!A7")
	return b.String()
}

// HasOTPInput mirrors _has_otp_input (app.py:10576-10588).
//
// The two negative guards are MANDATORY: the about-you page and the register
// phone-code page both render inputs that match the OTP selectors, and treating
// either as an email-OTP prompt derails the flow.
func (h *OTPHandler) HasOTPInput() bool {
	if strings.Contains(h.Page.URL(), "about-you") {
		return false
	}
	if h.HasAboutYouForm != nil && h.HasAboutYouForm() {
		return false
	}
	if h.LooksLikeRegisterPhoneCodePage != nil && h.LooksLikeRegisterPhoneCodePage() {
		return false
	}
	return len(h.Page.VisibleInputs(otpDetectSelectors)) > 0
}

// SubmitEmailCode mirrors _submit_email_code (app.py:10590-10625).
//
// minTimestamp is unix-epoch FLOAT seconds (the unit the mail readers expect).
// Two attempts: the first waits 600s for the code, the second 180s. When the
// first code is rejected as stale/expired the wait floor is rewound to now-5 so
// the mail reader is forced to wait for a genuinely NEWER email rather than
// re-reading the one that just failed.
func (h *OTPHandler) SubmitEmailCode(minTimestamp float64) error {
	h.logf("等待 OpenAI 邮箱验证码")
	waitMinTimestamp := minTimestamp
	lastError := ""

	for attempt := 1; attempt < 3; attempt++ {
		timeout := 180
		if attempt == 1 {
			timeout = 600
		}
		code, err := h.ReadEmailOTPCode(waitMinTimestamp, timeout)
		if err != nil {
			return err
		}

		inputs := h.Page.VisibleInputs(otpFillSelectors)
		if len(inputs) == 0 {
			return errors.New("页面未找到验证码输入框")
		}
		if len(inputs) >= 6 {
			// Split 6-box widget: clear every box, then one character per box.
			for _, inputBox := range inputs {
				if err := otpPlainFill(inputBox, ""); err != nil {
					return err
				}
			}
			chars := []rune(code)
			if len(chars) > 6 {
				chars = chars[:6]
			}
			for index, char := range chars {
				if err := otpPlainFill(inputs[index], string(char)); err != nil {
					return err
				}
			}
		} else {
			if err := otpPlainFill(inputs[0], code); err != nil {
				return err
			}
		}

		continueURL, err := h.ValidateEmailCodeAPI(code)
		if err != nil {
			// Python catches `except RuntimeError` here (app.py:10613), which DOES
			// include AccountDeactivatedError (app.py:3686 subclasses RuntimeError)
			// — so a deactivated-account detail whose text matches the stale-code
			// regex is retried in both languages, and the bare `raise` below still
			// preserves the type.
			//
			// It does NOT include a page.evaluate failure, which in Python escapes
			// _submit_email_code untouched. Go has one error channel, so the
			// "EmailOtpValidate 执行失败" wrapper reaches this classifier too;
			// harmless because no go-rod/CDP error text matches the regex, and the
			// only cost if one ever did is one extra 180s wait before the same
			// failure.
			lastError = err.Error()
			if attempt < 2 && otpStaleCodeRe.MatchString(lastError) {
				h.logf("邮箱验证码疑似旧码/过期，继续等待下一封新验证码")
				waitMinTimestamp = float64(time.Now().UnixNano())/1e9 - 5
				continue
			}
			// Bare `raise` in Python: the ORIGINAL error (possibly
			// *models.AccountDeactivatedError) must propagate untouched.
			return err
		}
		h.logf("已通过接口提交邮箱验证码")
		if continueURL != "" {
			if err := h.Page.Navigate(continueURL, 90*time.Second); err != nil {
				return err
			}
		}
		if h.WaitAfterOTPSubmit != nil {
			return h.WaitAfterOTPSubmit()
		}
		return nil
	}

	// Unreachable in practice (attempt 2 always returns or errors); kept to
	// mirror app.py:10625.
	if lastError == "" {
		lastError = "unknown"
	}
	return fmt.Errorf("邮箱验证码提交失败: %s", lastError)
}

// emailOTPValidateJS is the in-browser validate call of _validate_email_code_api
// (app.py:10631-10647), kept byte-faithful.
//
// CRITICAL: this MUST stay an in-page fetch executed through the live Chromium
// target. credentials:'include' rides the real session cookies AND the
// cf_clearance cookie that the browser earned; the spoofed origin/referer keep
// the request indistinguishable from the SPA's own. Re-routing it through
// internal/tlsclient would send it from a different TLS/cookie identity, which
// breaks auth and immediately re-triggers a Cloudflare challenge.
const emailOTPValidateJS = `async ({code}) => {
    const resp = await fetch('/api/accounts/email-otp/validate', {
        method: 'POST',
        credentials: 'include',
        headers: {
            accept: 'application/json',
            'content-type': 'application/json',
            origin: 'https://auth.openai.com',
            referer: 'https://auth.openai.com/email-verification',
        },
        body: JSON.stringify({ code }),
    });
    const text = await resp.text();
    let data = null;
    try { data = JSON.parse(text); } catch (_) {}
    return { ok: resp.ok, status: resp.status, text, data };
}`

// ValidateEmailCodeAPI mirrors _validate_email_code_api (app.py:10627-10667):
// POST the code from inside the page and return the continue_url (may be "").
//
// The inner 3-attempt loop is the Cloudflare retry ladder and is intentionally
// separate from SubmitEmailCode's 2-attempt "fetch a newer code" loop — a CF
// challenge must NOT consume an OTP attempt.
func (h *OTPHandler) ValidateEmailCodeAPI(code string) (string, error) {
	lastDetail := ""
	for attempt := 0; attempt < 3; attempt++ {
		v, err := h.Page.Rod.Eval(emailOTPValidateJS, map[string]string{"code": code})
		if err != nil {
			// Python lets a page.evaluate failure propagate out of this method.
			return "", fmt.Errorf("EmailOtpValidate 执行失败: %w", err)
		}
		result, _ := v.Value.Val().(map[string]any)
		if ok, _ := result["ok"].(bool); ok {
			data, _ := result["data"].(map[string]any)
			return otpContinueURL(data), nil
		}

		lastDetail = otpResultDetail(result)
		if openai.IsCloudflareChallengeText(lastDetail) && attempt < 2 {
			h.logf("EmailOtpValidate 触发 Cloudflare challenge，正在浏览器中打开挑战页并等待放行")
			if h.HandleCloudflareChallenge != nil {
				if err := h.HandleCloudflareChallenge(lastDetail); err != nil {
					return "", err
				}
			}
			continue
		}
		break
	}

	if openai.IsCloudflareChallengeText(lastDetail) {
		return "", errors.New("EmailOtpValidate 被 Cloudflare 持续拦截。请换更干净的动态代理，或在浏览器里的 Cloudflare 页面手动等待通过后重试。")
	}
	// Python uses str.casefold(); the needle is pure ASCII so ToLower matches it
	// identically.
	if strings.Contains(strings.ToLower(lastDetail), "account_deactivated") {
		return "", &models.AccountDeactivatedError{
			Msg: "OpenAI 在邮箱验证码校验时返回 account_deactivated: " + otpTruncate(lastDetail, 800),
		}
	}
	return "", fmt.Errorf("EmailOtpValidate 接口失败: %s", otpTruncate(lastDetail, 800))
}

// otpContinueURL mirrors the nested fallback of app.py:10652 —
// payload.continue_url || payload.page.payload.url || "".
func otpContinueURL(payload map[string]any) string {
	if s, _ := payload["continue_url"].(string); s != "" {
		return s
	}
	page, _ := payload["page"].(map[string]any)
	inner, _ := page["payload"].(map[string]any)
	s, _ := inner["url"].(string)
	return s
}

// otpResultDetail mirrors `str(result.get("text") or result.get("status") or "")`
// (app.py:10654): body text first, HTTP status as a decimal string otherwise.
func otpResultDetail(result map[string]any) string {
	if text, _ := result["text"].(string); text != "" {
		return text
	}
	// `or` fires on any falsy status, so 0 falls through but "403" does not. A
	// float64-only branch dropped a string status entirely, turning an
	// empty-bodied error response into an empty detail and hiding the HTTP code
	// from "EmailOtpValidate 接口失败: %s".
	switch status := result["status"].(type) {
	case float64:
		if status != 0 {
			return strconv.Itoa(int(status))
		}
	case string:
		return status
	case bool:
		if status {
			return "True"
		}
	}
	return ""
}

// otpManualDigits is `digits = re.sub(r"\D", "", code); return digits or code`
// (app.py:8997-8998), split out so it can be differentially tested against the
// Python source.
func otpManualDigits(code string) string {
	if digits := otpNonDigitRe.ReplaceAllString(code, ""); digits != "" {
		return digits
	}
	return code
}

// otpTruncate cuts to n CHARACTERS (Python slices str by rune, not byte).
func otpTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// otpPlainFill is Playwright's Locator.fill(): select whatever is in the box and
// insert the new text ("" -> Delete the selection).
//
// The split-OTP path (app.py:10606-10610) deliberately uses plain fill and NOT
// browser.Page.ForceFill: each of the six boxes advances focus on `input`, and
// ForceFill's extra change/blur dispatch makes the widget drop characters.
// Errors are returned rather than swallowed because Python's .fill() has no
// try/except there — a failed fill aborts the step in both languages.
func otpPlainFill(el *rod.Element, value string) error {
	if el == nil {
		return errors.New("验证码输入框不可用")
	}
	if err := el.Timeout(5 * time.Second).Focus(); err != nil {
		return err
	}
	// Best-effort: an already-empty box has nothing to select.
	_ = el.Timeout(5 * time.Second).SelectAllText()
	if value == "" {
		// CDP Input.insertText("") does not remove a selection, so clear the box
		// with a Delete key the way Playwright's fill("") does.
		return el.Timeout(5 * time.Second).Type(input.Delete)
	}
	return el.Timeout(5 * time.Second).Input(value)
}

// ReadEmailOTPCode mirrors _read_email_otp_code (app.py:8982-9001).
//
// minTimestamp is unix-epoch FLOAT seconds and is passed through to
// mail.Reader.WaitForCode unchanged, together with the standard lookback window.
// In manual mode the mail path is skipped entirely and the operator's input is
// reduced to its digits (falling back to the raw text when it has none).
func (h *OTPHandler) ReadEmailOTPCode(minTimestamp float64, timeout int) (string, error) {
	if h.ManualEmailOTP {
		if h.InputCallback == nil {
			return "", errors.New("已启用手动输入邮箱验证码，但未配置输入回调")
		}
		h.logf("手动邮箱验证码模式：跳过邮箱令牌/IMAP，等待人工输入")
		email := ""
		if h.Account != nil {
			email = h.Account.Email
		}
		code := strings.TrimSpace(h.InputCallback(
			"email-code",
			email,
			fmt.Sprintf("请输入 %s 收到的 OpenAI 邮箱验证码（一般 6 位数字）", email),
		))
		if code == "" {
			return "", errors.New("已取消邮箱验证码输入")
		}
		return otpManualDigits(code), nil
	}
	if h.Reader == nil {
		reader, err := mail.CreateMailReader(h.Account, mail.Log(h.logf), "")
		if err != nil {
			return "", err
		}
		h.Reader = reader
	}
	return h.Reader.WaitForCode(context.Background(), minTimestamp, timeout, mail.DefaultEmailOTPLookbackSeconds)
}
