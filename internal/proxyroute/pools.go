package proxyroute

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// router carries the settings, the resolved region lock and the log sink for
// one selection pass. It owns no listeners; everything here is pure.
type router struct {
	cfg       settings.Settings
	log       LogFunc
	region    string
	localOnly bool
}

func newRouter(cfg settings.Settings, log LogFunc) *router {
	if log == nil {
		log = func(string) {}
	}
	return &router{
		cfg:    cfg,
		log:    log,
		region: RegionCode(cfg),
		// _local_proxy_only_enabled (app.py:16712-16715).
		localOnly: proxypool.NormalizeRouteMode(cfg.ProxyRouteMode) == proxypool.RouteModeLocalOnly,
	}
}

func (r *router) label() string { return regionLabel(r.region) }

// readPool is the shared body of the four _read_*_dynamic_proxies helpers
// (app.py:16724-16742): 代理模式=全走本地代理 empties EVERY pool, so the chain
// degenerates to the local proxy alone.
func (r *router) readPool(text string) []string {
	if r.localOnly {
		return nil
	}
	return proxypool.ParseProxyPoolText(text)
}

// The four raw pools and the settings key each one really reads. Verified
// against app.py rather than against the widget names:
//
//	register (注册/获取 Session) -> dynamic_proxies        app.py:14090-14092 / 16724
//	create   (创建长链第一步)    -> payment_dynamic_proxy  app.py:14093-14096 / 16730
//	followup (创建长链后续)      -> followup_dynamic_proxy app.py:14097-14100 / 16735
//	approve  (Approve)           -> approve_dynamic_proxy  app.py:14101-14104 / 16740
func (r *router) registerPool() []string { return r.readPool(r.cfg.DynamicProxies) }
func (r *router) createPool() []string   { return r.readPool(r.cfg.PaymentDynamicProxy) }
func (r *router) followupPool() []string { return r.readPool(r.cfg.FollowupDynamicProxy) }
func (r *router) approvePool() []string  { return r.readPool(r.cfg.ApproveDynamicProxy) }

// filterByRegion mirrors _filter_link_proxies_by_region (app.py:16815-16850):
// keep the entries that already sit in the target region, rewriting a
// region-tagged URL to the target region where that is possible; drop the rest.
// With no region lock the list passes through untouched.
func (r *router) filterByRegion(proxies []string, roleLabel string) []string {
	if r.region == "" {
		return proxies
	}
	var matched []string
	skipped := map[string]int{}
	rewrittenFrom := map[string]int{}
	unknown := 0
	rewrittenCount := 0
	for _, proxy := range proxies {
		proxyRegion := RegionCodeFromText(proxy)
		rewritten := RewriteProxyRegionCode(proxy, r.region)
		switch {
		case RegionCodeFromText(rewritten) == r.region:
			matched = append(matched, rewritten)
			if proxyRegion != "" && proxyRegion != r.region && rewritten != proxy {
				rewrittenCount++
				rewrittenFrom[proxyRegion]++
			}
		case proxyRegion != "":
			skipped[proxyRegion]++
		default:
			unknown++
		}
	}
	if len(proxies) > 0 && (len(matched) != len(proxies) || rewrittenCount > 0) {
		details := countDetails(skipped, "%s=%d")
		if unknown > 0 {
			details = append(details, fmt.Sprintf("未识别=%d", unknown))
		}
		suffix := ""
		if len(details) > 0 {
			suffix = "；跳过 " + strings.Join(details, "，")
		}
		rewriteText := ""
		if rewrittenCount > 0 {
			var parts []string
			for _, code := range sortedKeys(rewrittenFrom) {
				parts = append(parts, fmt.Sprintf("%s->%s=%d", code, r.region, rewrittenFrom[code]))
			}
			rewriteText = "；自动切换 " + strings.Join(parts, "，")
		}
		r.log(fmt.Sprintf("撞链代理地区 %s: %s 可用 %d/%d%s%s",
			r.label(), roleLabel, len(matched), len(proxies), rewriteText, suffix))
	}
	return matched
}

