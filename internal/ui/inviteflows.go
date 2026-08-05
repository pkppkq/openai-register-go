package ui

// 本文件把 Team/K12 的多个既有单步能力组合成可取消、可审计的 Wails
// 父任务。真实邮箱、注册、浏览器接受和 Session 刷新都经由下方 seam；
// 永久测试只替换 seam，不会打开浏览器、访问业务服务或租用手机号。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

const (
	JobTeamInviteScanJoinBatch JobKind = "team_invite_scan_join_batch"
	JobTeamInviteScanJoin      JobKind = "team_invite_scan_join"
	JobK12AcceptRefreshBatch   JobKind = "k12_accept_refresh_batch"
	JobK12AcceptRefresh        JobKind = "k12_accept_refresh"
	JobK12RegisterJoinBatch    JobKind = "k12_register_join_batch"
	JobK12RegisterJoin         JobKind = "k12_register_join"
)

func init() {
	for _, kind := range []JobKind{
		JobTeamInviteScanJoinBatch,
		JobTeamInviteScanJoin,
		JobK12AcceptRefreshBatch,
		JobK12AcceptRefresh,
		JobK12RegisterJoinBatch,
		JobK12RegisterJoin,
	} {
		networkJobKinds[kind] = true
	}
}

// TeamInviteScanJoinRequest 扫描所选邮箱的 Team 邀请并接受。流程可能在
// 缺少完整登录态时执行注册/登录，也会改变远端 workspace 成员状态。
type TeamInviteScanJoinRequest struct {
	Emails    []string `json:"emails"`
	Confirmed bool     `json:"confirmed"`
}

// K12InviteFlowRequest 是两个 K12 组合流程共用的显式输入。
type K12InviteFlowRequest struct {
	Emails      []string `json:"emails"`
	WorkspaceID string   `json:"workspaceId"`
	Confirmed   bool     `json:"confirmed"`
}

// InviteFlowAccountResult 是一个账号的组合流程摘要。邀请链接和响应正文仅
// 通过任务结果返回给当前前端，不写日志。
type InviteFlowAccountResult struct {
	Email           string                `json:"email"`
	Flow            string                `json:"flow"`
	Status          string                `json:"status"`
	WorkspaceID     string                `json:"workspaceId"`
	InviteURL       string                `json:"inviteUrl,omitempty"`
	RequestStatus   string                `json:"requestStatus,omitempty"`
	RequestResponse string                `json:"requestResponse,omitempty"`
	Authenticated   bool                  `json:"authenticated"`
	Session         *SessionRefreshResult `json:"session,omitempty"`
	Error           string                `json:"error,omitempty"`
}

// InviteFlowBatchResult 是父任务结束后可由 GetNetworkJobResult 读取的结果。
type InviteFlowBatchResult struct {
	Flow      string                    `json:"flow"`
	Accounts  []InviteFlowAccountResult `json:"accounts"`
	Succeeded int                       `json:"succeeded"`
	Failed    int                       `json:"failed"`
	Cancelled int                       `json:"cancelled"`
}

type inviteFlowMode string

const (
	inviteFlowTeamScan    inviteFlowMode = "team_invite_scan_join"
	inviteFlowK12Accept   inviteFlowMode = "k12_accept_refresh"
	inviteFlowK12Register inviteFlowMode = "k12_register_join"
)

type inviteFlowMailReader interface {
	Close() error
	WaitForTeamInvite(context.Context, float64, int) (string, error)
	WaitForLink(context.Context, string, float64, int) (string, error)
}

type inviteFlowAuthFunc func(
	context.Context,
	*App,
	string,
	models.MailAccount,
	string,
	bool,
	func(string),
) (worker.SessionInfo, error)

type inviteFlowAcceptFunc func(
	context.Context,
	models.MailAccount,
	string,
	string,
	string,
	func(string),
) (WorkspaceInviteResult, error)

type inviteFlowRefreshFunc func(
	context.Context,
	models.MailAccount,
	string,
	string,
	string,
	func(string),
) (SessionRefreshResult, error)

