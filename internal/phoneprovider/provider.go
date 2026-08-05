package phoneprovider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/openai"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// LogFunc is `self._emit_log(msg, email_addr)` — the account-scoped logger.
type LogFunc func(email, msg string)

// SMSClient is the narrow slice of *smsbower.Client this package uses. It is an
// interface for ONE reason: every GetNumber call rents a billable number, so the
// tests must be able to fake it. Never widen this to the concrete client.
type SMSClient interface {
	GetNumber(ctx context.Context, service, country, maxPrice string) (smsbower.Number, error)
	GetPriceQuote(ctx context.Context, service, country string) (smsbower.PriceQuote, error)
	GetPriceTiers(ctx context.Context, service, country string) ([]smsbower.PriceTier, error)
	SetStatus(ctx context.Context, activationID string, status int) (string, error)
	WaitForCode(ctx context.Context, activationID string, timeout, interval int) (string, error)
}

var _ SMSClient = (*smsbower.Client)(nil)

// ClientFactory builds a client for one call. Python constructed a fresh
// SMSBowerClient(api_key) at every use site (app.py:16428, 16530, 16590) so the
// current key text always won; the factory keeps that property.
type ClientFactory func(apiKey string) (SMSClient, error)

// DefaultClientFactory uses the package defaults (no proxy — Python's
// SMSBowerClient went out over the plain requests session).
func DefaultClientFactory(apiKey string) (SMSClient, error) {
	client, err := smsbower.NewClient(apiKey, "", "")
	if err != nil {
		return nil, err
	}
	return client, nil
}

const (
	// smsbowerCodeTimeout / smsbowerCodeInterval: wait_for_code(activation_id,
	// timeout=180) with the client's default interval=5 (app.py:16590, 3853).
	smsbowerCodeTimeout  = 180
	smsbowerCodeInterval = 5
	// manualCodeTimeout: _wait_for_phone_code(..., timeout=120) — note the
	// function's own default is 180; the "code" action overrides it (app.py:16591).
	manualCodeTimeout = 120
	// tierRetryDelay: time.sleep(1.5) before retrying the same price tier
	// (app.py:16478).
	tierRetryDelay = 1500 * time.Millisecond
	// manualPollInterval: time.sleep(min(5, remaining)) (app.py:16666).
	manualPollInterval = 5 * time.Second
)

// noNumbersMarkers are the substrings that mean "this price tier is empty, try
// the next one" rather than "the rental failed" (app.py:16473).
var noNumbersMarkers = []string{"NO_NUMBERS", "MAX_PRICE_EXCEEDED", "没有可用号码", "超过最高限价"}

// transientBadStatuses are the classifier outputs that mean the SMS *network*
// wobbled rather than the number being bad (app.py:16608).
var transientBadStatuses = map[string]bool{"接码网络抖动": true, "网络抖动": true}

// Config wires the provider. Settings is required; Pool may be nil for an
// SMSBower-only provider (the account-bound and manual-pool branches then just
// find nothing, exactly like an empty self.phones/self.accounts).
type Config struct {
	Settings SettingsSource
	Pool     Pool
	Log      LogFunc

	// NewClient defaults to DefaultClientFactory.
	NewClient ClientFactory
	// HTTPGet defaults to defaultHTTPGet; it backs the manual pool's SMS-URL poll.
	HTTPGet HTTPGetFunc
	// Context aborts the blocking waits. Python had no cancellation here at all,
	// so a nil Context means context.Background() and the behaviour is identical.
	Context context.Context
	// Sleep defaults to time.Sleep; it is a seam so tests do not really wait.
	Sleep func(time.Duration)
}

// Provider implements worker.PhoneProvider.
type Provider struct {
	cfg Config

	// mu guards attempts only. Python used the single self.phone_lock for both
	// the counters and the phone/account pools, but never held it across the two
	// (the account scan block is exited before _smsbower_next_phone runs), so
	// splitting the lock is behaviour-preserving and removes any chance of
	// self-deadlock through the Pool interface.
	mu       sync.Mutex
	attempts map[string]int
}

