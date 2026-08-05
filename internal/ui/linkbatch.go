package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/batch"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/opll"
	"github.com/pkppkq/openai-register-go/internal/providerproxy"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/proxyroute"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// StartLinkBatchRequest 是“批量提链”的一次选择。
type StartLinkBatchRequest struct {
	Emails    []string `json:"emails"`
	Confirmed bool     `json:"confirmed"`
}

// LinkBatchSummary 立即返回父任务，以及因缺少 Access Token 而跳过的账号。
type LinkBatchSummary struct {
	Job     JobView  `json:"job"`
	Skipped []string `json:"skipped"`
}

type linkBatchAccount struct {
	Account     models.MailAccount
	AccessToken string
}

// linkAttemptFunc 是真实 OPLL 调用的测试缝。正式值会访问 OpenAI/Stripe；
// 单元测试必须替换它，避免创建真实 checkout。
type linkAttemptFunc func(
	ctx context.Context,
	account models.MailAccount,
	accessToken string,
	st settings.Settings,
	triple [3]string,
	log func(string),
) (*worker.PayLinkResult, error)

var runLinkAttempt linkAttemptFunc = generateOPLLLinkAttempt

// StartBatchGenerateLinks 从已保存 Session 的 Access Token 批量生成支付链接。
//
// 该操作会创建真实 checkout，后端必须收到展示过账号数量的明确确认。
func (a *App) StartBatchGenerateLinks(req StartLinkBatchRequest) (LinkBatchSummary, error) {
	if !req.Confirmed {
		return LinkBatchSummary{}, errors.New("批量生成支付链接前必须确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return LinkBatchSummary{}, err
	}
	st := settings.FromSnapshot(snapshot)
	mode, ok := models.PaymentModes[st.PaymentMode]
	if !ok {
		mode = models.PaymentModes[models.PaymentModeOrder[0]]
	}
	if mode.TrialShortLink {
		return LinkBatchSummary{}, errors.New("试用短链需要浏览器登录态，请使用“打开试用页”流程")
	}

	accounts, skipped, err := resolveLinkBatchSelection(snapshot, req.Emails)
	if err != nil {
		return LinkBatchSummary{}, err
	}
	if len(accounts) == 0 {
		return LinkBatchSummary{Skipped: skipped}, errors.New("选中的邮箱暂无 Access Token，请先执行“注册或登录并获取 Session”")
	}

	selection, err := proxyroute.PlanSettings(st, nil)
	if err != nil {
		return LinkBatchSummary{}, fmt.Errorf("准备支付链接代理失败: %w", err)
	}
	var source batch.ProxySource[[3]string]
	providerRoles := append([]proxypool.Role(nil), selection.ProviderRolesNeeded...)
	if len(providerRoles) > 0 {
		if err := validateProviderSettings(st); err != nil {
			return LinkBatchSummary{}, err
		}
		a.providerManager.UpdateMaxWorkers(st.LinkProxyPrecheckConcurrency)
		if err := a.providerManager.Configure(
			providerproxy.ConfigsFromSettings(st.ProviderProxyConfigs),
			st.LocalProxy,
		); err != nil {
			return LinkBatchSummary{}, fmt.Errorf("提供商代理配置无效: %w", err)
		}
		source = newProviderTripleSource(a.providerManager, selection)
	} else {
		triples := proxyroute.Triples(
			selection.CreateCandidates,
			selection.FollowupCandidates,
			selection.ApproveCandidates,
			len(accounts),
		)
		wantedPool := len(selection.CreateCandidates) > 0 ||
			len(selection.FollowupCandidates) > 0 ||
			len(selection.ApproveCandidates) > 0
		if wantedPool && len(triples) == 0 {
			return LinkBatchSummary{}, proxyroute.ErrProxyPoolExhausted
		}
		if len(triples) == 0 {
			// 没有动态代理时仍需提供一个“直连/仅本地代理”三元组。
			triples = make([][3]string, len(accounts))
		}
		source = newLinkTripleSource(triples)
	}

	ctx, cancel := context.WithCancel(context.Background())
	parentID, err := a.registerJob(JobBatchLink, "", "", cancel)
	if err != nil {
		cancel()
		return LinkBatchSummary{}, err
	}
	if err := a.resetLinkAttemptCounts(accounts); err != nil {
		cancel()
		a.markJobFinished(parentID, nil, err, false)
		return LinkBatchSummary{}, err
	}

	log := a.jobLogger(parentID)
	for _, email := range skipped {
		log(fmt.Sprintf("批量提取跳过无 Access Token 邮箱: %s", email))
	}
	log(fmt.Sprintf(
		"批量并发提取选中长链启动: %d 个账号，单账号撞链并发=%d，每账号最多重试=%d",
		len(accounts),
		batch.ClampRaceConcurrency(st.LinkRaceConcurrency),
		batch.ClampAttemptLimit(st.LinkAttemptLimit),
	))

	view, _ := a.jobView(parentID)
	go func() {
		defer cancel()
		if len(providerRoles) > 0 {
			var labels []string
			for _, role := range providerRoles {
				labels = append(labels, providerproxy.RoleLabel(role))
			}
			log(fmt.Sprintf(
				"等待提供商代理池达到启动库存 %d: %s",
				providerproxy.LowWater,
				strings.Join(labels, "、"),
			))
			if !a.providerManager.WaitUntilReady(providerproxy.LowWater, ctx.Done(), providerRoles) {
				waitErr := ctx.Err()
				if waitErr == nil {
					waitErr = a.providerManager.LastError()
				}
				if waitErr == nil {
					waitErr = errors.New("等待提供商代理池时任务已停止")
				}
				for _, item := range accounts {
					_ = a.persistLinkTerminalStatus(item.Account.Email, batch.StatusProxyExhausted)
				}
				a.markJobFinished(parentID, nil, waitErr, ctx.Err() != nil)
				return
			}
			log("提供商代理池已达到启动库存，开始批量提链")
		}
		report := a.runLinkBatch(ctx, parentID, accounts, st, source, log)
		a.markJobFinished(parentID, report, linkBatchReportError(report), ctx.Err() != nil)
	}()
	return LinkBatchSummary{Job: view, Skipped: skipped}, nil
}

func resolveLinkBatchSelection(snapshot map[string]any, emails []string) ([]linkBatchAccount, []string, error) {
	all := accountsFromSnapshot(snapshot)
	byKey := make(map[string]models.MailAccount, len(all))
	for _, account := range all {
		byKey[strings.ToLower(models.NormalizeEmailAddress(account.Email))] = account
	}
	sessions := sessionResultsFromSnapshot(snapshot)

	var (
		out     []linkBatchAccount
		skipped []string
	)
	seen := map[string]bool{}
	for _, raw := range emails {
		key := strings.ToLower(models.NormalizeEmailAddress(raw))
		if key == "" {
			return nil, nil, errors.New("选择中包含空邮箱")
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("选择中有重复账号: %s", raw)
		}
		seen[key] = true
		account, ok := byKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("账号不存在: %s", raw)
		}
		payload, _ := sessions[account.Email].(map[string]any)
		token := strings.TrimSpace(settings.PyStr(payload["access_token"]))
		if token == "" {
			skipped = append(skipped, account.Email)
			continue
		}
		out = append(out, linkBatchAccount{Account: account, AccessToken: token})
	}
	return out, skipped, nil
}