type inviteFlowK12RequestFunc func(context.Context, string, string, string) (int, string, error)

var inviteFlowNewMailReader = func(
	account *models.MailAccount,
	log mail.Log,
	proxyURL string,
) (inviteFlowMailReader, error) {
	return mail.CreateMailReader(account, log, proxyURL)
}

var inviteFlowAuthenticate inviteFlowAuthFunc = runInviteFlowAuthentication

var inviteFlowAcceptInvite inviteFlowAcceptFunc = func(
	ctx context.Context,
	account models.MailAccount,
	storageStateJSON, inviteURL, proxyURL string,
	log func(string),
) (WorkspaceInviteResult, error) {
	return workspaceInviteAcceptOne(ctx, account, storageStateJSON, inviteURL, proxyURL, log)
}

var inviteFlowRefreshSession inviteFlowRefreshFunc = func(
	ctx context.Context,
	account models.MailAccount,
	storageStateJSON, proxyURL, workspaceID string,
	log func(string),
) (SessionRefreshResult, error) {
	return sessionRefreshOne(ctx, account, storageStateJSON, proxyURL, workspaceID, log)
}

var inviteFlowRequestK12 inviteFlowK12RequestFunc = func(
	ctx context.Context,
	accessToken, workspaceID, proxyURL string,
) (int, string, error) {
	return networkRequestK12Invite(ctx, accessToken, workspaceID, proxyURL)
}

type inviteFlowJob struct {
	index       int
	account     models.MailAccount
	session     map[string]any
	settings    settings.Settings
	workspaceID string
	dynamic     string
}

// StartTeamInviteScanJoin 扫描 Team 邀请；缺少完整登录态时会先注册/登录，
// 随后接受邀请并刷新 Team Session。
func (a *App) StartTeamInviteScanJoin(req TeamInviteScanJoinRequest) (BatchSummary, error) {
	if !req.Confirmed {
		return BatchSummary{}, errors.New("扫描并接受 Team 邀请会改变远端成员状态，且缺少登录态时可能租号，必须先确认")
	}
	return a.startInviteFlowBatch(inviteFlowTeamScan, req.Emails, "")
}

// StartK12AcceptAndRefresh 请求 K12 邀请、等待 k12-invite 邮件、接受邀请
// 并刷新到目标 workspace。
func (a *App) StartK12AcceptAndRefresh(req K12InviteFlowRequest) (BatchSummary, error) {
	if !req.Confirmed {
		return BatchSummary{}, errors.New("K12 接受并刷新会请求并接受真实邀请，必须先确认")
	}
	return a.startInviteFlowBatch(inviteFlowK12Accept, req.Emails, req.WorkspaceID)
}

// StartK12RegisterAndJoin 先注册/登录取 Session，再完成请求、等待、接受和
// 刷新。注册可能租用手机号，因此后端强制要求 Confirmed。
func (a *App) StartK12RegisterAndJoin(req K12InviteFlowRequest) (BatchSummary, error) {
	if !req.Confirmed {
		return BatchSummary{}, errors.New("K12 一键注册加入可能租用短信号码并改变远端成员状态，必须先确认")
	}
	return a.startInviteFlowBatch(inviteFlowK12Register, req.Emails, req.WorkspaceID)
}

