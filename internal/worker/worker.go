// Package worker ports OpenAIRegisterPayLinkWorker (app.py:8846-12298) — the
// browser-driven OpenAI account registration + payment-link extraction flow.
// This file holds the shared worker state, construction, the proxy-health ->
// fingerprint handshake, browser teardown, and the process-global registry of
// browsers deliberately kept open after a run.
package worker

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxyhealth"
)

// LogFunc receives user-facing progress lines (the Python `log` callback).
type LogFunc func(string)

// InputFunc blocks for human input (manual email-OTP entry). kind is e.g.
// "email-code". Returns "" when the user cancels.
type InputFunc func(kind, email, prompt string) string

// FingerprintCallback persists an account's fixed fingerprint (best-effort; the
// Python version swallows any error from it).
type FingerprintCallback func(email string, fp models.DeviceFingerprint)

// Config mirrors the OpenAIRegisterPayLinkWorker constructor (app.py:8847).
type Config struct {
	Account       *models.MailAccount
	PaymentMode   string
	TargetAmount  string
	Headless      bool
	RegisterProxy models.ProxyConfig
	ExtractProxy  models.ProxyConfig
	Log           LogFunc

	PhoneProvider PhoneProvider
	InputCallback InputFunc

	// Link proxies fall back in a CASCADE (create<-extract, followup<-create,
	// approve<-followup). A naive per-field default breaks the chain when only
	// some are supplied.
	LinkCreateProxy   *models.ProxyConfig
	LinkFollowupProxy *models.ProxyConfig
	LinkApproveProxy  *models.ProxyConfig

	RequireJapanExtractProxy bool
	ForceLegacyPayPal        bool
	ManualEmailOTP           bool

	// TrialClaimScoreFallback opts into the DOM-scoring trial-claim pass, which
	// is OFF by default because the Python equivalent never actually ran (a JS
	// SyntaxError killed it), so no production run has ever exercised it and it
	// can click the wrong checkout. See ClickTrialClaimButtonOnPage.
	TrialClaimScoreFallback bool

	SavedFingerprint    *models.DeviceFingerprint
	FingerprintCallback FingerprintCallback

	ExtensionDir string
}

// Worker is one registration/extraction run.
type Worker struct {
	cfg Config
	log LogFunc

	Account     *models.MailAccount
	Fingerprint models.DeviceFingerprint
	// fingerprintFixed gates regeneration in PrepareFingerprintForProxy. A saved
	// fingerprint pre-fixes it so retries never drift.
	fingerprintFixed bool

	CurrentProxyHealth *models.ProxyHealthResult

	LinkCreateProxy   models.ProxyConfig
	LinkFollowupProxy models.ProxyConfig
	LinkApproveProxy  models.ProxyConfig

	ActiveRegisterPhone map[string]string
	otpReader           mail.Reader
}

// New builds a Worker, reproducing the constructor's normalization and the
// order-dependent link-proxy fallback cascade.
func New(cfg Config) *Worker {
	if cfg.Log == nil {
		cfg.Log = func(string) {}
	}
	if cfg.Account != nil {
		cfg.Account.Email = models.NormalizeEmailAddress(cfg.Account.Email)
	}

	create := cfg.ExtractProxy
	if cfg.LinkCreateProxy != nil {
		create = *cfg.LinkCreateProxy
	}
	followup := create
	if cfg.LinkFollowupProxy != nil {
		followup = *cfg.LinkFollowupProxy
	}
	approve := followup
	if cfg.LinkApproveProxy != nil {
		approve = *cfg.LinkApproveProxy
	}

	fp := models.GenerateRegisterFingerprint()
	fixed := false
	if cfg.SavedFingerprint != nil {
		fp = *cfg.SavedFingerprint
		fixed = true
	}

	return &Worker{
		cfg:               cfg,
		log:               cfg.Log,
		Account:           cfg.Account,
		Fingerprint:       fp,
		fingerprintFixed:  fixed,
		LinkCreateProxy:   create,
		LinkFollowupProxy: followup,
		LinkApproveProxy:  approve,
	}
}

// Log emits a progress line.
func (w *Worker) Log(msg string) { w.log(msg) }

