package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pkppkq/openai-register-go/internal/accounts"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// SessionSaveResult 是手工 Session MERGE/REPLACE 的返回值。
type SessionSaveResult struct {
	Email       string         `json:"email"`
	PlanType    string         `json:"planType"`
	Status      string         `json:"status"`
	Created     bool           `json:"created"`
	Summary     map[string]any `json:"summary"`
	SessionJSON string         `json:"sessionJson"`
}

// WorkflowClearResult 返回清空流程记录是否实际改动了 Session payload。
type WorkflowClearResult struct {
	Email   string `json:"email"`
	Changed bool   `json:"changed"`
}

// MergeManualSession 把粘贴内容解析后合并到指定账号的现有 Session payload。
func (a *App) MergeManualSession(email, sessionText, planOverride string) (SessionSaveResult, error) {
	var out SessionSaveResult
	parsed, err := localParseManualSessionInput(sessionText)
	if err != nil {
		return out, err
	}
	planOverride = strings.ToLower(localStrip(planOverride))
	if planOverride == "" {
		planOverride = "auto"
	}
	if !localAllowedPlanOverride(planOverride) {
		return out, fmt.Errorf("套餐覆盖必须是 auto、plus、free、team、k12 或 pro")
	}
	if planOverride != "auto" {
		openai.ApplyInferredPlanToSummary(parsed.Summary, planOverride, "用户手动指定", "manual")
	}

	err = a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, []string{email})
		if err != nil {
			return snapshot, nil, err
		}
		account := &all[indices[0]]
		sessions := sessionResultsFromSnapshot(snapshot)
		old, _ := sessions[account.Email].(map[string]any)
		next := make(map[string]any, len(old)+8)
		for key, value := range old {
			next[key] = value
		}
		planType := strings.ToLower(localSummaryText(parsed.Summary, "plan_type"))
		accountID := localSummaryText(parsed.Summary, "account_id")
		next["access_token"] = parsed.AccessToken
		next["session_json"] = parsed.SessionJSON
		next["access_summary"] = parsed.Summary
		next["plan_type"] = localFirstText(planType, old["plan_type"])
		next["chatgpt_plan_type"] = localFirstText(planType, old["chatgpt_plan_type"])
		next["account_id"] = localFirstText(accountID, old["account_id"], old["chatgpt_account_id"])
		next["chatgpt_account_id"] = localFirstText(accountID, old["chatgpt_account_id"], old["account_id"])
		sessions[account.Email] = next

		if localSupportedAccountPlan(planType) {
			account.AccountType = planType
		}
		account.Status = "Session已手动填入"
		snapshot["accounts"] = accountsToSnapshot(all)
		snapshot["session_results"] = sessions
		out = SessionSaveResult{
			Email: account.Email, PlanType: planType, Status: account.Status,
			Summary: parsed.Summary, SessionJSON: parsed.SessionJSON,
		}
		return snapshot, map[string]bool{account.Email: true}, nil
	})
	if err != nil {
		return out, err
	}
	a.Log(fmt.Sprintf("已手动填入 Session：plan=%s", localPlanForMessage(out.PlanType)))
	return out, nil
}

// ReplaceManualSession 把粘贴内容保存为新的临时账号和全新 Session payload。
func (a *App) ReplaceManualSession(sessionText string) (SessionSaveResult, error) {
	var out SessionSaveResult
	parsed, err := localParseManualSessionInput(sessionText)
	if err != nil {
		return out, err
	}
	err = a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		email := localPastedSessionEmail(all, time.Now())
		planType := strings.ToLower(localSummaryText(parsed.Summary, "plan_type"))
		if planType == "unknown" {
			planType = ""
		}
		account := models.MailAccount{
			Email: email, AccountType: "free", Status: "Session已获取",
			Group: models.AccountDefaultGroup,
		}
		if localSupportedAccountPlan(planType) {
			account.AccountType = planType
		}
		all = append(all, account)
		sessions := sessionResultsFromSnapshot(snapshot)
		accountID := localSummaryText(parsed.Summary, "account_id")
		sessions[email] = map[string]any{
			"access_token":       parsed.AccessToken,
			"session_json":       parsed.SessionJSON,
			"access_summary":     parsed.Summary,
			"plan_type":          planType,
			"chatgpt_plan_type":  planType,
			"account_id":         accountID,
			"chatgpt_account_id": accountID,
		}
		snapshot["accounts"] = accountsToSnapshot(all)
		snapshot["session_results"] = sessions
		out = SessionSaveResult{
			Email: email, PlanType: planType, Status: account.Status, Created: true,
			Summary: parsed.Summary, SessionJSON: parsed.SessionJSON,
		}
		return snapshot, map[string]bool{email: true}, nil
	})
	if err != nil {
		return out, err
	}
	a.Log("已从粘贴 Session JSON 解析 Access Token，可继续粘贴或多选后批量提取长链")
	return out, nil
}

