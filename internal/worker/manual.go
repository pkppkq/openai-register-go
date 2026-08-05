package worker

// 本文件承载需要“保留浏览器”的人工认证入口。它们和 Run/RunAuthOnly
// 共用同一套指纹、代理、Cloudflare、邮箱验证码与 OAuth 页面能力，但不会
// 生成支付链接，也不会主动租用手机号。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

const (
	externalOAuthDeadline = 15 * time.Minute
	externalOAuthNotice   = 20 * time.Second
)

// BrowserActionResult 是人工浏览器任务的非敏感结果。验证码只用于
// “手动登录取码”返回给当前桌面前端，不写入日志或状态文件。
type BrowserActionResult struct {
	Status      string `json:"status"`
	Code        string `json:"code,omitempty"`
	NeedsManual bool   `json:"needsManual,omitempty"`
	CallbackURL string `json:"callbackUrl,omitempty"`
}

// RunLoginAndKeep 登录已有账号，并在成功或非传输类页面异常时保留浏览器。
// 这对应 Python 的“登录并保留”，不会进入注册/手机号流程。
func (w *Worker) RunLoginAndKeep(ctx context.Context) (BrowserActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var b *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(b)
	}()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "登录"); err != nil {
		return BrowserActionResult{}, err
	}
	if err := w.preconnectOTPReader(); err != nil {
		w.log(fmt.Sprintf("邮箱收件接口预连接失败，继续打开登录浏览器；如出现验证码请手动输入: %v", err))
		w.closeOTPReader()
		w.otpReader = nil
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return BrowserActionResult{}, err
	}
	b = nb
	if err := b.ClearCookies(); err != nil {
		return BrowserActionResult{}, err
	}
	page, err := b.NewPage()
	if err != nil {
		return BrowserActionResult{}, err
	}
	w.LogBrowserProxyStatus("登录浏览器代理")

	fl := w.buildFlows(b, page, w.cfg.RegisterProxy)
	loginErr := fl.Register.LoginExisting(ctx, false)
	if loginErr != nil && IsAuthProxyTransportError(loginErr) {
		return BrowserActionResult{}, loginErr
	}

	result := BrowserActionResult{Status: "已登录"}
	if loginErr != nil && !b.HasChatGPTSession() {
		result.Status = "需手动登录"
		result.NeedsManual = true
		w.log(fmt.Sprintf("自动登录未完成，浏览器窗口已保留；请在窗口里手动继续/输入验证码: %v", loginErr))
	} else if loginErr != nil {
		w.log("自动登录中断但已检测到 ChatGPT 会话，浏览器窗口已保留")
	} else {
		w.log("登录并保留完成：已登录选中邮箱，浏览器窗口已保留")
	}
	w.park(b, w.cfg.RegisterProxy)
	b = nil
	return result, nil
}

// RunSessionReader 打开登录页、尝试通过首屏并填写邮箱，然后把浏览器留给
// 用户继续完成登录并自行打开 /api/auth/session。
func (w *Worker) RunSessionReader(ctx context.Context) (BrowserActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var b *browser.Browser
	defer func() { CloseBrowser(b) }()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "登录"); err != nil {
		return BrowserActionResult{}, err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return BrowserActionResult{}, err
	}
	b = nb
	if err := b.ClearCookies(); err != nil {
		return BrowserActionResult{}, err
	}
	page, err := b.NewPage()
	if err != nil {
		return BrowserActionResult{}, err
	}
	w.LogBrowserProxyStatus("登录浏览器代理")

	fl := w.buildFlows(b, page, w.cfg.RegisterProxy)
	if err := page.Navigate(openai.ChatGPTBaseURL, 60*time.Second); err != nil {
		return BrowserActionResult{}, err
	}
	signinURL, err := fl.Auth.CreateLoginURL()
	if err != nil {
		return BrowserActionResult{}, err
	}
	if err := page.Navigate(signinURL, 90*time.Second); err != nil {
		return BrowserActionResult{}, err
	}
	w.log("已打开 ChatGPT 登录页，准备填写邮箱")
	if !fl.CF.TryPassCloudflare(page, true, "手动登录首屏") {
		return BrowserActionResult{}, errors.New("手动登录首屏 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
	}

	filled := false
	for attempt := 0; attempt < 15; attempt++ {
		if err := ctx.Err(); err != nil {
			return BrowserActionResult{}, err
		}
		if fl.CF.IsCloudflareChallengePage(page) &&
			!fl.CF.TryPassCloudflare(page, true, "手动登录") {
			return BrowserActionResult{}, errors.New("手动登录 Cloudflare 自动+人工等待均未放行；请更换动态代理后重试")
		}
		if fl.Auth.FillEmailIfVisible() {
			filled = true
			break
		}
		if err := manualActionSleep(ctx, time.Second); err != nil {
			return BrowserActionResult{}, err
		}
	}
	if filled {
		w.log(fmt.Sprintf("已自动填写邮箱并点击继续: %s", w.Account.Email))
	} else {
		w.log("未找到邮箱输入框；浏览器已保留，请手动完成后续登录")
	}
	w.log(openai.ChatGPTBaseURL + "/api/auth/session 可用于复制 Session JSON，再回到软件点“填入Session”保存")

	w.park(b, w.cfg.RegisterProxy)
	b = nil
	return BrowserActionResult{Status: "已填邮箱", NeedsManual: true}, nil
}