func (a *App) resetLinkAttemptCounts(accounts []linkBatchAccount) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		counts := subMap(snapshot, "link_attempt_counts")
		if counts == nil {
			counts = map[string]any{}
		}
		for _, item := range accounts {
			counts[item.Account.Email] = 0
		}
		snapshot["link_attempt_counts"] = counts
		return snapshot, map[string]bool{}, nil
	})
}

func (a *App) runLinkBatch(
	ctx context.Context,
	parentID string,
	accounts []linkBatchAccount,
	st settings.Settings,
	source batch.ProxySource[[3]string],
	log func(string),
) batch.Report[linkBatchAccount] {
	jobs := make([]batch.Job[linkBatchAccount], 0, len(accounts))
	for _, item := range accounts {
		jobs = append(jobs, batch.Job[linkBatchAccount]{
			Key:     item.Account.Email,
			Payload: item,
		})
	}

	runner := batch.RunnerFunc[linkBatchAccount, [3]string](func(
		ctx context.Context,
		job batch.Job[linkBatchAccount],
		triple [3]string,
	) error {
		item := job.Payload
		result, err := runLinkAttempt(ctx, item.Account, item.AccessToken, st, triple, func(line string) {
			log(fmt.Sprintf("[%s] %s", item.Account.Email, line))
		})
		if err != nil {
			return err
		}
		if err := a.persistLinkAttemptSuccess(item.Account.Email, result); err != nil {
			return err
		}
		a.handleLinkSuccess(parentID, item.Account.Email)
		return nil
	})

	done := 0
	opts := batch.Options[linkBatchAccount, [3]string]{
		// Python 为每个账号起一个线程；Go 保留最多 30 个账号窗口，单账号内部
		// 的 Race 仍按设置并发，避免无界 goroutine 放大付费请求。
		Concurrency:  len(accounts),
		Race:         st.LinkRaceConcurrency,
		AttemptLimit: st.LinkAttemptLimit,
		Proxies:      source,
		Classify: func(err error) batch.Disposition {
			if opll.OpllIsNonRetryableLinkError(err) {
				return batch.Fail
			}
			return batch.Retry
		},
		FailureStatus: func(err error) string {
			if opll.OpllIsNonRetryableLinkError(err) {
				return models.ExceptionStatus(err, opll.OpllNonRetryableStatus(err))
			}
			return models.ExceptionStatus(err, batch.StatusAttemptsExhausted)
		},
		Messages: batch.LinkMessages(),
		Log: func(key, message string) {
			if key == "" {
				log(message)
				return
			}
			log(fmt.Sprintf("[%s] %s", key, message))
		},
		OnAttempt: func(email string, count int) {
			if err := a.persistLinkAttemptCount(email, count); err != nil {
				log(fmt.Sprintf("[%s] 保存撞链次数失败: %v", email, err))
			}
		},
		OnResult: func(result batch.Result[linkBatchAccount]) {
			done++
			a.setBatchProgress(parentID, len(accounts), done)
			if result.Status != "" && result.Outcome != batch.OutcomeSucceeded {
				if err := a.persistLinkTerminalStatus(result.Key, result.Status); err != nil {
					log(fmt.Sprintf("[%s] 保存批量提链状态失败: %v", result.Key, err))
				}
			}
		},
	}

	report := batch.Run(ctx, jobs, runner, opts)
	counts := report.Counts()
	log(fmt.Sprintf(
		"批量提链结束：成功 %d，失败 %d，代理耗尽 %d，达到上限 %d，已停止 %d",
		counts.Succeeded,
		counts.Failed,
		counts.ProxyExhausted,
		counts.AttemptsExhausted,
		counts.Cancelled,
	))
	return report
}

