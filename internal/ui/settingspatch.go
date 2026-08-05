package ui

// PatchSettings 提供原子、字段级的设置更新，避免前端先 LoadSettings、编辑
// 一个字段、再 SaveSettings 整份旧快照时，把后台刚轮换/移除的代理池恢复。

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

var patchableSettingKeys = buildPatchableSettingKeys()

func buildPatchableSettingKeys() map[string]bool {
	result := map[string]bool{}
	typ := reflect.TypeOf(settings.Settings{})
	for index := 0; index < typ.NumField(); index++ {
		tag := strings.TrimSpace(strings.Split(typ.Field(index).Tag.Get("json"), ",")[0])
		if tag != "" && tag != "-" {
			result[tag] = true
		}
	}
	return result
}

// PatchSettings 在最新磁盘状态上只覆盖 patch 指定的字段，并返回规范化后的
// 最新设置。未知字段会拒绝，避免拼写错误看似保存成功却永远不生效。
func (a *App) PatchSettings(patch map[string]any) (settings.Settings, error) {
	var updated settings.Settings
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		current, _ := snapshot["settings"].(map[string]any)
		if current == nil {
			current = map[string]any{}
		}
		if len(patch) == 0 {
			updated = settings.FromSnapshot(snapshot)
			return snapshot, map[string]bool{}, errNoStateChange
		}
		for key, value := range patch {
			if !patchableSettingKeys[key] {
				return snapshot, nil, fmt.Errorf("未知的设置字段: %s", key)
			}
			if key == "provider_proxy_configs" {
				merged, err := mergeProviderProxyPatch(current[key], value)
				if err != nil {
					return snapshot, nil, err
				}
				current[key] = merged
				continue
			}
			current[key] = value
		}
		snapshot["settings"] = current
		updated = settings.FromSnapshot(snapshot)
		cloudTouched := patchableCloudMailKeyPresent(patch)
		if cloudTouched {
			cloud, err := alias.CloudMailSettingsFrom(
				updated.CloudMailBase,
				updated.CloudMailToken,
				updated.CloudMailEnabled,
			)
			if err != nil {
				return snapshot, nil, fmt.Errorf("Cloud Mail 设置不可用: %w", err)
			}
			if cloud.Enabled && cloud.Token == "" {
				return snapshot, nil, fmt.Errorf("启用 Cloud Mail API 前请填写程序 Token")
			}
			updated.CloudMailBase = cloud.BaseURL
			updated.CloudMailToken = cloud.Token
		}
		return settings.ToSnapshot(updated, snapshot), map[string]bool{}, nil
	})
	if err != nil {
		return settings.Settings{}, err
	}
	if updated.ProxyRouteMode == settings.ProxyRouteModeLocalOnly && a.providerManager != nil {
		a.providerManager.Stop()
		a.Log("当前为“全走本地代理”，已停止并忽略提供商代理池")
	}
	return updated, nil
}

func patchableCloudMailKeyPresent(patch map[string]any) bool {
	for _, key := range []string{"cloud_mail_enabled", "cloud_mail_base", "cloud_mail_token"} {
		if _, ok := patch[key]; ok {
			return true
		}
	}
	return false
}

// provider_proxy_configs 支持按 role 深合并，使“只编辑 Approve”不会把第一步
// 和后续阶段在另一个任务中刚更新的配置整块覆盖。
func mergeProviderProxyPatch(current, patch any) (map[string]any, error) {
	base, _ := current.(map[string]any)
	if base == nil {
		base = map[string]any{}
	}
	incoming, ok := patch.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider_proxy_configs 必须是对象")
	}
	for role, value := range incoming {
		rolePatch, isMap := value.(map[string]any)
		if !isMap {
			base[role] = value
			continue
		}
		roleCurrent, _ := base[role].(map[string]any)
		if roleCurrent == nil {
			roleCurrent = map[string]any{}
		}
		for key, fieldValue := range rolePatch {
			roleCurrent[key] = fieldValue
		}
		base[role] = roleCurrent
	}
	return base, nil
}
