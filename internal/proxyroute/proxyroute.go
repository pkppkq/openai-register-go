// Package proxyroute picks the exits for the three stages of the payment-link
// pipeline — 创建长链第一步 (create), 后续 (followup) and Approve — and starts one
// local chain listener per distinct exit.
//
// The register path (internal/ui/config.go) needs a single chain. This is the
// three-stage case: each stage has its own manual pool, its own 复用代理 override
// and its own provider role, and each one falls back to the stage before it.
// Getting a stage wrong means the payment call leaves through the wrong exit,
// so every settings key below is cited to the app.py line that reads it.
//
// The selection half (Plan) is pure. The only I/O anywhere in the package is
// Open binding chain listeners on 127.0.0.1:0; nothing here connects to an
// upstream, and nothing here talks to OpenAI, Stripe or PayPal.
//
// Order of operations, mirroring refetch_selected_link (app.py:15219-15236),
// which is the single-account form of the same block used by the session and
// batch paths (app.py:15265-15272, 22764-22770, 23233-23238):
//
//	local_proxy                       normalize_proxy_url(settings.local_proxy)
//	reuse_payment_proxy   -> create   _reuse_link_proxy_for_region(..., "第一步")
//	reuse_followup_proxy  -> followup _reuse_link_proxy_for_region(..., "后续")
//	reuse_approve_proxy   -> approve  _reuse_link_proxy_for_region(..., "Approve")
//	create candidates    = [reuse] or _read_link_create_dynamic_proxies()
//	followup candidates  = [reuse] or _read_link_followup_dynamic_proxies(create)
//	approve candidates   = [reuse] or _read_link_approve_dynamic_proxies(followup)
//	triple               = _link_proxy_triples(...)[0]
package proxyroute

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxychain"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// LogFunc receives human-facing status lines (may be nil).
type LogFunc func(string)

// ErrProxyPoolExhausted is app.py:15230-15236 — a followup/approve pool with no
// create pool behind it produces no usable triple, and the run is stopped
// rather than silently falling back to a direct connection.
var ErrProxyPoolExhausted = errors.New("支付代理池已耗尽")

// Stage labels, matching PROVIDER_PROXY_ROLE_LABELS (app.py:294) and the
// role_label strings _reuse_link_proxy_for_region is called with
// (app.py:15222-15224).
const (
	StageLabelCreate   = "第一步"
	StageLabelFollowup = "后续"
	StageLabelApprove  = "Approve"
)

// Selection is the outcome of the pure selection pass: which dynamic proxy each
// stage will chain through, before any listener exists.
type Selection struct {
	// LocalProxy is normalize_proxy_url(settings.local_proxy) (app.py:15219).
	LocalProxy string
	// Region is the resolved 撞链代理地区 code ("" = no region lock).
	Region string
	// RegionLabel is the "JP 日本" form used in log lines ("" = 不限).
	RegionLabel string
	// LocalOnly reports 代理模式=全走本地代理 (app.py:16712), which empties every
	// pool and every reuse override.
	LocalOnly bool

	// The three dynamic proxies after the create -> followup -> approve
	// cascade. Empty means "no dynamic hop for this stage".
	CreateProxy   string
	FollowupProxy string
	ApproveProxy  string

	// The 复用代理 actually in force per stage after region handling
	// (_reuse_link_proxy_for_region); "" when the stage came from a pool.
	ReuseCreate   string
	ReuseFollowup string
	ReuseApprove  string

	// The pre-triple candidate lists, kept because the provider-fallback
	// decision is made from them (app.py:23249).
	CreateCandidates   []string
	FollowupCandidates []string
	ApproveCandidates  []string

	// ProviderRolesNeeded are the stages that have no manual/reuse proxy and
	// would draw from an enabled provider pool (app.py:16879-16890). Prewarming
	// that pool needs live probes and is not part of this package.
	ProviderRolesNeeded []proxypool.Role
}