// RunExternalOAuth 打开外部 OAuth authorize 链接，自动处理邮箱、验证码和
// 授权按钮；到 callback、手机号页、超时或用户取消时均保留浏览器。
func (w *Worker) RunExternalOAuth(ctx context.Context, oauthURL string) (BrowserActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	oauthURL = strings.TrimSpace(oauthURL)
	if oauthURL == "" {
		return BrowserActionResult{}, errors.New("OAuth 链接为空")
	}

	var b *browser.Browser
	defer func() {
		w.closeOTPReader()
		CloseBrowser(b)
	}()
	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "外部 OAuth"); err != nil {
		return BrowserActionResult{}, err
	}
	if err := w.preconnectOTPReader(); err != nil {
		return BrowserActionResult{}, err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return BrowserActionResult{}, err
	}
	b = nb
	page, err := b.NewPage()
	if err != nil {
		return BrowserActionResult{}, err
	}
	fl := w.buildFlows(b, page, w.cfg.RegisterProxy)
	w.log("已打开外部 OAuth 链接，开始自动登录")
	if err := page.Navigate(oauthURL, 90*time.Second); err != nil {
		return BrowserActionResult{}, err
	}

	minTimestamp := float64(time.Now().Add(-120*time.Second).UnixNano()) / 1e9
	deadline := time.Now().Add(externalOAuthDeadline)
	lastNotice := time.Time{}
	badGatewayRefreshes := 0
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			w.log("外部 OAuth 已停止，浏览器窗口已保留")
			w.park(b, w.cfg.RegisterProxy)
			b = nil
			return BrowserActionResult{Status: "OAuth已保留", NeedsManual: true}, nil
		}
		refreshed, count, err := fl.Team.RefreshBadGatewayIfVisible(badGatewayRefreshes, "外部OAuth")
		badGatewayRefreshes = count
		if err != nil {
			return BrowserActionResult{}, err
		}
		if refreshed {
			if err := manualActionSleep(ctx, 2*time.Second); err != nil {
				continue
			}
			continue
		}

		currentURL := page.URL()
		if strings.HasPrefix(currentURL, openai.DefaultRedirectURI) {
			w.log("外部 OAuth 已到 callback")
			w.park(b, w.cfg.RegisterProxy)
			b = nil
			return BrowserActionResult{Status: "OAuth登录完成", CallbackURL: currentURL}, nil
		}
		if strings.HasPrefix(currentURL, openai.AuthBaseURL+"/add-phone") ||
			fl.Phone.LooksLikeRegisterPhoneCodePage() {
			w.log("外部 OAuth 遇到手机号/短信验证页，已停止自动处理并保留浏览器")
			w.park(b, w.cfg.RegisterProxy)
			b = nil
			return BrowserActionResult{Status: "OAuth需手动", NeedsManual: true}, nil
		}
		if strings.Contains(currentURL, "email-verification") || fl.OTP.HasOTPInput() {
			w.log("外部 OAuth 等待并提交邮箱验证码")
			if err := fl.OTP.SubmitEmailCode(minTimestamp); err != nil {
				return BrowserActionResult{}, err
			}
			continue
		}
		if fl.Auth.FillEmailIfVisible() {
			minTimestamp = float64(time.Now().Add(-120*time.Second).UnixNano()) / 1e9
			if err := fl.Team.WaitTeamSSOProgress("提交外部 OAuth 邮箱后跳转", 45*time.Second); err != nil {
				return BrowserActionResult{}, err
			}
			continue
		}
		if clickExternalOAuthAuthorize(page) {
			if err := fl.Team.WaitTeamSSOProgress("外部 OAuth 授权按钮后跳转", 45*time.Second); err != nil {
				return BrowserActionResult{}, err
			}
			continue
		}
		if fl.Auth.ClickContinue() {
			if err := fl.Team.WaitTeamSSOProgress("外部 OAuth 继续按钮后跳转", 45*time.Second); err != nil {
				return BrowserActionResult{}, err
			}
			continue
		}
		if lastNotice.IsZero() || time.Since(lastNotice) >= externalOAuthNotice {
			remain := int(time.Until(deadline).Seconds())
			if remain < 0 {
				remain = 0
			}
			w.log(fmt.Sprintf("外部 OAuth 自动登录等待页面推进，剩余约 %ds，当前: %s", remain, truncateManualURL(currentURL, 120)))
			lastNotice = time.Now()
		}
		if err := manualActionSleep(ctx, time.Second); err != nil {
			continue
		}
	}

	w.log("外部 OAuth 自动登录等待结束，浏览器窗口已保留")
	w.park(b, w.cfg.RegisterProxy)
	b = nil
	return BrowserActionResult{Status: "OAuth已保留", NeedsManual: true}, nil
}