var _ worker.PhoneProvider = (*Provider)(nil)

func New(cfg Config) *Provider {
	if cfg.NewClient == nil {
		cfg.NewClient = DefaultClientFactory
	}
	if cfg.HTTPGet == nil {
		cfg.HTTPGet = defaultHTTPGet
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Provider{cfg: cfg, attempts: map[string]int{}}
}

func (p *Provider) context() context.Context {
	if p.cfg.Context == nil {
		return context.Background()
	}
	return p.cfg.Context
}

func (p *Provider) logf(email, format string, args ...any) {
	if p.cfg.Log == nil {
		return
	}
	if len(args) == 0 {
		p.cfg.Log(email, format)
		return
	}
	p.cfg.Log(email, fmt.Sprintf(format, args...))
}

func (p *Provider) receiveLimit() int {
	if p.cfg.Settings == nil {
		return 0
	}
	return p.cfg.Settings.PhoneReceiveLimit()
}

// attemptKey is `str(email_addr or "").strip().casefold()` (app.py:16420).
// Go has no casefold; ToLower differs only for full-fold pairs (ß→ss, ﬁ→fi)
// that cannot appear in a validated address, and using ToLower keeps this key
// consistent with the .lower() account matching in Pool.
func attemptKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// AttemptCount reports how many numbers SMSBower has rented for this address in
// the current run (the "本账号第 N 次" counter).
func (p *Provider) AttemptCount(email string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts[attemptKey(email)]
}

// ResetAttempts clears every counter, as the task start does (app.py:15195-15196).
func (p *Provider) ResetAttempts() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = map[string]int{}
}

// ---------------------------------------------------------------------------
// _phone_provider dispatch (app.py:16535-16637)
// ---------------------------------------------------------------------------

// Next is action "next" (app.py:16536-16582).
//
// It never returns an error: the Python branch could not raise (every SMSBower
// failure is swallowed into `{}` by _smsbower_next_phone), and worker treats a
// non-nil error as a hard abort of the whole registration.
func (p *Provider) Next(email string, opts map[string]string) (map[string]string, error) {
	// `str((payload or {}).get("country") or "").upper()` — no strip, so a padded
	// value simply fails the == "US" tests below, same as Python.
	requested := strings.ToUpper(opts["country"])

	if p.cfg.Pool != nil {
		lookup := p.cfg.Pool.AccountAuthPhone(email)
		if lookup.Found && p.authPhoneUsable(email, requested, lookup) {
			p.logf(email, "使用导入授权手机号: %s", lookup.Number)
			return map[string]string{
				"number":  lookup.Number,
				"sms_url": lookup.SMSURL,
				// Python put the bool True here; the map is string-typed, and the
				// only consumer is `bool(payload.get("account_bound"))`, which is
				// true for ANY non-empty string — so presence, not the text, is the
				// signal (see accountBound).
				"account_bound": "true",
			}, nil
		}
	}

	if phone := p.smsbowerNextPhone(email, requested); len(phone) > 0 {
		return phone, nil
	}

	if p.cfg.Pool != nil {
		if phone, ok := p.cfg.Pool.ReserveNext(email, requested, p.receiveLimit()); ok {
			p.logf(email, "使用手机号: %s", phone.Number)
			return map[string]string{"number": phone.Number, "sms_url": phone.SMSURL}, nil
		}
	}
	return nil, nil
}

// authPhoneUsable applies the two skip rules of app.py:16543-16555.
func (p *Provider) authPhoneUsable(email, requested string, lookup AuthPhoneLookup) bool {
	if requested == "US" && !strings.HasPrefix(lookup.Number, "+1") {
		return false
	}
	if lookup.SavedOK && blockedStatus(lookup.Saved.Status) {
		p.logf(email, "导入授权手机号已标记%s，自动跳过: %s %s",
			lookup.Saved.Status, lookup.Number, lookup.Saved.LastError)
		return false
	}
	return true
}

