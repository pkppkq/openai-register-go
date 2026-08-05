// Package worker holds the port of app.py's OpenAIRegisterPayLinkWorker.
//
// register.go ports the core register/login STATE MACHINE (app.py 9769-9933):
// _register (app.py:9769-9869) and _login_existing_account (app.py:9871-9933).
//
// This file contains NO step handlers. Every branch delegates to a sibling
// cluster that was ported separately (coreauth.go, cloudflare.go, emailotp.go,
// phone.go, aboutyou.go, teamsso.go); what lives here is purely the 600-second
// polling loop, the branch ORDER and the flag bookkeeping that routes between
// those handlers.
//
// Three properties of the original are load-bearing and are preserved verbatim:
//
//   - Branch order. The SPA can satisfy several probes at once (the about-you
//     page renders inputs that also match the OTP selectors, the phone page
//     matches the generic continue ladder, ...). The first matching branch wins,
//     so reordering silently changes which handler runs.
//   - Poll cadence. The 2s/1s sleeps and the sub-second visibility probes inside
//     the handlers are tuned to the SPA's re-render timing; a tighter loop races
//     React and reads a half-mounted DOM.
//   - Flag bookkeeping. email_code_submitted / about_you_submitted /
//     about_you_submitted_at / about_you_submit_retry_at are selectively reset on
//     nearly every branch. Resetting too much re-submits forever; resetting too
//     little gives up while the page is still progressing.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// RegisterLoopDeadline mirrors `deadline = time.time() + 600` — the outer budget
// of BOTH state machines (app.py:9779 and app.py:9884).
const RegisterLoopDeadline = 600 * time.Second

// RegisterPollInterval mirrors the `time.sleep(2)` that ends an idle iteration
// and gates the "already submitted, wait for the SPA" branches (app.py:9801,
// 9851, 9867, 9899, 9911, 9922, 9931).
const RegisterPollInterval = 2 * time.Second

// RegisterCFRecheckDelay mirrors the `time.sleep(1)` after a Cloudflare pass
// (app.py:9790, 9894) and the 1s tick of the about-you wait branch
// (app.py:9842).
const RegisterCFRecheckDelay = 1 * time.Second

// RegisterAboutYouRetryThrottle mirrors the TWO independent `>= 10` second
// guards of the about-you re-click branch (app.py:9831): at least 10s since the
// form was submitted AND at least 10s since the last re-click. Both must pass,
// which is what stops the loop from hammering the submit button while the SPA is
// still navigating.
const RegisterAboutYouRetryThrottle = 10 * time.Second

// RegisterOTPLookback mirrors the `- 120` of `otp_min_timestamp = time.time() - 120`
// (app.py:9773, 9857, 9875, 9928): the mail layer is told to accept codes from
// two minutes before the form was reached, because OpenAI frequently sends the
// mail before the SPA finishes rendering the OTP screen.
const RegisterOTPLookback = 120 * time.Second

// registerOTPFloor is `time.time() - 120` (app.py:9773): unix-epoch FLOAT
// seconds, which is exactly the unit mail.Reader.WaitForCode's minTimestamp
// expects. This is WALL-CLOCK on purpose — it is compared against mail
// headers, not used as a local deadline (those use time.Since below).
func registerOTPFloor() float64 {
	return float64(time.Now().UnixNano())/1e9 - RegisterOTPLookback.Seconds()
}