func (a *App) startInviteFlowBatch(
	mode inviteFlowMode,
	emails []string,
	workspaceID string,
) (BatchSummary, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return BatchSummary{}, err
	}
	st := settings.FromSnapshot(snapshot)
	accounts, skipped, err := a.resolveBatchSelection(snapshot, emails)
	if err != nil {
		return BatchSummary{}, err
	}
	if mode != inviteFlowTeamScan {
		workspaceID = strings.TrimSpace(workspaceID)
		if workspaceID == "" {
			workspaceID = strings.TrimSpace(st.K12WorkspaceID)
		}
		if workspaceID == "" {
			return BatchSummary{Skipped: skipped}, errors.New("请先填写 K12 Workspace ID")
		}
	}

	jobs := make([]inviteFlowJob, 0, len(accounts))
	authPool := inviteFlowAuthProxyPool(st)
	for _, original := range accounts {
		account := original
		if !inviteFlowMailboxReady(&account, st) {
			skipped = append(skipped, account.Email)
			continue
		}
		payload := networkSessionByEmail(snapshot, account.Email)
		switch mode {
		case inviteFlowK12Accept:
			if networkAccessTokenFromPayload(payload) == "" ||
				strings.TrimSpace(networkText(payload["storage_state_json"])) == "" {
				skipped = append(skipped, account.Email)
				continue
			}
		case inviteFlowK12Register:
			if strings.EqualFold(strings.TrimSpace(account.AccountType), "team") {
				skipped = append(skipped, account.Email)
				continue
			}
		}

		index := len(jobs)
		dynamic := ""
		switch mode {
		case inviteFlowTeamScan:
			dynamic = inviteFlowTeamDynamicProxy(payload, st, index)
		case inviteFlowK12Accept:
			dynamic = networkSessionDynamicProxy(payload, st)
		case inviteFlowK12Register:
			if len(authPool) > 0 {
				dynamic = authPool[index%len(authPool)]
			}
		}
		jobs = append(jobs, inviteFlowJob{
			index:       index,
			account:     account,
			session:     payload,
			settings:    st,
			workspaceID: workspaceID,
			dynamic:     dynamic,
		})
	}
	if len(jobs) == 0 {
		return BatchSummary{Skipped: skipped}, errors.New("没有资料完整、可执行的账号")
	}

	concurrency := inviteFlowConcurrency(mode, st, len(jobs), len(authPool))
	parentKind, _ := inviteFlowKinds(mode)
	ctx, cancel := context.WithCancel(context.Background())
	parentID, err := a.registerJob(parentKind, "", "", cancel)
	if err != nil {
		cancel()
		return BatchSummary{}, err
	}
	a.setBatchProgress(parentID, len(jobs), 0)
	log := a.jobLogger(parentID)
	for _, email := range skipped {
		log(fmt.Sprintf("跳过 %s：缺少本流程所需的邮箱、Session 或账号类型条件", email))
	}
	log(fmt.Sprintf("%s 批量启动：账号=%d，并发=%d", inviteFlowLabel(mode), len(jobs), concurrency))
	view, _ := a.jobView(parentID)
	go func() {
		defer cancel()
		a.runInviteFlowBatch(ctx, parentID, mode, jobs, concurrency, log)
	}()
	return BatchSummary{Job: view, Skipped: skipped}, nil
}

func inviteFlowMailboxReady(account *models.MailAccount, st settings.Settings) bool {
	if alias.AccountUsesCloudMail(
		account,
		st.CloudMailBase,
		st.CloudMailToken,
		st.CloudMailEnabled,
	) {
		return strings.TrimSpace(account.CloudMailBase) != "" &&
			strings.TrimSpace(account.CloudMailToken) != ""
	}
	return strings.TrimSpace(account.ClientID) != "" &&
		strings.TrimSpace(account.RefreshToken) != ""
}

func inviteFlowAuthProxyPool(st settings.Settings) []string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return nil
	}
	text := st.DynamicProxies
	if st.RegisterWithPaymentProxy {
		text = st.PaymentDynamicProxy
	}
	return proxypool.ParseProxyPoolText(text)
}

func inviteFlowTeamDynamicProxy(
	payload map[string]any,
	st settings.Settings,
	index int,
) string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return ""
	}
	local := proxypool.NormalizeProxyURL(st.LocalProxy)
	for _, key := range []string{
		"link_followup_proxy", "link_proxy", "link_create_proxy", "link_approve_proxy",
	} {
		candidate := proxypool.NormalizeProxyURL(networkText(payload[key]))
		if candidate != "" && candidate != local {
			return candidate
		}
	}
	pool := proxypool.ParseProxyPoolText(st.DynamicProxies)
	if len(pool) == 0 {
		return ""
	}
	return pool[index%len(pool)]
}

