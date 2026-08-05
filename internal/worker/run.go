package worker

// This file ports the worker's five public entry points (app.py:8871-9128):
// run, run_auth_only, run_team, run_register_and_authorize_rt and relink,
// plus the OTP-reader preconnect they share. Each one is the same skeleton:
//
//	health-check the proxy and fix the fingerprint -> launch a browser ->
//	clear cookies -> log the fingerprint -> open a page -> drive a flow ->
//	either PARK the browser for the user or close it.
//
// Two details are load-bearing and easy to lose in translation:
//
//  1. Python disarms its `finally: self._close_browser(...)` by rebinding the
//     local to None after parking the browser in KEPT_REGISTER_BROWSER_SESSIONS.
//     The Go equivalent is setting the captured local to nil before returning —
//     the deferred CloseBrowser reads it at call time, so a parked browser must
//     be nil'd or the user's window is closed out from under them.
//
//  2. run / run_auth_only wrap the WHOLE body in try/except and, on any error,
//     re-check whether the browser is in fact logged in (app.py:8905, 8946).
//     A late page error after a successful login is therefore not fatal. That
//     salvage path covers the session read too, not just registration.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// otpSubmitWaitSeconds is _wait_after_otp_submit's default timeout
// (app.py:10964).
const otpSubmitWaitSeconds = 20

// AuthResult is the dict returned by run_team (app.py:9032) and
// run_register_and_authorize_rt (app.py:9080): a session plus the OpenAI
// refresh token. Both always return an empty url and storage_state_json.
type AuthResult struct {
	SessionInfo
	OpenAIRT string `json:"openai_rt"`
}

// flows is one assembled set of collaborators bound to a single page. The
// Python worker is a god-object whose methods call each other directly; the Go
// port splits it into flow types, so the wiring the monolith got for free has
// to be done explicitly here.
type flows struct {
	Register *RegisterFlow
	Auth     *AuthURLBuilder
	CF       *CFSolver
	Team     *TeamSSOFlow
	OTP      *OTPHandler
	AboutYou *AboutYouFiller
	Phone    *PhoneHandler
}

// buildFlows assembles the collaborators for one page. The HTTP client is bound
// to the SAME proxy chain as the browser: Python issued these calls through
// `context.request`, which inherits the context's proxy and cookies, so a
// client on a different exit would be an instant mismatch.
func (w *Worker) buildFlows(b *browser.Browser, p *browser.Page, proxy models.ProxyConfig) *flows {
	client := tlsclient.NewOrNil(proxy.ChainURL, 30)

	auth := NewAuthURLBuilder(p, b, w.Account, w.Fingerprint, client, w.log)
	cf := NewCFSolver(b, p, w.cfg.Headless, w.log)
	team := NewTeamSSOFlow(b, p, w.Account, client, w.log)
	aboutYou := NewAboutYouFiller(p, w.log)
	phone := NewPhoneHandler(p, b, w.cfg.PhoneProvider, w.Account, w.log)

	otp := &OTPHandler{
		Page:           p,
		Browser:        b,
		Account:        w.Account,
		Reader:         w.otpReader,
		ManualEmailOTP: w.cfg.ManualEmailOTP,
		InputCallback:  w.cfg.InputCallback,
		Log:            w.log,

		ClickContinue:                  auth.ClickContinue,
		HasAboutYouForm:                aboutYou.HasAboutYouForm,
		LooksLikeRegisterPhoneCodePage: phone.LooksLikeRegisterPhoneCodePage,
		WaitAfterOTPSubmit:             func() error { return cf.WaitAfterOTPSubmit(otpSubmitWaitSeconds) },
		HandleCloudflareChallenge:      cf.HandleCloudflareChallenge,
	}

	// The Cloudflare solver probes whichever page the challenge landed on, which
	// is not always the flow's own tab, so its hooks are page-parameterised.
	// Leaving HasOTPInput nil makes WaitAfterOTPSubmit succeed on any URL that is
	// not /email-verification, where app.py:10974 would still be polling.
	cf.HasAboutYouForm = func(target *browser.Page) bool {
		return NewAboutYouFiller(target, w.log).HasAboutYouForm()
	}
	cf.HasOTPInput = func(target *browser.Page) bool {
		return w.otpProbe(b, target).HasOTPInput()
	}
	// LowerWindows stays nil: it is the Win32 z-order hack from the Tk UI
	// (lower_playwright_chromium_windows_later), and belongs to the Wails layer.

	phone.HasAboutYouForm = aboutYou.HasAboutYouForm

	// The about-you filler's own hooks. Leaving these nil is silent: three of the
	// five _about_you_submit_done success branches (app.py:11038/11044/11046) and
	// the _click_continue fallback (app.py:11052) would just never fire, so every
	// submit would burn its full 30s poll before "succeeding" by timeout.
	aboutYou.HasChatGPTSession = b.HasChatGPTSession
	aboutYou.HasRegisterPhoneNumberForm = phone.HasRegisterPhoneNumberForm
	aboutYou.HasVisiblePassword = otp.HasVisiblePassword
	aboutYou.ClickContinue = auth.ClickContinue

	team.Hooks = TeamSSOHooks{
		CreateSigninURL: auth.CreateOpenAISigninURL,
		// Python passes allow_manual=True from the Team path (app.py:10829).
		TryPassCloudflare:  func(reason string) bool { return cf.TryPassCloudflare(p, true, reason) },
		DetectRouteError:   auth.DetectRouteError,
		RetryRouteError:    auth.RetryRouteError,
		FillEmailIfVisible: auth.FillEmailIfVisible,
	}

	reg := NewRegisterFlow(b, p, w.Account, w.log)
	reg.Auth = auth
	reg.CF = cf
	reg.Team = team
	reg.OTP = otp
	reg.AboutYou = aboutYou
	reg.Phone = phone

	return &flows{Register: reg, Auth: auth, CF: cf, Team: team, OTP: otp, AboutYou: aboutYou, Phone: phone}
}