func linkBatchReportError(report batch.Report[linkBatchAccount]) error {
	counts := report.Counts()
	if counts.Succeeded > 0 || counts.Failed+counts.ProxyExhausted+counts.AttemptsExhausted == 0 {
		return nil
	}
	return fmt.Errorf(
		"批量提链全部失败：失败 %d，代理耗尽 %d，达到尝试上限 %d",
		counts.Failed,
		counts.ProxyExhausted,
		counts.AttemptsExhausted,
	)
}

func (a *App) persistLinkAttemptCount(email string, count int) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		counts := subMap(snapshot, "link_attempt_counts")
		if counts == nil {
			counts = map[string]any{}
		}
		counts[email] = count
		snapshot["link_attempt_counts"] = counts
		return snapshot, map[string]bool{}, nil
	})
}

func (a *App) persistLinkAttemptSuccess(email string, result *worker.PayLinkResult) error {
	if result == nil || strings.TrimSpace(result.URL) == "" {
		return errors.New("接口生成成功但没有返回支付链接")
	}
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		dirty := applyOutcome(snapshot, email, runOutcome{
			Status:  statusRelinkOK,
			Payload: resultPayload(result),
		})
		return snapshot, dirty, nil
	})
}

func (a *App) persistLinkTerminalStatus(email, status string) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		applyOutcome(snapshot, email, runOutcome{Status: status})
		return snapshot, map[string]bool{}, nil
	})
}

// linkTripleSource 是 Python queue.Queue 的并发安全等价物。
type linkTripleSource struct {
	mu    sync.Mutex
	items [][3]string
}

func newLinkTripleSource(items [][3]string) *linkTripleSource {
	return &linkTripleSource{items: append([][3]string(nil), items...)}
}

