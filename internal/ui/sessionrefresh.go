package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

const (
	JobSessionRefresh    JobKind = "session_refresh"
	JobK12SessionRefresh JobKind = "k12_session_refresh"
)

func init() {
	networkJobKinds[JobSessionRefresh] = true
	networkJobKinds[JobK12SessionRefresh] = true
}

// SessionRefreshRequest 描述单账号 Session 刷新；K12 模式会先切换到目标
// workspace，再读取新的 Session。
type SessionRefreshRequest struct {
	Email       string `json:"email"`
	K12         bool   `json:"k12"`
	WorkspaceID string `json:"workspaceId"`
}

// SessionRefreshResult 是浏览器刷新后写回 state.json 的稳定结果。
type SessionRefreshResult struct {
	Email            string         `json:"email"`
	AccessToken      string         `json:"accessToken"`
	SessionJSON      string         `json:"sessionJson"`
	StorageStateJSON string         `json:"storageStateJson"`
	AccessSummary    map[string]any `json:"accessSummary"`
	Fingerprint      map[string]any `json:"fingerprint"`
	WorkspaceID      string         `json:"workspaceId"`
	RefreshedAt      string         `json:"refreshedAt"`
}

type sessionRefreshFunc func(context.Context, models.MailAccount, string, string, string, func(string)) (SessionRefreshResult, error)

var sessionRefreshOne sessionRefreshFunc = refreshSessionWithBrowser

// StartRefreshSession 使用已保存的 storage_state_json 刷新一个账号。
func (a *App) StartRefreshSession(req SessionRefreshRequest) (JobView, error) {
	data, err := a.networkAccountData(req.Email)
	if err != nil {
		return JobView{}, err
	}
	storageState := networkText(data.Session["storage_state_json"])
	if storageState == "" {
		return JobView{}, errors.New("选中账号没有可用 storage_state_json，请先获取 Session")
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	kind := JobSessionRefresh
	if req.K12 {
		kind = JobK12SessionRefresh
		if workspaceID == "" {
			workspaceID = firstNetworkText(
				data.Session["target_workspace_id"],
				data.Session["k12_workspace_id"],
				data.Settings.K12WorkspaceID,
			)
		}
		if workspaceID == "" {
			return JobView{}, errors.New("刷新 K12 Session 前必须指定 workspace ID")
		}
	}

	return a.startNetworkJob(kind, data.Account.Email, func(ctx context.Context, log func(string)) (any, error) {
		dynamic := networkSessionDynamicProxy(data.Session, data.Settings)
		proxySession, proxyURL, err := a.networkProxy(data.Settings, dynamic, log)
		if err != nil {
			return nil, err
		}
		defer proxySession.Close()

		result, err := sessionRefreshOne(ctx, data.Account, storageState, proxyURL, workspaceID, log)
		if err != nil {
			if networkCancelled(ctx, err) {
				return nil, err
			}
			_ = a.networkPatchState(data.Account.Email,
				map[string]any{"status": "刷新Session失败"},
				map[string]any{"session_refresh_error": networkTruncate(err.Error(), 800)},
			)
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		accountPatch := map[string]any{"status": sessionRefreshStatus(result.AccessSummary, req.K12)}
		if data.Account.BrowserFingerprint == nil && len(result.Fingerprint) > 0 {
			accountPatch["browser_fingerprint"] = result.Fingerprint
		}
		if plan := strings.ToLower(networkText(result.AccessSummary["plan_type"])); sessionPlanTypes[plan] {
			accountPatch["account_type"] = plan
		}
		sessionPatch := map[string]any{
			"access_token":          result.AccessToken,
			"session_json":          result.SessionJSON,
			"storage_state_json":    result.StorageStateJSON,
			"access_summary":        result.AccessSummary,
			"target_workspace_id":   result.WorkspaceID,
			"session_refreshed_at":  result.RefreshedAt,
			"session_refresh_error": "",
		}
		if err := a.networkPatchState(data.Account.Email, accountPatch, sessionPatch); err != nil {
			return result, err
		}
		log(fmt.Sprintf("Session 刷新成功：plan=%s account尾号=%s",
			networkText(result.AccessSummary["plan_type"]),
			networkText(result.AccessSummary["account_id_tail"]),
		))
		return result, nil
	})
}

var sessionPlanTypes = map[string]bool{
	"free": true, "plus": true, "team": true, "k12": true, "pro": true,
}

func sessionRefreshStatus(summary map[string]any, k12 bool) string {
	if k12 {
		return "K12 Session已刷新"
	}
	plan := strings.ToLower(networkText(summary["plan_type"]))
	switch plan {
	case "k12":
		return "K12 Session已刷新"
	default:
		return "Session已刷新"
	}
}

func refreshSessionWithBrowser(
	ctx context.Context,
	account models.MailAccount,
	storageStateJSON string,
	proxyURL string,
	workspaceID string,
	log func(string),
) (SessionRefreshResult, error) {
	stored, err := browser.ParseStorageStateJSON(storageStateJSON)
	if err != nil {
		return SessionRefreshResult{}, fmt.Errorf("storage_state_json 无法解析: %w", err)
	}
	if workspaceID != "" {
		stored.Cookies = append(stored.Cookies, &proto.NetworkCookieParam{
			Name:     "_account",
			Value:    workspaceID,
			Domain:   "chatgpt.com",
			Path:     "/",
			Secure:   true,
			HTTPOnly: false,
			SameSite: proto.NetworkCookieSameSiteLax,
		})
	}

	fingerprint := account.BrowserFingerprint
	if fingerprint == nil {
		generated := models.GenerateRegisterFingerprint()
		fingerprint = &generated
	}
	if err := ctx.Err(); err != nil {
		return SessionRefreshResult{}, err
	}
	b, err := browser.Launch(browser.LaunchOptions{
		Fingerprint: *fingerprint,
		Headless:    false,
		ProxyServer: proxyURL,
	})
	if err != nil {
		return SessionRefreshResult{}, err
	}
	defer b.Close()
	if err := b.ApplyStorageState(stored); err != nil {
		return SessionRefreshResult{}, err
	}
	page, err := b.NewPage()
	if err != nil {
		return SessionRefreshResult{}, err
	}
	defer page.Close()

	sessionURL := openai.ChatGPTBaseURL + "/api/auth/session"
	if workspaceID != "" {
		values := url.Values{
			"exchange_workspace_token": {"true"},
			"workspace_id":             {workspaceID},
			"reason":                   {"setCurrentAccount"},
		}
		sessionURL += "?" + values.Encode()
		log("刷新 Session 正在切换目标 workspace 尾号=" + tailText(workspaceID, 8))
	}
	if err := page.Navigate(sessionURL, 25*time.Second); err != nil {
		return SessionRefreshResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return SessionRefreshResult{}, err
	}
	value, err := page.Rod.Eval(`() => document.body ? document.body.innerText : document.documentElement.innerText`)
	if err != nil || value == nil {
		return SessionRefreshResult{}, fmt.Errorf("读取 Session 响应失败: %w", err)
	}
	body := strings.TrimSpace(value.Value.Str())
	var session map[string]any
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		return SessionRefreshResult{}, fmt.Errorf("Session 接口返回不是有效 JSON: %s", networkTruncate(body, 300))
	}
	accessToken := networkText(session["accessToken"])
	if accessToken == "" {
		accessToken = networkText(session["access_token"])
	}
	if accessToken == "" {
		return SessionRefreshResult{}, errors.New("Session JSON 已获取，但未发现 accessToken，登录态可能已失效")
	}
	summary := openai.SummarizeChatGPTSessionPayload(session, accessToken)
	if backend, err := probeSessionBackend(page, summary, accessToken); err != nil {
		summary["backend_plan_error"] = networkTruncate(err.Error(), 500)
		log("Session 后台套餐校正失败: " + err.Error())
	} else {
		openai.MergeChatGPTBackendPlanSummary(summary, backend)
	}
	exported, err := b.ExportStorageState()
	if err != nil {
		return SessionRefreshResult{}, err
	}
	sessionRaw, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return SessionRefreshResult{}, err
	}
	storageRaw, err := json.Marshal(exported)
	if err != nil {
		return SessionRefreshResult{}, err
	}
	return SessionRefreshResult{
		Email:            account.Email,
		AccessToken:      accessToken,
		SessionJSON:      string(sessionRaw),
		StorageStateJSON: string(storageRaw),
		AccessSummary:    summary,
		Fingerprint:      models.FingerprintToMap(fingerprint),
		WorkspaceID:      workspaceID,
		RefreshedAt:      networkNowLocal(),
	}, nil
}

