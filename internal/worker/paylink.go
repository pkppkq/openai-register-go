// Package worker ports OpenAIRegisterPayLinkWorker (app.py:8846-12298).
//
// This file is the payment-link / trial-short-link cluster (app.py:11903-12298)
// plus the module-level trial-claim clicker it delegates to (app.py:8755-8843)
// and the two module-level proxy-log helpers the cluster calls
// (_log_link_proxy_group app.py:4003-4011, _detect_link_proxy_exits_concurrently
// app.py:4018-4051).
//
// The heavy lifting — checkout creation, Stripe/PayPal confirm, approve and the
// amount check — lives in internal/opll; this file is the browser-side driver:
// session/accessToken extraction, proxy-exit gating, the retry envelope and the
// trial "click the claim button and race the redirect" flow.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/opll"
	"github.com/pkppkq/openai-register-go/internal/proxyhealth"
)

// ---------------------------------------------------------------------------
// Timing / caps — every one of these mirrors a literal in app.py and must not
// be "rounded". All deadlines use monotonic time (time.Since / time.Now().Add).
// ---------------------------------------------------------------------------

const (
	// payLinkMaxAttempts is `for attempt in range(1, 16)` (app.py:11961).
	payLinkMaxAttempts = 15
	// payLinkElapsedCap is the SECOND half of the dual cap: even inside the 15
	// attempts, >120s elapsed breaks the loop (app.py:11964-11965).
	payLinkElapsedCap = 120 * time.Second
	// payLinkRetrySleep is time.sleep(4) between failed attempts (app.py:12133).
	payLinkRetrySleep = 4 * time.Second
	// payLinkNavTimeout is the goto timeout=60000 used at app.py:11958, 12139, 12254.
	payLinkNavTimeout = 60 * time.Second
	// payLinkBodyTextTimeout is inner_text(timeout=15000) (app.py:12140).
	payLinkBodyTextTimeout = 15 * time.Second

	// trialClaimDeadline is `time.time() + 90` (app.py:8756).
	trialClaimDeadline = 90 * time.Second
	// trialClaimPoll is time.sleep(0.8) at the end of each sweep (app.py:8842).
	trialClaimPoll = 800 * time.Millisecond
	// trialClaimLocateTimeout mirrors scroll_into_view_if_needed(timeout=1200)
	// (app.py:8786) — the locate+scroll step of the get_by_role path.
	trialClaimLocateTimeout = 1200 * time.Millisecond
	// trialClaimScanTimeout is Playwright's default page.evaluate timeout (30s)
	// for the scoring sweep at app.py:8793 (Python passed no explicit timeout).
	trialClaimScanTimeout = 30 * time.Second

	// trialRedirectWindow is `while time.time() - started < 60` (app.py:12261).
	trialRedirectWindow = 60 * time.Second
	// trialRedirectPoll is time.sleep(1) inside that race (app.py:12295).
	trialRedirectPoll = 1 * time.Second
)

// trialPricingURL is the promo page opened before clicking the claim button
// (app.py:12254).
const trialPricingURL = "https://chatgpt.com/?promo_campaign=plus-1-month-free#pricing"

// sessionEndpointURL is the raw session endpoint opened by _extract_session_info
// (app.py:12139).
const sessionEndpointURL = "https://chatgpt.com/api/auth/session"

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// AmountFields mirrors the four amount keys that travel together through the
// paylink cluster: the dict returned by _opll_amount_fields (app.py:11906-11912)
// and, when read raw off an opll result, the input of _opll_amount_log_text
// (app.py:11914-11923).
type AmountFields struct {
	StripeAmount       string `json:"stripe_amount"`
	StripeAmountSource string `json:"stripe_amount_source"`
	TargetAmount       string `json:"target_amount"`
	AmountCheck        string `json:"amount_check"`
}

// AmountFieldsFromLinkResult reads the raw amount keys off an opll result. Use
// the raw values for OpllAmountLogText (Python passed the raw link_result) and
// OpllAmountFields for the values embedded in the returned PayLinkResult.
func AmountFieldsFromLinkResult(r *opll.LinkResult) AmountFields {
	if r == nil {
		return AmountFields{}
	}
	return AmountFields{
		StripeAmount:       r.StripeAmount,
		StripeAmountSource: r.StripeAmountSource,
		TargetAmount:       r.TargetAmount,
		AmountCheck:        r.AmountCheck,
	}
}

// PayLinkResult mirrors the dict returned by _extract_pay_link
// (app.py:11989-12008 / 12046-12065 / 12075-12094 / 12106-12125). All four
// return sites build the same key set; only url/checkout_url/session_json/
// payment_link_type and the amount fields differ. JSON tags match the Python
// dict keys one-for-one so the state layer can round-trip them.
//
// Note the deliberate asymmetry baked into the proxy fields: LinkProxy /
// Link*Proxy report `dynamic_proxy or local_proxy` (the chain URL is NEVER
// reported), while the URLs actually used for routing are
// `chain_url or local_proxy or dynamic_proxy`.
type PayLinkResult struct {
	URL         string `json:"url"`
	CheckoutURL string `json:"checkout_url"`
	AccessToken string `json:"access_token"`
	SessionJSON string `json:"session_json"`

	LinkProxy      string `json:"link_proxy"`
	LinkProxyLabel string `json:"link_proxy_label"`
	LinkProxyExit  string `json:"link_proxy_exit"`

	LinkCreateProxy      string `json:"link_create_proxy"`
	LinkCreateProxyLabel string `json:"link_create_proxy_label"`
	LinkCreateProxyExit  string `json:"link_create_proxy_exit"`

	LinkFollowupProxy      string `json:"link_followup_proxy"`
	LinkFollowupProxyLabel string `json:"link_followup_proxy_label"`
	LinkFollowupProxyExit  string `json:"link_followup_proxy_exit"`

	LinkApproveProxy      string `json:"link_approve_proxy"`
	LinkApproveProxyLabel string `json:"link_approve_proxy_label"`
	LinkApproveProxyExit  string `json:"link_approve_proxy_exit"`

	PaymentLinkType string `json:"payment_link_type"`

	AmountFields
}

// TrialLinkResult mirrors the dict returned by _extract_trial_short_link_by_click
// (app.py:12267-12276 direct-PayPal branch, 12288-12294 checkout branch). The
// caller reads provider_redirect_url || long_url || url, so all three are kept
// separate instead of being collapsed.
type TrialLinkResult struct {
	URL                 string `json:"url"`
	LongURL             string `json:"long_url"`
	ProviderRedirectURL string `json:"provider_redirect_url"`
	CheckoutURL         string `json:"checkout_url"`
	AccessToken         string `json:"access_token"`
	SessionJSON         string `json:"session_json"`

	AmountFields
}

// SessionInfo mirrors the dict returned by _extract_session_info
// (app.py:12151-12156).
type SessionInfo struct {
	URL              string `json:"url"`
	AccessToken      string `json:"access_token"`
	SessionJSON      string `json:"session_json"`
	StorageStateJSON string `json:"storage_state_json"`
}

// LinkProxyExits mirrors the {create, followup, approve} dict returned by
// _detect_link_proxy_exits (app.py:11937-11946). A struct, not a map, because
// the failure gating below must run in a deterministic order.
type LinkProxyExits struct {
	Create   string `json:"create"`
	Followup string `json:"followup"`
	Approve  string `json:"approve"`
}

// ---------------------------------------------------------------------------
// PayLinkExtractor
// ---------------------------------------------------------------------------

