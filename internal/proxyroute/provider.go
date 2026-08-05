package proxyroute

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// ProviderRoles is PROVIDER_PROXY_ROLES (app.py:293) as proxypool roles, in
// order. Always range over this, never over the settings map: Python's dict is
// ordered and Go's is not, and the order decides log text and which role is
// reported first.
var ProviderRoles = []proxypool.Role{proxypool.RoleCreate, proxypool.RoleFollowup, proxypool.RoleApprove}

// ProviderConfig returns the persisted provider config for one stage.
// settings.FromSnapshot has already applied ProxyProviderConfig.from_state
// (app.py:1017-1030), including the 1..120 duration clamp.
func ProviderConfig(cfg settings.Settings, role proxypool.Role) settings.ProviderProxyConfig {
	if c, ok := cfg.ProviderProxyConfigs[string(role)]; ok {
		return c
	}
	return settings.DefaultProviderProxyConfig()
}

// EnabledProviderRoles is ProviderProxyPoolManager.enabled_roles
// (app.py:1141-1143), read off the persisted configs (which is what
// _apply_provider_proxy_configs hands the manager, app.py:12521).
func EnabledProviderRoles(cfg settings.Settings) []proxypool.Role {
	var out []proxypool.Role
	for _, role := range ProviderRoles {
		if ProviderConfig(cfg, role).Enabled {
			out = append(out, role)
		}
	}
	return out
}

// ProviderRolesNeeded mirrors _provider_roles_needed_for_link
// (app.py:16879-16890): the enabled provider roles that have neither a fixed
// (reuse) proxy nor any manual pool entry, i.e. the stages that would have to
// draw from the provider pool.
//
// 全走本地代理 returns nothing at all (app.py:16880-16881) — the same gate that
// empties every manual pool also switches the provider pools off.
func ProviderRolesNeeded(cfg settings.Settings, createProxies, followupProxies, approveProxies []string, fixed map[proxypool.Role]string) []proxypool.Role {
	if proxypool.NormalizeRouteMode(cfg.ProxyRouteMode) == proxypool.RouteModeLocalOnly {
		return nil
	}
	has := map[proxypool.Role]bool{
		proxypool.RoleCreate:   len(createProxies) > 0,
		proxypool.RoleFollowup: len(followupProxies) > 0,
		proxypool.RoleApprove:  len(approveProxies) > 0,
	}
	var out []proxypool.Role
	for _, role := range EnabledProviderRoles(cfg) {
		if fixed[role] != "" {
			continue
		}
		if has[role] {
			continue
		}
		out = append(out, role)
	}
	return out
}

// CheckJapanExtractProvider mirrors app.py:12516-12519: with 强制日本出口 on,
// the create-stage provider may only be configured for JP. This is the only
// way require_japan_extract_proxy touches proxy *selection* — everywhere else
// (app.py:17166) it gates on a measured proxy exit, which needs a live probe
// and is out of this package's scope.
func CheckJapanExtractProvider(cfg settings.Settings) error {
	if !cfg.RequireJapanExtractProxy {
		return nil
	}
	create := ProviderConfig(cfg, proxypool.RoleCreate)
	if !create.Enabled {
		return nil
	}
	regions, err := settings.ParseProviderRegions(create.Regions)
	if err != nil {
		return err
	}
	for _, region := range regions {
		if region != "JP" {
			return errors.New("已启用“强制日本出口”，第一步提供商 region 只能填写 JP")
		}
	}
	return nil
}

var providerSIDRe = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)

// BuildProviderProxyURL mirrors ProxyProviderConfig.build_proxy_url
// (app.py:991-1004): the rotating-provider credentials encode the region, the
// session id and the session lifetime in the username.
//
// An empty sid gets a fresh random one, matching `sid or random_provider_sid()`.
func BuildProviderProxyURL(c settings.ProviderProxyConfig, region, sid string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	// Validate() is a no-op for a disabled role (app.py:970-972), so a disabled
	// config would otherwise build a URL out of blank credentials.
	if !c.Enabled {
		return "", errors.New("提供商代理未启用")
	}
	region = pyUpper(pyStrip(region))
	regions, err := settings.ParseProviderRegions(c.Regions)
	if err != nil {
		return "", err
	}
	if !containsString(regions, region) {
		return "", fmt.Errorf("region 未配置: %s", region)
	}
	if sid == "" {
		sid = RandomProviderSID()
	}
	if !providerSIDRe.MatchString(sid) {
		return "", errors.New("sid 必须是 8 位字母或数字")
	}
	endpoint := pyStrip(c.Endpoint)
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	// urlsplit().hostname lowercases and unwraps [] on IPv6; net/url does the
	// unwrapping but not the lowercasing.
	host := strings.ToLower(parsed.Hostname())
	hostText := host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		hostText = "[" + host + "]"
	}
	// urlsplit().port is an int, so "0080" prints as "80".
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return "", fmt.Errorf("主机端口格式应为 hostname:port")
	}
	username := pyQuoteAll(pyStrip(c.Username))
	password := pyQuoteAll(c.Password)
	return fmt.Sprintf("http://%s-region-%s-sid-%s-t-%d:%s@%s:%d",
		username, region, sid, c.Duration, password, hostText, port), nil
}

const providerSIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomProviderSID mirrors random_provider_sid (app.py:1032-1034):
// 8 characters from secrets.choice over [A-Za-z0-9].
func RandomProviderSID() string {
	out := make([]byte, 8)
	n := big.NewInt(int64(len(providerSIDAlphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			// crypto/rand does not fail in practice; degrade to a valid sid
			// rather than returning something that fails the 8-char check.
			out[i] = providerSIDAlphabet[0]
			continue
		}
		out[i] = providerSIDAlphabet[v.Int64()]
	}
	return string(out)
}

// pyQuoteAll is urllib.parse.quote(s, safe="") — percent-encode every byte that
// is not in Python's _ALWAYS_SAFE set (letters, digits, "_.-~"). Neither
// url.QueryEscape (turns " " into "+") nor url.PathEscape (leaves ":@&=+$," and
// friends alone) matches it.
func pyQuoteAll(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
