package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/phoneprovider"
	"github.com/pkppkq/openai-register-go/internal/proxychain"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/proxyroute"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// This file maps the persisted settings.* keys onto worker.Config. Every key
// referenced here was read out of the user's real state.json rather than
// guessed — a wrong mapping here would run a money-spending job with the wrong
// payment mode or the wrong proxy.

// snapshot loads state.json. Callers must not hold a.mu.
func (a *App) snapshot() (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.Load()
}

// There are deliberately NO local settings coercion helpers in this package.
//
// There used to be (sStr / sBool / sStrDefault), and every one of them diverged
// from Python in a way that only shows up on real data:
//
//   - sBool returned false for the string "false" — correct-looking, and the
//     opposite of Python, where bool("false") is True. The Tk app round-trips
//     BooleanVars through strings, so a setting could silently re-enable itself.
//   - sBool returned false for a JSON 1/1.0, where bool(1) is True.
//   - sStr could not tell an absent key from a present-but-empty one, which for
//     local_proxy is the difference between "chain through the local proxy" and
//     "register from this machine's own address".
//
// settings.FromSnapshot models all of it — the `if key in settings` guards, the
// payment-mode alias table, the int()/float() blank rules — and is differentially
// verified against CPython. Decode once, read fields.

// accountByEmail finds one account in the persisted list.
func (a *App) accountByEmail(email string) (models.MailAccount, error) {
	want := models.NormalizeEmailAddress(email)
	if want == "" {
		return models.MailAccount{}, fmt.Errorf("未指定账号邮箱")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return models.MailAccount{}, err
	}
	for _, account := range accountsFromSnapshot(snapshot) {
		if strings.EqualFold(account.Email, want) {
			return account, nil
		}
	}
	return models.MailAccount{}, fmt.Errorf("账号不存在: %s", email)
}

// registerDynamicProxy picks the dynamic hop the register/session run chains
// through, mirroring _read_dynamic_proxies (app.py:16725) and the pool choice at
// app.py:15343 / 17682:
//
//   - the 全走本地代理 route mode empties every pool, so the chain degenerates to
//     the local proxy alone;
//   - register_with_payment_proxy swaps the register pool for the payment (create)
//     pool — that checkbox is labelled "特殊情况勾选" and is off by default, so the
//     normal pool is settings.dynamic_proxies.
//
// This is the SINGLE-ACCOUNT answer, and it deliberately does not rotate: a
// standalone StartRegister takes the pool's first entry, the same way one
// account run from the Tk app does. Rotation is per-attempt state that only a
// batch has — app.py's _take_dynamic_proxies moves the taken exit to the tail so
// consecutive accounts leave through different ones — and it lives where that
// state lives: authProxyPool builds the proxypool.Set, batchOptions.Proxies
// takes from it per attempt, and workerConfigProxy substitutes the result for
// this function's return value (batch.go).
func registerDynamicProxy(st settings.Settings) string {
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return ""
	}
	text := st.DynamicProxies
	if st.RegisterWithPaymentProxy {
		text = st.PaymentDynamicProxy
	}
	return firstProxy(text)
}

// entryDynamicProxy is the exit the run's OWN chain leaves through — which is
// not the same question per entry point.
//
// For relink it is deliberately NOT registerDynamicProxy: app.py:17917 is
//
//	extract_dynamic_proxy = self._next_dynamic_proxy(dynamic_proxies)
//	register_dynamic_proxy = create_dynamic_proxy if use_payment_proxy_for_register
//	                         else extract_dynamic_proxy
//
// so the browser extraction ALWAYS leaves through the register pool, and the
// 特殊情况 checkbox redirects only the login hop, and redirects it to the
// create stage of the link triple — not to payment_dynamic_proxy[0], which is
// merely one of the inputs the triple is derived from. openLinkRoutes finishes
// that half once the triple exists.
//
// _next_dynamic_proxy ROTATES, and this used to take entries[0] every time — so
// clicking 重新获取 down a list of accounts created every one of their payment
// links from the same exit, which is the thing the pool exists to prevent.
func (a *App) entryDynamicProxy(kind JobKind, st settings.Settings) string {
	if kind == JobRelink {
		if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
			return ""
		}
		return a.nextDynamicProxy(proxypool.ParseProxyPoolText(st.DynamicProxies))
	}
	return registerDynamicProxy(st)
}