// PayLinkExtractor owns the payment-link / trial-short-link cluster of
// OpenAIRegisterPayLinkWorker (app.py:11903-12298). The exported fields mirror
// the `self.*` attributes those methods read (app.py:8847-8896); browser/page
// mirror the `context` / `page` arguments the Python methods were handed.
type PayLinkExtractor struct {
	browser *browser.Browser
	page    *browser.Page
	log     func(string)

	// PaymentMode is self.payment_mode — a key of models.PaymentModes.
	PaymentMode string
	// TargetAmount is self.target_amount (already a plain string here; the
	// Python attribute was a Tk StringVar unwrapped by _target_amount_text).
	TargetAmount string
	// ForceLegacyPayPal is self.force_legacy_paypal (app.py:12096-12098).
	ForceLegacyPayPal bool
	// RequireJapanExtractProxy is self.require_japan_extract_proxy (app.py:11944).
	RequireJapanExtractProxy bool
	// TrialClaimScoreFallback opts into the DOM-scoring claim pass. Off by
	// default; see ClickTrialClaimButtonOnPage for why.
	TrialClaimScoreFallback bool

	// The three link proxies are ALREADY cascade-resolved by worker.New
	// (create<-extract, followup<-create, approve<-followup).
	LinkCreateProxy   models.ProxyConfig
	LinkFollowupProxy models.ProxyConfig
	LinkApproveProxy  models.ProxyConfig

	// LowerWindows mirrors lower_playwright_chromium_windows_later (app.py:456),
	// called from _extract_session_info's finally block. Optional; nil is a no-op
	// (the Python helper spawned a daemon thread whose failure was invisible).
	LowerWindows func(retries int)
}

// NewPayLinkExtractor binds the cluster to a live browser + page + log sink.
// The configuration fields are set by the caller (or by
// NewPayLinkExtractorFromWorker).
func NewPayLinkExtractor(b *browser.Browser, p *browser.Page, log func(string)) *PayLinkExtractor {
	return &PayLinkExtractor{browser: b, page: p, log: log}
}

// NewPayLinkExtractorFromWorker copies the paylink-relevant slice of a Worker's
// configuration (app.py:8847-8896) onto a fresh extractor, so the orchestration
// layer cannot silently drop e.g. RequireJapanExtractProxy.
func NewPayLinkExtractorFromWorker(w *Worker, b *browser.Browser, p *browser.Page) *PayLinkExtractor {
	e := NewPayLinkExtractor(b, p, nil)
	if w == nil {
		return e
	}
	e.log = w.log
	e.PaymentMode = w.cfg.PaymentMode
	e.TargetAmount = w.cfg.TargetAmount
	e.ForceLegacyPayPal = w.cfg.ForceLegacyPayPal
	e.RequireJapanExtractProxy = w.cfg.RequireJapanExtractProxy
	e.TrialClaimScoreFallback = w.cfg.TrialClaimScoreFallback
	e.LinkCreateProxy = w.LinkCreateProxy
	e.LinkFollowupProxy = w.LinkFollowupProxy
	e.LinkApproveProxy = w.LinkApproveProxy
	return e
}

// Page exposes the page the extractor drives (the orchestration layer needs it
// to park the browser after a successful extraction).
func (e *PayLinkExtractor) Page() *browser.Page { return e.page }

func (e *PayLinkExtractor) logLine(msg string) {
	if e == nil || e.log == nil {
		return
	}
	e.log(msg)
}

func (e *PayLinkExtractor) logf(format string, args ...any) {
	if e == nil || e.log == nil {
		return
	}
	if len(args) == 0 {
		e.log(format)
		return
	}
	e.log(fmt.Sprintf(format, args...))
}

// pageClosed mirrors the `page.is_closed()` guards (app.py:11956, 11962, 12262).
func (e *PayLinkExtractor) pageClosed() bool {
	return e.page == nil || e.page.Rod == nil || e.page.IsClosed()
}

// ---------------------------------------------------------------------------
// _target_amount_text (app.py:11903-11904)
// ---------------------------------------------------------------------------

// TargetAmountText mirrors _target_amount_text (app.py:11903-11904): the target
// amount as a trimmed string. The Tk StringVar unwrapping has no Go analogue —
// the value arrives already unwrapped on the struct.
func (e *PayLinkExtractor) TargetAmountText() string {
	return pyStrip(e.TargetAmount)
}

// ---------------------------------------------------------------------------
// _opll_amount_fields (app.py:11906-11912)
// ---------------------------------------------------------------------------

// OpllAmountFields mirrors _opll_amount_fields (app.py:11906-11912): normalize
// the four amount keys for the returned result.
//
// Python's four arms each read `link_result[k] if k in link_result else <dflt>`
// — the test is KEY PRESENCE, not truthiness, and only target_amount has a
// non-empty default (_target_amount_text()). A Go struct has no absent field, so
// the question is which arm it stands for, and the answer is "present":
// AmountFields is only ever built from an opll result, and every producer of one
// writes all four keys (opll_apply_amount_check sets target_amount and
// amount_check at app.py:4058/4060, and the direct-to-PayPal trial arm sets them
// inline at app.py:12274). There is no reachable Python input where the else
// branch fires.
//
// So there is deliberately NO empty-string fallback here. An earlier version had
// one, and it was wrong in the opposite direction: a result carrying an
// explicitly EMPTY target_amount — which is exactly what an amount check that
// was skipped produces — came back stamped with the configured target, making a
// skipped check look like a passed one in the stored result.
func (e *PayLinkExtractor) OpllAmountFields(in AmountFields) AmountFields {
	return in
}

// ---------------------------------------------------------------------------
// _opll_amount_log_text (app.py:11914-11923)
// ---------------------------------------------------------------------------

// OpllAmountLogText mirrors _opll_amount_log_text (app.py:11914-11923): the
// one-line amount-check summary, or "" when no target amount was configured.
// Feed it the RAW amount fields off the opll result (Python passed the raw
// link_result), not the OpllAmountFields output.
func (e *PayLinkExtractor) OpllAmountLogText(emailAddr string, in AmountFields) string {
	target := pyStrip(in.TargetAmount)
	if target == "" {
		return ""
	}
	actual := pyStrip(in.StripeAmount)
	source := pyStrip(in.StripeAmountSource)
	if source == "" {
		source = "未知"
	}
	check := pyStrip(in.AmountCheck)
	status := check
	if check == "passed" {
		status = "通过"
	} else if check == "" {
		status = "完成"
	}
	prefix := ""
	if emailAddr != "" {
		prefix = fmt.Sprintf("[%s] ", emailAddr)
	}
	return fmt.Sprintf("%s金额检查%s: 目标 %s, 实际 %s, 来源 %s", prefix, status, target, actual, source)
}

// ---------------------------------------------------------------------------
// _opll_error_text (app.py:11925-11929)
// ---------------------------------------------------------------------------

