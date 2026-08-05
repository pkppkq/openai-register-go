package ui

import (
	"context"
	"fmt"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pkppkq/openai-register-go/internal/providerproxy"
	"github.com/pkppkq/openai-register-go/internal/proxychain"
	"github.com/pkppkq/openai-register-go/internal/proxyhealth"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/proxyroute"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// EventProviderProxyStatus 是高级代理页面的实时库存事件。
const EventProviderProxyStatus = "provider-proxy-status"

// ProviderProxyStatusView 是一个提供商阶段的配置和实时库存。
type ProviderProxyStatusView struct {
	Role   string                       `json:"role"`
	Label  string                       `json:"label"`
	Config settings.ProviderProxyConfig `json:"config"`
	Status providerproxy.Status         `json:"status"`
	Text   string                       `json:"text"`
}

// ProviderProxyStatusEvent 是后台预热泵推送到前端的单阶段更新。
type ProviderProxyStatusEvent struct {
	Role   string               `json:"role"`
	Label  string               `json:"label"`
	Status providerproxy.Status `json:"status"`
	Text   string               `json:"text"`
}

func (a *App) initProviderManager() {
	ctx, cancel := context.WithCancel(context.Background())
	a.providerCtx = ctx
	a.providerCancel = cancel
	a.providerManager = providerproxy.New(
		a.detectProviderCandidate,
		providerproxy.WithContext(ctx),
		providerproxy.WithStatusCallback(a.emitProviderProxyStatus),
	)
}

// detectProviderCandidate 是唯一会实际连到提供商代理的步骤。Manager 只在用户
// 点击“应用配置并预热”或确认启动依赖提供商池的批量任务后调用它。
func (a *App) detectProviderCandidate(
	ctx context.Context,
	candidate providerproxy.Candidate,
	localProxy string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	server := proxychain.New(localProxy, candidate.URL, proxychain.LogFunc(a.Log))
	if err := server.Start(); err != nil {
		return "", fmt.Errorf("启动提供商检测代理失败: %w", err)
	}
	defer server.Close()
	result := proxyhealth.DetectProxyHealthWithRetry(
		server.URL(),
		15,
		3,
		proxyhealth.LogFunc(a.Log),
		providerproxy.RoleLabel(candidate.Role)+"提供商",
	)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return result.Summary(), nil
}

func (a *App) emitProviderProxyStatus(role proxypool.Role, status providerproxy.Status) {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, EventProviderProxyStatus, ProviderProxyStatusEvent{
		Role:   string(role),
		Label:  providerproxy.RoleLabel(role),
		Status: status,
		Text:   status.Text(),
	})
}

// ProviderProxyStatuses 返回三个阶段的当前配置与库存，顺序固定为
// 第一步、后续、Approve。
func (a *App) ProviderProxyStatuses() []ProviderProxyStatusView {
	if a.providerManager == nil {
		return []ProviderProxyStatusView{}
	}
	out := make([]ProviderProxyStatusView, 0, len(providerproxy.Roles))
	for _, role := range providerproxy.Roles {
		status := a.providerManager.Snapshot(role)
		out = append(out, ProviderProxyStatusView{
			Role:   string(role),
			Label:  providerproxy.RoleLabel(role),
			Config: a.providerManager.Config(role),
			Status: status,
			Text:   status.Text(),
		})
	}
	return out
}

// ApplyProviderProxySettings 校验已保存配置并启动后台预热。
//
// 预热会使用提供商流量；后端必须先收到明确确认。
func (a *App) ApplyProviderProxySettings(confirmed bool) ([]ProviderProxyStatusView, error) {
	if !confirmed {
		return nil, fmt.Errorf("应用提供商代理设置并启动预热前必须确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	st := settings.FromSnapshot(snapshot)
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		a.providerManager.Stop()
		a.Log("当前为“全走本地代理”，已忽略提供商代理池")
		return a.ProviderProxyStatuses(), nil
	}
	if err := validateProviderSettings(st); err != nil {
		return nil, err
	}
	a.providerManager.UpdateMaxWorkers(st.LinkProxyPrecheckConcurrency)
	if err := a.providerManager.Configure(
		providerproxy.ConfigsFromSettings(st.ProviderProxyConfigs),
		st.LocalProxy,
	); err != nil {
		return nil, fmt.Errorf("提供商代理配置无效: %w", err)
	}
	a.Log("已应用提供商代理配置，后台开始预热")
	return a.ProviderProxyStatuses(), nil
}

// StopProviderProxyPools 停止预热泵；已写入的设置不变。
func (a *App) StopProviderProxyPools() []ProviderProxyStatusView {
	if a.providerManager != nil {
		a.providerManager.Stop()
	}
	a.Log("已停止提供商代理池")
	return a.ProviderProxyStatuses()
}

func validateProviderSettings(st settings.Settings) error {
	if err := proxyroute.CheckJapanExtractProvider(st); err != nil {
		return err
	}
	configs := providerproxy.ConfigsFromSettings(st.ProviderProxyConfigs)
	for _, role := range providerproxy.Roles {
		if err := configs[role].Validate(); err != nil {
			return fmt.Errorf("%s: %w", providerproxy.RoleLabel(role), err)
		}
	}
	return nil
}