func inviteFlowConcurrency(
	mode inviteFlowMode,
	st settings.Settings,
	total int,
	authPoolSize int,
) int {
	concurrency := st.K12Concurrency
	switch mode {
	case inviteFlowTeamScan:
		concurrency = st.AuthConcurrency
	case inviteFlowK12Register:
		concurrency = min(st.K12Concurrency, st.AuthConcurrency)
		if authPoolSize > 0 {
			concurrency = min(concurrency, authPoolSize)
		}
	}
	return min(max(1, concurrency), total)
}

func inviteFlowKinds(mode inviteFlowMode) (JobKind, JobKind) {
	switch mode {
	case inviteFlowTeamScan:
		return JobTeamInviteScanJoinBatch, JobTeamInviteScanJoin
	case inviteFlowK12Accept:
		return JobK12AcceptRefreshBatch, JobK12AcceptRefresh
	default:
		return JobK12RegisterJoinBatch, JobK12RegisterJoin
	}
}

func inviteFlowLabel(mode inviteFlowMode) string {
	switch mode {
	case inviteFlowTeamScan:
		return "Team 邀请扫描加入"
	case inviteFlowK12Accept:
		return "K12 接受并刷新"
	default:
		return "K12 一键注册加入"
	}
}

func (a *App) runInviteFlowBatch(
	ctx context.Context,
	parentID string,
	mode inviteFlowMode,
	jobs []inviteFlowJob,
	concurrency int,
	log func(string),
) InviteFlowBatchResult {
	queue := make(chan inviteFlowJob, len(jobs))
	for _, job := range jobs {
		queue <- job
	}
	close(queue)

	results := make([]InviteFlowAccountResult, len(jobs))
	var done atomic.Int32
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-queue:
					if !ok {
						return
					}
					results[job.index] = a.runInviteFlowAccount(ctx, parentID, mode, job)
					completed := int(done.Add(1))
					a.setBatchProgress(parentID, len(jobs), completed)
				}
			}
		}()
	}
	workers.Wait()

	report := InviteFlowBatchResult{
		Flow:     string(mode),
		Accounts: results,
	}
	for index := range report.Accounts {
		result := &report.Accounts[index]
		if result.Email == "" {
			result.Email = jobs[index].account.Email
			result.Flow = string(mode)
			result.Status = string(StatusCancelled)
			result.Error = context.Canceled.Error()
		}
		switch result.Status {
		case string(StatusCancelled):
			report.Cancelled++
		default:
			if result.Error == "" {
				report.Succeeded++
			} else {
				report.Failed++
			}
		}
	}
	log(fmt.Sprintf("%s 批量结束：成功=%d，失败=%d，取消=%d",
		inviteFlowLabel(mode), report.Succeeded, report.Failed, report.Cancelled))
	a.markJobFinished(parentID, report, nil, ctx.Err() != nil)
	return report
}

func (a *App) runInviteFlowAccount(
	ctx context.Context,
	parentID string,
	mode inviteFlowMode,
	job inviteFlowJob,
) InviteFlowAccountResult {
	result := InviteFlowAccountResult{
		Email:       job.account.Email,
		Flow:        string(mode),
		WorkspaceID: job.workspaceID,
	}
	_, childKind := inviteFlowKinds(mode)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	childID, err := a.registerJob(childKind, job.account.Email, parentID, cancel)
	if err != nil {
		result.Status = inviteFlowFailureStatus(mode)
		result.Error = err.Error()
		return result
	}
	log := a.jobLogger(childID)

	switch mode {
	case inviteFlowTeamScan:
		result, err = a.runTeamInviteScanJoin(childCtx, childID, job, log)
	case inviteFlowK12Accept:
		result, err = a.runK12InviteFlow(childCtx, childID, mode, job, false, log)
	default:
		result, err = a.runK12InviteFlow(childCtx, childID, mode, job, true, log)
	}
	cancelled := childCtx.Err() != nil && err != nil
	if err != nil {
		result.Error = err.Error()
		if cancelled {
			result.Status = string(StatusCancelled)
		} else if result.Status == "" {
			result.Status = inviteFlowFailureStatus(mode)
		}
		if !cancelled {
			_ = a.persistInviteFlowFailure(job.account.Email, mode, result.Status, err)
		}
	}
	a.markJobFinished(childID, result, err, cancelled)
	return result
}

