// Tests for the mint half of the provider pool.
//
// MONEY SAFETY: nothing in this file (or in manager_test.go) opens a socket.
// Minted URLs are compared as strings and every probe is a fake; a real probe
// would burn a billed provider session. The endpoints below are deliberately
// unroutable placeholders — the shape, not the host, is what is under test.
package providerproxy

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

func mintConfig(regions string) settings.ProviderProxyConfig {
	return settings.ProviderProxyConfig{
		Enabled:  true,
		Username: "testaccount",
		Password: "s3cret",
		Endpoint: "us2.proxy.invalid:3010",
		Duration: 5,
		Regions:  regions,
	}
}

func TestRolesOrder(t *testing.T) {
	want := []proxypool.Role{proxypool.RoleCreate, proxypool.RoleFollowup, proxypool.RoleApprove}
	if len(Roles) != len(want) {
		t.Fatalf("Roles = %v, want %v", Roles, want)
	}
	for i, role := range want {
		if Roles[i] != role {
			t.Fatalf("Roles[%d] = %q, want %q (app.py:293 order is load-bearing)", i, Roles[i], role)
		}
	}
	// The register pool has no provider config (app.py:293 lists three roles).
	if IsRole(proxypool.RoleRegister) {
		t.Fatal("register must not be a provider role")
	}
	if got := RoleLabel(proxypool.RoleCreate); got != "第一步" {
		t.Fatalf("RoleLabel(create) = %q, want 第一步", got)
	}
	if got := RoleLabel(proxypool.RoleFollowup); got != "后续" {
		t.Fatalf("RoleLabel(followup) = %q, want 后续", got)
	}
	if got := RoleLabel(proxypool.RoleApprove); got != "Approve" {
		t.Fatalf("RoleLabel(approve) = %q, want Approve", got)
	}
	if got := RoleLabel(proxypool.RoleRegister); got != "register" {
		t.Fatalf("RoleLabel(register) = %q, want the raw name (app.py:16899 .get fallback)", got)
	}
}

func TestBuildProxyURLShape(t *testing.T) {
	// The real state.json shape: username / password / host:port / t=5 / one
	// region. app.py:1004.
	config := mintConfig("BR")
	got, err := BuildProxyURL(config, "BR", "T3stS9d8")
	if err != nil {
		t.Fatalf("BuildProxyURL: %v", err)
	}
	want := "http://testaccount-region-BR-sid-T3stS9d8-t-5:s3cret@us2.proxy.invalid:3010"
	if got != want {
		t.Fatalf("BuildProxyURL =\n %s\nwant\n %s", got, want)
	}
}

func TestBuildProxyURLFieldsEnterTheURL(t *testing.T) {
	tests := []struct {
		name   string
		config settings.ProviderProxyConfig
		region string
		want   string
	}{
		{
			// duration → -t-N (app.py:1004), the provider-side session hold in
			// minutes. It appears nowhere else.
			name: "duration",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Duration = 120
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-120:s3cret@us2.proxy.invalid:3010",
		},
		{
			// region is upper-cased and stripped with Python semantics
			// (app.py:993): \x1c is whitespace to str.strip and not to
			// strings.TrimSpace.
			name:   "region normalised",
			config: mintConfig("JP,US"),
			region: "\x1c us \x1c",
			want:   "http://testaccount-region-US-sid-T3stS9d8-t-5:s3cret@us2.proxy.invalid:3010",
		},
		{
			// urlsplit's .hostname lower-cases; url.Hostname() does not
			// (app.py:1000).
			name: "host lower-cased",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Endpoint = "US2.PROXY.Invalid:3010"
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-5:s3cret@us2.proxy.invalid:3010",
		},
		{
			// urlsplit's .port is an int, so a zero-padded port loses its
			// padding (app.py:1004).
			name: "port is an int",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Endpoint = "us2.proxy.invalid:03010"
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-5:s3cret@us2.proxy.invalid:3010",
		},
		{
			// An endpoint may carry a scheme; the minted URL is http either way
			// (app.py:999, app.py:1004).
			name: "endpoint scheme ignored",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Endpoint = "http://us2.proxy.invalid:3010"
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-5:s3cret@us2.proxy.invalid:3010",
		},
		{
			// hostname strips the brackets an IPv6 literal needs back
			// (app.py:1001).
			name: "ipv6 re-bracketed",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Endpoint = "[2001:db8::1]:3010"
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-5:s3cret@[2001:db8::1]:3010",
		},
		{
			// quote(..., safe="") on both halves of the userinfo (app.py:1002-1003).
			// QueryEscape would render the space as '+' and PathEscape would
			// leave '@' and ':' in place — either corrupts the userinfo.
			name: "userinfo percent-encoded",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Username = "user@corp"
				c.Password = "p@ss word/1:2"
				return c
			}(),
			region: "JP",
			want:   "http://user%40corp-region-JP-sid-T3stS9d8-t-5:p%40ss%20word%2F1%3A2@us2.proxy.invalid:3010",
		},
		{
			// The username is stripped again at app.py:1002; the password is
			// not — a leading space in a password is data.
			name: "username stripped, password not",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Username = "  spaced  "
				c.Password = " pad "
				return c
			}(),
			region: "JP",
			want:   "http://spaced-region-JP-sid-T3stS9d8-t-5:%20pad%20@us2.proxy.invalid:3010",
		},
		{
			// A disabled role skips validated() entirely (app.py:971) and still
			// mints. The pump never reaches it, but the function must not
			// invent a validation error Python does not raise.
			name: "disabled config still mints",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Enabled = false
				c.Duration = 999 // out of 1..120, unchecked while disabled
				return c
			}(),
			region: "JP",
			want:   "http://testaccount-region-JP-sid-T3stS9d8-t-999:s3cret@us2.proxy.invalid:3010",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildProxyURL(tc.config, tc.region, "T3stS9d8")
			if err != nil {
				t.Fatalf("BuildProxyURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("BuildProxyURL =\n %s\nwant\n %s", got, tc.want)
			}
		})
	}
}

