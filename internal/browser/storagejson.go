package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// ParseStorageStateJSON 解析 Go-Rod 与旧 Playwright 生成的 storage state。
//
// Playwright 的 cookies[].partitionKey 是顶层站点字符串，而新版 Chrome
// DevTools Protocol 使用包含 topLevelSite 和 hasCrossSiteAncestor 的对象。
// 这里在进入 Go-Rod 前完成兼容转换，避免一个旧字段导致整份 Cookie 丢失。
func ParseStorageStateJSON(text string) (*StorageState, error) {
	var raw struct {
		Cookies []json.RawMessage `json:"cookies"`
		Origins []json.RawMessage `json:"origins"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}

	state := &StorageState{
		Cookies: make([]*proto.NetworkCookieParam, 0, len(raw.Cookies)),
		Origins: make([]OriginStorage, 0, len(raw.Origins)),
	}
	for index, cookieRaw := range raw.Cookies {
		cookie, err := parseCompatibleCookie(cookieRaw)
		if err != nil {
			return nil, fmt.Errorf("cookies[%d]: %w", index, err)
		}
		state.Cookies = append(state.Cookies, cookie)
	}
	for index, originRaw := range raw.Origins {
		origin, err := parseCompatibleOrigin(originRaw)
		if err != nil {
			return nil, fmt.Errorf("origins[%d]: %w", index, err)
		}
		state.Origins = append(state.Origins, origin)
	}
	return state, nil
}

func parseCompatibleCookie(raw json.RawMessage) (*proto.NetworkCookieParam, error) {
	var cookie *proto.NetworkCookieParam
	if err := json.Unmarshal(raw, &cookie); err == nil {
		normalizeCookieExpiry(cookie)
		return cookie, nil
	} else {
		originalErr := err

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, originalErr
		}
		partitionRaw, ok := fields["partitionKey"]
		if !ok {
			return nil, originalErr
		}

		var legacyPartitionKey string
		if err := json.Unmarshal(partitionRaw, &legacyPartitionKey); err != nil {
			return nil, originalErr
		}
		legacyPartitionKey = strings.TrimSpace(legacyPartitionKey)
		if legacyPartitionKey == "" {
			// 空字符串在旧格式中等同于未分区 Cookie。
			delete(fields, "partitionKey")
		} else {
			hasCrossSiteAncestor := true
			if ancestorRaw, ok := fields["_crHasCrossSiteAncestor"]; ok {
				if err := json.Unmarshal(ancestorRaw, &hasCrossSiteAncestor); err != nil {
					return nil, fmt.Errorf("_crHasCrossSiteAncestor 必须是布尔值: %w", err)
				}
			}
			normalized, err := json.Marshal(proto.NetworkCookiePartitionKey{
				TopLevelSite:         legacyPartitionKey,
				HasCrossSiteAncestor: hasCrossSiteAncestor,
			})
			if err != nil {
				return nil, err
			}
			fields["partitionKey"] = normalized
		}
		// 这是 Playwright 的内部恢复提示，不属于 CDP CookieParam。
		delete(fields, "_crHasCrossSiteAncestor")

		normalizedRaw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		// 第一次失败的 Unmarshal 可能已经部分填充 cookie；必须清空，
		// 否则已删除的空 partitionKey 会残留为一个零值对象。
		cookie = nil
		if err := json.Unmarshal(normalizedRaw, &cookie); err != nil {
			return nil, err
		}
		normalizeCookieExpiry(cookie)
		return cookie, nil
	}
}

// Playwright 用 -1 表示会话 Cookie；CDP 的 SetCookies 则要求会话 Cookie
// 不携带 expires。归零后 proto 的 omitempty 会在再次导出时省略该字段。
func normalizeCookieExpiry(cookie *proto.NetworkCookieParam) {
	if cookie != nil && cookie.Expires < 0 {
		cookie.Expires = 0
	}
}

func parseCompatibleOrigin(raw json.RawMessage) (OriginStorage, error) {
	var encoded struct {
		Origin         string          `json:"origin"`
		LocalStorage   json.RawMessage `json:"localStorage"`
		SessionStorage json.RawMessage `json:"sessionStorage"`
	}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return OriginStorage{}, err
	}
	local, err := parseCompatibleStorageEntries(encoded.LocalStorage)
	if err != nil {
		return OriginStorage{}, fmt.Errorf("localStorage: %w", err)
	}
	session, err := parseCompatibleStorageEntries(encoded.SessionStorage)
	if err != nil {
		return OriginStorage{}, fmt.Errorf("sessionStorage: %w", err)
	}
	return OriginStorage{
		Origin:         encoded.Origin,
		LocalStorage:   local,
		SessionStorage: session,
	}, nil
}

// Playwright 将 Web Storage 编码为 [{name,value}]，Go 版历史数据则使用
// {"name":"value"}；内部统一为 map，重复键按浏览器 setItem 的行为以后值为准。
func parseCompatibleStorageEntries(raw json.RawMessage) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}

	switch raw[0] {
	case '{':
		var entries map[string]string
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	case '[':
		var encoded []struct {
			Name  *string `json:"name"`
			Value *string `json:"value"`
		}
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, err
		}
		entries := make(map[string]string, len(encoded))
		for index, entry := range encoded {
			if entry.Name == nil || entry.Value == nil {
				return nil, fmt.Errorf("条目[%d] 缺少字符串 name 或 value", index)
			}
			entries[*entry.Name] = *entry.Value
		}
		return entries, nil
	default:
		return nil, errors.New("必须是对象或 {name,value} 数组")
	}
}
