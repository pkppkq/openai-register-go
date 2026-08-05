package proxyroute

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// MONEY SAFETY: every test here either stays purely in memory or binds a
// listener on 127.0.0.1:0. Nothing connects to an upstream proxy, and no test
// reaches OpenAI, Stripe or PayPal.

// Real pool entries in the shape state.json stores them
// (host:port:user:pass, app.py's 711proxy format).
const (
	poolUS    = "p.example.com:10000:USER-zone-custom-region-US-session-1:pw"
	poolUS2   = "p.example.com:10000:USER-zone-custom-region-US-session-2:pw"
	poolJP    = "p.example.com:10000:USER-zone-custom-region-JP-session-3:pw"
	poolBR    = "p.example.com:10000:USER-zone-custom-region-BR-session-4:pw"
	poolPlain = "p2.example.com:20000:USER-session-9:pw"
)

func norm(s string) string { return proxypool.NormalizeProxyURL(s) }

func discard(string) {}

func planOf(t *testing.T, s map[string]any) Selection {
	t.Helper()
	sel, err := Plan(s, discard)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return sel
}

// ---------------------------------------------------------------------------
// the create -> followup -> approve cascade
// ---------------------------------------------------------------------------

func TestCascadeCreateOnlyFeedsAllThreeStages(t *testing.T) {
	// Only the 第一步 pool is filled: app.py:16921-16926 gives followup the
	// create list, and app.py:16945-16950 gives approve the followup list.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy": poolUS,
		"link_proxy_region":     "不限",
	})
	want := norm(poolUS)
	if sel.CreateProxy != want || sel.FollowupProxy != want || sel.ApproveProxy != want {
		t.Fatalf("create=%q followup=%q approve=%q, all should be %q",
			sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy, want)
	}
}

func TestCascadeFollowupPoolStopsAtApprove(t *testing.T) {
	// followup has its own pool; approve still inherits followup, not create.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolJP,
		"link_proxy_region":      "不限",
	})
	if sel.CreateProxy != norm(poolUS) {
		t.Errorf("create = %q", sel.CreateProxy)
	}
	if sel.FollowupProxy != norm(poolJP) {
		t.Errorf("followup = %q", sel.FollowupProxy)
	}
	if sel.ApproveProxy != norm(poolJP) {
		t.Errorf("approve should inherit followup, got %q", sel.ApproveProxy)
	}
}

func TestCascadeApprovePoolWins(t *testing.T) {
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolJP,
		"approve_dynamic_proxy":  poolBR,
		"link_proxy_region":      "不限",
	})
	if sel.CreateProxy != norm(poolUS) || sel.FollowupProxy != norm(poolJP) || sel.ApproveProxy != norm(poolBR) {
		t.Fatalf("create=%q followup=%q approve=%q", sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy)
	}
}

func TestCreatePoolEmptyFallsBackToRegisterPool(t *testing.T) {
	// app.py:16904-16916.
	sel := planOf(t, map[string]any{
		"dynamic_proxies":   poolUS,
		"link_proxy_region": "不限",
	})
	if sel.CreateProxy != norm(poolUS) {
		t.Fatalf("create = %q, want the register pool entry %q", sel.CreateProxy, norm(poolUS))
	}
}