// PrepareFingerprintForProxy mirrors _prepare_fingerprint_for_proxy
// (app.py:9155): health-check the proxy, then fix the fingerprint to the exit
// geo exactly once, persist it onto the account, and log the outcome.
func (w *Worker) PrepareFingerprintForProxy(proxy models.ProxyConfig, label string) (models.ProxyHealthResult, error) {
	proxyURL := firstNonEmpty(proxy.ChainURL, proxy.LocalProxy, proxy.DynamicProxy)
	// `not str(proxy.dynamic_proxy or "").strip()` (app.py:9157) — str.strip(),
	// which unlike TrimSpace also removes U+001C..U+001F.
	localOnly := pyStrip(proxy.DynamicProxy) == ""

	var health models.ProxyHealthResult
	if localOnly {
		w.log(fmt.Sprintf("[代理] %s全走本地：跳过 ipinfo，仅检测 auth.openai.com / 本地连通", label))
		health = proxyhealth.DetectLocalProxyHealthWithRetry(proxyURL, 15, 3, proxyhealth.LogFunc(w.log), label+"本地")
	} else {
		health = proxyhealth.DetectProxyHealthWithRetry(proxyURL, 15, 3, proxyhealth.LogFunc(w.log), label+"代理")
	}
	if !health.Success {
		return health, &models.ProxyExitCheckError{
			Msg:    fmt.Sprintf("%s代理健康检查失败: %s", label, health.Summary()),
			Status: "代理检测失败",
		}
	}
	w.CurrentProxyHealth = &health

	if !w.fingerprintFixed {
		fp, err := models.GenerateFingerprintForExit(health)
		if err != nil {
			// Local-only mode has no real exit geo — fall back, but STILL fix it,
			// otherwise every retry regenerates and the fingerprint drifts.
			w.log(fmt.Sprintf("[代理] 出口指纹生成失败，使用默认指纹: %v", err))
			fp = models.GenerateFingerprintForLocale("en-US", []string{"en-US", "en"}, "UTC")
		}
		w.Fingerprint = fp
		w.fingerprintFixed = true
		if w.Account != nil {
			w.Account.BrowserFingerprint = &fp
			if w.cfg.FingerprintCallback != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							w.log(fmt.Sprintf("[系统] 保存账号固定指纹失败，已忽略: %v", r))
						}
					}()
					w.cfg.FingerprintCallback(w.Account.Email, fp)
				}()
			}
		}
	}

	if localOnly {
		w.log(fmt.Sprintf("[代理] %s本地连通通过: Auth 可达 ChatGPT=%s 指纹时区=%s（已跳过 ipinfo）",
			label, dashIfZero(health.ChatGPTStatus), orDefault(health.Timezone, "UTC")))
	} else {
		w.log(fmt.Sprintf("[代理] %s出口检查通过: %s %s %s ChatGPT=%d Stripe=%d",
			label, health.IP, orDefault(health.Location(), health.Country),
			orDefault(health.Timezone, "UTC"), health.ChatGPTStatus, health.StripeStatus))
	}
	w.log(fmt.Sprintf("[系统] 当前账号固定指纹: %s", models.FingerprintSummaryText(&w.Fingerprint)))
	return health, nil
}

// NewBrowser mirrors _new_browser_context (app.py:9129): launch with the
// fingerprint-derived args and the proxy chain URL, then emulate per page.
func (w *Worker) NewBrowser(proxy models.ProxyConfig) (*browser.Browser, error) {
	return browser.Launch(browser.LaunchOptions{
		Fingerprint:  w.Fingerprint,
		Headless:     w.cfg.Headless,
		ProxyServer:  proxy.ChainURL,
		ExtensionDir: w.cfg.ExtensionDir,
	})
}

// LogBrowserProxyStatus mirrors _log_browser_proxy_status (app.py:9326).
func (w *Worker) LogBrowserProxyStatus(label string) {
	if w.CurrentProxyHealth != nil && w.CurrentProxyHealth.Success {
		w.log(fmt.Sprintf("[代理] %s: %s", label, w.CurrentProxyHealth.Summary()))
		return
	}
	w.log(fmt.Sprintf("[代理] %s: 未记录出口信息", label))
}

// CloseBrowser mirrors _close_browser (app.py:9197): close and swallow errors.
func CloseBrowser(b *browser.Browser) {
	if b == nil {
		return
	}
	defer func() { _ = recover() }()
	b.Close()
}

// CleanupProfileDir mirrors _cleanup_profile_dir (app.py:9209): Windows holds
// file locks briefly after the browser exits, so retry with a growing backoff.
func (w *Worker) CleanupProfileDir(dir string) {
	if dir == "" {
		return
	}
	for attempt := 0; attempt < 8; attempt++ {
		err := os.RemoveAll(dir)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Duration(500+attempt*250) * time.Millisecond)
	}
	w.log(fmt.Sprintf("临时浏览器目录清理失败，已忽略: %s", dir))
}