// nextDynamicProxy is _next_dynamic_proxy's body (app.py:17603-17606):
//
//	value = dynamic_proxies[self.dynamic_proxy_index % len(dynamic_proxies)]
//	self.dynamic_proxy_index += 1
//
// The modulus is taken against the CURRENT length, so editing the pool between
// two runs shifts which entry the same cursor lands on. That is Python's
// behaviour, not an oversight to tidy up: the cursor is not an index into a
// remembered list, it is a counter.
func (a *App) nextDynamicProxy(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	// Add returns the NEW value, so subtract one to use the pre-increment cursor
	// and keep the first call landing on entries[0].
	i := a.dynamicProxyIndex.Add(1) - 1
	return entries[i%uint64(len(entries))]
}

func firstProxy(text string) string {
	entries := proxypool.ParseProxyPoolText(text)
	if len(entries) == 0 {
		return ""
	}
	return entries[0]
}

// openLinkRoutes starts the create/followup/approve chains and hangs them off
// the config (app.py:17874-17905).
//
// Without this the whole payment pipeline fell back to ExtractProxy inside
// worker.New — meaning every link was created, followed up and approved through
// the exit the account had just LOGGED IN from. The three stages exist precisely
// so they do not share that address, and settings.reuse_*/provider_proxy_configs
// exist to control them; ignoring all of it silently created real links through
// the register exit.
func (a *App) openLinkRoutes(cfg *worker.Config, res *runResources, st settings.Settings, log func(string)) error {
	routes, err := proxyroute.OpenSettings(st, proxyroute.LogFunc(log))
	if err != nil {
		// ErrProxyPoolExhausted is app.py:15230-15236: a followup/approve pool with
		// no create pool behind it must stop the run, not fall back to direct.
		return fmt.Errorf("准备支付链接代理失败: %w", err)
	}
	res.links = routes

	create, followup, approve := routes.Create, routes.Followup, routes.Approve
	cfg.LinkCreateProxy = &create
	cfg.LinkFollowupProxy = &followup
	cfg.LinkApproveProxy = &approve

	// The other half of app.py:17918. The create stage's chain already terminates
	// on exactly the exit Python would have opened a second listener for, and a
	// chain server holds no per-connection state, so it is reused rather than
	// duplicated.
	if st.RegisterWithPaymentProxy {
		cfg.RegisterProxy = create
	}
	return nil
}

// proxySession is Python's `with ProxyChainServer(local, dynamic, log) as chain`
// (app.py:17721). The listener lives exactly as long as the job, so Close must run
// when the job ends — otherwise a later job can still reach a chain pointed at an
// exit it did not choose.
type proxySession struct {
	Config models.ProxyConfig
	server *proxychain.Server
}

func (p *proxySession) Close() {
	if p != nil && p.server != nil {
		p.server.Close()
	}
}

// openProxySession starts the chain for one run.
//
// Both proxies empty is a DIRECT connection, which is what Python does
// (ProxyChainServer.__enter__ returns without binding, app.py:5937) — but it means
// registering from this machine's own address, so it is logged rather than left
// silent.
func (a *App) openProxySession(st settings.Settings, dynamic string, log func(string)) (*proxySession, error) {
	local := proxypool.NormalizeProxyURL(st.LocalProxy)

	server := proxychain.New(local, dynamic, proxychain.LogFunc(log))
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("启动链式代理失败: %w", err)
	}
	cfg := models.ProxyConfig{LocalProxy: local, DynamicProxy: dynamic, ChainURL: server.URL()}
	if cfg.ChainURL == "" && log != nil {
		log("注册使用代理: 直连（settings.local_proxy 与动态代理池均为空）")
	}
	return &proxySession{Config: cfg, server: server}, nil
}

