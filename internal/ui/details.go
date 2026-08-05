package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	applogs "github.com/pkppkq/openai-register-go/internal/logs"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// WorkflowEntry 是账户详情页七步流程中的一个稳定、可序列化节点。
type WorkflowEntry struct {
	State     string `json:"state"`
	Detail    string `json:"detail"`
	UpdatedAt string `json:"updated_at"`
}

// AccountDetailAccount 是账户详情页所需的完整账户记录。
//
// 不直接暴露 models.MailAccount，是为了让 Wails 生成的字段名继续与
// state.json/Python 前端使用的 snake_case 一致。
type AccountDetailAccount struct {
	Email              string         `json:"email"`
	Password           string         `json:"password"`
	ClientID           string         `json:"client_id"`
	RefreshToken       string         `json:"refresh_token"`
	Raw                string         `json:"raw"`
	AccountType        string         `json:"account_type"`
	Status             string         `json:"status"`
	OpenaiRT           string         `json:"openai_rt"`
	AuthPhoneNumber    string         `json:"auth_phone_number"`
	AuthPhoneSMSURL    string         `json:"auth_phone_sms_url"`
	ReceiveMailbox     string         `json:"receive_mailbox"`
	MailProvider       string         `json:"mail_provider"`
	CloudMailBase      string         `json:"cloud_mail_base"`
	CloudMailToken     string         `json:"cloud_mail_token"`
	Group              string         `json:"group"`
	BrowserFingerprint map[string]any `json:"browser_fingerprint"`
}

// AccountDetails 是账户详情页的一次只读快照。
//
// Session、Fingerprint 和 Account 中的指纹均为深拷贝，调用方修改返回值
// 不会改写本次读取使用的状态快照，更不会触发 state.json 写入。
type AccountDetails struct {
	Account     AccountDetailAccount     `json:"account"`
	Session     map[string]any           `json:"session"`
	Workflow    map[string]WorkflowEntry `json:"workflow"`
	Fingerprint map[string]any           `json:"fingerprint"`

	Link string `json:"link"`

	LinkProxy      string `json:"linkProxy"`
	LinkProxyLabel string `json:"linkProxyLabel"`
	LinkProxyExit  string `json:"linkProxyExit"`

	LinkCreateProxy      string `json:"linkCreateProxy"`
	LinkCreateProxyLabel string `json:"linkCreateProxyLabel"`
	LinkCreateProxyExit  string `json:"linkCreateProxyExit"`

	LinkFollowupProxy      string `json:"linkFollowupProxy"`
	LinkFollowupProxyLabel string `json:"linkFollowupProxyLabel"`
	LinkFollowupProxyExit  string `json:"linkFollowupProxyExit"`

	LinkApproveProxy      string `json:"linkApproveProxy"`
	LinkApproveProxyLabel string `json:"linkApproveProxyLabel"`
	LinkApproveProxyExit  string `json:"linkApproveProxyExit"`

	Logs []applogs.Record `json:"logs"`
}

var workflowStepKeys = [...]string{"email", "proxy", "auth", "session", "trial", "link", "export"}

var workflowStates = map[string]bool{
	"未开始":  true,
	"进行中":  true,
	"成功":   true,
	"失败":   true,
	"需要人工": true,
	"跳过":   true,
}

// GetAccountDetails 返回一个账户的完整详情。邮箱匹配不区分大小写，并优先
// 使用账户列表中的规范写法定位对应 Session、支付链接与结构化日志。
func (a *App) GetAccountDetails(email string) (AccountDetails, error) {
	want := models.NormalizeEmailAddress(email)
	if strings.TrimSpace(want) == "" {
		return AccountDetails{}, fmt.Errorf("未指定账号邮箱")
	}

	snapshot, err := a.snapshot()
	if err != nil {
		return AccountDetails{}, err
	}
	account, ok := detailAccountByEmail(accountsFromSnapshot(snapshot), want)
	if !ok {
		return AccountDetails{}, fmt.Errorf("账号不存在: %s", email)
	}

	session := map[string]any{}
	if raw, found := detailValueByEmail(sessionResultsFromSnapshot(snapshot), account.Email); found {
		if payload, valid := raw.(map[string]any); valid {
			session = detailCloneMap(payload)
		}
	}

	link := ""
	if raw, found := detailValueByEmail(resultsFromSnapshot(snapshot), account.Email); found {
		link = detailText(raw)
	}
	workflow := deriveAccountWorkflow(account, session, strings.TrimSpace(link) != "")
	fingerprint := detailCloneMap(models.FingerprintToMap(account.BrowserFingerprint))

	out := AccountDetails{
		Account:     accountDetailAccount(account, fingerprint),
		Session:     session,
		Workflow:    workflow,
		Fingerprint: detailCloneMap(fingerprint),
		Link:        link,

		LinkProxy:      detailSessionRaw(session, "link_proxy"),
		LinkProxyLabel: detailSessionRaw(session, "link_proxy_label"),
		LinkProxyExit:  detailSessionRaw(session, "link_proxy_exit"),

		LinkCreateProxy:      detailSessionRaw(session, "link_create_proxy"),
		LinkCreateProxyLabel: detailSessionRaw(session, "link_create_proxy_label"),
		LinkCreateProxyExit:  detailSessionRaw(session, "link_create_proxy_exit"),

		LinkFollowupProxy:      detailSessionRaw(session, "link_followup_proxy"),
		LinkFollowupProxyLabel: detailSessionRaw(session, "link_followup_proxy_label"),
		LinkFollowupProxyExit:  detailSessionRaw(session, "link_followup_proxy_exit"),

		LinkApproveProxy:      detailSessionRaw(session, "link_approve_proxy"),
		LinkApproveProxyLabel: detailSessionRaw(session, "link_approve_proxy_label"),
		LinkApproveProxyExit:  detailSessionRaw(session, "link_approve_proxy_exit"),
		Logs:                  []applogs.Record{},
	}
	if a.logs != nil {
		out.Logs = a.logs.AccountRecords(account.Email)
		if out.Logs == nil {
			out.Logs = []applogs.Record{}
		}
	}
	return out, nil
}