func (a *App) runTeamInviteScanJoin(
	ctx context.Context,
	jobID string,
	job inviteFlowJob,
	log func(string),
) (InviteFlowAccountResult, error) {
	result := InviteFlowAccountResult{
		Email: job.account.Email,
		Flow:  string(inviteFlowTeamScan),
	}
	if err := a.networkPatchState(job.account.Email,
		map[string]any{"status": "Team邀请扫描中"},
		map[string]any{"team_invite_join_error": ""},
	); err != nil {
		return result, err
	}

	inviteURL, err := a.waitForInviteLink(
		ctx, &job.account, job.settings, "", true, log,
	)
	if err != nil {
		return result, err
	}
	if !validTeamInviteURL(inviteURL) {
		return result, errors.New("邮箱返回的链接不是有效 ChatGPT Team/Business 邀请")
	}
	result.InviteURL = inviteURL
	log("已找到 ChatGPT Team/Business 邀请链接")

	accessToken := networkAccessTokenFromPayload(job.session)
	storageState := strings.TrimSpace(networkText(job.session["storage_state_json"]))
	if accessToken == "" || storageState == "" {
		log("当前账号没有完整登录态，开始自动注册/登录并获取 Session")
		auth, authErr := inviteFlowAuthenticate(
			ctx, a, jobID, job.account, job.dynamic, false, log,
		)
		if authErr != nil {
			return result, authErr
		}
		if strings.TrimSpace(auth.AccessToken) == "" ||
			strings.TrimSpace(auth.StorageStateJSON) == "" {
			return result, errors.New("自动登录完成后仍缺少 storage_state/access_token")
		}
		accessToken = strings.TrimSpace(auth.AccessToken)
		storageState = strings.TrimSpace(auth.StorageStateJSON)
		result.Authenticated = true
		if err := a.persistInviteFlowAuthSession(job.account.Email, auth, "Team邀请扫描中"); err != nil {
			return result, err
		}
	}

	proxySession, proxyURL, err := a.networkProxy(job.settings, job.dynamic, log)
	if err != nil {
		return result, err
	}
	defer proxySession.Close()

	accepted, refreshed, workspaceID, err := a.acceptInviteAndRefresh(
		ctx, inviteFlowTeamScan, job.account, storageState,
		inviteURL, "", proxyURL, log,
	)
	if err != nil {
		return result, err
	}
	plan := openai.ClassifyChatGPTPlanText(networkText(refreshed.AccessSummary["plan_type"]))
	if plan != "team" {
		return result, fmt.Errorf("邀请已接受，但刷新结果仍不是 Team: plan=%s", firstNetworkText(plan, "unknown"))
	}
	if err := a.persistInviteFlowRefresh(
		job.account.Email, inviteFlowTeamScan, accepted, refreshed, workspaceID,
	); err != nil {
		return result, err
	}
	result.Status = "Team邀请已加入"
	result.WorkspaceID = workspaceID
	result.Session = &refreshed
	log("Team 邀请已接受并刷新 Session")
	return result, nil
}