// Plan runs the selection over the settings sub-map of state.json — the same
// map internal/ui hands around ( snapshot["settings"] ). No listeners are bound.
func Plan(s map[string]any, log LogFunc) (Selection, error) {
	return PlanSettings(settingsFromMap(s), log)
}

// PlanSettings is Plan for callers that already hold typed settings.
func PlanSettings(cfg settings.Settings, log LogFunc) (Selection, error) {
	r := newRouter(cfg, log)

	sel := Selection{
		// app.py:15219 — the local hop is always normalized before use.
		LocalProxy:  proxypool.NormalizeProxyURL(cfg.LocalProxy),
		Region:      r.region,
		RegionLabel: r.label(),
		LocalOnly:   r.localOnly,
	}

	// reuse_payment_proxy / reuse_followup_proxy / reuse_approve_proxy.
	// settings.FromSnapshot already applied the seeding rule of
	// app.py:14105-14110: reuse_followup_proxy is copied from
	// reuse_payment_proxy only when the KEY IS ABSENT. A present-but-empty
	// reuse_followup_proxy stays empty and the followup stage falls through to
	// its pool instead of inheriting the create reuse proxy.
	sel.ReuseCreate = r.reuseProxy(cfg.ReusePaymentProxy, StageLabelCreate)
	sel.ReuseFollowup = r.reuseProxy(cfg.ReuseFollowupProxy, StageLabelFollowup)
	sel.ReuseApprove = r.reuseProxy(cfg.ReuseApproveProxy, StageLabelApprove)

	// app.py:15225-15227: a reuse proxy replaces the whole pool for its stage,
	// and the create list is what the followup fallback reads, which is why the
	// three lines must run in this order.
	if sel.ReuseCreate != "" {
		sel.CreateCandidates = []string{sel.ReuseCreate}
	} else {
		sel.CreateCandidates = r.linkCreateProxies()
	}
	if sel.ReuseFollowup != "" {
		sel.FollowupCandidates = []string{sel.ReuseFollowup}
	} else {
		sel.FollowupCandidates = r.linkFollowupProxies(sel.CreateCandidates)
	}
	if sel.ReuseApprove != "" {
		sel.ApproveCandidates = []string{sel.ReuseApprove}
	} else {
		sel.ApproveCandidates = r.linkApproveProxies(sel.FollowupCandidates)
	}

	fixed := map[proxypool.Role]string{
		proxypool.RoleCreate:   sel.ReuseCreate,
		proxypool.RoleFollowup: sel.ReuseFollowup,
		proxypool.RoleApprove:  sel.ReuseApprove,
	}
	sel.ProviderRolesNeeded = ProviderRolesNeeded(cfg, sel.CreateCandidates, sel.FollowupCandidates, sel.ApproveCandidates, fixed)

	// app.py:15228-15236.
	wanted := len(sel.CreateCandidates) > 0 || len(sel.FollowupCandidates) > 0 || len(sel.ApproveCandidates) > 0
	count := 0
	if wanted {
		count = 1
	}
	triples := Triples(sel.CreateCandidates, sel.FollowupCandidates, sel.ApproveCandidates, count)
	if wanted && len(triples) == 0 {
		return sel, ErrProxyPoolExhausted
	}
	if len(triples) > 0 {
		sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy = triples[0][0], triples[0][1], triples[0][2]
	}

	r.logPlan(sel)
	return sel, nil
}