func (s *linkTripleSource) Take(ctx context.Context) ([3]string, bool) {
	var zero [3]string
	if ctx.Err() != nil {
		return zero, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return zero, false
	}
	item := s.items[0]
	s.items = s.items[1:]
	return item, true
}

func (s *linkTripleSource) Recycle(item [3]string) {
	s.mu.Lock()
	s.items = append(s.items, item)
	s.mu.Unlock()
}

// providerTripleSource 按固定 create→followup→approve 顺序取三段代理。
// 提供商候选是一次性会话，因此本类型故意不实现 Recycle。
type providerTripleSource struct {
	manager *providerproxy.Manager
	roles   map[proxypool.Role]bool
	fixed   map[proxypool.Role]string

	mu     sync.Mutex
	manual map[proxypool.Role][]string
}

func newProviderTripleSource(
	manager *providerproxy.Manager,
	selection proxyroute.Selection,
) *providerTripleSource {
	roles := make(map[proxypool.Role]bool, len(selection.ProviderRolesNeeded))
	for _, role := range selection.ProviderRolesNeeded {
		roles[role] = true
	}
	return &providerTripleSource{
		manager: manager,
		roles:   roles,
		fixed: map[proxypool.Role]string{
			proxypool.RoleCreate:   selection.ReuseCreate,
			proxypool.RoleFollowup: selection.ReuseFollowup,
			proxypool.RoleApprove:  selection.ReuseApprove,
		},
		manual: map[proxypool.Role][]string{
			proxypool.RoleCreate:   append([]string(nil), selection.CreateCandidates...),
			proxypool.RoleFollowup: append([]string(nil), selection.FollowupCandidates...),
			proxypool.RoleApprove:  append([]string(nil), selection.ApproveCandidates...),
		},
	}
}

func (s *providerTripleSource) Take(ctx context.Context) ([3]string, bool) {
	var zero [3]string
	if ctx.Err() != nil || s.manager == nil {
		return zero, false
	}
	deadline := time.Now().Add(providerproxy.TakeTimeout)
	selected := map[proxypool.Role]string{}
	for _, role := range providerproxy.Roles {
		value := proxypool.NormalizeProxyURL(s.fixed[role])
		if value == "" && s.roles[role] {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return zero, false
			}
			candidate, ok := s.manager.Take(role, remaining, ctx.Done())
			if !ok {
				value = s.takeManual(role)
				if value == "" {
					return zero, false
				}
			} else {
				value = candidate.URL
			}
		} else if value == "" {
			value = s.takeManual(role)
		}
		switch role {
		case proxypool.RoleFollowup:
			if value == "" {
				value = selected[proxypool.RoleCreate]
			}
		case proxypool.RoleApprove:
			if value == "" {
				value = selected[proxypool.RoleFollowup]
			}
		}
		selected[role] = value
	}
	create, followup, approve := proxyroute.Triple(
		selected[proxypool.RoleCreate],
		selected[proxypool.RoleFollowup],
		selected[proxypool.RoleApprove],
	)
	return [3]string{create, followup, approve}, true
}

func (s *providerTripleSource) takeManual(role proxypool.Role) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.manual[role]
	if len(items) == 0 {
		return ""
	}
	value := items[0]
	s.manual[role] = items[1:]
	return value
}

