package ui

// 本文件是 X1 支付窗口的 Wails 边界。所有真实浏览器工作均在可取消任务中
// 执行；绑定先做两层确认，再从 state.json 读取已保存长链，绝不接受前端
// 任意 URL 携带卡资料打开。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/paymentwindow"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

const (
	JobPaymentWindow JobKind = "payment_window"

	PaymentProxyNew        = "new"
	PaymentProxyExtraction = "extraction"
)

func init() {
	networkJobKinds[JobPaymentWindow] = true
}

// OpenPaymentWindowRequest 打开一个账号已保存的长链。
//
// Confirmed 是“打开真实支付页”的确认；AutoConfirm=true 还必须同时传
// ConfirmAutoCharge=true，表示用户另外确认了自动点击确认/订阅按钮。
type OpenPaymentWindowRequest struct {
	Email             string `json:"email"`
	ProxyMode         string `json:"proxyMode"`
	Confirmed         bool   `json:"confirmed"`
	AutoConfirm       bool   `json:"autoConfirm"`
	ConfirmAutoCharge bool   `json:"confirmAutoCharge"`
}

// OpenPaymentWindowsRequest 批量打开，每个账号拥有独立 Chromium、代理链、
// PP 手机和支付卡。确认覆盖本次去重后的账号数量。
type OpenPaymentWindowsRequest struct {
	Emails            []string `json:"emails"`
	Confirmed         bool     `json:"confirmed"`
	AutoConfirm       bool     `json:"autoConfirm"`
	ConfirmAutoCharge bool     `json:"confirmAutoCharge"`
}

type PaymentWindowSkip struct {
	Email string `json:"email"`
	Error string `json:"error"`
}

type OpenPaymentWindowsResult struct {
	Jobs    []JobView           `json:"jobs"`
	Skipped []PaymentWindowSkip `json:"skipped"`
}

type paymentWindowPrepared struct {
	Account      models.MailAccount
	Settings     settings.Settings
	Link         string
	DynamicProxy string
	Phone        string
	SMSURL       string
	Card         string
	Notes        []string
}

type activePaymentWindow struct {
	email   string
	session *proxySession
}

// PaymentProxySwitchResult 是支付窗口热切换后的脱敏结果。
type PaymentProxySwitchResult struct {
	Email string `json:"email"`
	Proxy string `json:"proxy"`
}

type paymentWindowRunFunc func(context.Context, paymentwindow.Options) (paymentwindow.Result, error)

var runPaymentWindow paymentWindowRunFunc = paymentwindow.Run

// StartOpenPaymentWindow 打开单个支付窗口并立即返回任务。
func (a *App) StartOpenPaymentWindow(req OpenPaymentWindowRequest) (JobView, error) {
	if err := validatePaymentConfirmation(1, req.Confirmed, req.AutoConfirm, req.ConfirmAutoCharge); err != nil {
		return JobView{}, err
	}
	mode, err := normalizePaymentProxyMode(req.ProxyMode)
	if err != nil {
		return JobView{}, err
	}
	account, err := a.paymentWindowPreflight(req.Email)
	if err != nil {
		return JobView{}, err
	}
	return a.startPaymentWindowJob(account.Email, mode, req.AutoConfirm)
}

// StartOpenPaymentWindows 批量启动独立支付窗口。无长链或已经有任务运行的
// 账号进入 Skipped；至少成功登记一个任务时不因其他账号失败而回滚。
func (a *App) StartOpenPaymentWindows(req OpenPaymentWindowsRequest) (OpenPaymentWindowsResult, error) {
	emails := uniquePaymentEmails(req.Emails)
	if len(emails) == 0 {
		return OpenPaymentWindowsResult{}, errors.New("请先选中邮箱")
	}
	if err := validatePaymentConfirmation(len(emails), req.Confirmed, req.AutoConfirm, req.ConfirmAutoCharge); err != nil {
		return OpenPaymentWindowsResult{}, err
	}
	out := OpenPaymentWindowsResult{
		Jobs:    []JobView{},
		Skipped: []PaymentWindowSkip{},
	}
	for _, email := range emails {
		account, err := a.paymentWindowPreflight(email)
		if err != nil {
			out.Skipped = append(out.Skipped, PaymentWindowSkip{Email: email, Error: err.Error()})
			continue
		}
		view, err := a.startPaymentWindowJob(account.Email, PaymentProxyNew, req.AutoConfirm)
		if err != nil {
			out.Skipped = append(out.Skipped, PaymentWindowSkip{Email: account.Email, Error: err.Error()})
			continue
		}
		out.Jobs = append(out.Jobs, view)
	}
	if len(out.Jobs) == 0 {
		return out, errors.New("选中的邮箱里没有可打开的长链接")
	}
	return out, nil
}

