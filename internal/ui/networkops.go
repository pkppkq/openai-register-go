package ui

// 本文件承载 UI_SPEC 中纯 HTTP/邮箱类远程操作的 Wails 绑定。
//
// 这些操作都可能持续数秒，因此绑定只负责校验、登记任务并立即返回 JobView；
// 真正调用在后台 goroutine 中完成。测试通过下方包级 seam 注入假函数，绝不
// 触发真实 OpenAI、Team、Stripe、SMSBower 或 Cloud Mail 接口。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/opll"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
)

// 远程操作使用独立 JobKind，避免与会启动浏览器或租号的 worker 入口混淆。
const (
	JobRefreshAccountType JobKind = "refresh_account_type"
	JobTeamInvite         JobKind = "team_invite"
	JobTeamLeave          JobKind = "team_leave"
	JobK12RequestInvite   JobKind = "k12_request_invite"
	JobTrialEligibility   JobKind = "trial_eligibility"
	JobDeactivationScan   JobKind = "deactivation_scan"
	JobTurnstileProbe     JobKind = "turnstile_probe"
	JobSMSBowerRead       JobKind = "smsbower_read"
	JobCloudMailProbe     JobKind = "cloud_mail_probe"
	JobCloudMailToken     JobKind = "cloud_mail_token"
	JobCPAExportRefresh   JobKind = "cpa_export_refresh"
	JobSub2APIExport      JobKind = "sub2api_export"
)

var networkJobKinds = map[JobKind]bool{
	JobRefreshAccountType: true,
	JobTeamInvite:         true,
	JobTeamLeave:          true,
	JobK12RequestInvite:   true,
	JobTrialEligibility:   true,
	JobDeactivationScan:   true,
	JobTurnstileProbe:     true,
	JobSMSBowerRead:       true,
	JobCloudMailProbe:     true,
	JobCloudMailToken:     true,
	JobCPAExportRefresh:   true,
	JobSub2APIExport:      true,
}

// NetworkJobResult 让无需落盘的探测结果（余额、健康检查等）可被前端读取。
type NetworkJobResult struct {
	Job    JobView `json:"job"`
	Result any     `json:"result"`
}

// GetNetworkJobResult 返回已结束远程任务的结果。
func (a *App) GetNetworkJobResult(id string) (NetworkJobResult, error) {
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()
	j := a.jobs.jobs[id]
	if j == nil {
		return NetworkJobResult{}, fmt.Errorf("任务不存在: %s", id)
	}
	if !networkJobKinds[j.view.Kind] {
		return NetworkJobResult{}, fmt.Errorf("任务不是远程操作: %s", id)
	}
	if j.view.Status == StatusRunning {
		return NetworkJobResult{Job: j.view}, fmt.Errorf("任务尚未结束: %s", id)
	}
	return NetworkJobResult{Job: j.view, Result: j.result}, nil
}

type networkRun func(context.Context, func(string)) (any, error)

// startNetworkJob 统一远程绑定的登记、取消与完成状态。
func (a *App) startNetworkJob(kind JobKind, email string, run networkRun) (JobView, error) {
	return a.startNetworkJobWithLogEmail(kind, email, email, run)
}

// startNetworkJobWithLogEmail 允许非账户远程任务保留稳定冲突身份，同时明确
// 指定结构化日志是否应进入某个账户缓冲区。
func (a *App) startNetworkJobWithLogEmail(kind JobKind, email, logEmail string, run networkRun) (JobView, error) {
	ctx, cancel := context.WithCancel(context.Background())
	id, err := a.registerJobWithLogEmail(kind, email, logEmail, "", cancel)
	if err != nil {
		cancel()
		return JobView{}, err
	}
	view, _ := a.jobView(id)

	go func() {
		defer cancel()
		result, runErr := run(ctx, a.jobLogger(id))
		// 只有操作确实以取消错误退出时才标记 cancelled。某些既有领域
		// 函数没有 context 参数；若请求已成功返回，即使用户稍晚点击停止，
		// 也必须保存并报告真实远程结果，不能假装外部副作用没有发生。
		cancelled := ctx.Err() != nil && runErr != nil
		a.markJobFinished(id, result, runErr, cancelled)
	}()

	return view, nil
}

// ---------------------------------------------------------------------------
// 可注入网络 seam
// ---------------------------------------------------------------------------

type detectAccountTypeFunc func(context.Context, string, string) (string, string, string, error)
type refreshAccessTokenFunc func(context.Context, string, string) (map[string]any, error)
type teamInviteFunc func(context.Context, string, string, string, string) (int, string, error)
type teamLeaveFunc func(context.Context, string, string, string, string) (int, string, openai.TeamLeaveDetail, error)
type k12InviteFunc func(context.Context, string, string, string) (int, string, error)
type trialEligibilityFunc func(context.Context, string, string, string) (opll.TrialEligibility, error)
type deactivationScanFunc func(context.Context, *models.MailAccount, string, int, int, func(string)) (mail.DeactivationResult, error)
type smsbowerReadFunc func(context.Context, SMSBowerReadRequest) (SMSBowerReadResult, error)
type turnstileProbeFunc func(context.Context, string) (TurnstileProbeResult, error)
type cloudMailProbeFunc func(context.Context, string, string, string) error
type cloudMailTokenFunc func(context.Context, string, string, string) (string, error)

var networkDetectAccountType detectAccountTypeFunc = func(ctx context.Context, refreshToken, proxyURL string) (string, string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	return openai.DetectOpenAIAccountType(refreshToken, proxyURL)
}

var networkRefreshAccessToken refreshAccessTokenFunc = func(ctx context.Context, refreshToken, proxyURL string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return openai.RefreshOpenAIAccessToken(refreshToken, proxyURL)
}

var networkSendTeamInvite teamInviteFunc = func(ctx context.Context, accessToken, accountID, targetEmail, proxyURL string) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	return openai.ChatGPTTeamSendInvite(accessToken, accountID, targetEmail, proxyURL)
}