// logPlan is _log_link_proxy_plan (app.py:16891-16899) plus the reuse notices
// of app.py:23251-23258.
//
// DIVERGENCE: Python prefixes each line with the button that triggered the run
// ("批量提取长链代理计划" …). This package has no such context, so it uses one
// neutral label.
func (r *router) logPlan(sel Selection) {
	providerText := "无"
	if len(sel.ProviderRolesNeeded) > 0 {
		var labels []string
		for _, role := range sel.ProviderRolesNeeded {
			labels = append(labels, settings.ProviderProxyRoleLabels[string(role)])
		}
		providerText = strings.Join(labels, "、")
	}
	regionLabelText := sel.RegionLabel
	if regionLabelText == "" {
		regionLabelText = "不限"
	}
	r.log(fmt.Sprintf("长链代理计划: 地区=%s，手工代理 第一段=%d 后续=%d Approve=%d，提供商兜底=%s",
		regionLabelText, len(sel.CreateCandidates), len(sel.FollowupCandidates), len(sel.ApproveCandidates), providerText))
	if sel.ReuseCreate != "" {
		r.log("长链第一步优先使用复用代理: " + maskProxyURL(sel.ReuseCreate))
	}
	if sel.ReuseFollowup != "" {
		r.log("长链后续优先使用复用代理: " + maskProxyURL(sel.ReuseFollowup))
	}
	if sel.ReuseApprove != "" {
		r.log("长链 Approve 优先使用复用代理: " + maskProxyURL(sel.ReuseApprove))
	} else if len(sel.CreateCandidates) == 0 && len(sel.FollowupCandidates) == 0 {
		// app.py:23257-23258.
		r.log("创建长链代理池为空，提取长链改用当前本地代理")
	}
}

// ---------------------------------------------------------------------------
// live chains
// ---------------------------------------------------------------------------

// Routes holds the three per-stage ProxyConfigs and owns the chain listeners
// behind them. A Routes value must not outlive the run it was built for: call
// Close when the job ends, or a later job can still reach a listener pointed at
// an exit it did not choose.
type Routes struct {
	Selection Selection

	Create   models.ProxyConfig
	Followup models.ProxyConfig
	Approve  models.ProxyConfig

	mu      sync.Mutex
	closed  bool
	servers []*proxychain.Server
}

// Open runs Plan and starts the chains. The settings map is the "settings"
// object of state.json.
func Open(s map[string]any, log LogFunc) (*Routes, error) {
	return OpenSettings(settingsFromMap(s), log)
}

// OpenSettings is Open for callers that already hold typed settings.
//
// Chain listeners are deduplicated by normalized dynamic proxy, exactly as
// link_chain_for does (app.py:17876-17880): three stages sharing one exit share
// one listener, and the three ProxyConfigs then carry the same chain_url.
//
// On any error every listener started so far is closed before returning.
func OpenSettings(cfg settings.Settings, log LogFunc) (*Routes, error) {
	sel, err := PlanSettings(cfg, log)
	if err != nil {
		return nil, err
	}

	routes := &Routes{Selection: sel}
	chains := map[string]*proxychain.Server{}

	chainURLFor := func(dynamicProxy string) (string, error) {
		key := proxypool.NormalizeProxyURL(dynamicProxy)
		if server, ok := chains[key]; ok {
			return server.URL(), nil
		}
		server := proxychain.New(sel.LocalProxy, key, proxychain.LogFunc(log))
		chains[key] = server
		// Track before Start: a listener that bound and then failed later must
		// still be reachable by Close.
		routes.servers = append(routes.servers, server)
		if err := server.Start(); err != nil {
			return "", fmt.Errorf("启动链式代理失败: %w", err)
		}
		return server.URL(), nil
	}

	// Fixed order, never a map range: the chain each stage gets must be
	// reproducible.
	stages := []struct {
		dynamic string
		out     *models.ProxyConfig
	}{
		{sel.CreateProxy, &routes.Create},
		{sel.FollowupProxy, &routes.Followup},
		{sel.ApproveProxy, &routes.Approve},
	}
	for _, stage := range stages {
		url, err := chainURLFor(stage.dynamic)
		if err != nil {
			routes.Close()
			return nil, err
		}
		// app.py:17884-17888: dynamic_proxy is the stage's own value, chain_url
		// the (possibly shared) listener, local_proxy the same for all three.
		*stage.out = models.ProxyConfig{
			LocalProxy:   sel.LocalProxy,
			DynamicProxy: stage.dynamic,
			ChainURL:     url,
		}
	}

	if routes.Create.ChainURL == "" && log != nil {
		// Both hops empty: ProxyChainServer.__enter__ returns without binding
		// (app.py:5937), so the payment calls go out from this machine.
		log("长链三段代理: 直连（settings.local_proxy 与创建长链代理池均为空）")
	}
	return routes, nil
}