func validatePaymentConfirmation(count int, confirmed, autoConfirm, confirmAutoCharge bool) error {
	if !confirmed {
		return fmt.Errorf("打开 %d 个真实支付窗口可能产生扣款，必须由用户明确确认", count)
	}
	if autoConfirm && !confirmAutoCharge {
		return errors.New("自动确认可能完成真实订阅或扣款，必须单独确认后才能启用")
	}
	return nil
}

func normalizePaymentProxyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", PaymentProxyNew:
		return PaymentProxyNew, nil
	case PaymentProxyExtraction:
		return PaymentProxyExtraction, nil
	default:
		return "", fmt.Errorf("未知的支付窗口代理模式: %s", value)
	}
}

func uniquePaymentEmails(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		email := models.NormalizeEmailAddress(value)
		key := strings.ToLower(email)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, email)
	}
	return out
}

// paymentWindowPreflight 在登记任务前只读检查账号、长链和扩展目录，避免一个
// 明显无效的点击占用任务 id。卡池/手机池消费留到任务登记后原子执行。
func (a *App) paymentWindowPreflight(email string) (models.MailAccount, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return models.MailAccount{}, err
	}
	account, err := paymentAccountFromSnapshot(snapshot, email)
	if err != nil {
		return models.MailAccount{}, err
	}
	link := paymentLinkFromSnapshot(snapshot, account.Email)
	if link == "" {
		return models.MailAccount{}, errors.New("暂无长链接")
	}
	if err := paymentwindow.ValidateLink(link); err != nil {
		return models.MailAccount{}, err
	}
	if _, err := paymentwindow.ValidateExtensionDir(settings.FromSnapshot(snapshot).PaypalExtensionDir); err != nil {
		return models.MailAccount{}, err
	}
	return account, nil
}

func (a *App) startPaymentWindowJob(email, proxyMode string, autoConfirm bool) (JobView, error) {
	return a.startNetworkJob(JobPaymentWindow, email, func(ctx context.Context, log func(string)) (any, error) {
		prepared, err := a.preparePaymentWindow(email, proxyMode)
		if err != nil {
			return paymentwindow.Result{}, err
		}
		for _, note := range prepared.Notes {
			log(note)
		}
		if prepared.Phone != "" {
			log("PP 手机号与接码配置已原子轮换")
		}
		if prepared.Card != "" {
			log("支付卡资料已原子取用（日志不显示卡号）")
		}

		proxy, err := a.openProxySession(prepared.Settings, prepared.DynamicProxy, log)
		if err != nil {
			return paymentwindow.Result{}, err
		}
		defer proxy.Close()
		active := a.registerActivePaymentWindow(prepared.Account.Email, proxy)
		defer a.unregisterActivePaymentWindow(prepared.Account.Email, active)

		result, runErr := runPaymentWindow(ctx, paymentwindow.Options{
			Link:             prepared.Link,
			Email:            prepared.Account.Email,
			ProxyURL:         proxy.Config.ChainURL,
			DisplayProxy:     firstNonEmptyPaymentProxy(proxy.Config.DynamicProxy, proxy.Config.LocalProxy),
			ExtensionDir:     prepared.Settings.PaypalExtensionDir,
			Phone:            prepared.Phone,
			Card:             prepared.Card,
			SMSURL:           prepared.SMSURL,
			SavedFingerprint: prepared.Account.BrowserFingerprint,
			AutoConfirm:      autoConfirm,
			Log:              log,
			OnFingerprint: func(fp models.DeviceFingerprint) {
				a.saveFingerprint(prepared.Account.Email, fp)
			},
		})
		if runErr != nil {
			return result, runErr
		}
		if result.MarkedPlus {
			if err := a.networkPatchState(prepared.Account.Email, map[string]any{
				"account_type": "plus",
				"status":       "Plus",
			}, nil); err != nil {
				return result, fmt.Errorf("支付已完成，但保存 Plus 状态失败: %w", err)
			}
			log("支付确认流程完成，账号已标记为 Plus")
		}
		return result, nil
	})
}

