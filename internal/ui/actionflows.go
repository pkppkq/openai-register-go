package ui

// 本文件收口“全部操作”页中跨越多个基础入口的组合动作。每个入口只负责
// 编排已有 Go 能力；真实注册、浏览器和代理请求均保留可替换测试缝。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/providerproxy"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/proxyroute"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// DomainRandomRTRequest 是“域名邮箱随机取 RT”的付费确认。
type DomainRandomRTRequest struct {
	Confirmed bool `json:"confirmed"`
}

// DomainRandomRTResult 同时返回刚创建的邮箱与其后台任务。
type DomainRandomRTResult struct {
	Email string  `json:"email"`
	Job   JobView `json:"job"`
}

type domainRandomRTStarter func(*App, string) (JobView, error)

var startDomainRandomRTJob domainRandomRTStarter = func(a *App, email string) (JobView, error) {
	return a.startJobView(JobRegisterAndRT, email)
}

// StartDomainRandomRT 创建一个 Cloud Mail 随机邮箱并立即注册、授权取 RT。
// 该流程可能租用 SMSBower 号码，因此后端同样强制显式确认。
func (a *App) StartDomainRandomRT(req DomainRandomRTRequest) (DomainRandomRTResult, error) {
	if !req.Confirmed {
		return DomainRandomRTResult{}, errors.New("域名邮箱随机取 RT 可能租用短信号码，必须由用户明确确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return DomainRandomRTResult{}, err
	}
	st := settings.FromSnapshot(snapshot)
	cloud, err := alias.CloudMailSettingsFrom(st.CloudMailBase, st.CloudMailToken, st.CloudMailEnabled)
	if err != nil {
		return DomainRandomRTResult{}, fmt.Errorf("Cloud Mail 设置不可用：%w", err)
	}
	if !cloud.Enabled || strings.TrimSpace(cloud.Token) == "" {
		return DomainRandomRTResult{}, errors.New("请先启用 Cloud Mail 并填写 Token")
	}

	created, err := a.CreateDomainMailAccounts(DomainMailRequest{Count: 1})
	if err != nil {
		return DomainRandomRTResult{}, err
	}
	if len(created.Emails) != 1 {
		return DomainRandomRTResult{}, errors.New("未能创建随机域名邮箱")
	}
	email := created.Emails[0]
	job, err := startDomainRandomRTJob(a, email)
	if err != nil {
		return DomainRandomRTResult{Email: email}, fmt.Errorf("随机邮箱已创建，但启动取 RT 任务失败: %w", err)
	}
	return DomainRandomRTResult{Email: email, Job: job}, nil
}

// BatchRelinkRequest 是“批量重新获取”的账号选区。
type BatchRelinkRequest struct {
	Emails    []string `json:"emails"`
	Confirmed bool     `json:"confirmed"`
}

// BatchRelinkSummary 立即返回父任务及启动前跳过的账号。
type BatchRelinkSummary struct {
	Job     JobView  `json:"job"`
	Skipped []string `json:"skipped"`
}

// BatchRelinkAccountResult 是父任务保存的逐账号结果。
type BatchRelinkAccountResult struct {
	Email  string `json:"email"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type relinkAssignmentRunner func(
	context.Context,
	*App,
	string,
	models.MailAccount,
	string,
	[3]string,
	func(string),
) (any, string, error)

var runRelinkAssignment relinkAssignmentRunner = func(
	ctx context.Context,
	a *App,
	jobID string,
	account models.MailAccount,
	extractProxy string,
	triple [3]string,
	log func(string),
) (any, string, error) {
	return a.runJobWithProxyRoutes(ctx, jobID, JobRelink, account, &extractProxy, &triple, log)
}

// StartBatchRelink 为每个账号只执行一次重新登录与提链。三段支付代理在启动
// 前按账号固定分配，避免并发任务全部读取池首；最多同时运行 30 个账号。
func (a *App) StartBatchRelink(req BatchRelinkRequest) (BatchRelinkSummary, error) {
	if !req.Confirmed {
		return BatchRelinkSummary{}, errors.New("批量重新获取会真实登录并创建支付链接，必须由用户明确确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return BatchRelinkSummary{}, err
	}
	st := settings.FromSnapshot(snapshot)
	accounts, skipped, err := a.resolveBatchSelection(snapshot, req.Emails)
	if err != nil {
		return BatchRelinkSummary{}, err
	}
	if len(accounts) == 0 {
		return BatchRelinkSummary{Skipped: skipped}, errors.New("没有可执行的账号")
	}

	selection, err := proxyroute.PlanSettings(st, nil)
	if err != nil {
		return BatchRelinkSummary{}, fmt.Errorf("准备支付链接代理失败: %w", err)
	}
	wantedManual := len(selection.CreateCandidates) > 0 ||
		len(selection.FollowupCandidates) > 0 ||
		len(selection.ApproveCandidates) > 0
	manualTriples := proxyroute.Triples(
		selection.CreateCandidates,
		selection.FollowupCandidates,
		selection.ApproveCandidates,
		len(accounts),
	)
	if wantedManual && len(manualTriples) == 0 && len(selection.ProviderRolesNeeded) == 0 {
		return BatchRelinkSummary{}, proxyroute.ErrProxyPoolExhausted
	}
	if len(selection.ProviderRolesNeeded) > 0 {
		if err := validateProviderSettings(st); err != nil {
			return BatchRelinkSummary{}, err
		}
		a.providerManager.UpdateMaxWorkers(st.LinkProxyPrecheckConcurrency)
		if err := a.providerManager.Configure(
			providerproxy.ConfigsFromSettings(st.ProviderProxyConfigs),
			st.LocalProxy,
		); err != nil {
			return BatchRelinkSummary{}, fmt.Errorf("提供商代理配置无效: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	parentID, err := a.registerJob(JobBatchRelink, "", "", cancel)
	if err != nil {
		cancel()
		return BatchRelinkSummary{}, err
	}
	extractProxies, err := a.takeRelinkExtractProxies(len(accounts))
	if err != nil {
		cancel()
		a.markJobFinished(parentID, nil, err, false)
		return BatchRelinkSummary{}, err
	}
	a.setBatchProgress(parentID, len(accounts), 0)
	log := a.jobLogger(parentID)
	for _, email := range skipped {
		log(fmt.Sprintf("批量重新获取跳过未通过启动检查的账号: %s", email))
	}
	log(fmt.Sprintf("批量重新获取启动：%d 个账号，每个账号只尝试一次", len(accounts)))

	view, _ := a.jobView(parentID)
	go func() {
		defer cancel()
		triples, planErr := a.resolveBatchRelinkTriples(ctx, selection, st, len(accounts), manualTriples, log)
		if planErr != nil {
			a.markJobFinished(parentID, nil, planErr, ctx.Err() != nil)
			return
		}
		results := a.runBatchRelink(ctx, parentID, accounts, extractProxies, triples, log)
		succeeded := 0
		for _, result := range results {
			if result.Error == "" {
				succeeded++
			}
		}
		var runErr error
		if succeeded == 0 && ctx.Err() == nil {
			runErr = errors.New("批量重新获取全部失败")
		}
		log(fmt.Sprintf("批量重新获取结束：成功 %d，失败/停止 %d", succeeded, len(results)-succeeded))
		a.markJobFinished(parentID, results, runErr, ctx.Err() != nil)
	}()
	return BatchRelinkSummary{Job: view, Skipped: skipped}, nil
}

// takeRelinkExtractProxies 对注册代理池执行一次与 Python 相同的 TakeN，
// 并立即持久化轮转后的顺序。
func (a *App) takeRelinkExtractProxies(count int) ([]string, error) {
	taken := []string{}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		pool := proxypool.NewPool(st.DynamicProxies)
		taken = pool.TakeN(count)
		if len(taken) == 0 {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		st.DynamicProxies = pool.Text()
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return taken, err
}

func (a *App) resolveBatchRelinkTriples(
	ctx context.Context,
	selection proxyroute.Selection,
	st settings.Settings,
	count int,
	manual [][3]string,
	log func(string),
) ([][3]string, error) {
	if len(selection.ProviderRolesNeeded) == 0 {
		out := make([][3]string, count)
		for index := range out {
			if index < len(manual) {
				out[index] = manual[index]
			}
		}
		return out, nil
	}
	roles := append([]proxypool.Role(nil), selection.ProviderRolesNeeded...)
	if !a.providerManager.WaitUntilReady(providerproxy.LowWater, ctx.Done(), roles) {
		err := ctx.Err()
		if err == nil {
			err = a.providerManager.LastError()
		}
		if err == nil {
			err = errors.New("等待提供商代理池失败")
		}
		return nil, err
	}
	log("提供商代理池已达到启动库存，开始分配批量重新获取代理")

	needsProvider := map[proxypool.Role]bool{}
	for _, role := range roles {
		needsProvider[role] = true
	}
	candidates := map[proxypool.Role][]string{
		proxypool.RoleCreate:   selection.CreateCandidates,
		proxypool.RoleFollowup: selection.FollowupCandidates,
		proxypool.RoleApprove:  selection.ApproveCandidates,
	}
	fixed := map[proxypool.Role]string{
		proxypool.RoleCreate:   selection.ReuseCreate,
		proxypool.RoleFollowup: selection.ReuseFollowup,
		proxypool.RoleApprove:  selection.ReuseApprove,
	}

	out := make([][3]string, 0, count)
	for index := 0; index < count; index++ {
		selected := map[proxypool.Role]string{}
		for _, role := range providerproxy.Roles {
			value := fixed[role]
			if value == "" && needsProvider[role] {
				candidate, ok := a.providerManager.Take(role, providerproxy.TakeTimeout, ctx.Done())
				if !ok {
					return nil, fmt.Errorf("提供商%s代理不足", providerproxy.RoleLabel(role))
				}
				value = candidate.URL
			} else if value == "" {
				value = relinkCandidateAt(candidates[role], index)
			}
			if role == proxypool.RoleFollowup && value == "" {
				value = selected[proxypool.RoleCreate]
			}
			if role == proxypool.RoleApprove && value == "" {
				value = selected[proxypool.RoleFollowup]
			}
			selected[role] = value
		}
		create, followup, approve := proxyroute.Triple(
			selected[proxypool.RoleCreate],
			selected[proxypool.RoleFollowup],
			selected[proxypool.RoleApprove],
		)
		out = append(out, [3]string{create, followup, approve})
	}
	return out, nil
}

func relinkCandidateAt(values []string, index int) string {
	if index >= 0 && index < len(values) {
		return values[index]
	}
	if len(values) == 1 {
		return values[0]
	}
	return ""
}

func (a *App) runBatchRelink(
	ctx context.Context,
	parentID string,
	accounts []models.MailAccount,
	extractProxies []string,
	triples [][3]string,
	log func(string),
) []BatchRelinkAccountResult {
	results := make([]BatchRelinkAccountResult, len(accounts))
	limit := len(accounts)
	if limit > 30 {
		limit = 30
	}
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	done := 0

	for index, account := range accounts {
		if ctx.Err() != nil {
			results[index] = BatchRelinkAccountResult{
				Email: account.Email, Status: string(StatusCancelled), Error: ctx.Err().Error(),
			}
			continue
		}
		wg.Add(1)
		go func(index int, account models.MailAccount) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[index] = BatchRelinkAccountResult{
					Email: account.Email, Status: string(StatusCancelled), Error: ctx.Err().Error(),
				}
				return
			}

			childCtx, childCancel := context.WithCancel(ctx)
			defer childCancel()
			childID, err := a.registerJob(JobRelink, account.Email, parentID, childCancel)
			if err != nil {
				results[index] = BatchRelinkAccountResult{
					Email: account.Email, Status: string(StatusFailed), Error: err.Error(),
				}
			} else {
				extract := ""
				if index < len(extractProxies) {
					extract = extractProxies[index]
				}
				triple := [3]string{}
				if index < len(triples) {
					triple = triples[index]
				}
				childLog := a.jobLogger(childID)
				result, used, runErr := runRelinkAssignment(
					childCtx, a, childID, account, extract, triple, childLog,
				)
				cancelled := childCtx.Err() != nil && ctx.Err() != nil
				a.finishJob(childID, JobRelink, account, result, runErr, cancelled)
				a.dropFailedStandaloneProxy(used, runErr, childLog)
				status := StatusSucceeded
				errText := ""
				if cancelled {
					status = StatusCancelled
					errText = childCtx.Err().Error()
				} else if runErr != nil {
					status = StatusFailed
					errText = runErr.Error()
				}
				results[index] = BatchRelinkAccountResult{
					Email: account.Email, Status: string(status), Error: errText,
				}
			}
			progressMu.Lock()
			done++
			a.setBatchProgress(parentID, len(accounts), done)
			progressMu.Unlock()
		}(index, account)
	}
	wg.Wait()
	return results
}
