package ui

// 本文件把认证类操作暴露为 Wails 后台任务。协议流程只走 HTTP；
// 人工流程会打开并保留 Chromium。测试必须替换下方 seam，不能登录真实账号。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/authproto"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/turnstile"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

const (
	JobProtocolRegister      JobKind = "protocol_register"
	JobProtocolRegisterBatch JobKind = "protocol_register_batch"
	JobOAuthAuthorize        JobKind = "oauth_authorize"
	JobOAuthAuthorizeBatch   JobKind = "oauth_authorize_batch"
	JobKeepLogin             JobKind = "keep_login"
	JobSessionReader         JobKind = "session_reader"
	JobExternalOAuth         JobKind = "external_oauth"
	JobManualLoginCode       JobKind = "manual_login_code"
)

func init() {
	for _, kind := range []JobKind{
		JobProtocolRegister,
		JobProtocolRegisterBatch,
		JobOAuthAuthorize,
		JobOAuthAuthorizeBatch,
		JobKeepLogin,
		JobSessionReader,
		JobExternalOAuth,
		JobManualLoginCode,
	} {
		networkJobKinds[kind] = true
	}
}

// AuthBatchRequest 是协议注册和 OAuth 授权的账号选区。
// OAuth 授权可能按设置租用短信号码，因此必须额外确认。
type AuthBatchRequest struct {
	Emails    []string `json:"emails"`
	Confirmed bool     `json:"confirmed"`
}

type ExternalOAuthRequest struct {
	Email                string `json:"email"`
	URL                  string `json:"url"`
	ConfirmedNonStandard bool   `json:"confirmedNonStandard"`
}