// OpllErrorText mirrors _opll_error_text (app.py:11925-11929): an amount
// mismatch gets its stripe source appended, everything else is str(exc).
func (e *PayLinkExtractor) OpllErrorText(err error) string {
	if err == nil {
		return ""
	}
	var mismatch *models.AmountMismatchError
	if errors.As(err, &mismatch) {
		source := mismatch.StripeAmountSource
		if source == "" {
			source = "未知"
		}
		return fmt.Sprintf("%s; 来源: %s", mismatch.Error(), source)
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// _detect_proxy_exit / _proxy_exit_is_japan (app.py:11931-11935)
// ---------------------------------------------------------------------------

// DetectProxyExit mirrors _detect_proxy_exit (app.py:11931-11932): the health
// summary of one proxy URL (timeout 15s, 3 attempts, label "支付链接代理").
// A failed check yields a summary starting with "检测失败", which is what the
// gating below keys on.
func (e *PayLinkExtractor) DetectProxyExit(proxyURL string) string {
	var log proxyhealth.LogFunc
	if e != nil && e.log != nil {
		log = proxyhealth.LogFunc(e.log)
	}
	return proxyhealth.DetectProxyHealthWithRetry(proxyURL, 15, 3, log, "支付链接代理").Summary()
}

// payLinkJapanExitRE mirrors the matcher in _proxy_exit_is_japan
// (app.py:11935). RE2 needs no restructuring here (no backrefs/lookaround), but
// `\s` must be Python's: the summary interpolates ipinfo's city/org strings, and
// an ASCII-only `\s` read " JP " as NOT Japan. Only remaining dialect
// difference: Go's `$` does not also match before a trailing newline, and
// summaries never end in one.
var payLinkJapanExitRE = regexp.MustCompile(`(?:^|` + pyWS + `)JP(?:/|` + pyWS + `|$)`)

// ProxyExitIsJapan mirrors _proxy_exit_is_japan (app.py:11934-11935).
func (e *PayLinkExtractor) ProxyExitIsJapan(proxyExit string) bool {
	return payLinkJapanExitRE.MatchString(proxyExit)
}

// ---------------------------------------------------------------------------
// _detect_link_proxy_exits (app.py:11937-11946)
//   + _detect_link_proxy_exits_concurrently (app.py:4018-4051)
// ---------------------------------------------------------------------------

// payLinkProxyStepLabels mirrors LINK_PROXY_LOG_STEPS (app.py:3979-3983) —
// index 0 = "create", 1 = "followup", 2 = "approve". The order is the log order
// AND the failure-gating order.
var payLinkProxyStepLabels = [3]string{"第一步", "后续", "Approve"}

// payLinkProxyLogPadding mirrors LINK_PROXY_LOG_PADDING (app.py:3984-3988).
var payLinkProxyLogPadding = map[string]string{
	"第一步":     "    ",
	"后续":      "      ",
	"Approve": "   ",
}

func payLinkAlignPadding(label string) string {
	if p, ok := payLinkProxyLogPadding[label]; ok {
		return p
	}
	return " "
}

// payLinkFormatAlignedProxyLog mirrors _format_aligned_proxy_log (app.py:3991-3994).
func payLinkFormatAlignedProxyLog(label, proxyLabel string) string {
	label = pyStrip(label)
	proxyText := pyStrip(proxyLabel)
	if proxyText == "" {
		proxyText = "直连"
	}
	return fmt.Sprintf("代理[%s]%s\t: %s", label, payLinkAlignPadding(label), proxyText)
}

// payLinkFormatAlignedExitLog mirrors _format_aligned_exit_log (app.py:3997-4000).
func payLinkFormatAlignedExitLog(label, proxyExit string) string {
	label = pyStrip(label)
	exitText := pyStrip(proxyExit)
	if exitText == "" {
		exitText = "未记录"
	}
	return fmt.Sprintf("出口[%s]%s\t: %s", label, payLinkAlignPadding(label), exitText)
}

// payLinkLogProxyGroup mirrors _log_link_proxy_group (app.py:4003-4011).
// Kept unexported: it is a module-level helper shared with clusters that have
// not been ported yet, and should move to a shared package when they land.
func payLinkLogProxyGroup(log func(string), create, followup, approve models.ProxyConfig, actionText string) {
	if log == nil {
		return
	}
	prefix := ""
	if trimmed := pyStrip(actionText); trimmed != "" {
		prefix = trimmed + "，"
	}
	for i, proxy := range [3]models.ProxyConfig{create, followup, approve} {
		log(prefix + payLinkFormatAlignedProxyLog(payLinkProxyStepLabels[i], payLinkProxyChainLabel(proxy)))
	}
}

// payLinkProxyExitFailedText mirrors _proxy_exit_failed_text (app.py:4014-4015).
func payLinkProxyExitFailedText(proxyExit string) bool {
	return strings.HasPrefix(pyStrip(proxyExit), "检测失败")
}

// DetectLinkProxyExits mirrors _detect_link_proxy_exits (app.py:11937-11946)
// and the module helper it delegates to, _detect_link_proxy_exits_concurrently
// (app.py:4018-4051): probe the three link proxies CONCURRENTLY (Python used a
// 3-worker ThreadPool), log the aligned exit lines in step order, then gate.
//
// Gating order is deterministic and must stay that way: every failed probe
// raises first (in create/followup/approve order), and only then does the
// Japan requirement apply — and only to the CREATE exit.
//
// The cached_exits parameter of the Python helper is omitted: the paylink call
// site never passes it.
func (e *PayLinkExtractor) DetectLinkProxyExits(createProxyURL, followupProxyURL, approveProxyURL string) (LinkProxyExits, error) {
	urls := [3]string{createProxyURL, followupProxyURL, approveProxyURL}
	var results [3]string

	var wg sync.WaitGroup
	for i := 0; i < len(urls); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Python wrapped future.result() in `except Exception as exc:
			// exits[key] = f"检测失败: {exc}"`. DetectProxyExit returns a
			// "检测失败[...]" summary instead of erroring, so only a panic can
			// reach this branch — it must not take the process down, and the
			// text must still trip payLinkProxyExitFailedText.
			defer func() {
				if r := recover(); r != nil {
					results[i] = fmt.Sprintf("检测失败: %v", r)
				}
			}()
			results[i] = e.DetectProxyExit(urls[i])
		}(i)
	}
	// wg.Wait is the join that the `with ThreadPoolExecutor(...)` block provided;
	// every path below it runs after all three goroutines are done, so none leak.
	wg.Wait()

	exits := LinkProxyExits{Create: results[0], Followup: results[1], Approve: results[2]}
	for i, label := range payLinkProxyStepLabels {
		e.logLine(payLinkFormatAlignedExitLog(label, results[i]))
	}
	for i, label := range payLinkProxyStepLabels {
		if payLinkProxyExitFailedText(results[i]) {
			return exits, models.NewProxyExitCheckError(
				fmt.Sprintf("%s代理出口检测失败，已放弃当前代理组: %s", label, results[i]))
		}
	}
	if e.RequireJapanExtractProxy && !e.ProxyExitIsJapan(exits.Create) {
		return exits, &models.ProxyExitCheckError{
			Msg:    fmt.Sprintf("第一步代理出口不是日本，已放弃当前代理组: %s", exits.Create),
			Status: "代理非日本",
		}
	}
	return exits, nil
}

// ---------------------------------------------------------------------------
// Proxy routing plan — the ASYMMETRY between routed and reported proxies
// ---------------------------------------------------------------------------

// linkProxyPlan bundles the per-attempt proxy values computed at
// app.py:11970-11975 (trial) and 12024-12029 (non-trial). Both blocks are
// identical and both encode the same asymmetry:
//
//	ROUTE url  = chain_url || local_proxy || dynamic_proxy   (followup falls back
//	             to create, approve to followup)
//	REPORTED   = dynamic_proxy || local_proxy                (NO chain_url; same
//	             cascade)
//
// Reporting the chain URL would leak the local relay address into the saved
// account record instead of the proxy the traffic actually exited from.
type linkProxyPlan struct {
	CreateURL   string
	FollowupURL string
	ApproveURL  string

	CreateUsed   string
	FollowupUsed string
	ApproveUsed  string

	CreateLabel   string
	FollowupLabel string
	ApproveLabel  string

	CreateExit   string
	FollowupExit string
	ApproveExit  string
}