// RunManualLoginCode 打开 ChatGPT 登录页并保留窗口，同时只读等待邮箱验证码。
func (w *Worker) RunManualLoginCode(ctx context.Context) (BrowserActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var b *browser.Browser
	defer func() { CloseBrowser(b) }()

	if _, err := w.PrepareFingerprintForProxy(w.cfg.RegisterProxy, "ChatGPT登录"); err != nil {
		return BrowserActionResult{}, err
	}
	nb, err := w.NewBrowser(w.cfg.RegisterProxy)
	if err != nil {
		return BrowserActionResult{}, err
	}
	b = nb
	page, err := b.NewPage()
	if err != nil {
		return BrowserActionResult{}, err
	}
	if err := page.Navigate(openai.ChatGPTBaseURL, 60*time.Second); err != nil {
		return BrowserActionResult{}, err
	}
	w.park(b, w.cfg.RegisterProxy)
	b = nil
	w.log("已打开 ChatGPT 登录页；开始监听邮箱验证码")

	reader, err := mail.CreateMailReader(w.Account, mail.Log(w.log), "")
	if err != nil {
		return BrowserActionResult{}, err
	}
	defer reader.Close()
	minTimestamp := float64(time.Now().Add(-120*time.Second).UnixNano()) / 1e9
	code, err := reader.WaitForCode(ctx, minTimestamp, 600, mail.DefaultEmailOTPLookbackSeconds)
	if err != nil {
		return BrowserActionResult{}, err
	}
	return BrowserActionResult{Status: "验证码已弹出", Code: code, NeedsManual: true}, nil
}

// IsAuthProxyTransportError 是 Python _is_auth_proxy_transport_error 的标记表。
// 只有这些错误才允许更换代理；页面业务错误不能通过重试反复触发登录动作。
func IsAuthProxyTransportError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	markers := []string{
		"connect tunnel failed", "response 502", "curl: (35)", "curl: (52)",
		"curl: (56)", "empty reply from server", "tls connect error",
		"proxy connect", "connection aborted", "connection closed",
		"connection reset", "connection refused", "err_connection_closed",
		"err_connection_aborted", "err_connection_reset", "err_timed_out",
		"winerror 10053", "你的主机中的软件中止了一个已建立的连接",
		"timed out", "timeout", "连接超时", "代理连接",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func manualActionSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncateManualURL(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func clickExternalOAuthAuthorize(page *browser.Page) bool {
	if page == nil || page.Rod == nil {
		return false
	}
	value, err := page.Rod.Eval(externalOAuthAuthorizeJS)
	return err == nil && value != nil && value.Value.Bool()
}

const externalOAuthAuthorizeJS = `() => {
    const visible = el => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const candidates = Array.from(document.querySelectorAll(
        'button, a, [role="button"]'
    )).filter(visible);
    const target = candidates.find(el => {
        const text = ((el.textContent || '') + ' ' + (el.getAttribute('aria-label') || ''))
            .replace(/\s+/g, ' ').trim();
        return /Authorize|授权|允許|允许|Approve|同意|Continue|继续|続行/i.test(text);
    });
    if (!target || target.disabled || target.getAttribute('aria-disabled') === 'true') return false;
    target.scrollIntoView({ block: 'center', inline: 'center' });
    target.click();
    return true;
}`
