package ui

// 本文件承载手工代理池的两个后台操作，以及支付扩展目录选择。
// 检测函数保留可替换 seam，单元测试不得访问真实代理或 OpenAI。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/proxychain"
	"github.com/pkppkq/openai-register-go/internal/proxyhealth"
	"github.com/pkppkq/openai-register-go/internal/proxypool"
	"github.com/pkppkq/openai-register-go/internal/proxyroute"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

const (
	JobProxyPoolPrecheck JobKind = "proxy_pool_precheck"
	JobProxyPoolCleanup  JobKind = "proxy_pool_cleanup"
)

func init() {
	networkJobKinds[JobProxyPoolPrecheck] = true
	networkJobKinds[JobProxyPoolCleanup] = true
}

// ProxyPoolOperationRequest 是会消耗代理流量或改写代理池的确认请求。
type ProxyPoolOperationRequest struct {
	Confirmed bool `json:"confirmed"`
}

// ProxyRoleCheckResult 是预检中一个阶段的统计。
type ProxyRoleCheckResult struct {
	Role    string `json:"role"`
	Label   string `json:"label"`
	Total   int    `json:"total"`
	Checked int    `json:"checked"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
}

// ProxyPoolPrecheckResult 描述支付三段代理池的一轮只读预检。
type ProxyPoolPrecheckResult struct {
	UniqueChecked int                    `json:"uniqueChecked"`
	Passed        int                    `json:"passed"`
	Failed        int                    `json:"failed"`
	Roles         []ProxyRoleCheckResult `json:"roles"`
	Failures      []ProxyFailure         `json:"failures"`
}

// ProxyFailure 是经过掩码处理的失败明细。
type ProxyFailure struct {
	Proxy  string `json:"proxy"`
	Detail string `json:"detail"`
}

// ProxyPoolCleanupResult 描述清理前后的数量。RemovedEntries 是四个池中
// 实际删除的行数，同一个失效节点出现在多个池时会大于 FailedProxies。
type ProxyPoolCleanupResult struct {
	UniqueChecked  int            `json:"uniqueChecked"`
	Passed         int            `json:"passed"`
	FailedProxies  int            `json:"failedProxies"`
	RemovedEntries int            `json:"removedEntries"`
	RemovedByRole  map[string]int `json:"removedByRole"`
	Failures       []ProxyFailure `json:"failures"`
}

type proxyHealthCheckFunc func(
	context.Context,
	string,
	string,
	bool,
	func(string),
) models.ProxyHealthResult

var runManualProxyHealthCheck proxyHealthCheckFunc = detectManualProxyHealth
var proxyCleanupRetryDelay = 600 * time.Millisecond

// CountProxyPoolText 返回与 Python parse_proxy_pool_text 完全一致的实际条目数。
// 前端用它刷新“剩余 N”，不能按非空行估算：一行可能包含多个代理。
func (a *App) CountProxyPoolText(text string) int {
	return len(proxypool.ParseProxyPoolText(text))
}

// OpenPaymentExtensionDirectory 打开原生目录选择器。返回空字符串表示取消。
func (a *App) OpenPaymentExtensionDirectory() (string, error) {
	if a.ctx == nil {
		return "", errors.New("窗口尚未就绪")
	}
	st, err := a.LoadSettings()
	if err != nil {
		return "", err
	}
	path, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:            "选择支付链接扩展目录",
		DefaultDirectory: strings.TrimSpace(st.PaypalExtensionDir),
	})
	if err != nil {
		return "", fmt.Errorf("选择扩展目录失败: %w", err)
	}
	return path, nil
}

// StartProxyPoolPrecheck 并发预检支付第一步、后续、Approve 三个手工代理池。
// 本操作只读，不会删除用户配置；但会真实消耗代理流量，因此要求确认。
func (a *App) StartProxyPoolPrecheck(req ProxyPoolOperationRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("代理池预检会真实连接代理并消耗流量，必须由用户明确确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return JobView{}, err
	}
	st := settings.FromSnapshot(snapshot)
	if st.ProxyRouteMode == settings.ProxyRouteModeLocalOnly {
		return JobView{}, errors.New("当前为“全走本地代理”，手工动态代理池已被忽略")
	}
	candidates, err := proxyPrecheckCandidates(st)
	if err != nil {
		return JobView{}, err
	}
	if len(candidates) == 0 {
		return JobView{}, errors.New("支付三段手工代理池均为空")
	}
	return a.startNetworkJobWithLogEmail(
		JobProxyPoolPrecheck,
		"proxy-pool-precheck",
		"",
		func(ctx context.Context, log func(string)) (any, error) {
			return runProxyPoolPrecheck(ctx, st, candidates, log), nil
		},
	)
}

// StartProxyPoolCleanup 低并发检测四个手工池；连续两次不可用的节点会从
// 四池全部移除。该修改不可由网络探测自动触发，必须先由用户确认。
func (a *App) StartProxyPoolCleanup(req ProxyPoolOperationRequest) (JobView, error) {
	if !req.Confirmed {
		return JobView{}, errors.New("清理会从四个手工代理池移除失效节点，必须由用户明确确认")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return JobView{}, err
	}
	st := settings.FromSnapshot(snapshot)
	proxies := allManualDynamicProxies(st)
	if len(proxies) == 0 {
		return JobView{}, errors.New("四个手工代理池均为空")
	}
	return a.startNetworkJobWithLogEmail(
		JobProxyPoolCleanup,
		"proxy-pool-cleanup",
		"",
		func(ctx context.Context, log func(string)) (any, error) {
			return a.runProxyPoolCleanup(ctx, st, proxies, log)
		},
	)
}

type proxyRoleCandidate struct {
	Role  proxypool.Role
	Label string
	Proxy string
}

func proxyPrecheckCandidates(st settings.Settings) ([]proxyRoleCandidate, error) {
	// 复用代理不属于三个可编辑池；预检只检查池内容，所以先清空复用值。
	planSettings := st
	planSettings.ReusePaymentProxy = ""
	planSettings.ReuseFollowupProxy = ""
	planSettings.ReuseApproveProxy = ""
	selection, err := proxyroute.PlanSettings(planSettings, nil)
	if err != nil {
		return nil, fmt.Errorf("准备支付代理池预检失败: %w", err)
	}
	limit := st.LinkProxyPrecheckLimit
	if limit < 1 {
		limit = settings.DefaultLinkProxyPrecheckLimit
	}
	roles := []struct {
		role  proxypool.Role
		label string
		items []string
	}{
		{proxypool.RoleCreate, proxyroute.StageLabelCreate, selection.CreateCandidates},
		{proxypool.RoleFollowup, proxyroute.StageLabelFollowup, selection.FollowupCandidates},
		{proxypool.RoleApprove, proxyroute.StageLabelApprove, selection.ApproveCandidates},
	}
	out := make([]proxyRoleCandidate, 0)
	for _, role := range roles {
		count := min(limit, len(role.items))
		for _, value := range role.items[:count] {
			if normalized := proxypool.NormalizeProxyURL(value); normalized != "" {
				out = append(out, proxyRoleCandidate{
					Role: role.role, Label: role.label, Proxy: normalized,
				})
			}
		}
	}
	return out, nil
}

func runProxyPoolPrecheck(
	ctx context.Context,
	st settings.Settings,
	candidates []proxyRoleCandidate,
	log func(string),
) ProxyPoolPrecheckResult {
	unique := uniqueCandidateProxies(candidates)
	concurrency := min(max(1, st.LinkProxyPrecheckConcurrency), len(unique))
	if log != nil {
		log(fmt.Sprintf(
			"支付代理池出口预检启动: 唯一节点=%d，并发=%d，每池上限=%d",
			len(unique), concurrency, st.LinkProxyPrecheckLimit,
		))
	}
	health := checkProxyList(ctx, st.LocalProxy, unique, concurrency, true, log)

	result := ProxyPoolPrecheckResult{
		UniqueChecked: len(unique),
		Roles:         proxyRoleResultSkeleton(candidates),
		Failures:      []ProxyFailure{},
	}
	seenFailure := map[string]bool{}
	for _, candidate := range candidates {
		value := health[candidate.Proxy]
		passed := value.Success
		if candidate.Role == proxypool.RoleCreate &&
			st.RequireJapanExtractProxy &&
			!strings.EqualFold(strings.TrimSpace(value.Country), "JP") {
			passed = false
		}
		for index := range result.Roles {
			if result.Roles[index].Role != string(candidate.Role) {
				continue
			}
			result.Roles[index].Checked++
			if passed {
				result.Roles[index].Passed++
			} else {
				result.Roles[index].Failed++
			}
			break
		}
		if passed || seenFailure[candidate.Proxy] {
			continue
		}
		seenFailure[candidate.Proxy] = true
		detail := value.Summary()
		if candidate.Role == proxypool.RoleCreate &&
			st.RequireJapanExtractProxy &&
			value.Success &&
			!strings.EqualFold(strings.TrimSpace(value.Country), "JP") {
			detail = "第一步代理出口不是日本: " + detail
		}
		result.Failures = append(result.Failures, ProxyFailure{
			Proxy: proxypool.MaskProxyURL(candidate.Proxy), Detail: detail,
		})
	}
	result.Failed = len(result.Failures)
	result.Passed = result.UniqueChecked - result.Failed
	if log != nil {
		log(fmt.Sprintf("支付代理池出口预检完成: 通过=%d，失败=%d", result.Passed, result.Failed))
		for _, failure := range result.Failures[:min(8, len(result.Failures))] {
			log(fmt.Sprintf("无效支付代理: %s => %s", failure.Proxy, failure.Detail))
		}
		if len(result.Failures) > 8 {
			log(fmt.Sprintf("另有 %d 个失败明细已省略", len(result.Failures)-8))
		}
	}
	return result
}

func (a *App) runProxyPoolCleanup(
	ctx context.Context,
	st settings.Settings,
	proxies []string,
	log func(string),
) (ProxyPoolCleanupResult, error) {
	if log != nil {
		log(fmt.Sprintf("手工代理池清理启动: 唯一节点=%d，并发=4，每条最多检测 2 次", len(proxies)))
	}
	health := checkProxyListWithAttempts(ctx, st.LocalProxy, proxies, min(4, len(proxies)), 2, log)
	failed := make([]string, 0)
	result := ProxyPoolCleanupResult{
		UniqueChecked: len(proxies),
		RemovedByRole: map[string]int{},
		Failures:      []ProxyFailure{},
	}
	for _, proxy := range proxies {
		value := health[proxy]
		// 403 仍是一次有效 HTTP 响应；只有完全没有 ChatGPT 响应才删除。
		if value.Success && value.ChatGPTStatus != 0 {
			result.Passed++
			continue
		}
		failed = append(failed, proxy)
		result.Failures = append(result.Failures, ProxyFailure{
			Proxy: proxypool.MaskProxyURL(proxy), Detail: value.Summary(),
		})
	}
	result.FailedProxies = len(failed)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(failed) > 0 {
		counts, total, err := a.removeManualProxiesEverywhere(failed)
		if err != nil {
			return result, err
		}
		result.RemovedEntries = total
		for role, count := range counts {
			result.RemovedByRole[string(role)] = count
		}
	}
	if log != nil {
		log(fmt.Sprintf(
			"手工代理池清理完成: 通过=%d，失效节点=%d，移除池条目=%d",
			result.Passed, result.FailedProxies, result.RemovedEntries,
		))
		for _, failure := range result.Failures[:min(12, len(result.Failures))] {
			log(fmt.Sprintf("无效代理: %s => %s", failure.Proxy, failure.Detail))
		}
		if len(result.Failures) > 12 {
			log(fmt.Sprintf("另有 %d 个无效代理明细已省略", len(result.Failures)-12))
		}
	}
	return result, nil
}

func uniqueCandidateProxies(candidates []proxyRoleCandidate) []string {
	values := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.Proxy == "" || seen[candidate.Proxy] {
			continue
		}
		seen[candidate.Proxy] = true
		values = append(values, candidate.Proxy)
	}
	return values
}

func proxyRoleResultSkeleton(candidates []proxyRoleCandidate) []ProxyRoleCheckResult {
	roles := []struct {
		role  proxypool.Role
		label string
	}{
		{proxypool.RoleCreate, proxyroute.StageLabelCreate},
		{proxypool.RoleFollowup, proxyroute.StageLabelFollowup},
		{proxypool.RoleApprove, proxyroute.StageLabelApprove},
	}
	out := make([]ProxyRoleCheckResult, 0, len(roles))
	for _, role := range roles {
		total := 0
		for _, candidate := range candidates {
			if candidate.Role == role.role {
				total++
			}
		}
		out = append(out, ProxyRoleCheckResult{
			Role: string(role.role), Label: role.label, Total: total,
		})
	}
	return out
}

func allManualDynamicProxies(st settings.Settings) []string {
	values := []string{}
	for _, text := range []string{
		st.DynamicProxies,
		st.PaymentDynamicProxy,
		st.FollowupDynamicProxy,
		st.ApproveDynamicProxy,
	} {
		values = append(values, proxypool.ParseProxyPoolText(text)...)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		normalized := proxypool.NormalizeProxyURL(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

func checkProxyList(
	ctx context.Context,
	local string,
	proxies []string,
	concurrency int,
	full bool,
	log func(string),
) map[string]models.ProxyHealthResult {
	return checkProxyListUsing(
		ctx, local, proxies, concurrency, full, log, runManualProxyHealthCheck,
	)
}

func checkProxyListUsing(
	ctx context.Context,
	local string,
	proxies []string,
	concurrency int,
	full bool,
	log func(string),
	checker proxyHealthCheckFunc,
) map[string]models.ProxyHealthResult {
	results := make([]models.ProxyHealthResult, len(proxies))
	if len(proxies) == 0 {
		return map[string]models.ProxyHealthResult{}
	}
	concurrency = min(max(1, concurrency), len(proxies))
	indexes := make(chan int)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range indexes {
				if err := ctx.Err(); err != nil {
					results[index] = models.ProxyHealthResult{
						Success: false, FailedStage: "任务", Error: err.Error(),
					}
					continue
				}
				results[index] = checker(ctx, local, proxies[index], full, log)
			}
		}()
	}
	for index := range proxies {
		indexes <- index
	}
	close(indexes)
	wg.Wait()

	out := make(map[string]models.ProxyHealthResult, len(proxies))
	for index, proxy := range proxies {
		out[proxy] = results[index]
	}
	return out
}

func checkProxyListWithAttempts(
	ctx context.Context,
	local string,
	proxies []string,
	concurrency int,
	attempts int,
	log func(string),
) map[string]models.ProxyHealthResult {
	if attempts < 1 {
		attempts = 1
	}
	original := runManualProxyHealthCheck
	wrapped := func(
		ctx context.Context,
		local string,
		proxy string,
		_ bool,
		log func(string),
	) models.ProxyHealthResult {
		last := models.ProxyHealthResult{
			Success: false, FailedStage: "代理", Error: "未执行检测",
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return models.ProxyHealthResult{Success: false, FailedStage: "任务", Error: err.Error()}
			}
			last = original(ctx, local, proxy, false, log)
			if last.Success && last.ChatGPTStatus != 0 {
				return last
			}
			if attempt < attempts && !waitProxyCleanupRetry(ctx) {
				return models.ProxyHealthResult{Success: false, FailedStage: "任务", Error: ctx.Err().Error()}
			}
		}
		return last
	}
	return checkProxyListUsing(ctx, local, proxies, concurrency, false, log, wrapped)
}

func waitProxyCleanupRetry(ctx context.Context) bool {
	if proxyCleanupRetryDelay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(proxyCleanupRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func detectManualProxyHealth(
	ctx context.Context,
	local string,
	dynamic string,
	full bool,
	log func(string),
) models.ProxyHealthResult {
	if err := ctx.Err(); err != nil {
		return models.ProxyHealthResult{Success: false, FailedStage: "任务", Error: err.Error()}
	}
	server := proxychain.New(local, dynamic, proxychain.LogFunc(log))
	if err := server.Start(); err != nil {
		return models.ProxyHealthResult{Success: false, FailedStage: "代理链", Error: err.Error()}
	}
	defer server.Close()
	proxyURL := server.URL()
	if proxyURL == "" {
		proxyURL = proxypool.NormalizeProxyURL(dynamic)
	}
	if full {
		return proxyhealth.DetectProxyHealth(proxyURL, 15)
	}
	return proxyhealth.DetectLocalProxyHealth(proxyURL, 10)
}

func (a *App) removeManualProxiesEverywhere(
	failed []string,
) (map[proxypool.Role]int, int, error) {
	var (
		counts map[proxypool.Role]int
		total  int
	)
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		pools := proxypool.NewSet()
		pools.SetText(proxypool.RoleRegister, st.DynamicProxies)
		pools.SetText(proxypool.RoleCreate, st.PaymentDynamicProxy)
		pools.SetText(proxypool.RoleFollowup, st.FollowupDynamicProxy)
		pools.SetText(proxypool.RoleApprove, st.ApproveDynamicProxy)
		counts, total = pools.RemoveEverywhere(failed)
		if total == 0 {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		st.DynamicProxies = pools.Text(proxypool.RoleRegister)
		st.PaymentDynamicProxy = pools.Text(proxypool.RoleCreate)
		st.FollowupDynamicProxy = pools.Text(proxypool.RoleFollowup)
		st.ApproveDynamicProxy = pools.Text(proxypool.RoleApprove)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return counts, total, err
}