func TestBuildProxyURLErrors(t *testing.T) {
	tests := []struct {
		name   string
		config settings.ProviderProxyConfig
		region string
		sid    string
		want   string
	}{
		{
			name:   "region not configured",
			config: mintConfig("JP"),
			region: "US",
			sid:    "T3stS9d8",
			want:   "region 未配置: US", // app.py:995
		},
		{
			name:   "sid too short",
			config: mintConfig("JP"),
			region: "JP",
			sid:    "Ab3xZ9",
			want:   "sid 必须是 8 位字母或数字", // app.py:998
		},
		{
			name:   "sid not alphanumeric",
			config: mintConfig("JP"),
			region: "JP",
			sid:    "Ab3x-90q",
			want:   "sid 必须是 8 位字母或数字",
		},
		{
			name: "enabled config is validated first",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Password = ""
				return c
			}(),
			region: "JP",
			sid:    "T3stS9d8",
			want:   "密码不能为空", // app.py:979
		},
		{
			// DIVERGENCE (documented on endpointHostPort): Python would splice
			// the literal "None" in as the port.
			name: "disabled config without a port",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JP")
				c.Enabled = false
				c.Endpoint = "us2.proxy.invalid"
				return c
			}(),
			region: "JP",
			sid:    "T3stS9d8",
			want:   "主机端口格式应为 hostname:port",
		},
		{
			name: "region list unparseable",
			config: func() settings.ProviderProxyConfig {
				c := mintConfig("JPN")
				c.Enabled = false
				return c
			}(),
			region: "JP",
			sid:    "T3stS9d8",
			want:   "国家代码必须是两位字母: JPN", // app.py:948
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildProxyURL(tc.config, tc.region, tc.sid)
			if err == nil {
				t.Fatalf("BuildProxyURL returned %q, want error %q", got, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("BuildProxyURL error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestBuildProxyURLMintsRandomSID(t *testing.T) {
	config := mintConfig("JP")
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		url, err := BuildProxyURL(config, "JP", "")
		if err != nil {
			t.Fatalf("BuildProxyURL: %v", err)
		}
		_, rest, ok := strings.Cut(url, "-sid-")
		if !ok {
			t.Fatalf("no -sid- segment in %s", url)
		}
		sid, _, _ := strings.Cut(rest, "-t-")
		if !validSID(sid) {
			t.Fatalf("minted sid %q is not 8 alphanumerics", sid)
		}
		seen[sid] = true
	}
	if len(seen) < 30 {
		t.Fatalf("only %d distinct sids out of 32 — a repeated sid is a reused provider session", len(seen))
	}
}

func TestRandomProviderSID(t *testing.T) {
	sid, err := RandomProviderSID()
	if err != nil {
		t.Fatalf("RandomProviderSID: %v", err)
	}
	if len(sid) != SIDLength || !validSID(sid) {
		t.Fatalf("RandomProviderSID = %q", sid)
	}
	for _, r := range sid {
		if !strings.ContainsRune(sidAlphabet, r) {
			t.Fatalf("sid %q contains %q, outside app.py:1034's alphabet", sid, r)
		}
	}
}

func TestRegionSelectionUsesSettingsParser(t *testing.T) {
	// parse_provider_regions is reused from internal/settings; this asserts the
	// order it hands back, since that order IS the mint round-robin.
	regions, err := settings.ParseProviderRegions(" jp,　us ;BR，jp ")
	if err != nil {
		t.Fatalf("ParseProviderRegions: %v", err)
	}
	want := []string{"JP", "US", "BR"}
	if len(regions) != len(want) {
		t.Fatalf("regions = %v, want %v", regions, want)
	}
	for i := range want {
		if regions[i] != want[i] {
			t.Fatalf("regions = %v, want %v", regions, want)
		}
	}
}

func TestProxyExitCountry(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1.2.3.4 JP/Tokyo/Tokyo Asia/Tokyo AS1234 Org ChatGPT=200 Stripe=200", "JP"},
		{"JP/Tokyo", "JP"},
		{"1.2.3.4 br/Sao Paulo", "BR"},
		{"检测失败[出口]: HTTP 429", ""},
		{"", ""},
		{"1.2.3.4 JPN/Tokyo", ""},
		// RE2's \s is ASCII-only; app.py:1039 splits on Python's Unicode \s, so
		// an ideographic space must still delimit.
		{"1.2.3.4　JP　Asia/Tokyo", "JP"},
		// str.upper() is full case mapping: U+FB01 upper-cases to "FI".
		{"1.2.3.4 ﬁ/Helsinki", "FI"},
	}
	for _, tc := range tests {
		if got := ProxyExitCountry(tc.in); got != tc.want {
			t.Fatalf("ProxyExitCountry(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProxyExitFailed(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"检测失败[出口]: HTTP 429", true},
		{"  检测失败: boom", true},
		// str.strip() eats U+001C..U+001F where strings.TrimSpace does not.
		{"\x1c检测失败: boom", true},
		{"1.2.3.4 JP/Tokyo", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := ProxyExitFailed(tc.in); got != tc.want {
			t.Fatalf("ProxyExitFailed(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestStatusText(t *testing.T) {
	if got := (Status{}).Text(); got != "未启用" {
		t.Fatalf("Text() = %q, want 未启用", got)
	}
	if got := (Status{Enabled: true, Ready: 12, Inflight: 3, Target: 500}).Text(); got != "可用 12/500 检测中 3" {
		t.Fatalf("Text() = %q", got)
	}
	// app.py:18724 is `int(status.get('target') or PROVIDER_PROXY_TARGET_STOCK)`
	// — Python truthiness, so 0 falls back to 500.
	if got := (Status{Enabled: true, Target: 0}).Text(); got != "可用 0/500 检测中 0" {
		t.Fatalf("Text() with zero target = %q", got)
	}
	if got := LoadedStatusText(true); got != "已启用，未预热" {
		t.Fatalf("LoadedStatusText(true) = %q", got)
	}
	if got := LoadedStatusText(false); got != "未启用" {
		t.Fatalf("LoadedStatusText(false) = %q", got)
	}
}

func TestPyParseInt10(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"3010", 3010, true},
		{"03010", 3010, true},
		{" 3010 ", 3010, true},
		{"+3010", 3010, true},
		{"3_010", 3010, true}, // PEP 515; strconv.Atoi rejects this
		{"-1", -1, true},      // parsed, then rejected by the range check
		{"_3010", 0, false},
		{"3__010", 0, false},
		{"", 0, false},
		{"30a0", 0, false},
	}
	for _, tc := range tests {
		got, ok := pyParseInt10(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("pyParseInt10(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEndpointHostPort(t *testing.T) {
	tests := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"us2.proxy.invalid:3010", "us2.proxy.invalid", 3010, true},
		{"http://us2.proxy.invalid:3010", "us2.proxy.invalid", 3010, true},
		{"socks5://us2.proxy.invalid:1080", "us2.proxy.invalid", 1080, true},
		{"[2001:db8::1]:3010", "2001:db8::1", 3010, true},
		{"us2.proxy.invalid", "", 0, false},
		{"us2.proxy.invalid:70000", "", 0, false},
		// _hostinfo partitions at the FIRST colon, so this is port "80:90".
		{"us2.proxy.invalid:80:90", "", 0, false},
	}
	for _, tc := range tests {
		host, port, err := endpointHostPort(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("endpointHostPort(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
		if err == nil && (host != tc.host || port != tc.port) {
			t.Fatalf("endpointHostPort(%q) = (%q, %d), want (%q, %d)", tc.in, host, port, tc.host, tc.port)
		}
	}
}