func (a *App) runK12InviteFlow(
	ctx context.Context,
	jobID string,
	mode inviteFlowMode,
	job inviteFlowJob,
	authenticate bool,
	log func(string),
) (InviteFlowAccountResult, error) {
	result := InviteFlowAccountResult{
		Email:       job.account.Email,
		Flow:        string(mode),
		WorkspaceID: job.workspaceID,
	}
	startStatus := "K12接受中"
	if authenticate {
		startStatus = "K12一键注册中"
	}
	if err := a.networkPatchState(job.account.Email,
		map[string]any{"status": startStatus},
		map[string]any{string(mode) + "_error": ""},
	); err != nil {
		return result, err
	}

	accessToken := networkAccessTokenFromPayload(job.session)
	storageState := strings.TrimSpace(networkText(job.session["storage_state_json"]))
	if authenticate {
		auth, err := inviteFlowAuthenticate(
			ctx, a, jobID, job.account, job.dynamic, job.settings.Headless, log,
		)
		if err != nil {
			return result, err
		}
		if strings.TrimSpace(auth.AccessToken) == "" ||
			strings.TrimSpace(auth.StorageStateJSON) == "" {
			return result, errors.New("注册/登录完成后未获取到 K12 所需 Session / storage_state")
		}
		accessToken = strings.TrimSpace(auth.AccessToken)
		storageState = strings.TrimSpace(auth.StorageStateJSON)
		result.Authenticated = true
		if err := a.persistInviteFlowAuthSession(job.account.Email, auth, startStatus); err != nil {
			return result, err
		}
	}
	if accessToken == "" || storageState == "" {
		return result, errors.New("缺少 K12 流程所需的 Access Token 或 storage_state_json")
	}

	proxySession, proxyURL, err := a.networkProxy(job.settings, job.dynamic, log)
	if err != nil {
		return result, err
	}
	defer proxySession.Close()

	status, response, requestErr := inviteFlowRequestK12(
		ctx, accessToken, job.workspaceID, proxyURL,
	)
	response = strings.TrimSpace(response)
	statusText := strconv.Itoa(status)
	if requestErr != nil {
		statusText = "ERROR"
		response = requestErr.Error()
	}
	result.RequestStatus = statusText
	result.RequestResponse = networkTruncate(response, 4000)
	requestOK := requestErr == nil && status >= 200 && status < 300
	requestAccountStatus := "K12失败"
	if requestOK {
		requestAccountStatus = "K12请求成功"
	}
	persistErr := a.networkPatchState(job.account.Email,
		map[string]any{"status": requestAccountStatus},
		map[string]any{
			"k12_workspace_id":      job.workspaceID,
			"k12_status":            statusText,
			"k12_response":          result.RequestResponse,
			"k12_requested_at":      networkNowUTC(),
			string(mode) + "_error": "",
		},
	)
	if persistErr != nil {
		return result, networkJoin(requestErr, persistErr)
	}
	if requestErr != nil {
		return result, requestErr
	}
	if !requestOK {
		return result, networkHTTPError("K12 请求邀请", status, response)
	}
	log(fmt.Sprintf("K12 邀请请求成功：HTTP %d", status))

	inviteURL, err := a.waitForInviteLink(
		ctx, &job.account, job.settings, proxyURL, false, log,
	)
	if err != nil {
		return result, err
	}
	if !validK12InviteURL(inviteURL) {
		return result, errors.New("邮箱返回的链接不是有效 ChatGPT K12 邀请")
	}
	result.InviteURL = inviteURL
	workspaceID := workspaceIDFromInviteURL(inviteURL)
	if workspaceID == "" {
		workspaceID = job.workspaceID
	}
	result.WorkspaceID = workspaceID
	log("已收到 K12 邀请链接，开始接受并刷新 Session")

	accepted, refreshed, workspaceID, err := a.acceptInviteAndRefresh(
		ctx, mode, job.account, storageState,
		inviteURL, workspaceID, proxyURL, log,
	)
	if err != nil {
		return result, err
	}
	if err := a.persistInviteFlowRefresh(
		job.account.Email, mode, accepted, refreshed, workspaceID,
	); err != nil {
		return result, err
	}
	result.Status = "K12接受已刷新"
	result.WorkspaceID = workspaceID
	result.Session = &refreshed
	log("K12 邀请已接受并刷新 Session")
	return result, nil
}

func (a *App) waitForInviteLink(
	ctx context.Context,
	account *models.MailAccount,
	st settings.Settings,
	proxyURL string,
	team bool,
	log func(string),
) (string, error) {
	oldRefreshToken := account.RefreshToken
	secrets := mailboxSecrets(*account, st, oldRefreshToken, proxyURL)
	safeLog := func(line string) {
		log(mailboxRedact(line, secrets...))
	}
	reader, err := inviteFlowNewMailReader(account, mail.Log(safeLog), proxyURL)
	if err != nil {
		return "", mailboxSafeError(err, secrets...)
	}
	var inviteURL string
	if team {
		inviteURL, err = reader.WaitForTeamInvite(ctx, 0, 120)
	} else {
		inviteURL, err = reader.WaitForLink(ctx, "k12-invite", 0, 240)
	}
	closeErr := reader.Close()
	persistErr := a.persistMailboxRefreshToken(
		account.Email, oldRefreshToken, account.RefreshToken, account.Raw,
	)
	return strings.TrimSpace(inviteURL), errors.Join(
		mailboxSafeError(err, secrets...),
		mailboxSafeError(closeErr, secrets...),
		mailboxSafeError(persistErr, secrets...),
	)
}