func detailAccountByEmail(accounts []models.MailAccount, email string) (models.MailAccount, bool) {
	for _, account := range accounts {
		if strings.EqualFold(models.NormalizeEmailAddress(account.Email), email) {
			return account, true
		}
	}
	return models.MailAccount{}, false
}

// detailValueByEmail 先取完全一致的键；旧状态里若只有大小写不同的键，则按
// 排序后的首个匹配项读取，避免 Go map 的随机遍历造成结果漂移。
func detailValueByEmail(values map[string]any, email string) (any, bool) {
	if value, ok := values[email]; ok {
		return value, true
	}
	keys := make([]string, 0)
	for key := range values {
		if strings.EqualFold(models.NormalizeEmailAddress(key), email) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	return values[keys[0]], true
}

func accountDetailAccount(account models.MailAccount, fingerprint map[string]any) AccountDetailAccount {
	return AccountDetailAccount{
		Email:              account.Email,
		Password:           account.Password,
		ClientID:           account.ClientID,
		RefreshToken:       account.RefreshToken,
		Raw:                account.Raw,
		AccountType:        account.AccountType,
		Status:             account.Status,
		OpenaiRT:           account.OpenaiRT,
		AuthPhoneNumber:    account.AuthPhoneNumber,
		AuthPhoneSMSURL:    account.AuthPhoneSMSURL,
		ReceiveMailbox:     account.ReceiveMailbox,
		MailProvider:       account.MailProvider,
		CloudMailBase:      account.CloudMailBase,
		CloudMailToken:     account.CloudMailToken,
		Group:              account.Group,
		BrowserFingerprint: detailCloneMap(fingerprint),
	}
}

func deriveAccountWorkflow(account models.MailAccount, session map[string]any, hasLink bool) map[string]WorkflowEntry {
	out := make(map[string]WorkflowEntry, len(workflowStepKeys))
	explicit := make(map[string]bool, len(workflowStepKeys))
	for _, key := range workflowStepKeys {
		out[key] = WorkflowEntry{State: "未开始"}
	}

	if saved, ok := session["workflow"].(map[string]any); ok {
		for _, key := range workflowStepKeys {
			value, exists := saved[key]
			if !exists {
				continue
			}
			item, valid := value.(map[string]any)
			if !valid {
				continue
			}
			state := detailTextOr(item["state"], "未开始")
			if !workflowStates[state] {
				state = "未开始"
			}
			out[key] = WorkflowEntry{
				State:     state,
				Detail:    detailClipRunes(detailTextOr(item["detail"], ""), 500),
				UpdatedAt: detailTextOr(item["updated_at"], ""),
			}
			explicit[key] = true
		}
	}

	if account.Email != "" && !explicit["email"] {
		out["email"] = WorkflowEntry{State: "未开始", Detail: "已导入账号，尚未检查邮箱"}
	}

	if !explicit["session"] && strings.TrimSpace(detailSessionText(session, "access_token")) != "" {
		summary, _ := session["access_summary"].(map[string]any)
		plan := detailFirstText(
			detailMapValue(summary, "plan_type"),
			session["plan_type"],
			session["chatgpt_plan_type"],
		)
		if plan == "" {
			plan = "unknown"
		}
		expiresAt := detailText(detailMapValue(summary, "expires_at"))
		detail := "plan=" + plan
		if expiresAt != "" {
			detail += "，到期=" + expiresAt
		}
		out["session"] = WorkflowEntry{State: "成功", Detail: detail}
	}

	if !explicit["link"] && hasLink {
		out["link"] = WorkflowEntry{State: "成功", Detail: "长链已保存"}
	}

	if !explicit["trial"] {
		trialStatus := detailSessionText(session, "plus_trial_status")
		if trialStatus != "" {
			state := "进行中"
			switch trialStatus {
			case "eligible":
				state = "成功"
			case "not_eligible", "error":
				state = "失败"
			}
			eligible := detailSessionText(session, "plus_trial_eligible")
			out["trial"] = WorkflowEntry{
				State:  state,
				Detail: trialStatus + " eligible=" + eligible,
			}
		}
	}
	return out
}

func detailSessionText(session map[string]any, key string) string {
	return strings.TrimSpace(detailSessionRaw(session, key))
}

func detailSessionRaw(session map[string]any, key string) string {
	return detailTextOr(session[key], "")
}

func detailFirstText(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(detailTextOr(value, "")); text != "" {
			return text
		}
	}
	return ""
}

func detailMapValue(values map[string]any, key string) any {
	if values == nil {
		return nil
	}
	return values[key]
}

func detailText(value any) string {
	return detailTextOr(value, "")
}

// detailTextOr 对齐 Python 的 str(value or fallback)，避免 JSON null 被显示为
// 字面量 "None"。
func detailTextOr(value any, fallback string) string {
	if !settings.PyTruthy(value) {
		return fallback
	}
	return settings.PyStr(value)
}

func detailClipRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func detailCloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = detailCloneValue(value)
	}
	return out
}

func detailCloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return detailCloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = detailCloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	default:
		return value
	}
}