func TestNoSettingsAtAllStillUsesTheDefaultLocalProxy(t *testing.T) {
	// An absent local_proxy key is NOT a direct connection: the Tk StringVar
	// defaults to http://127.0.0.1:7890 (app.py:12340), so the chain still binds
	// and every stage exits through the local proxy.
	sel := planOf(t, map[string]any{})
	if sel.LocalProxy != "http://127.0.0.1:7890" {
		t.Fatalf("local proxy = %q, want the app.py:12340 default", sel.LocalProxy)
	}
	if sel.CreateProxy != "" || sel.FollowupProxy != "" || sel.ApproveProxy != "" {
		t.Fatalf("expected no dynamic hop, got %q/%q/%q", sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy)
	}
	routes, err := Open(map[string]any{}, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer routes.Close()
	if routes.Create.ChainURL == "" {
		t.Fatal("expected a bound chain for the default local proxy")
	}
	c, f, a := routes.RequestURLs()
	if c == "" || c != f || f != a {
		t.Fatalf("request URLs = %q/%q/%q, want one shared chain URL", c, f, a)
	}
}

func TestEmptyEverythingIsDirect(t *testing.T) {
	// Truly empty means local_proxy present and blank.
	empty := map[string]any{"local_proxy": ""}
	sel := planOf(t, empty)
	if sel.LocalProxy != "" {
		t.Fatalf("local proxy = %q", sel.LocalProxy)
	}
	if sel.CreateProxy != "" || sel.FollowupProxy != "" || sel.ApproveProxy != "" {
		t.Fatalf("expected no dynamic hop, got %q/%q/%q", sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy)
	}
	routes, err := Open(empty, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer routes.Close()
	for name, cfg := range map[string]models.ProxyConfig{
		"create": routes.Create, "followup": routes.Followup, "approve": routes.Approve,
	} {
		if cfg != (models.ProxyConfig{}) {
			t.Errorf("%s = %+v, want the zero config (direct)", name, cfg)
		}
	}
	c, f, a := routes.RequestURLs()
	if c != "" || f != "" || a != "" {
		t.Errorf("request URLs = %q/%q/%q, want all empty", c, f, a)
	}
}

func TestApprovePoolWithoutCreatePoolIsExhausted(t *testing.T) {
	// app.py:16994-16997 returns no triples at all, and app.py:15230-15236
	// stops the run rather than silently going direct.
	_, err := Plan(map[string]any{
		"approve_dynamic_proxy": poolBR,
		"link_proxy_region":     "不限",
	}, discard)
	if !errors.Is(err, ErrProxyPoolExhausted) {
		t.Fatalf("err = %v, want ErrProxyPoolExhausted", err)
	}
	if _, err := Open(map[string]any{
		"approve_dynamic_proxy": poolBR,
		"link_proxy_region":     "不限",
	}, discard); !errors.Is(err, ErrProxyPoolExhausted) {
		t.Fatalf("Open err = %v, want ErrProxyPoolExhausted", err)
	}
}

// ---------------------------------------------------------------------------
// route-mode gate
// ---------------------------------------------------------------------------

func TestLocalOnlyRouteModeEmptiesEveryPoolAndEveryReuse(t *testing.T) {
	// app.py:16712-16742 and 16853-16854.
	s := map[string]any{
		"proxy_route_mode":       "全走本地代理",
		"local_proxy":            "http://127.0.0.1:7897",
		"dynamic_proxies":        poolUS,
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolJP,
		"approve_dynamic_proxy":  poolBR,
		"reuse_payment_proxy":    norm(poolUS),
		"reuse_followup_proxy":   norm(poolJP),
		"reuse_approve_proxy":    norm(poolBR),
		"link_proxy_region":      "不限",
	}
	sel := planOf(t, s)
	if !sel.LocalOnly {
		t.Fatal("LocalOnly not detected")
	}
	if sel.CreateProxy != "" || sel.FollowupProxy != "" || sel.ApproveProxy != "" {
		t.Fatalf("dynamic hops survived 全走本地代理: %q/%q/%q", sel.CreateProxy, sel.FollowupProxy, sel.ApproveProxy)
	}
	if sel.ReuseCreate != "" || sel.ReuseFollowup != "" || sel.ReuseApprove != "" {
		t.Fatalf("reuse proxies survived 全走本地代理: %q/%q/%q", sel.ReuseCreate, sel.ReuseFollowup, sel.ReuseApprove)
	}
	if sel.LocalProxy != "http://127.0.0.1:7897" {
		t.Fatalf("local proxy = %q", sel.LocalProxy)
	}

	routes, err := Open(s, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer routes.Close()
	// All three stages have the same (empty) dynamic proxy, so link_chain_for
	// hands them one shared listener (app.py:17876-17880).
	if routes.Create.ChainURL == "" {
		t.Fatal("local proxy set but no chain listener bound")
	}
	if routes.Create.ChainURL != routes.Followup.ChainURL || routes.Followup.ChainURL != routes.Approve.ChainURL {
		t.Fatalf("expected one shared chain, got %q/%q/%q",
			routes.Create.ChainURL, routes.Followup.ChainURL, routes.Approve.ChainURL)
	}
	if n := len(routes.servers); n != 1 {
		t.Fatalf("started %d listeners, want 1", n)
	}
}

func TestLocalOnlyDisablesProviderFallback(t *testing.T) {
	// app.py:16880-16881.
	s := map[string]any{
		"proxy_route_mode": "全走本地代理",
		"provider_proxy_configs": map[string]any{
			"create":   map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
			"followup": map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
			"approve":  map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
		},
	}
	if roles := planOf(t, s).ProviderRolesNeeded; len(roles) != 0 {
		t.Fatalf("provider roles = %v, want none under 全走本地代理", roles)
	}
}

func TestProviderRolesNeededOnlyForStagesWithoutProxies(t *testing.T) {
	// app.py:16883-16890: an enabled role with a manual pool or a reuse proxy
	// does not need the provider pool.
	s := map[string]any{
		"payment_dynamic_proxy": poolUS,
		"reuse_approve_proxy":   norm(poolBR),
		"link_proxy_region":     "不限",
		"provider_proxy_configs": map[string]any{
			"create":   map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
			"followup": map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
			"approve":  map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP"},
		},
	}
	// create has a pool; followup inherits the create list (also non-empty);
	// approve is fixed by its reuse proxy. Nothing is left for the provider.
	if roles := planOf(t, s).ProviderRolesNeeded; len(roles) != 0 {
		t.Fatalf("provider roles = %v, want none", roles)
	}

	// With no manual pools at all, every enabled role needs the provider pool,
	// reported in PROVIDER_PROXY_ROLES order.
	delete(s, "payment_dynamic_proxy")
	delete(s, "reuse_approve_proxy")
	roles := planOf(t, s).ProviderRolesNeeded
	want := []proxypool.Role{proxypool.RoleCreate, proxypool.RoleFollowup, proxypool.RoleApprove}
	if len(roles) != len(want) {
		t.Fatalf("provider roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("provider roles = %v, want %v (order is fixed, never map order)", roles, want)
		}
	}
}

// ---------------------------------------------------------------------------
// reuse keys
// ---------------------------------------------------------------------------

func TestReuseFollowupSeededOnlyWhenKeyAbsent(t *testing.T) {
	// app.py:14105-14110: reuse_followup_proxy inherits reuse_payment_proxy when
	// the KEY IS MISSING. Present-but-empty must stay empty, or the followup
	// call silently leaves through the create exit.
	absent := planOf(t, map[string]any{
		"reuse_payment_proxy":   norm(poolUS),
		"payment_dynamic_proxy": poolJP,
		"link_proxy_region":     "不限",
	})
	if absent.ReuseFollowup != norm(poolUS) {
		t.Fatalf("absent key: followup reuse = %q, want it seeded from reuse_payment_proxy", absent.ReuseFollowup)
	}

	empty := planOf(t, map[string]any{
		"reuse_payment_proxy":    norm(poolUS),
		"reuse_followup_proxy":   "",
		"followup_dynamic_proxy": poolJP,
		"link_proxy_region":      "不限",
	})
	if empty.ReuseFollowup != "" {
		t.Fatalf("present-but-empty key: followup reuse = %q, want empty", empty.ReuseFollowup)
	}
	if empty.FollowupProxy != norm(poolJP) {
		t.Fatalf("followup = %q, want the followup pool entry %q", empty.FollowupProxy, norm(poolJP))
	}

	// reuse_approve_proxy has no such seeding rule (app.py:14111-14112).
	if absent.ReuseApprove != "" {
		t.Fatalf("approve reuse = %q, want empty — approve is never seeded", absent.ReuseApprove)
	}
}

func TestReuseProxyReplacesTheWholePoolForItsStage(t *testing.T) {
	// app.py:15225-15227.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolUS2,
		"reuse_approve_proxy":    norm(poolBR),
		"link_proxy_region":      "不限",
	})
	if sel.ApproveProxy != norm(poolBR) {
		t.Fatalf("approve = %q, want the reuse proxy %q", sel.ApproveProxy, norm(poolBR))
	}
	if sel.CreateProxy != norm(poolUS) || sel.FollowupProxy != norm(poolUS2) {
		t.Fatalf("reuse leaked into other stages: create=%q followup=%q", sel.CreateProxy, sel.FollowupProxy)
	}
}

// ---------------------------------------------------------------------------
// region lock
// ---------------------------------------------------------------------------

func TestRegionLockFiltersAndRewritesPools(t *testing.T) {
	// link_proxy_region "JP 日本": the US entry is rewritten to JP
	// (app.py:16824-16829), the untagged one is dropped as 未识别.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy": poolUS + "\n" + poolPlain,
		"link_proxy_region":     "JP 日本",
	})
	if sel.Region != "JP" {
		t.Fatalf("region = %q", sel.Region)
	}
	if got := RegionCodeFromText(sel.CreateProxy); got != "JP" {
		t.Fatalf("create proxy %q resolves to region %q, want JP", sel.CreateProxy, got)
	}
	if len(sel.CreateCandidates) != 1 {
		t.Fatalf("candidates = %v, want only the rewritable entry", sel.CreateCandidates)
	}
}

