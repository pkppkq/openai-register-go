// Package paymentwindow 打开与注册浏览器完全隔离的支付 Chromium 窗口。
//
// 这里不负责决定“是否允许支付”：Wails 绑定必须先完成显式确认，并把
// AutoConfirm 作为经过第二次确认的能力传入。该包只执行已经批准的一次窗口
// 任务，且不会把卡号、短信链接或手机号写入日志和返回值。
package paymentwindow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"

	appbrowser "github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxyhealth"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
)

const (
	paymentNavigateTimeout = 90 * time.Second
	paymentPollInterval    = time.Second
)

// Options 是一次支付窗口的全部输入。敏感字段只在内存中传给浏览器。
type Options struct {
	Link             string
	Email            string
	ProxyURL         string
	DisplayProxy     string
	ExtensionDir     string
	Phone            string
	Card             string
	SMSURL           string
	SavedFingerprint *models.DeviceFingerprint
	AutoConfirm      bool
	Log              func(string)
	OnFingerprint    func(models.DeviceFingerprint)
}

// Result 描述窗口怎样结束，不包含任何支付资料。
type Result struct {
	Completed    bool `json:"completed"`
	MarkedPlus   bool `json:"markedPlus"`
	ClosedByUser bool `json:"closedByUser"`
}

type paymentSeed struct {
	Phone  string `json:"phone"`
	Card   string `json:"card"`
	SMSURL string `json:"smsUrl"`
}

var detectPaymentProxyHealth = proxyhealth.DetectProxyHealth

// ValidateLink 只接受 HTTPS。具体链接必须由上层从 state.json 的 results
// 中解析，避免把支付资料注入用户随意提供的网页。
func ValidateLink(raw string) error {
	text := strings.TrimSpace(raw)
	parsed, err := url.Parse(text)
	if err != nil || parsed == nil {
		return fmt.Errorf("支付链接格式错误")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("支付链接必须是有效的 HTTPS 地址")
	}
	return nil
}