// otpProbe builds a read-only OTPHandler bound to an arbitrary page, for the
// Cloudflare solver's cross-tab probes. It wires only the two negative guards
// _has_otp_input consults (app.py:10554); the submit-side hooks are deliberately
// absent because this handler never submits anything.
func (w *Worker) otpProbe(b *browser.Browser, p *browser.Page) *OTPHandler {
	aboutYou := NewAboutYouFiller(p, w.log)
	phone := NewPhoneHandler(p, b, w.cfg.PhoneProvider, w.Account, w.log)
	return &OTPHandler{
		Page:                           p,
		Browser:                        b,
		Account:                        w.Account,
		Log:                            w.log,
		HasAboutYouForm:                aboutYou.HasAboutYouForm,
		LooksLikeRegisterPhoneCodePage: phone.LooksLikeRegisterPhoneCodePage,
	}
}

// preconnectOTPReader mirrors _preconnect_otp_reader (app.py:8968): open the
// mailbox before registration starts so the OTP mail is not missed while the
// IMAP handshake is still in flight.
func (w *Worker) preconnectOTPReader() error {
	if w.cfg.ManualEmailOTP {
		w.log("手动邮箱验证码模式：跳过邮箱令牌/IMAP 预连接")
		return nil
	}
	if w.otpReader != nil {
		return nil
	}
	provider := ""
	if w.Account != nil {
		provider = w.Account.MailProvider
	}
	w.log(fmt.Sprintf("提前连接%s，准备接收 OpenAI 验证码", mailProviderLabel(provider)))
	reader, err := mail.CreateMailReader(w.Account, mail.Log(w.log), "")
	if err != nil {
		return err
	}
	// Assign BEFORE connecting, exactly as Python does: a failed connect must
	// still leave the reader for the caller's teardown to close.
	w.otpReader = reader
	return reader.Connect()
}

// mailProviderLabel is
// `"Cloud Mail API" if str(self.account.mail_provider or "").casefold() == "cloudmail" else "邮箱 IMAP"`
// (app.py:8977).
//
// There is NO .strip() in Python, so " cloudmail " is IMAP — an earlier
// TrimSpace here reported the wrong reader in the pre-connect log while
// create_mail_reader (which does its own untrimmed compare) opened the other
// one. casefold() and EqualFold agree for this needle: every rune of
// "cloudmail" has a two-element simple-fold orbit, and the length-changing
// foldings (ß, İ) cannot produce any of them.
func mailProviderLabel(mailProvider string) string {
	if strings.EqualFold(mailProvider, "cloudmail") {
		return "Cloud Mail API"
	}
	return "邮箱 IMAP"
}

// closeOTPReader is the `if self.otp_reader: self.otp_reader.close()` that runs
// in each entry point's finally. Teardown errors are dropped — Python's callers
// are inside a finally too, where raising would mask the real error.
func (w *Worker) closeOTPReader() {
	if w.otpReader == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = w.otpReader.Close()
}

// logFingerprint emits the per-run fingerprint line. prefix is "" for the
// register paths and "Team " for run_team (app.py:9014).
func (w *Worker) logFingerprint(prefix string) {
	fp := w.Fingerprint
	w.log(fmt.Sprintf("%s浏览器指纹: Chrome/%s %dx%d %s %s cpu=%d mem=%d",
		prefix, fp.ChromeMajor(), fp.ViewportWidth, fp.ViewportHeight,
		fp.Locale, fp.Timezone, fp.HardwareConcurrency, fp.DeviceMemory))
}

