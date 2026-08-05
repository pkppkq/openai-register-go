// Package providerproxy 迁移旧版 Python/Tkinter app.py 中的
// ProviderProxyPoolManager 和 ProxyProviderConfig 提供商侧逻辑。
//
// This is UI_SPEC gap G19. Where internal/proxypool rotates a *pasted list* of
// proxy URLs, this package mints them: a provider account
// (username / password / endpoint / duration / regions) is turned into an
// endless supply of single-use session URLs, one background pool per role
// (create / followup / approve). Each minted URL is probed once for its exit
// country and only stocked if the country matches the region it asked for.
//
// Reference implementation:
//   - parse_provider_regions         app.py:940-954   (reused from internal/settings)
//   - ProxyProviderConfig            app.py:958-1030  (reused from internal/settings)
//   - ProxyProviderConfig.build_proxy_url app.py:991-1004 (ported here, mint.go)
//   - random_provider_sid            app.py:1033-1035
//   - proxy_exit_country             app.py:1038-1040
//   - _proxy_exit_failed_text        app.py:4014-4015
//   - ProviderProxyCandidate         app.py:1044-1048
//   - ProviderProxyPoolManager       app.py:1051-1276
//   - constants                      app.py:288-294
//
// MONEY SAFETY: every minted URL is a *billed provider session*. The package
// never opens a socket on its own — the single outbound step, the exit probe,
// is the injected Detector (the port of GUI._detect_provider_proxy_candidate,
// app.py:12644-12649). Time is the injected Clock. Both exist so that the whole
// pool can be exercised without minting anything real.
//
// Where Go's natural behaviour differs from Python's, Python wins; every such
// spot carries a comment citing the app.py line, and the handful of places
// where Go *cannot* match are marked DIVERGENCE.
package providerproxy