const sessionBackendProbeJS = `async ({ accountId, accessToken }) => {
    const endpoints = [
        '/backend-api/accounts/check/v4-2023-04-27',
        accountId ? '/backend-api/accounts/' + encodeURIComponent(accountId) + '/subscription' : '',
        '/backend-api/me'
    ].filter(Boolean);
    const fetchOne = async endpoint => {
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), 8000);
        try {
            const headers = { accept: 'application/json' };
            if (accessToken) headers.authorization = 'Bearer ' + accessToken;
            const response = await fetch(endpoint, {
                credentials: 'include',
                cache: 'no-store',
                headers,
                signal: controller.signal
            });
            const text = await response.text();
            let payload = null;
            try { payload = JSON.parse(text); } catch (_) {}
            return {
                endpoint,
                status: response.status,
                ok: response.ok,
                payload,
                bodyText: payload ? '' : text.slice(0, 300)
            };
        } catch (error) {
            return {
                endpoint,
                status: 0,
                ok: false,
                payload: null,
                bodyText: String(error && error.message || error).slice(0, 300)
            };
        } finally {
            clearTimeout(timer);
        }
    };
    return await Promise.all(endpoints.map(fetchOne));
}`

func probeSessionBackend(page *browser.Page, summary map[string]any, accessToken string) (any, error) {
	value, err := page.Rod.Eval(sessionBackendProbeJS, map[string]any{
		"accountId":   networkText(summary["account_id"]),
		"accessToken": accessToken,
	})
	if err != nil || value == nil {
		return nil, err
	}
	raw, err := json.Marshal(value.Value.Raw())
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func tailText(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return value
	}
	return string(runes[len(runes)-count:])
}