func (e *PayLinkExtractor) linkProxyPlan() linkProxyPlan {
	create, followup, approve := e.LinkCreateProxy, e.LinkFollowupProxy, e.LinkApproveProxy

	createURL := firstNonEmpty(create.ChainURL, create.LocalProxy, create.DynamicProxy)
	followupURL := firstNonEmpty(followup.ChainURL, followup.LocalProxy, followup.DynamicProxy, createURL)
	approveURL := firstNonEmpty(approve.ChainURL, approve.LocalProxy, approve.DynamicProxy, followupURL)

	createUsed := firstNonEmpty(create.DynamicProxy, create.LocalProxy)
	followupUsed := firstNonEmpty(followup.DynamicProxy, followup.LocalProxy, createUsed)
	approveUsed := firstNonEmpty(approve.DynamicProxy, approve.LocalProxy, followupUsed)

	return linkProxyPlan{
		CreateURL:     createURL,
		FollowupURL:   followupURL,
		ApproveURL:    approveURL,
		CreateUsed:    createUsed,
		FollowupUsed:  followupUsed,
		ApproveUsed:   approveUsed,
		CreateLabel:   payLinkProxyChainLabel(create),
		FollowupLabel: payLinkProxyChainLabel(followup),
		ApproveLabel:  payLinkProxyChainLabel(approve),
	}
}

func (p *linkProxyPlan) applyExits(exits LinkProxyExits) {
	p.CreateExit = exits.Create
	p.FollowupExit = exits.Followup
	p.ApproveExit = exits.Approve
}

// payLinkProxyCredRE strips inline credentials. Python used the lookbehind
// `(?<=://)[^/@\s]+@`; RE2 has no lookbehind, so the `://` is captured and put
// back by the replacement template instead.
var payLinkProxyCredRE = regexp.MustCompile(`(://)[^/@\s]+@`)

// payLinkMaskProxyURL mirrors mask_proxy_url (app.py:2564-2576).
// Local duplicate of a module-level helper that has no Go home yet; move it to
// a shared package once the proxy/UI clusters land.
func payLinkMaskProxyURL(proxyURL string) string {
	text := pyStrip(proxyURL)
	if text == "" {
		return "直连"
	}
	if parsed, err := url.Parse(text); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		if parsed.User.Username() != "" || password != "" {
			// urlsplit().hostname is LOWERCASED; parsed.Hostname() is not.
			host := strings.ToLower(parsed.Hostname())
			port := ""
			if p := parsed.Port(); p != "" {
				port = ":" + p
			}
			// urlunsplit emits "//"+netloc with no scheme when the scheme is
			// empty; hardcoding "://" produced the malformed "://***@host:8080"
			// for a scheme-relative proxy URL.
			prefix := "//"
			if parsed.Scheme != "" {
				prefix = parsed.Scheme + "://"
			}
			out := prefix + "***@" + host + port + parsed.Path
			if parsed.RawQuery != "" {
				out += "?" + parsed.RawQuery
			}
			if parsed.Fragment != "" {
				out += "#" + parsed.Fragment
			}
			return out
		}
	}
	return payLinkProxyCredRE.ReplaceAllString(text, "${1}***@")
}

// payLinkProxyChainLabel mirrors ProxyConfig.label / format_proxy_chain_label
// (app.py:739-741, 2579-2582). The `{:<30}` pad counts characters, so the Go
// version pads by rune count, not bytes.
func payLinkProxyChainLabel(cfg models.ProxyConfig) string {
	return fmt.Sprintf("本地=%s -> 动态=%s",
		payLinkPadRunes(payLinkMaskProxyURL(cfg.LocalProxy), 30),
		payLinkMaskProxyURL(cfg.DynamicProxy))
}

func payLinkPadRunes(value string, width int) string {
	if n := len([]rune(value)); n < width {
		return value + strings.Repeat(" ", width-n)
	}
	return value
}

// ---------------------------------------------------------------------------
// _extract_pay_link (app.py:11948-12134)
// ---------------------------------------------------------------------------

// ExtractPayLink mirrors _extract_pay_link (app.py:11948-12134): drive the
// configured payment mode until a usable link falls out, retrying transient
// failures under a DUAL cap.
//
// Faithfully preserved:
//   - dual retry cap: at most 15 attempts AND at most 120s elapsed (the elapsed
//     check breaks the loop, it does not raise), 4s between attempts;
//   - a ProxyExitCheckError is re-raised IMMEDIATELY and never retried;
//   - any error whose text says the page/target is gone becomes the fatal
//     "浏览器被关闭，…提取已停止" error (go-rod's session/context-destroyed
//     errors are deliberately mapped onto that branch — see
//     payLinkIsBrowserClosedError);
//   - the per-mode success gate: PayPal/GoPay read provider_redirect_url ||
//     long_url and REQUIRE OpllIsPaypalSuccessURL, Apple Pay reads long_url ||
//     stripe_hosted_url and only requires non-empty, trial reads
//     provider_redirect_url || long_url || url and requires OpllIsPaypalSuccessURL.
func (e *PayLinkExtractor) ExtractPayLink() (*PayLinkResult, error) {
	paymentModeName := e.PaymentMode
	mode, ok := models.PaymentModes[paymentModeName]
	if !ok {
		mode = models.PaymentModes["无卡长链接 US/USD"]
	}
	trialShortLink := mode.TrialShortLink
	applePayHosted := mode.ApplePayHosted
	paymentProvider := strings.ToLower(strings.TrimSpace(orDefault(mode.PaymentProvider, "paypal")))

	linkLabel := "支付长链接"
	switch {
	case trialShortLink:
		linkLabel = "试用短链"
	case applePayHosted:
		linkLabel = "Apple Pay 支付页"
	case paymentProvider == "gopay":
		linkLabel = "GoPay 长链接"
	}
	e.logf("提取%s: %s", linkLabel, paymentModeName)

	if e.pageClosed() {
		return nil, fmt.Errorf("浏览器页面已关闭，无法提取%s", linkLabel)
	}
	// Python's goto sat OUTSIDE the retry try/except: a failure here aborts.
	if err := e.page.Navigate(openai.ChatGPTBaseURL, payLinkNavTimeout); err != nil {
		return nil, err
	}

	lastError := "未知错误"
	started := time.Now()
	for attempt := 1; attempt <= payLinkMaxAttempts; attempt++ {
		if e.pageClosed() {
			return nil, fmt.Errorf("浏览器页面已关闭，无法提取%s", linkLabel)
		}
		if time.Since(started) > payLinkElapsedCap {
			break
		}
		e.logf("正在提取%s (%d/%d)", linkLabel, attempt, payLinkMaxAttempts)

		result, err := e.extractPayLinkOnce(mode, trialShortLink, applePayHosted, paymentProvider)
		if err == nil {
			return result, nil
		}
		lastError = err.Error()

		var exitErr *models.ProxyExitCheckError
		if errors.As(err, &exitErr) {
			return nil, err
		}
		if payLinkIsBrowserClosedError(err) {
			return nil, fmt.Errorf("浏览器被关闭，%s提取已停止", linkLabel)
		}
		e.logf("%s提取失败，准备重试: %s", linkLabel, payLinkTruncate(e.OpllErrorText(err), 180))
		time.Sleep(payLinkRetrySleep)
	}
	return nil, fmt.Errorf("提取%s失败: %s", linkLabel, lastError)
}