func TestRegionAutoFollowsPaymentMode(t *testing.T) {
	// link_proxy_region_selection_to_code + _payment_mode_country
	// (app.py:2527-2534 / 16756-16761). This is the value state.json actually
	// holds today: 自动(跟随支付地区).
	sel := planOf(t, map[string]any{
		"payment_mode":      "无卡长链接 BR/BRL",
		"link_proxy_region": "自动(跟随支付地区)",
	})
	if sel.Region != "BR" {
		t.Fatalf("region = %q, want BR from the payment mode", sel.Region)
	}
}

func TestApproveReuseKeepsItsOwnRegion(t *testing.T) {
	// app.py:16858-16862: the Approve stage is the last hop and is NOT rewritten
	// to the target region — a BR reuse proxy stays BR under a JP lock.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy": poolJP,
		"reuse_approve_proxy":   norm(poolBR),
		"link_proxy_region":     "JP 日本",
	})
	if sel.ReuseApprove != norm(poolBR) {
		t.Fatalf("approve reuse = %q, want it left in BR", sel.ReuseApprove)
	}
	if sel.ApproveProxy != norm(poolBR) {
		t.Fatalf("approve = %q", sel.ApproveProxy)
	}
}

func TestCreateReuseIsRewrittenToRegion(t *testing.T) {
	// app.py:16863-16866: a non-Approve reuse proxy in the wrong region is
	// switched over when the URL carries a rewritable region marker.
	sel := planOf(t, map[string]any{
		"reuse_payment_proxy": norm(poolBR),
		"link_proxy_region":   "JP 日本",
	})
	if RegionCodeFromText(sel.ReuseCreate) != "JP" {
		t.Fatalf("create reuse = %q, want it rewritten to JP", sel.ReuseCreate)
	}
}