// runResources are the live handles one job owns. Close must run when the job
// ends: the proxy chain is a listening socket, and the phone provider may be
// holding a RENTED, BILLABLE number that has to be released.
type runResources struct {
	proxy *proxySession
	// links is the create/followup/approve triple, non-nil only for JobRelink.
	// It owns up to three more listeners.
	links *proxyroute.Routes
	phone *phoneprovider.SMSBowerProvider
}

func (r *runResources) Close() {
	if r == nil {
		return
	}
	r.proxy.Close()
	if r.links != nil {
		r.links.Close()
	}
	if r.phone != nil {
		r.phone.Close()
	}
}

// phoneProvider builds the SMSBower-backed provider for one run (UI_SPEC gap G3).
//
// ctx is the JOB context, which is what makes the release automatic: the adapter
// watches it and hands every outstanding rental back when the job is stopped or
// simply finishes. Snapshot is a live reader rather than a captured map so a price
// cap or an API key edited mid-run takes effect — and so smsbower_max_price goes
// through settings.FromSnapshot, whose asymmetric ""->"0.07" load rule is the
// difference between a cap and renting at any price (app.py:14190 vs 14278).
func (a *App) phoneProvider(ctx context.Context, snapshot map[string]any, log func(string)) *phoneprovider.SMSBowerProvider {
	pool := a.sharedPhonePool()
	if err := a.refreshPhonePool(); err != nil {
		// workerConfig 已经成功读取过本次启动快照。最新读取偶发失败时使用
		// 它初始化共享池，避免把手工号码池静默降级为空；后续动作的实时
		// settings 读取仍会继续尝试 state。
		if log != nil {
			log("刷新共享手机号池失败，使用任务启动快照: " + err.Error())
		}
		a.refreshPhonePoolFromSnapshot(snapshot)
	}
	return phoneprovider.NewSMSBowerProvider(phoneprovider.SMSBowerConfig{
		Snapshot: func() map[string]any { s, _ := a.snapshot(); return s },
		Pool:     pool,
		Context:  ctx,
		// The worker logs per account already; drop the address to avoid printing
		// it twice on every line.
		Log: func(_, msg string) { log(msg) },
	})
}

// entryPointCaps is Python's four worker constructor sites, which do NOT pass the
// same arguments. Reading one settings block into all five Go entry points quietly
// gives each flow capabilities its Python counterpart does not have.
//
//	site                       app.py   phone  input  manualOTP  legacyPP  japanExit  link*
//	_run_account_once           17727     yes    yes     yes        yes       no        no
//	_run_team_account_once      17837     no     no      no         no        yes       no
//	_run_domain_mail_rt_once    17786     yes    yes     yes        no        no        no
//	_refetch_account_once       17895     no     no      no         yes       yes       YES
type entryCaps struct {
	phoneProvider       bool
	inputCallback       bool
	manualEmailOTP      bool
	forceLegacyPayPal   bool
	requireJapanExtract bool
	// linkProxies is the three-stage create/followup/approve cascade. ONLY
	// _refetch_account_once passes link_create_proxy / link_followup_proxy /
	// link_approve_proxy (app.py:17903-17905); every other constructor leaves them
	// None, and worker.New then falls the whole payment pipeline back to
	// ExtractProxy.
	linkProxies bool
}

func entryPointCaps(kind JobKind) entryCaps {
	switch kind {
	case JobTeam:
		return entryCaps{requireJapanExtract: true}
	case JobRegisterAndRT:
		return entryCaps{phoneProvider: true, inputCallback: true, manualEmailOTP: true}
	case JobRelink:
		return entryCaps{forceLegacyPayPal: true, requireJapanExtract: true, linkProxies: true}
	case JobKeepLogin, JobExternalOAuth:
		return entryCaps{inputCallback: true, manualEmailOTP: true}
	case JobSessionReader, JobManualLoginCode:
		return entryCaps{}
	default: // JobRegister / JobAuthOnly
		return entryCaps{phoneProvider: true, inputCallback: true, manualEmailOTP: true, forceLegacyPayPal: true}
	}
}