// park hands the browser to the process-global kept-session registry so the
// user keeps a logged-in window. Callers MUST nil their local afterwards.
func (w *Worker) park(b *browser.Browser, proxy models.ProxyConfig) {
	email := ""
	if w.Account != nil {
		email = w.Account.Email
	}
	ParkBrowser(email, b, proxy.DynamicProxy)
}

// openRegisterBrowser is the shared prologue: proxy health -> fingerprint ->
// launch -> clear cookies -> log fingerprint -> first page -> proxy status.
func (w *Worker) openRegisterBrowser(proxy models.ProxyConfig, healthLabel, fpPrefix, proxyLabel string) (*browser.Browser, *browser.Page, error) {
	if _, err := w.PrepareFingerprintForProxy(proxy, healthLabel); err != nil {
		return nil, nil, err
	}
	b, err := w.NewBrowser(proxy)
	if err != nil {
		return nil, nil, err
	}
	if err := b.ClearCookies(); err != nil {
		CloseBrowser(b)
		return nil, nil, err
	}
	w.logFingerprint(fpPrefix)
	p, err := b.NewPage()
	if err != nil {
		CloseBrowser(b)
		return nil, nil, err
	}
	w.LogBrowserProxyStatus(proxyLabel)
	return b, p, nil
}

// Run mirrors run (app.py:8871): register or log in, keep the window open, and
// read the session out of it.
func (w *Worker) Run(ctx context.Context) (result *SessionInfo, err error) {
	var b *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(b) // nil once parked
	}()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "认证"); err != nil {
		return nil, err
	}
	if err := w.preconnectOTPReader(); err != nil {
		return nil, err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return nil, err
	}
	b = nb
	if err := b.ClearCookies(); err != nil {
		return nil, err
	}
	w.logFingerprint("")
	p, err := b.NewPage()
	if err != nil {
		return nil, err
	}
	w.LogBrowserProxyStatus("认证浏览器代理")

	fl := w.buildFlows(b, p, w.cfg.RegisterProxy)
	result, err = func() (*SessionInfo, error) {
		if err := fl.Register.Register(ctx); err != nil {
			return nil, err
		}
		w.log("[认证] 认证完成，当前窗口保持打开；开始读取 Session 信息")
		return NewPayLinkExtractorFromWorker(w, b, p).ExtractSessionInfo()
	}()
	if err != nil {
		// app.py:8905 — a login that already succeeded outranks a later page error.
		if !b.HasChatGPTSession() {
			return nil, err
		}
		w.log("[认证] 检测到浏览器已登录成功，忽略前序页面异常并读取 Session")
		result, err = NewPayLinkExtractorFromWorker(w, b, p).ExtractSessionInfo()
		if err != nil {
			return nil, err
		}
	}

	w.park(b, w.cfg.RegisterProxy)
	b = nil
	return result, nil
}

// RunAuthOnly mirrors run_auth_only (app.py:8927): register or log in and leave
// the window open, reading nothing back.
func (w *Worker) RunAuthOnly(ctx context.Context) (err error) {
	var b *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(b)
	}()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "认证"); err != nil {
		return err
	}
	if err := w.preconnectOTPReader(); err != nil {
		return err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return err
	}
	b = nb
	if err := b.ClearCookies(); err != nil {
		return err
	}
	p, err := b.NewPage()
	if err != nil {
		return err
	}
	w.LogBrowserProxyStatus("认证浏览器代理")

	fl := w.buildFlows(b, p, w.cfg.RegisterProxy)
	if err := fl.Register.Register(ctx); err != nil {
		if !b.HasChatGPTSession() {
			return err
		}
		w.park(b, w.cfg.RegisterProxy)
		b = nil
		w.log("[认证] 检测到浏览器已登录成功，忽略前序页面异常并保持窗口打开")
		return nil
	}

	w.park(b, w.cfg.RegisterProxy)
	b = nil
	w.log("[认证] 注册或登录完成，浏览器窗口保持打开")
	return nil
}