// Sent is action "sent" (app.py:16592-16595): SMSBower status 1 = "the number
// was accepted and an SMS was requested". worker calls this ONLY after OpenAI
// actually rendered the code form; firing it earlier bills a dead number.
//
// It always returns nil because _smsbower_set_activation_status swallows every
// failure (app.py:16532) — a nil-safe no-op path matters, since worker aborts
// the attempt on a non-nil error.
func (p *Provider) Sent(email string, phone map[string]string) error {
	if isSMSBower(phone) {
		p.setActivationStatus(email, phone, smsbower.StatusReadyToReceive, "标记短信已发送")
	}
	return nil
}

// Code is action "code" (app.py:16583-16591).
func (p *Provider) Code(email string, phone map[string]string) (string, error) {
	if isSMSBower(phone) {
		// The RAW key var, not the validated settings (app.py:16586-16587).
		apiKey := ""
		if p.cfg.Settings != nil {
			apiKey = p.cfg.Settings.SMSBowerAPIKey()
		}
		if apiKey == "" {
			return "", &smsbower.Error{Msg: "SMSBower API Key 为空，无法查询验证码"}
		}
		client, err := p.cfg.NewClient(apiKey)
		if err != nil {
			return "", err
		}
		// Not stripped, matching `str(payload.get("activation_id") or "")`.
		return client.WaitForCode(p.context(), phone["activation_id"], smsbowerCodeTimeout, smsbowerCodeInterval)
	}
	return p.waitForPhoneCode(phone["number"], phone["sms_url"], manualCodeTimeout)
}

// Good is action "good" (app.py:16596-16601): SMSBower status 6 = finish, i.e.
// "the code worked, charge the rental". Only reachable after the code was
// submitted and accepted.
func (p *Provider) Good(email string, phone map[string]string) error {
	if isSMSBower(phone) {
		p.setActivationStatus(email, phone, smsbower.StatusFinish, "完成")
		// The per-email attempt counter is dropped only on success, so the "第 N
		// 次" log counts the numbers burned on the CURRENT registration.
		p.mu.Lock()
		delete(p.attempts, attemptKey(email))
		p.mu.Unlock()
	}
	return nil
}

// Bad is action "bad" (app.py:16602-16636). For SMSBower both branches send
// status 8 = cancel — the difference is only the label and the log line, because
// a network wobble is not the number's fault but the activation still has to be
// released before the next rental.
func (p *Provider) Bad(email string, phone map[string]string) error {
	number := phone["number"]
	errText := phone["error"]
	status := phone["status"]
	if status == "" {
		status = defaultBadStatus
	}

	if isSMSBower(phone) {
		if smsbower.IsTransientError(errText) || transientBadStatuses[status] {
			p.setActivationStatus(email, phone, smsbower.StatusCancel, "因网络抖动取消")
			p.logf(email, "SMSBower 网络抖动，已取消当前激活并换号 [%s]: %s %s", status, number, errText)
		} else {
			p.setActivationStatus(email, phone, smsbower.StatusCancel, "取消")
			p.logf(email, "SMSBower 号码已标记不可用 [%s]: %s %s", status, number, errText)
		}
		return nil
	}

	if p.cfg.Pool == nil {
		return nil
	}
	if accountBound(phone) {
		p.cfg.Pool.MarkUnusable(number, phone["sms_url"], status, errText, true)
		p.logf(email, "导入授权手机号已标记不可用 [%s]: %s %s", status, number, errText)
		return nil
	}
	p.cfg.Pool.MarkUnusable(number, "", status, errText, false)
	return nil
}

func isSMSBower(phone map[string]string) bool { return phone["provider"] == "smsbower" }

