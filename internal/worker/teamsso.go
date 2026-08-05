// teamsso.go ports the Team-SSO + browser-OAuth refresh-token cluster of
// OpenAIRegisterPayLinkWorker (app.py:9333-9768) to go-rod.
//
// The cluster drives two long-running SPA loops:
//   - RegisterTeamSSO      (_register_team_sso, app.py:9333-9392)
//   - AuthorizeRTFromBrowser (_authorize_rt_from_browser, app.py:9664-9697)
//
// plus the page probes/clickers they share. Timings, retry caps and every
// user-facing Chinese log string are kept exactly as in Python: the caps are
// anti-loop guards and the cadences are tuned to the OpenAI SPA.

package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// ---------------------------------------------------------------------------
// tunables (all mirrored 1:1 from app.py — do not "round" them)
// ---------------------------------------------------------------------------

const (
	// teamSSOLoopDeadline is `deadline = time.time() + 600` (app.py:9341).
	teamSSOLoopDeadline = 600 * time.Second
	// teamOAuthDeadline is `while time.time() - started < 180` (app.py:9672).
	teamOAuthDeadline = 180 * time.Second
	// teamSSOTextProbeTimeout is the inner_text/title probe timeout=1000 used by
	// _refresh_bad_gateway_if_visible / _page_has_text / _team_onboarding_pending
	// / _select_team_workspace_if_visible (app.py:9422-9451).
	teamSSOTextProbeTimeout = 1000 * time.Millisecond
	// teamSSONoticeInterval is the 15s log throttle used by every wait loop.
	teamSSONoticeInterval = 15 * time.Second
	// teamSSOBadGatewayCap is `if refresh_count >= 8` (app.py:9432).
	teamSSOBadGatewayCap = 8
	// teamSSORouteErrorCap is `if route_error_retries < 3` (app.py:9359).
	teamSSORouteErrorCap = 3
	// teamSSOWorkspaceClickCap is `if workspace_clicks < 5` (app.py:9381).
	teamSSOWorkspaceClickCap = 5
	// teamSSOWorkspaceClickDelay is page.mouse.click(..., delay=80) (app.py:9539).
	teamSSOWorkspaceClickDelay = 80 * time.Millisecond
	// teamSSOWorkspaceSettle is page.wait_for_timeout(800) (app.py:9542).
	teamSSOWorkspaceSettle = 800 * time.Millisecond
)

// teamSSOWaitProgressTexts is the early-exit text set of _wait_team_sso_progress
// (app.py:9411). CASE-SENSITIVE plain substring match — unlike the regex probes
// in this file, which are case-insensitive. Do not swap the two.
var teamSSOWaitProgressTexts = []string{
	"批准登录", "Approve login", "Approve sign-in", "Verify it's you", "验证是您本人", "sign-in-consent", "callback",
}

// EVERY `\s` below is spelled pyWS, not `\s`. All three regexes are applied to
// page.title() / document.body.innerText, and rendered OpenAI pages routinely
// separate words with NBSP (U+00A0) or, in CJK layouts, U+3000. RE2's `\s` is
// [\t\n\f\r ] and matches neither, while Python's `\s` matches both — so the
// ASCII spelling made Go MISS what Python caught (verified: "Host Error",
// "HTTP　502", "espacio de trabajo", "借助 Codex").

// teamSSOBadGatewayRe mirrors app.py:9430. Missing a 502 here means the SSO
// loop never reloads and burns its whole 600s budget on a dead origin page.
var teamSSOBadGatewayRe = regexp.MustCompile(`(?i)Bad gateway|Error code 502|Host` + pyWS + `+Error|HTTP` + pyWS + `*502`)

// teamSSOWorkspaceGateRe mirrors the multilingual page-text gate of
// _select_team_workspace_if_visible (app.py:9454-9460). The pt/es/fr arms are
// the ONLY way those locales reach the workspace picker — nothing else in the
// alternation matches "espacio de trabajo".
var teamSSOWorkspaceGateRe = regexp.MustCompile(`(?i)采用何种方式|何种方式.*登录|工作空间|工作区|workspace|workspaces|sign in|` +
	`Escolha` + pyWS + `+um` + pyWS + `+espaço` + pyWS + `+de` + pyWS + `+trabalho|espaço` + pyWS + `+de` + pyWS + `+trabalho|` +
	`Choose` + pyWS + `+(?:a` + pyWS + `+)?workspace|select` + pyWS + `+(?:a` + pyWS + `+)?workspace|` +
	`espacio` + pyWS + `+de` + pyWS + `+trabajo|espace` + pyWS + `+de` + pyWS + `+travail`)

// teamSSOOnboardingPendingRe mirrors _team_onboarding_pending (app.py:9564).
var teamSSOOnboardingPendingRe = regexp.MustCompile(`(?i)What kind of work do you do|Select the option that best applies|你从事哪种工作|你从事什么工作|借助` + pyWS + `*Codex|更快完成工作|选择你的工作应用|启用这些应用|work apps|Maybe later|Skip|稍后再说|跳过`)

// ---------------------------------------------------------------------------
// flow object
// ---------------------------------------------------------------------------

