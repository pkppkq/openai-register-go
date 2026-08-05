package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
)

const JobWorkspaceInviteAccept JobKind = "workspace_invite_accept"

func init() {
	networkJobKinds[JobWorkspaceInviteAccept] = true
}

// WorkspaceInviteRequest 接受 Team/K12 邀请，并可在接受后立即刷新 Session。
type WorkspaceInviteRequest struct {
	Email          string `json:"email"`
	Kind           string `json:"kind"`
	InviteURL      string `json:"inviteUrl"`
	WorkspaceID    string `json:"workspaceId"`
	RefreshSession bool   `json:"refreshSession"`
	Confirmed      bool   `json:"confirmed"`
}

// WorkspaceInviteResult 记录浏览器接受邀请的可审计结果。
type WorkspaceInviteResult struct {
	Kind             string                `json:"kind"`
	InviteURL        string                `json:"inviteUrl"`
	WorkspaceID      string                `json:"workspaceId"`
	FinalURL         string                `json:"finalUrl"`
	ClickedText      string                `json:"clickedText"`
	StorageStateJSON string                `json:"storageStateJson"`
	Fingerprint      map[string]any        `json:"fingerprint"`
	AcceptedAt       string                `json:"acceptedAt"`
	Session          *SessionRefreshResult `json:"session,omitempty"`
}

type workspaceInviteAcceptFunc func(
	context.Context,
	models.MailAccount,
	string,
	string,
	string,
	func(string),
) (WorkspaceInviteResult, error)

var workspaceInviteAcceptOne workspaceInviteAcceptFunc = acceptWorkspaceInviteWithBrowser

// StartAcceptWorkspaceInvite 启动一次显式确认过的邀请接受流程。
func (a *App) StartAcceptWorkspaceInvite(req WorkspaceInviteRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("接受 Team/K12 邀请会改变外部工作空间成员状态，必须先确认")
	}
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "team" && kind != "k12" {
		return JobView{}, errors.New("邀请类型必须是 team 或 k12")
	}
	inviteURL := strings.TrimSpace(req.InviteURL)
	if inviteURL == "" {
		inviteURL = firstNetworkText(data.Session[kind+"_invite_url"])
	}
	if !validChatGPTInviteURL(inviteURL) {
		return JobView{}, errors.New("缺少有效的 ChatGPT Team/K12 邀请链接")
	}
	storageState := networkText(data.Session["storage_state_json"])
	if storageState == "" {
		return JobView{}, errors.New("当前账号没有 storage_state_json，无法在浏览器中接受邀请")
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		workspaceID = workspaceIDFromInviteURL(inviteURL)
	}
	if workspaceID == "" {
		workspaceID = firstNetworkText(
			data.Session[kind+"_workspace_id"],
			data.Session["target_workspace_id"],
		)
	}
	if kind == "k12" && workspaceID == "" {
		workspaceID = strings.TrimSpace(data.Settings.K12WorkspaceID)
	}

	return a.startNetworkJob(JobWorkspaceInviteAccept, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		dynamic := networkSessionDynamicProxy(data.Session, data.Settings)
		proxySession, proxyURL, err := a.networkProxy(data.Settings, dynamic, log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		result, err := workspaceInviteAcceptOne(ctx, data.Account, storageState, inviteURL, proxyURL, log)
		if err != nil {
			return nil, err
		}
		result.Kind = kind
		result.InviteURL = inviteURL
		result.WorkspaceID = workspaceID
		sessionPatch := map[string]any{
			"storage_state_json":    result.StorageStateJSON,
			kind + "_invite_url":    inviteURL,
			kind + "_accept_result": "accepted",
			kind + "_accepted_at":   result.AcceptedAt,
			kind + "_workspace_id":  workspaceID,
			"target_workspace_id":   workspaceID,
		}
		accountPatch := map[string]any{"status": inviteAcceptedStatus(kind, false)}
		if data.Account.BrowserFingerprint == nil && len(result.Fingerprint) > 0 {
			accountPatch["browser_fingerprint"] = result.Fingerprint
		}

		if req.RefreshSession {
			refreshed, refreshErr := sessionRefreshOne(
				ctx, data.Account, result.StorageStateJSON, proxyURL, workspaceID, log,
			)
			if refreshErr != nil {
				sessionPatch[kind+"_refresh_error"] = networkTruncate(refreshErr.Error(), 800)
				if persistErr := a.networkPatchState(data.Account.Email, accountPatch, sessionPatch); persistErr != nil {
					return result, errors.Join(refreshErr, persistErr)
				}
				return result, refreshErr
			}
			result.Session = &refreshed
			sessionPatch["access_token"] = refreshed.AccessToken
			sessionPatch["session_json"] = refreshed.SessionJSON
			sessionPatch["storage_state_json"] = refreshed.StorageStateJSON
			sessionPatch["access_summary"] = refreshed.AccessSummary
			sessionPatch["session_refreshed_at"] = refreshed.RefreshedAt
			sessionPatch[kind+"_refresh_error"] = ""
			accountPatch["status"] = inviteAcceptedStatus(kind, true)
			if plan := strings.ToLower(networkText(refreshed.AccessSummary["plan_type"])); sessionPlanTypes[plan] {
				accountPatch["account_type"] = plan
			}
		}
		if err := a.networkPatchState(data.Account.Email, accountPatch, sessionPatch); err != nil {
			return result, err
		}
		log(fmt.Sprintf("%s 邀请已接受%s", strings.ToUpper(kind), map[bool]string{true: "并刷新 Session", false: ""}[req.RefreshSession]))
		return result, nil
	})
}