// unlockApproveRegions mirrors _unlock_approve_proxy_regions (app.py:16786-16814).
// Approve is the last hop and deliberately does NOT stay locked to the payment
// region: if the pool holds exits in other countries those are tried first.
// Returns nil when there is nothing to unlock, which is the caller's signal to
// fall through to the region-filtered path.
func (r *router) unlockApproveRegions(proxies []string, sourceLabel string) []string {
	var normalized []string
	for _, proxy := range proxies {
		if pyStrip(proxy) == "" {
			continue
		}
		normalized = append(normalized, proxypool.NormalizeProxyURL(proxy))
	}
	if r.region == "" || len(normalized) == 0 {
		return nil
	}
	var nonTarget, unknown, target []string
	counts := map[string]int{}
	for _, proxy := range normalized {
		proxyRegion := RegionCodeFromText(proxy)
		switch {
		case proxyRegion != "" && proxyRegion != r.region:
			nonTarget = append(nonTarget, proxy)
			counts[proxyRegion]++
		case proxyRegion != "":
			target = append(target, proxy)
		default:
			unknown = append(unknown, proxy)
		}
	}
	if len(nonTarget) == 0 {
		return nil
	}
	details := strings.Join(countDetails(counts, "%s=%d"), "，")
	var extra []string
	if len(unknown) > 0 {
		extra = append(extra, fmt.Sprintf("未标地区=%d", len(unknown)))
	}
	if len(target) > 0 {
		extra = append(extra, fmt.Sprintf("%s=%d", r.region, len(target)))
	}
	suffix := ""
	if len(extra) > 0 {
		suffix = "；顺延 " + strings.Join(extra, "，")
	}
	r.log(fmt.Sprintf("撞链代理地区 %s: %s 自动放开地区，Approve 优先轮非 %s 代理 %s%s",
		r.label(), sourceLabel, r.region, details, suffix))
	out := make([]string, 0, len(nonTarget)+len(unknown)+len(target))
	out = append(out, nonTarget...)
	out = append(out, unknown...)
	out = append(out, target...)
	return out
}

// linkCreateProxies mirrors _read_link_create_dynamic_proxies (app.py:16904-16916):
// the create pool, and if it yields nothing for this region, the register pool.
func (r *router) linkCreateProxies() []string {
	proxies := r.createPool()
	if filtered := r.filterByRegion(proxies, "第一步代理池"); len(filtered) > 0 {
		return filtered
	}
	fallback := r.filterByRegion(r.registerPool(), "注册动态代理池")
	if len(fallback) > 0 {
		if len(proxies) > 0 {
			r.log("第一步代理池没有匹配地区代理，长链第一步改用注册动态代理池中匹配项")
		} else {
			r.log("第一步代理池为空，长链第一步自动复用注册动态代理池")
		}
	}
	return fallback
}

// linkFollowupProxies mirrors _read_link_followup_dynamic_proxies
// (app.py:16917-16927): the followup pool, else the create list it was handed.
func (r *router) linkFollowupProxies(createProxies []string) []string {
	raw := r.followupPool()
	if proxies := r.filterByRegion(raw, "后续代理池"); len(proxies) > 0 {
		return proxies
	}
	fallback := append([]string(nil), createProxies...)
	if len(fallback) > 0 {
		message := "后续代理池为空"
		if len(raw) > 0 {
			message = "后续代理池没有匹配地区代理"
		}
		r.log(message + "，长链后续自动复用第一步代理")
	}
	return fallback
}

// linkApproveProxies mirrors _read_link_approve_dynamic_proxies
// (app.py:16928-16950). Five ranked sources, in this order:
//
//  1. approve pool with regions unlocked
//  2. approve pool filtered to the region
//  3. followup pool with regions unlocked
//  4. create (payment) pool with regions unlocked
//  5. the followup list handed in
func (r *router) linkApproveProxies(followupProxies []string) []string {
	raw := r.approvePool()
	if unlocked := r.unlockApproveRegions(raw, "Approve 代理池"); len(unlocked) > 0 {
		return unlocked
	}
	if proxies := r.filterByRegion(raw, "Approve 代理池"); len(proxies) > 0 {
		return proxies
	}
	rawFollowup := r.followupPool()
	if unlocked := r.unlockApproveRegions(rawFollowup, "后续代理池"); len(unlocked) > 0 {
		message := "Approve 代理池为空"
		if len(raw) > 0 {
			message = "Approve 代理池没有匹配地区代理"
		}
		r.log(message + "，长链 Approve 自动复用后续代理池的原始地区")
		return unlocked
	}
	rawCreate := r.createPool()
	if unlocked := r.unlockApproveRegions(rawCreate, "第一步代理池"); len(unlocked) > 0 {
		message := "Approve 代理池为空"
		if len(raw) > 0 || len(rawFollowup) > 0 {
			message = "Approve/后续代理池都没有匹配地区代理"
		}
		r.log(message + "，长链 Approve 自动复用第一步代理池的原始地区")
		return unlocked
	}
	fallback := append([]string(nil), followupProxies...)
	if len(fallback) > 0 {
		message := "Approve 代理池为空"
		if len(raw) > 0 {
			message = "Approve 代理池没有匹配地区代理"
		}
		r.log(message + "，长链 Approve 自动复用后续代理")
	}
	return fallback
}