// TeamSSOHooks are the callbacks this cluster needs from sibling worker
// clusters. They are injected instead of called directly so the Team-SSO port
// stays independent of the Cloudflare / core-register files.
type TeamSSOHooks struct {
	// CreateSigninURL mirrors _create_openai_signin_url (app.py:9969).
	CreateSigninURL func() (string, error)
	// TryPassCloudflare mirrors _try_pass_cloudflare(page, allow_manual=True,
	// reason=...) (app.py:10829).
	TryPassCloudflare func(reason string) bool
	// DetectRouteError mirrors _detect_route_error (app.py:9935).
	DetectRouteError func() string
	// RetryRouteError mirrors _retry_route_error (app.py:9945).
	RetryRouteError func() bool
	// FillEmailIfVisible mirrors _fill_email_if_visible (app.py:10195).
	FillEmailIfVisible func() bool
}

// TeamSSOFlow drives the Team SSO login and the in-browser OAuth authorization
// that yields the codex refresh token. It mirrors the `self`/`page`/`context`
// trio the Python methods close over (app.py:9333-9768).
type TeamSSOFlow struct {
	browser *browser.Browser
	page    *browser.Page
	account *models.MailAccount
	client  *tlsclient.Client
	log     func(string)

	// Hooks must be wired before RegisterTeamSSO; AuthorizeRTFromBrowser and the
	// individual probes work without them.
	Hooks TeamSSOHooks
}

// NewTeamSSOFlow builds a TeamSSOFlow. `client` is the TLS-impersonating HTTP
// client used for the code->token exchange (Python used
// context.request.post, app.py:9739); `log` may be nil.
func NewTeamSSOFlow(b *browser.Browser, p *browser.Page, account *models.MailAccount, client *tlsclient.Client, log func(string)) *TeamSSOFlow {
	return &TeamSSOFlow{browser: b, page: p, account: account, client: client, log: log}
}

func (f *TeamSSOFlow) logf(format string, args ...any) {
	if f.log == nil {
		return
	}
	f.log(fmt.Sprintf(format, args...))
}

func (f *TeamSSOFlow) accountEmail() string {
	if f.account == nil {
		return ""
	}
	return f.account.Email
}