// RegisterFlow drives _register (app.py:9769-9869) and _login_existing_account
// (app.py:9871-9933). It mirrors the `self` + (page, context) trio those two
// methods close over: the live tab, the owning browser, the account, and the
// six sibling clusters that own the individual steps.
//
// Every collaborator is a concrete type from this package — the state machine
// deliberately does NOT re-implement any step:
//
//	Auth     -> _create_openai_signin_url / _create_login_url / _detect_route_error /
//	            _retry_route_error / _click_continue / _fill_email_if_visible
//	CF       -> _is_cloudflare_challenge_page / _try_pass_cloudflare
//	Team     -> _select_team_workspace_if_visible
//	OTP      -> _has_visible_password / _fill_password_step / _has_otp_input /
//	            _submit_email_code
//	AboutYou -> _has_about_you_form / _fill_about_you /
//	            _about_you_current_values_ok / _click_finish_creating_account
//	Phone    -> _handle_phone_continue_if_visible
//
// Phone may be nil: that is the `if not self.phone_provider: return False`
// early-out of app.py:10210, i.e. "this run has no phone pool". Every other
// collaborator is required and is checked up front (see requireCollaborators)
// rather than silently degrading a branch to "not present", which would let the
// loop spin until the 600s budget expires.
type RegisterFlow struct {
	// Browser owns the cookie jar and every tab; the session probe is
	// browser-wide because _has_chatgpt_session (app.py:10062) scans ALL pages
	// of the context, not just the active one.
	Browser *browser.Browser
	// Page is the tab the auth flow runs in (Python's `page` argument).
	Page *browser.Page
	// Account supplies the address logged at entry (Python's self.account).
	Account *models.MailAccount
	// Log mirrors self.log; may be nil.
	Log func(string)

	Auth     *AuthURLBuilder
	CF       *CFSolver
	Team     *TeamSSOFlow
	OTP      *OTPHandler
	AboutYou *AboutYouFiller
	Phone    *PhoneHandler
}

// NewRegisterFlow builds a RegisterFlow over an already-wired set of cluster
// handlers. It mirrors the point in the Python run() where `self` already holds
// the browser, page and account and the step methods are simply available
// (app.py:8891, 8939, 9062, 9110).
func NewRegisterFlow(b *browser.Browser, p *browser.Page, account *models.MailAccount, log func(string)) *RegisterFlow {
	return &RegisterFlow{Browser: b, Page: p, Account: account, Log: log}
}

func (f *RegisterFlow) logf(format string, args ...any) {
	if f == nil || f.Log == nil {
		return
	}
	if len(args) == 0 {
		f.Log(format)
		return
	}
	f.Log(fmt.Sprintf(format, args...))
}

func (f *RegisterFlow) email() string {
	if f == nil || f.Account == nil {
		return ""
	}
	return f.Account.Email
}

// pageURL is `url = page.url` (app.py:9786, 9889), read ONCE per iteration and
// reused by every URL test in that iteration — re-reading mid-iteration could
// mix two different pages into one decision.
//
// browser.Page.URL() yields "" when the CDP read fails, where Python's page.url
// would have raised out of the loop; "" simply matches no route branch and the
// iteration falls through to the idle sleep, which is the safer degradation.
func (f *RegisterFlow) pageURL() string {
	if f == nil || f.Page == nil {
		return ""
	}
	return f.Page.URL()
}

// hasChatGPTSession is _has_chatgpt_session (app.py:10062-10085). The ported
// probe lives on browser.Browser because it must fetch /api/auth/session from
// EVERY chatgpt.com tab, not only the active one.
func (f *RegisterFlow) hasChatGPTSession() bool {
	if f == nil || f.Browser == nil {
		return false
	}
	return f.Browser.HasChatGPTSession()
}

// handlePhoneContinueIfVisible is _handle_phone_continue_if_visible
// (app.py:10209-10276). A nil PhoneHandler reproduces the provider-less
// early-out at app.py:10210-10211.
func (f *RegisterFlow) handlePhoneContinueIfVisible() (bool, error) {
	if f == nil || f.Phone == nil {
		return false, nil
	}
	return f.Phone.HandlePhoneContinueIfVisible()
}