// SwitchPaymentWindowProxy 对当前仍打开的支付窗口原地替换动态上游。浏览器
// 始终连接同一个 127.0.0.1 链地址，因此不需要关闭页面或重新注入扩展资料。
func (a *App) SwitchPaymentWindowProxy(email string) (PaymentProxySwitchResult, error) {
	key := strings.ToLower(models.NormalizeEmailAddress(email))
	if key == "" {
		return PaymentProxySwitchResult{}, errors.New("请先选择一个已打开支付窗口的账号")
	}
	a.paymentWindowsMu.Lock()
	active := a.paymentWindows[key]
	if active == nil || active.session == nil || active.session.server == nil {
		a.paymentWindowsMu.Unlock()
		return PaymentProxySwitchResult{}, errors.New("当前没有打开中的支付窗口")
	}
	if active.session.Config.ChainURL == "" {
		a.paymentWindowsMu.Unlock()
		return PaymentProxySwitchResult{}, errors.New("当前支付窗口启动时未使用本地代理链，无法热切换；请用动态代理重新打开")
	}
	a.paymentWindowsMu.Unlock()

	next, err := a.takePaymentSwitchProxy()
	if err != nil {
		return PaymentProxySwitchResult{}, err
	}

	a.paymentWindowsMu.Lock()
	current := a.paymentWindows[key]
	if current != active || current.session == nil || current.session.server == nil {
		a.paymentWindowsMu.Unlock()
		return PaymentProxySwitchResult{}, errors.New("支付窗口已关闭，未执行代理切换")
	}
	current.session.server.SetDynamicProxy(next)
	current.session.Config.DynamicProxy = next
	a.paymentWindowsMu.Unlock()

	masked := proxypool.MaskProxyURL(next)
	a.log("已手动切换支付窗口动态代理: "+masked, active.email)
	return PaymentProxySwitchResult{Email: active.email, Proxy: masked}, nil
}

func (a *App) registerActivePaymentWindow(email string, session *proxySession) *activePaymentWindow {
	active := &activePaymentWindow{email: email, session: session}
	key := strings.ToLower(models.NormalizeEmailAddress(email))
	if key == "" {
		return active
	}
	a.paymentWindowsMu.Lock()
	if a.paymentWindows == nil {
		a.paymentWindows = map[string]*activePaymentWindow{}
	}
	a.paymentWindows[key] = active
	a.paymentWindowsMu.Unlock()
	return active
}

func (a *App) unregisterActivePaymentWindow(email string, active *activePaymentWindow) {
	key := strings.ToLower(models.NormalizeEmailAddress(email))
	if key == "" {
		return
	}
	a.paymentWindowsMu.Lock()
	if a.paymentWindows[key] == active {
		delete(a.paymentWindows, key)
	}
	a.paymentWindowsMu.Unlock()
}

func (a *App) takePaymentSwitchProxy() (string, error) {
	next := ""
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
			return snapshot, nil, errors.New("当前为“全走本地代理”，无需切换到支付动态代理")
		}
		pool := proxypool.NewPool(st.PaymentDynamicProxy)
		next = pool.Take()
		if next == "" {
			return snapshot, nil, errors.New("当前没有可用的支付链接动态代理")
		}
		st.PaymentDynamicProxy = pool.Text()
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return next, err
}

// preparePaymentWindow 在一次 mutateState 中同时决定代理、轮换 PP 手机池并
// 消费支付卡。任一解析失败时 callback 返回错误，三者都不会部分落盘。
func (a *App) preparePaymentWindow(email, proxyMode string) (paymentWindowPrepared, error) {
	var out paymentWindowPrepared
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		account, err := paymentAccountFromSnapshot(snapshot, email)
		if err != nil {
			return snapshot, nil, err
		}
		link := paymentLinkFromSnapshot(snapshot, account.Email)
		if link == "" {
			return snapshot, nil, errors.New("暂无长链接")
		}
		if err := paymentwindow.ValidateLink(link); err != nil {
			return snapshot, nil, err
		}

		st := settings.FromSnapshot(snapshot)
		if _, err := paymentwindow.ValidateExtensionDir(st.PaypalExtensionDir); err != nil {
			return snapshot, nil, err
		}
		changed := false
		notes := []string{}

		dynamicProxy, proxyChanged, proxyNotes, err := a.paymentWindowProxy(
			snapshot,
			account.Email,
			&st,
			proxyMode,
		)
		if err != nil {
			return snapshot, nil, err
		}
		changed = changed || proxyChanged
		notes = append(notes, proxyNotes...)

		phone := strings.TrimSpace(st.PaypalPhone)
		smsURL := strings.TrimSpace(st.PaypalSMSURL)
		phoneLines := uiPhoneInputLines(st.PaypalPhonePool)
		if len(phoneLines) > 0 {
			index := st.PaypalPhonePoolIndex % len(phoneLines)
			if index < 0 {
				index = 0
			}
			entry, parseErr := models.ParsePayPalPhoneLine(phoneLines[index])
			if parseErr != nil {
				return snapshot, nil, fmt.Errorf("PP手机号+接码池第 %d 行格式错误: %w", index+1, parseErr)
			}
			phone, smsURL = entry.Number, entry.SMSURL
			st.PaypalPhonePoolIndex++
			changed = true
		}

		cardText := strings.TrimSpace(st.PaypalCard)
		cards := localPaymentCardsFromSnapshot(snapshot)
		cardsChanged := false
		if cardText != "" && len(cards) > 0 {
			cardIndex := -1
			for index := range cards {
				if cards[index].Status == "未用" {
					cardIndex = index
					break
				}
			}
			if cardIndex < 0 {
				return snapshot, nil, errors.New("支付卡池没有未用卡，请导入新卡或重置卡池")
			}
			replaced, replaceErr := localReplacePayPalCardHead(cardText, cards[cardIndex])
			if replaceErr != nil {
				return snapshot, nil, replaceErr
			}
			cardText = replaced
			cards[cardIndex].Status = "已用"
			cardsChanged = true
			changed = true
		}

		out = paymentWindowPrepared{
			Account:      account,
			Settings:     st,
			Link:         link,
			DynamicProxy: dynamicProxy,
			Phone:        phone,
			SMSURL:       smsURL,
			Card:         cardText,
			Notes:        notes,
		}
		if !changed {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		next := settings.ToSnapshot(st, snapshot)
		if cardsChanged {
			next["payment_cards"] = localPaymentCardsToSnapshot(cards)
		}
		return next, map[string]bool{}, nil
	})
	return out, err
}