var networkLeaveTeam teamLeaveFunc = func(ctx context.Context, accessToken, accountID, memberEmail, proxyURL string) (int, string, openai.TeamLeaveDetail, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", openai.TeamLeaveDetail{}, err
	}
	return openai.ChatGPTTeamLeaveWorkspace(accessToken, accountID, memberEmail, proxyURL)
}

var networkRequestK12Invite k12InviteFunc = func(ctx context.Context, accessToken, workspaceID, proxyURL string) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	return openai.K12RequestWorkspaceInvite(accessToken, workspaceID, proxyURL)
}

var networkDetectTrialEligibility trialEligibilityFunc = func(ctx context.Context, accessToken, proxyURL, country string) (opll.TrialEligibility, error) {
	if err := ctx.Err(); err != nil {
		return opll.TrialEligibility{}, err
	}
	return opll.DetectPlusTrialEligibility(accessToken, proxyURL, country)
}

var networkScanDeactivation deactivationScanFunc = func(ctx context.Context, account *models.MailAccount, proxyURL string, days, maxMessages int, log func(string)) (mail.DeactivationResult, error) {
	if err := ctx.Err(); err != nil {
		return mail.DeactivationResult{}, err
	}
	reader, err := mail.CreateMailReader(account, mail.Log(log), proxyURL)
	if err != nil {
		return mail.DeactivationResult{}, err
	}
	defer reader.Close()
	result, err := reader.ScanOpenAIDeactivationNotice(days, maxMessages)
	if err != nil {
		return mail.DeactivationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return mail.DeactivationResult{}, err
	}
	return result, nil
}

var networkReadSMSBower smsbowerReadFunc = readSMSBower
var networkProbeTurnstile turnstileProbeFunc = probeTurnstile

var networkProbeCloudMail cloudMailProbeFunc = func(ctx context.Context, baseURL, token, probeEmail string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := mail.NewCloudMailClient(baseURL, token)
	if err != nil {
		return err
	}
	_, err = client.ListMails(probeEmail, "", 1)
	if err != nil {
		return err
	}
	return ctx.Err()
}

var networkGenerateCloudMailToken cloudMailTokenFunc = func(ctx context.Context, baseURL, adminEmail, adminPassword string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return mail.CloudMailGenerateToken(baseURL, adminEmail, adminPassword)
}

// ---------------------------------------------------------------------------
// 账号与 Session 读取/写入
// ---------------------------------------------------------------------------

type networkAccountData struct {
	Account  models.MailAccount
	Session  map[string]any
	Settings settings.Settings
}

func (a *App) networkAccountData(email string) (networkAccountData, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return networkAccountData{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	want := models.NormalizeEmailAddress(email)
	if want == "" {
		return networkAccountData{}, errors.New("未指定账号邮箱")
	}
	for _, row := range accountsFromSnapshot(snapshot) {
		if !strings.EqualFold(row.Email, want) {
			continue
		}
		payload := networkSessionByEmail(snapshot, row.Email)
		return networkAccountData{
			Account:  row,
			Session:  payload,
			Settings: settings.FromSnapshot(snapshot),
		}, nil
	}
	return networkAccountData{}, fmt.Errorf("账号不存在: %s", email)
}

func networkSessionByEmail(snapshot map[string]any, email string) map[string]any {
	sessions, _ := snapshot["session_results"].(map[string]any)
	for key, value := range sessions {
		if strings.EqualFold(key, email) {
			if payload, ok := value.(map[string]any); ok {
				return payload
			}
		}
	}
	return map[string]any{}
}

// networkPatchState 只修改指定账号和 Session 字段，保留 Python 写入的未知键。
func (a *App) networkPatchState(email string, accountFields, sessionFields map[string]any) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		rows, _ := snapshot["accounts"].([]any)
		actualEmail := ""
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			rowEmail := networkText(m["email"])
			if !strings.EqualFold(rowEmail, email) {
				continue
			}
			actualEmail = rowEmail
			for key, value := range accountFields {
				m[key] = value
			}
			break
		}
		if actualEmail == "" {
			return snapshot, nil, fmt.Errorf("账号不存在: %s", email)
		}
		if len(sessionFields) == 0 {
			return snapshot, map[string]bool{}, nil
		}

		sessions, _ := snapshot["session_results"].(map[string]any)
		if sessions == nil {
			sessions = map[string]any{}
		}
		sessionKey := actualEmail
		for key := range sessions {
			if strings.EqualFold(key, actualEmail) {
				sessionKey = key
				break
			}
		}
		payload, _ := sessions[sessionKey].(map[string]any)
		if payload == nil {
			payload = map[string]any{}
		}
		for key, value := range sessionFields {
			payload[key] = value
		}
		sessions[sessionKey] = payload
		snapshot["session_results"] = sessions
		return snapshot, map[string]bool{sessionKey: true}, nil
	})
}

func networkText(value any) string {
	if !settings.PyTruthy(value) {
		return ""
	}
	return strings.TrimSpace(settings.PyStr(value))
}

func networkCancelled(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func networkJoin(primary, persistErr error) error {
	switch {
	case primary == nil:
		return persistErr
	case persistErr == nil:
		return primary
	default:
		return errors.Join(primary, persistErr)
	}
}

func networkHTTPError(label string, status int, body string) error {
	return fmt.Errorf("%s失败: HTTP %d %s", label, status, networkTruncate(body, 800))
}

func networkTruncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func networkNowLocal() string {
	return time.Now().Format("2006-01-02T15:04:05")
}

func networkNowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// networkSessionDynamicProxy 对齐 Python 的 _k12_dynamic_proxy_for_payload。
func networkSessionDynamicProxy(payload map[string]any, st settings.Settings) string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return ""
	}
	local := proxypool.NormalizeProxyURL(st.LocalProxy)
	for _, key := range []string{"link_followup_proxy", "link_proxy", "link_create_proxy", "link_approve_proxy"} {
		proxy := proxypool.NormalizeProxyURL(networkText(payload[key]))
		if proxy != "" && proxy != local {
			return proxy
		}
	}
	for _, poolText := range []string{st.DynamicProxies, st.PaymentDynamicProxy, st.FollowupDynamicProxy} {
		for _, proxy := range proxypool.ParseProxyPoolText(poolText) {
			if proxy != "" && proxy != local {
				return proxy
			}
		}
	}
	return ""
}