// AuthAccountResult 是批量父任务最终保存的逐账号摘要。
type AuthAccountResult struct {
	Email  string `json:"email"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type authRecordRunner func(
	context.Context,
	models.MailAccount,
	settings.Settings,
	string,
	authproto.InputCallback,
	authproto.PhoneProvider,
	bool,
	func(string),
) (openai.AuthRecord, error)

// 两个 seam 分开，测试可以分别证明“协议模式绝不带手机号提供者”和
// “OAuth 授权只有在明确确认后才可能带手机号提供者”。
var runProtocolRecord authRecordRunner = runAuthRecordFlow
var runOAuthRecord authRecordRunner = runAuthRecordFlow

type browserActionRunner func(context.Context, *worker.Worker, string) (worker.BrowserActionResult, error)

var runKeepLoginAction browserActionRunner = func(ctx context.Context, w *worker.Worker, _ string) (worker.BrowserActionResult, error) {
	return w.RunLoginAndKeep(ctx)
}
var runSessionReaderAction browserActionRunner = func(ctx context.Context, w *worker.Worker, _ string) (worker.BrowserActionResult, error) {
	return w.RunSessionReader(ctx)
}
var runExternalOAuthAction browserActionRunner = func(ctx context.Context, w *worker.Worker, value string) (worker.BrowserActionResult, error) {
	return w.RunExternalOAuth(ctx, value)
}
var runManualLoginCodeAction browserActionRunner = func(ctx context.Context, w *worker.Worker, _ string) (worker.BrowserActionResult, error) {
	return w.RunManualLoginCode(ctx)
}

// StartProtocolRegisterSession 对选区顺序执行纯请求 OAuth + 邮箱验证码流程。
// PhoneProvider 永远为 nil，OpenAI 要求手机号时只报告并跳过，不租号、不扣费。
func (a *App) StartProtocolRegisterSession(req AuthBatchRequest) (BatchSummary, error) {
	return a.startAuthRecordBatch(JobProtocolRegisterBatch, JobProtocolRegister, req, false)
}

// StartOAuthAuthorizeRT 对选区顺序获取 OpenAI refresh_token。若设置启用了
// SMSBower，此操作可能租号，所以后端也要求 Confirmed=true。
func (a *App) StartOAuthAuthorizeRT(req AuthBatchRequest) (BatchSummary, error) {
	if !req.Confirmed {
		return BatchSummary{}, errors.New("OAuth 授权可能租用短信号码，必须由用户明确确认")
	}
	return a.startAuthRecordBatch(JobOAuthAuthorizeBatch, JobOAuthAuthorize, req, true)
}

// StartKeepLogin 登录已有账号并保留浏览器。
func (a *App) StartKeepLogin(email string) (JobView, error) {
	return a.startBrowserAction(JobKeepLogin, email, "", runKeepLoginAction)
}

// StartOpenSessionReader 打开辅助登录页并自动填写邮箱。
func (a *App) StartOpenSessionReader(email string) (JobView, error) {
	return a.startBrowserAction(JobSessionReader, email, "", runSessionReaderAction)
}

// StartExternalOAuth 只允许在 auth.openai.com 上打开 HTTPS 链接。非标准
// authorize 路径仍需用户额外确认，但不会把邮箱或验证码带到任意第三方站点。
func (a *App) StartExternalOAuth(req ExternalOAuthRequest) (JobView, error) {
	value, standard, err := validateExternalOAuthURL(req.URL)
	if err != nil {
		return JobView{}, err
	}
	if !standard && !req.ConfirmedNonStandard {
		return JobView{}, errors.New("链接不是标准 OpenAI OAuth authorize 路径，必须额外确认")
	}
	return a.startBrowserAction(JobExternalOAuth, req.Email, value, runExternalOAuthAction)
}

// StartManualLoginCode 打开 ChatGPT 并只读监听邮箱验证码。
func (a *App) StartManualLoginCode(email string) (JobView, error) {
	return a.startBrowserAction(JobManualLoginCode, email, "", runManualLoginCodeAction)
}

func (a *App) startAuthRecordBatch(
	parentKind JobKind,
	childKind JobKind,
	req AuthBatchRequest,
	allowPhone bool,
) (BatchSummary, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return BatchSummary{}, err
	}
	accounts, skipped, err := a.resolveBatchSelection(snapshot, req.Emails)
	if err != nil {
		return BatchSummary{}, err
	}
	if len(accounts) == 0 {
		return BatchSummary{Skipped: skipped}, errors.New("没有可执行的账号")
	}
	st := settings.FromSnapshot(snapshot)

	ctx, cancel := context.WithCancel(context.Background())
	parentID, err := a.registerJob(parentKind, "", "", cancel)
	if err != nil {
		cancel()
		return BatchSummary{}, err
	}
	a.setBatchProgress(parentID, len(accounts), 0)
	log := a.jobLogger(parentID)
	for _, email := range skipped {
		log(fmt.Sprintf("跳过 %s：未通过启动前检查", email))
	}
	view, _ := a.jobView(parentID)
	go func() {
		defer cancel()
		a.runAuthRecordBatch(ctx, parentID, childKind, accounts, st, allowPhone, log)
	}()
	return BatchSummary{Job: view, Skipped: skipped}, nil
}

func (a *App) runAuthRecordBatch(
	ctx context.Context,
	parentID string,
	childKind JobKind,
	accounts []models.MailAccount,
	st settings.Settings,
	allowPhone bool,
	log func(string),
) {
	pool, poolSize := a.authProxyPool(st)
	results := make([]AuthAccountResult, 0, len(accounts))
	for index, account := range accounts {
		if ctx.Err() != nil {
			results = append(results, AuthAccountResult{
				Email: account.Email, Status: string(StatusCancelled), Error: ctx.Err().Error(),
			})
			break
		}
		result := a.runAuthRecordAccount(ctx, parentID, childKind, account, st, pool, poolSize, allowPhone)
		results = append(results, result)
		a.setBatchProgress(parentID, len(accounts), index+1)
	}
	succeeded := 0
	for _, result := range results {
		if result.Error == "" {
			succeeded++
		}
	}
	log(fmt.Sprintf("认证批量任务结束：成功 %d，失败/跳过 %d", succeeded, len(results)-succeeded))
	a.markJobFinished(parentID, results, nil, ctx.Err() != nil)
}

func (a *App) runAuthRecordAccount(
	ctx context.Context,
	parentID string,
	childKind JobKind,
	account models.MailAccount,
	st settings.Settings,
	pool *proxypool.Set,
	poolSize int,
	allowPhone bool,
) AuthAccountResult {
	attempts := 1
	if pool != nil && poolSize > 0 {
		attempts = poolSize
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return AuthAccountResult{Email: account.Email, Status: string(StatusCancelled), Error: err.Error()}
		}
		dynamic := ""
		if pool != nil {
			dynamic = pool.TakeAuth(st.RegisterWithPaymentProxy)
			if dynamic == "" {
				break
			}
		}
		childCtx, cancel := context.WithCancel(ctx)
		childID, err := a.registerJob(childKind, account.Email, parentID, cancel)
		if err != nil {
			cancel()
			return AuthAccountResult{Email: account.Email, Status: "失败", Error: err.Error()}
		}
		childLog := a.jobLogger(childID)
		record, runErr := a.runAuthRecordAttempt(
			childCtx, childID, childKind, account, st, dynamic, allowPhone, childLog,
		)
		cancelled := childCtx.Err() != nil && runErr != nil
		a.markJobFinished(childID, record, runErr, cancelled)
		cancel()
		if runErr == nil {
			return AuthAccountResult{Email: account.Email, Status: authSuccessStatus(childKind)}
		}
		lastErr = runErr
		retryable := protocolErrorRetryable(runErr)
		if childKind == JobOAuthAuthorize {
			retryable = worker.IsAuthProxyTransportError(runErr)
		}
		if pool == nil || !retryable || attempt >= attempts {
			break
		}
		childLog(fmt.Sprintf("第 %d/%d 次失败，自动切换下一个代理: %v", attempt, attempts, runErr))
	}

	status := authFailureStatus(childKind, lastErr)
	if ctx.Err() == nil {
		_ = a.networkPatchState(account.Email, map[string]any{"status": status}, nil)
	}
	message := ""
	if lastErr != nil {
		message = lastErr.Error()
	}
	return AuthAccountResult{Email: account.Email, Status: status, Error: message}
}

func (a *App) runAuthRecordAttempt(
	ctx context.Context,
	jobID string,
	kind JobKind,
	account models.MailAccount,
	st settings.Settings,
	dynamic string,
	allowPhone bool,
	log func(string),
) (openai.AuthRecord, error) {
	proxy, err := a.openProxySession(st, dynamic, log)
	if err != nil {
		return openai.AuthRecord{}, err
	}
	defer proxy.Close()

	input := a.authInputCallback(ctx, jobID)
	var phoneProvider authproto.PhoneProvider
	var closePhone func()
	if allowPhone && st.SMSBowerEnabled && strings.TrimSpace(st.SMSBowerAPIKey) != "" {
		snapshot, snapshotErr := a.snapshot()
		if snapshotErr != nil {
			return openai.AuthRecord{}, snapshotErr
		}
		provider := a.phoneProvider(ctx, snapshot, log)
		phoneProvider = adaptAuthPhoneProvider(provider)
		closePhone = provider.Close
	}
	if closePhone != nil {
		defer closePhone()
	}

	runner := runProtocolRecord
	allowManualPhone := false
	if kind == JobOAuthAuthorize {
		runner = runOAuthRecord
		allowManualPhone = phoneProvider == nil
	}
	record, err := runner(
		ctx, account, st, proxy.Config.ChainURL, input, phoneProvider, allowManualPhone, log,
	)
	if err != nil {
		return openai.AuthRecord{}, err
	}
	if kind == JobProtocolRegister {
		if err := a.persistProtocolRecord(account.Email, record); err != nil {
			return openai.AuthRecord{}, err
		}
		log("协议注册取Session成功，已保存 Access Token / Session 记录")
		return record, nil
	}
	if err := a.persistOAuthRecord(account.Email, account.AccountType, record); err != nil {
		return openai.AuthRecord{}, err
	}
	log("OAuth RT 获取成功")
	return record, nil
}

func runAuthRecordFlow(
	ctx context.Context,
	account models.MailAccount,
	st settings.Settings,
	proxyURL string,
	input authproto.InputCallback,
	phoneProvider authproto.PhoneProvider,
	allowManualPhone bool,
	log func(string),
) (openai.AuthRecord, error) {
	if err := ctx.Err(); err != nil {
		return openai.AuthRecord{}, err
	}
	flow, err := authproto.New(authproto.Options{
		Account:                &account,
		Log:                    authproto.Log(log),
		PhoneProvider:          phoneProvider,
		InputCallback:          input,
		ProxyURL:               proxyURL,
		AllowManualPhone:       allowManualPhone,
		ManualEmailOTP:         st.ManualEmailOTP,
		TurnstileSolverEnabled: st.TurnstileSolverEnabled,
		TurnstileSolverURL:     st.TurnstileSolverURL,
		MailReaderFactory: authproto.NewMailReaderFactory(authproto.MailOTPOptions{
			Context: ctx,
		}),
		TurnstileSolver: turnstile.SolveToken,
	})
	if err != nil {
		return openai.AuthRecord{}, err
	}
	return flow.Run()
}

func (a *App) authInputCallback(ctx context.Context, jobID string) authproto.InputCallback {
	input := a.inputCallback(ctx, jobID)
	return func(kind, email, prompt string) (string, error) {
		answer := strings.TrimSpace(input(kind, email, prompt))
		if answer != "" {
			return answer, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", errors.New("已取消")
	}
}

func adaptAuthPhoneProvider(provider worker.PhoneProvider) authproto.PhoneProvider {
	if provider == nil {
		return nil
	}
	return func(action, email string, payload any) (any, error) {
		phone := authPhoneMap(payload)
		switch action {
		case "next":
			return provider.Next(email, map[string]string{})
		case "sent":
			return nil, provider.Sent(email, phone)
		case "code":
			return provider.Code(email, phone)
		case "good":
			return nil, provider.Good(email, phone)
		case "bad":
			return nil, provider.Bad(email, phone)
		default:
			return nil, fmt.Errorf("未知手机号动作: %s", action)
		}
	}
}

func authPhoneMap(value any) map[string]string {
	if typed, ok := value.(map[string]string); ok {
		return typed
	}
	out := map[string]string{}
	if raw, ok := value.(map[string]any); ok {
		for key, item := range raw {
			out[key] = settings.PyStr(item)
		}
	}
	return out
}

func (a *App) persistProtocolRecord(email string, record openai.AuthRecord) error {
	summary := openai.SummarizeChatGPTAccessToken(record.AccessToken)
	plan := openai.ClassifyChatGPTPlanText(openai.FirstNonEmpty(summary["plan_type"]))
	accountFields := map[string]any{
		"status": "协议Session已获取",
	}
	if record.RefreshToken != "" {
		accountFields["openai_rt"] = record.RefreshToken
	}
	if planTypesAdopted[plan] {
		accountFields["account_type"] = plan
	}

	sessionJSON, err := marshalProtocolSession(email, record)
	if err != nil {
		return err
	}
	sessionFields := map[string]any{
		"access_token":   record.AccessToken,
		"session_json":   sessionJSON,
		"access_summary": summary,
	}
	if record.RefreshToken != "" {
		sessionFields["openai_rt"] = record.RefreshToken
	}
	if plan != "" && plan != "unknown" {
		sessionFields["plan_type"] = plan
		sessionFields["chatgpt_plan_type"] = plan
	}
	accountID := openai.FirstNonEmpty(summary["account_id"], record.AccountID)
	if accountID != "" {
		sessionFields["account_id"] = accountID
		sessionFields["chatgpt_account_id"] = accountID
	}
	return a.networkPatchState(email, accountFields, sessionFields)
}

func (a *App) persistOAuthRecord(email, oldAccountType string, record openai.AuthRecord) error {
	if strings.TrimSpace(record.RefreshToken) == "" {
		return errors.New("授权成功但未获取到 refresh_token")
	}
	summary := openai.SummarizeChatGPTAccessToken(record.AccessToken)
	plan := openai.ClassifyChatGPTPlanText(openai.FirstNonEmpty(summary["plan_type"], oldAccountType))
	accountType := oldAccountType
	if planTypesAdopted[plan] {
		accountType = plan
	}
	status := rtStatusByPlan[accountType]
	if status == "" {
		status = statusRTCollected
	}
	return a.networkPatchState(email, map[string]any{
		"openai_rt":    record.RefreshToken,
		"account_type": accountType,
		"status":       status,
	}, nil)
}

func marshalProtocolSession(email string, record openai.AuthRecord) (string, error) {
	payload := map[string]any{
		"source":      "protocol_oauth",
		"email":       email,
		"accessToken": record.AccessToken,
		"auth_record": record,
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\r\n"), nil
}

func protocolErrorRetryable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	nonRetry := []string{
		"该账号进入密码登录页", "等待 openai 邮箱验证码超时",
		"emailotpvalidate请求失败", "wrong_email_otp_code",
		"已取消", "短信验证码", "add-phone",
	}
	for _, marker := range nonRetry {
		if strings.Contains(text, marker) {
			return false
		}
	}
	retry := []string{
		"oauthurl请求失败: 403", "oauthurl请求失败: 429",
		"oauthurl请求失败: 502", "oauthurl请求失败: 503",
		"oauthurl请求失败: 504", "timeout", "timed out", "proxy",
		"connection", "连接", "超时", "tls", "turnstile",
		"cloudflare challenge", "cf-challenge", "challenges.cloudflare.com",
		"风控", "被拦截", "403", "429",
	}
	for _, marker := range retry {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func authSuccessStatus(kind JobKind) string {
	if kind == JobProtocolRegister {
		return "协议Session已获取"
	}
	return "RT已获取"
}

func authFailureStatus(kind JobKind, err error) string {
	if kind == JobProtocolRegister {
		var phone *models.PhoneRequiredError
		if errors.As(err, &phone) {
			return models.ExceptionStatus(err, "协议需手机号(未接码)")
		}
		return "协议注册失败"
	}
	return "授权失败"
}

func (a *App) startBrowserAction(
	kind JobKind,
	email string,
	value string,
	runner browserActionRunner,
) (JobView, error) {
	account, err := a.accountByEmail(email)
	if err != nil {
		return JobView{}, err
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return JobView{}, err
	}
	if err := preflight(snapshot, account); err != nil {
		return JobView{}, err
	}
	st := settings.FromSnapshot(snapshot)
	dynamic := a.nextDynamicProxy(loginDynamicProxies(st))

	ctx, cancel := context.WithCancel(context.Background())
	id, err := a.registerJob(kind, account.Email, "", cancel)
	if err != nil {
		cancel()
		return JobView{}, err
	}
	view, _ := a.jobView(id)
	go func() {
		defer cancel()
		log := a.jobLogger(id)
		cfg, resources, buildErr := a.workerConfigProxy(ctx, kind, account, &dynamic, log)
		if buildErr != nil {
			a.markJobFinished(id, nil, buildErr, ctx.Err() != nil)
			return
		}
		cfg.Headless = false
		if kind == JobKeepLogin || kind == JobExternalOAuth {
			cfg.InputCallback = a.inputCallback(ctx, id)
		}
		w := worker.New(cfg)
		parkedBefore := worker.ParkedBrowserGeneration(account.Email)
		result, runErr := runner(ctx, w, value)
		if !worker.AttachParkedCleanupSince(account.Email, parkedBefore, resources.Close) {
			resources.Close()
		}
		cancelled := ctx.Err() != nil && runErr != nil
		if !cancelled {
			status := result.Status
			if runErr != nil {
				status = browserFailureStatus(kind)
			}
			if status != "" {
				if patchErr := a.networkPatchState(account.Email, map[string]any{"status": status}, nil); patchErr != nil && runErr == nil {
					runErr = patchErr
				}
			}
		}
		a.dropFailedStandaloneProxy(dynamic, runErr, log)
		a.markJobFinished(id, result, runErr, cancelled)
	}()
	return view, nil
}

func loginDynamicProxies(st settings.Settings) []string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return nil
	}
	if st.RegisterWithPaymentProxy {
		return proxypool.ParseProxyPoolText(st.PaymentDynamicProxy)
	}
	if values := proxypool.ParseProxyPoolText(st.DynamicProxies); len(values) > 0 {
		return values
	}
	return proxypool.ParseProxyPoolText(st.PaymentDynamicProxy)
}

func browserFailureStatus(kind JobKind) string {
	switch kind {
	case JobKeepLogin:
		return "登录失败"
	case JobSessionReader:
		return "辅助登录失败"
	case JobExternalOAuth:
		return "OAuth登录失败"
	case JobManualLoginCode:
		return "手动取码失败"
	default:
		return "失败"
	}
}

func validateExternalOAuthURL(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") {
		return "", false, errors.New("OAuth 链接必须是有效的 HTTPS 地址")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host != "auth.openai.com" {
		return "", false, errors.New("为避免泄露邮箱或验证码，只允许打开 auth.openai.com")
	}
	standard := strings.HasPrefix(strings.ToLower(parsed.Path), "/oauth/authorize") ||
		strings.HasPrefix(strings.ToLower(parsed.Path), "/api/accounts/authorize")
	return value, standard, nil
}