// workerConfig assembles the run configuration for one account. The returned
// runResources own a listening socket and possibly a billable phone rental; the
// caller must Close them when the job ends.
func (a *App) workerConfig(ctx context.Context, kind JobKind, account models.MailAccount, log func(string)) (worker.Config, *runResources, error) {
	return a.workerConfigProxy(ctx, kind, account, nil, log)
}

// workerConfigProxy is workerConfig with the run's own exit overridden.
//
// dynamicOverride is non-nil only for a batch, where _run_account_thread takes a
// FRESH proxy for every attempt (app.py:17671-17679) instead of always using the
// pool's first entry. A pointer rather than a string because "" is a meaningful
// override — it is what 全走本地代理 and an exhausted pool both produce — and must
// not be confused with "not overridden".
func (a *App) workerConfigProxy(ctx context.Context, kind JobKind, account models.MailAccount, dynamicOverride *string, log func(string)) (worker.Config, *runResources, error) {
	return a.workerConfigProxyRoutes(ctx, kind, account, dynamicOverride, nil, log)
}

// workerConfigProxyRoutes 允许“批量重新获取”把预先分配给每个账号的
// create/followup/approve 三段代理固定到本次运行。单账号入口传 nil，
// 继续按实时设置选择；批量入口传入显式三元组，避免所有并发账号误用池首。
func (a *App) workerConfigProxyRoutes(
	ctx context.Context,
	kind JobKind,
	account models.MailAccount,
	dynamicOverride *string,
	linkTriple *[3]string,
	log func(string),
) (worker.Config, *runResources, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return worker.Config{}, nil, err
	}
	// ONE decoder. settings.FromSnapshot models every key's load-time coercion —
	// the payment-mode alias table, the bool()/int()/float() semantics, the
	// key-presence defaults — and has been differentially verified against
	// CPython over thousands of round trips. Re-coercing here with local helpers
	// is how the payment_mode and local_proxy divergences got in.
	st := settings.FromSnapshot(snapshot)
	caps := entryPointCaps(kind)

	// A batch already took its exit from the rotating pool, so entryDynamicProxy
	// is not even consulted — calling it would advance the 重新获取 cursor for an
	// attempt that is not using it.
	dynamic := ""
	if dynamicOverride != nil {
		dynamic = *dynamicOverride
	} else {
		dynamic = a.entryDynamicProxy(kind, st)
	}
	session, err := a.openProxySession(st, dynamic, log)
	if err != nil {
		return worker.Config{}, nil, err
	}
	res := &runResources{proxy: session}
	if caps.phoneProvider {
		res.phone = a.phoneProvider(ctx, snapshot, log)
	}

	cfg := worker.Config{
		Account: &account,
		// Validated + alias-mapped, not raw: app.py:14076-14080 maps the 短链
		// spellings onto the canonical name and silently keeps the default for
		// anything unrecognised. settings.FromSnapshot already models that.
		PaymentMode:  st.PaymentMode,
		TargetAmount: st.TargetAmount,
		Headless:     st.Headless,
		// app.py:17722 is literally `extract_proxy = register_proxy`: the session
		// fetch reuses the exact exit the registration ran through, so OpenAI never
		// sees the login move to a different address mid-flow. They are one chain,
		// not two roles.
		//
		// Relink is the one entry point where they can differ, and openLinkRoutes
		// separates them there — see entryDynamicProxy.
		RegisterProxy: session.Config,
		ExtractProxy:  session.Config,
		Log:           log,
		// G3, and PER ENTRY POINT — see entryPointCaps. Handing this to Relink
		// would let a relink login rent a billable number that Python's
		// _refetch_account_once can never rent, because it passes no provider.
		PhoneProvider: res.phone,

		ManualEmailOTP:           caps.manualEmailOTP && st.ManualEmailOTP,
		ForceLegacyPayPal:        caps.forceLegacyPayPal && st.ForceLegacyPaypal,
		RequireJapanExtractProxy: caps.requireJapanExtract && st.RequireJapanExtractProxy,
		// ExtensionDir is deliberately NOT set. payment_extension_dir is read at
		// app.py:24549/24618/24653 and every one of them feeds
		// _open_payment_link_worker (17959), a separate GUI-side browser. The
		// worker's own _new_browser_context (9130-9137) never passes
		// --load-extension, and OpenAIRegisterPayLinkWorker.__init__ (8847) has no
		// extension parameter at all. Loading the PayPal extension into every
		// registration browser injects its content scripts and a
		// chrome-extension:// surface into the anti-detection flow.

		// TrialClaimScoreFallback stays at its safe default (false). See
		// worker.Config for why it is not simply enabled.

		// InputCallback is filled in by runJob, which is where the job id the
		// prompt round-trip keys off exists.
		//
		// Link*Proxy is filled in below, for JobRelink only.
	}
	if caps.linkProxies {
		linkSettings := st
		if linkTriple != nil {
			// 复用项优先于池，且一个显式空三元组表示只走本地代理/直连。
			// 清空三段池可防止空元素重新回落到实时池首。
			linkSettings.ReusePaymentProxy = linkTriple[0]
			linkSettings.ReuseFollowupProxy = linkTriple[1]
			linkSettings.ReuseApproveProxy = linkTriple[2]
			linkSettings.PaymentDynamicProxy = ""
			linkSettings.FollowupDynamicProxy = ""
			linkSettings.ApproveDynamicProxy = ""
			linkSettings.ProviderProxyConfigs = map[string]settings.ProviderProxyConfig{}
		}
		if err := a.openLinkRoutes(&cfg, res, linkSettings, log); err != nil {
			res.Close()
			return worker.Config{}, nil, err
		}
	}
	if account.BrowserFingerprint != nil {
		cfg.SavedFingerprint = account.BrowserFingerprint
	}
	cfg.FingerprintCallback = a.saveFingerprint
	return cfg, res, nil
}