func networkLoginDynamicProxy(st settings.Settings) string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return ""
	}
	return firstProxy(st.DynamicProxies)
}

func (a *App) networkProxy(st settings.Settings, dynamic string, log func(string)) (*proxySession, string, error) {
	session, err := a.openProxySession(st, dynamic, log)
	if err != nil {
		return nil, "", err
	}
	return session, session.Config.ChainURL, nil
}

// ---------------------------------------------------------------------------
// 刷新账号类型
// ---------------------------------------------------------------------------

type RefreshAccountTypeRequest struct {
	Email string `json:"email"`
}

type RefreshAccountTypeResult struct {
	AccountType         string `json:"accountType"`
	Detail              string `json:"detail"`
	RefreshTokenRotated bool   `json:"refreshTokenRotated"`
}

// StartRefreshAccountType 使用账号已保存的 OpenAI RT 刷新套餐类型。
func (a *App) StartRefreshAccountType(req RefreshAccountTypeRequest) (JobView, error) {
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	refreshToken := strings.TrimSpace(data.Account.OpenaiRT)
	if refreshToken == "" {
		return JobView{}, errors.New("这个邮箱还没有 rt_token，请先 OAuth授权取RT")
	}

	return a.startNetworkJob(JobRefreshAccountType, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkLoginDynamicProxy(data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		log("刷新类型中")
		accountType, detail, newRT, callErr := networkDetectAccountType(ctx, refreshToken, proxyURL)
		if callErr != nil {
			if networkCancelled(ctx, callErr) {
				return nil, callErr
			}
			persistErr := a.networkPatchState(data.Account.Email, map[string]any{"status": "刷新失败"}, nil)
			return nil, networkJoin(callErr, persistErr)
		}
		accountType = strings.ToLower(strings.TrimSpace(accountType))
		if accountType == "" {
			return nil, errors.New("OpenAI 未返回账号类型")
		}
		if strings.TrimSpace(newRT) == "" {
			newRT = refreshToken
		}
		status := "Free"
		switch accountType {
		case "team":
			status = "Team"
		case "plus":
			status = "已绑定手机号"
		}
		result := RefreshAccountTypeResult{
			AccountType:         accountType,
			Detail:              detail,
			RefreshTokenRotated: newRT != refreshToken,
		}
		persistErr := a.networkPatchState(data.Account.Email, map[string]any{
			"account_type": accountType,
			"openai_rt":    newRT,
			"status":       status,
		}, nil)
		if persistErr == nil {
			log(fmt.Sprintf("当前类型: %s (%s)", accountType, detail))
		}
		return result, persistErr
	})
}

// ---------------------------------------------------------------------------
// Team 邀请与退出
// ---------------------------------------------------------------------------

type TeamInviteRequest struct {
	Email               string `json:"email"`
	TargetEmail         string `json:"targetEmail"`
	ConfirmBillableSeat bool   `json:"confirmBillableSeat"`
}

type TeamInviteResult struct {
	TargetEmail string `json:"targetEmail"`
	Status      string `json:"status"`
	Response    string `json:"response"`
	AccountID   string `json:"accountId"`
	Hint        string `json:"hint"`
	SentAt      string `json:"sentAt"`
}

type TeamLeaveRequest struct {
	Email     string `json:"email"`
	Confirmed bool   `json:"confirmed"`
}

type TeamLeaveResult struct {
	Status    string                 `json:"status"`
	Response  string                 `json:"response"`
	AccountID string                 `json:"accountId"`
	Detail    openai.TeamLeaveDetail `json:"detail"`
	LeftAt    string                 `json:"leftAt"`
}

type networkTeamAccessData struct {
	AccessToken     string
	AccountID       string
	AccessSummary   map[string]any
	NewRefreshToken string
}

// StartTeamInvite 发送会新增计费席位的 Team 邀请。
func (a *App) StartTeamInvite(req TeamInviteRequest) (JobView, error) {
	if !req.ConfirmBillableSeat {
		return JobView{}, errors.New("Team 邀请会新增计费席位，必须由前端确认后再调用")
	}
	target := models.NormalizeEmailAddress(req.TargetEmail)
	if !strings.Contains(target, "@") {
		return JobView{}, fmt.Errorf("目标邮箱格式错误: %s", target)
	}
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(data.Account.AccountType), "team") {
		return JobView{}, errors.New("邀请成员请只选择已标记为 Team 的账号")
	}
	if networkAccessTokenFromPayload(data.Session) == "" && strings.TrimSpace(data.Account.OpenaiRT) == "" {
		return JobView{}, errors.New("当前 Team 账号没有 Access Token，也没有可刷新的 OpenAI RT")
	}

	return a.startNetworkJob(JobTeamInvite, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkSessionDynamicProxy(data.Session, data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		access, err := networkResolveTeamAccess(ctx, data.Account, data.Session, proxyURL)
		if err != nil {
			return nil, err
		}
		log(fmt.Sprintf("Team 邀请成员启动: 邀请者=%s 目标=%s", data.Account.Email, target))
		status, body, callErr := networkSendTeamInvite(ctx, access.AccessToken, access.AccountID, target, proxyURL)
		if callErr != nil && networkCancelled(ctx, callErr) {
			return nil, callErr
		}
		stamp := networkNowUTC()
		statusText := strconv.Itoa(status)
		if callErr != nil {
			statusText = "ERROR"
			body = callErr.Error()
		}
		hint := openai.TeamInviteFailureHint(status, body)
		result := TeamInviteResult{
			TargetEmail: target,
			Status:      statusText,
			Response:    networkTruncate(strings.TrimSpace(body), 4000),
			AccountID:   access.AccountID,
			Hint:        hint,
			SentAt:      stamp,
		}
		ok := callErr == nil && status >= 200 && status < 300
		accountStatus := "Team邀请失败"
		if ok {
			accountStatus = "Team邀请已发送"
		}
		accountPatch := map[string]any{"status": accountStatus}
		if access.NewRefreshToken != "" {
			accountPatch["openai_rt"] = access.NewRefreshToken
		}
		persistErr := a.networkPatchState(data.Account.Email, accountPatch, map[string]any{
			"access_token":             access.AccessToken,
			"access_summary":           access.AccessSummary,
			"account_id":               access.AccountID,
			"chatgpt_account_id":       access.AccountID,
			"team_workspace_id":        access.AccountID,
			"team_invite_target_email": target,
			"team_invite_status":       statusText,
			"team_invite_response":     result.Response,
			"team_invite_sent_at":      stamp,
		})
		if ok {
			log(fmt.Sprintf("Team 邀请成员成功: HTTP %d target=%s", status, target))
			return result, persistErr
		}
		if hint != "" {
			log(hint)
		}
		if callErr == nil {
			callErr = networkHTTPError("Team 邀请成员", status, body)
		}
		return result, networkJoin(callErr, persistErr)
	})
}