// accountBound is `bool(payload.get("account_bound"))` (app.py:16615). Python
// truthiness on a non-empty string is True — even for "false" — so anything but
// the empty/absent value counts.
func accountBound(phone map[string]string) bool { return phone["account_bound"] != "" }

// ---------------------------------------------------------------------------
// _smsbower_set_activation_status (app.py:16523-16533)
// ---------------------------------------------------------------------------

// setActivationStatus posts the activation status and never propagates a
// failure. status is one of smsbower.Status* — 1 sent / 6 finish / 8 cancel.
func (p *Provider) setActivationStatus(email string, payload map[string]string, status int, label string) {
	activationID := strings.TrimSpace(payload["activation_id"])
	apiKey := ""
	if p.cfg.Settings != nil {
		apiKey = p.cfg.Settings.SMSBowerAPIKey()
	}
	// No id or no key: silently do nothing (app.py:16527-16528). This is also the
	// manual-pool path's exit, which is why Sent/Good/Bad are safe to call for a
	// non-SMSBower phone.
	if activationID == "" || apiKey == "" {
		return
	}
	client, err := p.cfg.NewClient(apiKey)
	if err != nil {
		// SMSBowerClient(api_key) itself raising lands in the same except clause.
		p.logf(email, "SMSBower 激活%s状态回传失败: %s %v", label, activationID, err)
		return
	}
	result, err := client.SetStatus(p.context(), activationID, status)
	if err != nil {
		p.logf(email, "SMSBower 激活%s状态回传失败: %s %v", label, activationID, err)
		return
	}
	p.logf(email, "SMSBower 激活已%s: %s (%s)", label, activationID, result)
}

// ---------------------------------------------------------------------------
// _smsbower_next_phone (app.py:16408-16521)
// ---------------------------------------------------------------------------

// smsbowerNextPhone rents a number. Returns nil (Python's `{}`) for EVERY
// failure so the caller falls back to the manual pool.
//
// MONEY: the attempt counter is incremented before the rental and rolled back on
// failure, so it counts numbers actually rented for this address.
func (p *Provider) smsbowerNextPhone(email, requestedCountry string) map[string]string {
	src := p.cfg.Settings
	if src == nil || !src.SMSBowerEnabled() {
		return nil
	}
	settings, err := src.SMSBowerSettings()
	if err != nil {
		p.logf(email, "SMSBower 设置无效，改用手工手机号池: %v", err)
		return nil
	}
	if settings.APIKey == "" {
		p.logf(email, "SMSBower 已启用但 API Key 为空，改用手工手机号池")
		return nil
	}

	key := attemptKey(email)
	p.mu.Lock()
	used := p.attempts[key]
	p.attempts[key] = used + 1
	p.mu.Unlock()

	// giveUp restores the counter by ASSIGNING `used` back (app.py:16500-16501) —
	// not by deleting and not by decrementing a value another goroutine may have
	// moved: Python re-assigns the captured pre-increment value, and a concurrent
	// Next() for the same address would be clobbered the same way there.
	giveUp := func(cause error) map[string]string {
		p.mu.Lock()
		p.attempts[key] = used
		p.mu.Unlock()
		if smsbower.IsTransientError(cause) {
			p.logf(email, "SMSBower 自动取号网络失败，改用手工手机号池: %v", cause)
		} else {
			p.logf(email, "SMSBower 自动取号失败，改用手工手机号池: %v", cause)
		}
		return nil
	}

	// An unknown country falls back to the configured id (app.py:16424).
	country := settings.Country
	if mapped, ok := openai.SMSBowerCountryIDs[strings.ToUpper(requestedCountry)]; ok {
		country = mapped
	}

	ctx := p.context()
	client, err := p.cfg.NewClient(settings.APIKey)
	if err != nil {
		return giveUp(err)
	}

	priceDetail, stock, tierPrices := p.scanPrices(ctx, client, email, settings, country)
	if len(tierPrices) == 0 {
		// No tier walk: one attempt at the configured cap, which may be ""
		// (= no cap) (app.py:16458-16459).
		tierPrices = []string{settings.MaxPrice}
	}

	activation, winningPrice, err := p.rentNumber(ctx, client, email, settings.Service, country, tierPrices)
	if err != nil {
		return giveUp(err)
	}

	p.logf(email, "SMSBower 自动取号成功 (本账号第 %d 次): %s 国家ID=%s 服务=%s%s 激活ID=%s",
		used+1, activation.Number, country, settings.Service, priceDetail, activation.ActivationID)

	return map[string]string{
		"number":        activation.Number,
		"sms_url":       "smsbower://" + activation.ActivationID,
		"provider":      "smsbower",
		"activation_id": activation.ActivationID,
		// `str(quote.get("cost") or "")` where quote["cost"] was overwritten with
		// the winning tier price string (app.py:16469/16485) — so this is the price
		// cap that actually bought the number, never the quoted float.
		"price": winningPrice,
		"stock": stock,
	}
}

