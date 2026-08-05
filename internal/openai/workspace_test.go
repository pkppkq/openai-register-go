package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNextAuthSessionTokenFromStorageState(t *testing.T) {
	exact := `{"cookies":[{"name":"other","value":"x"},{"name":"__Secure-next-auth.session-token","value":"exact-token"}]}`
	if got := NextAuthSessionTokenFromStorageState(exact); got != "exact-token" {
		t.Fatalf("exact=%q", got)
	}
	chunks := `{"cookies":[
		{"name":"__Secure-next-auth.session-token.2","value":"C"},
		{"name":"__Secure-next-auth.session-token.0","value":"A"},
		{"name":"__Secure-next-auth.session-token.1","value":"B"}]}`
	if got := NextAuthSessionTokenFromStorageState(chunks); got != "ABC" {
		t.Fatalf("chunks=%q", got)
	}
	if got := NextAuthSessionTokenFromStorageState(`not-json`); got != "" {
		t.Fatalf("invalid=%q", got)
	}
}

func TestParseTeamWorkspaceResponsePreservesDocumentOrder(t *testing.T) {
	raw := `{"accounts":{
		"first":{"account":{"account_id":"workspace-first","role":"owner"},"plan":"team"},
		"second":{"account":{"account_id":"workspace-second","role":"owner"},"plan":"team"}}}`
	got, err := parseTeamWorkspaceResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "workspace-first" || got.Role != "owner" {
		t.Fatalf("workspace=%#v", got)
	}
	if _, err := parseTeamWorkspaceResponse(`{"accounts":{}}`); err == nil {
		t.Fatal("空工作空间应被拒绝")
	}
}

func TestParseWorkspaceSessionResponseValidatesWorkspace(t *testing.T) {
	workspaceID := "workspace-12345678"
	token := jwt(map[string]any{
		"exp": float64(2_000_000_000),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": workspaceID,
			"chatgpt_plan_type":  "team",
		},
	})
	raw, err := json.Marshal(map[string]any{"accessToken": token, "user": map[string]any{"email": "member@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseWorkspaceSessionResponse(raw, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != token || got.WorkspaceID != workspaceID ||
		!strings.Contains(got.SessionJSON, "accessToken") {
		t.Fatalf("result=%#v", got)
	}
	if _, err := parseWorkspaceSessionResponse(raw, "other-workspace"); err == nil ||
		!strings.Contains(err.Error(), "切换校验失败") {
		t.Fatalf("错误=%v", err)
	}
}