// ---------------------------------------------------------------------------
// Kept-open browser registry (module-global KEPT_REGISTER_BROWSER_SESSIONS).
// ---------------------------------------------------------------------------

// KeptSession is a browser deliberately left running after a worker returns so
// the user can keep using the logged-in window.
type KeptSession struct {
	Browser      *browser.Browser
	DynamicProxy string
	// Cleanup 持有浏览器依赖的本地代理链。浏览器启动参数指向 chain_url，
	// 因此窗口被保留时不能让任务 defer 先关闭链。
	Cleanup    func()
	generation uint64
}

var (
	keptMu       sync.Mutex
	keptSessions = map[string]*KeptSession{}
	keptSequence atomic.Uint64
)

// ParkBrowser stores a live browser under the (lowercased) email, closing any
// previous one for that account. The caller must then treat the browser as NOT
// owned — mirroring the Python nil-reassign trick that disarms the finally-close.
func ParkBrowser(email string, b *browser.Browser, dynamicProxy string) {
	key := strings.ToLower(strings.TrimSpace(email))
	keptMu.Lock()
	prev := keptSessions[key]
	keptSessions[key] = &KeptSession{
		Browser: b, DynamicProxy: dynamicProxy, generation: keptSequence.Add(1),
	}
	keptMu.Unlock()
	if prev != nil {
		CloseBrowser(prev.Browser)
		if prev.Cleanup != nil {
			prev.Cleanup()
		}
	}
}

// ParkedBrowserGeneration 返回当前保留窗口的代次。任务开始前保存它，
// 结束后即可区分“本任务刚保留的窗口”和此前就存在的旧窗口。
func ParkedBrowserGeneration(email string) uint64 {
	key := strings.ToLower(strings.TrimSpace(email))
	keptMu.Lock()
	defer keptMu.Unlock()
	if session := keptSessions[key]; session != nil {
		return session.generation
	}
	return 0
}

// AttachParkedCleanupSince 只在当前窗口比 before 更新时移交资源。这样一个
// 启动失败的任务不会误把自己的代理链挂到同邮箱的旧保留窗口上。
func AttachParkedCleanupSince(email string, before uint64, cleanup func()) bool {
	if cleanup == nil {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(email))
	keptMu.Lock()
	session := keptSessions[key]
	if session == nil || session.generation == before {
		keptMu.Unlock()
		return false
	}
	previous := session.Cleanup
	session.Cleanup = cleanup
	keptMu.Unlock()
	if previous != nil {
		previous()
	}
	return true
}

// AttachParkedCleanup 用于没有旧窗口歧义的调用者和兼容现有测试。
func AttachParkedCleanup(email string, cleanup func()) bool {
	return AttachParkedCleanupSince(email, 0, cleanup)
}

// TakeParkedBrowser removes and returns a parked browser, if any.
func TakeParkedBrowser(email string) *KeptSession {
	key := strings.ToLower(strings.TrimSpace(email))
	keptMu.Lock()
	defer keptMu.Unlock()
	s := keptSessions[key]
	delete(keptSessions, key)
	return s
}

// CloseParkedBrowser closes and forgets a parked browser.
func CloseParkedBrowser(email string) {
	if s := TakeParkedBrowser(email); s != nil {
		CloseBrowser(s.Browser)
		if s.Cleanup != nil {
			s.Cleanup()
		}
	}
}

// CloseAllParkedBrowsers 关闭并清空所有被保留的浏览器。
//
// 这些浏览器会故意跨任务存活，因此不能依赖单个任务的 defer 回收；桌面应用
// 退出时必须统一关闭，避免残留 Chrome 进程和仍然可用的登录会话。
func CloseAllParkedBrowsers() {
	keptMu.Lock()
	sessions := make([]*KeptSession, 0, len(keptSessions))
	for _, session := range keptSessions {
		sessions = append(sessions, session)
	}
	keptSessions = map[string]*KeptSession{}
	keptMu.Unlock()

	for _, session := range sessions {
		if session != nil {
			CloseBrowser(session.Browser)
			if session.Cleanup != nil {
				session.Cleanup()
			}
		}
	}
}

// firstNonEmpty is Python's `a or b or c` over strings (app.py:9156). The `or`
// fires on the EMPTY string only: a whitespace-only chain_url is TRUTHY in
// Python and is passed to the health check as-is, so trimming here would
// silently health-check a different proxy than the browser is launched with
// (NewBrowser uses proxy.ChainURL verbatim).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// orDefault is Python's `value or DEFAULT` (app.py:9186-9192) — again, empty
// string only. `health.timezone or 'UTC'` keeps a " " timezone.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func dashIfZero(v int) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprint(v)
}