// saveFingerprint persists the fingerprint the worker settled on
// (_save_account_fingerprint, app.py:20999).
//
// Why this matters: without it every run generates a fresh random device, so the
// same OpenAI account logs in from a different machine every time — which is
// precisely the signal that gets accounts flagged. The worker only calls this for
// the exit-matched fingerprint, never the provisional one from its constructor.
//
// app.py:21008 compares before writing, and that guard is load-bearing rather than
// an optimisation: this fires on every proxy handshake, and rewriting state.json
// each time would hammer a file the Python app is also holding.
func (a *App) saveFingerprint(email string, fp models.DeviceFingerprint) {
	stored := models.NormalizeFingerprintForStorage(&fp)
	if stored == nil {
		return
	}
	key := strings.ToLower(models.NormalizeEmailAddress(email))
	if key == "" {
		return
	}
	// flush=true, NOT the debounced path. A debounced write is held in Store.pending
	// and dropped by the next flush=true save; and the background writer would then
	// touch Store's deferredSessionIndex concurrently with a Load from a UI call —
	// a concurrent map read+write, which is a fatal runtime error, not a tolerable
	// race. Best-effort, like Python: a failed fingerprint write must not fail the run.
	_ = a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		rows, _ := snapshot["accounts"].([]any)
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			account := models.AccountFromMap(m)
			if strings.ToLower(models.NormalizeEmailAddress(account.Email)) != key {
				continue
			}
			if models.FingerprintsEqual(account.BrowserFingerprint, stored) {
				return nil, nil, errNoStateChange
			}
			m["browser_fingerprint"] = models.FingerprintToMap(stored)
			return snapshot, map[string]bool{}, nil
		}
		// The account vanished from state.json between the run starting and now.
		return nil, nil, errNoStateChange
	})
}