// reuseProxy mirrors _reuse_link_proxy_for_region (app.py:16852-16870).
//
// roleLabel is 第一步 / 后续 / Approve; the Approve branch is special-cased by
// prefix exactly as Python does, because the last hop keeps its own region
// instead of being rewritten or dropped.
func (r *router) reuseProxy(proxy, roleLabel string) string {
	if r.localOnly {
		return ""
	}
	proxy = proxypool.NormalizeProxyURL(proxy)
	if proxy == "" || r.region == "" {
		return proxy
	}
	proxyRegion := RegionCodeFromText(proxy)
	if strings.HasPrefix(pyLower(pyStrip(roleLabel)), "approve") && proxyRegion != "" && proxyRegion != r.region {
		r.log(fmt.Sprintf("撞链代理地区 %s: %s复用代理保留原地区 %s，最后一步自动放开地区锁定",
			r.label(), roleLabel, proxyRegion))
		return proxy
	}
	if proxyRegion != "" && proxyRegion != r.region {
		rewritten := RewriteProxyRegionCode(proxy, r.region)
		if RegionCodeFromText(rewritten) == r.region && rewritten != proxy {
			r.log(fmt.Sprintf("撞链代理地区 %s: %s复用代理已自动从 %s 切换到 %s",
				r.label(), roleLabel, proxyRegion, r.region))
			return rewritten
		}
		r.log(fmt.Sprintf("撞链代理地区 %s: %s复用代理是 %s，无法自动切换，已忽略并改用对应地区代理池",
			r.label(), roleLabel, proxyRegion))
		return ""
	}
	return proxy
}

// ---------------------------------------------------------------------------
// triples
// ---------------------------------------------------------------------------

// Triple mirrors _link_proxy_pair + _link_proxy_triple (app.py:16953-16962).
// This is the dynamic-proxy level half of the cascade: an empty followup takes
// the create proxy, an empty approve takes the (already resolved) followup one.
// Python's `or` fires on "" — normalize_proxy_url returns "" for blank input —
// so the order create -> followup -> approve is load-bearing.
func Triple(create, followup, approve string) (string, string, string) {
	create = proxypool.NormalizeProxyURL(create)
	followup = proxypool.NormalizeProxyURL(followup)
	if followup == "" {
		followup = create
	}
	approve = proxypool.NormalizeProxyURL(approve)
	if approve == "" {
		approve = followup
	}
	return create, followup, approve
}

// Triples mirrors _link_proxy_triples (app.py:16990-17026): zip the three pools
// into per-job (create, followup, approve) assignments.
//
//   - a single entry in a pool is reused for all `count` jobs;
//   - a shorter pool falls back to the previous stage's pick for that index;
//   - an empty create pool with a non-empty followup/approve pool returns no
//     triples at all, which callers read as 代理池已耗尽.
func Triples(createProxies, followupProxies, approveProxies []string, count int) [][3]string {
	create := normalizeNonEmpty(createProxies)
	followup := normalizeNonEmpty(followupProxies)
	approve := normalizeNonEmpty(approveProxies)

	if len(create) == 0 {
		if len(followup) > 0 || len(approve) > 0 {
			return nil
		}
		out := make([][3]string, 0, maxInt(0, count))
		for i := 0; i < count; i++ {
			out = append(out, [3]string{"", "", ""})
		}
		return out
	}

	roleCapacity := func(proxies []string, fallbackCapacity int) int {
		if len(proxies) == 0 {
			return fallbackCapacity
		}
		if count != 0 && len(proxies) == 1 {
			return count
		}
		return len(proxies)
	}
	pickProxy := func(proxies []string, index int, fallback string) string {
		if len(proxies) == 0 {
			return fallback
		}
		if index < len(proxies) {
			return proxies[index]
		}
		if count != 0 && len(proxies) == 1 {
			return proxies[0]
		}
		return fallback
	}

	createCapacity := len(create)
	if count != 0 && len(create) == 1 {
		createCapacity = count
	}
	followupCapacity := roleCapacity(followup, createCapacity)
	approveCapacity := roleCapacity(approve, followupCapacity)
	total := minInt(createCapacity, minInt(followupCapacity, approveCapacity))
	if count == 0 {
		total = maxInt(0, total)
	}

	out := make([][3]string, 0, maxInt(0, total))
	for index := 0; index < total; index++ {
		createProxy := pickProxy(create, index, create[0])
		followupProxy := pickProxy(followup, index, createProxy)
		approveProxy := pickProxy(approve, index, followupProxy)
		c, f, a := Triple(createProxy, followupProxy, approveProxy)
		out = append(out, [3]string{c, f, a})
	}
	return out
}

func normalizeNonEmpty(proxies []string) []string {
	var out []string
	for _, proxy := range proxies {
		if pyStrip(proxy) == "" {
			continue
		}
		out = append(out, proxypool.NormalizeProxyURL(proxy))
	}
	return out
}

func countDetails(counts map[string]int, format string) []string {
	var out []string
	for _, code := range sortedKeys(counts) {
		out = append(out, fmt.Sprintf(format, code, counts[code]))
	}
	return out
}

// sortedKeys keeps log output deterministic where Python iterates a sorted
// dict; Go map order is random and must never leak into output.
func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