// extractPayLinkOnce is the body of one retry attempt (the contents of the
// try block at app.py:11967-12125).
func (e *PayLinkExtractor) extractPayLinkOnce(mode models.PaymentMode, trialShortLink, applePayHosted bool, paymentProvider string) (*PayLinkResult, error) {
	if trialShortLink {
		// app.py:11968-12008
		targetAmount := e.TargetAmountText()
		plan := e.linkProxyPlan()
		payLinkLogProxyGroup(e.log, e.LinkCreateProxy, e.LinkFollowupProxy, e.LinkApproveProxy, "提取试用 PayPal 长链")
		exits, err := e.DetectLinkProxyExits(plan.CreateURL, plan.FollowupURL, plan.ApproveURL)
		if err != nil {
			return nil, err
		}
		plan.applyExits(exits)

		linkResult, err := e.ExtractTrialShortLinkByClick(plan.CreateURL, plan.FollowupURL, plan.ApproveURL, targetAmount)
		if err != nil {
			return nil, err
		}
		longURL := pyStrip(firstNonEmpty(linkResult.ProviderRedirectURL, linkResult.LongURL, linkResult.URL))
		if !opll.OpllIsPaypalSuccessURL(longURL) {
			return nil, fmt.Errorf("试用 checkout 已生成，但没有提取到可用 PayPal approve 长链: %s", payLinkTruncate(longURL, 160))
		}
		if amountLog := e.OpllAmountLogText("", linkResult.AmountFields); amountLog != "" {
			e.logLine(amountLog)
		}
		e.logLine("[支付链接] 试用 PayPal approve 长链已生成，认证浏览器窗口保持打开")
		return e.buildResult(longURL, linkResult.CheckoutURL, linkResult.AccessToken, linkResult.SessionJSON,
			"trial_paypal_approve", plan, linkResult.AmountFields), nil
	}

	// app.py:12009-12021
	accessToken, sessionJSON, err := e.readSessionInfo()
	if err != nil {
		return nil, err
	}
	e.logLine("已提取 ChatGPT session/accessToken")

	country := orDefault(mode.Country, "US")
	currency := orDefault(mode.Currency, models.CurrencyForCountry(country))

	plan := e.linkProxyPlan()
	proxyActionText := ""
	if applePayHosted {
		proxyActionText = "生成 Apple Pay hosted 支付页"
	} else if paymentProvider == "gopay" {
		proxyActionText = "提取 GoPay 长链接"
	}
	payLinkLogProxyGroup(e.log, e.LinkCreateProxy, e.LinkFollowupProxy, e.LinkApproveProxy, proxyActionText)
	exits, err := e.DetectLinkProxyExits(plan.CreateURL, plan.FollowupURL, plan.ApproveURL)
	if err != nil {
		return nil, err
	}
	plan.applyExits(exits)
	// Python read the target amount AFTER the exit checks in this branch and
	// BEFORE them in the trial branch; the order is preserved on both sides.
	targetAmount := e.TargetAmountText()

	switch {
	case applePayHosted:
		// app.py:12037-12065
		linkResult, err := opll.GenerateOpllHostedLongLink(accessToken, country, currency,
			plan.CreateURL, plan.FollowupURL, plan.ApproveURL, targetAmount)
		if err != nil {
			return nil, err
		}
		longURL := pyStrip(firstNonEmpty(linkResult.LongURL, linkResult.StripeHostedURL))
		if longURL == "" {
			return nil, fmt.Errorf("接口生成成功但没有返回 Apple Pay 支付页链接: %s", payLinkResultRepr(linkResult))
		}
		amounts := AmountFieldsFromLinkResult(linkResult)
		if amountLog := e.OpllAmountLogText("", amounts); amountLog != "" {
			e.logLine(amountLog)
		}
		e.logLine("Apple Pay hosted 支付页已生成；请用 Safari/iPhone/Mac 打开并手动付款")
		return e.buildResult(longURL, longURL, accessToken, sessionJSON, "apple_pay_hosted", plan, amounts), nil

	case paymentProvider == "gopay":
		// app.py:12066-12094
		linkResult, err := opll.GenerateOpllGopayLongLink(accessToken, country, currency,
			plan.CreateURL, plan.FollowupURL, plan.ApproveURL, targetAmount)
		if err != nil {
			return nil, err
		}
		longURL := pyStrip(firstNonEmpty(linkResult.ProviderRedirectURL, linkResult.LongURL))
		if longURL == "" {
			return nil, fmt.Errorf("接口提取成功但没有返回 GoPay 长链: %s", payLinkResultRepr(linkResult))
		}
		amounts := AmountFieldsFromLinkResult(linkResult)
		if amountLog := e.OpllAmountLogText("", amounts); amountLog != "" {
			e.logLine(amountLog)
		}
		e.logLine("GoPay 长链接已生成，注册浏览器窗口保持打开")
		return e.buildResult(longURL, longURL, accessToken, sessionJSON, "gopay_redirect", plan, amounts), nil

	default:
		// app.py:12095-12125
		if e.ForceLegacyPayPal {
			e.logLine("[支付链接] 旧版强撞 PayPal 已开启：忽略 init 支付方式列表，继续尝试 PayPal confirm")
		}
		linkResult, err := opll.GenerateOpllPaypalLongLink(accessToken, country, currency,
			plan.CreateURL, plan.FollowupURL, plan.ApproveURL, targetAmount, e.ForceLegacyPayPal)
		if err != nil {
			return nil, err
		}
		longURL := pyStrip(firstNonEmpty(linkResult.ProviderRedirectURL, linkResult.LongURL))
		if !opll.OpllIsPaypalSuccessURL(longURL) {
			return nil, fmt.Errorf("返回的不是可用 PayPal 跳转链接，拒绝保存: %s", payLinkTruncate(longURL, 160))
		}
		amounts := AmountFieldsFromLinkResult(linkResult)
		if amountLog := e.OpllAmountLogText("", amounts); amountLog != "" {
			e.logLine(amountLog)
		}
		e.logLine("[支付链接] PayPal 跳转链接已生成，认证浏览器窗口保持打开")
		return e.buildResult(longURL, longURL, accessToken, sessionJSON, "paypal_approve", plan, amounts), nil
	}
}

// buildResult assembles the common key set of the four _extract_pay_link return
// dicts, including the routed-vs-reported proxy asymmetry.
func (e *PayLinkExtractor) buildResult(linkURL, checkoutURL, accessToken, sessionJSON, paymentLinkType string, plan linkProxyPlan, amounts AmountFields) *PayLinkResult {
	return &PayLinkResult{
		URL:         linkURL,
		CheckoutURL: checkoutURL,
		AccessToken: accessToken,
		SessionJSON: sessionJSON,

		// "link_proxy*" is an alias of the FOLLOWUP stage (app.py:11994-11996).
		LinkProxy:      plan.FollowupUsed,
		LinkProxyLabel: plan.FollowupLabel,
		LinkProxyExit:  plan.FollowupExit,

		LinkCreateProxy:      plan.CreateUsed,
		LinkCreateProxyLabel: plan.CreateLabel,
		LinkCreateProxyExit:  plan.CreateExit,

		LinkFollowupProxy:      plan.FollowupUsed,
		LinkFollowupProxyLabel: plan.FollowupLabel,
		LinkFollowupProxyExit:  plan.FollowupExit,

		LinkApproveProxy:      plan.ApproveUsed,
		LinkApproveProxyLabel: plan.ApproveLabel,
		LinkApproveProxyExit:  plan.ApproveExit,

		PaymentLinkType: paymentLinkType,
		AmountFields:    e.OpllAmountFields(amounts),
	}
}