// ValidateExtensionDir 在消费手机号或支付卡之前检查扩展路径。
func ValidateExtensionDir(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(text)
	if err != nil {
		return "", fmt.Errorf("解析扩展目录失败: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("扩展目录不存在: %s", absolute)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("扩展路径不是目录: %s", absolute)
	}
	if !appbrowser.ExtensionManifestExists(absolute) {
		return "", fmt.Errorf("扩展目录缺少 manifest.json: %s", absolute)
	}
	return absolute, nil
}

// Run 启动支付窗口并阻塞到窗口关闭、自动确认完成或 ctx 被取消。
func Run(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateLink(opts.Link); err != nil {
		return Result{}, err
	}
	extensionDir, err := ValidateExtensionDir(opts.ExtensionDir)
	if err != nil {
		return Result{}, err
	}
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	displayProxy := opts.DisplayProxy
	if strings.TrimSpace(displayProxy) == "" {
		displayProxy = opts.ProxyURL
	}
	log("[支付窗口] 使用代理: " + proxypool.MaskProxyURL(displayProxy))

	health, err := paymentProxyHealth(ctx, opts.ProxyURL, log)
	if err != nil {
		return Result{}, err
	}
	log(fmt.Sprintf(
		"[代理] 支付窗口出口检查通过: %s %s %s ChatGPT=%d Stripe=%d",
		health.IP,
		firstNonBlank(health.Location(), health.Country),
		firstNonBlank(health.Timezone, "UTC"),
		health.ChatGPTStatus,
		health.StripeStatus,
	))

	fingerprint, reused, err := paymentFingerprint(opts.SavedFingerprint, health)
	if err != nil {
		return Result{}, err
	}
	if !reused && opts.OnFingerprint != nil {
		opts.OnFingerprint(fingerprint)
	}

	profileDir, err := os.MkdirTemp("", "paylink-profile-")
	if err != nil {
		return Result{}, fmt.Errorf("创建支付浏览器隔离目录失败: %w", err)
	}
	defer cleanupProfile(profileDir, log)
	if err := seedBrowserPreferences(profileDir); err != nil {
		log("支付浏览器偏好写入失败，已忽略: " + err.Error())
	}
	log("支付窗口已创建全新隔离浏览器环境")

	b, err := appbrowser.Launch(appbrowser.LaunchOptions{
		Fingerprint:  fingerprint,
		Headless:     false,
		ProxyServer:  strings.TrimSpace(opts.ProxyURL),
		ExtensionDir: extensionDir,
		UserDataDir:  profileDir,
		ExtraArgs: [][]string{
			{
				"disable-features",
				"IsolateOrigins,site-per-process,AutofillServerCommunication,AutofillEnableAccountWalletStorage,AutofillCreditCardUpload,AutofillEnablePaymentsMandatoryReauth",
			},
			{"disable-save-password-bubble"},
			{"password-store", "basic"},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("启动支付 Chromium 失败: %w", err)
	}

	var closeOnce sync.Once
	closeBrowser := func() { closeOnce.Do(b.Close) }
	defer closeBrowser()
	cancelWatchDone := make(chan struct{})
	defer close(cancelWatchDone)
	go func() {
		select {
		case <-ctx.Done():
			closeBrowser()
		case <-cancelWatchDone:
		}
	}()

	if err := b.ClearCookies(); err != nil {
		return Result{}, fmt.Errorf("清理支付浏览器 Cookie 失败: %w", err)
	}
	// launcher 可能自带一个 about:blank；支付窗口只保留下面创建、已完整
	// 安装指纹和敏感数据域限制的标签页。
	if pages, listErr := b.Rod.Pages(); listErr == nil {
		for _, page := range pages {
			_ = page.Close()
		}
	}
	page, err := b.NewPage()
	if err != nil {
		return Result{}, err
	}
	seed := paymentSeed{Phone: opts.Phone, Card: opts.Card, SMSURL: opts.SMSURL}
	if err := installPaymentSeed(page.Rod, seed); err != nil {
		return Result{}, fmt.Errorf("安装支付扩展资料失败: %w", err)
	}
	if extensionDir != "" {
		log("已加载支付链接扩展目录: " + extensionDir)
	}
	if err := page.Navigate(opts.Link, paymentNavigateTimeout); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, err
	}
	log("支付链接已在支持扩展的全新 Chromium 窗口打开；关闭窗口后任务结束")

	result, err := monitor(ctx, b, page, seed, opts.AutoConfirm, log)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	return result, nil
}

func paymentProxyHealth(ctx context.Context, proxyURL string, log func(string)) (models.ProxyHealthResult, error) {
	var last models.ProxyHealthResult
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		last = detectPaymentProxyHealth(proxyURL, 15)
		if last.Success {
			return last, nil
		}
		if attempt < 3 {
			log(fmt.Sprintf(
				"[代理] 支付窗口代理健康检查失败，准备重试(%d/3): %s",
				attempt,
				last.Summary(),
			))
			timer := time.NewTimer(1500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return last, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return last, &models.ProxyExitCheckError{
		Msg:    "支付窗口代理健康检查失败: " + last.Summary(),
		Status: "代理检测失败",
	}
}

func paymentFingerprint(saved *models.DeviceFingerprint, health models.ProxyHealthResult) (models.DeviceFingerprint, bool, error) {
	account := &models.MailAccount{BrowserFingerprint: saved}
	var generateErr error
	decision := models.ResolveAccountFingerprint(account, func() models.DeviceFingerprint {
		fp, err := models.GenerateFingerprintForExit(health)
		if err != nil {
			generateErr = err
			return models.DeviceFingerprint{}
		}
		return fp
	})
	if generateErr != nil {
		return models.DeviceFingerprint{}, false, generateErr
	}
	if !models.ValidFingerprint(&decision.Fingerprint) {
		return models.DeviceFingerprint{}, false, errors.New("支付浏览器指纹无效")
	}
	return decision.Fingerprint, decision.Reused, nil
}

func seedBrowserPreferences(profileDir string) error {
	defaultDir := filepath.Join(profileDir, "Default")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(defaultDir, "Preferences")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	payload := map[string]any{
		"autofill": map[string]any{
			"credit_card_enabled": false,
			"profile_enabled":     false,
		},
		"credentials_enable_service": false,
		"profile": map[string]any{
			"password_manager_enabled": false,
		},
		"payments": map[string]any{
			"can_make_payment_enabled": false,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func cleanupProfile(profileDir string, log func(string)) {
	if strings.TrimSpace(profileDir) == "" {
		return
	}
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if err := os.RemoveAll(profileDir); err == nil || os.IsNotExist(err) {
			return
		} else {
			last = err
		}
		time.Sleep(time.Duration(500+attempt*250) * time.Millisecond)
	}
	if last != nil {
		log("临时支付浏览器目录清理失败，已忽略: " + last.Error())
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

// monitor 只在 AutoConfirm=true 时点击具有实际支付副作用的按钮。
func monitor(
	ctx context.Context,
	b *appbrowser.Browser,
	initial *appbrowser.Page,
	seed paymentSeed,
	autoConfirm bool,
	log func(string),
) (Result, error) {
	prepared := map[string]*appbrowser.Page{string(initial.Rod.TargetID): initial}
	seeded := map[string]bool{string(initial.Rod.TargetID): true}
	confirmReady := map[string]time.Time{}
	confirmedURL := map[string]string{}
	successReady := map[string]time.Time{}
	signupFilled := map[string]time.Time{}

	ticker := time.NewTicker(paymentPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-ticker.C:
		}

		rawPages, err := b.Rod.Pages()
		if err != nil || len(rawPages) == 0 {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{ClosedByUser: true}, nil
		}

		openCount := 0
		for _, raw := range rawPages {
			targetID := string(raw.TargetID)
			page := prepared[targetID]
			if page == nil {
				page, err = b.PreparePage(raw)
				if err != nil {
					continue
				}
				prepared[targetID] = page
			}
			if page.IsClosed() {
				continue
			}
			openCount++
			if !seeded[targetID] {
				_ = installPaymentSeed(page.Rod, seed)
				seeded[targetID] = true
			}
			currentURL := page.URL()
			parsed, _ := url.Parse(currentURL)
			host := strings.ToLower(parsedHostname(parsed))
			path := ""
			if parsed != nil {
				path = parsed.Path
			}

			if isPayPalHost(host) && strings.HasPrefix(path, "/checkoutweb/signup") {
				if last := signupFilled[targetID]; time.Since(last) >= 2*time.Second {
					if autofillPaymentExtension(page.Rod, seed) {
						log("已自动填入支付扩展资料")
					}
					signupFilled[targetID] = time.Now()
				}
			}
			if !autoConfirm {
				continue
			}

			if isPayPalHost(host) && strings.HasPrefix(path, "/agreements/approve") {
				switch handlePayPalAgreement(page.Rod) {
				case "clicked_create_account":
					log("已点击 PayPal 创建账户按钮")
				case "submitted_signup_email":
					log("已填写 PayPal 随机邮箱并点击继续支付")
				}
				continue
			}
			if host != "pay.openai.com" || !strings.HasPrefix(path, "/c/pay/") {
				continue
			}
			now := time.Now()
			if previous := confirmedURL[targetID]; previous == "" {
				ready := confirmReady[targetID]
				if ready.IsZero() {
					confirmReady[targetID] = now.Add(5 * time.Second)
					log("检测到 OpenAI 支付确认页，等待 5 秒后自动点击确认按钮")
					continue
				}
				if now.Before(ready) {
					continue
				}
				if clickOpenAIConfirm(page.Rod) {
					confirmedURL[targetID] = currentURL
					log("已点击 OpenAI 支付确认按钮，等待后续跳转")
				} else {
					confirmReady[targetID] = now.Add(time.Second)
				}
				continue
			}
			if currentURL == confirmedURL[targetID] {
				continue
			}
			ready := successReady[targetID]
			if ready.IsZero() {
				successReady[targetID] = now.Add(5 * time.Second)
				log("检测到支付确认后跳转页，等待 5 秒后核验")
				continue
			}
			if now.Before(ready) {
				continue
			}
			if clickOpenAIConfirm(page.Rod) {
				confirmedURL[targetID] = currentURL
				delete(successReady, targetID)
				log("已点击返回后的 OpenAI 支付确认按钮")
				continue
			}
			return Result{Completed: true, MarkedPlus: true}, nil
		}
		if openCount == 0 {
			return Result{ClosedByUser: true}, nil
		}
	}
}

func parsedHostname(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	return parsed.Hostname()
}

func isPayPalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "paypal.com" || strings.HasSuffix(host, ".paypal.com")
}

func installPaymentSeed(page *rod.Page, seed paymentSeed) error {
	raw, err := json.Marshal(seed)
	if err != nil {
		return err
	}
	script := strings.Replace(paymentSeedInitJS, "__PAYMENT_SEED__", string(raw), 1)
	if _, err := page.EvalOnNewDocument(script); err != nil {
		return err
	}
	// 新弹出的页面可能已经完成导航；立即执行一次，同时保留上面的
	// new-document 脚本给后续重定向。
	_, _ = page.Eval(script)
	return nil
}

const paymentSeedInitJS = `(() => {
    const data = __PAYMENT_SEED__;
    const host = String(location.hostname || '').toLowerCase().replace(/\.$/, '');
    const trusted = host === 'paypal.com' || host.endsWith('.paypal.com')
        || host === 'pay.openai.com' || host === 'checkout.stripe.com';
    if (!trusted) return false;
    const phone = data.phone || '';
    const card = data.card || '';
    const smsUrl = data.smsUrl || '';
    try {
        localStorage.setItem('opencode_paypal_phone', phone);
        localStorage.setItem('opencode_paypal_card', card);
        localStorage.setItem('ppaf_phone', phone);
        localStorage.setItem('ppaf_card', card);
        localStorage.setItem('opencode_paypal_sms_url', smsUrl);
        localStorage.setItem('ppaf_sms_url', smsUrl);
    } catch (_) {}
    try {
        if (globalThis.chrome && chrome.storage && chrome.storage.local) {
            chrome.storage.local.set({
                lastCardInput: card,
                lastPhone: phone,
                paypalSmsUrl: smsUrl,
                lastCardSavedAt: Date.now()
            });
        }
    } catch (_) {}
    return true;
})();`

const paymentAutofillJS = `async (data) => {
    const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
    const setValue = (el, value) => {
        if (!el) return false;
        const proto = el instanceof HTMLTextAreaElement
            ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const desc = Object.getOwnPropertyDescriptor(proto, 'value');
        if (desc && desc.set) desc.set.call(el, value); else el.value = value;
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return true;
    };
    const waitFor = async selector => {
        for (let i = 0; i < 12; i++) {
            const el = document.querySelector(selector);
            if (el) return el;
            await sleep(500);
        }
        return null;
    };
    let filled = false;
    const phone = data.phone || '';
    const card = data.card || '';
    const smsUrl = data.smsUrl || '';
    try {
        localStorage.setItem('ppaf_phone', phone);
        localStorage.setItem('ppaf_card', card);
        localStorage.setItem('ppaf_sms_url', smsUrl);
    } catch (_) {}
    try {
        if (globalThis.chrome && chrome.storage && chrome.storage.local) {
            await chrome.storage.local.set({
                lastCardInput: card, lastPhone: phone,
                paypalSmsUrl: smsUrl, lastCardSavedAt: Date.now()
            });
        }
    } catch (_) {}
    const stripeBtn = document.querySelector('#stripe-autofill-btn');
    if (stripeBtn) {
        stripeBtn.click();
        const input = await waitFor('#saf-input');
        const ok = await waitFor('#saf-ok');
        if (input && ok) {
            setValue(input, card);
            ok.click();
            filled = true;
        }
    }
    const paypalBtn = document.querySelector('#ppaf-btn');
    if (paypalBtn) {
        paypalBtn.click();
        const phoneInput = await waitFor('#ppaf-phone');
        const cardInput = await waitFor('#ppaf-card');
        const fillBtn = await waitFor('#ppaf-fill');
        if (phoneInput && cardInput && fillBtn) {
            setValue(phoneInput, phone);
            setValue(cardInput, card);
            fillBtn.click();
            filled = true;
        }
    }
    return filled;
}`

func autofillPaymentExtension(page *rod.Page, seed paymentSeed) bool {
	if page == nil || strings.TrimSpace(seed.Card) == "" {
		return false
	}
	value, err := page.Eval(paymentAutofillJS, seed)
	return err == nil && value != nil && value.Value.Bool()
}

const openAIConfirmJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0
            && s.display !== 'none' && s.visibility !== 'hidden';
    };
    const buttons = Array.from(document.querySelectorAll(
        'button, [role="button"], input[type="submit"]'
    ));
    const target = buttons.find(el => {
        if (!visible(el) || el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
        const text = ((el.textContent || '') + ' ' + (el.getAttribute('value') || '')
            + ' ' + (el.getAttribute('aria-label') || ''))
            .trim().toLowerCase();
        if (/cancel|back|return|キャンセル|戻る/.test(text)) return false;
        return /subscribe|confirm|continue|pay|complete|同意|続行|確認|支払|購入|登録/.test(text);
    });
    if (!target) return false;
    target.scrollIntoView({ block: 'center' });
    target.click();
    return true;
}`

func clickOpenAIConfirm(page *rod.Page) bool {
	value, err := page.Eval(openAIConfirmJS)
	return err == nil && value != nil && value.Value.Bool()
}

const payPalAgreementJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0
            && s.display !== 'none' && s.visibility !== 'hidden';
    };
    const setValue = (el, value) => {
        if (!el) return false;
        const proto = el instanceof HTMLTextAreaElement
            ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const desc = Object.getOwnPropertyDescriptor(proto, 'value');
        if (desc && desc.set) desc.set.call(el, value); else el.value = value;
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return true;
    };
    const randomEmail = () =>
        'pp' + Date.now() + Math.floor(Math.random() * 10000) + '@gmail.com';
    const candidates = Array.from(document.querySelectorAll(
        'button, a[role="button"], input[type="submit"]'
    ));
    const createBtn = candidates.find(el => {
        if (!visible(el) || el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
        const text = ((el.textContent || '') + ' ' + (el.getAttribute('value') || '')
            + ' ' + (el.getAttribute('aria-label') || ''))
            .trim().toLowerCase();
        return text.includes('アカウントを開設')
            || text.includes('アカウントを作成')
            || text.includes('create account')
            || text.includes('sign up');
    });
    if (createBtn) {
        createBtn.scrollIntoView({ block: 'center' });
        createBtn.click();
        return 'clicked_create_account';
    }
    const emailInput = Array.from(document.querySelectorAll('input')).find(input => {
        if (!visible(input) || input.disabled) return false;
        const meta = ((input.type || '') + ' ' + (input.name || '') + ' '
            + (input.id || '') + ' ' + (input.placeholder || '') + ' '
            + (input.getAttribute('aria-label') || '')).toLowerCase();
        return meta.includes('email') || meta.includes('login_email') || meta.includes('メール');
    });
    if (!emailInput || String(emailInput.value || '').trim()) return '';
    setValue(emailInput, randomEmail());
    const continueBtn = candidates.find(el => {
        if (!visible(el) || el.disabled || el.getAttribute('aria-disabled') === 'true') return false;
        const text = ((el.textContent || '') + ' ' + (el.getAttribute('value') || '')
            + ' ' + (el.getAttribute('aria-label') || ''))
            .trim().toLowerCase();
        if (/cancel|back|return|キャンセル|戻る/.test(text)) return false;
        return text.includes('支払いを続ける')
            || text.includes('continue to payment')
            || text.includes('continue')
            || text.includes('次へ');
    });
    if (!continueBtn) return '';
    continueBtn.scrollIntoView({ block: 'center' });
    continueBtn.click();
    return 'submitted_signup_email';
}`

func handlePayPalAgreement(page *rod.Page) string {
	value, err := page.Eval(payPalAgreementJS)
	if err != nil || value == nil {
		return ""
	}
	return value.Value.Str()
}
