package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

func TestNetworkResolveTeamAccessExchangesPersonalSession(t *testing.T) {
	personalToken := networkTestJWT("free", "personal-account")
	teamToken := networkTestJWT("team", "workspace-team")
	payload := map[string]any{
		"access_token": personalToken,
		"storage_state_json": `{"cookies":[
			{"name":"__Secure-next-auth.session-token.1","value":"B"},
			{"name":"__Secure-next-auth.session-token.0","value":"A"}]}`,
	}

	oldLookup := networkFindTeamWorkspace
	oldExchange := networkExchangeTeamWorkspace
	t.Cleanup(func() {
		networkFindTeamWorkspace = oldLookup
		networkExchangeTeamWorkspace = oldExchange
	})
	networkFindTeamWorkspace = func(
		ctx context.Context, accessToken, proxyURL string,
	) (openai.TeamWorkspace, error) {
		if ctx.Err() != nil || accessToken != personalToken || proxyURL != "http://proxy.invalid:1234" {
			t.Fatalf("workspace 查询参数不符: %q %q", accessToken, proxyURL)
		}
		return openai.TeamWorkspace{AccountID: "workspace-team", PlanType: "team", Role: "member"}, nil
	}
	networkExchangeTeamWorkspace = func(
		ctx context.Context, sessionToken, workspaceID, proxyURL string,
	) (openai.WorkspaceSession, error) {
		if ctx.Err() != nil || sessionToken != "AB" || workspaceID != "workspace-team" {
			t.Fatalf("workspace 交换参数不符: %q %q", sessionToken, workspaceID)
		}
		return openai.WorkspaceSession{
			AccessToken: teamToken,
			Summary: map[string]any{
				"plan_type":  "team",
				"account_id": "workspace-team",
			},
			WorkspaceID: "workspace-team",
		}, nil
	}

	got, err := networkResolveTeamAccess(context.Background(), models.MailAccount{
		Email: "member@example.com",
	}, payload, "http://proxy.invalid:1234")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != teamToken || got.AccountID != "workspace-team" ||
		networkText(got.AccessSummary["plan_type"]) != "team" {
		t.Fatalf("result=%#v", got)
	}
}

func TestNetworkResolveTeamAccessRefusesMismatchedTokenWithoutCookie(t *testing.T) {
	oldLookup := networkFindTeamWorkspace
	t.Cleanup(func() { networkFindTeamWorkspace = oldLookup })
	networkFindTeamWorkspace = func(
		context.Context, string, string,
	) (openai.TeamWorkspace, error) {
		return openai.TeamWorkspace{AccountID: "workspace-team", PlanType: "team"}, nil
	}
	_, err := networkResolveTeamAccess(context.Background(), models.MailAccount{},
		map[string]any{"access_token": networkTestJWT("free", "personal")}, "")
	if err == nil || !strings.Contains(err.Error(), "Session Cookie") {
		t.Fatalf("错误=%v", err)
	}
}