import (
	"regexp"
	"strconv"
	"time"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// Pool sizing, app.py:288-292.
const (
	// TargetStock is PROVIDER_PROXY_TARGET_STOCK (app.py:288): the per-role
	// ready-queue length the background pump refills towards.
	TargetStock = 500
	// LowWater is PROVIDER_PROXY_LOW_WATER (app.py:289): dropping to or below
	// it re-arms refilling, and it is also the stock 批量提链 waits for before
	// it starts (app.py:23410).
	LowWater = 200
	// MaxWorkers is PROVIDER_PROXY_MAX_WORKERS (app.py:290), the default cap on
	// concurrent exit probes. app.py overrides it per apply with
	// link_proxy_precheck_concurrency (app.py:12520).
	MaxWorkers = 30
	// TakeTimeout is PROVIDER_PROXY_TAKE_TIMEOUT (app.py:291) — the budget for
	// the whole create+followup+approve triple, not per role (app.py:23357).
	TakeTimeout = 60 * time.Second
)

// BackoffSeconds is PROVIDER_PROXY_BACKOFF_SECONDS (app.py:292). Consecutive
// failed probes index into it, clamped at the last entry (app.py:1271).
var BackoffSeconds = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// Roles is PROVIDER_PROXY_ROLES (app.py:293) as proxypool roles.
//
// Order is load-bearing three times over: it is the mint round-robin
// (app.py:1217-1221), the order 提供商兜底 is rendered in (app.py:16899), and
// the order the triple is acquired in (app.py:23359). It is derived from
// settings.ProviderProxyRoles rather than re-typed so there is one source of
// truth, and every loop in this package ranges over this slice — NEVER over a
// map, whose Go iteration order is random where a Python dict's is insertion
// ordered.
var Roles = func() []proxypool.Role {
	roles := make([]proxypool.Role, 0, len(settings.ProviderProxyRoles))
	for _, role := range settings.ProviderProxyRoles {
		roles = append(roles, proxypool.Role(role))
	}
	return roles
}()

// RoleLabel is PROVIDER_PROXY_ROLE_LABELS (app.py:294): 第一步 / 后续 / Approve.
// Unknown roles fall back to the raw name, matching the `.get(role, role)` at
// app.py:16899.
func RoleLabel(role proxypool.Role) string {
	if label, ok := settings.ProviderProxyRoleLabels[string(role)]; ok {
		return label
	}
	return string(role)
}

// IsRole reports whether role is one of the three provider roles. proxypool has
// a fourth (register) which has no provider pool.
func IsRole(role proxypool.Role) bool {
	for _, candidate := range Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

// Candidate is ProviderProxyCandidate (app.py:1044-1048): one minted provider
// session plus the exit string the probe came back with.
type Candidate struct {
	// Role is the pool it was minted for.
	Role proxypool.Role `json:"role"`
	// URL is the minted session URL — a billed session the moment it is used.
	URL string `json:"url"`
	// Region is the two-letter country the URL asked the provider for.
	Region string `json:"region"`
	// ProxyExit is the Detector's verdict, e.g.
	// "1.2.3.4 JP/Tokyo/Tokyo Asia/Tokyo AS1234 ChatGPT=200 Stripe=200", or a
	// string starting 检测失败 (app.py:763-778, app.py:1250-1252).
	ProxyExit string `json:"proxy_exit"`
}

// Status is the snapshot dict of app.py:1149-1158, keyed the same way so it can
// go straight to the frontend.
type Status struct {
	Enabled  bool `json:"enabled"`
	Ready    int  `json:"ready"`
	Inflight int  `json:"inflight"`
	Target   int  `json:"target"`
	LowWater int  `json:"low_water"`
	Failures int  `json:"failures"`
}

// Text ports the 状态 cell the event pump writes (app.py:18721-18728).
//
// `int(status.get('target') or PROVIDER_PROXY_TARGET_STOCK)` is Python
// truthiness, not a nil check: a target of 0 renders as 500.
func (s Status) Text() string {
	if !s.Enabled {
		return "未启用"
	}
	target := s.Target
	if target == 0 {
		target = TargetStock
	}
	return "可用 " + strconv.Itoa(s.Ready) + "/" + strconv.Itoa(target) +
		" 检测中 " + strconv.Itoa(s.Inflight)
}

// LoadedStatusText ports _show_loaded_provider_proxy_status (app.py:12540) —
// the状态 shown after load, before any pump has run.
func LoadedStatusText(enabled bool) string {
	if enabled {
		return "已启用，未预热"
	}
	return "未启用"
}

// proxyExitCountryRe is proxy_exit_country's pattern (app.py:1039).
//
// TRAP: Python's `\s` on a str pattern is Unicode-aware; RE2's is ASCII-only
// and omits \v and U+001C..U+001F, which str.isspace() does match. The class is
// spelled out the same way internal/settings and internal/proxypool spell
// theirs. Python's unanchored `$` also matches before a trailing newline, but
// that case is already covered by the `\s` alternative (the newline itself
// matches), so no extra alternative is needed.
var proxyExitCountryRe = regexp.MustCompile(
	`(?:^|[\s\p{Z}\x{0085}\x{000B}\x{001C}-\x{001F}])([A-Z]{2})(?:/|[\s\p{Z}\x{0085}\x{000B}\x{001C}-\x{001F}]|$)`)

// ProxyExitCountry ports proxy_exit_country (app.py:1038-1040): the two-letter
// country pulled out of a ProxyHealthResult.summary (app.py:762-778, which
// renders location as "COUNTRY/REGION/CITY"). Returns "" when absent.
func ProxyExitCountry(proxyExit string) string {
	match := proxyExitCountryRe.FindStringSubmatch(pyUpper(proxyExit))
	if match == nil {
		return ""
	}
	return match[1]
}

// ProxyExitFailed ports _proxy_exit_failed_text (app.py:4014-4015).
func ProxyExitFailed(proxyExit string) bool {
	return hasPrefix(pyStrip(proxyExit), "检测失败")
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