func generateOPLLLinkAttempt(
	ctx context.Context,
	account models.MailAccount,
	accessToken string,
	st settings.Settings,
	triple [3]string,
	log func(string),
) (*worker.PayLinkResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg := st
	cfg.ReusePaymentProxy = triple[0]
	cfg.ReuseFollowupProxy = triple[1]
	cfg.ReuseApproveProxy = triple[2]
	cfg.PaymentDynamicProxy = ""
	cfg.FollowupDynamicProxy = ""
	cfg.ApproveDynamicProxy = ""
	routes, err := proxyroute.OpenSettings(cfg, proxyroute.LogFunc(log))
	if err != nil {
		return nil, err
	}
	defer routes.Close()

	extractor := worker.NewPayLinkExtractor(nil, nil, log)
	extractor.PaymentMode = st.PaymentMode
	extractor.TargetAmount = st.TargetAmount
	extractor.ForceLegacyPayPal = st.ForceLegacyPaypal
	extractor.RequireJapanExtractProxy = st.RequireJapanExtractProxy
	extractor.LinkCreateProxy = routes.Create
	extractor.LinkFollowupProxy = routes.Followup
	extractor.LinkApproveProxy = routes.Approve

	createURL, followupURL, approveURL := routes.RequestURLs()
	exits, err := extractor.DetectLinkProxyExits(createURL, followupURL, approveURL)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	mode, ok := models.PaymentModes[st.PaymentMode]
	if !ok {
		mode = models.PaymentModes[models.PaymentModeOrder[0]]
	}
	country := mode.Country
	if country == "" {
		country = "US"
	}
	currency := mode.Currency
	if currency == "" {
		currency = models.CurrencyForCountry(country)
	}
	provider := strings.ToLower(strings.TrimSpace(mode.PaymentProvider))
	if provider == "" {
		provider = "paypal"
	}

	var link *opll.LinkResult
	switch {
	case mode.ApplePayHosted:
		link, err = opll.GenerateOpllHostedLongLink(
			accessToken, country, currency,
			createURL, followupURL, approveURL, st.TargetAmount,
		)
	case provider == "gopay":
		link, err = opll.GenerateOpllGopayLongLink(
			accessToken, country, currency,
			createURL, followupURL, approveURL, st.TargetAmount,
		)
	default:
		link, err = opll.GenerateOpllPaypalLongLink(
			accessToken, country, currency,
			createURL, followupURL, approveURL, st.TargetAmount, st.ForceLegacyPaypal,
		)
	}
	if err != nil {
		log("接口提取长链失败: " + extractor.OpllErrorText(err))
		if opll.OpllIsNonRetryableLinkError(err) {
			if hint := opll.OpllNonRetryableHint(err); hint != "" {
				log(hint)
			}
		}
		return nil, err
	}
	if link == nil {
		return nil, errors.New("接口生成成功但没有返回结果")
	}

	longURL := strings.TrimSpace(link.ProviderRedirectURL)
	paymentType := "paypal_approve"
	switch {
	case mode.ApplePayHosted:
		longURL = firstLinkValue(link.LongURL, link.StripeHostedURL)
		paymentType = "apple_pay_hosted"
	case provider == "gopay":
		longURL = firstLinkValue(link.ProviderRedirectURL, link.LongURL)
		paymentType = "gopay_redirect"
	default:
		longURL = firstLinkValue(link.ProviderRedirectURL, link.LongURL)
		if longURL != "" && !opll.OpllIsPaypalSuccessURL(longURL) {
			return nil, fmt.Errorf("返回的不是可用 PayPal 跳转链接，拒绝保存: %.160s", longURL)
		}
	}
	if longURL == "" {
		return nil, errors.New("接口生成成功但没有返回支付链接")
	}

	createUsed, followupUsed, approveUsed := routes.UsedProxies()
	result := &worker.PayLinkResult{
		URL:                    longURL,
		CheckoutURL:            firstLinkValue(link.CheckoutURL, longURL),
		AccessToken:            accessToken,
		LinkProxy:              followupUsed,
		LinkProxyLabel:         linkProxyLabel(routes.Followup),
		LinkProxyExit:          exits.Followup,
		LinkCreateProxy:        createUsed,
		LinkCreateProxyLabel:   linkProxyLabel(routes.Create),
		LinkCreateProxyExit:    exits.Create,
		LinkFollowupProxy:      followupUsed,
		LinkFollowupProxyLabel: linkProxyLabel(routes.Followup),
		LinkFollowupProxyExit:  exits.Followup,
		LinkApproveProxy:       approveUsed,
		LinkApproveProxyLabel:  linkProxyLabel(routes.Approve),
		LinkApproveProxyExit:   exits.Approve,
		PaymentLinkType:        paymentType,
		AmountFields:           worker.AmountFieldsFromLinkResult(link),
	}
	log(fmt.Sprintf("[%s] 支付链接提取完成: %s", account.Email, longURL))
	return result, nil
}

func firstLinkValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func linkProxyLabel(cfg models.ProxyConfig) string {
	return fmt.Sprintf(
		"本地=%s -> 动态=%s",
		proxypool.MaskProxyURL(cfg.LocalProxy),
		proxypool.MaskProxyURL(cfg.DynamicProxy),
	)
}