// scanPrices ports the price block (app.py:16429-16457). It cannot fail the
// rental: every error is logged and the walk falls through to a single attempt
// at the configured cap, exactly like Python's `except Exception as price_exc`.
func (p *Provider) scanPrices(ctx context.Context, client SMSClient, email string, settings Settings, country string) (priceDetail, stock string, tierPrices []string) {
	tiers, err := client.GetPriceTiers(ctx, settings.Service, country)
	if err != nil {
		p.logf(email, "SMSBower 价格查询失败，继续取号: %v", err)
		return "", "", nil
	}

	maxPriceValue := 0.0
	if settings.MaxPrice != "" {
		// Validated by NormalizeSettings, so a parse failure is impossible here;
		// Python would have raised out of _smsbower_settings long before.
		maxPriceValue, _ = strconv.ParseFloat(settings.MaxPrice, 64)
	}

	eligible := make([]smsbower.PriceTier, 0, len(tiers))
	for _, tier := range tiers {
		// `not max_price_value or tier[0] <= max_price_value`: 0.0 (no cap) is
		// falsy so everything is eligible. NaN keeps every comparison false, which
		// empties the list — the Python quirk preserved by NormalizeSettings.
		if maxPriceValue == 0 || tier.Cost <= maxPriceValue {
			eligible = append(eligible, tier)
		}
	}

	if len(eligible) > 0 {
		shown := eligible
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts := make([]string, 0, len(shown))
		for _, tier := range shown {
			parts = append(parts, fmt.Sprintf("%s(%d)", formatG(tier.Cost), tier.Count))
		}
		tierText := strings.Join(parts, ", ")
		if settings.MaxPrice != "" {
			priceDetail = fmt.Sprintf(" 限价≤%s 可用档=%s", settings.MaxPrice, tierText)
		} else {
			priceDetail = " 可用档=" + tierText
		}
		total := 0
		for _, tier := range eligible {
			total += tier.Count
		}
		tierPrices = make([]string, 0, len(eligible))
		for _, tier := range eligible {
			// The %g text is what gets sent as maxPrice — a different float format
			// would change the price cap the API sees.
			tierPrices = append(tierPrices, formatG(tier.Cost))
		}
		return priceDetail, countText(total), tierPrices
	}

	quote, err := client.GetPriceQuote(ctx, settings.Service, country)
	if err != nil {
		p.logf(email, "SMSBower 价格查询失败，继续取号: %v", err)
		return "", "", nil
	}
	// Python skipped a cost/count of "" or None only; 0 was still printed. The Go
	// client decodes both into numbers, so a missing field is indistinguishable
	// from 0 and is printed — a log-only difference.
	detailParts := []string{"价格≈" + pyFloatRepr(quote.Cost), "库存≈" + strconv.Itoa(quote.Count)}
	priceDetail = " " + strings.Join(detailParts, " ")
	if settings.MaxPrice != "" {
		// Appended WITHOUT a separator, so with no parts it would start with "；"
		// (app.py:16454-16455).
		priceDetail += fmt.Sprintf("；无≤%s的可用档", settings.MaxPrice)
	}
	return priceDetail, countText(quote.Count), nil
}