// payLinkIsBrowserClosedError decides whether an attempt failure means "the
// user closed the browser" — app.py:12130 tested
// `"Target page" in str(exc) or "closed" in str(exc).lower()`, which matched
// Playwright's "Target page, context or browser has been closed".
//
// go-rod reports the same condition through typed CDP errors instead of that
// sentence, so those are mapped onto the same branch DELIBERATELY: without it a
// closed browser would be retried 15 times / 120s instead of aborting.
func payLinkIsBrowserClosedError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	if strings.Contains(text, "Target page") {
		return true
	}
	if strings.Contains(strings.ToLower(text), "closed") {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	var closeCanceled *rod.PageCloseCanceledError
	if errors.As(err, &closeCanceled) {
		return true
	}
	var pageNotFound *rod.PageNotFoundError
	if errors.As(err, &pageNotFound) {
		return true
	}
	var cdpErr *cdp.Error
	if errors.As(err, &cdpErr) {
		switch {
		case strings.Contains(cdpErr.Message, "Session with given id not found"),
			strings.Contains(cdpErr.Message, "Cannot find context with specified id"),
			strings.Contains(cdpErr.Message, "Execution context was destroyed"),
			strings.Contains(cdpErr.Message, "Inspected target navigated or closed"),
			strings.Contains(cdpErr.Message, "Not attached to an active page"):
			return true
		}
	}
	return false
}