// requireHooks fails fast (instead of silently skipping a step) when the
// cross-cluster callbacks RegisterTeamSSO depends on were not wired.
func (f *TeamSSOFlow) requireHooks() error {
	var missing []string
	if f.Hooks.CreateSigninURL == nil {
		missing = append(missing, "CreateSigninURL")
	}
	if f.Hooks.TryPassCloudflare == nil {
		missing = append(missing, "TryPassCloudflare")
	}
	if f.Hooks.DetectRouteError == nil {
		missing = append(missing, "DetectRouteError")
	}
	if f.Hooks.RetryRouteError == nil {
		missing = append(missing, "RetryRouteError")
	}
	if f.Hooks.FillEmailIfVisible == nil {
		missing = append(missing, "FillEmailIfVisible")
	}
	if len(missing) > 0 {
		return fmt.Errorf("Team SSO 流程未接线: 缺少 %s", strings.Join(missing, ", "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// RegisterTeamSSO — _register_team_sso (app.py:9333-9392)
// ---------------------------------------------------------------------------

// RegisterTeamSSO mirrors _register_team_sso (app.py:9333-9392): open the
// ChatGPT signin URL, clear Cloudflare, then poll the SPA for up to 600s
// handling 502s, route errors, onboarding, the SSO approval screen, the
// workspace picker and the email form until a ChatGPT session exists.
//
// The three retry counters are anti-loop guards and accumulate ACROSS loop
// iterations exactly as in Python: bad-gateway refreshes cap at 8, route-error
// retries at 3, workspace clicks at 5, and the approve click is a one-shot latch.
func (f *TeamSSOFlow) RegisterTeamSSO() error {
	if err := f.requireHooks(); err != nil {
		return err
	}
	f.logf("[认证] 开始 Team SSO 认证: %s", f.accountEmail())
	if err := f.page.Navigate(openai.ChatGPTBaseURL, 60*time.Second); err != nil {
		return fmt.Errorf("Team SSO 打开 ChatGPT 首页失败: %w", err)
	}
	signinURL, err := f.Hooks.CreateSigninURL()
	if err != nil {
		return err
	}
	if err := f.page.Navigate(signinURL, 90*time.Second); err != nil {
		return fmt.Errorf("Team SSO 打开认证页失败: %w", err)
	}
	f.log2("[认证] 已打开 Team SSO 页面，准备填写随机邮箱")
	if !f.Hooks.TryPassCloudflare("Team SSO 首屏") {
		return errors.New("Team SSO 首屏 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
	}

	deadline := time.Now().Add(teamSSOLoopDeadline)
	routeErrorRetries := 0
	workspaceClicks := 0
	approveClicked := false
	var lastWaitNotice time.Time // Python starts at 0.0 -> first notice fires immediately
	badGatewayRefreshes := 0

	for time.Now().Before(deadline) {
		if f.page.IsCloudflareChallengePage() {
			if !f.Hooks.TryPassCloudflare("Team SSO") {
				return errors.New("Team SSO Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
			}
			time.Sleep(1 * time.Second)
			continue
		}
		refreshed, count, err := f.RefreshBadGatewayIfVisible(badGatewayRefreshes, "Team SSO")
		badGatewayRefreshes = count
		if err != nil {
			return err
		}
		if refreshed {
			time.Sleep(3 * time.Second)
			continue
		}
		if errorText := f.Hooks.DetectRouteError(); errorText != "" {
			if routeErrorRetries < teamSSORouteErrorCap && f.Hooks.RetryRouteError() {
				routeErrorRetries++
				f.logf("Team SSO 页面超时，已点击重试 (%d/3)", routeErrorRetries)
				time.Sleep(5 * time.Second)
				continue
			}
			return fmt.Errorf("Team SSO 页面错误，通常是代理/风控导致接口超时: %s", errorText)
		}
		if f.CompleteTeamOnboardingIfVisible() {
			time.Sleep(2 * time.Second)
			continue
		}
		if f.hasChatGPTSession() {
			if f.TeamOnboardingPending() {
				if time.Since(lastWaitNotice) >= teamSSONoticeInterval {
					f.log2("Team SSO 已登录，继续等待 onboarding 完成")
					lastWaitNotice = time.Now()
				}
				time.Sleep(2 * time.Second)
				continue
			}
			f.log2("[认证] Team SSO 认证完成，已获得 ChatGPT 会话")
			return nil
		}
		if !approveClicked && f.ApproveSSOLoginIfVisible() {
			approveClicked = true
			if err := f.WaitTeamSSOProgress("批准登录后跳转", 90*time.Second); err != nil {
				return err
			}
			continue
		}
		if workspaceClicks < teamSSOWorkspaceClickCap && f.SelectTeamWorkspaceIfVisible() {
			workspaceClicks++
			if err := f.WaitTeamSSOProgress("工作空间选择后跳转", 90*time.Second); err != nil {
				return err
			}
			continue
		}
		if f.Hooks.FillEmailIfVisible() {
			if err := f.WaitTeamSSOProgress("提交 Team 邮箱后跳转", 60*time.Second); err != nil {
				return err
			}
			continue
		}
		if time.Since(lastWaitNotice) >= teamSSONoticeInterval {
			f.logf("Team SSO 等待页面推进中: %s", teamSSOTrunc(f.page.URL(), 100))
			lastWaitNotice = time.Now()
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("Team SSO 认证流程超时；如果浏览器停在人机验证或异常页面，请手动处理后重试")
}

// WaitTeamSSOProgress mirrors _wait_team_sso_progress (app.py:9394-9418): wait
// up to `timeout` for the SPA to move on (session appears, URL changes, or a
// known approval/callback text shows up), logging progress every 15s. It never
// fails on timeout — it just logs and lets the caller keep polling. It DOES
// return an error when the bad-gateway refresh cap trips (Python raised there).
//
// The bad-gateway counter is LOCAL to each call (reset per invocation), unlike
// the caller's accumulating counter — that asymmetry is intentional in Python.
func (f *TeamSSOFlow) WaitTeamSSOProgress(label string, timeout time.Duration) error {
	started := time.Now()
	startURL := f.page.URL()
	var lastNotice time.Time
	badGatewayRefreshes := 0
	for time.Since(started) < timeout {
		refreshed, count, err := f.RefreshBadGatewayIfVisible(badGatewayRefreshes, label)
		badGatewayRefreshes = count
		if err != nil {
			return err
		}
		if refreshed {
			startURL = f.page.URL()
			time.Sleep(3 * time.Second)
			continue
		}
		if f.hasChatGPTSession() {
			return nil
		}
		currentURL := f.page.URL()
		// Page.URL() returns "" when the CDP read fails; Python's page.url would
		// have kept the previous value, so ignore the blank read instead of
		// reporting a bogus navigation.
		if currentURL != "" && currentURL != startURL {
			f.logf("%s: 已跳转到 %s", label, teamSSOTrunc(currentURL, 100))
			return nil
		}
		if f.PageHasText(teamSSOWaitProgressTexts) {
			return nil
		}
		if time.Since(lastNotice) >= teamSSONoticeInterval {
			remain := int(timeout.Seconds() - time.Since(started).Seconds())
			if remain < 0 {
				remain = 0
			}
			f.logf("%s: 仍在等待页面响应，剩余约 %ds", label, remain)
			lastNotice = time.Now()
		}
		time.Sleep(1 * time.Second)
	}
	f.logf("%s: 等待 %ds 未检测到跳转，继续轮询当前页面", label, int(timeout/time.Second))
	return nil
}

// RefreshBadGatewayIfVisible mirrors _refresh_bad_gateway_if_visible
// (app.py:9420-9440): when the title+body look like a Cloudflare/origin 502,
// reload the page and report (refreshed, newCount). At 8 accumulated refreshes
// it stops and returns an error instead (Python raised RuntimeError).
func (f *TeamSSOFlow) RefreshBadGatewayIfVisible(refreshCount int, label string) (bool, int, error) {
	title, _ := f.pageTitle()
	body, _ := f.bodyInnerText()
	text := title + "\n" + body
	if !teamSSOBadGatewayRe.MatchString(text) {
		return false, refreshCount, nil
	}
	if refreshCount >= teamSSOBadGatewayCap {
		return false, refreshCount, fmt.Errorf("%s: 连续检测到 Bad gateway/502，已刷新 %d 次仍未恢复", label, refreshCount)
	}
	refreshCount++
	f.logf("%s: 检测到 Bad gateway/502，刷新页面重试 (%d/8)", label, refreshCount)
	if err := f.reloadPage(60 * time.Second); err != nil {
		f.logf("%s: 502 页面刷新失败，继续等待: %s", label, teamSSOTrunc(err.Error(), 120))
	}
	return true, refreshCount, nil
}

// PageHasText mirrors _page_has_text (app.py:9442-9447): CASE-SENSITIVE plain
// substring match over document.body.innerText. Returns false when the body
// cannot be read.
func (f *TeamSSOFlow) PageHasText(texts []string) bool {
	body, ok := f.bodyInnerText()
	if !ok {
		return false
	}
	for _, text := range texts {
		if strings.Contains(body, text) {
			return true
		}
	}
	return false
}

// SelectTeamWorkspaceIfVisible mirrors _select_team_workspace_if_visible
// (app.py:9449-9556): on the workspace chooser, score every visible/enabled
// Team-ish row and click the best one with a REAL mouse press at x = 92% of the
// row width (right edge) with an 80ms press delay — element.click() is
// deliberately avoided because it lands on the legal/link text on the left and
// looks non-human. Waits 800ms for the SPA to settle afterwards.
func (f *TeamSSOFlow) SelectTeamWorkspaceIfVisible() bool {
	pageText, _ := f.bodyInnerText()
	if !teamSSOWorkspaceGateRe.MatchString(pageText) {
		return false
	}
	v, err := f.page.Rod.Eval(selectTeamWorkspaceJS)
	if err != nil || v == nil || v.Value.Nil() {
		// Python: `except: result = {}` -> falls through to the empty-candidate
		// branch and returns False.
		return false
	}
	// Python also accepted a bare `True` result (legacy shape).
	if v.Value.Bool() {
		f.log2("已自动选择 Team 工作空间")
		return true
	}
	if v.Value.Get("ok").Bool() {
		xv := v.Value.Get("x")
		yv := v.Value.Get("y")
		if xv.Nil() || yv.Nil() {
			f.logf("Team 工作空间鼠标点击失败: %s", "坐标缺失")
			return false
		}
		x, y := xv.Num(), yv.Num()
		if err := f.page.Rod.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
			f.logf("Team 工作空间鼠标点击失败: %s", teamSSOTrunc(err.Error(), 160))
			return false
		}
		if err := f.page.Rod.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
			f.logf("Team 工作空间鼠标点击失败: %s", teamSSOTrunc(err.Error(), 160))
			return false
		}
		time.Sleep(teamSSOWorkspaceClickDelay)
		if err := f.page.Rod.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
			f.logf("Team 工作空间鼠标点击失败: %s", teamSSOTrunc(err.Error(), 160))
			return false
		}
		f.logf("已自动点击 Team 工作空间: %s", teamSSOWorkspaceLabel(v.Value.Get("text").Str()))
		time.Sleep(teamSSOWorkspaceSettle)
		return true
	}
	var candidates []string
	for _, item := range v.Value.Get("candidates").Arr() {
		// `[str(item)[:80] for item in raw if str(item).strip()]` (app.py:9553):
		// the FILTER strips, the kept value does NOT.
		text := item.Str()
		if pyStrip(text) == "" {
			continue
		}
		candidates = append(candidates, teamSSOTrunc(text, 80))
	}
	if len(candidates) > 0 {
		shown := candidates
		if len(shown) > 8 {
			shown = shown[:8]
		}
		f.logf("未找到可点击 Team 工作空间，候选: %s", strings.Join(shown, ", "))
	}
	return false
}

// TeamOnboardingPending mirrors _team_onboarding_pending (app.py:9558-9567):
// case-insensitive regex over the body text for any of the post-login Team
// onboarding screens.
func (f *TeamSSOFlow) TeamOnboardingPending() bool {
	body, ok := f.bodyInnerText()
	if !ok {
		return false
	}
	return teamSSOOnboardingPendingRe.MatchString(body)
}

// CompleteTeamOnboardingIfVisible mirrors _complete_team_onboarding_if_visible
// (app.py:9569-9614): pick "Engineering" on the work-type screen, else click
// "Maybe later"/"Not now", else "Skip". Returns whether anything was clicked.
func (f *TeamSSOFlow) CompleteTeamOnboardingIfVisible() bool {
	result := ""
	if v, err := f.page.Rod.Eval(completeTeamOnboardingJS); err == nil && v != nil && !v.Value.Nil() {
		result = v.Value.Str()
	}
	if result == "" {
		return false
	}
	switch result {
	case "work":
		f.log2("已选择 Team onboarding 工作类型: Engineering")
	case "later":
		f.log2("已点击 Team onboarding 稍后再说")
	case "skip":
		f.log2("已点击 Team onboarding 跳过")
	default:
		f.log2("已处理 Team onboarding")
	}
	return true
}

// ApproveSSOLoginIfVisible mirrors _approve_sso_login_if_visible
// (app.py:9616-9644): click the multilingual "批准登录 / Approve sign-in"
// button while explicitly excluding the deny/"not my account" variants.
func (f *TeamSSOFlow) ApproveSSOLoginIfVisible() bool {
	v, err := f.page.Rod.Eval(approveSSOLoginJS)
	if err != nil || v == nil || !v.Value.Bool() {
		return false
	}
	f.log2("已点击批准登录")
	return true
}

// ---------------------------------------------------------------------------
// browser OAuth -> refresh token
// ---------------------------------------------------------------------------

// PrepareBrowserOAuthURL mirrors _prepare_browser_oauth_url
// (app.py:9646-9662): build the codex-CLI PKCE authorize URL and return it
// together with the code_verifier. Query parameter ORDER matches Python's
// urlencode(dict) insertion order (Go's url.Values.Encode would sort them).
func (f *TeamSSOFlow) PrepareBrowserOAuthURL() (string, string) {
	state := openai.RandomURLSafeString(24)
	codeVerifier := openai.RandomURLSafeString(64)
	pairs := [][2]string{
		{"client_id", openai.DefaultClientID},
		{"response_type", "code"},
		{"redirect_uri", openai.DefaultRedirectURI},
		{"scope", "openid email profile offline_access"},
		{"state", state},
		{"code_challenge", openai.PKCECodeChallenge(codeVerifier)},
		{"code_challenge_method", "S256"},
		{"prompt", "login"},
		{"id_token_add_organizations", "true"},
		{"codex_cli_simplified_flow", "true"},
		{"login_hint", f.accountEmail()},
	}
	parts := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
	}
	return openai.AuthBaseURL + "/oauth/authorize?" + strings.Join(parts, "&"), codeVerifier
}

// AuthorizeRTFromBrowser mirrors _authorize_rt_from_browser
// (app.py:9664-9697): run the PKCE authorize flow in the already-logged-in tab
// and exchange the resulting code for a refresh token, within 180s.
//
// CRITICAL: the flow ends when the browser tries to navigate to the DEAD
// localhost:1455 callback. That navigation ALWAYS fails (connection refused) —
// success is detected by the page URL having the redirect-URI prefix, never by
// navigation success. The initial Navigate error is therefore logged and
// swallowed instead of aborting (Playwright's goto could raise here too; see
// the report note).
func (f *TeamSSOFlow) AuthorizeRTFromBrowser() (openai.AuthRecord, error) {
	oauthURL, codeVerifier := f.PrepareBrowserOAuthURL()
	f.log2("在当前登录标签页发起 OAuth 授权获取 RT")
	if err := f.page.Navigate(oauthURL, 90*time.Second); err != nil {
		// A refused localhost:1455 callback surfaces as a navigation error here;
		// the URL is still readable, so keep polling instead of failing.
		f.logf("OAuth 授权页导航未正常结束，继续读取当前 URL: %s", teamSSOTrunc(err.Error(), 120))
	}
	started := time.Now()
	approveClicked := false
	var lastNotice time.Time
	badGatewayRefreshes := 0
	for time.Since(started) < teamOAuthDeadline {
		refreshed, count, err := f.RefreshBadGatewayIfVisible(badGatewayRefreshes, "Team OAuth")
		badGatewayRefreshes = count
		if err != nil {
			return openai.AuthRecord{}, err
		}
		if refreshed {
			time.Sleep(3 * time.Second)
			continue
		}
		currentURL := f.page.URL()
		if strings.HasPrefix(currentURL, openai.DefaultRedirectURI) {
			callback, err := f.ExtractOAuthCallbackFromURL(currentURL)
			if err != nil {
				return openai.AuthRecord{}, err
			}
			f.log2("已获取 OAuth 授权 code，交换 refresh_token")
			return f.ExchangeBrowserCodeForToken(callback.Code, codeVerifier)
		}
		if f.CompleteTeamOnboardingIfVisible() {
			time.Sleep(2 * time.Second)
			continue
		}
		if !approveClicked && f.ApproveSSOLoginIfVisible() {
			approveClicked = true
			if err := f.WaitTeamSSOProgress("OAuth 批准登录后跳转", 60*time.Second); err != nil {
				return openai.AuthRecord{}, err
			}
			continue
		}
		if f.ClickCodexConsentIfVisible() {
			if err := f.WaitTeamSSOProgress("OAuth 授权确认后跳转", 60*time.Second); err != nil {
				return openai.AuthRecord{}, err
			}
			continue
		}
		if time.Since(lastNotice) >= teamSSONoticeInterval {
			remain := int(teamOAuthDeadline.Seconds() - time.Since(started).Seconds())
			if remain < 0 {
				remain = 0
			}
			f.logf("Team OAuth 等待 callback 中，剩余约 %ds，当前 URL: %s", remain, teamSSOTrunc(currentURL, 100))
			lastNotice = time.Now()
		}
		time.Sleep(1 * time.Second)
	}
	return openai.AuthRecord{}, fmt.Errorf("Team OAuth 授权 180 秒内未到 callback，当前 URL: %s", f.page.URL())
}

// ClickCodexConsentIfVisible mirrors _click_codex_consent_if_visible
// (app.py:9699-9726): click the multilingual Authorize/Continue consent button
// on the codex OAuth screen.
func (f *TeamSSOFlow) ClickCodexConsentIfVisible() bool {
	v, err := f.page.Rod.Eval(clickCodexConsentJS)
	if err != nil || v == nil || !v.Value.Bool() {
		return false
	}
	f.log2("已点击授权/继续按钮")
	return true
}

// OAuthCallback is the dict returned by _extract_oauth_callback_from_url
// (app.py:9728-9734).
type OAuthCallback struct {
	CallbackURL string
	Code        string
}

// ExtractOAuthCallbackFromURL mirrors _extract_oauth_callback_from_url
// (app.py:9728-9734): pull ?code= out of the (dead) localhost callback URL and
// fail loudly when it is missing.
//
// url.Query() agrees with parse_qs on everything that matters here — first
// occurrence wins, '+' decodes to space, a blank value is "no code", and both
// split on '&' only (CPython dropped ';' in 3.9.2, Go in 1.17). The one
// divergence: Go's ParseQuery DROPS a pair with a malformed %-escape where
// unquote keeps it literally, so "?code=a%zz" is "missing code" here and
// "a%zz" in Python. An OAuth code is base64url, so it never contains '%', and
// both paths end in a failed exchange regardless.
func (f *TeamSSOFlow) ExtractOAuthCallbackFromURL(callbackURL string) (OAuthCallback, error) {
	code := ""
	if parsed, err := url.Parse(callbackURL); err == nil {
		code = parsed.Query().Get("code")
	}
	if code == "" {
		return OAuthCallback{}, fmt.Errorf("callback 中缺少 code: %s", callbackURL)
	}
	return OAuthCallback{CallbackURL: callbackURL, Code: code}, nil
}

// ExchangeBrowserCodeForToken mirrors _exchange_browser_code_for_token
// (app.py:9736-9760): POST the authorization code + PKCE verifier to each token
// endpoint IN ORDER (/api/oauth/oauth2/token then /oauth/token) and normalize
// the first 2xx response. NormalizeOpenAIAuthRecord fails loudly on a missing
// access/refresh/id token, account_id or exp — that error is returned as-is and
// does NOT fall through to the next endpoint (same as Python).
//
// Python issued this through Playwright's context.request (browser cookie jar);
// the Go port uses the TLS-impersonating client because a page-side fetch from
// the dead localhost:1455 origin would be blocked by CORS.
func (f *TeamSSOFlow) ExchangeBrowserCodeForToken(code, codeVerifier string) (openai.AuthRecord, error) {
	if f.client == nil {
		return openai.AuthRecord{}, errors.New("Team Code换Token失败: 未配置 HTTP 客户端")
	}
	headers := openai.OpenAIBrowserHeaders(map[string]string{
		"accept":         "application/json",
		"content-type":   "application/x-www-form-urlencoded",
		"sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors",
		"sec-fetch-site": "same-site",
	})
	form := [][2]string{
		{"grant_type", "authorization_code"},
		{"client_id", openai.DefaultClientID},
		{"code", code},
		{"redirect_uri", openai.DefaultRedirectURI},
		{"code_verifier", codeVerifier},
	}
	parts := make([]string, 0, len(form))
	for _, kv := range form {
		parts = append(parts, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
	}
	body := []byte(strings.Join(parts, "&"))

	lastError := ""
	for _, tokenURL := range openai.AuthOAuthTokenURLs {
		// Python issued this through context.request, which rides the browser's
		// cookie jar. Without cf_clearance / __cf_bm, Cloudflare can challenge the
		// POST and both endpoints fail with "Team Code换Token失败".
		reqHeaders := headers
		if parsed, perr := url.Parse(tokenURL); perr == nil {
			if cookie := browserCookieHeaderFor(f.browser, parsed); cookie != "" {
				reqHeaders = make(map[string]string, len(headers)+1)
				for k, v := range headers {
					reqHeaders[k] = v
				}
				reqHeaders["cookie"] = cookie
			}
		}
		status, respBody, err := f.client.DoSimple("POST", tokenURL, body, reqHeaders)
		if err != nil {
			// DELIBERATE DIVERGENCE: Python's context.request.post is not wrapped
			// (app.py:9739), so a transport failure aborts the whole method and the
			// SECOND endpoint is never tried. Trying it costs nothing (the request
			// never reached OpenAI) and recovers the common case where only one of
			// the two hosts is blocked by the proxy; the failure text is preserved
			// in last_error either way.
			lastError = fmt.Sprintf("endpoint=%s 请求失败 %s", tokenURL, teamSSOTrunc(err.Error(), 300))
			continue
		}
		if status >= 200 && status < 300 { // Playwright response.ok
			var payload map[string]any
			if err := json.Unmarshal(respBody, &payload); err != nil || payload == nil {
				// Python's response.json() would raise here too — fail loudly.
				return openai.AuthRecord{}, fmt.Errorf("Team Code换Token失败: endpoint=%s 响应不是 JSON: %s",
					tokenURL, teamSSOTrunc(string(respBody), 300))
			}
			return openai.NormalizeOpenAIAuthRecord(f.accountEmail(), payload)
		}
		lastError = fmt.Sprintf("endpoint=%s HTTP %d %s", tokenURL, status, teamSSOTrunc(string(respBody), 300))
	}
	return openai.AuthRecord{}, fmt.Errorf("Team Code换Token失败: %s", lastError)
}

// SessionPayloadFromRecord mirrors _session_payload_from_record
// (app.py:9762-9767): the minimal /api/auth/session-shaped payload persisted
// alongside the account.
func (f *TeamSSOFlow) SessionPayloadFromRecord(record openai.AuthRecord) map[string]any {
	return map[string]any{
		"user":        map[string]any{"email": f.accountEmail()},
		"accessToken": record.AccessToken,
		"expires":     record.Expired,
	}
}

// ---------------------------------------------------------------------------
// small page helpers (Playwright locator/title shims)
// ---------------------------------------------------------------------------

// log2 logs a literal (non-formatted) message.
func (f *TeamSSOFlow) log2(msg string) {
	if f.log != nil {
		f.log(msg)
	}
}

// hasChatGPTSession mirrors _has_chatgpt_session (app.py:10062) — the browser
// wrapper already scans every chatgpt.com page for an accessToken.
func (f *TeamSSOFlow) hasChatGPTSession() bool {
	if f.browser == nil {
		return false
	}
	return f.browser.HasChatGPTSession()
}

// bodyInnerText is page.locator("body").inner_text(timeout=1000). The bool is
// false when the read failed (Python's `except` branch).
func (f *TeamSSOFlow) bodyInnerText() (string, bool) {
	if f.page == nil || f.page.Rod == nil {
		return "", false
	}
	v, err := f.page.Rod.Timeout(teamSSOTextProbeTimeout).Eval(`() => document.body ? (document.body.innerText || '') : ''`)
	if err != nil || v == nil || v.Value.Nil() {
		return "", false
	}
	return v.Value.Str(), true
}

// pageTitle is page.title(timeout=1000).
func (f *TeamSSOFlow) pageTitle() (string, bool) {
	if f.page == nil || f.page.Rod == nil {
		return "", false
	}
	v, err := f.page.Rod.Timeout(teamSSOTextProbeTimeout).Eval(`() => document.title || ''`)
	if err != nil || v == nil || v.Value.Nil() {
		return "", false
	}
	return v.Value.Str(), true
}

// reloadPage is page.reload(wait_until="domcontentloaded", timeout=...).
func (f *TeamSSOFlow) reloadPage(timeout time.Duration) error {
	if f.page == nil || f.page.Rod == nil {
		return errors.New("page 已关闭")
	}
	if err := f.page.Rod.Timeout(timeout).Reload(); err != nil {
		return err
	}
	return f.page.WaitDOMContentLoaded(timeout)
}

// teamSSOWorkspaceLabel is `str(result.get('text') or 'My Team')[:80]`
// (app.py:9540). The `or` fires on the EMPTY string only — a whitespace-only
// label is truthy in Python and is logged verbatim, so it must NOT be trimmed
// before the fallback test, and the 80-rune cut applies to the RAW text.
func teamSSOWorkspaceLabel(text string) string {
	if text == "" {
		text = "My Team"
	}
	return teamSSOTrunc(text, 80)
}

// teamSSOTrunc slices by runes, matching Python's str[:n] on code points.
func teamSSOTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------------------------------------------------------------------------
// embedded JS — kept VERBATIM from app.py. Any regex/predicate drift silently
// mis-clicks (e.g. selecting the personal account instead of the Team
// workspace), so do not "clean these up". The only edit is splicing JS template
// literals (backticks) out of the Go raw strings.
// ---------------------------------------------------------------------------

// selectTeamWorkspaceJS is the workspace scorer of
// _select_team_workspace_if_visible (app.py:9465-9527), verbatim: visibility +
// enabled predicate, teamPattern vs badPattern across ~6 languages, an 8-level
// ancestor row walk, size/tag scoring with a -100 penalty for personal-account
// matches, and the x = left + width*0.92 click point clamped to [left+8, right-8].
var selectTeamWorkspaceJS = `() => {
                    const visible = el => {
                        if (!el) return false;
                        const r = el.getBoundingClientRect();
                        const s = getComputedStyle(el);
                        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
                    };
                    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
                    const textOf = el => ` + "`" + `${el.innerText || el.textContent || ''} ${el.getAttribute?.('aria-label') || ''} ${el.getAttribute?.('data-testid') || ''}` + "`" + `.replace(/\s+/g, ' ').trim();
                    const badPattern = /Conta\s+pessoal|Personal|personal\s+account|个人|個人|pessoal|Google|Microsoft|Apple|密码|password|电话|phone|Termos|Política|privacy|privacy policy/i;
                    const teamPattern = /(^|\s)(My\s+Team|Team|Teams)(\s|$)|工作空间|工作区|Equipe|Organiza[cç][aã]o|Organization|empresa/i;
                    const seeds = Array.from(document.querySelectorAll('button, a, [role="button"], [role="link"], [tabindex], [onclick], li, div, span'))
                        .filter(el => visible(el) && enabled(el));
                    const candidates = seeds
                        .map(textOf)
                        .filter(Boolean)
                        .filter(text => /Team|Workspace|trabalho|工作空间|工作区|Conta\s+pessoal|Personal/i.test(text))
                        .slice(0, 20);
                    const teamSeeds = seeds.filter(el => {
                        const text = textOf(el);
                        return teamPattern.test(text) && !badPattern.test(text);
                    });
                    const scoreTarget = (el) => {
                        const r = el.getBoundingClientRect();
                        const text = textOf(el);
                        let score = 0;
                        if (el.matches?.('button, a, [role="button"], [role="link"], [tabindex], [onclick]')) score += 10;
                        if (r.width >= 180 && r.width <= 720) score += 8;
                        if (r.height >= 36 && r.height <= 140) score += 8;
                        if (/^My\s+Team$/i.test(text)) score += 6;
                        if (/My\s+Team/i.test(text)) score += 4;
                        if (/Team|Workspace|工作空间|工作区/i.test(text)) score += 2;
                        if (badPattern.test(text)) score -= 100;
                        if (r.top < 0 || r.left < 0) score -= 10;
                        return score;
                    };
                    const rowFor = (seed) => {
                        const choices = [];
                        let el = seed;
                        while (el && el !== document.body && choices.length < 8) {
                            if (visible(el) && enabled(el)) {
                                const text = textOf(el);
                                if (teamPattern.test(text) && !badPattern.test(text)) choices.push(el);
                            }
                            el = el.parentElement;
                        }
                        choices.sort((a, b) => scoreTarget(b) - scoreTarget(a));
                        return choices[0] || seed;
                    };
                    const targets = Array.from(new Set(teamSeeds.map(rowFor))).filter(el => visible(el) && enabled(el));
                    targets.sort((a, b) => scoreTarget(b) - scoreTarget(a));
                    const target = targets[0];
                    if (!target) return { ok: false, candidates };
                    target.scrollIntoView({ block: 'center', inline: 'center' });
                    const r = target.getBoundingClientRect();
                    return {
                        ok: true,
                        text: textOf(target),
                        x: Math.max(r.left + 8, Math.min(r.right - 8, r.left + r.width * 0.92)),
                        y: r.top + r.height / 2,
                        candidates,
                    };
                }`

// completeTeamOnboardingJS is _complete_team_onboarding_if_visible's evaluate
// body (app.py:9572-9606), verbatim. Returns 'work' | 'later' | 'skip' | ”.
const completeTeamOnboardingJS = `() => {
                    const visible = el => {
                        if (!el) return false;
                        const r = el.getBoundingClientRect();
                        const s = getComputedStyle(el);
                        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
                    };
                    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
                    const body = document.body?.textContent || '';
                    const candidates = Array.from(document.querySelectorAll('button, a, [role="button"]')).filter(el => visible(el) && enabled(el));

                    if (/What kind of work do you do|Select the option that best applies|你从事哪种工作|你从事什么工作/i.test(body)) {
                        const target = candidates.find(el => /Engineering|工程/i.test((el.textContent || '').trim())) || candidates[0];
                        if (!target) return '';
                        target.scrollIntoView({ block: 'center', inline: 'center' });
                        target.click();
                        return 'work';
                    }

                    const later = candidates.find(el => /Maybe later|Not now|稍后再说|稍後再說|以后再说|暫時不要/i.test((el.textContent || '').trim()));
                    if (later) {
                        later.scrollIntoView({ block: 'center', inline: 'center' });
                        later.click();
                        return 'later';
                    }

                    const skip = candidates.find(el => /Skip|跳过|跳過/i.test((el.textContent || '').trim()));
                    if (skip) {
                        skip.scrollIntoView({ block: 'center', inline: 'center' });
                        skip.click();
                        return 'skip';
                    }

                    return '';
                }`

// approveSSOLoginJS is _approve_sso_login_if_visible's evaluate body
// (app.py:9619-9637), verbatim.
var approveSSOLoginJS = `() => {
                    const visible = el => {
                        if (!el) return false;
                        const r = el.getBoundingClientRect();
                        const s = getComputedStyle(el);
                        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
                    };
                    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
                    const candidates = Array.from(document.querySelectorAll('button, a, [role="button"], input[type="submit"]')).filter(el => visible(el) && enabled(el));
                    const approve = candidates.find(el => {
                        const text = ` + "`" + `${el.value || ''} ${el.textContent || ''} ${el.getAttribute('aria-label') || ''}` + "`" + `.replace(/\s+/g, ' ').trim();
                        return /批准登录|批准登入|Approve\s+(login|sign[- ]?in)|Approve\s+sign[- ]?in/i.test(text)
                            && !/不认识|不認識|Not.*account|deny|cancel/i.test(text);
                    });
                    if (!approve) return false;
                    approve.scrollIntoView({ block: 'center', inline: 'center' });
                    approve.click();
                    return true;
                }`

// clickCodexConsentJS is _click_codex_consent_if_visible's evaluate body
// (app.py:9702-9719), verbatim.
var clickCodexConsentJS = `() => {
                    const visible = el => {
                        if (!el) return false;
                        const r = el.getBoundingClientRect();
                        const s = getComputedStyle(el);
                        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
                    };
                    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
                    const candidates = Array.from(document.querySelectorAll('button, a, [role="button"], input[type="submit"]')).filter(el => visible(el) && enabled(el));
                    const target = candidates.find(el => {
                        const text = ` + "`" + `${el.value || ''} ${el.textContent || ''} ${el.getAttribute('aria-label') || ''}` + "`" + `.replace(/\s+/g, ' ').trim();
                        return /Authorize|授权|允許|允许|Continue|继续|続行|Approve/i.test(text);
                    });
                    if (!target) return false;
                    target.scrollIntoView({ block: 'center', inline: 'center' });
                    target.click();
                    return true;
                }`
