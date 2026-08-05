package ui

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestLocalOpsManualSessionMergeAndWorkflowClear(t *testing.T) {
	token := localOpsTestJWT("free", "acct-token")
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{
			Email: "session@example.com", AccountType: "free", Group: models.AccountDefaultGroup,
		}},
		map[string]any{
			"session@example.com": map[string]any{
				"storage_state_json": "keep-storage",
				"plan_type":          "old-plan",
				"workflow":           map[string]any{"auth": map[string]any{"state": "成功"}},
			},
		},
	))
	sessionText := `{"accessToken":` + localOpsJSONString(token) + `,"account":{"id":"acct-session"},"plan_type":"plus","note":"中文"}`
	result, err := app.MergeManualSession("SESSION@example.com", sessionText, "team")
	if err != nil {
		t.Fatalf("MergeManualSession: %v", err)
	}
	if result.Email != "session@example.com" || result.PlanType != "team" || result.Status != "Session已手动填入" {
		t.Fatalf("MERGE 返回异常: %#v", result)
	}
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	payload := sessionResultsFromSnapshot(snapshot)["session@example.com"].(map[string]any)
	if payload["storage_state_json"] != "keep-storage" {
		t.Fatal("MERGE 丢失了旧 Session payload 字段")
	}
	if payload["access_token"] != token || payload["plan_type"] != "team" {
		t.Fatalf("MERGE 新字段异常: %#v", payload)
	}
	if payload["account_id"] != "acct-token" || payload["chatgpt_account_id"] != "acct-token" {
		t.Fatalf("JWT 中的账号 ID 应优先于 Session body: %#v", payload)
	}
	if !strings.Contains(payload["session_json"].(string), `"note": "中文"`) {
		t.Fatalf("Session JSON 未保留 UTF-8 内容: %q", payload["session_json"])
	}
	page, _ := app.ListAccounts(AccountFilter{})
	row := localRowsByKey(page.Rows)["session@example.com"]
	if row.AccountType != "team" || row.Status != "Session已手动填入" {
		t.Fatalf("账号字段未随 MERGE 更新: %#v", row)
	}

	cleared, err := app.ClearAccountWorkflow("session@example.com")
	if err != nil || !cleared.Changed {
		t.Fatalf("ClearAccountWorkflow = %#v, %v", cleared, err)
	}
	snapshot, _ = app.snapshot()
	payload = sessionResultsFromSnapshot(snapshot)["session@example.com"].(map[string]any)
	if _, ok := payload["workflow"]; ok {
		t.Fatal("workflow 键仍存在")
	}
	if payload["storage_state_json"] != "keep-storage" || payload["access_token"] != token {
		t.Fatal("清空 workflow 改动了其他 Session 字段")
	}
	cleared, err = app.ClearAccountWorkflow("session@example.com")
	if err != nil || cleared.Changed {
		t.Fatalf("重复清空应是无副作用成功: %#v, %v", cleared, err)
	}
}

func TestLocalOpsManualSessionReplaceCreatesIndependentAccount(t *testing.T) {
	token := localOpsTestJWT("plus", "acct-plus")
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{Email: "existing@example.com", AccountType: "free"}},
		map[string]any{"existing@example.com": map[string]any{"access_token": "existing-token"}},
	))
	nested := `{"accessToken":` + localOpsJSONString(token) + `,"account":{"id":"acct-wrapper"},"plan_type":"plus"}`
	wrapper := `{"access_token":` + localOpsJSONString(token) + `,"payload":{"sessionJson":` + localOpsJSONString(nested) + `}}`
	result, err := app.ReplaceManualSession(wrapper)
	if err != nil {
		t.Fatalf("ReplaceManualSession: %v", err)
	}
	if !result.Created || !strings.HasPrefix(result.Email, "pasted-session-") || result.PlanType != "plus" {
		t.Fatalf("REPLACE 返回异常: %#v", result)
	}
	snapshot, _ := app.snapshot()
	if len(accountsFromSnapshot(snapshot)) != 2 {
		t.Fatalf("REPLACE 未创建新账号")
	}
	sessions := sessionResultsFromSnapshot(snapshot)
	if sessions["existing@example.com"].(map[string]any)["access_token"] != "existing-token" {
		t.Fatal("REPLACE 改动了已有账号 Session")
	}
	payload := sessions[result.Email].(map[string]any)
	if payload["access_token"] != token || payload["account_id"] != "acct-plus" {
		t.Fatalf("临时账号 Session 异常: %#v", payload)
	}
	page, _ := app.ListAccounts(AccountFilter{})
	row := localRowsByKey(page.Rows)[result.Email]
	if row.AccountType != "plus" || row.Status != "Session已获取" {
		t.Fatalf("临时账号字段异常: %#v", row)
	}
}

func TestLocalOpsManualSessionRejectsInvalidInputWithoutWrite(t *testing.T) {
	app, stateFile := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{Email: "a@example.com", AccountType: "free"}},
		nil,
	))
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("读取 before: %v", err)
	}
	if _, err := app.MergeManualSession("a@example.com", "not-a-token", "auto"); err == nil {
		t.Fatal("无效 Session 输入应拒绝")
	}
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("读取 after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("无效 Session 输入写入了状态文件")
	}
}

func TestLocalOpsAccessTokenExtractionParity(t *testing.T) {
	token := localOpsTestJWT("free", "acct")
	cases := []struct {
		input string
		want  string
	}{
		{"Bearer  " + token, token},
		{token, token},
		{`{"outer":{"access_token":` + localOpsJSONString(token) + `}}`, token},
		{`{"token":"","nested":{"accessToken":` + localOpsJSONString(token) + `}}`, token},
		{`"accessToken": "` + token + `"`, token},
		{`{"no_token":true}`, ""},
	}
	for _, item := range cases {
		if got := localExtractAccessToken(item.input); got != item.want {
			t.Errorf("localExtractAccessToken(%q)=%q，期望 %q", item.input, got, item.want)
		}
	}
}

func localOpsTestJWT(plan, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"exp": 4102444800,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-test",
		},
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".signature-padding-for-local-test"
}

func localOpsJSONString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