// StartTeamLeave 让当前成员主动退出 Team；Owner 会由领域函数拦截。
func (a *App) StartTeamLeave(req TeamLeaveRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("退出 Team 会释放席位并使当前 Team Session 失效，必须先确认")
	}
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(data.Account.AccountType), "team") {
		return JobView{}, errors.New("选中账号当前未标记为 Team，请先刷新 Session 确认工作空间")
	}
	if networkAccessTokenFromPayload(data.Session) == "" && strings.TrimSpace(data.Account.OpenaiRT) == "" {
		return JobView{}, errors.New("当前 Team 账号没有 Access Token，也没有可刷新的 OpenAI RT")
	}

	return a.startNetworkJob(JobTeamLeave, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkSessionDynamicProxy(data.Session, data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		access, err := networkResolveTeamAccess(ctx, data.Account, data.Session, proxyURL)
		if err != nil {
			return nil, err
		}
		status, body, detail, callErr := networkLeaveTeam(ctx, access.AccessToken, access.AccountID, data.Account.Email, proxyURL)
		if callErr != nil && networkCancelled(ctx, callErr) {
			return nil, callErr
		}
		stamp := networkNowUTC()
		statusText := strconv.Itoa(status)
		if callErr != nil {
			statusText = "ERROR"
			body = callErr.Error()
		}
		result := TeamLeaveResult{
			Status:    statusText,
			Response:  networkTruncate(strings.TrimSpace(body), 4000),
			AccountID: access.AccountID,
			Detail:    detail,
			LeftAt:    stamp,
		}
		ok := callErr == nil && status >= 200 && status < 300
		accountStatus := "Team退出失败"
		if ok {
			accountStatus = "已退出Team（待刷新）"
		}
		accountPatch := map[string]any{"status": accountStatus}
		if access.NewRefreshToken != "" {
			accountPatch["openai_rt"] = access.NewRefreshToken
		}
		persistErr := a.networkPatchState(data.Account.Email, accountPatch, map[string]any{
			"access_token":         access.AccessToken,
			"access_summary":       access.AccessSummary,
			"account_id":           access.AccountID,
			"chatgpt_account_id":   access.AccountID,
			"team_leave_status":    statusText,
			"team_leave_response":  result.Response,
			"team_leave_role":      detail.Role,
			"team_leave_member_id": detail.MemberID,
			"team_left_at":         stamp,
		})
		if ok {
			log(fmt.Sprintf("Team 退出成功: HTTP %d，席位已释放；请刷新 Session 获取当前套餐", status))
			return result, persistErr
		}
		if callErr == nil {
			callErr = networkHTTPError("Team 退出", status, body)
		}
		return result, networkJoin(callErr, persistErr)
	})
}

func networkResolveTeamAccess(ctx context.Context, account models.MailAccount, payload map[string]any, proxyURL string) (networkTeamAccessData, error) {
	accessToken := networkAccessTokenFromPayload(payload)
	newRT := ""
	if accessToken == "" && strings.TrimSpace(account.OpenaiRT) != "" {
		refreshed, err := networkRefreshAccessToken(ctx, strings.TrimSpace(account.OpenaiRT), proxyURL)
		if err != nil {
			return networkTeamAccessData{}, err
		}
		accessToken = networkExtractAccessToken(networkText(refreshed["access_token"]))
		newRT = networkText(refreshed["refresh_token"])
	}
	if accessToken == "" {
		return networkTeamAccessData{}, errors.New("当前 Team 账号没有可用 Access Token")
	}

	summary := openai.SummarizeChatGPTAccessToken(accessToken)
	tokenAccountID := networkText(summary["account_id"])
	savedSummary, _ := payload["access_summary"].(map[string]any)
	workspaceID := firstNetworkText(
		payload["team_workspace_id"],
		savedSummary["team_workspace_id"],
		savedSummary["backend_workspace_id"],
		payload["account_id"],
		payload["chatgpt_account_id"],
		tokenAccountID,
	)
	plan := openai.ClassifyChatGPTPlanText(networkText(summary["plan_type"]))
	savedPlan := openai.ClassifyChatGPTPlanText(networkText(savedSummary["plan_type"]))

	// 普通个人 Token 也能查询其可访问的 Team workspace。旧实现直接拒绝，
	// 使拥有有效 Session Cookie 的账号永远无法完成邀请；现在先查询真实
	// workspace，再用 Cookie 换取该工作空间的 Token。
	if workspaceID == "" || plan != "team" {
		workspace, lookupErr := networkFindTeamWorkspace(ctx, accessToken, proxyURL)
		if lookupErr == nil && workspace.AccountID != "" {
			workspaceID = workspace.AccountID
			summary["plan_type"] = "team"
			summary["team_workspace_id"] = workspace.AccountID
			summary["backend_workspace_id"] = workspace.AccountID
			summary["backend_workspace_role"] = workspace.Role
			plan = "team"
		} else if workspaceID == "" {
			if lookupErr != nil {
				return networkTeamAccessData{}, lookupErr
			}
			return networkTeamAccessData{}, errors.New("当前 Team Session 缺少 Team account_id")
		}
	}
	if workspaceID == "" {
		return networkTeamAccessData{}, errors.New("当前 Team Session 缺少 Team account_id")
	}
	if tokenAccountID == "" || !strings.EqualFold(tokenAccountID, workspaceID) {
		sessionToken := openai.NextAuthSessionTokenFromStorageState(networkText(payload["storage_state_json"]))
		if sessionToken == "" {
			return networkTeamAccessData{}, errors.New("当前 Session 不是目标 Team workspace Token，且 storage_state_json 中没有可用 Session Cookie")
		}
		exchanged, err := networkExchangeTeamWorkspace(ctx, sessionToken, workspaceID, proxyURL)
		if err != nil {
			return networkTeamAccessData{}, err
		}
		accessToken = exchanged.AccessToken
		summary = exchanged.Summary
		tokenAccountID = networkText(summary["account_id"])
		plan = openai.ClassifyChatGPTPlanText(networkText(summary["plan_type"]))
	}
	if plan != "team" && savedPlan != "team" {
		return networkTeamAccessData{}, errors.New("当前 Session 未确认 Team 套餐；请先刷新 Team Session 后重试")
	}
	summary["team_workspace_id"] = workspaceID
	return networkTeamAccessData{
		AccessToken:     accessToken,
		AccountID:       workspaceID,
		AccessSummary:   summary,
		NewRefreshToken: newRT,
	}, nil
}

