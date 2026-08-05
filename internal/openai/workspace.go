package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	http "github.com/bogdanfinn/fhttp"

	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// TeamWorkspace 是当前 Access Token 可访问的 Team 工作空间。
type TeamWorkspace struct {
	AccountID string `json:"accountId"`
	PlanType  string `json:"planType"`
	Role      string `json:"role"`
	Detail    string `json:"detail"`
}

// WorkspaceSession 是 Session Cookie 切换到工作空间后返回的 Token 结果。
type WorkspaceSession struct {
	AccessToken string         `json:"accessToken"`
	SessionJSON string         `json:"sessionJson"`
	Summary     map[string]any `json:"accessSummary"`
	WorkspaceID string         `json:"workspaceId"`
}

// ChatGPTTeamWorkspaceForAccessToken 查询当前登录态可用的 Team workspace。
func ChatGPTTeamWorkspaceForAccessToken(accessToken, proxyURL string) (TeamWorkspace, error) {
	session, err := teamNewChatGPTSession(accessToken, proxyURL)
	if err != nil {
		return TeamWorkspace{}, err
	}
	status, body, err := session.do("GET", teamAccountsCheckURL(), nil, nil)
	if err != nil {
		return TeamWorkspace{}, err
	}
	if !teamHTTPOK(status) {
		return TeamWorkspace{}, fmt.Errorf("查询 Team workspace 失败: HTTP %d %s", status, teamTruncateRunes(body, 500))
	}
	return parseTeamWorkspaceResponse(body)
}

func parseTeamWorkspaceResponse(body string) (TeamWorkspace, error) {
	payload, err := DecodeOrderedJSON([]byte(body))
	if err != nil {
		return TeamWorkspace{}, errors.New("查询 Team workspace 返回的不是有效 JSON")
	}
	workspace := accountTeamWorkspaceFromBackendPayload(payload)
	if workspace.AccountID == "" {
		return TeamWorkspace{}, errors.New("当前登录态没有找到可用的 Team workspace；请先确认账号已进入 Team")
	}
	return TeamWorkspace{
		AccountID: workspace.AccountID,
		PlanType:  workspace.PlanType,
		Role:      workspace.Role,
		Detail:    workspace.Detail,
	}, nil
}

type storageCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// NextAuthSessionTokenFromStorageState 从 Playwright/Go-Rod storage state 中
// 读取 next-auth Cookie，并支持 NextAuth 的分片 Cookie。
func NextAuthSessionTokenFromStorageState(storageStateText string) string {
	var state struct {
		Cookies []storageCookie `json:"cookies"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(storageStateText)), &state); err != nil {
		return ""
	}
	for _, base := range []string{"__Secure-next-auth.session-token", "next-auth.session-token"} {
		for _, cookie := range state.Cookies {
			if cookie.Name == base && cookie.Value != "" {
				return cookie.Value
			}
		}
		pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d+)$`)
		type chunk struct {
			index int
			value string
		}
		var chunks []chunk
		for _, cookie := range state.Cookies {
			match := pattern.FindStringSubmatch(cookie.Name)
			if match == nil {
				continue
			}
			index, err := strconv.Atoi(match[1])
			if err == nil {
				chunks = append(chunks, chunk{index: index, value: cookie.Value})
			}
		}
		if len(chunks) > 0 {
			sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].index < chunks[j].index })
			var token strings.Builder
			for _, item := range chunks {
				token.WriteString(item.value)
			}
			return token.String()
		}
	}
	return ""
}

// ChatGPTExchangeWorkspaceSession 使用 Session Cookie 换取目标 workspace 的
// Access Token，并校验服务端实际返回的 account_id。
func ChatGPTExchangeWorkspaceSession(sessionToken, workspaceID, proxyURL string) (WorkspaceSession, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	workspaceID = strings.TrimSpace(workspaceID)
	if sessionToken == "" {
		return WorkspaceSession{}, errors.New("缺少 ChatGPT Session Cookie，无法切换 Team workspace")
	}
	if workspaceID == "" {
		return WorkspaceSession{}, errors.New("缺少 Team workspace ID，无法换取工作空间 Token")
	}
	client, err := tlsclient.New(proxyURL, teamRequestTimeoutSeconds)
	if err != nil {
		return WorkspaceSession{}, err
	}
	query := url.Values{
		"exchange_workspace_token": {"true"},
		"workspace_id":             {workspaceID},
		"reason":                   {"setCurrentAccount"},
	}
	headers := http.Header{
		"user-agent":      {DefaultUserAgent},
		"accept":          {"application/json"},
		"accept-language": {"en-US,en;q=0.9"},
		"cookie":          {"__Secure-next-auth.session-token=" + sessionToken},
		"referer":         {ChatGPTBaseURL + "/"},
		http.HeaderOrderKey: {
			"user-agent", "accept", "accept-language", "cookie", "referer",
		},
	}
	status, raw, err := client.Do("GET", ChatGPTBaseURL+"/api/auth/session?"+query.Encode(), nil, headers)
	if err != nil {
		return WorkspaceSession{}, err
	}
	if !teamHTTPOK(status) {
		return WorkspaceSession{}, fmt.Errorf("Team workspace Token 交换失败: HTTP %d %s",
			status, teamTruncateRunes(string(raw), 500))
	}
	return parseWorkspaceSessionResponse(raw, workspaceID)
}

func parseWorkspaceSessionResponse(raw []byte, workspaceID string) (WorkspaceSession, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return WorkspaceSession{}, errors.New("Team workspace Token 交换结果不是有效 JSON")
	}
	if payload == nil {
		return WorkspaceSession{}, errors.New("Team workspace Token 交换结果不是对象")
	}
	accessToken := teamResolveAccessToken(FirstNonEmpty(payload["accessToken"], payload["access_token"]))
	if accessToken == "" {
		return WorkspaceSession{}, errors.New("Team workspace Token 交换成功，但响应中没有 accessToken")
	}
	summary := SummarizeChatGPTSessionPayload(payload, accessToken)
	returnedAccountID := strings.TrimSpace(FirstNonEmpty(summary["account_id"]))
	if returnedAccountID != workspaceID {
		actualTail := accountTail(returnedAccountID, 8)
		if actualTail == "" {
			actualTail = "-"
		}
		return WorkspaceSession{}, fmt.Errorf(
			"Team workspace 切换校验失败: 期望尾号=%s，实际尾号=%s",
			accountTail(workspaceID, 8),
			actualTail,
		)
	}
	formatted, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return WorkspaceSession{}, err
	}
	return WorkspaceSession{
		AccessToken: accessToken,
		SessionJSON: string(formatted),
		Summary:     summary,
		WorkspaceID: workspaceID,
	}, nil
}