func validTeamInviteURL(raw string) bool {
	if !validChatGPTInviteURL(raw) {
		return false
	}
	lower := strings.ToLower(raw)
	return !strings.Contains(lower, "k12-invite") &&
		!strings.Contains(lower, "teacher")
}

func validK12InviteURL(raw string) bool {
	if !validChatGPTInviteURL(raw) {
		return false
	}
	return strings.Contains(strings.ToLower(raw), "k12-invite")
}

func (a *App) acceptInviteAndRefresh(
	ctx context.Context,
	mode inviteFlowMode,
	account models.MailAccount,
	storageStateJSON string,
	inviteURL string,
	workspaceID string,
	proxyURL string,
	log func(string),
) (WorkspaceInviteResult, SessionRefreshResult, string, error) {
	accepted, err := inviteFlowAcceptInvite(
		ctx, account, storageStateJSON, inviteURL, proxyURL, log,
	)
	if err != nil {
		return WorkspaceInviteResult{}, SessionRefreshResult{}, workspaceID, err
	}
	if strings.TrimSpace(accepted.StorageStateJSON) == "" {
		return accepted, SessionRefreshResult{}, workspaceID,
			errors.New("邀请已点击，但浏览器未返回新的 storage_state_json")
	}
	if accepted.AcceptedAt == "" {
		accepted.AcceptedAt = networkNowUTC()
	}
	if accepted.InviteURL == "" {
		accepted.InviteURL = inviteURL
	}
	if accepted.WorkspaceID != "" {
		workspaceID = strings.TrimSpace(accepted.WorkspaceID)
	}
	if workspaceID == "" {
		workspaceID = workspaceIDFromInviteURL(inviteURL)
	}
	accepted.Kind = inviteFlowInviteKind(mode)
	accepted.WorkspaceID = workspaceID
	if err := a.persistInviteAcceptance(account.Email, mode, accepted, workspaceID); err != nil {
		return accepted, SessionRefreshResult{}, workspaceID, err
	}

	refreshed, err := inviteFlowRefreshSession(
		ctx, account, accepted.StorageStateJSON, proxyURL, workspaceID, log,
	)
	if err != nil {
		return accepted, refreshed, workspaceID, err
	}
	if strings.TrimSpace(refreshed.AccessToken) == "" ||
		strings.TrimSpace(refreshed.StorageStateJSON) == "" {
		return accepted, refreshed, workspaceID,
			errors.New("邀请接受后刷新 Session 未返回 Access Token 或 storage_state_json")
	}
	if refreshed.WorkspaceID == "" {
		refreshed.WorkspaceID = workspaceID
	}
	return accepted, refreshed, workspaceID, nil
}

func inviteFlowInviteKind(mode inviteFlowMode) string {
	if mode == inviteFlowTeamScan {
		return "team"
	}
	return "k12"
}

func (a *App) persistInviteAcceptance(
	email string,
	mode inviteFlowMode,
	accepted WorkspaceInviteResult,
	workspaceID string,
) error {
	kind := inviteFlowInviteKind(mode)
	accountPatch := map[string]any{
		"status": inviteAcceptedStatus(kind, false),
	}
	if len(accepted.Fingerprint) > 0 {
		accountPatch["browser_fingerprint"] = accepted.Fingerprint
	}
	return a.networkPatchState(email, accountPatch, map[string]any{
		"storage_state_json":    accepted.StorageStateJSON,
		kind + "_invite_url":    accepted.InviteURL,
		kind + "_workspace_id":  workspaceID,
		kind + "_accept_result": "accepted",
		kind + "_accepted_at":   accepted.AcceptedAt,
		"target_workspace_id":   workspaceID,
		string(mode) + "_error": "",
	})
}