func firstNetworkText(values ...any) string {
	for _, value := range values {
		if text := networkText(value); text != "" {
			return text
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// K12 请求邀请
// ---------------------------------------------------------------------------

type K12RequestInviteRequest struct {
	Email       string `json:"email"`
	WorkspaceID string `json:"workspaceId"`
}

type K12RequestInviteResult struct {
	WorkspaceID string `json:"workspaceId"`
	Status      string `json:"status"`
	Response    string `json:"response"`
}

// StartK12RequestInvite 只执行 K12 的纯 HTTP 请求邀请步骤。
func (a *App) StartK12RequestInvite(req K12RequestInviteRequest) (JobView, error) {
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(data.Settings.K12WorkspaceID)
	}
	if workspaceID == "" {
		return JobView{}, errors.New("请先填写 K12 Workspace ID")
	}
	accessToken := networkAccessTokenFromPayload(data.Session)
	if accessToken == "" {
		return JobView{}, errors.New("选中账号没有可用 Access Token，请先执行“注册或登录并获取 Session”")
	}

	return a.startNetworkJob(JobK12RequestInvite, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkSessionDynamicProxy(data.Session, data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		status, body, callErr := networkRequestK12Invite(ctx, accessToken, workspaceID, proxyURL)
		if callErr != nil && networkCancelled(ctx, callErr) {
			return nil, callErr
		}
		statusText := strconv.Itoa(status)
		if callErr != nil {
			statusText = "ERROR"
			body = callErr.Error()
		}
		result := K12RequestInviteResult{
			WorkspaceID: workspaceID,
			Status:      statusText,
			Response:    strings.TrimSpace(body),
		}
		ok := callErr == nil && status >= 200 && status < 300
		accountStatus := "K12失败"
		if ok {
			accountStatus = "K12请求成功"
		}
		persistErr := a.networkPatchState(data.Account.Email, map[string]any{"status": accountStatus}, map[string]any{
			"k12_workspace_id": workspaceID,
			"k12_status":       statusText,
			"k12_response":     result.Response,
		})
		if ok {
			log(fmt.Sprintf("K12 请求完成: HTTP %d %s", status, networkTruncate(body, 500)))
			return result, persistErr
		}
		if callErr == nil {
			callErr = networkHTTPError("K12 请求", status, body)
		}
		return result, networkJoin(callErr, persistErr)
	})
}

// ---------------------------------------------------------------------------
// Plus 试用资格
// ---------------------------------------------------------------------------

type TrialEligibilityRequest struct {
	Email           string `json:"email"`
	Country         string `json:"country"`
	ConfirmCheckout bool   `json:"confirmCheckout"`
}

type TrialEligibilityResult struct {
	Eligible          bool   `json:"eligible"`
	Status            string `json:"status"`
	Amount            string `json:"amount"`
	AmountSource      string `json:"amountSource"`
	Currency          string `json:"currency"`
	Country           string `json:"country"`
	CheckoutSessionID string `json:"checkoutSessionId"`
	ProcessorEntity   string `json:"processorEntity"`
	CheckedAt         string `json:"checkedAt"`
	Detail            string `json:"detail"`
}

// StartTrialEligibility 会创建真实 checkout，但不会提交支付方式或扣款。
func (a *App) StartTrialEligibility(req TrialEligibilityRequest) (JobView, error) {
	if !req.ConfirmCheckout {
		return JobView{}, errors.New("检测试用会创建真实 checkout，必须由前端确认后再调用")
	}
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	accessToken := networkAccessTokenFromPayload(data.Session)
	if accessToken == "" {
		return JobView{}, errors.New("选中账号没有可用 Access Token，请先获取 Session")
	}
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	if country == "" {
		country = "US"
	}

	return a.startNetworkJob(JobTrialEligibility, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkSessionDynamicProxy(data.Session, data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		detected, callErr := networkDetectTrialEligibility(ctx, accessToken, proxyURL, country)
		if callErr != nil && networkCancelled(ctx, callErr) {
			return nil, callErr
		}
		stamp := networkNowLocal()
		result := TrialEligibilityResult{CheckedAt: stamp}
		if callErr == nil {
			result.Eligible = detected.Eligible
			result.Status = detected.Status
			result.Amount = detected.Amount
			result.AmountSource = detected.AmountSource
			result.Currency = detected.Currency
			result.Country = detected.Country
			result.CheckoutSessionID = detected.CheckoutSessionID
			result.ProcessorEntity = detected.ProcessorEntity
		} else {
			result.Status = "error"
			result.Detail = networkTruncate(callErr.Error(), 1000)
		}
		accountStatus := "试用检测失败"
		if result.Status == "eligible" {
			accountStatus = "有Plus试用"
		} else if result.Status == "not_eligible" {
			accountStatus = "无Plus试用"
		}
		persistErr := a.networkPatchState(data.Account.Email, map[string]any{"status": accountStatus}, map[string]any{
			"plus_trial_eligible":      strconv.FormatBool(result.Eligible),
			"plus_trial_status":        result.Status,
			"plus_trial_amount":        result.Amount,
			"plus_trial_currency":      result.Currency,
			"plus_trial_country":       result.Country,
			"plus_trial_amount_source": result.AmountSource,
			"plus_trial_checked_at":    stamp,
			"plus_trial_detail":        result.Detail,
		})
		if callErr != nil {
			return result, networkJoin(callErr, persistErr)
		}
		if result.Eligible {
			log(fmt.Sprintf("Plus 试用资格检测：有资格，checkout 应付金额=%s %s", result.Amount, result.Currency))
		} else {
			log(fmt.Sprintf("Plus 试用资格检测：暂无资格，checkout 应付金额=%s %s", result.Amount, result.Currency))
		}
		return result, persistErr
	})
}

// ---------------------------------------------------------------------------
// 查封禁邮件
// ---------------------------------------------------------------------------

type DeactivationScanRequest struct {
	Email                string `json:"email"`
	Days                 int    `json:"days"`
	MaxMessagesPerFolder int    `json:"maxMessagesPerFolder"`
}

type DeactivationScanResult struct {
	mail.DeactivationResult
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// StartDeactivationScan 只读扫描邮箱中的 OpenAI 停用通知。
func (a *App) StartDeactivationScan(req DeactivationScanRequest) (JobView, error) {
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	account := data.Account
	usesCloud := alias.AccountUsesCloudMail(
		&account,
		data.Settings.CloudMailBase,
		data.Settings.CloudMailToken,
		data.Settings.CloudMailEnabled,
	)
	if !usesCloud && (strings.TrimSpace(account.ClientID) == "" || strings.TrimSpace(account.RefreshToken) == "") {
		return JobView{}, errors.New("选中的邮箱没有可用的 Cloud Mail API 或 Outlook OAuth 收件配置")
	}
	days := req.Days
	if days < 1 {
		days = 90
	}
	maxMessages := req.MaxMessagesPerFolder
	if maxMessages < 10 {
		maxMessages = 120
	}
	if maxMessages > 500 {
		maxMessages = 500
	}

	return a.startNetworkJob(JobDeactivationScan, account.Email, func(ctx context.Context, log func(string)) (any, error) {
		proxySession, proxyURL, err := a.networkProxy(data.Settings, networkLoginDynamicProxy(data.Settings), log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		scan, callErr := networkScanDeactivation(ctx, &account, proxyURL, days, maxMessages, log)
		if callErr != nil && networkCancelled(ctx, callErr) {
			return nil, callErr
		}
		result := DeactivationScanResult{DeactivationResult: scan}
		if callErr != nil {
			result.Status = "error"
			result.Detail = networkTruncate(callErr.Error(), 1000)
			result.CheckedAt = networkNowLocal()
		} else if scan.Found {
			result.Status = "found"
		} else {
			result.Status = "not_found"
		}
		accountStatus := "未见封禁邮件"
		if result.Status == "found" {
			accountStatus = "疑似已封禁"
		} else if result.Status == "error" {
			accountStatus = "封禁检查失败"
		}
		latest := scan.Latest
		subject, dateText, folder, from, to, snippet := "", "", "", "", "", ""
		if latest != nil {
			subject = latest.Subject
			dateText = firstNetworkText(latest.Date, latest.MailTimeISO)
			folder = latest.Folder
			from = latest.From
			to = latest.To
			snippet = latest.Snippet
		}
		if snippet == "" {
			snippet = result.Detail
		}
		accountPatch := map[string]any{
			"status":           accountStatus,
			"refresh_token":    account.RefreshToken,
			"raw":              account.Raw,
			"mail_provider":    account.MailProvider,
			"cloud_mail_base":  account.CloudMailBase,
			"cloud_mail_token": account.CloudMailToken,
		}
		persistErr := a.networkPatchState(account.Email, accountPatch, map[string]any{
			"openai_deactivation_found":                strconv.FormatBool(scan.Found),
			"openai_deactivation_status":               result.Status,
			"openai_deactivation_count":                strconv.Itoa(scan.Count),
			"openai_deactivation_checked_at":           result.CheckedAt,
			"openai_deactivation_subject":              subject,
			"openai_deactivation_date":                 dateText,
			"openai_deactivation_folder":               folder,
			"openai_deactivation_from":                 from,
			"openai_deactivation_to":                   to,
			"openai_deactivation_snippet":              snippet,
			"openai_deactivation_alias_mismatch_count": strconv.Itoa(scan.AliasMismatchCount),
		})
		if callErr != nil {
			return result, networkJoin(callErr, persistErr)
		}
		if scan.Found {
			log(fmt.Sprintf("发现 OpenAI 封禁/停用邮件，共 %d 封", scan.Count))
		} else {
			log("未发现当前账号的 OpenAI 封禁/停用邮件")
		}
		return result, persistErr
	})
}

// ---------------------------------------------------------------------------
// Turnstile Solver 健康探测
// ---------------------------------------------------------------------------

type TurnstileProbeRequest struct {
	URL string `json:"url"`
}

type TurnstileProbeResult struct {
	URL      string   `json:"url"`
	Status   int      `json:"status"`
	Attempts []string `json:"attempts"`
}

// StartTurnstileProbe 依次探测 /health、/v1/health 和根路径。
func (a *App) StartTurnstileProbe(req TurnstileProbeRequest) (JobView, error) {
	base := strings.TrimSpace(req.URL)
	if base == "" {
		st, err := a.LoadSettings()
		if err != nil {
			return JobView{}, err
		}
		base = strings.TrimSpace(st.TurnstileSolverURL)
	}
	if base == "" {
		base = settings.TurnstileSolverDefaultURL
	}
	base = strings.TrimRight(base, "/")
	if err := validateHTTPBase(base); err != nil {
		return JobView{}, fmt.Errorf("Turnstile Solver URL 无效: %w", err)
	}
	return a.startNetworkJob(JobTurnstileProbe, "", func(ctx context.Context, log func(string)) (any, error) {
		result, err := networkProbeTurnstile(ctx, base)
		if err == nil {
			log(fmt.Sprintf("Turnstile Solver 可达: %s -> HTTP %d", result.URL, result.Status))
		}
		return result, err
	})
}

func probeTurnstile(ctx context.Context, base string) (TurnstileProbeResult, error) {
	client := &http.Client{}
	result := TurnstileProbeResult{Attempts: []string{}}
	lastError := ""
	for _, endpoint := range []string{base + "/health", base + "/v1/health", base + "/"} {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Attempts = append(result.Attempts, endpoint)
		reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			lastError = err.Error()
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			cancel()
			lastError = err.Error()
			continue
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		cancel()
		if response.StatusCode < 500 {
			result.URL = endpoint
			result.Status = response.StatusCode
			return result, nil
		}
		lastError = fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	if lastError == "" {
		lastError = "无法连接"
	}
	return result, fmt.Errorf("Turnstile Solver 连接失败: %s", lastError)
}

func validateHTTPBase(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("必须是 http:// 或 https:// 地址")
	}
	return nil
}

// ---------------------------------------------------------------------------
// SMSBower 只读余额/价格
// ---------------------------------------------------------------------------

type SMSBowerReadRequest struct {
	APIKey        string `json:"apiKey"`
	Service       string `json:"service"`
	Country       string `json:"country"`
	IncludePrices bool   `json:"includePrices"`
}

type SMSBowerPriceQuote struct {
	Cost  float64 `json:"cost"`
	Count int     `json:"count"`
}

type SMSBowerPriceTier struct {
	Cost  float64 `json:"cost"`
	Count int     `json:"count"`
}

type SMSBowerReadResult struct {
	Balance string              `json:"balance"`
	Quote   *SMSBowerPriceQuote `json:"quote,omitempty"`
	Tiers   []SMSBowerPriceTier `json:"tiers"`
}

// smsbowerReadClient 故意只暴露三个只读方法；租号 GetNumber 不在该接口中，
// 因而本绑定的默认实现和测试替身都无法误触付费租号。
type smsbowerReadClient interface {
	GetBalance(context.Context) (string, error)
	GetPriceQuote(context.Context, string, string) (smsbower.PriceQuote, error)
	GetPriceTiers(context.Context, string, string) ([]smsbower.PriceTier, error)
}

var networkNewSMSBowerReadClient = func(apiKey string) (smsbowerReadClient, error) {
	return smsbower.NewClient(apiKey, "", "")
}

// StartSMSBowerReadTest 只调用 getBalance/getPrices/getPricesV2，绝不调用租号接口。
func (a *App) StartSMSBowerReadTest(req SMSBowerReadRequest) (JobView, error) {
	st, err := a.LoadSettings()
	if err != nil {
		return JobView{}, err
	}
	if strings.TrimSpace(req.APIKey) == "" {
		req.APIKey = st.SMSBowerAPIKey
	}
	if strings.TrimSpace(req.Service) == "" {
		req.Service = st.SMSBowerService
	}
	if strings.TrimSpace(req.Country) == "" {
		req.Country = st.SMSBowerCountry
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Service = strings.TrimSpace(req.Service)
	req.Country = strings.TrimSpace(req.Country)
	if req.APIKey == "" {
		return JobView{}, errors.New("请先填写 SMSBower API Key")
	}
	validation := settings.Defaults()
	validation.SMSBowerEnabled = true
	validation.SMSBowerAPIKey = req.APIKey
	validation.SMSBowerService = req.Service
	validation.SMSBowerCountry = req.Country
	validation.SMSBowerMaxPrice = ""
	if err := validation.ValidateSMSBower(); err != nil {
		return JobView{}, err
	}
	return a.startNetworkJob(JobSMSBowerRead, "", func(ctx context.Context, log func(string)) (any, error) {
		result, err := networkReadSMSBower(ctx, req)
		if err == nil {
			log(fmt.Sprintf("SMSBower API 检测通过，余额: %s", result.Balance))
		}
		return result, err
	})
}

func readSMSBower(ctx context.Context, req SMSBowerReadRequest) (SMSBowerReadResult, error) {
	client, err := networkNewSMSBowerReadClient(req.APIKey)
	if err != nil {
		return SMSBowerReadResult{}, err
	}
	balance, err := client.GetBalance(ctx)
	if err != nil {
		return SMSBowerReadResult{}, err
	}
	result := SMSBowerReadResult{Balance: balance, Tiers: []SMSBowerPriceTier{}}
	if !req.IncludePrices {
		return result, nil
	}
	quote, err := client.GetPriceQuote(ctx, req.Service, req.Country)
	if err != nil {
		return SMSBowerReadResult{}, err
	}
	result.Quote = &SMSBowerPriceQuote{Cost: quote.Cost, Count: quote.Count}
	tiers, err := client.GetPriceTiers(ctx, req.Service, req.Country)
	if err != nil {
		return SMSBowerReadResult{}, err
	}
	for _, tier := range tiers {
		result.Tiers = append(result.Tiers, SMSBowerPriceTier{Cost: tier.Cost, Count: tier.Count})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Cloud Mail 连通与 Token
// ---------------------------------------------------------------------------

type CloudMailProbeRequest struct {
	BaseURL    string `json:"baseUrl"`
	Token      string `json:"token"`
	ProbeEmail string `json:"probeEmail"`
}

type CloudMailProbeResult struct {
	BaseURL    string `json:"baseUrl"`
	ProbeEmail string `json:"probeEmail"`
}

type CloudMailTokenRequest struct {
	BaseURL           string `json:"baseUrl"`
	AdminEmail        string `json:"adminEmail"`
	AdminPassword     string `json:"adminPassword"`
	ConfirmInvalidate bool   `json:"confirmInvalidate"`
}

type CloudMailTokenResult struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

// StartCloudMailProbe 用随机探针地址读取至多一条邮件列表，不读取正文。
func (a *App) StartCloudMailProbe(req CloudMailProbeRequest) (JobView, error) {
	st, err := a.LoadSettings()
	if err != nil {
		return JobView{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(st.CloudMailBase), "/")
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = strings.TrimSpace(st.CloudMailToken)
	}
	if _, err := mail.NewCloudMailClient(base, token); err != nil {
		return JobView{}, err
	}
	probeEmail := models.NormalizeEmailAddress(req.ProbeEmail)
	if probeEmail == "" {
		probeEmail = fmt.Sprintf("codex-healthcheck-%d@%s", time.Now().Unix(), models.DefaultDomainMailDomain)
	}
	return a.startNetworkJob(JobCloudMailProbe, "", func(ctx context.Context, log func(string)) (any, error) {
		err := networkProbeCloudMail(ctx, base, token, probeEmail)
		if err != nil {
			return CloudMailProbeResult{BaseURL: base, ProbeEmail: probeEmail}, err
		}
		log("Cloud Mail API 检测通过")
		return CloudMailProbeResult{BaseURL: base, ProbeEmail: probeEmail}, nil
	})
}

// StartCloudMailTokenGeneration 生成并保存会替换旧值的程序 Token。
func (a *App) StartCloudMailTokenGeneration(req CloudMailTokenRequest) (JobView, error) {
	if !req.ConfirmInvalidate {
		return JobView{}, errors.New("生成新 Cloud Mail Token 会使旧 Token 失效，必须先确认")
	}
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if err := validateHTTPBase(base); err != nil {
		return JobView{}, fmt.Errorf("Cloud Mail Base URL 格式错误: %w", err)
	}
	adminEmail := models.NormalizeEmailAddress(req.AdminEmail)
	if !strings.Contains(adminEmail, "@") {
		return JobView{}, errors.New("请输入有效的 Cloud Mail 管理员邮箱")
	}
	if req.AdminPassword == "" {
		return JobView{}, errors.New("请输入 Cloud Mail 管理员密码")
	}
	return a.startNetworkJob(JobCloudMailToken, "", func(ctx context.Context, log func(string)) (any, error) {
		token, callErr := networkGenerateCloudMailToken(ctx, base, adminEmail, req.AdminPassword)
		if callErr != nil {
			return nil, callErr
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, errors.New("Cloud Mail 生成 Token 成功但返回空 Token")
		}
		result := CloudMailTokenResult{BaseURL: base, Token: token}
		persistErr := a.persistCloudMailToken(base, token)
		if persistErr == nil {
			log("Cloud Mail 程序 Token 已生成并保存")
		}
		return result, persistErr
	})
}

func (a *App) persistCloudMailToken(base, token string) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		st.CloudMailEnabled = true
		st.CloudMailBase = base
		st.CloudMailToken = token
		snapshot = settings.ToSnapshot(st, snapshot)

		accounts := accountsFromSnapshot(snapshot)
		pointers := make([]*models.MailAccount, 0, len(accounts))
		for index := range accounts {
			pointers = append(pointers, &accounts[index])
		}
		alias.ApplyCloudMailRuntimeConfig(pointers, base, token, true)
		snapshot["accounts"] = accountsToSnapshot(accounts)
		return snapshot, map[string]bool{}, nil
	})
}

// ---------------------------------------------------------------------------
// Session 文本中的 Access Token
// ---------------------------------------------------------------------------

var networkSessionTokenRE = regexp.MustCompile(`"(?:accessToken|access_token|token)"\s*:\s*"([^"]+)"`)

type networkJSONObject interface {
	Get(string) any
	Keys() []string
}

func networkAccessTokenFromPayload(payload map[string]any) string {
	for _, key := range []string{"access_token", "accessToken"} {
		if token := networkExtractAccessToken(networkText(payload[key])); token != "" {
			return token
		}
	}
	return networkExtractAccessToken(networkText(payload["session_json"]))
}

// networkExtractAccessToken 对齐 opll 中尚未导出的 Session 文本解析器。
func networkExtractAccessToken(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer"))
	}
	if decoded, err := openai.DecodeOrderedJSON([]byte(raw)); err == nil {
		return networkFindAccessToken(decoded)
	}
	if match := networkSessionTokenRE.FindStringSubmatch(raw); match != nil {
		return strings.TrimSpace(match[1])
	}
	if strings.Count(raw, ".") >= 2 && len(raw) > 80 {
		return raw
	}
	return ""
}

func networkFindAccessToken(value any) string {
	switch typed := value.(type) {
	case networkJSONObject:
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if token := networkText(typed.Get(key)); token != "" {
				return token
			}
		}
		for _, key := range typed.Keys() {
			if token := networkFindAccessToken(typed.Get(key)); token != "" {
				return token
			}
		}
	case map[string]any:
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if token := networkText(typed[key]); token != "" {
				return token
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if token := networkFindAccessToken(typed[key]); token != "" {
				return token
			}
		}
	case []any:
		for _, child := range typed {
			if token := networkFindAccessToken(child); token != "" {
				return token
			}
		}
	}
	return ""
}