func TestApprovePoolUnlocksNonTargetRegionsFirst(t *testing.T) {
	// _unlock_approve_proxy_regions (app.py:16786-16814): with a JP lock, a
	// BR entry in the Approve pool is preferred over the JP one.
	sel := planOf(t, map[string]any{
		"payment_dynamic_proxy": poolJP,
		"approve_dynamic_proxy": poolJP + "\n" + poolBR,
		"link_proxy_region":     "JP 日本",
	})
	if got := RegionCodeFromText(sel.ApproveProxy); got != "BR" {
		t.Fatalf("approve = %q (region %q), want the non-target BR exit first", sel.ApproveProxy, got)
	}
}

// ---------------------------------------------------------------------------
// the runtime cascade (app.py:11970-11975)
// ---------------------------------------------------------------------------

func TestRequestURLsCascade(t *testing.T) {
	cases := []struct {
		name                      string
		create, followup, approve models.ProxyConfig
		wantC, wantF, wantA       string
	}{
		{
			name:   "chain wins over everything",
			create: models.ProxyConfig{LocalProxy: "http://l", DynamicProxy: "http://d", ChainURL: "http://c"},
			wantC:  "http://c", wantF: "http://c", wantA: "http://c",
		},
		{
			name:   "local beats dynamic when there is no chain",
			create: models.ProxyConfig{LocalProxy: "http://l", DynamicProxy: "http://d"},
			wantC:  "http://l", wantF: "http://l", wantA: "http://l",
		},
		{
			name:   "dynamic is the last own-field option",
			create: models.ProxyConfig{DynamicProxy: "http://d"},
			wantC:  "http://d", wantF: "http://d", wantA: "http://d",
		},
		{
			name:     "followup falls back to create, approve to followup",
			create:   models.ProxyConfig{ChainURL: "http://c"},
			followup: models.ProxyConfig{},
			approve:  models.ProxyConfig{},
			wantC:    "http://c", wantF: "http://c", wantA: "http://c",
		},
		{
			name:     "approve follows the RESOLVED followup, not create",
			create:   models.ProxyConfig{ChainURL: "http://c"},
			followup: models.ProxyConfig{ChainURL: "http://f"},
			approve:  models.ProxyConfig{},
			wantC:    "http://c", wantF: "http://f", wantA: "http://f",
		},
		{
			name:  "all empty stays empty (direct)",
			wantC: "", wantF: "", wantA: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, f, a := RequestURLs(tc.create, tc.followup, tc.approve)
			if c != tc.wantC || f != tc.wantF || a != tc.wantA {
				t.Fatalf("got %q/%q/%q, want %q/%q/%q", c, f, a, tc.wantC, tc.wantF, tc.wantA)
			}
		})
	}
}

