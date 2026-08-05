/*
  主壳的安全接线回归。

  这些断言只读取 App.svelte 源码，不渲染组件，也不导入 Wails bridge。目的不是
  测试 Svelte 编译器，而是锁住三个最容易在重排 UI 时退化的安全边界：
  批量提链只能进入 Go 端批量父任务；后端要求的高风险确认位必须由确认回调
  显式传 true；主壳不能绕过 lib/api.ts 直接调用生成绑定。
*/
import { describe, expect, it } from 'vitest'
// @ts-expect-error Vite/Vitest 在运行时原样加载 `?raw`；检查配置未引入 vite/client 声明。
import APP_SOURCE from '../../App.svelte?raw'

function sourceBetween(start: string, end: string): string {
  const from = APP_SOURCE.indexOf(start)
  const to = APP_SOURCE.indexOf(end, from + start.length)
  if (from < 0 || to < 0) throw new Error(`未找到 App.svelte 片段：${start} -> ${end}`)
  return APP_SOURCE.slice(from, to)
}

describe('App 批量任务接线', () => {
  it('Workbench 的多选提链只启动一个 Go 批量父任务', () => {
    const relink = sourceBetween('onrelink={(emails: string[]) =>', 'onstop={stopAll}')
    expect(relink).toContain("startLinkBatch(emails, '批量提链')")
    expect(relink).not.toContain('GenerateLinks(')
    expect(APP_SOURCE).toContain('StartBatchGenerateLinks({ emails, confirmed: true })')
  })

  it('Team 与普通账号统一进入 Go 批量父任务，由后端规范化类型后派发', () => {
    const batch = sourceBetween('async function startBatch(', '/** 单账号旧入口')
    expect(batch).toContain('StartBatchRegister({ emails, collectSession, confirmed: true })')
    expect(batch).not.toContain('StartRegister(')
    expect(batch).not.toContain('account_type ===')
    expect(APP_SOURCE).not.toContain('teamBatchProblem')
  })

  it('历史任务快照不会为每个终态任务重新读取全部账户', () => {
    const mountJobs = sourceBetween('ListJobs()', '.catch((e) =>')
    expect(mountJobs).toContain('upsertJob(view, false)')
    expect(mountJobs).not.toContain('loadAccounts()')
  })
})

describe('App 全部操作分组导航', () => {
  it('组内切换不会经过会重置为 account 的 NAV.actions 默认值', () => {
    const switcher = sourceBetween('function setActionGroup(', '// -- Loading')
    expect(switcher).toContain('activeActionGroup = next')
    expect(switcher).toContain("activePane = 'actions'")
    expect(switcher).not.toContain('navigate(')
  })
})