// ClearAccountWorkflow 删除指定账号 Session payload 中的 workflow 键。
func (a *App) ClearAccountWorkflow(email string) (WorkflowClearResult, error) {
	var out WorkflowClearResult
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, []string{email})
		if err != nil {
			return snapshot, nil, err
		}
		canonical := all[indices[0]].Email
		out.Email = canonical
		sessions := sessionResultsFromSnapshot(snapshot)
		payload, ok := sessions[canonical].(map[string]any)
		if !ok {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		if _, exists := payload["workflow"]; !exists {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		delete(payload, "workflow")
		sessions[canonical] = payload
		snapshot["session_results"] = sessions
		out.Changed = true
		return snapshot, map[string]bool{canonical: true}, nil
	})
	if err != nil {
		return out, err
	}
	a.Log("已清空当前账号流程状态")
	return out, nil
}

type localManualSession struct {
	AccessToken string
	SessionJSON string
	Summary     map[string]any
}

func localParseManualSessionInput(text string) (localManualSession, error) {
	raw := localStrip(text)
	accessToken := localExtractAccessToken(raw)
	if accessToken == "" {
		return localManualSession{}, fmt.Errorf("未从粘贴内容中解析到 accessToken")
	}

	var (
		sessionJSON    string
		summaryPayload any
		hasPayload     bool
	)
	root, rootErr := localDecodeJSON(raw)
	if rootErr == nil {
		if object, ok := root.(localJSONObject); ok {
			nestedText := localFirstJSONText(
				localObjectGet(object, "session_json"),
				localObjectGet(object, "sessionJson"),
				localObjectGet(localObjectAsObject(localObjectGet(object, "payload")), "session_json"),
				localObjectGet(localObjectAsObject(localObjectGet(object, "payload")), "sessionJson"),
			)
			if nestedText != "" {
				nested, err := localDecodeJSON(nestedText)
				if err == nil {
					if _, ok := nested.(localJSONObject); ok {
						sessionJSON = localPrettyJSON(nested)
						summaryPayload, _ = openai.DecodeOrderedJSON([]byte(nestedText))
						hasPayload = true
					}
				} else {
					sessionJSON = nestedText
				}
			} else if localFindAccessToken(object) != "" {
				sessionJSON = localPrettyJSON(object)
				summaryPayload, _ = openai.DecodeOrderedJSON([]byte(raw))
				hasPayload = true
			}
		}
	}
	if sessionJSON == "" && strings.HasPrefix(raw, "{") {
		sessionJSON = raw
	}

	summary := openai.SummarizeChatGPTAccessToken(accessToken)
	if hasPayload {
		summary = openai.SummarizeChatGPTSessionPayload(summaryPayload, accessToken)
	}
	return localManualSession{AccessToken: accessToken, SessionJSON: sessionJSON, Summary: summary}, nil
}

var localSessionTokenRE = regexp.MustCompile(`"(?:accessToken|access_token|token)"\s*:\s*"([^"]+)"`)

func localExtractAccessToken(text string) string {
	raw := localStrip(text)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "Bearer ") {
		return localStrip(raw[len("Bearer"):])
	}
	if decoded, err := localDecodeJSON(raw); err == nil {
		return localFindAccessToken(decoded)
	}
	if match := localSessionTokenRE.FindStringSubmatch(raw); match != nil {
		return localStrip(match[1])
	}
	if strings.Count(raw, ".") >= 2 && utf8.RuneCountInString(raw) > 80 {
		return raw
	}
	return ""
}

type localJSONField struct {
	Key   string
	Value any
}

type localJSONObject []localJSONField