func TestUsedProxiesCascadePrefersDynamicOverLocal(t *testing.T) {
	// app.py:11973-11975: this cascade skips chain_url entirely and reports the
	// real exit, dynamic first.
	create := models.ProxyConfig{LocalProxy: "http://l", DynamicProxy: "http://d", ChainURL: "http://c"}
	followup := models.ProxyConfig{LocalProxy: "http://l", ChainURL: "http://c2"}
	approve := models.ProxyConfig{ChainURL: "http://c3"}
	c, f, a := UsedProxies(create, followup, approve)
	if c != "http://d" {
		t.Errorf("create used = %q, want the dynamic proxy", c)
	}
	if f != "http://l" {
		t.Errorf("followup used = %q, want its own local proxy", f)
	}
	if a != "http://l" {
		t.Errorf("approve used = %q, want the followup's resolved value", a)
	}
}

// ---------------------------------------------------------------------------
// listener lifecycle
// ---------------------------------------------------------------------------

func TestOpenSharesOneListenerPerDistinctExit(t *testing.T) {
	routes, err := Open(map[string]any{
		"local_proxy":            "http://127.0.0.1:7897",
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolUS, // same exit -> same chain
		"approve_dynamic_proxy":  poolBR,
		"link_proxy_region":      "不限",
	}, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer routes.Close()
	if routes.Create.ChainURL != routes.Followup.ChainURL {
		t.Errorf("identical exits should share a chain: %q vs %q", routes.Create.ChainURL, routes.Followup.ChainURL)
	}
	if routes.Approve.ChainURL == routes.Create.ChainURL {
		t.Errorf("a different exit must get its own chain, both are %q", routes.Approve.ChainURL)
	}
	if n := len(routes.servers); n != 2 {
		t.Fatalf("started %d listeners, want 2", n)
	}
	// Each stage keeps its OWN dynamic proxy even when the chain is shared.
	if routes.Create.DynamicProxy != norm(poolUS) || routes.Approve.DynamicProxy != norm(poolBR) {
		t.Fatalf("dynamic proxies = %q / %q", routes.Create.DynamicProxy, routes.Approve.DynamicProxy)
	}
}

func TestCloseReleasesEveryListener(t *testing.T) {
	routes, err := Open(map[string]any{
		"local_proxy":            "http://127.0.0.1:7897",
		"payment_dynamic_proxy":  poolUS,
		"followup_dynamic_proxy": poolJP,
		"approve_dynamic_proxy":  poolBR,
		"link_proxy_region":      "不限",
	}, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	urls := []string{routes.Create.ChainURL, routes.Followup.ChainURL, routes.Approve.ChainURL}
	seen := map[string]bool{}
	for _, u := range urls {
		if u == "" {
			t.Fatalf("chain URLs = %v, expected three bound listeners", urls)
		}
		seen[u] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected three distinct listeners, got %v", urls)
	}
	// Every listener is live before Close (loopback only — no upstream dial).
	for _, u := range urls {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(u, "http://"), 2*time.Second)
		if err != nil {
			t.Fatalf("listener %s not accepting before Close: %v", u, err)
		}
		_ = conn.Close()
	}

	routes.Close()
	routes.Close() // must be idempotent

	for _, u := range urls {
		addr := strings.TrimPrefix(u, "http://")
		if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			_ = conn.Close()
			t.Fatalf("listener %s still accepting after Close", u)
		}
	}
}