// countText is `str(count or "")` — 0 prints as empty (app.py:16520).
func countText(count int) string {
	if count == 0 {
		return ""
	}
	return strconv.Itoa(count)
}

// rentNumber walks the price tiers (app.py:16460-16498). THIS IS THE CALL THAT
// SPENDS MONEY. Error handling per tier, in Python's exact order:
//
//	NO_NUMBERS / MAX_PRICE_EXCEEDED  -> next tier
//	transient                        -> sleep 1.5s, retry the SAME tier once,
//	                                    then next tier if it is empty or wobbles
//	anything else                    -> abort the whole rental
func (p *Provider) rentNumber(ctx context.Context, client SMSClient, email, service, country string, tierPrices []string) (smsbower.Number, string, error) {
	lastTierError := ""

	for _, attemptPrice := range tierPrices {
		activation, err := client.GetNumber(ctx, service, country, attemptPrice)
		if err == nil {
			return activation, attemptPrice, nil
		}
		// Python caught SMSBowerError only; any other exception escapes to the
		// outer handler (which also returns {}, but without the tier bookkeeping).
		if !isSMSBowerError(err) {
			return smsbower.Number{}, "", err
		}
		lastTierError = err.Error()

		if hasNoNumbersMarker(lastTierError) {
			p.logf(email, "SMSBower 低价档 %s 暂无可用号，尝试下一档: %v", priceLabel(attemptPrice), err)
			continue
		}
		if !smsbower.IsTransientError(lastTierError) {
			return smsbower.Number{}, "", err
		}

		p.logf(email, "SMSBower 取号网络抖动，短暂重试同档 %s: %v", priceLabel(attemptPrice), err)
		p.cfg.Sleep(tierRetryDelay)

		activation, retryErr := client.GetNumber(ctx, service, country, attemptPrice)
		if retryErr == nil {
			return activation, attemptPrice, nil
		}
		if !isSMSBowerError(retryErr) {
			return smsbower.Number{}, "", retryErr
		}
		lastTierError = retryErr.Error()
		if hasNoNumbersMarker(lastTierError) {
			p.logf(email, "SMSBower 低价档 %s 暂无可用号，尝试下一档: %v", priceLabel(attemptPrice), retryErr)
			continue
		}
		if smsbower.IsTransientError(lastTierError) {
			p.logf(email, "SMSBower 同档仍网络异常，继续下一档: %v", retryErr)
			continue
		}
		return smsbower.Number{}, "", retryErr
	}

	if lastTierError == "" {
		lastTierError = "SMSBower 所有限价档均未取到号码"
	}
	return smsbower.Number{}, "", &smsbower.Error{Msg: lastTierError}
}

// priceLabel is `attempt_price or '不限'` (app.py:16474).
func priceLabel(price string) string {
	if price == "" {
		return "不限"
	}
	return price
}

func hasNoNumbersMarker(text string) bool {
	for _, marker := range noNumbersMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isSMSBowerError(err error) bool {
	var target *smsbower.Error
	return errors.As(err, &target)
}

// formatG reproduces Python's f"{cost:g}" (app.py:16435, 16442): 6 significant
// digits, trailing zeros removed, %e past the usual thresholds. Go's default
// shortest-round-trip 'g' (precision -1) is NOT the same — 0.123456789 would
// render in full instead of as 0.123457 — and this string is sent as the price
// cap, so the difference is billable.
func formatG(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// pyFloatRepr approximates Python's str(float) for log text: shortest
// round-trip, but integral values keep a ".0".
func pyFloatRepr(v float64) string {
	text := strconv.FormatFloat(v, 'g', -1, 64)
	if strings.ContainsAny(text, ".eEnI") {
		return text
	}
	return text + ".0"
}