// RunTeam mirrors run_team (app.py:9003): drive the Team SSO signup, then take
// a refresh token out of the logged-in browser.
//
// Unlike the register paths this one never preconnects a mail reader (Team SSO
// has no email OTP step), so its teardown has no reader close either.
func (w *Worker) RunTeam(ctx context.Context) (*AuthResult, error) {
	// A saved fingerprint stays pinned; otherwise Team gets its own profile pool.
	if !w.fingerprintFixed {
		w.Fingerprint = models.GenerateTeamFingerprint()
	}

	var b *browser.Browser
	defer func() { CloseBrowser(b) }()

	nb, p, err := w.openRegisterBrowser(w.cfg.RegisterProxy, "Team 认证", "Team ", "Team 认证浏览器代理")
	if err != nil {
		return nil, err
	}
	b = nb

	fl := w.buildFlows(b, p, w.cfg.RegisterProxy)
	if err := fl.Team.RegisterTeamSSO(); err != nil {
		return nil, err
	}
	record, err := fl.Team.AuthorizeRTFromBrowser()
	if err != nil {
		return nil, err
	}
	w.log("Team RT 获取成功")

	w.park(b, w.cfg.RegisterProxy)
	b = nil

	return w.authResult(fl.Team, record)
}

// RunRegisterAndAuthorizeRT mirrors run_register_and_authorize_rt
// (app.py:9045): normal registration, then an OAuth round-trip for a refresh
// token instead of a session read.
func (w *Worker) RunRegisterAndAuthorizeRT(ctx context.Context) (result *AuthResult, err error) {
	var b *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(b)
	}()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "认证"); err != nil {
		return nil, err
	}
	if err := w.preconnectOTPReader(); err != nil {
		return nil, err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return nil, err
	}
	b = nb
	if err := b.ClearCookies(); err != nil {
		return nil, err
	}
	w.logFingerprint("")
	p, err := b.NewPage()
	if err != nil {
		return nil, err
	}
	w.LogBrowserProxyStatus("认证浏览器代理")

	fl := w.buildFlows(b, p, w.cfg.RegisterProxy)
	if err := fl.Register.Register(ctx); err != nil {
		return nil, err
	}
	w.log("域名邮箱注册完成，开始 OAuth 授权获取 RT")
	record, err := fl.Team.AuthorizeRTFromBrowser()
	if err != nil {
		return nil, err
	}

	w.park(b, w.cfg.RegisterProxy)
	b = nil

	return w.authResult(fl.Team, record)
}

// Relink mirrors relink (app.py:9092): log into an existing account on the
// register proxy, carry the login state over to the extract proxy, and pull a
// fresh payment link there.
func (w *Worker) Relink(ctx context.Context) (*PayLinkResult, error) {
	var loginBrowser, extractBrowser *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(loginBrowser)
		CloseBrowser(extractBrowser)
	}()

	lb, loginPage, err := w.openRegisterBrowser(w.cfg.RegisterProxy, "登录", "", "登录浏览器代理")
	if err != nil {
		return nil, err
	}
	loginBrowser = lb

	fl := w.buildFlows(loginBrowser, loginPage, w.cfg.RegisterProxy)
	if err := fl.Register.LoginExisting(ctx, true); err != nil {
		return nil, err
	}

	// Export BEFORE closing: the state is the only thing that crosses proxies.
	state, err := loginBrowser.ExportStorageState()
	if err != nil {
		return nil, err
	}
	w.log("登录完成，已保存登录态，切换到长链接提取代理")

	CloseBrowser(loginBrowser)
	loginBrowser = nil

	if _, err := w.PrepareFingerprintForProxy(w.cfg.ExtractProxy, "支付链接"); err != nil {
		return nil, err
	}
	eb, err := w.NewBrowser(w.cfg.ExtractProxy)
	if err != nil {
		return nil, err
	}
	extractBrowser = eb
	// Playwright took storage_state at context creation; go-rod has to be seeded
	// after launch but still before the first navigation.
	if err := extractBrowser.ApplyStorageState(state); err != nil {
		return nil, err
	}
	extractPage, err := extractBrowser.NewPage()
	if err != nil {
		return nil, err
	}
	w.LogBrowserProxyStatus("长链接浏览器代理")

	return NewPayLinkExtractorFromWorker(w, extractBrowser, extractPage).ExtractPayLink()
}

// authResult builds the shared run_team / run_register_and_authorize_rt return
// value (app.py:9032, 9080).
func (w *Worker) authResult(team *TeamSSOFlow, record openai.AuthRecord) (*AuthResult, error) {
	sessionJSON, err := marshalIndentNoEscape(team.SessionPayloadFromRecord(record))
	if err != nil {
		return nil, err
	}
	return &AuthResult{
		SessionInfo: SessionInfo{
			URL:              "",
			AccessToken:      record.AccessToken,
			SessionJSON:      sessionJSON,
			StorageStateJSON: "",
		},
		OpenAIRT: record.RefreshToken,
	}, nil
}

// marshalIndentNoEscape reproduces json.dumps(..., ensure_ascii=False, indent=2).
// encoding/json escapes <, > and & by default, which json.dumps does not.
func marshalIndentNoEscape(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