// payLinkResultRepr stands in for Python's `f"...: {link_result}"` dict repr in
// the two "接口…成功但没有返回…" errors. The rendering differs (compact JSON vs
// a Python dict literal); the diagnostic content is the same.
func payLinkResultRepr(r *opll.LinkResult) string {
	if r == nil {
		return "{}"
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// session / accessToken extraction
// ---------------------------------------------------------------------------

// payLinkSessionProbeJS is the session fetch of app.py:12010-12016 (identical
// copy at 12245-12251). The Python version threw from inside the page; this
// version returns a status object so the Go side can rebuild the two error
// messages verbatim instead of digging them out of a wrapped JS stack.
const payLinkSessionProbeJS = `async () => {
    const sessionResp = await fetch('/api/auth/session', { credentials: 'include' });
    if (!sessionResp.ok) return { ok: false, status: sessionResp.status };
    const session = await sessionResp.json();
    if (!session.accessToken) return { ok: false, session };
    return { ok: true, accessToken: session.accessToken, session };
}`

// readSessionInfo runs payLinkSessionProbeJS and applies the Python-side checks
// (app.py:12009-12020 / 12244-12252): "Session 请求失败: HTTP <code>" when the
// endpoint is not ok, "无法获取 accessToken，请确认已登录" when the token is
// missing. sessionJSON is json.dumps(session, ensure_ascii=False, indent=2).
func (e *PayLinkExtractor) readSessionInfo() (string, string, error) {
	if e.page == nil || e.page.Rod == nil {
		return "", "", errors.New("浏览器页面已关闭")
	}
	v, err := e.page.Rod.Eval(payLinkSessionProbeJS)
	if err != nil {
		return "", "", err
	}
	if v == nil || v.Value.Nil() {
		return "", "", errors.New("无法获取 accessToken，请确认已登录")
	}
	if !v.Value.Get("ok").Bool() {
		if status := v.Value.Get("status"); !status.Nil() {
			return "", "", fmt.Errorf("Session 请求失败: HTTP %d", status.Int())
		}
		return "", "", errors.New("无法获取 accessToken，请确认已登录")
	}
	accessToken := v.Value.Get("accessToken").Str()
	if accessToken == "" {
		return "", "", errors.New("无法获取 accessToken，请确认已登录")
	}
	return accessToken, payLinkDumpJSON(v.Value.Get("session").Val()), nil
}

// payLinkDumpJSON is json.dumps(value or {}, ensure_ascii=False, indent=2).
// SetEscapeHTML(false) keeps `&`, `<` and `>` literal like Python does.
//
// One unavoidable difference: Python preserves the server's key order, Go sorts
// map keys. The blob is stored/displayed, never parsed by key order.
func payLinkDumpJSON(value any) string {
	if value == nil {
		value = map[string]any{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return "{}"
	}
	return strings.TrimRight(buf.String(), "\n")
}

// payLinkDumpCompactJSON is json.dumps(value, ensure_ascii=False) (no indent).
func payLinkDumpCompactJSON(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// ---------------------------------------------------------------------------
// _extract_session_info (app.py:12136-12158)
// ---------------------------------------------------------------------------

// ExtractSessionInfo mirrors _extract_session_info (app.py:12136-12158): open a
// fresh tab on the raw session endpoint, parse the body as JSON, and pair it
// with the exported storage state.
//
// Two faithfully-preserved quirks: the tab is NEVER closed (Python leaked it —
// see the report), and the window-lowering hook fires in a deferred block
// whatever happens. storage_state() has no go-rod equivalent, so
// browser.ExportStorageState (cookies + localStorage) stands in for it.
func (e *PayLinkExtractor) ExtractSessionInfo() (*SessionInfo, error) {
	if e.browser == nil {
		return nil, errors.New("浏览器已关闭")
	}
	defer func() {
		if e.LowerWindows != nil {
			e.LowerWindows(10)
		}
	}()

	page, err := e.browser.NewPage()
	if err != nil {
		return nil, err
	}
	if err := page.Navigate(sessionEndpointURL, payLinkNavTimeout); err != nil {
		return nil, err
	}
	raw, err := payLinkBodyInnerText(page, payLinkBodyTextTimeout)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(raw)

	var session any
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		return nil, fmt.Errorf("Session 接口返回不是有效 JSON: %s", payLinkTruncate(body, 300))
	}
	accessToken := ""
	if obj, ok := session.(map[string]any); ok {
		accessToken = payLinkAsString(obj["accessToken"])
	}
	if accessToken == "" {
		e.logLine("[Session] Session JSON 已获取，但未发现 accessToken")
	} else {
		e.logLine("[Session] Session JSON 和 Access Token 已获取")
	}

	state, err := e.browser.ExportStorageState()
	if err != nil {
		return nil, err
	}
	stateJSON, err := payLinkDumpCompactJSON(state)
	if err != nil {
		return nil, err
	}
	return &SessionInfo{
		URL:              "",
		AccessToken:      accessToken,
		SessionJSON:      payLinkDumpJSON(session),
		StorageStateJSON: stateJSON,
	}, nil
}

// payLinkBodyInnerText is page.locator("body").inner_text(timeout=...). Unlike
// the other clusters' variants this returns the error, because the Python call
// site let the timeout propagate instead of swallowing it.
func payLinkBodyInnerText(p *browser.Page, timeout time.Duration) (string, error) {
	if p == nil || p.Rod == nil {
		return "", errors.New("浏览器页面已关闭")
	}
	v, err := p.Rod.Timeout(timeout).Eval(`() => (document.body && document.body.innerText) || ''`)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return v.Value.Str(), nil
}

// ---------------------------------------------------------------------------
// _click_trial_claim_button (app.py:12160-12161)
//   -> click_trial_claim_button_on_page (app.py:8755-8843)
// ---------------------------------------------------------------------------

// ClickTrialClaimButton mirrors _click_trial_claim_button (app.py:12160-12161):
// a pure delegation to the module-level clicker.
//
// The method body at app.py:12162-12241 is UNREACHABLE (the `return` on 12161
// precedes it) and is NOT ported. Its live behaviour differs from what runs:
// the dead block polls for 300s at 1s intervals and clicks via
// get_by_text("Claim free offer") / element.click(), while the code that
// actually executes polls for 90s at 0.8s intervals, locates by accessible-name
// regex ladder and clicks by real mouse coordinates with force=True.
func (e *PayLinkExtractor) ClickTrialClaimButton(beforeClick func()) bool {
	return ClickTrialClaimButtonOnPage(e.page, beforeClick, e.log, e.TrialClaimScoreFallback)
}

// trialClaimNamePatterns mirrors text_patterns (app.py:8772-8780) — the
// get_by_role("button", name=<regex>) priority ladder, in order. The strings are
// both the JS RegExp source and (verbatim, as `pattern.pattern`) the text logged
// on a hit, so they must not be reformatted.
var trialClaimNamePatterns = []string{
	`Claim\s*free\s*offer`,
	`Claim\s*Plus`,
	`Get\s*Plus`,
	`Start.*trial`,
	`free.*trial`,
	`领取\s*Plus|Plus\s*免费|免费优惠`,
	`無料.*Plus|Plus.*無料`,
}

// trialClaimRoleButtonJS replaces Playwright's
// `page.get_by_role("button", name=<regex>).first` +
// `scroll_into_view_if_needed()` (app.py:8785-8786), which go-rod has no
// equivalent for. It walks the document in order, keeps elements whose computed
// role is "button" and that are visible+enabled (Playwright's role engine skips
// hidden nodes, and force-clicking a hidden node would still have thrown on the
// scroll step), matches the whitespace-normalized accessible name against the
// pattern with the 'i' flag (= re.I), then scrolls the winner into view and
// returns its centre so the caller can do a real mouse click.
const trialClaimRoleButtonJS = `(source) => {
    const re = new RegExp(source, 'i');
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.display !== 'none' && s.visibility !== 'hidden';
    };
    const roleOf = el => {
        const explicit = (el.getAttribute('role') || '').trim().toLowerCase();
        if (explicit) return explicit;
        const tag = el.tagName.toLowerCase();
        if (tag === 'button' || tag === 'summary') return 'button';
        if (tag === 'input') {
            const type = (el.getAttribute('type') || '').toLowerCase();
            return (type === 'button' || type === 'submit' || type === 'reset' || type === 'image') ? 'button' : '';
        }
        return '';
    };
    const nameOf = el => {
        const labelled = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
            .map(id => { const node = document.getElementById(id); return node ? (node.textContent || '') : ''; })
            .join(' ').trim();
        const aria = (el.getAttribute('aria-label') || '').trim();
        const text = (el.textContent || '').trim();
        const value = el.tagName.toLowerCase() === 'input' ? (el.getAttribute('value') || '').trim() : '';
        const title = (el.getAttribute('title') || '').trim();
        return (labelled || aria || text || value || title).replace(/\s+/g, ' ').trim();
    };
    const nodes = Array.from(document.querySelectorAll('button, summary, input, [role]'));
    for (const el of nodes) {
        if (roleOf(el) !== 'button') continue;
        // NO disabled filter on purpose: Python clicks this rung with
        // click(force=True) (app.py:8788), which bypasses Playwright's
        // actionability checks INCLUDING "enabled", and get_by_role does not skip
        // disabled nodes either. A greyed-out "Claim free offer" is clicked there,
        // so skipping it here would silently fall through to the scoring pass.
        // (The phone cluster's ladder DOES filter disabled — that one is a plain
        // click() whose actionability wait makes Python skip the rung.)
        if (!visible(el)) continue;
        const name = nameOf(el);
        if (!re.test(name)) continue;
        el.scrollIntoView({ block: 'center', inline: 'center' });
        const r = el.getBoundingClientRect();
        return { ok: true, text: name, x: r.left + r.width / 2, y: r.top + r.height / 2 };
    }
    return { ok: false };
}`

// trialClaimScoreJS is the scoring fallback of click_trial_claim_button_on_page
// (app.py:8794-8829), copied verbatim (only re-indented).
//
// PYTHON BUG, corrected here: the Python source embeds this JS in a NON-raw
// triple-quoted string, so `\t\n\r` inside `[\t\n\r ]*` are interpolated into
// real TAB/LF/CR characters. A line terminator inside a JS regex literal is a
// SyntaxError, so in Python this whole function fails to parse on every sweep
// and the fallback never runs (each attempt just logs "点击领取按钮尝试失败").
// A Go raw string passes the escapes through unchanged, so the fallback works
// as it was written to.
const trialClaimScoreJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.display !== 'none' && s.visibility !== 'hidden';
    };
    const enabled = el => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
    const clickableFor = el => el?.closest?.('button, a, [role="button"], [onclick], [tabindex]') || el;
    const candidates = Array.from(new Set([
        ...Array.from(document.querySelectorAll('button, a, [role="button"], [onclick], [tabindex]')),
        ...Array.from(document.querySelectorAll('body *')).map(clickableFor),
    ])).filter(el => visible(el) && enabled(el));
    const score = el => {
        const text = ` + "`${el.textContent || ''} ${el.getAttribute('aria-label') || ''} ${el.getAttribute('data-testid') || ''}`" + `.trim();
        if (/Claim[\t\n\r ]*free[\t\n\r ]*offer|领取[\t\n\r ]*Plus|Plus[\t\n\r ]*免费|免费优惠|無料.*Plus|Plus.*無料|Claim[\t\n\r ]*Plus|Get[\t\n\r ]*Plus|Start.*trial|free.*trial|Try[\t\n\r ]*Plus/i.test(text)) return 10;
        if (/Plus/i.test(text) && /free|trial|claim|get|start|upgrade|subscribe|continue|领取|免费|無料|続行|開始|アップグレード/i.test(text)) return 8;
        if (/claim|get|start|upgrade|subscribe|continue|领取|免费|無料|続行|開始|購入|登録/i.test(text)) return 3;
        return 0;
    };
    const item = candidates
        .map(el => ({ el, score: score(el) }))
        .filter(item => item.score > 0)
        .sort((a, b) => b.score - a.score)[0];
    if (!item) {
        return { ok: false, candidates: candidates.slice(0, 8).map(el => ` + "`${el.textContent || ''} ${el.getAttribute('aria-label') || ''}`" + `.trim()).filter(Boolean) };
    }
    item.el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = item.el.getBoundingClientRect();
    return {
        ok: true,
        text: ` + "`${item.el.textContent || ''} ${item.el.getAttribute('aria-label') || ''}`" + `.trim(),
        x: r.left + r.width / 2,
        y: r.top + r.height / 2,
    };
}`

// ClickTrialClaimButtonOnPage mirrors click_trial_claim_button_on_page
// (app.py:8755-8843): for up to 90 seconds, sweep the accessible-name ladder
// first and the DOM-scoring fallback second, clicking the winner with a real
// mouse click; sleep 0.8s between sweeps.
//
// beforeClick fires at most once, immediately before the first click attempt
// (Python's `before_clicked` latch), even if that attempt then fails. log may
// be nil.
// scoreFallback enables the DOM-scoring pass. It defaults to false because the
// equivalent Python pass is dead code (see trialClaimScoreJS) — every production
// run to date has claimed the trial via the role-button ladder alone, and the
// lowest scoring tier awards 3 points to any visible element whose text merely
// matches /claim|get|start|upgrade|subscribe|continue|领取|免费/. On the pricing
// page that can be several buttons, and a wrong click here lands on a different
// plan's checkout rather than failing cleanly.
func ClickTrialClaimButtonOnPage(p *browser.Page, beforeClick func(), log func(string), scoreFallback bool) bool {
	if p == nil || p.Rod == nil {
		return false
	}
	deadline := time.Now().Add(trialClaimDeadline)
	beforeClicked := false
	runBeforeClick := func() {
		if beforeClick != nil && !beforeClicked {
			beforeClick()
			beforeClicked = true
		}
	}
	writeLog := func(message string) {
		if log == nil {
			return
		}
		// Python's write_log swallowed any exception from the log callback.
		defer func() { _ = recover() }()
		log(message)
	}

	for time.Now().Before(deadline) {
		for _, pattern := range trialClaimNamePatterns {
			// Python wrapped every pattern in its own `except Exception: pass`,
			// so a miss or an error just advances to the next pattern.
			v, err := p.Rod.Timeout(trialClaimLocateTimeout).Eval(trialClaimRoleButtonJS, pattern)
			if err != nil || v == nil || v.Value.Nil() || !v.Value.Get("ok").Bool() {
				continue
			}
			runBeforeClick()
			if !phoneClickPoint(p, v.Value.Get("x").Num(), v.Value.Get("y").Num()) {
				// Playwright raised here and fell into the same `pass`; the
				// error text was Playwright-specific and is not reconstructed.
				continue
			}
			writeLog(fmt.Sprintf("已通过按钮文本点击领取按钮: %s", pattern))
			return true
		}

		v, err := p.Rod.Timeout(trialClaimScanTimeout).Eval(trialClaimScoreJS)
		if err != nil {
			writeLog(fmt.Sprintf("点击领取按钮尝试失败: %s", payLinkTruncate(err.Error(), 120)))
		} else if v != nil && !v.Value.Nil() {
			scored := v.Value.Get("ok").Bool()
			if scored && scoreFallback {
				runBeforeClick()
				if phoneClickPoint(p, v.Value.Get("x").Num(), v.Value.Get("y").Num()) {
					writeLog(fmt.Sprintf("已通过坐标点击领取按钮: %s", payLinkTruncate(v.Value.Get("text").Str(), 80)))
					return true
				}
			} else if scored {
				// Report the would-be target so the operator can decide whether to
				// turn the fallback on, instead of it silently doing nothing.
				writeLog(fmt.Sprintf("评分兜底命中但未启用（默认关闭），跳过点击: %s",
					payLinkTruncate(v.Value.Get("text").Str(), 80)))
			} else if candidates := v.Value.Get("candidates").Arr(); len(candidates) > 0 {
				texts := make([]string, 0, 5)
				for i, candidate := range candidates {
					if i >= 5 {
						break
					}
					texts = append(texts, payLinkTruncate(candidate.Str(), 40))
				}
				writeLog(fmt.Sprintf("暂未命中领取按钮，页面候选: %s", strings.Join(texts, ", ")))
			}
		}
		time.Sleep(trialClaimPoll)
	}
	return false
}

// ---------------------------------------------------------------------------
// _extract_trial_short_link_by_click (app.py:12243-12296)
// ---------------------------------------------------------------------------

// ExtractTrialShortLinkByClick mirrors _extract_trial_short_link_by_click
// (app.py:12243-12296): claim the promo in the real browser, then race the
// redirect for 60 seconds and branch on where the page landed.
//
// Three outcomes, exactly as in Python:
//   - paypal.com — the promo went straight to PayPal. The browser URL IS the
//     link: returned with NO HTTP call at all, and amount_check is
//     "skipped" (no target amount) or "unknown" (target amount unverifiable).
//   - pay.openai.com / checkout.stripe.com — rebuild the checkout from the URL
//     and continue the PayPal pipeline over the followup/approve proxies.
//   - neither within 60s — error naming the current URL.
//
// createProxyURL is accepted for signature parity and, exactly as in Python,
// never used: the checkout already exists, so only the followup and approve
// stages have any work left.
//
// The country/currency handed to OpllCheckoutFromURL are HARD-CODED "US"/"USD"
// regardless of the configured payment mode — preserved verbatim, because the
// promo checkout is a US/USD one no matter which mode selected the trial.
func (e *PayLinkExtractor) ExtractTrialShortLinkByClick(createProxyURL, followupProxyURL, approveProxyURL, targetAmount string) (*TrialLinkResult, error) {
	_ = createProxyURL

	accessToken, sessionJSON, err := e.readSessionInfo()
	if err != nil {
		return nil, err
	}
	e.logLine("已提取 ChatGPT session/accessToken")

	if err := e.page.Navigate(trialPricingURL, payLinkNavTimeout); err != nil {
		return nil, err
	}
	e.logLine("已打开试用页面，准备点击领取按钮")

	if !e.ClickTrialClaimButton(nil) {
		return nil, errors.New("试用页面未找到领取 Plus 免费优惠按钮")
	}
	e.logLine("已点击领取按钮，等待跳转到试用 checkout")

	started := time.Now()
	for time.Since(started) < trialRedirectWindow {
		if e.pageClosed() {
			return nil, errors.New("浏览器页面已关闭，无法等待试用短链跳转")
		}
		currentURL := e.page.URL()

		if strings.Contains(currentURL, "paypal.com") {
			e.logLine("试用页面已直接跳转到 PayPal")
			amountCheck := "skipped"
			if pyStrip(targetAmount) != "" {
				amountCheck = "unknown"
			}
			return &TrialLinkResult{
				URL:                 currentURL,
				LongURL:             currentURL,
				ProviderRedirectURL: currentURL,
				CheckoutURL:         currentURL,
				AccessToken:         accessToken,
				SessionJSON:         sessionJSON,
				AmountFields: AmountFields{
					TargetAmount: targetAmount,
					AmountCheck:  amountCheck,
				},
			}, nil
		}

		if strings.Contains(currentURL, "pay.openai.com") || strings.Contains(currentURL, "checkout.stripe.com") {
			e.logLine("试用 checkout 已通过页面点击生成，继续提取 PayPal approve 长链")
			checkout, err := opll.OpllCheckoutFromURL(currentURL, "US", "USD")
			if err != nil {
				return nil, err
			}
			result, err := opll.GenerateOpllPaypalLongLinkFromCheckout(accessToken, checkout,
				followupProxyURL, approveProxyURL, targetAmount, e.ForceLegacyPayPal)
			if err != nil {
				return nil, err
			}
			return &TrialLinkResult{
				URL:                 firstNonEmpty(result.ProviderRedirectURL, result.LongURL),
				LongURL:             result.LongURL,
				ProviderRedirectURL: result.ProviderRedirectURL,
				CheckoutURL:         currentURL,
				AccessToken:         accessToken,
				SessionJSON:         sessionJSON,
				AmountFields:        AmountFieldsFromLinkResult(result),
			}, nil
		}

		time.Sleep(trialRedirectPoll)
	}
	return nil, fmt.Errorf("点击试用按钮后 60 秒内未跳转到支付页，当前 URL: %s", e.page.URL())
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// payLinkTruncate slices by characters (Python str[:n]), never bytes, so the
// Chinese log strings cannot be cut mid-rune.
func payLinkTruncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// payLinkAsString mirrors Python's `str(value or "")` for a decoded JSON value.
func payLinkAsString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