func TestCloseOnPartialAndNilIsSafe(t *testing.T) {
	var nilRoutes *Routes
	nilRoutes.Close() // must not panic

	partial := &Routes{}
	partial.Close()
	partial.Close()

	// A Routes that bound one listener and never got the other two.
	routes, err := Open(map[string]any{"local_proxy": "http://127.0.0.1:7897"}, discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	routes.servers = append(routes.servers, nil) // a slot that failed to build
	routes.Close()
	routes.Close()
}

// ---------------------------------------------------------------------------
// triples
// ---------------------------------------------------------------------------

func TestTripleFallbackOrder(t *testing.T) {
	c, f, a := Triple("http://c", "", "")
	if c != "http://c" || f != "http://c" || a != "http://c" {
		t.Fatalf("got %q/%q/%q", c, f, a)
	}
	c, f, a = Triple("http://c", "http://f", "")
	if a != "http://f" {
		t.Fatalf("approve = %q, want the followup proxy", a)
	}
	c, f, a = Triple("", "", "")
	if c != "" || f != "" || a != "" {
		t.Fatalf("got %q/%q/%q, want all empty", c, f, a)
	}
}

func TestTriplesShapes(t *testing.T) {
	// One entry per pool is reused for every job (app.py:16999-17001).
	got := Triples([]string{"http://c"}, nil, nil, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for _, triple := range got {
		if triple != [3]string{"http://c", "http://c", "http://c"} {
			t.Fatalf("triple = %v", triple)
		}
	}
	// No create pool but a followup pool: nothing usable (app.py:16994-16996).
	if got := Triples(nil, []string{"http://f"}, nil, 1); got != nil {
		t.Fatalf("got %v, want no triples", got)
	}
	// Nothing anywhere: `count` empty triples, i.e. direct.
	if got := Triples(nil, nil, nil, 2); len(got) != 2 || got[0] != [3]string{"", "", ""} {
		t.Fatalf("got %v", got)
	}
	// Shorter approve pool is capped by the shortest capacity.
	got = Triples([]string{"http://c1", "http://c2"}, []string{"http://f1", "http://f2"}, []string{"http://a1"}, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (single approve entry reused)", len(got))
	}
	if got[1] != [3]string{"http://c2", "http://f2", "http://a1"} {
		t.Fatalf("second triple = %v", got[1])
	}
}

// ---------------------------------------------------------------------------
// provider proxies
// ---------------------------------------------------------------------------

func TestBuildProviderProxyURL(t *testing.T) {
	cfg := providerConfig()
	got, err := BuildProviderProxyURL(cfg, "jp", "Ab12Cd34")
	if err != nil {
		t.Fatalf("BuildProviderProxyURL: %v", err)
	}
	want := "http://us%3Aer-region-JP-sid-Ab12Cd34-t-7:p%40ss@us2.proxy.invalid:3010"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if _, err := BuildProviderProxyURL(cfg, "US", "Ab12Cd34"); err == nil {
		t.Error("a region outside the configured list must be rejected")
	}
	if _, err := BuildProviderProxyURL(cfg, "JP", "short"); err == nil {
		t.Error("a malformed sid must be rejected")
	}
	auto, err := BuildProviderProxyURL(cfg, "JP", "")
	if err != nil {
		t.Fatalf("auto sid: %v", err)
	}
	if !strings.Contains(auto, "-sid-") || len(RandomProviderSID()) != 8 {
		t.Fatalf("auto sid url = %q", auto)
	}
}

func TestJapanExtractGateOnProviderCreateRegion(t *testing.T) {
	// app.py:12516-12519.
	s := map[string]any{
		"require_japan_extract_proxy": true,
		"provider_proxy_configs": map[string]any{
			"create": map[string]any{"enabled": true, "username": "u", "password": "p", "endpoint": "h.example:1", "duration": 5, "regions": "JP BR"},
		},
	}
	if err := CheckJapanExtractProvider(settingsFromMap(s)); err == nil {
		t.Fatal("非 JP region with 强制日本出口 must be rejected")
	}
	s["provider_proxy_configs"].(map[string]any)["create"].(map[string]any)["regions"] = "JP"
	if err := CheckJapanExtractProvider(settingsFromMap(s)); err != nil {
		t.Fatalf("JP-only should pass: %v", err)
	}
	// 强制日本出口 off: no constraint at all.
	s["require_japan_extract_proxy"] = false
	s["provider_proxy_configs"].(map[string]any)["create"].(map[string]any)["regions"] = "BR"
	if err := CheckJapanExtractProvider(settingsFromMap(s)); err != nil {
		t.Fatalf("gate should be off: %v", err)
	}
}

// providerConfig is a valid provider role with characters that must be
// percent-encoded in the credentials.
func providerConfig() settings.ProviderProxyConfig {
	return settings.ProviderProxyConfig{
		Enabled:  true,
		Username: "us:er",
		Password: "p@ss",
		Endpoint: "us2.proxy.invalid:3010",
		Duration: 7,
		Regions:  "JP",
	}
}