func (a *App) paymentWindowProxy(
	snapshot map[string]any,
	email string,
	st *settings.Settings,
	mode string,
) (string, bool, []string, error) {
	if st == nil {
		return "", false, nil, errors.New("支付窗口设置为空")
	}
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return "", false, []string{"当前为“全走本地代理”，支付窗口忽略动态及历史提链代理"}, nil
	}
	if mode == PaymentProxyNew {
		if proxy, next := takePaymentPoolHead(st.FollowupDynamicProxy); proxy != "" {
			st.FollowupDynamicProxy = next
			return proxy, true, []string{"后续动态代理已取用并轮转到队尾"}, nil
		}
		if proxy, next := takePaymentPoolHead(st.PaymentDynamicProxy); proxy != "" {
			st.PaymentDynamicProxy = next
			return proxy, true, []string{"支付链接动态代理已取用并轮转到队尾"}, nil
		}
		return "", false, []string{"支付窗口未配置动态代理，将使用本地代理或直连"}, nil
	}

	payload := networkSessionByEmail(snapshot, email)
	history := proxypool.NormalizeProxyURL(networkText(payload["link_proxy"]))
	if history == "" {
		history = proxypool.NormalizeProxyURL(networkText(payload["link_followup_proxy"]))
	}
	local := proxypool.NormalizeProxyURL(st.LocalProxy)
	candidates := proxypool.ParseProxyPoolText(st.FollowupDynamicProxy)
	if len(candidates) == 0 {
		candidates = proxypool.ParseProxyPoolText(st.PaymentDynamicProxy)
	}
	if len(candidates) == 0 {
		candidates = proxypool.ParseProxyPoolText(st.DynamicProxies)
	}
	fallback := ""
	if len(candidates) > 0 {
		fallback = a.nextDynamicProxy(candidates)
	}
	switch {
	case history != "" && history != local:
		return history, false, []string{
			"使用长链生成时保存的后续代理打开支付窗口: " + proxypool.MaskProxyURL(history),
		}, nil
	case history == local && fallback != "":
		return fallback, false, []string{"当前长链只保存了本地代理，已改用设置里的提链代理"}, nil
	case history == "" && fallback != "":
		return fallback, false, []string{"当前长链未保存提链代理，已改用设置里的提链代理"}, nil
	case local != "":
		return "", false, []string{"当前选中邮箱暂无动态提链代理，将使用本地代理"}, nil
	default:
		return "", false, nil, errors.New("当前选中邮箱暂无长链提取代理")
	}
}

func takePaymentPoolHead(text string) (string, string) {
	pool := proxypool.NewPool(text)
	value := pool.Take()
	return value, pool.Text()
}

func paymentAccountFromSnapshot(snapshot map[string]any, email string) (models.MailAccount, error) {
	want := models.NormalizeEmailAddress(email)
	if want == "" {
		return models.MailAccount{}, errors.New("未指定账号邮箱")
	}
	for _, account := range accountsFromSnapshot(snapshot) {
		if strings.EqualFold(account.Email, want) {
			return account, nil
		}
	}
	return models.MailAccount{}, fmt.Errorf("账号不存在: %s", email)
}

func paymentLinkFromSnapshot(snapshot map[string]any, email string) string {
	results, _ := snapshot["results"].(map[string]any)
	for key, value := range results {
		if strings.EqualFold(key, email) {
			return strings.TrimSpace(networkText(value))
		}
	}
	return ""
}

func firstNonEmptyPaymentProxy(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