func (a *App) persistInviteFlowRefresh(
	email string,
	mode inviteFlowMode,
	accepted WorkspaceInviteResult,
	refreshed SessionRefreshResult,
	workspaceID string,
) error {
	kind := inviteFlowInviteKind(mode)
	status := "K12接受已刷新"
	accountType := ""
	if kind == "team" {
		status = "Team邀请已加入"
		accountType = "team"
	} else {
		plan := openai.ClassifyChatGPTPlanText(networkText(refreshed.AccessSummary["plan_type"]))
		if sessionPlanTypes[plan] {
			accountType = plan
		}
	}
	accountPatch := map[string]any{"status": status}
	if accountType != "" {
		accountPatch["account_type"] = accountType
	}
	fingerprint := refreshed.Fingerprint
	if len(fingerprint) == 0 {
		fingerprint = accepted.Fingerprint
	}
	if len(fingerprint) > 0 {
		accountPatch["browser_fingerprint"] = fingerprint
	}
	return a.networkPatchState(email, accountPatch, map[string]any{
		"access_token":          refreshed.AccessToken,
		"session_json":          refreshed.SessionJSON,
		"storage_state_json":    refreshed.StorageStateJSON,
		"access_summary":        refreshed.AccessSummary,
		"session_refreshed_at":  refreshed.RefreshedAt,
		"target_workspace_id":   workspaceID,
		kind + "_workspace_id":  workspaceID,
		kind + "_invite_url":    accepted.InviteURL,
		kind + "_accept_result": "accepted",
		kind + "_accepted_at":   accepted.AcceptedAt,
		string(mode) + "_error": "",
	})
}

func (a *App) persistInviteFlowAuthSession(
	email string,
	session worker.SessionInfo,
	status string,
) error {
	summary := openai.SummarizeChatGPTAccessToken(session.AccessToken)
	accountPatch := map[string]any{"status": status}
	plan := openai.ClassifyChatGPTPlanText(networkText(summary["plan_type"]))
	if planTypesAdopted[plan] {
		accountPatch["account_type"] = plan
	}
	return a.networkPatchState(email, accountPatch, map[string]any{
		"access_token":       session.AccessToken,
		"session_json":       session.SessionJSON,
		"storage_state_json": session.StorageStateJSON,
		"access_summary":     summary,
	})
}

func (a *App) persistInviteFlowFailure(
	email string,
	mode inviteFlowMode,
	status string,
	err error,
) error {
	return a.networkPatchState(email,
		map[string]any{"status": status},
		map[string]any{
			string(mode) + "_error": networkTruncate(err.Error(), 800),
		},
	)
}

func inviteFlowFailureStatus(mode inviteFlowMode) string {
	switch mode {
	case inviteFlowTeamScan:
		return "Team邀请失败"
	case inviteFlowK12Accept:
		return "K12接受失败"
	default:
		return "K12失败"
	}
}

func runInviteFlowAuthentication(
	ctx context.Context,
	a *App,
	jobID string,
	account models.MailAccount,
	dynamicProxy string,
	headless bool,
	log func(string),
) (worker.SessionInfo, error) {
	cfg, resources, err := a.workerConfigProxy(
		ctx, JobRegister, account, &dynamicProxy, log,
	)
	if err != nil {
		return worker.SessionInfo{}, err
	}
	cfg.Headless = headless
	cfg.InputCallback = a.inputCallback(ctx, jobID)
	parkedBefore := worker.ParkedBrowserGeneration(account.Email)
	defer func() {
		if worker.AttachParkedCleanupSince(account.Email, parkedBefore, resources.Close) {
			return
		}
		resources.Close()
	}()
	result, err := worker.New(cfg).Run(ctx)
	if err != nil {
		return worker.SessionInfo{}, err
	}
	if result == nil {
		return worker.SessionInfo{}, errors.New("注册/登录流程没有返回 Session")
	}
	return *result, nil
}