// requireCollaborators fails fast when a cluster handler the loop depends on was
// not wired. Python could not hit this case (the steps were methods on self), so
// there is no Chinese counterpart string — it is a Go-only assembly guard, and
// failing loudly is preferable to a branch that can never fire and a run that
// dies 600 seconds later with a misleading timeout.
//
// needProfileSteps distinguishes the two machines: _register drives the
// about-you form, _login_existing_account treats those pages as terminal errors
// and never touches them.
func (f *RegisterFlow) requireCollaborators(needProfileSteps bool) error {
	var missing []string
	if f.Browser == nil {
		missing = append(missing, "Browser")
	}
	if f.Page == nil {
		missing = append(missing, "Page")
	}
	if f.Auth == nil {
		missing = append(missing, "Auth")
	}
	if f.CF == nil {
		missing = append(missing, "CF")
	}
	if f.Team == nil {
		missing = append(missing, "Team")
	}
	if f.OTP == nil {
		missing = append(missing, "OTP")
	}
	if needProfileSteps && f.AboutYou == nil {
		missing = append(missing, "AboutYou")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("RegisterFlow 未装配完整: 缺少 %s", strings.Join(missing, ", "))
}

// registerStepFlags is the loop-carried bookkeeping of _register
// (app.py:9780-9783). It is a struct only so the two reset SHAPES are named and
// auditable; the semantics are byte-for-byte the Python assignments.
type registerStepFlags struct {
	// emailCodeSubmitted mirrors email_code_submitted (app.py:9780): the OTP for
	// the CURRENT email was already typed in, so the OTP branch must wait rather
	// than burn a second code.
	emailCodeSubmitted bool
	// aboutYouSubmitted mirrors about_you_submitted (app.py:9781).
	aboutYouSubmitted bool
	// aboutYouSubmittedAt mirrors about_you_submitted_at (app.py:9782). The zero
	// Time stands in for Python's 0.0 epoch: both mean "infinitely long ago", so
	// the first throttle check always passes.
	aboutYouSubmittedAt time.Time
	// aboutYouSubmitRetryAt mirrors about_you_submit_retry_at (app.py:9783).
	aboutYouSubmitRetryAt time.Time
}

// resetAll is the four-line reset repeated by the phone-route, phone,
// password and email-fill branches (app.py:9808-9811, 9815-9818, 9822-9825,
// 9858-9861): the page moved to a genuinely new step, so nothing that was
// submitted for the previous step is still pending.
func (s *registerStepFlags) resetAll() {
	s.emailCodeSubmitted = false
	s.resetAboutYou()
}

// resetAboutYou is the three-line reset used where email_code_submitted must
// SURVIVE: the "values no longer ok -> re-fill" escape (app.py:9838-9840) and
// the fall-through at the bottom of the loop (app.py:9864-9866).
func (s *registerStepFlags) resetAboutYou() {
	s.aboutYouSubmitted = false
	s.aboutYouSubmittedAt = time.Time{}
	s.aboutYouSubmitRetryAt = time.Time{}
}

// throttlePassed is `now - <timestamp> >= 10` (app.py:9831) with Python's 0.0
// sentinel mapped onto the zero Time. Monotonic: now.Sub(t) on two time.Now()
// values is wall-clock-jump immune.
func throttlePassed(now, since time.Time, window time.Duration) bool {
	if since.IsZero() {
		return true
	}
	return now.Sub(since) >= window
}

// registerSleep is `time.sleep(n)` made cancellable. The DURATION is unchanged —
// only the ability to abort early is added, which Python did not have. A
// cancelled/expired context returns its error so the caller can leave the loop.
func registerSleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// loopExitError distinguishes the two ways the polling loop can end. Python only
// had one (the 600s budget -> TimeoutError); a caller-driven cancellation is
// Go-only and is surfaced as the raw context error so callers can use
// errors.Is(err, context.Canceled).
func loopExitError(parent context.Context, timeoutMsg string) error {
	if err := parent.Err(); err != nil {
		return err
	}
	return errors.New(timeoutMsg)
}

// ---------------------------------------------------------------------------
// _register (app.py:9769-9869)
// ---------------------------------------------------------------------------

// Register mirrors _register (app.py:9769-9869): open ChatGPT, mint the signup
// URL, clear the first-screen Cloudflare challenge, then poll the auth SPA for
// up to 600s, routing each rendered screen to its handler until a real ChatGPT
// session exists.
//
// Branch order inside the loop is verbatim Python and MUST NOT be reordered:
//
//	Cloudflare challenge -> route error -> Team workspace picker -> session probe
//	-> add-phone/phone-verification route -> phone form anywhere
//	-> password page -> about-you page -> email-verification page -> email form
//
// Termination is a VALID SESSION (hasChatGPTSession), never a URL — the SPA
// reaches chatgpt.com before the session cookie is usable.
//
// ctx only adds cancellability; the 600s budget, the sleeps and the per-probe
// timeouts are unchanged.
func (f *RegisterFlow) Register(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.requireCollaborators(true); err != nil {
		return err
	}

	f.logf("[认证] 开始注册或登录: %s", f.email())
	if err := ctx.Err(); err != nil {
		return err
	}
	// page.goto(CHATGPT_BASE_URL, wait_until="domcontentloaded", timeout=60000)
	// (app.py:9771). Python let the Playwright error propagate untouched; the
	// wrapper adds a locator so the two navigations stay distinguishable.
	if err := f.Page.Navigate(openai.ChatGPTBaseURL, 60*time.Second); err != nil {
		return fmt.Errorf("打开 ChatGPT 首页失败: %w", err)
	}
	signinURL, err := f.Auth.CreateOpenAISigninURL()
	if err != nil {
		return err
	}
	// Seeded BEFORE the navigation (app.py:9773) — the OTP mail can already be in
	// flight by the time the page renders.
	otpMinTimestamp := registerOTPFloor()
	if err := f.Page.Navigate(signinURL, 90*time.Second); err != nil {
		return fmt.Errorf("打开 OpenAI 认证页失败: %w", err)
	}
	f.logf("[认证] 已打开 OpenAI 认证页；如出现人机验证将自动尝试过盾，失败后再请手动完成")
	if !f.CF.TryPassCloudflare(f.Page, true, "注册首屏") {
		return errors.New("注册首屏 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
	}

	// deadline = time.time() + 600 (app.py:9779), expressed as a context deadline
	// derived from the caller's so either can end the loop.
	loopCtx, cancel := context.WithTimeout(ctx, RegisterLoopDeadline)
	defer cancel()

	flags := registerStepFlags{}
	routeErrorRetries := 0

	for loopCtx.Err() == nil {
		url := f.pageURL()

		// --- Cloudflare (app.py:9787-9791) ---
		if f.CF.IsCloudflareChallengePage(f.Page) {
			if !f.CF.TryPassCloudflare(f.Page, true, "注册流程") {
				return errors.New("注册流程 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
			}
			if err := registerSleep(loopCtx, RegisterCFRecheckDelay); err != nil {
				break
			}
			continue
		}

		// --- Route error (app.py:9792-9799) ---
		if errorText := f.Auth.DetectRouteError(); errorText != "" {
			if routeErrorRetries < RouteErrorMaxRetries && f.Auth.RetryRouteError() {
				routeErrorRetries++
				// Python hardcodes the "/3" denominator.
				f.logf("OpenAI 页面超时，已点击重试 (%d/3)", routeErrorRetries)
				if err := registerSleep(loopCtx, RouteErrorRetryDelay); err != nil {
					break
				}
				continue
			}
			return fmt.Errorf("OpenAI 页面错误，通常是代理/风控导致接口超时: %s", errorText)
		}

		// --- Team workspace chooser (app.py:9800-9802) ---
		if f.Team.SelectTeamWorkspaceIfVisible() {
			if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
				break
			}
			continue
		}

		// --- Success (app.py:9803-9805) ---
		if f.hasChatGPTSession() {
			f.logf("[认证] 认证完成，已获得 ChatGPT 会话")
			return nil
		}

		// --- Phone verification ROUTE (app.py:9806-9813) ---
		// On these two routes the phone step is MANDATORY: if the handler cannot
		// take it over, the run is dead (no other branch can advance the page).
		if strings.Contains(url, "add-phone") || strings.Contains(url, "phone-verification") {
			handled, phoneErr := f.handlePhoneContinueIfVisible()
			if phoneErr != nil {
				// Python's _handle_phone_continue_if_visible re-raised pre-submit
				// failures out of _register; propagate identically.
				return phoneErr
			}
			if handled {
				flags.resetAll()
				continue
			}
			return errors.New("当前账号触发手机验证，但未找到可自动处理的电话验证页面")
		}

		// --- Phone form on ANY other route (app.py:9814-9819) ---
		// Same handler, but here "not handled" is merely "no phone form here" and
		// the loop keeps going.
		handledPhone, phoneErr := f.handlePhoneContinueIfVisible()
		if phoneErr != nil {
			return phoneErr
		}
		if handledPhone {
			flags.resetAll()
			continue
		}

		// --- Password step (app.py:9820-9826) ---
		if strings.Contains(url, "password") && f.OTP.HasVisiblePassword() {
			if err := f.OTP.FillPasswordStep(); err != nil {
				return err
			}
			flags.resetAll()
			continue
		}

		// --- About-you profile form (app.py:9827-9848) ---
		if strings.Contains(url, "about-you") || f.AboutYou.HasAboutYouForm() {
			// app.py:9828 — only this one flag is cleared here; the about-you
			// timestamps must survive so the throttles below still work.
			flags.emailCodeSubmitted = false

			if flags.aboutYouSubmitted {
				// A single `now` for BOTH comparisons and for the write-back,
				// exactly as app.py:9830.
				now := time.Now()
				if throttlePassed(now, flags.aboutYouSubmittedAt, RegisterAboutYouRetryThrottle) &&
					throttlePassed(now, flags.aboutYouSubmitRetryAt, RegisterAboutYouRetryThrottle) {
					if f.AboutYou.AboutYouCurrentValuesOK() {
						// The form is still filled correctly but the SPA never
						// navigated -> re-press submit (app.py:9833-9835).
						if f.AboutYou.ClickFinishCreatingAccount() || f.Auth.ClickContinue() {
							flags.aboutYouSubmitRetryAt = now
							f.logf("基础资料已提交但页面未跳转，已重新点击提交按钮")
						}
					} else {
						// React reverted / cleared the inputs -> drop back to a full
						// re-fill on the NEXT iteration (app.py:9836-9841).
						f.logf("基础资料提交后输入值异常，将重新填写")
						flags.resetAboutYou()
						continue
					}
				}
				// Reached both when the throttle blocked and after a re-click
				// (app.py:9842-9843).
				if err := registerSleep(loopCtx, RegisterCFRecheckDelay); err != nil {
					break
				}
				continue
			}

			if err := f.AboutYou.FillAboutYou(); err != nil {
				return err
			}
			flags.aboutYouSubmitted = true
			flags.aboutYouSubmittedAt = time.Now()
			// NOT resetAboutYou(): only the retry clock is cleared (app.py:9847).
			flags.aboutYouSubmitRetryAt = time.Time{}
			continue
		}

		// --- Email verification code (app.py:9849-9855) ---
		if strings.Contains(url, "email-verification") || f.OTP.HasOTPInput() {
			if flags.emailCodeSubmitted {
				if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
					break
				}
				continue
			}
			if err := f.OTP.SubmitEmailCode(otpMinTimestamp); err != nil {
				return err
			}
			flags.emailCodeSubmitted = true
			continue
		}

		// --- Email form (app.py:9856-9862) ---
		if f.Auth.FillEmailIfVisible() {
			// A FRESH email submission restarts the OTP window; without this
			// re-seed the reader would happily return the previous screen's code.
			otpMinTimestamp = registerOTPFloor()
			flags.resetAll()
			continue
		}

		// --- Idle (app.py:9863-9867) ---
		// Nothing recognizable on screen: if an about-you submission was pending,
		// it is no longer on the about-you page, so forget it (but KEEP
		// email_code_submitted).
		if flags.aboutYouSubmitted {
			flags.resetAboutYou()
		}
		if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
			break
		}
	}

	// raise TimeoutError(...) (app.py:9869)
	return loopExitError(ctx, "认证流程超时；如果浏览器停在人机验证或异常页面，请手动处理后重试")
}

