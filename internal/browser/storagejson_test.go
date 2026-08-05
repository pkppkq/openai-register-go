package browser

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestParseStorageStateJSONConvertsLegacyPartitionKeyWithoutDroppingCookies(t *testing.T) {
	const input = `{
		"cookies": [
			{
				"name": "partitioned",
				"value": "cookie-value",
				"domain": ".example.test",
				"path": "/account",
				"expires": -1,
				"httpOnly": true,
				"secure": true,
				"sameSite": "None",
				"priority": "High",
				"sameParty": true,
				"sourceScheme": "Secure",
				"sourcePort": 443,
				"partitionKey": "https://top.example"
			},
			{
				"name": "ordinary",
				"value": "still-present",
				"domain": "example.test",
				"path": "/"
			}
		],
		"origins": [
			{
				"origin": "https://example.test",
				"localStorage": [
					{"name": "local-key", "value": "local-value"}
				],
				"sessionStorage": [
					{"name": "session-key", "value": "session-value"}
				]
			}
		]
	}`

	state, err := ParseStorageStateJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 2 {
		t.Fatalf("Cookie 数量=%d，期望 2", len(state.Cookies))
	}

	partitioned := state.Cookies[0]
	if partitioned.Name != "partitioned" ||
		partitioned.Value != "cookie-value" ||
		partitioned.Domain != ".example.test" ||
		partitioned.Path != "/account" ||
		!partitioned.HTTPOnly ||
		!partitioned.Secure ||
		partitioned.SameSite != proto.NetworkCookieSameSiteNone ||
		partitioned.Priority != proto.NetworkCookiePriorityHigh ||
		!partitioned.SameParty ||
		partitioned.SourceScheme != proto.NetworkCookieSourceSchemeSecure ||
		partitioned.SourcePort == nil ||
		*partitioned.SourcePort != 443 {
		t.Fatalf("旧 Cookie 字段未完整保留: %#v", partitioned)
	}
	if partitioned.Expires != 0 {
		t.Fatalf("Playwright 会话 Cookie expires 未归零: %v", partitioned.Expires)
	}
	if partitioned.PartitionKey == nil ||
		partitioned.PartitionKey.TopLevelSite != "https://top.example" ||
		!partitioned.PartitionKey.HasCrossSiteAncestor {
		t.Fatalf("partitionKey 未正确归一化: %#v", partitioned.PartitionKey)
	}
	if state.Cookies[1].Name != "ordinary" || state.Cookies[1].Value != "still-present" {
		t.Fatalf("普通 Cookie 被修改或丢失: %#v", state.Cookies[1])
	}
	if len(state.Origins) != 1 ||
		state.Origins[0].LocalStorage["local-key"] != "local-value" ||
		state.Origins[0].SessionStorage["session-key"] != "session-value" {
		t.Fatalf("浏览器存储未保留: %#v", state.Origins)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"expires"`) {
		t.Fatalf("会话 Cookie 的 expires 应在导出时省略: %s", raw)
	}
}

func TestParseStorageStateJSONUsesLegacyCrossSiteAncestorHint(t *testing.T) {
	state, err := ParseStorageStateJSON(`{
		"cookies": [{
			"name": "partitioned",
			"value": "value",
			"partitionKey": "https://top.example",
			"_crHasCrossSiteAncestor": false
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 ||
		state.Cookies[0].PartitionKey == nil ||
		state.Cookies[0].PartitionKey.HasCrossSiteAncestor {
		t.Fatalf("Playwright 跨站提示未保留: %#v", state.Cookies)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "_crHasCrossSiteAncestor") {
		t.Fatalf("Playwright 隐藏字段不应传给 Rod: %s", raw)
	}
}

func TestParseStorageStateJSONKeepsModernAndEmptyLegacyPartitionKeys(t *testing.T) {
	const input = `{
		"cookies": [
			{
				"name": "modern",
				"value": "one",
				"expires": 1893456000,
				"partitionKey": {
					"topLevelSite": "https://modern.example",
					"hasCrossSiteAncestor": true
				}
			},
			{
				"name": "legacy-empty",
				"value": "two",
				"partitionKey": "   "
			}
		],
		"origins": [{
			"origin": "https://modern.example",
			"localStorage": {"modern-local": "one"},
			"sessionStorage": {"modern-session": "two"}
		}]
	}`

	state, err := ParseStorageStateJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 2 {
		t.Fatalf("Cookie 数量=%d，期望 2", len(state.Cookies))
	}
	if key := state.Cookies[0].PartitionKey; key == nil ||
		key.TopLevelSite != "https://modern.example" ||
		!key.HasCrossSiteAncestor {
		t.Fatalf("新版 partitionKey 被错误修改: %#v", key)
	}
	if state.Cookies[1].Name != "legacy-empty" || state.Cookies[1].PartitionKey != nil {
		t.Fatalf("空旧 partitionKey 未按未分区 Cookie 处理: %#v", state.Cookies[1])
	}
	if state.Cookies[0].Expires != 1893456000 {
		t.Fatalf("有效 expires 被错误修改: %v", state.Cookies[0].Expires)
	}
	if len(state.Origins) != 1 ||
		state.Origins[0].LocalStorage["modern-local"] != "one" ||
		state.Origins[0].SessionStorage["modern-session"] != "two" {
		t.Fatalf("Go 对象形式的浏览器存储未保留: %#v", state.Origins)
	}
}

func TestParseStorageStateJSONRejectsUnrelatedCookieTypeError(t *testing.T) {
	_, err := ParseStorageStateJSON(`{
		"cookies": [{
			"name": "bad",
			"value": "value",
			"secure": "yes",
			"partitionKey": "https://top.example"
		}]
	}`)
	if err == nil ||
		!strings.Contains(err.Error(), "cookies[0]") ||
		!strings.Contains(err.Error(), "secure") {
		t.Fatalf("错误=%v", err)
	}
}

func TestParseStorageStateJSONRejectsMalformedPartitionMetadata(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "partitionKey 类型错误",
			input: `{"cookies":[{"name":"bad","value":"value","partitionKey":42}]}`,
		},
		{
			name:  "跨站提示类型错误",
			input: `{"cookies":[{"name":"bad","value":"value","partitionKey":"https://top.example","_crHasCrossSiteAncestor":"false"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseStorageStateJSON(test.input); err == nil ||
				!strings.Contains(err.Error(), "cookies[0]") {
				t.Fatalf("错误=%v", err)
			}
		})
	}
}

func TestParseStorageStateJSONRejectsMalformedStorageEntries(t *testing.T) {
	_, err := ParseStorageStateJSON(`{
		"cookies": [],
		"origins": [{
			"origin": "https://example.test",
			"localStorage": [{"name": "missing-value"}]
		}]
	}`)
	if err == nil ||
		!strings.Contains(err.Error(), "origins[0]") ||
		!strings.Contains(err.Error(), "localStorage") {
		t.Fatalf("错误=%v", err)
	}
}

func TestCookiesToParamsPreservesPartitionKeyForExport(t *testing.T) {
	source := &proto.NetworkCookie{
		Name:   "partitioned",
		Value:  "value",
		Domain: "third-party.example",
		Path:   "/",
		PartitionKey: &proto.NetworkCookiePartitionKey{
			TopLevelSite:         "https://top.example",
			HasCrossSiteAncestor: true,
		},
	}
	params := cookiesToParams([]*proto.NetworkCookie{source})
	if len(params) != 1 || params[0].PartitionKey == nil {
		t.Fatalf("导出时丢失 partitionKey: %#v", params)
	}
	if params[0].PartitionKey.TopLevelSite != "https://top.example" ||
		!params[0].PartitionKey.HasCrossSiteAncestor {
		t.Fatalf("导出的 partitionKey 不一致: %#v", params[0].PartitionKey)
	}
	if params[0].PartitionKey == source.PartitionKey {
		t.Fatal("导出结果复用了源指针，后续修改可能污染浏览器返回值")
	}

	raw, err := json.Marshal(&StorageState{Cookies: params})
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		Cookies []struct {
			PartitionKey json.RawMessage `json:"partitionKey"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	if len(encoded.Cookies) != 1 ||
		!strings.Contains(string(encoded.Cookies[0].PartitionKey), `"topLevelSite":"https://top.example"`) {
		t.Fatalf("导出的 JSON 未包含对象 partitionKey: %s", raw)
	}
}