// Close shuts down every chain listener. Safe to call twice, safe on a
// partially-constructed Routes, and safe on a nil receiver.
func (r *Routes) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	servers := r.servers
	r.servers = nil
	r.mu.Unlock()
	for _, server := range servers {
		if server != nil {
			server.Close()
		}
	}
}

// Stage returns one stage's ProxyConfig by role.
func (r *Routes) Stage(role proxypool.Role) models.ProxyConfig {
	switch role {
	case proxypool.RoleFollowup:
		return r.Followup
	case proxypool.RoleApprove:
		return r.Approve
	default:
		return r.Create
	}
}

// ---------------------------------------------------------------------------
// the runtime cascade
// ---------------------------------------------------------------------------

// RequestURLs is the proxy URL each stage's HTTP call actually uses
// (app.py:11970-11972, repeated verbatim at 12024-12026):
//
//	create   = chain_url or local_proxy or dynamic_proxy
//	followup = chain_url or local_proxy or dynamic_proxy or create
//	approve  = chain_url or local_proxy or dynamic_proxy or followup
//
// Python `or` fires on "", and the cross-stage tail is what makes a stage with
// nothing configured leave through the previous stage's exit instead of going
// direct. The order create -> followup -> approve is load-bearing: approve
// falls back to the ALREADY-RESOLVED followup URL, not to followup's raw fields.
func RequestURLs(create, followup, approve models.ProxyConfig) (string, string, string) {
	createURL := firstNonEmpty(create.ChainURL, create.LocalProxy, create.DynamicProxy)
	followupURL := firstNonEmpty(followup.ChainURL, followup.LocalProxy, followup.DynamicProxy, createURL)
	approveURL := firstNonEmpty(approve.ChainURL, approve.LocalProxy, approve.DynamicProxy, followupURL)
	return createURL, followupURL, approveURL
}

// UsedProxies is the exit each stage is reported as having used
// (app.py:11973-11975 / 12027-12029). Note this cascade skips chain_url and
// prefers dynamic over local — it names the real exit, not the local hop the
// request was handed to.
func UsedProxies(create, followup, approve models.ProxyConfig) (string, string, string) {
	createUsed := firstNonEmpty(create.DynamicProxy, create.LocalProxy)
	followupUsed := firstNonEmpty(followup.DynamicProxy, followup.LocalProxy, createUsed)
	approveUsed := firstNonEmpty(approve.DynamicProxy, approve.LocalProxy, followupUsed)
	return createUsed, followupUsed, approveUsed
}

// RequestURLs on the live routes.
func (r *Routes) RequestURLs() (string, string, string) {
	return RequestURLs(r.Create, r.Followup, r.Approve)
}

// UsedProxies on the live routes.
func (r *Routes) UsedProxies() (string, string, string) {
	return UsedProxies(r.Create, r.Followup, r.Approve)
}

// firstNonEmpty is Python's `a or b or c` over strings.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// settingsFromMap decodes the state.json "settings" object. It goes through
// settings.FromSnapshot rather than reading keys by hand so that every
// load-time quirk of GUI.load_state applies — in particular the
// reuse_followup_proxy seeding (app.py:14105-14110), the proxy_route_mode and
// link_proxy_region validation against their option lists (app.py:14087-14089,
// 14113-14115) and the provider duration clamp.
func settingsFromMap(s map[string]any) settings.Settings {
	if s == nil {
		s = map[string]any{}
	}
	return settings.FromSnapshot(map[string]any{settings.SettingsKey: s})
}