describe('App 高风险确认位', () => {
  it('只有确认回调构造 Team / 试用请求时才传后端确认位', () => {
    expect(APP_SOURCE).toMatch(/StartTeamInvite\(\{[\s\S]{0,260}confirmBillableSeat:\s*true/)
    expect(APP_SOURCE).toMatch(/StartTeamLeave\(\{[\s\S]{0,180}confirmed:\s*true/)
    expect(APP_SOURCE).toMatch(/StartTrialEligibility\(\{[\s\S]{0,220}confirmCheckout:\s*true/)
  })

  it('三个高风险调用附近都有 confirmThen 门', () => {
    for (const marker of ['StartTeamInvite({', 'StartTeamLeave({', 'StartTrialEligibility({']) {
      const call = APP_SOURCE.indexOf(marker)
      expect(call, marker).toBeGreaterThan(0)
      expect(APP_SOURCE.slice(Math.max(0, call - 900), call), marker).toContain('confirmThen(')
    }
  })

  it('六个组合操作都经过确认门并调用专用 Go 绑定', () => {
    for (const marker of [
      'StartDomainRandomRT({ confirmed: true })',
      'StartBatchRelink({ emails, confirmed: true })',
      'SwitchPaymentWindowProxy(email)',
      'StartTeamInviteScanJoin({ emails, confirmed: true })',
      'StartK12AcceptAndRefresh({',
      'StartK12RegisterAndJoin({',
    ]) {
      const call = APP_SOURCE.indexOf(marker)
      expect(call, marker).toBeGreaterThan(0)
    }
    expect(APP_SOURCE).not.toContain('UNAVAILABLE_FULL_ACTIONS')
  })

  it('支付窗口自动确认只在两处支付弹窗开放，并把本次选择传给后端双确认位', () => {
    expect(APP_SOURCE.match(/allowPaymentAutoConfirm:\s*true/g)).toHaveLength(2)

    const single = sourceBetween(
      'async function openPaymentWindow(',
      'async function openSelectedPaymentWindows(',
    )
    const batch = sourceBetween(
      'async function openSelectedPaymentWindows(',
      'function handleDetailAction(',
    )
    for (const flow of [single, batch]) {
      expect(flow).toContain('autoConfirm,')
      expect(flow).toContain('confirmAutoCharge: autoConfirm')
      expect(flow).not.toContain('autoConfirm: false')
      expect(flow).not.toContain('confirmAutoCharge: false')
    }

    expect(APP_SOURCE).toContain('{#key confirmRequest}')
    expect(APP_SOURCE).toContain('async function runConfirmed(autoConfirmPayment: boolean)')
  })
})

describe('App 桥接边界', () => {
  it('不直接导入生成的 ui/App 绑定', () => {
    expect(APP_SOURCE).not.toMatch(/wailsjs\/go\/ui\/App/)
    expect(APP_SOURCE).toContain("from './lib/api'")
  })
})

describe('App 事件并发边界', () => {
  it('结构化日志先进入独立环形缓冲，快照尚未返回时也不会丢失', () => {
    const handler = sourceBetween('EventsOn(EVENT_LOG_RECORD', 'EventsOn(EVENT_JOB')
    expect(handler).toContain('streamedLogRecords =')
    expect(handler.indexOf('streamedLogRecords =')).toBeLessThan(handler.indexOf('if (!logSnapshot) return'))
    expect(APP_SOURCE).toContain('mergeLogRecords(snapshot.global ?? [], streamedLogRecords')
  })

  it('任务进入终态时移除对应 Prompt 并释放有界队列槽位', () => {
    const settle = sourceBetween('function settleJobUI(', 'function upsertJob(')
    expect(settle).toContain('request.jobId !== view.id')
    expect(settle).toContain('resolveTerminalJob(view.id)')
    const promptHandler = sourceBetween('EventsOn(EVENT_PROMPT', 'EventsOn(EVENT_PROVIDER_PROXY_STATUS')
    expect(promptHandler).toContain('isTerminalJob(')
  })

  it('K12 多选按设置并发等待任务终态，停止全部会作废待派发项', () => {
    const bounded = sourceBetween('async function startBoundedNetworkTasks(', '// -- Export (S13)')
    expect(bounded).toContain('await waitForJobTerminal(view.id)')
    expect(bounded).toContain('generation === networkQueueGeneration')
    expect(APP_SOURCE).toContain(
      "startBoundedNetworkTasks(emails, 'K12 请求邀请', k12Concurrency",
    )
    const stop = sourceBetween('async function stopAll()', 'async function cancelJob(')
    expect(stop).toContain('networkQueueGeneration += 1')
    expect(stop).toContain('resolveAllTerminalWaiters()')
  })
})

describe('App 本地操作互斥与设置门', () => {
  it('别名和域名邮箱创建共用互斥锁，防止双击重复写入', () => {
    for (const [start, end] of [
      ['async function createPlusAliases()', 'async function createDomainMail()'],
      ['async function createDomainMail()', 'function openManualSession('],
    ] as const) {
      const operation = sourceBetween(start, end)
      expect(operation).toContain('if (localActionBusy) return')
      expect(operation).toContain('localActionBusy = true')
      expect(operation).toContain('localActionBusy = false')
    }
  })

  it('全部操作在设置成功读取前保持禁用', () => {
    const actions = sourceBetween('<ActionsFull', '/>')
    expect(actions).toContain('busy={busy || !settingsReady}')
  })

  it('右侧账户选择区可删除所选，并在任务或删除进行中禁用', () => {
    const dock = sourceBetween('<!-- S4 选择账户 dock.', '</aside>')
    expect(dock).toContain('>删除所选</button')
    expect(dock).toContain('disabled={selected.length === 0 || busy || accountDialogBusy}')
    expect(dock).toContain('onclick={() => void deleteSelectedAccounts()}')

    const deletion = sourceBetween('async function deleteSelectedAccounts()', 'async function clearAllAccounts()')
    expect(deletion).toContain('if (busy || accountDialogBusy) return')
    expect(deletion).toContain('window.confirm(')
    expect(deletion).toContain('DeleteAccounts(emails, true)')

    const busyState = sourceBetween('let busy = $derived(', '/** 批量任务可同时等待')
    expect(busyState).toContain('accountDialogBusy')
  })
})

describe('App 最终入口接线', () => {
  it('Cloud Mail 与 Turnstile 的可见控件会启动专用探测任务', () => {
    expect(APP_SOURCE).toContain('StartCloudMailProbe({')
    expect(APP_SOURCE).toContain('StartCloudMailTokenGeneration({')
    expect(APP_SOURCE).toContain('StartTurnstileProbe({ url })')
    expect(APP_SOURCE).toContain('oncloudtoken={generateCloudMailToken}')
    expect(APP_SOURCE).toContain('onturnstileprobe={probeTurnstile}')
  })

  it('支付目录选择、邮箱状态与音频按钮不再使用空处理器或旧占位状态', () => {
    expect(APP_SOURCE).toContain('onpickdir={choosePaymentExtensionDirectory}')
    expect(APP_SOURCE).toContain('status={mailboxStatus}')
    expect(APP_SOURCE).toContain('busy={mailboxBusy}')
    expect(APP_SOURCE).toContain('onrefreshdevices={refreshAudioDevices}')
    expect(APP_SOURCE).toContain('ontestsound={testSuccessSound}')
    expect(APP_SOURCE).not.toContain('status="绑定不可用"')
    expect(APP_SOURCE).not.toContain('onrefreshdevices={() => {}}')
    expect(APP_SOURCE).not.toContain('ontestsound={() => {}}')
  })

  it('代理池剩余数量使用 Go 的 Python 兼容解析器并防抖刷新', () => {
    expect(APP_SOURCE).toContain('CountProxyPoolText(text)')
    expect(APP_SOURCE).toContain('const PROXY_POOL_COUNT_DEBOUNCE_MS = 30')
    expect(APP_SOURCE).toContain('counts={proxyPoolCounts}')
  })

  it('sub2api 缺 RT 时先 OAuth，成功后自动继续原导出', () => {
    const flow = sourceBetween('async function startSub2APIExport()', 'async function launchSub2APIExport(')
    expect(flow).toContain('StartOAuthAuthorizeRT({')
    expect(flow).toContain('pendingJobActions.set(summary.job.id')
    expect(flow).toContain('await launchSub2APIExport(emails)')
  })

  it('ZIP 对齐 Python，校验后直接保存而不打开文本预览', () => {
    const refresh = sourceBetween('function requestConversionRefresh(', 'function requestExportPreview(')
    expect(refresh).toContain("after === 'conversion_zip'")
    expect(refresh).toContain('saveExportDirect(after, selection)')
  })
})