func inviteAcceptedStatus(kind string, refreshed bool) string {
	if kind == "k12" {
		if refreshed {
			return "K12请求成功/Session已刷新"
		}
		return "K12邀请已接受（待刷新）"
	}
	if refreshed {
		return "Team Session已刷新"
	}
	return "Team邀请已接受（待刷新）"
}

func validChatGPTInviteURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "chatgpt.com" && host != "chat.openai.com" {
		return false
	}
	text := strings.ToLower(parsed.EscapedPath() + "?" + parsed.RawQuery)
	return strings.Contains(text, "invite") || strings.Contains(text, "join") || strings.Contains(text, "teacher")
}

func workspaceIDFromInviteURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	values := parsed.Query()
	for _, key := range []string{"wId", "wid", "workspace_id", "workspaceId"} {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func acceptWorkspaceInviteWithBrowser(
	ctx context.Context,
	account models.MailAccount,
	storageStateJSON, inviteURL, proxyURL string,
	log func(string),
) (WorkspaceInviteResult, error) {
	stored, err := browser.ParseStorageStateJSON(storageStateJSON)
	if err != nil {
		return WorkspaceInviteResult{}, fmt.Errorf("storage_state_json 无法解析: %w", err)
	}
	fingerprint := account.BrowserFingerprint
	if fingerprint == nil {
		generated := models.GenerateRegisterFingerprint()
		fingerprint = &generated
	}
	b, err := browser.Launch(browser.LaunchOptions{
		Fingerprint: *fingerprint,
		Headless:    false,
		ProxyServer: proxyURL,
	})
	if err != nil {
		return WorkspaceInviteResult{}, err
	}
	defer b.Close()
	if err := b.ApplyStorageState(stored); err != nil {
		return WorkspaceInviteResult{}, err
	}
	page, err := b.NewPage()
	if err != nil {
		return WorkspaceInviteResult{}, err
	}
	defer page.Close()
	if err := page.Navigate(inviteURL, 45*time.Second); err != nil {
		return WorkspaceInviteResult{}, err
	}

	var clickedText string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return WorkspaceInviteResult{}, err
		}
		body := workspacePageText(page)
		lower := strings.ToLower(body)
		if strings.Contains(lower, "invite expired") ||
			strings.Contains(lower, "invitation has expired") ||
			strings.Contains(body, "邀请已过期") {
			return WorkspaceInviteResult{}, errors.New("邀请链接已过期")
		}
		if clickedText == "" {
			if clicked, text := page.ClickButtonByText([]string{
				"Accept invite", "Accept invitation", "Join workspace", "Join team",
				"接受邀请", "加入工作空间", "加入团队", "加入",
				"Aceptar invitación", "Accepter l’invitation",
			}); clicked {
				clickedText = text
				log("已点击邀请接受按钮: " + text)
				time.Sleep(2 * time.Second)
				continue
			}
		}
		current := page.URL()
		if clickedText != "" && !strings.Contains(strings.ToLower(current), "invite") {
			break
		}
		time.Sleep(time.Second)
	}
	if clickedText == "" {
		return WorkspaceInviteResult{}, errors.New("邀请页面未找到可点击的接受按钮")
	}
	exported, err := b.ExportStorageState()
	if err != nil {
		return WorkspaceInviteResult{}, err
	}
	raw, err := json.Marshal(exported)
	if err != nil {
		return WorkspaceInviteResult{}, err
	}
	return WorkspaceInviteResult{
		FinalURL:         page.URL(),
		ClickedText:      clickedText,
		StorageStateJSON: string(raw),
		Fingerprint:      models.FingerprintToMap(fingerprint),
		AcceptedAt:       networkNowUTC(),
	}, nil
}

func workspacePageText(page *browser.Page) string {
	value, err := page.Rod.Eval(`() => document.body ? document.body.innerText : ''`)
	if err != nil || value == nil {
		return ""
	}
	return networkTruncate(value.Value.Str(), 4000)
}