// ---------------------------------------------------------------------------
// _login_existing_account (app.py:9871-9933)
// ---------------------------------------------------------------------------

// LoginExisting mirrors _login_existing_account (app.py:9871-9933): log an
// EXISTING account back in (screen_hint=login) so a fresh payment long-link can
// be minted.
//
// It is deliberately NOT Register with a flag — the two diverge in three ways
// and collapsing them would break the "登录并保留" (login and keep the browser)
// mode:
//
//  1. autoCloudflare == false disables auto-solving entirely. The loop then only
//     NOTIFIES the user once (the manual_cf_notified latch, app.py:9887/9896-9898)
//     and keeps polling at 2s while they solve it by hand; the latch is cleared
//     the moment the page is no longer a challenge (app.py:9901) so a LATER
//     challenge notifies again.
//  2. The phone and password pages are TERMINAL errors (app.py:9916-9919) instead
//     of handled steps — this entry point must never spend a phone number or
//     mutate the account's password.
//  3. There is no about-you branch at all, so no about-you bookkeeping exists.
func (f *RegisterFlow) LoginExisting(ctx context.Context, autoCloudflare bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.requireCollaborators(false); err != nil {
		return err
	}

	f.logf("开始登录已有账号: %s", f.email())
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Page.Navigate(openai.ChatGPTBaseURL, 60*time.Second); err != nil {
		return fmt.Errorf("打开 ChatGPT 首页失败: %w", err)
	}
	signinURL, err := f.Auth.CreateLoginURL()
	if err != nil {
		return err
	}
	otpMinTimestamp := registerOTPFloor()
	if err := f.Page.Navigate(signinURL, 90*time.Second); err != nil {
		return fmt.Errorf("打开 OpenAI 登录页失败: %w", err)
	}
	if autoCloudflare {
		f.logf("已打开 OpenAI 登录页；如出现人机验证将自动尝试过盾，失败后再请手动完成")
		if !f.CF.TryPassCloudflare(f.Page, true, "登录首屏") {
			return errors.New("登录首屏 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
		}
	} else {
		f.logf("已打开 OpenAI 登录页；登录并保留已关闭自动过盾，如出现 Cloudflare 请在浏览器中手动完成")
	}

	loopCtx, cancel := context.WithTimeout(ctx, RegisterLoopDeadline)
	defer cancel()

	emailCodeSubmitted := false
	routeErrorRetries := 0
	manualCFNotified := false

	for loopCtx.Err() == nil {
		url := f.pageURL()

		// --- Cloudflare (app.py:9890-9900) ---
		if f.CF.IsCloudflareChallengePage(f.Page) {
			if autoCloudflare {
				if !f.CF.TryPassCloudflare(f.Page, true, "登录流程") {
					return errors.New("登录流程 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
				}
				if err := registerSleep(loopCtx, RegisterCFRecheckDelay); err != nil {
					break
				}
			} else {
				// One notice per challenge, then just keep waiting for the human.
				if !manualCFNotified {
					f.logf("检测到 Cloudflare 挑战：自动过盾已关闭，请在保留浏览器中手动完成")
					manualCFNotified = true
				}
				if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
					break
				}
			}
			continue
		}
		// Cleared on every non-challenge iteration (app.py:9901) so a second,
		// later challenge is announced again.
		manualCFNotified = false

		// --- Route error (app.py:9902-9909) ---
		if errorText := f.Auth.DetectRouteError(); errorText != "" {
			if routeErrorRetries < RouteErrorMaxRetries && f.Auth.RetryRouteError() {
				routeErrorRetries++
				f.logf("OpenAI 登录页超时，已点击重试 (%d/3)", routeErrorRetries)
				if err := registerSleep(loopCtx, RouteErrorRetryDelay); err != nil {
					break
				}
				continue
			}
			return fmt.Errorf("OpenAI 登录页错误，通常是代理/风控导致接口超时: %s", errorText)
		}

		// --- Team workspace chooser (app.py:9910-9912) ---
		if f.Team.SelectTeamWorkspaceIfVisible() {
			if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
				break
			}
			continue
		}

		// --- Success (app.py:9913-9915) ---
		if f.hasChatGPTSession() {
			f.logf("登录完成，已获得 ChatGPT 会话")
			return nil
		}

		// --- Terminal: phone verification (app.py:9916-9917) ---
		if strings.Contains(url, "add-phone") || strings.Contains(url, "phone-verification") {
			return errors.New("当前账号触发手机验证，重新获取长链接已停止")
		}

		// --- Terminal: password login (app.py:9918-9919) ---
		if strings.Contains(url, "password") && f.OTP.HasVisiblePassword() {
			return errors.New("该账号进入密码登录页，当前只支持邮箱验证码重新获取长链接")
		}

		// --- Email verification code (app.py:9920-9926) ---
		if strings.Contains(url, "email-verification") || f.OTP.HasOTPInput() {
			if emailCodeSubmitted {
				if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
					break
				}
				continue
			}
			if err := f.OTP.SubmitEmailCode(otpMinTimestamp); err != nil {
				return err
			}
			emailCodeSubmitted = true
			continue
		}

		// --- Email form (app.py:9927-9930) ---
		if f.Auth.FillEmailIfVisible() {
			otpMinTimestamp = registerOTPFloor()
			emailCodeSubmitted = false
			continue
		}

		// --- Idle (app.py:9931) ---
		if err := registerSleep(loopCtx, RegisterPollInterval); err != nil {
			break
		}
	}

	// raise TimeoutError(...) (app.py:9933)
	return loopExitError(ctx, "重新获取长链接登录流程超时；如果浏览器停在人机验证或异常页面，请手动处理后重试")
}
