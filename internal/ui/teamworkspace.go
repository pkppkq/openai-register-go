package ui

import (
	"context"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

type teamWorkspaceLookupFunc func(context.Context, string, string) (openai.TeamWorkspace, error)
type teamWorkspaceExchangeFunc func(context.Context, string, string, string) (openai.WorkspaceSession, error)

var networkFindTeamWorkspace teamWorkspaceLookupFunc = func(
	ctx context.Context,
	accessToken, proxyURL string,
) (openai.TeamWorkspace, error) {
	if err := ctx.Err(); err != nil {
		return openai.TeamWorkspace{}, err
	}
	result, err := openai.ChatGPTTeamWorkspaceForAccessToken(accessToken, proxyURL)
	if err != nil {
		return openai.TeamWorkspace{}, err
	}
	if err := ctx.Err(); err != nil {
		return openai.TeamWorkspace{}, err
	}
	return result, nil
}

var networkExchangeTeamWorkspace teamWorkspaceExchangeFunc = func(
	ctx context.Context,
	sessionToken, workspaceID, proxyURL string,
) (openai.WorkspaceSession, error) {
	if err := ctx.Err(); err != nil {
		return openai.WorkspaceSession{}, err
	}
	result, err := openai.ChatGPTExchangeWorkspaceSession(sessionToken, workspaceID, proxyURL)
	if err != nil {
		return openai.WorkspaceSession{}, err
	}
	if err := ctx.Err(); err != nil {
		return openai.WorkspaceSession{}, err
	}
	return result, nil
}