func localDecodeJSON(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	value, err := localDecodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON 后存在多余内容")
		}
		return nil, err
	}
	return value, nil
}

func localDecodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := localJSONObject{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("JSON 对象键不是字符串")
			}
			value, err := localDecodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			replaced := false
			for index := range object {
				if object[index].Key == key {
					object[index].Value = value
					replaced = true
					break
				}
			}
			if !replaced {
				object = append(object, localJSONField{Key: key, Value: value})
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		items := []any{}
		for decoder.More() {
			value, err := localDecodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return items, nil
	default:
		return nil, fmt.Errorf("未知 JSON 分隔符: %q", delim)
	}
}

func localFindAccessToken(value any) string {
	switch typed := value.(type) {
	case localJSONObject:
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if token := localJSONValueText(localObjectGet(typed, key)); token != "" {
				return token
			}
		}
		for _, field := range typed {
			if token := localFindAccessToken(field.Value); token != "" {
				return token
			}
		}
	case []any:
		for _, item := range typed {
			if token := localFindAccessToken(item); token != "" {
				return token
			}
		}
	}
	return ""
}

func localObjectGet(object localJSONObject, key string) any {
	for _, field := range object {
		if field.Key == key {
			return field.Value
		}
	}
	return nil
}

func localObjectAsObject(value any) localJSONObject {
	object, _ := value.(localJSONObject)
	return object
}

func localFirstJSONText(values ...any) string {
	for _, value := range values {
		if text := localJSONValueText(value); text != "" {
			return text
		}
	}
	return ""
}

func localJSONValueText(value any) string {
	if !localJSONTruthy(value) {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return localStrip(typed)
	case bool:
		if typed {
			return "True"
		}
		return ""
	case json.Number:
		return typed.String()
	default:
		return localStrip(settings.PyStr(localJSONToPlain(value)))
	}
}

func localJSONTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		number, err := typed.Float64()
		return err != nil || number != 0
	case localJSONObject:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return true
	}
}

func localJSONToPlain(value any) any {
	switch typed := value.(type) {
	case localJSONObject:
		out := make(map[string]any, len(typed))
		for _, field := range typed {
			out[field.Key] = localJSONToPlain(field.Value)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = localJSONToPlain(item)
		}
		return out
	default:
		return typed
	}
}

func localPrettyJSON(value any) string {
	compact := localMarshalJSON(value)
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return string(compact)
	}
	return indented.String()
}

func localMarshalJSON(value any) []byte {
	var buffer bytes.Buffer
	localWriteJSON(&buffer, value)
	return buffer.Bytes()
}

func localWriteJSON(buffer *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case localJSONObject:
		buffer.WriteByte('{')
		for index, field := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			localWriteJSONScalar(buffer, field.Key)
			buffer.WriteByte(':')
			localWriteJSON(buffer, field.Value)
		}
		buffer.WriteByte('}')
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			localWriteJSON(buffer, item)
		}
		buffer.WriteByte(']')
	default:
		localWriteJSONScalar(buffer, typed)
	}
}

func localWriteJSONScalar(buffer *bytes.Buffer, value any) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		buffer.WriteString("null")
		return
	}
	buffer.Write(bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'}))
}

func localSummaryText(summary map[string]any, key string) string {
	return localValueText(summary[key])
}

func localFirstText(values ...any) string {
	for _, value := range values {
		if text := localValueText(value); text != "" {
			return text
		}
	}
	return ""
}

func localAllowedPlanOverride(plan string) bool {
	switch plan {
	case "auto", "plus", "free", "team", "k12", "pro":
		return true
	default:
		return false
	}
}

func localSupportedAccountPlan(plan string) bool {
	switch plan {
	case "free", "plus", "team", "k12", "pro":
		return true
	default:
		return false
	}
}

func localPlanForMessage(plan string) string {
	if plan == "" {
		return "unknown"
	}
	return plan
}

func localPastedSessionEmail(all []models.MailAccount, now time.Time) string {
	base := now.Format("pasted-session-20060102-150405")
	existing := make(map[string]bool, len(all))
	for _, account := range all {
		existing[accounts.Key(account)] = true
	}
	email := base
	for counter := 2; existing[accounts.KeyOf(email)]; counter++ {
		email = fmt.Sprintf("%s-%d", base, counter)
	}
	return email
}

func localStrip(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
	})
}
