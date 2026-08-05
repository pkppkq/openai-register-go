// Package worker ports OpenAIRegisterPayLinkWorker (app.py:8846-12298).
//
// This file is the Cloudflare / Turnstile cluster (app.py:10669-10990). The pure
// text matchers live in internal/openai and the low-level DOM primitives live in
// internal/browser; everything here is ORCHESTRATION: the auto-solve loop, the
// manual-wait loop, the headless hard-gate and the challenge-tab lifecycle.
package worker

import (
	"fmt"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// cfTruncateRunes slices by characters (Python str slicing), not bytes, so the
// Chinese/localized log strings never get cut mid-rune.
func cfTruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// CFSolver ports the Cloudflare/Turnstile method cluster of
// OpenAIRegisterPayLinkWorker (app.py:10669-10990): _is_cloudflare_challenge,
// _extract_cloudflare_challenge_url, _is_cloudflare_challenge_page,
// _has_cloudflare_clearance, _click_turnstile_checkbox, _try_pass_cloudflare,
// _handle_cloudflare_challenge, _wait_after_otp_submit and _page_text_summary.
//
// Browser/Page/Headless/Log mirror the worker attributes the Python methods read
// off `self`. The three func fields are seams for behaviour that lives outside
// this cluster in Python and outside this file in Go; all of them are optional
// and nil-safe:
//
//   - LowerWindows mirrors lower_playwright_chromium_windows_later
//     (app.py:456) — the Win32 z-order demotion that keeps the automated Chromium
//     behind the UI. No Go equivalent exists yet; leave nil for a no-op.
//   - HasAboutYouForm mirrors _has_about_you_form (app.py:10991), owned by the
//     about-you-form cluster.
//   - HasOTPInput mirrors _has_otp_input (app.py:10576), owned by the
//     password/email-OTP cluster.
//
// Leaving HasAboutYouForm / HasOTPInput nil degrades WaitAfterOTPSubmit to
// URL-only detection (the probes read as "not present"), which can make it
// return early where Python would have kept polling — wire them up in the
// assembled worker.
type CFSolver struct {
	Browser  *browser.Browser
	Page     *browser.Page
	Headless bool
	Log      func(string)

	LowerWindows    func(retries int)
	HasAboutYouForm func(p *browser.Page) bool
	HasOTPInput     func(p *browser.Page) bool
}

// NewCFSolver builds a CFSolver over the worker's live browser + primary page,
// mirroring how the Python methods bind to self.browser / self.headless / self.log.
func NewCFSolver(b *browser.Browser, p *browser.Page, headless bool, log func(string)) *CFSolver {
	return &CFSolver{Browser: b, Page: p, Headless: headless, Log: log}
}

func (s *CFSolver) logf(format string, args ...any) {
	if s == nil || s.Log == nil {
		return
	}
	if len(args) == 0 {
		s.Log(format)
		return
	}
	s.Log(fmt.Sprintf(format, args...))
}

// lowerWindows invokes the optional z-order hook; nil is a no-op (Python fired a
// daemon thread whose failure was invisible to the caller, so a silent no-op is
// the faithful degradation).
func (s *CFSolver) lowerWindows(retries int) {
	if s.LowerWindows == nil {
		return
	}
	s.LowerWindows(retries)
}

// target resolves the page a method should act on: an explicit page, else the
// solver's primary page.
func (s *CFSolver) target(p *browser.Page) *browser.Page {
	if p != nil {
		return p
	}
	return s.Page
}

// IsCloudflareChallenge mirrors _is_cloudflare_challenge (app.py:10669-10687).
// The matcher itself lives in internal/openai; this is the method-shaped
// call site the rest of the worker uses.
func (s *CFSolver) IsCloudflareChallenge(text string) bool {
	return openai.IsCloudflareChallengeText(text)
}

// ExtractCloudflareChallengeURL mirrors _extract_cloudflare_challenge_url
// (app.py:10689-10699). The parser lives in internal/openai.
func (s *CFSolver) ExtractCloudflareChallengeURL(text string) string {
	return openai.ExtractCloudflareChallengeURL(text)
}

// IsCloudflareChallengePage mirrors _is_cloudflare_challenge_page
// (app.py:10701-10721): detector JS, then title+URL, then body text. A nil or
// closed page reads as "no challenge", exactly like Python's three swallowed
// exceptions falling through to `return False`.
func (s *CFSolver) IsCloudflareChallengePage(p *browser.Page) bool {
	page := s.target(p)
	if page == nil {
		return false
	}
	return page.IsCloudflareChallengePage()
}

// HasCloudflareClearance mirrors _has_cloudflare_clearance (app.py:10723-10746):
// a non-empty, unexpired cf_clearance cookie in the browser context. Python read
// cookies from page.context; go-rod scopes cookies to the browser, so the check
// is browser-wide (same context in practice — one worker owns one browser).
func (s *CFSolver) HasCloudflareClearance() bool {
	if s.Browser == nil {
		return false
	}
	return s.Browser.HasCloudflareClearance()
}

// ClickTurnstileCheckbox mirrors _click_turnstile_checkbox (app.py:10748-10809):
// best-effort force-click of the top-level challenge widgets, then inside each
// Cloudflare iframe. The DOM walk is browser.Page.ClickTurnstileCheckbox; this
// wrapper only reproduces the Python log lines verbatim
// ("[CF] 点击挑战控件: <selector>" / "[CF] iframe 点击: <selector> (<url>)").
func (s *CFSolver) ClickTurnstileCheckbox(p *browser.Page) bool {
	page := s.target(p)
	if page == nil {
		return false
	}
	clicked, detail := page.ClickTurnstileCheckbox()
	if detail != "" {
		if strings.HasPrefix(detail, "挑战控件:") {
			s.logf("[CF] 点击%s", detail)
		} else {
			s.logf("[CF] %s", detail)
		}
	}
	return clicked
}

// TryPassCloudflare mirrors _try_pass_cloudflare (app.py:10829-10910).
//
// Returns true when the challenge is absent or has been cleared, false on
// timeout. Shape, verbatim from Python:
//
//	no challenge                -> true immediately
//	headless                    -> log + false (hard gate, cannot solve)
//	auto phase  CF_AUTO_SOLVE_TIMEOUT (45s): re-check, click at most once per
//	                             1.0s, sleep 0.6s
//	re-check after the auto deadline
//	allowManual == false        -> log + false
//	manual phase CF_MANUAL_WAIT_TIMEOUT (90s): re-check, progress log every 10s,
//	                             click EVERY iteration, sleep 1.0s
//	final re-check
//
// The success heuristic is deliberately lenient (cf_clearance present OR the
// challenge markers are gone) because a solved managed challenge does not always
// materialise a cf_clearance cookie on the page's own origin.
//
// Pass p == nil to act on the solver's primary page.
func (s *CFSolver) TryPassCloudflare(p *browser.Page, allowManual bool, reason string) bool {
	page := s.target(p)
	if !s.IsCloudflareChallengePage(page) {
		return true
	}

	if s.Headless {
		s.logf("[CF] headless 模式无法处理 Cloudflare 挑战")
		return false
	}

	label := ""
	if reason != "" {
		label = fmt.Sprintf(" (%s)", reason)
	}
	s.logf("[CF] 检测到 Cloudflare 挑战%s，尝试自动过盾…", label)
	// app.py:10846-10847 calls lower_playwright_chromium_windows_later(0.35) and
	// (1.2). Those floats land on the RETRIES parameter, not `delay` — Python bug
	// (see report): the first degrades to a single pass and the second raises
	// TypeError inside its daemon thread. Modelled as two 1-retry passes.
	s.lowerWindows(1)
	s.lowerWindows(1)

	autoDeadline := time.Now().Add(time.Duration(openai.CFAutoSolveTimeout) * time.Second)
	var lastClick time.Time
	for time.Now().Before(autoDeadline) {
		if s.HasCloudflareClearance() || !s.IsCloudflareChallengePage(page) {
			s.logf("[CF] 自动过盾成功")
			return true
		}
		now := time.Now()
		if lastClick.IsZero() || now.Sub(lastClick) >= time.Second {
			s.ClickTurnstileCheckbox(page)
			lastClick = now
		}
		time.Sleep(600 * time.Millisecond)
	}

	if s.HasCloudflareClearance() || !s.IsCloudflareChallengePage(page) {
		s.logf("[CF] 自动过盾成功")
		return true
	}

	if !allowManual {
		s.logf("[CF] 自动过盾失败（未启用人工等待）")
		return false
	}

	s.logf("[CF] 自动过盾未完成，请在浏览器中手动通过人机验证（最多 %ds）…", openai.CFManualWaitTimeout)
	// app.py:10880: lower_playwright_chromium_windows_later(0.2) -> retries=1.
	s.lowerWindows(1)
	manualDeadline := time.Now().Add(time.Duration(openai.CFManualWaitTimeout) * time.Second)
	var lastNotice time.Time
	for time.Now().Before(manualDeadline) {
		if s.HasCloudflareClearance() || !s.IsCloudflareChallengePage(page) {
			s.logf("[CF] 人工过盾成功")
			return true
		}
		now := time.Now()
		if lastNotice.IsZero() || now.Sub(lastNotice) >= 10*time.Second {
			remain := int(manualDeadline.Sub(now).Seconds())
			if remain < 0 {
				remain = 0
			}
			s.logf("[CF] 仍在等待手动过盾，剩余约 %ds", remain)
			lastNotice = now
		}
		s.ClickTurnstileCheckbox(page)
		s.lowerWindows(1)
		time.Sleep(1000 * time.Millisecond)
	}

	if s.HasCloudflareClearance() || !s.IsCloudflareChallengePage(page) {
		s.logf("[CF] 过盾成功")
		return true
	}
	s.logf("[CF] 自动+人工等待均未放行")
	return false
}

// HandleCloudflareChallenge mirrors _handle_cloudflare_challenge
// (app.py:10912-10962): the email-OTP path's recovery when the OTP API answers
// with a Cloudflare interstitial.
//
// Headless is a hard gate (no human can solve it) and returns the verbatim
// Chinese error. Otherwise the challenge URL is extracted from challengeHTML and
// opened in the primary page; if that navigation fails a FALLBACK TAB is opened
// instead — that tab is closed on EVERY exit path (navigation failure,
// try-pass failure, and success) so the challenge tab never leaks into the
// parked browser session. On success the primary page is returned to
// /email-verification and the caller retries the OTP submit.
func (s *CFSolver) HandleCloudflareChallenge(challengeHTML string) error {
	if s.Headless {
		return fmt.Errorf("触发 Cloudflare challenge，但当前开启了无头模式，无法手动验证；请取消 UI 中的“无头浏览器”后重试")
	}

	challengeURL := openai.ExtractCloudflareChallengeURL(challengeHTML)
	page := s.Page
	target := page
	openedExtra := false
	if challengeURL != "" {
		s.logf("[OTP] Cloudflare challenge URL: %s", cfTruncateRunes(challengeURL, 160))
		if err := page.Navigate(challengeURL, 90*time.Second); err != nil {
			s.logf("[OTP] 当前页打开 challenge 失败，改用新标签: %v", err)
			extra, newErr := s.Browser.NewPage()
			if newErr != nil {
				// Python's page.context.new_page() raising left opened_extra
				// False and target still == page: nothing to close.
				return fmt.Errorf("无法打开 Cloudflare 挑战页: %w", newErr)
			}
			target = extra
			openedExtra = true
			s.lowerWindows(10)
			if gotoErr := target.Navigate(challengeURL, 90*time.Second); gotoErr != nil {
				target.Close()
				return fmt.Errorf("无法打开 Cloudflare 挑战页: %w", gotoErr)
			}
		} else {
			target = page
		}
	} else {
		s.logf("[OTP] 未解析到独立 challenge URL，直接在当前页尝试过盾")
	}

	if !s.TryPassCloudflare(target, true, "OTP challenge") {
		if openedExtra {
			target.Close()
		}
		return fmt.Errorf(
			"Cloudflare 自动+人工等待均未放行（auto %ds + manual %ds）；请更换动态代理后重试",
			openai.CFAutoSolveTimeout, openai.CFManualWaitTimeout,
		)
	}

	if openedExtra {
		target.Close()
	}
	s.lowerWindows(10)
	if err := page.Navigate(openai.AuthBaseURL+"/email-verification", 90*time.Second); err != nil {
		s.logf("[OTP] 返回 email-verification 失败: %v", err)
	}
	s.logf("Cloudflare 已放行，重试提交邮箱验证码")
	return nil
}

// WaitAfterOTPSubmit mirrors _wait_after_otp_submit (app.py:10964-10981): after
// the email verification code is submitted, poll for up to timeoutSeconds
// (Python default 20; pass 0 for that default) at a 1s cadence until one of
//
//	a live ChatGPT session          -> nil
//	the about-you step              -> nil
//	we simply left the OTP page     -> nil
//
// A ChatGPT tab that exists but has no session yet keeps the loop alive. On
// timeout, an open ChatGPT tab is still treated as success (with the Python log
// line); otherwise the verbatim Chinese error carries a page-text summary.
func (s *CFSolver) WaitAfterOTPSubmit(timeoutSeconds int) error {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 20
	}
	page := s.Page
	started := time.Now()
	timeout := time.Duration(timeoutSeconds) * time.Second
	for time.Since(started) < timeout {
		if s.Browser != nil && s.Browser.HasChatGPTSession() {
			return nil
		}
		if s.Browser != nil && s.Browser.ContextHasChatGPTPage() {
			time.Sleep(1 * time.Second)
			continue
		}
		url := ""
		if page != nil {
			url = page.URL()
		}
		if strings.Contains(url, "about-you") || s.aboutYouForm(page) {
			return nil
		}
		if !(strings.Contains(url, "email-verification") || s.otpInput(page)) {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	if s.Browser != nil && s.Browser.ContextHasChatGPTPage() {
		s.logf("邮箱验证码提交后已打开 ChatGPT 页面，继续等待 session 生效")
		return nil
	}
	return fmt.Errorf(
		"验证码提交后页面仍停留在邮箱验证页，可能验证码已过期/已使用或页面校验失败。页面内容: %s",
		s.PageTextSummary(page, 0),
	)
}

func (s *CFSolver) aboutYouForm(p *browser.Page) bool {
	if s.HasAboutYouForm == nil || p == nil {
		return false
	}
	return s.HasAboutYouForm(p)
}

func (s *CFSolver) otpInput(p *browser.Page) bool {
	if s.HasOTPInput == nil || p == nil {
		return false
	}
	return s.HasOTPInput(p)
}

// PageTextSummary mirrors _page_text_summary (app.py:10983-10989): the page's
// body text with whitespace collapsed, truncated to maxLength characters, with
// the page URL as the fallback both when the body cannot be read (1.5s budget)
// and when the collapsed text is empty. Pass maxLength <= 0 for Python's
// default of 300; pass nil for the solver's primary page.
func (s *CFSolver) PageTextSummary(p *browser.Page, maxLength int) string {
	page := s.target(p)
	if page == nil {
		return ""
	}
	if maxLength <= 0 {
		maxLength = 300
	}
	url := page.URL()
	v, err := page.Rod.Timeout(1500 * time.Millisecond).Eval(`() => (document.body && document.body.innerText) || ''`)
	if err != nil || v == nil {
		return url
	}
	// pyCollapseStrip, not TrimSpace + an ASCII \s: this text feeds
	// ClassifyPhoneRejection, which decides how the phone pool marks a number.
	text := pyCollapseStrip(v.Value.Str())
	if summary := cfTruncateRunes(text, maxLength); summary != "" {
		return summary
	}
	return url
}
