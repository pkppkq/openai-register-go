<script lang="ts">
  // Shell. UI_SPEC §S1: sidebar (142px) + content + right dock (320px), with a
  // taskbar across the bottom.
  //
  // The shell owns everything shared: the account list, the selection (§0.4 —
  // the dock is a second, mirrored view of it), the four persisted view fields
  // (分组 / 状态 / sort column / direction) and every backend call. The panes
  // are pure views over props.
  import { onMount } from 'svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import { NAV, type NavEntry, type PaneKey } from './lib/nav'
  import Workbench, {
    GROUP_ALL,
    SORT_COLUMNS,
    STATUS_FILTER_ALL,
    STATUS_FILTER_OPTIONS,
    matchesGroup,
    matchesStatusFilter,
    type SortColumn,
    type SortDirection,
  } from './lib/pages/Workbench.svelte'
  import ImportAccounts from './lib/pages/ImportAccounts.svelte'
  import {
    PROXY_POOL_ORDER,
    PROXY_ROUTE_MODE_DEFAULT,
    type ProxyPoolKey,
    type ProxyPools,
    type ProxyRouteMode,
  } from './lib/pages/ProxySettings.svelte'
  import ProxySettingsFull, {
    type LinkReuseProxies,
    type ProviderProxyStageStatus,
    type ProxyFullAction,
    type ProxyFullChange,
  } from './lib/pages/ProxySettingsFull.svelte'
  import PhoneSms, {
    SMSBOWER_DEFAULTS,
    TURNSTILE_DEFAULTS,
    normalizeSmsBower,
    validateSmsBower,
    type SmsBowerSettings,
    type TurnstileSettings,
  } from './lib/pages/PhoneSms.svelte'
  import PhonePoolFull, {
    type PhonePoolAction,
    type PhonePoolEntry,
  } from './lib/pages/PhonePoolFull.svelte'
  import PaymentProfile, { PAYPAL_DEFAULTS, type PaypalSettings } from './lib/pages/PaymentProfile.svelte'
  import PaymentCardPoolFull, {
    type PaymentCardPoolAction,
  } from './lib/pages/PaymentCardPoolFull.svelte'
  import ActionsFull, {
    DEFAULT_K12_WORKSPACE_ID,
    SESSION_CONVERT_FORMATS,
    type ActionGroupKey,
    type FullActionKey,
    type SessionConvertFormat,
  } from './lib/pages/ActionsFull.svelte'
  import AccountDetailsFull, {
    type AccountDetailAction,
    type AccountDetailTab,
    type DetailLogLevel,
    type DetailLogLine,
    type SessionPayload,
  } from './lib/pages/AccountDetailsFull.svelte'
  import GeneralSettings, {
    AUDIO_DEFAULT_DEVICE_LABEL,
    SOUND_DEFAULTS,
    type SoundSettings,
  } from './lib/pages/GeneralSettings.svelte'
  import JobPane from './lib/pages/JobPane.svelte'
  import ExportPane from './lib/pages/ExportPane.svelte'
  import type { CloudMailSettings } from './lib/pages/MailImport.svelte'
  import AccountManagementDialog, {
    type ManagedAccountType,
  } from './lib/AccountManagementDialog.svelte'
  import GroupManagementDialog, {
    type GroupDialogMode,
    type GroupOperation,
  } from './lib/GroupManagementDialog.svelte'
  import AutoClassifyDialog, {
    type AutoClassifyMode,
    type AutoClassifyScope,
  } from './lib/AutoClassifyDialog.svelte'
  import ManualSessionDialog, {
    type ManualSessionMode,
    type ManualSessionSubmission,
  } from './lib/ManualSessionDialog.svelte'
  import MailboxManagerDialog, {
    type MailboxDialogAction,
    type MailboxMessage,
  } from './lib/MailboxManagerDialog.svelte'
  import ProviderProxyDialog, {
    EMPTY_PROVIDER_PROXY_CONFIG,
    type ProviderProxyConfig,
    type ProviderProxyRole,
  } from './lib/ProviderProxyDialog.svelte'
  import ExportPreviewDialog from './lib/ExportPreviewDialog.svelte'
  import ConfirmAction, { type ConfirmRequest } from './lib/ConfirmAction.svelte'
  import PromptDialog from './lib/PromptDialog.svelte'
  import {
    Environment,
    LoadSummary,
    ListAccounts,
    ALL_ACCOUNTS,
    ImportAccounts as ImportAccountsCall,
    OpenAccountFile,
    LoadSettings,
    PatchSettings,
    AutoClassifyAccounts,
    ClearAccountWorkflow,
    ClearAccounts,
    ClearPhones,
    CountProxyPoolText,
    CopySessionConversion,
    CreateAccountGroup,
    CreateDomainMailAccounts,
    CreatePlusAliasAccounts,
    DeleteAccountGroup,
    DeleteAccounts,
    ImportPaymentCards,
    ImportPhones,
    ListJobs,
    ListPhones,
    ListMailboxFolders,
    ListMailboxMessages,
    GetMailboxMessage,
    ExtractMailboxCode,
    ExtractMailboxInviteLink,
    ListPaymentCards,
    LoadLogs,
    MergeManualSession,
    MoveAccountsToGroup,
    ProviderProxyStatuses,
    RenameAccountGroup,
    ReplaceManualSession,
    ResetPaymentCards,
    ResetPhones,
    SelectLogAccount,
    SetAccountType,
    StartBatchRegister,
    StartBatchGenerateLinks,
    StartBatchRelink,
    GenerateLinks,
    StartDeactivationScan,
    StartCloudMailProbe,
    StartCloudMailTokenGeneration,
    StartDomainRandomRT,
    StartK12AcceptAndRefresh,
    StartK12RegisterAndJoin,
    StartK12RequestInvite,
    StartRefreshSession,
    StartRefreshAccountType,
    StartSMSBowerReadTest,
    StartTeamInvite,
    StartTeamLeave,
    StartTrialEligibility,
    StartTurnstileProbe,
    StartAcceptWorkspaceInvite,
    StartOpenPaymentWindow,
    StartOpenPaymentWindows,
    StartManualPhoneCode,
    StartProtocolRegisterSession,
    StartOAuthAuthorizeRT,
    StartKeepLogin,
    StartOpenSessionReader,
    StartExternalOAuth,
    StartManualLoginCode,
    StartCPAExportRefresh,
    StartSub2APIExport,
    SaveSub2APIExport,
    StartTeamInviteScanJoin,
    SwitchPaymentWindowProxy,
    OpenPaymentExtensionDirectory,
    StartProxyPoolPrecheck,
    StartProxyPoolCleanup,
    ApplyProviderProxySettings,
    GetNetworkJobResult,
    GetAccountDetails,
    StopAll,
    CancelJob,
    AnswerPrompt,
    PreviewExport,
    SaveExport,
    CopyExportPreview,
    ExportMissingRT,
    PrepareSub2APIExport,
    EVENT_LOG,
    EVENT_LOG_RECORD,
    EVENT_JOB,
    EVENT_PROMPT,
    EVENT_PROVIDER_PROXY_STATUS,
    type AccountRow,
    type AccountDetails,
    type ExportKind,
    type ExportPreview,
    type LogRecord,
    type LogSnapshot,
    type MissingRTView,
    type NetworkJobResult,
    type PaymentCardView,
    type ProviderProxyStatusEvent,
    type ProviderProxyStatusView,
    type Sub2APIPlan,
    type Env,
    type JobView,
    type PromptRequest,
    type Settings,
    type SettingsPatch,
    type StateSummary,
  } from './lib/api'
  import { ClipboardSetText, EventsOn } from '../wailsjs/runtime/runtime'

  let active = $state('workbench')
  let activePane = $state<PaneKey>('workbench')
  let activeActionGroup = $state<ActionGroupKey>('account')
  let env = $state<Env | null>(null)
  let summary = $state<StateSummary | null>(null)
  let error = $state('')
  let notice = $state('')
  let logs = $state<string[]>([])

  // 全局错误出现时立即清掉上一条成功提示，避免页面同时挂着红、绿两条互相
  // 矛盾的结果。成功路径仍显式清空 error，保留具体操作自己的文案。
  $effect(() => {
    if (error && notice) notice = ''
  })

  let rows = $state<AccountRow[]>([])
  /** `AccountPage.total`; equal to rows.length while we fetch ALL_ACCOUNTS. */
  let total = $state(0)
  let knownGroups = $state<string[]>([])
  let accountsError = $state('')
  let accountsLoading = $state(true)
  /** Selected row keys — lowercased emails (UI_SPEC §0.3), not list indices. */
  let selected = $state<string[]>([])
  /** Rows the account table is currently showing; drives 显示 N. */
  let shown = $state(0)

  // The four persisted view fields (§3). Seeded from LoadSettings; every change
  // goes back through persist.
  let group = $state(GROUP_ALL)
  let status = $state(STATUS_FILTER_ALL)
  let sortColumn = $state<SortColumn>('email')
  let sortDirection = $state<SortDirection>('custom')
  /**
   * False until LoadSettings has succeeded once。虽然 PatchSettings 只更新指定
   * 字段，仍不允许一个尚未完成初始化的控件把默认值误写进真实 state.json。
   */
  let settingsReady = $state(false)
  let settingsError = $state('')

  // S17 代理 (§6: 本地代理, 代理模式 and the four manual pools — nothing else).
  let localProxy = $state('')
  /** Raw `proxy_route_mode`; the pane coerces an unknown value to 照旧. */
  let routeMode = $state<string>(PROXY_ROUTE_MODE_DEFAULT)
  let pools = $state<ProxyPools>({ register: '', create: '', followup: '', approve: '' })
  let proxyPoolCounts = $state<Partial<Record<ProxyPoolKey, number>>>({})
  let providers = $state<Record<ProviderProxyRole, ProviderProxyConfig>>({
    create: { ...EMPTY_PROVIDER_PROXY_CONFIG },
    followup: { ...EMPTY_PROVIDER_PROXY_CONFIG },
    approve: { ...EMPTY_PROVIDER_PROXY_CONFIG },
  })
  let providerStatuses = $state<Partial<Record<ProviderProxyRole, ProviderProxyStageStatus>>>({})
  let proxyRegion = $state('不限')
  let raceConcurrency = $state(1)
  let precheckLimit = $state(500)
  let precheckConcurrency = $state(100)
  let reuseProxies = $state<LinkReuseProxies>({ create: '', followup: '', approve: '' })
  let requireJapan = $state(false)
  let registerWithPaymentProxy = $state(false)
  let forceLegacyPaypal = $state(false)
  let providerDialogOpen = $state(false)
  let providerDialogRole = $state<ProviderProxyRole>('create')
  let providerBusy = $state(false)
  let providerError = $state('')

  // S15 手机与接码 (§6: the SMSBower block only).
  let smsbower = $state<SmsBowerSettings>({ ...SMSBOWER_DEFAULTS })
  let smsError = $state('')
  let smsStatus = $state('')
  let smsSaving = $state(false)
  let turnstile = $state<TurnstileSettings>({ ...TURNSTILE_DEFAULTS })
  let turnstileBusy = $state(false)
  let turnstileError = $state('')
  let turnstileStatus = $state('')
  let phones = $state<PhonePoolEntry[]>([])
  let phoneInput = $state('')
  let phoneMaxReceiveCount = $state(0)
  let selectedPhoneKey = $state('')
  let phonePoolBusy = $state(false)
  let phonePoolError = $state('')

  // S16 支付资料 — the PayPal profile keys only. The payment WINDOW (G20) is
  // deferred: it completes real charges unattended, so nothing here may ever
  // gain a button that opens a payment link.
  let paypal = $state<PaypalSettings>({ ...PAYPAL_DEFAULTS })
  let paypalStatus = $state('')
  let paypalSaving = $state(false)
  let paymentCards = $state<PaymentCardView[]>([])
  let paymentCardInput = $state('')
  let paymentCardBusy = $state(false)
  let paymentCardStatus = $state('')
  /** 别名、域名邮箱等本地写操作的互斥锁，防止双击重复创建。 */
  let localActionBusy = $state(false)
  /** K12 多账号队列仍在派发时保持按钮禁用，即使两批任务间短暂无 running。 */
  let networkQueueBusy = $state(false)

  // S18 设置 (the 提示音 tab — app.py 14021 maps the `settings` nav key onto it).
  let sound = $state<SoundSettings>({ ...SOUND_DEFAULTS })
  let audioDevices = $state<string[]>([AUDIO_DEFAULT_DEVICE_LABEL])
  let audioBusy = $state(false)
  const audioDeviceIDs = new Map<string, string>()

  // S19 全部操作页拥有的三个持久设置。
  let convertFormat = $state<SessionConvertFormat>('sub2api')
  let manualEmailOtp = $state(false)
  let k12WorkspaceId = $state(DEFAULT_K12_WORKSPACE_ID)
  let k12Concurrency = $state(1)

  let importText = $state('')
  let importError = $state('')
  let lastImport = $state('')
  let importing = $state(false)
  let cloudMail = $state<CloudMailSettings>({ enabled: false, baseUrl: '', token: '' })
  let cloudMailBusy = $state(false)
  let cloudMailError = $state('')
  let cloudMailStatus = $state('')

  // 本地账户管理状态要先于全局 busy 声明，保证写入期间其他任务入口同步锁定。
  let accountDialogOpen = $state(false)
  let accountDialogError = $state('')
  let accountDialogBusy = $state(false)

  /** Every job this session, in arrival order (jobs.go JobView). */
  let jobs = $state<JobView[]>([])
  let running = $derived(jobs.filter((job) => job.status === 'running'))
  const terminalJobWaiters = new Map<string, Set<() => void>>()
  /** StopAll 递增代次，让尚未派发的 K12 队列立即失效。 */
  let networkQueueGeneration = 0
  /** 网络任务、导入或本地账户写入进行中时，相关操作按钮保持禁用。 */
  let busy = $derived(
    running.length > 0 ||
      importing ||
      paymentCardBusy ||
      providerBusy ||
      phonePoolBusy ||
      localActionBusy ||
      networkQueueBusy ||
      accountDialogBusy,
  )

  /** 批量任务可同时等待多个人工输入；按到达顺序逐个展示，不能互相覆盖。 */
  let promptQueue = $state<PromptRequest[]>([])
  let prompt = $derived(promptQueue[0] ?? null)
  let answeringPromptId = ''

  // 结构化日志与详情页。启动前已有的完整 Session 会由后续
  // GetAccountDetails 绑定读取；在绑定落地前这里只展示本次会话可靠拿到的数据。
  let logSnapshot = $state<LogSnapshot | null>(null)
  /**
   * 事件可能早于 LoadLogs / SelectLogAccount 返回；必须先进入独立环形缓冲，
   * 再与快照按 seq 合并，不能因为 logSnapshot 尚为空就直接丢弃。
   */
  let streamedLogRecords = $state<LogRecord[]>([])
  let detailTab = $state<AccountDetailTab>('result')
  let detailSnapshot = $state<AccountDetails | null>(null)
  let detailLoading = $state(false)
  let detailError = $state('')
  let detailPayloads = $state<Record<string, SessionPayload>>({})
  let networkResults = $state<Record<string, NetworkJobResult>>({})
  const fetchedNetworkResults = new Set<string>()
  const fetchingNetworkResults = new Set<string>()

  // 其他本地管理对话框。
  let groupDialogOpen = $state(false)
  let groupDialogMode = $state<GroupDialogMode>('create')
  let groupDialogError = $state('')
  let groupDialogBusy = $state(false)
  let autoClassifyOpen = $state(false)
  let autoClassifyError = $state('')
  let autoClassifyBusy = $state(false)
  let manualSessionOpen = $state(false)
  let manualSessionMode = $state<ManualSessionMode>('merge')
  let manualSessionError = $state('')
  let manualSessionBusy = $state(false)

  // 邮箱管理窗口只读连接真实文件夹、邮件列表与正文；不提供删除或移动操作。
  let mailboxOpen = $state(false)
  let mailboxEmail = $state('')
  let mailboxAliasParent = $state('')
  let mailboxFolders = $state<string[]>([])
  let mailboxFolder = $state('INBOX')
  let mailboxLimit = $state(80)
  let mailboxSearch = $state('')
  let mailboxMessages = $state<MailboxMessage[]>([])
  let mailboxSelectedMessage = $state('')
  let mailboxBody = $state('')
  let mailboxBusy = $state(false)
  let mailboxStatus = $state('准备读取邮箱')
  let mailboxError = $state('')

  /*
    The confirmation gate in front of every money-spending button.

    The Tk app has none — its buttons fire immediately (app.py:13670/13726/13746).
    It is added here because a webview button is far easier to hit by accident
    than a Tk one, and because one click can now fan out across a whole
    selection: 注册取 Session rents a billable phone number per account, and
    重新获取 creates a real payment link per account. `pending` holds the thunk
    the dialog will run, so nothing between the click and 确定 can reach a
    binding.
  */
  let confirmRequest = $state<ConfirmRequest | null>(null)
  let pendingAction: ((autoConfirmPayment: boolean) => Promise<void>) | null = null

  // S13 导出. No confirmation gate here on purpose: nothing on that pane starts
  // a run, touches the network or costs anything, and the S25 preview modal is
  // already a check-before-you-write step.
  let exportPreview = $state<ExportPreview | null>(null)
  /** The kind the open preview was built from; SaveExport rebuilds from it. */
  let exportKind = $state<ExportKind | null>(null)
  let exportSelection = $state<string[]>([])
  let exportSub2APIJobId = $state('')
  let exportBusy = $state(false)
  let exportMissing = $state<MissingRTView | null>(null)
  let exportSub2API = $state<Sub2APIPlan | null>(null)
  const pendingJobActions = new Map<string, () => Promise<void>>()
  const pendingSub2APIJobs = new Set<string>()

  let dockSearch = $state('')

  // §S2 / app.py 19563 — the sidebar and the workbench header show the same
  // string, so it is derived once here.
  let accountSummary = $derived(`账户 ${total} · 显示 ${shown} · 已选 ${selected.length}`)

  let accountKeys = $derived(rows.map((row) => row.key))
  let selectedKeys = $derived(new Set(selected))
  let selectedRows = $derived(rows.filter((row) => selectedKeys.has(row.key)))
  let selectedEmails = $derived(selectedRows.map((row) => row.email))
  let detailAccount = $derived(selectedRows[0] ?? null)
  let detailViewAccount = $derived(detailSnapshot?.account ?? detailAccount)
  let detailPayload = $derived(
    detailAccount ? (detailPayloads[detailAccount.key] ?? {}) : {},
  )
  let detailLink = $derived(detailSnapshot?.link || detailAccount?.link || '')
  let actionVisibleRows = $derived(
    rows.filter((row) => matchesGroup(row, group) && matchesStatusFilter(row, status)),
  )
  let actionVisibleKeys = $derived(actionVisibleRows.map((row) => row.key))
  let accountLogLines = $derived(
    toDetailLogs(
      mergeLogRecords(detailSnapshot?.logs ?? [], logSnapshot?.account ?? [], 2_000),
    ),
  )
  let globalLogLines = $derived(toDetailLogs(logSnapshot?.global ?? []))

  // app.py 18625 `当前任务：{email} · {status}`, else 空闲 (18982). JobView
  // carries the lifecycle, not the worker's Chinese status — §4.2's per-account
  // `status` event is not emitted yet — so the four lifecycle values are mapped
  // onto app.py's own status vocabulary rather than invented.
  const JOB_STATUS_TEXT: Record<string, string> = {
    running: '处理中',
    succeeded: '成功',
    failed: '失败',
    cancelled: '已取消',
  }
  let taskSummary = $derived.by(() => {
    const job = running[running.length - 1]
    if (!job) return '当前任务：空闲'
    const more = running.length > 1 ? `（共 ${running.length} 个）` : ''
    return `当前任务：${job.email} · ${JOB_STATUS_TEXT[job.status] ?? job.status}${more}`
  })

  /**
   * The dock's own filter (`_global_account_list_indices`, app.py 19204): ONE
   * casefolded substring over email + type + status + group — not the
   * workbench's AND-ed multi-term search.
   */
  let dockRows = $derived.by(() => {
    const query = dockSearch.trim().toLowerCase()
    if (query === '') return rows
    return rows.filter((row) =>
      [row.email, row.account_type, row.statusText, row.group].join(' ').toLowerCase().includes(query),
    )
  })

  function select(entry: NavEntry, persistPage = true) {
    active = entry.key
    activePane = entry.pane
    if (entry.pane === 'actions') {
      activeActionGroup = (entry.actionGroup ?? 'account') as ActionGroupKey
    }
    if (persistPage) void persist({ workspace_page: entry.key })
  }

  function navigate(key: string, persistPage = true) {
    const entry = NAV.find((item) => item.key === key) ?? NAV[0]
    select(entry, persistPage)
  }

  function setActionGroup(next: ActionGroupKey) {
    activeActionGroup = next
    const navKey = next === 'team' || next === 'k12' ? next : 'actions'
    // 组内切换不能再走 select(NAV.actions)：该导航项默认 account，会把
    // auth/link/export 刚写入的 next 立即覆盖掉。
    active = navKey
    activePane = 'actions'
    void persist({ workspace_page: navKey })
  }

  // -- Loading ---------------------------------------------------------------

  let accountsLoadInFlight: Promise<void> | null = null
  let accountsReloadQueued = false

  async function performAccountLoad() {
    try {
      // ALL_ACCOUNTS: the filter bar is applied in the browser. ListAccounts
      // re-reads state.json — and Store.Load rebuilds session_results from
      // every split file under state_data/sessions/ — on each call, so a
      // round-trip per keystroke of the 搜索 box is the wrong shape. The row
      // carries statusText / hasSession / link precisely so the frontend
      // filter and accounts.Display cannot disagree.
      const page = await ListAccounts(ALL_ACCOUNTS)
      rows = page.rows ?? []
      const liveKeys = new Set(rows.map((row) => row.key))
      selected = selected.filter((key) => liveKeys.has(key))
      total = page.total
      knownGroups = page.groups ?? []
      accountsError = ''
    } catch (e) {
      rows = []
      total = 0
      knownGroups = []
      accountsError = `读取账户列表失败：${String(e)}`
    }
  }

  function loadAccounts(): Promise<void> {
    if (accountsLoadInFlight) {
      accountsReloadQueued = true
      return accountsLoadInFlight
    }
    accountsLoading = true
    accountsLoadInFlight = performAccountLoad().finally(() => {
      accountsLoadInFlight = null
      if (accountsReloadQueued) {
        accountsReloadQueued = false
        void loadAccounts()
      } else {
        accountsLoading = false
      }
    })
    return accountsLoadInFlight
  }

  async function loadSettings(restoreWorkspace = false) {
    try {
      const s = await LoadSettings()
      // app.py 14063-14074 coerces every saved value against its option list
      // rather than trusting the file.
      const savedGroup = s.account_group_filter ?? ''
      const validGroups = [GROUP_ALL, ...(s.account_groups ?? [])]
      group = validGroups.includes(savedGroup) ? savedGroup : GROUP_ALL
      status = STATUS_FILTER_OPTIONS.includes(s.account_status_filter) ? s.account_status_filter : STATUS_FILTER_ALL
      sortColumn = (SORT_COLUMNS as readonly string[]).includes(s.account_sort_column)
        ? (s.account_sort_column as SortColumn)
        : 'email'
      sortDirection =
        s.account_sort_direction === 'asc' || s.account_sort_direction === 'desc' ? s.account_sort_direction : 'custom'

      // S17. Passed through raw — ProxySettings mirrors app.py 14089's coercion
      // for the combo, and Go re-normalises on write.
      localProxy = s.local_proxy ?? ''
      routeMode = s.proxy_route_mode ?? ''
      pools = {
        register: s.dynamic_proxies ?? '',
        create: s.payment_dynamic_proxy ?? '',
        followup: s.followup_dynamic_proxy ?? '',
        approve: s.approve_dynamic_proxy ?? '',
      }
      scheduleAllProxyPoolCounts(pools, 0)
      const savedProviders = s.provider_proxy_configs ?? {}
      providers = {
        create: { ...EMPTY_PROVIDER_PROXY_CONFIG, ...(savedProviders.create ?? {}) },
        followup: { ...EMPTY_PROVIDER_PROXY_CONFIG, ...(savedProviders.followup ?? {}) },
        approve: { ...EMPTY_PROVIDER_PROXY_CONFIG, ...(savedProviders.approve ?? {}) },
      }
      proxyRegion = s.link_proxy_region ?? '不限'
      raceConcurrency = s.link_race_concurrency ?? 1
      precheckLimit = s.link_proxy_precheck_limit ?? 500
      precheckConcurrency = s.link_proxy_precheck_concurrency ?? 100
      reuseProxies = {
        create: s.reuse_payment_proxy ?? '',
        followup: s.reuse_followup_proxy ?? '',
        approve: s.reuse_approve_proxy ?? '',
      }
      requireJapan = s.require_japan_extract_proxy ?? false
      registerWithPaymentProxy = s.register_with_payment_proxy ?? false
      forceLegacyPaypal = s.force_legacy_paypal ?? false

      // S15. Also raw: app.py 14181-14190's load-side coercion (strip, then
      // substitute the default for a blank 服务代码 / 国家 ID / 最高单价) already
      // happened inside settings.FromSnapshot (snapshot.go:270-280). Note
      // 最高单价 is asymmetric on purpose — save writes "" for "no cap", load
      // turns "" back into 0.07 — so re-applying it here would double up.
      smsbower = {
        enabled: s.smsbower_enabled ?? false,
        apiKey: s.smsbower_api_key ?? '',
        service: s.smsbower_service ?? '',
        country: s.smsbower_country ?? '',
        maxPrice: s.smsbower_max_price ?? '',
      }
      turnstile = {
        enabled: s.turnstile_solver_enabled ?? false,
        url: s.turnstile_solver_url?.trim() || TURNSTILE_DEFAULTS.url,
      }
      phoneMaxReceiveCount = s.phone_max_receive_count ?? 0
      cloudMail = {
        enabled: s.cloud_mail_enabled ?? false,
        baseUrl: s.cloud_mail_base ?? '',
        token: s.cloud_mail_token ?? '',
      }

      // S16. Raw again: app.py 14153-14164's load-side coercion (strip, then
      // the DEFAULT_PAYPAL_EXTENSION_DIR fallback for a blank directory) is
      // already applied by settings.FromSnapshot (snapshot.go:207-224). The
      // four paypal_* fields are stored verbatim and stripped only on write.
      paypal = {
        phone: s.paypal_phone ?? '',
        card: s.paypal_card ?? '',
        smsUrl: s.paypal_sms_url ?? '',
        phonePool: s.paypal_phone_pool ?? '',
        extensionDir: s.payment_extension_dir ?? '',
      }

      // S18. `success_audio_device` gets the same treatment — snapshot.go:297
      // has already substituted 系统默认 for a blank label.
      sound = {
        successSound: s.success_sound_enabled ?? true,
        pauseOthers: s.pause_others_on_link_success ?? true,
        audioDevice: s.success_audio_device ?? '',
      }

      const savedFormat = (s.session_convert_format ?? '').toLowerCase()
      convertFormat = SESSION_CONVERT_FORMATS.includes(savedFormat as SessionConvertFormat)
        ? (savedFormat as SessionConvertFormat)
        : 'sub2api'
      manualEmailOtp = s.manual_email_otp ?? false
      k12WorkspaceId = s.k12_workspace_id?.trim() || DEFAULT_K12_WORKSPACE_ID
      k12Concurrency = Math.min(30, Math.max(1, Math.trunc(s.k12_concurrency || 1)))

      if (restoreWorkspace) {
        const savedPage = NAV.find((entry) => entry.key === s.workspace_page)
        if (savedPage) select(savedPage, false)
      }

      settingsReady = true
      settingsError = ''
    } catch (e) {
      // Deliberately leaves settingsReady false — see its declaration. That
      // gates EVERY write, not just the view fields, so the message has to name
      // the settings panes too rather than let them look live.
      settingsError = `读取设置失败，界面筛选和代理 / 接码 / 支付资料 / 全部操作 / 提示音设置都不会被保存：${String(e)}`
    }
  }

  // -- Settings writes -------------------------------------------------------

  /**
   * Serialises settings writes. Two combo changes in the same tick would
   * otherwise each load the same baseline and the second would undo the first.
   */
  let settingsWrites: Promise<void> = Promise.resolve()

  /**
   * 这是界面唯一的设置写路径。PatchSettings 在 Go 状态锁内合并最新快照，
   * 不再存在 Load → Save 的 TOCTOU。失败字段保留在 retrySettingsPatch 中，
   * 下一次编辑或真实任务启动前会原样重试，不能静默丢失。
   */
  let retrySettingsPatch: SettingsPatch = {}

  function persist(patch: SettingsPatch): Promise<void> {
    if (!settingsReady) return settingsWrites
    Object.assign(retrySettingsPatch, patch)
    settingsWrites = settingsWrites
      .then(async () => {
        const attempted: SettingsPatch = { ...retrySettingsPatch }
        if (Object.keys(attempted).length === 0) return
        await PatchSettings(attempted)
        const remaining: SettingsPatch = { ...retrySettingsPatch }
        for (const rawKey of Object.keys(attempted)) {
          const key = rawKey as keyof SettingsPatch
          if (Object.is(remaining[key], attempted[key])) delete remaining[key]
        }
        retrySettingsPatch = remaining
        settingsError = ''
      })
      .catch((e) => {
        settingsError = `保存设置失败：${String(e)}`
      })
    return settingsWrites
  }

  /**
   * Coalesced write for the fields the user TYPES into (本地代理 and the four
   * pool textareas), funnelled into the same `persist`.
   *
   * Tk never writes per keystroke: those widgets are read at save_state time
   * (app.py 14238-14243), and Store.save is itself debounced. A save per
   * character would mean a LoadSettings + a 60-key rewrite of the real
   * state.json per character.
   */
  const TYPING_FLUSH_MS = 400
  let pendingPatch: SettingsPatch = {}
  let flushTimer: ReturnType<typeof setTimeout> | null = null

  function persistTyped(patch: SettingsPatch) {
    Object.assign(pendingPatch, patch)
    if (flushTimer !== null) clearTimeout(flushTimer)
    flushTimer = setTimeout(() => {
      flushTimer = null
      const batch = pendingPatch
      pendingPatch = {}
      persist(batch)
    }, TYPING_FLUSH_MS)
  }

  /**
   * Cancel the pending debounce and hand its patch back, so an explicit 保存 can
   * fold the last few keystrokes into its own single write instead of racing a
   * timer that would re-save a stale baseline right after it.
   */
  function takePendingTyped(): SettingsPatch {
    if (flushTimer !== null) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    const batch = pendingPatch
    pendingPatch = {}
    return batch
  }

  /**
   * 真实任务启动前把文本框的 400ms 防抖写入落盘。否则用户刚改完代理就点
   * 注册/提链时，Go 会从 state.json 读取上一次出口配置。
   */
  async function flushSettingsBeforeTask() {
    if (!settingsReady) throw new Error('设置尚未加载完成，不能启动真实任务')
    const typed = takePendingTyped()
    if (Object.keys(typed).length > 0 || Object.keys(retrySettingsPatch).length > 0) {
      await persist(typed)
    }
    await settingsWrites
    if (settingsError) throw new Error(settingsError)
  }

  function setGroup(next: string) {
    group = next
    // app.py 20194 `_on_account_group_filter_changed`: clear the selection,
    // re-render, save. The rows it referred to are about to stop being visible.
    selected = []
    persist({ account_group_filter: next })
  }

  function setStatus(next: string) {
    status = next
    persist({ account_status_filter: next })
  }

  function setSort(column: SortColumn, direction: SortDirection) {
    sortColumn = column
    sortDirection = direction
    persist({ account_sort_column: column, account_sort_direction: direction })
  }

  // -- S17 代理 ---------------------------------------------------------------

  function setLocalProxy(value: string) {
    localProxy = value
    // Stored unstripped: app.py 14238 is the one proxy field that does not call
    // .strip(), and settings.ToSnapshot repeats that (snapshot.go:395).
    persistTyped({ local_proxy: value })
  }

  async function setRouteMode(mode: ProxyRouteMode) {
    routeMode = mode
    // NOT debounced: Tk persists this one on selection (app.py 13511 →
    // _on_proxy_route_mode_changed 16717 → save_state). Go re-normalises it
    // through proxypool.NormalizeRouteMode, so an unknown value cannot land.
    await persist({ proxy_route_mode: mode })
    // 只有路由模式已经成功落盘，才停止提供商代理池；否则内存 UI、磁盘配置
    // 与实际运行中的池会形成三种不同状态。
    if (settingsError) return
    if (mode === '全走本地代理') {
      // PatchSettings 已在成功落盘后原子停止池；这里只刷新展示快照。
      void ProviderProxyStatuses()
        .then(applyProviderStatusViews)
        .catch((e) => {
          error = `刷新提供商代理池状态失败：${String(e)}`
        })
    }
  }

  /**
   * ProxyPoolKey → the settings key it persists to, as
   * PROXY_POOL_SETTINGS_KEYS says but typed against `Settings`, so renaming a
   * field on the Go side breaks the build instead of silently writing nothing.
   * Spelled out per branch rather than indexed: these four names are
   * load-bearing and the register pool's key is `dynamic_proxies`, which does
   * NOT follow the other three's naming.
   */
  function poolPatch(key: ProxyPoolKey, text: string): SettingsPatch {
    switch (key) {
      case 'register':
        return { dynamic_proxies: text }
      case 'create':
        return { payment_dynamic_proxy: text }
      case 'followup':
        return { followup_dynamic_proxy: text }
      case 'approve':
        return { approve_dynamic_proxy: text }
    }
  }

  // Python 会在文本变化后 30ms 用完整解析器刷新“剩余 N”。解析放在 Go
  // 侧以复用同一套兼容规则；版本号防止较慢的旧结果覆盖较新的输入。
  const PROXY_POOL_COUNT_DEBOUNCE_MS = 30
  const proxyPoolCountTimers = new Map<ProxyPoolKey, ReturnType<typeof setTimeout>>()
  const proxyPoolCountVersions: Record<ProxyPoolKey, number> = {
    register: 0,
    create: 0,
    followup: 0,
    approve: 0,
  }

  function scheduleProxyPoolCount(
    key: ProxyPoolKey,
    text: string,
    delay = PROXY_POOL_COUNT_DEBOUNCE_MS,
  ) {
    const previous = proxyPoolCountTimers.get(key)
    if (previous !== undefined) clearTimeout(previous)
    const version = ++proxyPoolCountVersions[key]
    proxyPoolCountTimers.set(
      key,
      setTimeout(() => {
        proxyPoolCountTimers.delete(key)
        void CountProxyPoolText(text)
          .then((count) => {
            if (proxyPoolCountVersions[key] !== version) return
            proxyPoolCounts = {
              ...proxyPoolCounts,
              [key]: Math.max(0, Math.trunc(Number(count) || 0)),
            }
          })
          .catch(() => {
            if (proxyPoolCountVersions[key] !== version) return
            const next = { ...proxyPoolCounts }
            delete next[key]
            proxyPoolCounts = next
          })
      }, delay),
    )
  }

  function scheduleAllProxyPoolCounts(nextPools: ProxyPools, delay = PROXY_POOL_COUNT_DEBOUNCE_MS) {
    for (const key of PROXY_POOL_ORDER) scheduleProxyPoolCount(key, nextPools[key], delay)
  }

  function setPool(key: ProxyPoolKey, text: string) {
    const next = { ...pools }
    next[key] = text
    pools = next
    scheduleProxyPoolCount(key, text)
    // Not trimmed here — ToSnapshot applies Python's own strip (snapshot.go:401).
    persistTyped(poolPatch(key, text))
  }

  function applyProviderStatusViews(views: ProviderProxyStatusView[]) {
    const next: Partial<Record<ProviderProxyRole, ProviderProxyStageStatus>> = {
      ...providerStatuses,
    }
    for (const view of views ?? []) {
      if (view.role !== 'create' && view.role !== 'followup' && view.role !== 'approve') continue
      next[view.role] = {
        ready: view.status?.ready ?? 0,
        target: view.status?.target ?? 0,
        checking: view.status?.inflight ?? 0,
        message: view.text ?? '',
      }
    }
    providerStatuses = next
  }

  async function loadProviderStatuses() {
    try {
      applyProviderStatusViews(await ProviderProxyStatuses())
    } catch (e) {
      error = `读取提供商代理状态失败：${String(e)}`
    }
  }

  function setProxyFull(change: ProxyFullChange) {
    switch (change.field) {
      case 'region':
        proxyRegion = change.value
        void persist({ link_proxy_region: change.value })
        break
      case 'raceConcurrency':
        raceConcurrency = change.value
        void persist({ link_race_concurrency: change.value })
        break
      case 'precheckLimit':
        precheckLimit = change.value
        void persist({ link_proxy_precheck_limit: change.value })
        break
      case 'precheckConcurrency':
        precheckConcurrency = change.value
        void persist({ link_proxy_precheck_concurrency: change.value })
        break
      case 'reuse':
        reuseProxies = { ...reuseProxies, [change.role]: change.value }
        if (change.role === 'create') persistTyped({ reuse_payment_proxy: change.value })
        else if (change.role === 'followup') persistTyped({ reuse_followup_proxy: change.value })
        else persistTyped({ reuse_approve_proxy: change.value })
        break
      case 'requireJapan':
        requireJapan = change.value
        void persist({ require_japan_extract_proxy: change.value })
        break
      case 'registerWithPaymentProxy':
        registerWithPaymentProxy = change.value
        void persist({ register_with_payment_proxy: change.value })
        break
      case 'forceLegacyPaypal':
        forceLegacyPaypal = change.value
        void persist({ force_legacy_paypal: change.value })
        break
      case 'extensionDir':
        paypal = { ...paypal, extensionDir: change.value }
        persistTyped({ payment_extension_dir: change.value })
        break
    }
  }

  async function choosePaymentExtensionDirectory() {
    try {
      const path = await OpenPaymentExtensionDirectory()
      if (!path) return
      paypal = { ...paypal, extensionDir: path }
      await persist({ payment_extension_dir: path })
      if (!settingsError) {
        notice = '支付扩展目录已更新'
        error = ''
      }
    } catch (e) {
      error = `选择支付扩展目录失败：${String(e)}`
    }
  }

  function handleProxyFullAction(action: ProxyFullAction) {
    if (action.kind === 'edit_provider') {
      providerDialogRole = action.role
      providerError = ''
      providerDialogOpen = true
      return
    }
    if (action.kind === 'apply_providers') {
      void applyProviderSettings()
      return
    }
    if (action.kind === 'choose_extension_dir') {
      void choosePaymentExtensionDirectory()
      return
    }
    const cleanup = action.kind === 'cleanup'
    const accepted = window.confirm(
      cleanup
        ? '确认联网检测四个手工代理池，并从所有池删除连续两次不可用的节点？此修改无法从界面撤销。'
        : '确认联网预检支付三段代理池？会真实连接代理并消耗代理流量，但不会修改池内容。',
    )
    if (!accepted) return
    void (async () => {
      providerBusy = true
      try {
        await flushSettingsBeforeTask()
        const view = cleanup
          ? await StartProxyPoolCleanup({ confirmed: true })
          : await StartProxyPoolPrecheck({ confirmed: true })
        upsertJob(view)
        error = ''
      } catch (e) {
        error = `${cleanup ? '清理' : '预检'}代理池失败：${String(e)}`
      } finally {
        providerBusy = false
      }
    })()
  }

  async function saveProvider(role: ProviderProxyRole, value: ProviderProxyConfig) {
    providerBusy = true
    providerError = ''
    const next = { ...providers, [role]: value }
    providers = next
    // 后端按 role 深合并；只发送当前编辑项，避免覆盖另一阶段刚保存的配置。
    await persist({ provider_proxy_configs: { [role]: value } })
    providerBusy = false
    if (settingsError === '') providerDialogOpen = false
    else providerError = settingsError
  }

  async function applyProviderSettings() {
    const accepted = window.confirm(
      '应用配置会连接代理提供商并在后台预热启用的代理池，可能消耗提供商流量额度。确认继续？',
    )
    if (!accepted) return
    providerBusy = true
    try {
      await flushSettingsBeforeTask()
      applyProviderStatusViews(await ApplyProviderProxySettings(true))
      error = ''
    } catch (e) {
      error = `应用提供商代理配置失败：${String(e)}`
    } finally {
      providerBusy = false
    }
  }

  // -- S15 手机与接码 ----------------------------------------------------------

  /**
   * `保存设置` — app.py 14388 `save_smsbower_settings`: validate, echo the
   * normalised 服务代码 / 国家 ID back into the entries (14396-14397, so a blank
   * box visibly becomes dr / 33), save, then log 14400's line. app.py raises the
   * failures through messagebox.showwarning; here they land in the pane's banner.
   */
  async function saveSmsBower() {
    const problem = validateSmsBower(smsbower)
    if (problem) {
      smsError = problem
      smsStatus = ''
      return
    }
    smsbower = normalizeSmsBower(smsbower)
    smsError = ''
    smsStatus = ''
    smsSaving = true
    try {
      await persist({
        smsbower_enabled: smsbower.enabled,
        smsbower_api_key: smsbower.apiKey,
        smsbower_service: smsbower.service,
        smsbower_country: smsbower.country,
        smsbower_max_price: smsbower.maxPrice,
      })
      // app.py 14400. `persist` reports its own failure through settingsError,
      // so success is "the chain came back clean".
      if (settingsError === '') smsStatus = 'SMSBower 接码设置已保存'
    } finally {
      smsSaving = false
    }
  }

  function setPhoneMaxReceiveCount(value: number) {
    phoneMaxReceiveCount = value
    void persist({ phone_max_receive_count: value })
  }

  function applyPhonesResult(result: {
    phones?: PhonePoolEntry[]
    message?: string
    errors?: string[]
  }) {
    phones = result.phones ?? []
    if (selectedPhoneKey && !phones.some((phone) => phone.number.trim() === selectedPhoneKey)) {
      selectedPhoneKey = ''
    }
    phonePoolError = (result.errors ?? []).join('；')
    if (result.message && !phonePoolError) {
      notice = result.message
      error = ''
    }
  }

  async function loadPhones() {
    try {
      applyPhonesResult(await ListPhones())
    } catch (e) {
      phones = []
      phonePoolError = `读取手机号池失败：${String(e)}`
    }
  }

  async function handlePhonePoolAction(action: PhonePoolAction, selectedKey = '') {
    if (phonePoolBusy) return
    if (
      action === 'clear' &&
      !window.confirm('确认清空全部手机号？任务运行中后端会拒绝；此操作无法从界面撤销。')
    ) {
      return
    }
    if (
      action === 'manual_code' &&
      !window.confirm(
        `确认联网轮询 ${selectedKey} 已保存的接码链接，最多等待 30 秒？不会租用新号码。`,
      )
    ) {
      return
    }
    phonePoolBusy = true
    phonePoolError = ''
    try {
      if (action === 'manual_code') {
        const view = await StartManualPhoneCode(selectedKey)
        upsertJob(view)
        notice = `手工取码任务已启动：${selectedKey}`
        error = ''
        return
      }
      const result =
        action === 'import'
          ? await ImportPhones(phoneInput)
          : action === 'reset'
            ? await ResetPhones()
            : await ClearPhones(true)
      applyPhonesResult(result)
      if (action === 'import' && (result.errors?.length ?? 0) === 0) phoneInput = ''
    } catch (e) {
      phonePoolError = `${
        action === 'import' ? '导入' : action === 'reset' ? '重置' : '清空'
      }手机号失败：${String(e)}`
    } finally {
      phonePoolBusy = false
    }
  }

  async function testSmsBower() {
    const problem = validateSmsBower(smsbower)
    if (problem) {
      smsError = problem
      smsStatus = ''
      return
    }
    const accepted = window.confirm(
      '将连接 SMSBower，只读取余额和价格，不会租用号码。确认开始只读检测？',
    )
    if (!accepted) return
    smsbower = normalizeSmsBower(smsbower)
    smsSaving = true
    smsError = ''
    smsStatus = ''
    try {
      await persist({
        smsbower_enabled: smsbower.enabled,
        smsbower_api_key: smsbower.apiKey,
        smsbower_service: smsbower.service,
        smsbower_country: smsbower.country,
        smsbower_max_price: smsbower.maxPrice,
      })
      await flushSettingsBeforeTask()
      const job = await StartSMSBowerReadTest({
        apiKey: smsbower.apiKey,
        service: smsbower.service,
        country: smsbower.country,
        includePrices: true,
      })
      upsertJob(job)
      smsStatus = 'SMSBower 只读检测任务已启动，可在“任务”页查看进度。'
    } catch (e) {
      smsError = `测试余额失败：${String(e)}`
    } finally {
      smsSaving = false
    }
  }

  function setTurnstile(next: TurnstileSettings) {
    turnstile = next
    turnstileError = ''
    turnstileStatus = ''
  }

  async function saveTurnstile() {
    turnstileBusy = true
    turnstileError = ''
    try {
      const url = turnstile.url.trim() || TURNSTILE_DEFAULTS.url
      turnstile = { ...turnstile, url }
      await persist({
        turnstile_solver_enabled: turnstile.enabled,
        turnstile_solver_url: url,
      })
      await settingsWrites
      if (settingsError) {
        turnstileError = settingsError
      } else {
        turnstileStatus = 'Turnstile Solver 设置已保存'
      }
    } finally {
      turnstileBusy = false
    }
  }

  async function probeTurnstile() {
    if (!window.confirm('确认联网探测 Turnstile Solver 健康接口？不会提交验证码任务。')) return
    turnstileBusy = true
    turnstileError = ''
    turnstileStatus = ''
    try {
      const url = turnstile.url.trim() || TURNSTILE_DEFAULTS.url
      const view = await StartTurnstileProbe({ url })
      upsertJob(view)
      turnstileStatus = 'Turnstile Solver 探测任务已启动，可在“任务”页查看进度。'
    } catch (e) {
      turnstileError = `Turnstile Solver 探测失败：${String(e)}`
    } finally {
      turnstileBusy = false
    }
  }

  function setCloudMail(next: CloudMailSettings) {
    cloudMail = next
    cloudMailError = ''
    cloudMailStatus = ''
  }

  async function saveCloudMail() {
    cloudMailBusy = true
    cloudMailError = ''
    try {
      await persist({
        cloud_mail_enabled: cloudMail.enabled,
        cloud_mail_base: cloudMail.baseUrl,
        cloud_mail_token: cloudMail.token,
      })
      await settingsWrites
      if (settingsError) cloudMailError = settingsError
      else cloudMailStatus = 'Cloud Mail 设置已保存'
    } finally {
      cloudMailBusy = false
    }
  }

  async function probeCloudMail(probeEmail: string) {
    if (!window.confirm('确认联网只读探测 Cloud Mail API？最多读取一条邮件列表，不读取正文。')) return
    cloudMailBusy = true
    cloudMailError = ''
    cloudMailStatus = ''
    try {
      const view = await StartCloudMailProbe({
        baseUrl: cloudMail.baseUrl,
        token: cloudMail.token,
        probeEmail: probeEmail.trim(),
      })
      upsertJob(view)
      cloudMailStatus = 'Cloud Mail 只读探测任务已启动，可在“任务”页查看进度。'
    } catch (e) {
      cloudMailError = `Cloud Mail 探测失败：${String(e)}`
    } finally {
      cloudMailBusy = false
    }
  }

  async function generateCloudMailToken(adminEmail: string, adminPassword: string) {
    if (
      !window.confirm(
        '生成新的 Cloud Mail 程序 Token 会立即使旧 Token 失效，并把新 Token 保存到本机设置。确认继续？',
      )
    ) {
      return
    }
    cloudMailBusy = true
    cloudMailError = ''
    cloudMailStatus = ''
    try {
      const view = await StartCloudMailTokenGeneration({
        baseUrl: cloudMail.baseUrl,
        adminEmail: adminEmail.trim(),
        adminPassword,
        confirmInvalidate: true,
      })
      upsertJob(view)
      cloudMailStatus = 'Cloud Mail Token 生成任务已启动；完成后会自动保存新 Token。'
    } catch (e) {
      cloudMailError = `生成 Cloud Mail Token 失败：${String(e)}`
    } finally {
      cloudMailBusy = false
    }
  }

  // -- S16 支付资料 ------------------------------------------------------------

  /** The pane's shape → the five settings keys it owns. Typed against `Settings`. */
  function paypalPatch(value: PaypalSettings): SettingsPatch {
    return {
      paypal_phone: value.phone,
      paypal_card: value.card,
      paypal_sms_url: value.smsUrl,
      paypal_phone_pool: value.phonePool,
      payment_extension_dir: value.extensionDir,
    }
  }

  /**
   * Every field on S16 is typed, so all five go through the debounce — a save
   * per character would be a LoadSettings plus a 60-key rewrite of the real
   * state.json per character.
   *
   * Tk gets away with reading these Entries at save_state time (app.py
   * 14261-14265) because save_state runs constantly, from every other control
   * on the app. Here nothing else would ever write them, so leaving them until
   * 保存 would quietly lose a card the user typed and then navigated away from.
   * Values are stored unstripped; ToSnapshot applies Python's own strip
   * (snapshot.go:419-423).
   */
  function setPaypal(next: PaypalSettings) {
    paypal = next
    // 支付资料页和高级代理页编辑的是同一个 payment_extension_dir。
    // The banner described the values as they were when 保存 ran.
    paypalStatus = ''
    persistTyped(paypalPatch(next))
  }

  /**
   * `保存` — app.py 14362 `save_paypal_settings`: save_state, then log 14364's
   * line. It validates and coerces nothing, so this is a flush plus a receipt.
   */
  async function savePaypal() {
    // One write, not two: the debounce's patch rides along.
    const patch = { ...takePendingTyped(), ...paypalPatch(paypal) }
    paypalStatus = ''
    paypalSaving = true
    try {
      await persist(patch)
      // `persist` reports its own failure through settingsError, so success is
      // "the chain came back clean".
      if (settingsError === '') paypalStatus = 'PayPal 扩展资料已保存'
    } finally {
      paypalSaving = false
    }
  }

  async function loadPaymentCards() {
    try {
      const result = await ListPaymentCards()
      paymentCards = result.cards ?? []
      paymentCardStatus = ''
    } catch (e) {
      paymentCards = []
      paymentCardStatus = `读取支付卡池失败：${String(e)}`
    }
  }

  async function handlePaymentCardAction(action: PaymentCardPoolAction) {
    paymentCardBusy = true
    try {
      const result =
        action === 'import'
          ? await ImportPaymentCards(paymentCardInput)
          : await ResetPaymentCards()
      paymentCards = result.cards ?? []
      paymentCardStatus = result.message || `支付卡池已更新，共 ${result.total} 张。`
      if (action === 'import' && (result.errors?.length ?? 0) === 0) paymentCardInput = ''
      if ((result.errors?.length ?? 0) > 0) {
        paymentCardStatus += ` 未导入：${result.errors.join('；')}`
      }
    } catch (e) {
      paymentCardStatus = `${action === 'import' ? '导入' : '重置'}支付卡失败：${String(e)}`
    } finally {
      paymentCardBusy = false
    }
  }

  // -- S18 设置 ---------------------------------------------------------------

  /**
   * Two checkboxes and a readonly combo — nothing typed, so like 代理模式 these
   * persist on the change itself (app.py 13647/13648 pass `command=save_state`
   * and 13654 binds `<<ComboboxSelected>>` to it).
   */
  function setSound(next: SoundSettings) {
    sound = next
    persist({
      success_sound_enabled: next.successSound,
      success_audio_device: next.audioDevice,
      pause_others_on_link_success: next.pauseOthers,
    })
  }

  async function refreshAudioDevices() {
    audioBusy = true
    try {
      if (typeof navigator === 'undefined' || !navigator.mediaDevices?.enumerateDevices) {
        throw new Error('当前 WebView 不支持音频输出设备枚举')
      }
      const devices = await navigator.mediaDevices.enumerateDevices()
      const outputs = devices.filter((device) => device.kind === 'audiooutput')
      audioDeviceIDs.clear()
      audioDeviceIDs.set(AUDIO_DEFAULT_DEVICE_LABEL, '')
      const labels = outputs.map((device, index) => {
        const name = device.label.trim() || `音频输出 ${index + 1}`
        const label = `${index}: ${name} / WebAudio`
        audioDeviceIDs.set(label, device.deviceId)
        return label
      })
      audioDevices = [AUDIO_DEFAULT_DEVICE_LABEL, ...labels]
      if (
        sound.audioDevice &&
        sound.audioDevice !== AUDIO_DEFAULT_DEVICE_LABEL &&
        !audioDevices.includes(sound.audioDevice)
      ) {
        // 未授予设备标签权限时 enumerateDevices 只会返回空标签，不能据此
        // 判定用户保存的设备已消失；保留旧标签供其继续选择。
        audioDevices = [...audioDevices, sound.audioDevice]
      }
      notice = `已刷新音频输出设备：${outputs.length} 个`
      error = ''
    } catch (e) {
      error = `刷新音频设备失败：${String(e)}`
    } finally {
      audioBusy = false
    }
  }

  async function testSuccessSound() {
    audioBusy = true
    try {
      const AudioContextClass =
        window.AudioContext ??
        (window as typeof window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
      if (!AudioContextClass) throw new Error('当前 WebView 不支持 WebAudio')
      const context = new AudioContextClass()
      const sinkID = audioDeviceIDs.get(sound.audioDevice) ?? ''
      const routed = context as AudioContext & {
        setSinkId?: (sinkId: string) => Promise<void>
      }
      if (sinkID && routed.setSinkId) await routed.setSinkId(sinkID)

      const gain = context.createGain()
      gain.gain.setValueAtTime(0.0001, context.currentTime)
      gain.gain.exponentialRampToValueAtTime(0.12, context.currentTime + 0.015)
      gain.gain.setValueAtTime(0.12, context.currentTime + 0.38)
      gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.42)
      gain.connect(context.destination)

      const first = context.createOscillator()
      first.type = 'sine'
      first.frequency.value = 880
      first.connect(gain)
      first.start(context.currentTime)
      first.stop(context.currentTime + 0.21)

      const second = context.createOscillator()
      second.type = 'sine'
      second.frequency.value = 1320
      second.connect(gain)
      second.start(context.currentTime + 0.21)
      second.stop(context.currentTime + 0.42)

      await new Promise((resolve) => setTimeout(resolve, 460))
      await context.close()
      notice =
        sinkID && !routed.setSinkId
          ? '提示音播放完成；当前 WebView 不支持指定输出设备，已使用系统默认设备。'
          : '提示音播放完成'
      error = ''
    } catch (e) {
      error = `测试提示音失败：${String(e)}`
    } finally {
      audioBusy = false
    }
  }

  function setManualEmailOtp(enabled: boolean) {
    manualEmailOtp = enabled
    void persist({ manual_email_otp: enabled })
  }

  function setConvertFormat(format: SessionConvertFormat) {
    convertFormat = format
    void persist({ session_convert_format: format })
  }

  function setK12WorkspaceId(value: string) {
    k12WorkspaceId = value
    persistTyped({ k12_workspace_id: value })
  }

  function setK12Concurrency(value: number) {
    k12Concurrency = value
    void persist({ k12_concurrency: value })
  }

  // -- Actions ---------------------------------------------------------------

  async function refreshLocalState() {
    await loadAccounts()
    try {
      summary = await LoadSummary()
    } catch {
      // 账号列表已经刷新成功时，概览读取失败不覆盖更具体的操作结果。
    }
  }

  function ensureSelected(label: string, exactlyOne = false): string[] | null {
    if (selectedEmails.length === 0) {
      error = `${label}：请先选择账号`
      return null
    }
    if (exactlyOne && selectedEmails.length !== 1) {
      error = `${label}一次只能处理一个账号，当前已选 ${selectedEmails.length} 个`
      return null
    }
    return [...selectedEmails]
  }

  function askCount(title: string, defaultValue: number, max: number): number | null {
    const raw = window.prompt(`${title}（1–${max}）`, String(defaultValue))
    if (raw === null) return null
    const value = Number.parseInt(raw.trim(), 10)
    if (!Number.isFinite(value) || value < 1 || value > max) {
      error = `${title}必须是 1–${max} 的整数`
      return null
    }
    return value
  }

  async function deleteSelectedAccounts() {
    if (busy || accountDialogBusy) return
    const emails = ensureSelected('删除选中')
    if (!emails) return
    const preview = emails.slice(0, 20).join('\n')
    const tail = emails.length > 20 ? `\n……另有 ${emails.length - 20} 个账号` : ''
    if (
      !window.confirm(
        `确认删除 ${emails.length} 个账号、支付链接和撞链次数？为兼容 Python，独立 Session 结果会保留，重新导入同邮箱后仍可恢复。\n\n${preview}${tail}`,
      )
    ) {
      return
    }
    accountDialogBusy = true
    try {
      const result = await DeleteAccounts(emails, true)
      selected = []
      accountDialogOpen = false
      notice = result.message || `已删除 ${result.deleted} 个账号`
      error = ''
      await refreshLocalState()
    } catch (e) {
      accountDialogError = `删除账号失败：${String(e)}`
      error = accountDialogError
    } finally {
      accountDialogBusy = false
    }
  }

  async function clearAllAccounts() {
    if (rows.length === 0) return
    if (
      !window.confirm(
        `确认清空账户列表中的 ${rows.length} 个账号、支付链接和撞链次数？独立 Session 结果会保留；此操作不能从界面撤销。`,
      )
    ) {
      return
    }
    try {
      const result = await ClearAccounts(true)
      selected = []
      group = GROUP_ALL
      notice = result.message || '账户列表已清空'
      error = ''
      await refreshLocalState()
      await loadSettings()
    } catch (e) {
      error = `清空账户列表失败：${String(e)}`
    }
  }

  async function setSelectedAccountType(accountType: ManagedAccountType) {
    const emails = ensureSelected('设置账号类型')
    if (!emails) return
    if (
      accountType === 'free' &&
      !window.confirm('设置为 Free 会按 Python 兼容规则清空这些账号保存的 OpenAI RT。确认继续？')
    ) {
      return
    }
    accountDialogBusy = true
    try {
      const result = await SetAccountType(emails, accountType, accountType === 'free')
      accountDialogError = ''
      notice = result.message || `已更新 ${result.updated} 个账号`
      error = ''
      await refreshLocalState()
    } catch (e) {
      accountDialogError = `设置账号类型失败：${String(e)}`
    } finally {
      accountDialogBusy = false
    }
  }

  async function moveSelectedToGroup(nextGroup: string) {
    const emails = ensureSelected('移动账号')
    if (!emails) return
    accountDialogBusy = true
    try {
      await MoveAccountsToGroup(emails, nextGroup)
      accountDialogError = ''
      await refreshLocalState()
    } catch (e) {
      accountDialogError = `移动账号失败：${String(e)}`
    } finally {
      accountDialogBusy = false
    }
  }

  function openGroupDialog(mode: GroupDialogMode) {
    groupDialogMode = mode
    groupDialogError = ''
    groupDialogOpen = true
  }

  async function submitGroupOperation(operation: GroupOperation) {
    groupDialogBusy = true
    groupDialogError = ''
    try {
      if (operation.kind === 'create') {
        await CreateAccountGroup(operation.name)
      } else if (operation.kind === 'rename') {
        await RenameAccountGroup(operation.oldName, operation.newName)
        if (group === operation.oldName) group = operation.newName
      } else {
        await DeleteAccountGroup(operation.name, true)
        if (group === operation.name) group = GROUP_ALL
      }
      groupDialogOpen = false
      await loadSettings()
      await refreshLocalState()
    } catch (e) {
      groupDialogError = `分组操作失败：${String(e)}`
    } finally {
      groupDialogBusy = false
    }
  }

  async function submitAutoClassify(mode: AutoClassifyMode, scope: AutoClassifyScope) {
    autoClassifyBusy = true
    autoClassifyError = ''
    try {
      const result = await AutoClassifyAccounts({
        mode,
        scope,
        selectedEmails: [...selectedEmails],
        currentGroup: group,
        currentStatus: status,
        // Workbench 的临时搜索框不持久化，当前 shell 没有读取它的绑定。
        currentSearch: '',
      })
      autoClassifyOpen = false
      notice = result.message || `已分类 ${result.updated} 个账号`
      error = ''
      await loadSettings()
      await refreshLocalState()
    } catch (e) {
      autoClassifyError = `自动分类失败：${String(e)}`
    } finally {
      autoClassifyBusy = false
    }
  }

  async function createPlusAliases() {
    if (localActionBusy) return
    const emails = ensureSelected('别名注册')
    if (!emails) return
    const count = askCount('每个主邮箱生成的别名数量', 1, 4)
    if (count === null) return
    localActionBusy = true
    try {
      const result = await CreatePlusAliasAccounts({ emails, count })
      if (result.errors?.length) error = `${result.message}；${result.errors.join('；')}`
      else {
        notice = result.message
        error = ''
      }
      await refreshLocalState()
    } catch (e) {
      error = `生成别名账号失败：${String(e)}`
    } finally {
      localActionBusy = false
    }
  }

  async function createDomainMail() {
    if (localActionBusy) return
    const count = askCount('生成域名邮箱数量', 10, 500)
    if (count === null) return
    const receiveMailboxes: Record<string, string> = {}
    for (const row of selectedRows) {
      if (row.receive_mailbox?.trim()) receiveMailboxes[row.email] = row.receive_mailbox.trim()
    }
    localActionBusy = true
    try {
      const result = await CreateDomainMailAccounts({
        selectedEmails: [...selectedEmails],
        count,
        receiveMailboxes,
      })
      if (result.errors?.length) error = `${result.message}；${result.errors.join('；')}`
      else {
        notice = result.message
        error = ''
      }
      await loadSettings()
      await refreshLocalState()
    } catch (e) {
      error = `生成域名邮箱失败：${String(e)}`
    } finally {
      localActionBusy = false
    }
  }

  function openManualSession(mode: ManualSessionMode) {
    if (mode === 'merge' && !ensureSelected('填入 Session', true)) return
    manualSessionMode = mode
    manualSessionError = ''
    manualSessionOpen = true
  }

  function tokenFromJSON(value: unknown): string {
    if (typeof value === 'string') return ''
    if (Array.isArray(value)) {
      for (const item of value) {
        const token = tokenFromJSON(item)
        if (token) return token
      }
      return ''
    }
    if (!value || typeof value !== 'object') return ''
    const object = value as Record<string, unknown>
    for (const key of ['accessToken', 'access_token', 'token']) {
      if (typeof object[key] === 'string' && object[key].trim()) return object[key].trim()
    }
    for (const item of Object.values(object)) {
      const token = tokenFromJSON(item)
      if (token) return token
    }
    return ''
  }

  function payloadFromSessionResult(result: {
    email: string
    planType: string
    summary: Record<string, unknown>
    sessionJson: string
  }): SessionPayload {
    let decoded: unknown = {}
    try {
      decoded = JSON.parse(result.sessionJson)
    } catch {
      // 后端已经验证并保存；前端无法解析时仍原样展示 session_json。
    }
    return {
      access_token: tokenFromJSON(decoded),
      session_json: result.sessionJson,
      access_summary: result.summary,
      plan_type: result.planType,
      chatgpt_plan_type: result.planType,
    }
  }

  function payloadFromAccountDetails(details: AccountDetails): SessionPayload {
    return {
      ...(details.session ?? {}),
      workflow: details.workflow ?? {},
      link_proxy: details.linkProxy,
      link_proxy_label: details.linkProxyLabel,
      link_proxy_exit: details.linkProxyExit,
      link_create_proxy: details.linkCreateProxy,
      link_create_proxy_label: details.linkCreateProxyLabel,
      link_create_proxy_exit: details.linkCreateProxyExit,
      link_followup_proxy: details.linkFollowupProxy,
      link_followup_proxy_label: details.linkFollowupProxyLabel,
      link_followup_proxy_exit: details.linkFollowupProxyExit,
      link_approve_proxy: details.linkApproveProxy,
      link_approve_proxy_label: details.linkApproveProxyLabel,
      link_approve_proxy_exit: details.linkApproveProxyExit,
    }
  }

  let detailRequestSequence = 0

  async function loadAccountDetails(email: string) {
    const request = ++detailRequestSequence
    if (!email) {
      detailSnapshot = null
      detailError = ''
      detailLoading = false
      return
    }
    detailLoading = true
    detailError = ''
    try {
      const details = await GetAccountDetails(email)
      if (
        request !== detailRequestSequence ||
        detailAccount?.email.toLowerCase() !== email.toLowerCase()
      ) {
        return
      }
      detailSnapshot = details
      const key = details.account.email.toLowerCase()
      detailPayloads = {
        ...detailPayloads,
        [key]: payloadFromAccountDetails(details),
      }
    } catch (e) {
      if (request === detailRequestSequence) {
        detailSnapshot = null
        detailError = `读取账户详情失败：${String(e)}`
      }
    } finally {
      if (request === detailRequestSequence) detailLoading = false
    }
  }

  async function submitManualSession(submission: ManualSessionSubmission) {
    manualSessionBusy = true
    manualSessionError = ''
    try {
      const result =
        submission.mode === 'merge'
          ? await MergeManualSession(
              submission.email,
              submission.text,
              submission.planOverride,
            )
          : await ReplaceManualSession(submission.text)
      const key = result.email.toLowerCase()
      detailPayloads = {
        ...detailPayloads,
        [key]: payloadFromSessionResult(result),
      }
      manualSessionOpen = false
      await refreshLocalState()
      selected = [key]
      detailTab = 'session'
      navigate('workbench')
    } catch (e) {
      manualSessionError = `保存 Session 失败：${String(e)}`
    } finally {
      manualSessionBusy = false
    }
  }

  async function runImport() {
    importing = true
    try {
      // The Go side owns the parse, the merge AND the destination group (it
      // reads settings.account_group_filter); the preview only mirrors it.
      const result = await ImportAccountsCall(importText)
      importError = ''
      // Already formatted verbatim as app.py 14721's log line.
      lastImport = result.message
      await loadAccounts()
    } catch (e) {
      importError = `导入账号失败：${String(e)}`
    } finally {
      importing = false
    }
  }

  /**
   * 从文件导入 (app.py:14348). It FILLS THE BOX and stops there — Python does not
   * import on open either, so the user still reviews the parse preview and
   * presses 导入 themselves. An empty return means the dialog was cancelled,
   * which must not blank whatever is already typed.
   */
  async function loadAccountFile() {
    try {
      const text = await OpenAccountFile()
      if (text !== '') importText = text
      importError = ''
    } catch (e) {
      importError = `读取文件失败：${String(e)}`
    }
  }

  /**
   * 注册取 Session / 注册或登录 over a selection — _run_accounts (app.py:17609),
   * through the backend's batch parent rather than a loop here.
   *
   * The loop is the wrong shape and used to be what this did: StartRegister
   * returns as soon as the job is registered, so calling it once per selected
   * address launches one browser per account SIMULTANEOUSLY. app.py never does
   * that — _run_accounts holds a bounded 认证 window and hands each attempt a
   * fresh exit (app.py:17671-17679). StartBatchRegister is that orchestrator;
   * it returns the parent job plus the accounts preflight refused.
   *
   * SPENDS MONEY. Reachable only through the confirmation dialog.
   */
  async function startBatch(emails: string[], collectSession: boolean, what: string) {
    if (emails.length === 0) return
    try {
      await flushSettingsBeforeTask()
      const summary = await StartBatchRegister({ emails, collectSession, confirmed: true })
      upsertJob(summary.job)
      const skipped = summary.skipped ?? []
      // A skip is not a failure: preflight refused the account (Cloud Mail token
      // missing, 邮箱锁定) and wrote the reason into its row. Saying nothing
      // would look like it ran.
      error =
        skipped.length === 0
          ? ''
          : `${what}已跳过 ${skipped.length} 个账号（见任务日志与账号状态）：${skipped.join('、')}`
    } catch (e) {
      error = `${what}失败：${String(e)}`
    }
  }

  async function startAuthBatch(
    emails: string[],
    what: string,
    call: () => Promise<{ job: JobView; skipped?: string[] }>,
  ) {
    if (emails.length === 0) return
    try {
      await flushSettingsBeforeTask()
      const summary = await call()
      upsertJob(summary.job)
      const skipped = summary.skipped ?? []
      error =
        skipped.length === 0
          ? ''
          : `${what}已跳过 ${skipped.length} 个账号：${skipped.join('、')}`
    } catch (e) {
      error = `${what}失败：${String(e)}`
    }
  }

  /** 单账号旧入口，仅供“重新获取”单选动作使用。批量入口禁止循环它。 */
  async function startTasks(call: (email: string) => Promise<JobView>, emails: string[], what: string) {
    if (emails.length === 0) return
    const failures: string[] = []
    try {
      await flushSettingsBeforeTask()
    } catch (e) {
      error = `${what}失败：${String(e)}`
      return
    }
    for (const email of emails) {
      try {
        upsertJob(await call(email))
      } catch (e) {
        failures.push(`${email}: ${String(e)}`)
      }
    }
    error = failures.length === 0 ? '' : `${what}失败：${failures.join('；')}`
  }

  /**
   * 批量提链必须是一整个父任务。循环 GenerateLinks 会同时打开无界数量的
   * 浏览器/代理链，也无法统一取消；StartBatchGenerateLinks 在 Go 端持有
   * 三段代理队列、并发和每账号尝试上限。
   */
  async function startLinkBatch(emails: string[], what: string) {
    if (emails.length === 0) return
    try {
      await flushSettingsBeforeTask()
      const result = await StartBatchGenerateLinks({ emails, confirmed: true })
      upsertJob(result.job)
      const skipped = result.skipped ?? []
      error =
        skipped.length === 0
          ? ''
          : `${what}已跳过 ${skipped.length} 个账号：${skipped.join('、')}`
    } catch (e) {
      error = `${what}失败：${String(e)}`
    }
  }

  async function startNetworkTasks(
    emails: string[],
    what: string,
    call: (email: string) => Promise<JobView>,
  ) {
    const failures: string[] = []
    try {
      await flushSettingsBeforeTask()
    } catch (e) {
      error = `${what}失败：${String(e)}`
      return
    }
    for (const email of emails) {
      try {
        upsertJob(await call(email))
      } catch (e) {
        failures.push(`${email}: ${String(e)}`)
      }
    }
    error = failures.length === 0 ? '' : `${what}部分启动失败：${failures.join('；')}`
  }

  function isTerminalJob(view: JobView | undefined): boolean {
    return Boolean(view && view.status !== 'running')
  }

  function waitForJobTerminal(id: string): Promise<void> {
    if (isTerminalJob(jobs.find((view) => view.id === id))) return Promise.resolve()
    return new Promise((resolve) => {
      const waiters = terminalJobWaiters.get(id) ?? new Set<() => void>()
      waiters.add(resolve)
      terminalJobWaiters.set(id, waiters)
    })
  }

  function resolveTerminalJob(id: string) {
    const waiters = terminalJobWaiters.get(id)
    if (!waiters) return
    terminalJobWaiters.delete(id)
    for (const resolve of waiters) resolve()
  }

  function resolveAllTerminalWaiters() {
    for (const id of [...terminalJobWaiters.keys()]) resolveTerminalJob(id)
  }

  /**
   * K12 多选不是简单循环 Start*：绑定会立即创建 goroutine 后返回，循环会在
   * 数毫秒内把 N 个任务全部放出去。这里的 worker 槽位要等当前 Job 真正结束
   * 才领取下一个邮箱，因此运行中的 K12 任务始终不超过用户设置的并发数。
   */
  async function startBoundedNetworkTasks(
    emails: string[],
    what: string,
    concurrency: number,
    call: (email: string) => Promise<JobView>,
  ) {
    if (emails.length === 0 || networkQueueBusy) return
    networkQueueBusy = true
    const generation = networkQueueGeneration
    const failures: string[] = []
    try {
      await flushSettingsBeforeTask()
      if (generation !== networkQueueGeneration) return
      const limit = Math.min(
        emails.length,
        Math.max(1, Math.trunc(Number.isFinite(concurrency) ? concurrency : 1)),
      )
      let cursor = 0
      const worker = async () => {
        while (generation === networkQueueGeneration) {
          const index = cursor
          cursor += 1
          if (index >= emails.length) return
          const email = emails[index]
          try {
            const view = await call(email)
            upsertJob(view)
            if (!isTerminalJob(view)) await waitForJobTerminal(view.id)
          } catch (e) {
            failures.push(`${email}: ${String(e)}`)
          }
        }
      }
      await Promise.all(Array.from({ length: limit }, () => worker()))
      if (generation === networkQueueGeneration) {
        error = failures.length === 0 ? '' : `${what}部分启动失败：${failures.join('；')}`
      }
    } catch (e) {
      if (generation === networkQueueGeneration) error = `${what}失败：${String(e)}`
    } finally {
      networkQueueBusy = false
    }
  }

  // -- Export (S13) ----------------------------------------------------------

  /*
    Every export is two steps, matching _preview_and_save_text
    (app.py:24081-24087): build + show, then — only from the modal's 确定导出 —
    write. The backend rebuilds the document for SaveExport rather than being
    handed the previewed text back, so the bytes on disk always come from
    internal/export and can never be whatever the webview happened to hold.

    Errors are surfaced with err.Error() unchanged: every failure these can
    produce is one of internal/export's typed errors, whose text IS the verbatim
    messagebox.showwarning body app.py shows.
  */

  async function previewExportNow(kind: ExportKind, selection = [...selected]) {
    exportBusy = true
    try {
      if (kind === 'conversion' || kind === 'conversion_zip') await settingsWrites
      exportPreview = await PreviewExport(kind, selection)
      exportKind = kind
      exportSub2APIJobId = ''
      exportSelection = [...selection]
      error = ''
    } catch (e) {
      exportPreview = null
      exportKind = null
      exportSelection = []
      error = String(e)
    } finally {
      exportBusy = false
    }
  }

  async function saveExportDirect(kind: ExportKind, selection = [...selected]) {
    exportBusy = true
    try {
      if (kind === 'conversion' || kind === 'conversion_zip') await settingsWrites
      const result = await SaveExport(kind, selection)
      if (!result.cancelled) {
        notice = result.message || `导出已保存：${result.path}`
        error = ''
      }
    } catch (e) {
      error = String(e)
    } finally {
      exportBusy = false
    }
  }

  function closeExportPreview() {
    exportPreview = null
    exportKind = null
    exportSub2APIJobId = ''
    exportSelection = []
  }

  async function saveExport() {
    if (exportSub2APIJobId) {
      exportBusy = true
      try {
        const result = await SaveSub2APIExport(exportSub2APIJobId)
        if (!result.cancelled) {
          notice = result.message || `已保存 ${result.count} 条 sub2api 记录`
          error = ''
          closeExportPreview()
        }
      } catch (e) {
        error = `保存 sub2api 导出失败：${String(e)}`
      } finally {
        exportBusy = false
      }
      return
    }
    const kind = exportKind
    if (!kind) return
    exportBusy = true
    try {
      const result = await SaveExport(kind, exportSelection)
      // app.py:24085 / 24393 `if not path: return` — a dismissed save dialog
      // writes nothing, logs nothing, and leaves the preview open so the user
      // can try again.
      if (!result.cancelled) {
        closeExportPreview()
        error = ''
      }
    } catch (e) {
      error = String(e)
    } finally {
      exportBusy = false
    }
  }

  async function copyExportPreview() {
    if (exportSub2APIJobId) {
      await copyText(
        exportPreview?.text ?? '',
        'sub2api 预览为空',
        'sub2api 预览已复制',
      )
      return
    }
    const kind = exportKind
    if (!kind) return
    exportBusy = true
    try {
      // The returned text is what actually landed on the clipboard — one byte
      // shorter than the preview, because Tk copies `.rstrip("\n")`. Showing it
      // back keeps the box and the clipboard in agreement.
      exportPreview = await CopyExportPreview(kind, exportSelection)
      error = ''
    } catch (e) {
      error = String(e)
    } finally {
      exportBusy = false
    }
  }

  async function checkMissingRT() {
    try {
      exportMissing = await ExportMissingRT(selected)
      error = ''
    } catch (e) {
      exportMissing = null
      error = String(e)
    }
  }

  async function checkSub2API() {
    try {
      exportSub2API = await PrepareSub2APIExport(selected)
      error = ''
    } catch (e) {
      exportSub2API = null
      error = String(e)
    }
  }

  async function startCPARefresh(
    emails: string[],
    selection: string[],
    after: 'copy' | ExportKind,
  ) {
    try {
      await flushSettingsBeforeTask()
      const view = await StartCPAExportRefresh(emails)
      pendingJobActions.set(view.id, async () => {
        if (after === 'copy') await copyConversionNow(emails)
        else if (after === 'conversion_zip') await saveExportDirect(after, selection)
        else await previewExportNow(after, selection)
      })
      upsertJob(view)
      const latest = jobs.find((job) => job.id === view.id)
      if (latest && latest.status !== 'running') void completePendingJobAction(latest)
      error = ''
    } catch (e) {
      error = `CPA 导出前刷新失败：${String(e)}`
    }
  }

  function requestConversionRefresh(after: 'copy' | ExportKind) {
    const emails = ensureSelected(after === 'copy' ? '复制转换' : '导出转换')
    if (!emails) return
    const selection = [...selected]
    if (convertFormat !== 'cpa') {
      if (after === 'copy') void copyConversionNow(emails)
      else if (after === 'conversion_zip') void saveExportDirect(after, selection)
      else void previewExportNow(after, selection)
      return
    }
    confirmThen(
      {
        label: after === 'copy' ? 'CPA 复制转换' : 'CPA 导出转换',
        source: 'internal/ui/export.go',
        detail: 'CPA 格式会先用 OpenAI RT 联网刷新 Access Token，再继续原导出动作。',
        emails,
        costs: ['会连接 OpenAI 并可能消耗代理流量；不会租号或支付。'],
      },
      () => startCPARefresh(emails, selection, after),
    )
  }

  function requestExportPreview(kind: ExportKind) {
    if (kind === 'authorized' || kind === 'email_rt') {
      void requestAuthorizedExport(kind)
      return
    }
    if (kind === 'conversion' || kind === 'conversion_zip') {
      requestConversionRefresh(kind)
      return
    }
    void previewExportNow(kind, [...selected])
  }

  async function requestAuthorizedExport(kind: 'authorized' | 'email_rt') {
    const emails = ensureSelected(kind === 'authorized' ? '导出已授权账号' : '导出邮箱 RT')
    if (!emails) return
    const selection = [...selected]
    let missing: MissingRTView
    try {
      missing = await ExportMissingRT(emails)
      exportMissing = missing
    } catch (e) {
      error = `检查缺少 RT 的账号失败：${String(e)}`
      return
    }
    if ((missing.emails?.length ?? 0) === 0) {
      await previewExportNow(kind, selection)
      return
    }
    confirmThen(
      {
        label: kind === 'authorized' ? '授权后导出账号' : '授权后导出邮箱 RT',
        source: 'internal/ui/authops.go + export.go',
        detail: missing.prompt || '先为缺少 RT 的账号执行 OAuth 授权，再回到导出预览。',
        emails: missing.emails,
        costs: ['OAuth 授权可能按设置租用 SMSBower 手机号，并会真实登录 OpenAI。'],
      },
      async () => {
        try {
          await flushSettingsBeforeTask()
          const summary = await StartOAuthAuthorizeRT({
            emails: missing.emails,
            confirmed: true,
          })
          pendingJobActions.set(summary.job.id, async () => {
            const remaining = await ExportMissingRT(emails)
            exportMissing = remaining
            if ((remaining.emails?.length ?? 0) > 0) {
              error =
                `OAuth 批量任务结束后仍有 ${remaining.emails.length} 个账号缺少 RT，` +
                `已拒绝静默跳过：${remaining.emails.join('、')}`
              return
            }
            await previewExportNow(kind, selection)
          })
          upsertJob(summary.job)
          const latest = jobs.find((job) => job.id === summary.job.id)
          if (latest && latest.status !== 'running') void completePendingJobAction(latest)
          error =
            (summary.skipped?.length ?? 0) === 0
              ? ''
              : `授权已跳过 ${summary.skipped.length} 个账号：${summary.skipped.join('、')}`
        } catch (e) {
          error = `启动导出前 OAuth 授权失败：${String(e)}`
        }
      },
    )
  }

  async function completePendingJobAction(view: JobView) {
    if (view.status === 'running') return
    const action = pendingJobActions.get(view.id)
    if (!action) return
    pendingJobActions.delete(view.id)
    if (view.status !== 'succeeded') {
      error = view.error || `前置任务未成功：${view.kind}`
      return
    }
    await action()
  }

  function presentSub2APIResult(jobID: string, network: NetworkJobResult) {
    if (!pendingSub2APIJobs.has(jobID)) return
    pendingSub2APIJobs.delete(jobID)
    if (network.job.status !== 'succeeded') {
      error = network.job.error || 'sub2api 导出任务失败'
      return
    }
    const result = objectValue(network.result)
    const failures = Array.isArray(result.failures) ? result.failures : []
    exportPreview = {
      kind: 'sub2api',
      title: 'sub2api 导出预览',
      text: typeof result.text === 'string' ? result.text : '',
      suggestedName: 'sub2api.json',
      count: typeof result.count === 'number' ? result.count : 0,
      skipped: failures.map((failure) => {
        const item = objectValue(failure)
        return `${String(item.email ?? '')}: ${String(item.error ?? '')}`
      }),
      skippedNote:
        failures.length > 0 ? `有 ${failures.length} 个账号刷新失败，详见任务日志。` : '',
      entries: [],
    }
    exportKind = null
    exportSub2APIJobId = jobID
    exportSelection = []
    error = ''
  }

  async function startSub2APIExport() {
    const emails = ensureSelected('sub2api 导出')
    if (!emails) return
    let plan: Sub2APIPlan
    try {
      plan = await PrepareSub2APIExport(emails)
    } catch (e) {
      error = `校验 sub2api 选区失败：${String(e)}`
      return
    }
    if ((plan.missingEmails?.length ?? 0) > 0) {
      exportSub2API = plan
      confirmThen(
        {
          label: '授权并导出 sub2api',
          source: 'internal/ui/export.go',
          detail:
            (plan.authorizationPrompt ||
              `先为 ${plan.missingEmails.length} 个缺少 RT 的账号执行 OAuth 授权，`) +
            '授权完成后自动继续刷新 Access Token 并显示 sub2api 预览。',
          emails,
          costs: ['OAuth 授权可能租用 SMSBower 手机号，并会真实登录 OpenAI、消耗代理流量。'],
        },
        async () => {
          try {
            await flushSettingsBeforeTask()
            const summary = await StartOAuthAuthorizeRT({
              emails: plan.missingEmails,
              confirmed: true,
            })
            pendingJobActions.set(summary.job.id, async () => {
              const remaining = await PrepareSub2APIExport(emails)
              exportSub2API = remaining
              if ((remaining.missingEmails?.length ?? 0) > 0) {
                error =
                  `OAuth 批量任务结束后仍有 ${remaining.missingEmails.length} 个账号缺少 RT，` +
                  `已拒绝静默跳过：${remaining.missingEmails.join('、')}`
                return
              }
              await launchSub2APIExport(emails)
            })
            upsertJob(summary.job)
            const latest = jobs.find((job) => job.id === summary.job.id)
            if (latest && latest.status !== 'running') void completePendingJobAction(latest)
            error =
              (summary.skipped?.length ?? 0) === 0
                ? ''
                : `授权已跳过 ${summary.skipped.length} 个账号：${summary.skipped.join('、')}`
          } catch (e) {
            error = `启动 sub2api 导出前 OAuth 授权失败：${String(e)}`
          }
        },
      )
      return
    }
    confirmThen(
      {
        label: 'sub2api 导出',
        source: 'internal/ui/export.go',
        detail: '刷新所选账号 Access Token，生成可信 sub2api 文档并在完成后显示预览。',
        emails,
        costs: ['会连接 OpenAI 并可能消耗代理流量；保存文件前仍会再次预览。'],
      },
      () => launchSub2APIExport(emails),
    )
  }

  async function launchSub2APIExport(emails: string[]) {
    try {
      await flushSettingsBeforeTask()
      const view = await StartSub2APIExport(emails)
      pendingSub2APIJobs.add(view.id)
      upsertJob(view)
      const cached = networkResults[view.id]
      if (cached) presentSub2APIResult(view.id, cached)
      error = ''
    } catch (e) {
      error = `启动 sub2api 导出失败：${String(e)}`
    }
  }

  /**
   * Raise the confirmation for a spending action. Nothing runs until the dialog
   * is answered; cancelling drops the thunk on the floor.
   */
  function confirmThen(
    request: ConfirmRequest,
    run: (autoConfirmPayment: boolean) => Promise<void>,
  ) {
    if (request.emails.length === 0) {
      error = '请先选择账号'
      return
    }
    confirmRequest = request
    pendingAction = run
  }

  function cancelConfirm() {
    confirmRequest = null
    pendingAction = null
  }

  async function runConfirmed(autoConfirmPayment: boolean) {
    const run = pendingAction
    confirmRequest = null
    pendingAction = null
    if (run) await run(autoConfirmPayment)
  }

  /**
   * A Team account put through 注册取 Session is re-dispatched to Worker.RunTeam
   * by the backend (bindings.go, app.py:17718) — and a Team run creates a
   * BILLABLE SEAT, which a plain registration does not. The dialog says so
   * rather than letting the difference be invisible.
   */
  function teamNotes(emails: string[]): string[] {
    const keys = new Set(emails.map((e) => e.toLowerCase()))
    const team = rows.filter(
      (row) =>
        keys.has(row.email.toLowerCase()) &&
        row.account_type.trim().toLowerCase() === 'team',
    )
    if (team.length === 0) return []
    return [
      `其中 ${team.length} 个是 Team 账号，会走 Team 注册流程（Worker.RunTeam），可能创建计费席位：` +
        team.map((row) => row.email).join('、'),
    ]
  }

  async function startDomainRandomRTFlow() {
    try {
      await flushSettingsBeforeTask()
      const result = await StartDomainRandomRT({ confirmed: true })
      upsertJob(result.job)
      await refreshLocalState()
      const created = rows.find(
        (row) => row.email.toLowerCase() === result.email.toLowerCase(),
      )
      if (created) selected = [created.key]
      notice = `随机域名邮箱已创建并启动取 RT：${result.email}`
      error = ''
    } catch (e) {
      error = `域名邮箱随机取 RT 失败：${String(e)}`
    }
  }

  async function startBatchRelinkFlow(emails: string[]) {
    try {
      await flushSettingsBeforeTask()
      const summary = await StartBatchRelink({ emails, confirmed: true })
      upsertJob(summary.job)
      const skipped = summary.skipped ?? []
      error =
        skipped.length === 0
          ? ''
          : `批量重新获取已跳过 ${skipped.length} 个账号：${skipped.join('、')}`
    } catch (e) {
      error = `批量重新获取失败：${String(e)}`
    }
  }

  async function switchSelectedPaymentProxy(email: string) {
    try {
      await flushSettingsBeforeTask()
      const result = await SwitchPaymentWindowProxy(email)
      notice = `${result.email} 的支付窗口已切换代理：${result.proxy}`
      error = ''
    } catch (e) {
      error = `切换支付代理失败：${String(e)}`
    }
  }

  async function copyConversionNow(emails: string[]) {
    exportBusy = true
    try {
      await settingsWrites
      await CopySessionConversion(emails)
      notice = `已按 ${convertFormat} 格式复制转换结果`
      error = ''
    } catch (e) {
      error = `复制转换失败：${String(e)}`
    } finally {
      exportBusy = false
    }
  }

  function handleFullAction(action: FullActionKey) {
    switch (action) {
      case 'import_accounts':
        navigate('mail')
        return
      case 'open_mailbox': {
        const emails = ensureSelected('邮箱管理', true)
        if (!emails) return
        confirmThen(
          {
            label: '邮箱管理',
            source: 'internal/ui/mailbox.go',
            detail: `连接 ${emails[0]} 的只读邮箱，读取文件夹与最近邮件。`,
            emails,
            costs: ['会连接邮箱服务并可能消耗代理流量；不会删除、移动或标记邮件。'],
          },
          () => openMailbox(emails[0]),
        )
        return
      }
      case 'create_plus_alias':
        void createPlusAliases()
        return
      case 'create_domain_mail':
        void createDomainMail()
        return
      case 'select_visible':
        if (actionVisibleKeys.length === 0) error = '当前分组没有可见邮箱'
        else selected = [...actionVisibleKeys]
        return
      case 'invert_visible': {
        if (actionVisibleKeys.length === 0) {
          error = '当前分组没有可见邮箱'
          return
        }
        const current = new Set(selected)
        for (const key of actionVisibleKeys) {
          if (current.has(key)) current.delete(key)
          else current.add(key)
        }
        selected = [...current]
        return
      }
      case 'clear_selection':
        selected = []
        error = ''
        return
      case 'refresh_account_type': {
        const emails = ensureSelected('刷新类型')
        if (!emails) return
        confirmThen(
          {
            label: '刷新类型',
            source: 'UI_SPEC §2.B.27',
            detail: '使用保存的 OpenAI RT 联网刷新账号类型。',
            emails,
            costs: ['会连接 OpenAI，并可能消耗代理提供商流量；不会租号或支付。'],
          },
          () =>
            startNetworkTasks(emails, '刷新类型', (email) =>
              StartRefreshAccountType({ email }),
            ),
        )
        return
      }
      case 'auto_classify':
        autoClassifyError = ''
        autoClassifyOpen = true
        return
      case 'delete_selected':
        void deleteSelectedAccounts()
        return
      case 'clear_accounts':
        void clearAllAccounts()
        return
      case 'auth_only':
      case 'register_session': {
        const emails = ensureSelected(action === 'auth_only' ? '注册或登录' : '注册并取 Session')
        if (!emails) return
        const collectSession = action === 'register_session'
        const label = collectSession ? '注册并取Session' : '注册或登录'
        confirmThen(
          {
            label,
            source: collectSession ? 'app.py:13670' : 'app.py:13726',
            detail: collectSession
              ? '完成认证并保存 Session / Access Token。'
              : '完成认证并保留浏览器，不读取 Session。',
            emails,
            costs: ['每个账号可能租用一个 SMSBower 手机号（真实计费）。'],
            notes: collectSession ? teamNotes(emails) : [],
          },
          () => startBatch(emails, collectSession, label),
        )
        return
      }
      case 'keep_login': {
        const emails = ensureSelected('登录并保留', true)
        if (!emails) return
        confirmThen(
          {
            label: '登录并保留',
            source: 'internal/ui/authops.go',
            detail: '打开浏览器完成登录并保留窗口，直到用户手动关闭。',
            emails,
            costs: ['会真实登录 OpenAI；根据账号状态可能需要邮箱验证码。'],
          },
          () => startNetworkTasks(emails, '登录并保留', StartKeepLogin),
        )
        return
      }
      case 'protocol_register_session': {
        const emails = ensureSelected('协议注册取 Session')
        if (!emails) return
        confirmThen(
          {
            label: '协议注册取Session',
            source: 'internal/ui/authops.go',
            detail: '不打开浏览器，顺序执行纯 HTTP OAuth 与邮箱验证码流程。',
            emails,
            costs: ['会真实连接 OpenAI 与邮箱；协议模式后端保证不会租用手机号。'],
          },
          () =>
            startAuthBatch(emails, '协议注册取 Session', () =>
              StartProtocolRegisterSession({ emails, confirmed: false }),
            ),
        )
        return
      }
      case 'refresh_session': {
        const emails = ensureSelected('刷新 Session')
        if (!emails) return
        confirmThen(
          {
            label: '刷新 Session',
            source: 'internal/ui/sessionrefresh.go',
            detail: '使用已保存 storage_state_json 打开浏览器并重新读取 Session。',
            emails,
            costs: ['会真实连接 ChatGPT 并可能消耗代理流量；不会支付或租号。'],
          },
          () =>
            startBoundedNetworkTasks(emails, '刷新 Session', 1, (email) =>
              StartRefreshSession({ email, k12: false, workspaceId: '' }),
            ),
        )
        return
      }
      case 'fill_session':
        openManualSession('merge')
        return
      case 'open_session_reader': {
        const emails = ensureSelected('辅助登录 Session', true)
        if (!emails) return
        confirmThen(
          {
            label: '辅助登录Session',
            source: 'internal/ui/authops.go',
            detail: '打开 ChatGPT 登录页并填写邮箱，浏览器将保持打开。',
            emails,
            costs: ['会真实打开 ChatGPT；后续操作由用户在浏览器中完成。'],
          },
          () => startNetworkTasks(emails, '辅助登录 Session', StartOpenSessionReader),
        )
        return
      }
      case 'oauth_authorize_rt': {
        const emails = ensureSelected('OAuth 授权取 RT')
        if (!emails) return
        confirmThen(
          {
            label: 'OAuth授权取RT',
            source: 'UI_SPEC §2.C.36',
            detail: '注册/登录后执行 OAuth 授权并保存 OpenAI refresh_token。',
            emails,
            costs: ['每个账号可能租用 SMSBower 手机号，并会真实登录 OpenAI。'],
          },
          () =>
            startAuthBatch(emails, 'OAuth 授权取 RT', () =>
              StartOAuthAuthorizeRT({ emails, confirmed: true }),
            ),
        )
        return
      }
      case 'external_oauth': {
        const emails = ensureSelected('打开 OAuth 链接', true)
        if (!emails) return
        const url = window.prompt('请粘贴 auth.openai.com 的 HTTPS OAuth 链接')
        if (url === null) return
        confirmThen(
          {
            label: '打开OAuth链接',
            source: 'internal/ui/authops.go',
            detail: '后端会校验域名必须是 auth.openai.com；非标准 authorize 路径也按本次确认放行。',
            emails,
            costs: ['会真实打开并登录 OpenAI OAuth 页面；窗口不会自动关闭。'],
          },
          () =>
            startNetworkTasks(emails, '打开 OAuth 链接', (email) =>
              StartExternalOAuth({
                email,
                url: url.trim(),
                confirmedNonStandard: true,
              }),
            ),
        )
        return
      }
      case 'manual_login_code': {
        const emails = ensureSelected('手动登录取码', true)
        if (!emails) return
        confirmThen(
          {
            label: '手动登录取码',
            source: 'internal/ui/authops.go',
            detail: '打开 ChatGPT 登录页并只读监听所选邮箱验证码。',
            emails,
            costs: ['会真实连接 ChatGPT 与邮箱；不会租号或支付。'],
          },
          () => startNetworkTasks(emails, '手动登录取码', StartManualLoginCode),
        )
        return
      }
      case 'deactivation_check': {
        const emails = ensureSelected('查封禁邮件')
        if (!emails) return
        confirmThen(
          {
            label: '查封禁邮件',
            source: 'UI_SPEC §2.D.47',
            detail: '联网扫描最近 90 天的邮箱停用通知。',
            emails,
            costs: ['会连接邮箱服务并可能消耗代理流量；不会租号或支付。'],
          },
          () =>
            startNetworkTasks(emails, '查封禁邮件', (email) =>
              StartDeactivationScan({ email, days: 90, maxMessagesPerFolder: 120 }),
            ),
        )
        return
      }
      case 'domain_random_rt':
        confirmThen(
          {
            label: '域名邮箱随机取RT',
            source: 'internal/ui/actionflows.go',
            detail: '新建一个随机 @mail.example.com 邮箱，并立即完成注册与 OAuth 授权取 RT。',
            emails: ['（新建 1 个随机域名邮箱）'],
            costs: ['注册流程可能租用一个 SMSBower 手机号，并会真实连接 OpenAI 与 Cloud Mail。'],
          },
          startDomainRandomRTFlow,
        )
        return
      case 'generate_from_session':
      case 'batch_generate': {
        const emails = ensureSelected(action === 'generate_from_session' ? 'Session 生成' : '批量生成')
        if (!emails) return
        const label = action === 'generate_from_session' ? 'Session 生成' : '批量生成'
        confirmThen(
          {
            label,
            source: 'internal/ui/linkbatch.go',
            detail: '从已保存 Session 为所选账号生成真实支付链接。',
            emails,
            costs: ['每个账号会创建真实 checkout / 支付链接，并消耗代理流量。'],
          },
          () => startLinkBatch(emails, label),
        )
        return
      }
      case 'paste_session':
        openManualSession('replace')
        return
      case 'select_sessions': {
        const keys = actionVisibleRows.filter((row) => row.hasSession).map((row) => row.key)
        if (keys.length === 0) error = '当前列表没有可批量提取的 Session'
        else selected = keys
        return
      }
      case 'trial_check': {
        const emails = ensureSelected('检测试用')
        if (!emails) return
        confirmThen(
          {
            label: '检测试用',
            source: 'UI_SPEC §2.D.46',
            detail: '为所选 Session 创建真实 checkout 以读取试用金额。',
            emails,
            costs: ['每个账号会创建一个真实 checkout；不会提交卡片或扣款。'],
          },
          () =>
            startNetworkTasks(emails, '检测试用', (email) =>
              StartTrialEligibility({ email, country: '', confirmCheckout: true }),
            ),
        )
        return
      }
      case 'relink': {
        const emails = ensureSelected('重新获取', true)
        if (!emails) return
        confirmThen(
          {
            label: '重新获取',
            source: 'app.py:13746',
            detail: '重新登录单个账号并获取一条新的支付链接。',
            emails,
            costs: ['会真实登录 OpenAI 并创建支付链接。'],
          },
          () => startTasks((email) => GenerateLinks({ email, confirmed: true }), emails, '重新获取'),
        )
        return
      }
      case 'batch_relink': {
        const emails = ensureSelected('批量重新获取')
        if (!emails) return
        confirmThen(
          {
            label: '批量重新获取',
            source: 'internal/ui/actionflows.go',
            detail: '为所选账号固定分配三段代理，并发重新登录；每个账号只尝试一次提链。',
            emails,
            costs: ['每个账号会真实登录 OpenAI、创建支付链接并消耗代理流量。'],
          },
          () => startBatchRelinkFlow(emails),
        )
        return
      }
      case 'switch_payment_proxy': {
        const emails = ensureSelected('切换支付代理', true)
        if (!emails) return
        confirmThen(
          {
            label: '切换支付代理',
            source: 'internal/ui/paymentwindow.go',
            detail: `热切换 ${emails[0]} 当前已打开支付窗口的动态上游代理。`,
            emails,
            costs: ['会从支付代理池消费下一个代理并改变当前真实支付页面的网络出口。'],
          },
          () => switchSelectedPaymentProxy(emails[0]),
        )
        return
      }
      case 'export_authorized':
        void requestAuthorizedExport('authorized')
        return
      case 'export_email_rt':
        void requestAuthorizedExport('email_rt')
        return
      case 'export_sessions':
        requestExportPreview('sessions')
        return
      case 'export_raw':
        requestExportPreview('raw')
        return
      case 'copy_conversion':
        requestConversionRefresh('copy')
        return
      case 'export_conversion':
        requestExportPreview('conversion')
        return
      case 'export_zip':
        requestExportPreview('conversion_zip')
        return
      case 'export_sub2api':
        void startSub2APIExport()
        return
      case 'team_invite': {
        const emails = ensureSelected('邀请 Team 成员', true)
        if (!emails) return
        const targetEmail = window.prompt('请输入要邀请加入 Team 的目标邮箱')
        if (targetEmail === null) return
        if (!targetEmail.trim().includes('@')) {
          error = '请输入有效的 Team 邀请目标邮箱'
          return
        }
        confirmThen(
          {
            label: '邀请成员',
            source: 'UI_SPEC §2.F.60',
            detail: `使用 ${emails[0]} 的 Team Token 邀请 ${targetEmail.trim()}。`,
            emails,
            costs: ['会创建一个真实、可能计费的 Team 席位。'],
          },
          () =>
            startNetworkTasks(emails, 'Team 邀请', (email) =>
              StartTeamInvite({
                email,
                targetEmail: targetEmail.trim(),
                confirmBillableSeat: true,
              }),
            ),
        )
        return
      }
      case 'team_leave': {
        const emails = ensureSelected('退出 Team', true)
        if (!emails) return
        confirmThen(
          {
            label: '退出 Team',
            source: 'UI_SPEC §2.F.61',
            detail: '使用成员自己的 Token 退出当前 Team workspace。',
            emails,
            costs: ['会释放席位并使当前 Team Session 失效，无法从本界面撤销。'],
          },
          () =>
            startNetworkTasks(emails, '退出 Team', (email) =>
              StartTeamLeave({ email, confirmed: true }),
            ),
        )
        return
      }
      case 'team_scan_join': {
        const emails = ensureSelected('扫描邀请加入')
        if (!emails) return
        confirmThen(
          {
            label: '扫描邀请加入',
            source: 'internal/ui/inviteflows.go',
            detail: '扫描所选邮箱中的 Team/Business 邀请，接受后切换 workspace 并刷新 Session。',
            emails,
            costs: ['会读取真实邮箱并改变远端 Team 成员状态；缺少登录态时可能租用手机号。'],
          },
          () =>
            startAuthBatch(emails, '扫描 Team 邀请并加入', () =>
              StartTeamInviteScanJoin({ emails, confirmed: true }),
            ),
        )
        return
      }
      case 'k12_register_join': {
        const emails = ensureSelected('K12 一键注册加入')
        if (!emails) return
        confirmThen(
          {
            label: '一键注册加入',
            source: 'internal/ui/inviteflows.go',
            detail: `注册/登录取 Session，请求并接受 K12 邀请，再切换到 ${k12WorkspaceId.trim()} 刷新 Session。`,
            emails,
            costs: ['可能为每个账号租用 SMSBower 手机号，并会接受真实 K12 workspace 邀请。'],
          },
          () =>
            startAuthBatch(emails, 'K12 一键注册加入', () =>
              StartK12RegisterAndJoin({
                emails,
                workspaceId: k12WorkspaceId.trim(),
                confirmed: true,
              }),
            ),
        )
        return
      }
      case 'k12_request_invite': {
        const emails = ensureSelected('K12 请求邀请')
        if (!emails) return
        confirmThen(
          {
            label: 'K12 请求邀请',
            source: 'UI_SPEC §2.G.64',
            detail: `向 K12 workspace ${k12WorkspaceId.trim()} 请求邀请。`,
            emails,
            costs: ['会连接 OpenAI K12 接口并可能消耗代理流量；不会租号或支付。'],
          },
          () =>
            startBoundedNetworkTasks(emails, 'K12 请求邀请', k12Concurrency, (email) =>
              StartK12RequestInvite({ email, workspaceId: k12WorkspaceId.trim() }),
            ),
        )
        return
      }
      case 'k12_accept_refresh': {
        const emails = ensureSelected('K12 接受并刷新')
        if (!emails) return
        confirmThen(
          {
            label: '接受并刷新',
            source: 'internal/ui/inviteflows.go',
            detail: `请求 K12 邀请、等待并接受邮箱链接，再切换到 ${k12WorkspaceId.trim()} 刷新 Session。`,
            emails,
            costs: ['会读取真实邮箱、接受真实 K12 邀请并消耗代理流量。'],
          },
          () =>
            startAuthBatch(emails, 'K12 接受并刷新', () =>
              StartK12AcceptAndRefresh({
                emails,
                workspaceId: k12WorkspaceId.trim(),
                confirmed: true,
              }),
            ),
        )
        return
      }
      case 'k12_refresh': {
        const emails = ensureSelected('刷新 K12 Session')
        if (!emails) return
        confirmThen(
          {
            label: '刷新K12',
            source: 'internal/ui/sessionrefresh.go',
            detail: `强制切换到 K12 workspace ${k12WorkspaceId.trim()} 后刷新 Session。`,
            emails,
            costs: ['会真实登录 ChatGPT 并切换 workspace；普通 Plus 账号不要执行。'],
          },
          () =>
            startBoundedNetworkTasks(emails, '刷新 K12 Session', k12Concurrency, (email) =>
              StartRefreshSession({
                email,
                k12: true,
                workspaceId: k12WorkspaceId.trim(),
              }),
            ),
        )
        return
      }
      default: {
        const unreachable: never = action
        error = `无法识别的全部操作：${String(unreachable)}`
      }
    }
  }

  async function stopAll() {
    // 先让尚未派发的有界队列失效；否则当前任务被取消后，队列会把下一个邮箱
    // 当作空出的槽位继续启动，违背“停止全部”。
    networkQueueGeneration += 1
    resolveAllTerminalWaiters()
    try {
      await StopAll()
      promptQueue = []
      answeringPromptId = ''
      error = ''
    } catch (e) {
      error = `停止失败：${String(e)}`
    }
  }

  async function cancelJob(id: string) {
    try {
      await CancelJob(id)
      promptQueue = promptQueue.filter((request) => request.jobId !== id)
      if (answeringPromptId === id) answeringPromptId = ''
      error = ''
    } catch (e) {
      error = `取消任务失败：${String(e)}`
    }
  }

  function toDetailLogs(records: readonly LogRecord[]): DetailLogLine[] {
    return records.map((record) => {
      const raw = (record.level || '').toLowerCase()
      const level: DetailLogLevel =
        raw === 'error' || raw === 'failed'
          ? 'error'
          : raw === 'success' || raw === 'ok'
            ? 'success'
            : raw === 'attention' || raw === 'warning' || raw === 'warn'
              ? 'attention'
              : 'normal'
      return {
        message: `${record.ts ? `${record.ts} ` : ''}${record.message}`,
        level,
      }
    })
  }

  function detailProxySummary(payload: SessionPayload): string {
    const text = (value: unknown) => (value === null || value === undefined ? '' : String(value))
    const create = text(payload.link_create_proxy || payload.link_proxy || payload.link_create_proxy_label)
    const followup = text(payload.link_followup_proxy || payload.link_followup_proxy_label || create)
    const approve = text(payload.link_approve_proxy || payload.link_approve_proxy_label || followup)
    if (!create && !followup && !approve) return ''
    return `第一步=${create || '未记录'}\n后续=${followup || create || '未记录'}\nApprove=${approve || followup || create || '未记录'}`
  }

  async function copyText(value: string, emptyMessage: string, successMessage: string) {
    if (!value.trim()) {
      error = emptyMessage
      return
    }
    try {
      const copied = await ClipboardSetText(value)
      if (copied) {
        notice = successMessage
        error = ''
      } else {
        error = '写入剪贴板失败'
      }
    } catch (e) {
      error = `写入剪贴板失败：${String(e)}`
    }
  }

  function sessionField(payload: SessionPayload, key: string): string {
    const value = payload[key]
    return typeof value === 'string' ? value : ''
  }

  async function openPaymentWindow(
    email: string,
    proxyMode: 'new' | 'extraction',
    autoConfirm: boolean,
  ) {
    try {
      await flushSettingsBeforeTask()
      upsertJob(
        await StartOpenPaymentWindow({
          email,
          proxyMode,
          confirmed: true,
          // 后端仍会同时校验两个字段，避免单一前端状态误触发真实扣款。
          autoConfirm,
          confirmAutoCharge: autoConfirm,
        }),
      )
      error = ''
    } catch (e) {
      error = `打开支付窗口失败：${String(e)}`
    }
  }

  async function openSelectedPaymentWindows(emails: string[], autoConfirm: boolean) {
    try {
      await flushSettingsBeforeTask()
      const result = await StartOpenPaymentWindows({
        emails,
        confirmed: true,
        autoConfirm,
        confirmAutoCharge: autoConfirm,
      })
      for (const view of result.jobs ?? []) upsertJob(view)
      error =
        (result.skipped?.length ?? 0) === 0
          ? ''
          : `已跳过 ${result.skipped.length} 个支付窗口：${result.skipped
              .map((item) => `${item.email}: ${item.error}`)
              .join('；')}`
    } catch (e) {
      error = `批量打开支付窗口失败：${String(e)}`
    }
  }

  function handleDetailAction(action: AccountDetailAction) {
    const row = detailAccount
    if (!row) {
      error = '请先选择账号'
      return
    }
    switch (action) {
      case 'copy_link':
        void copyText(row.link ?? '', '暂无长链接', '长链接已复制')
        break
      case 'copy_proxy':
        void copyText(
          detailProxySummary(detailPayload),
          '当前选中邮箱暂无长链使用代理',
          '长链代理摘要已复制',
        )
        break
      case 'open_saved_proxy':
      case 'open_new_proxy': {
        const label = action === 'open_saved_proxy' ? '提链代理打开' : '新代理打开'
        confirmThen(
          {
            label,
            source: 'internal/ui/paymentwindow.go',
            detail: `使用${action === 'open_saved_proxy' ? '提链时保存的代理' : '新的支付代理'}打开 ${row.email} 的真实支付页。`,
            emails: [row.email],
            costs: [
              '会消费支付代理/PP 手机/卡池条目并打开真实支付页。',
              '只有在本弹窗另行勾选时才会自动点击支付页最终确认；该操作可能立即产生真实扣款。',
            ],
            allowPaymentAutoConfirm: true,
          },
          (autoConfirmPayment) =>
            openPaymentWindow(
              row.email,
              action === 'open_saved_proxy' ? 'extraction' : 'new',
              autoConfirmPayment,
            ),
        )
        break
      }
      case 'open_selected_links': {
        const emails = [...selectedEmails]
        confirmThen(
          {
            label: '批量打开支付窗口',
            source: 'internal/ui/paymentwindow.go',
            detail: `为 ${emails.length} 个所选账号分别打开独立真实支付页。`,
            emails,
            costs: [
              '每个窗口会消费自己的代理/PP 手机/卡池条目。',
              '只有在本弹窗另行勾选时才会自动点击各支付页最终确认；可能对多个账号立即产生真实扣款。',
            ],
            allowPaymentAutoConfirm: true,
          },
          (autoConfirmPayment) => openSelectedPaymentWindows(emails, autoConfirmPayment),
        )
        break
      }
      case 'copy_access_token':
        void copyText(
          sessionField(detailPayload, 'access_token'),
          '当前邮箱暂无 Access Token',
          'Access Token 已复制',
        )
        break
      case 'copy_session_json':
        void copyText(
          sessionField(detailPayload, 'session_json'),
          '当前邮箱暂无 Session JSON',
          'Session JSON 已复制',
        )
        break
      case 'clear_workflow':
        void (async () => {
          try {
            await ClearAccountWorkflow(row.email)
            await loadAccountDetails(row.email)
            error = ''
          } catch (e) {
            error = `清空流程失败：${String(e)}`
          }
        })()
        break
      case 'check_deactivation':
        confirmThen(
          {
            label: '查封禁邮件',
            source: 'UI_SPEC §2.D.47',
            detail: `扫描 ${row.email} 最近 90 天的 OpenAI 停用通知。`,
            emails: [row.email],
            costs: ['会连接邮箱服务并可能消耗代理流量；不会租号或支付。'],
          },
          () =>
            startNetworkTasks([row.email], '查封禁邮件', (email) =>
              StartDeactivationScan({ email, days: 90, maxMessagesPerFolder: 120 }),
            ),
        )
        break
      case 'open_mailbox':
        confirmThen(
          {
            label: '邮箱管理',
            source: 'internal/ui/mailbox.go',
            detail: `连接 ${row.email} 的只读邮箱并读取邮件。`,
            emails: [row.email],
            costs: ['会连接邮箱服务并可能消耗代理流量；不会删除或移动邮件。'],
          },
          () => openMailbox(row.email),
        )
        break
      case 'manual_login_code':
        confirmThen(
          {
            label: '手动登录取码',
            source: 'internal/ui/authops.go',
            detail: `打开 ${row.email} 的 ChatGPT 登录页并监听邮箱验证码。`,
            emails: [row.email],
            costs: ['会真实连接 ChatGPT 与邮箱；不会租号或支付。'],
          },
          () => startNetworkTasks([row.email], '手动登录取码', StartManualLoginCode),
        )
        break
      default: {
        const unreachable: never = action
        error = `无法识别的详情操作：${String(unreachable)}`
      }
    }
  }

  async function refreshMailboxFolders() {
    mailboxBusy = true
    mailboxError = ''
    mailboxStatus = '正在读取文件夹'
    try {
      const folders = await ListMailboxFolders(mailboxEmail)
      mailboxFolders = folders.length ? folders : ['INBOX']
      if (!mailboxFolders.includes(mailboxFolder)) mailboxFolder = mailboxFolders[0] ?? 'INBOX'
      mailboxStatus = `已读取 ${mailboxFolders.length} 个文件夹`
    } catch (e) {
      mailboxStatus = '读取文件夹失败'
      mailboxError = String(e)
    } finally {
      mailboxBusy = false
    }
  }

  async function refreshMailboxMessages(openaiOnly = false) {
    mailboxBusy = true
    mailboxError = ''
    mailboxStatus = '正在读取邮件'
    try {
      const query = openaiOnly ? 'openai' : mailboxSearch
      mailboxMessages = await ListMailboxMessages({
        email: mailboxEmail,
        folder: mailboxFolder,
        limit: mailboxLimit,
        query,
      })
      mailboxSelectedMessage = ''
      mailboxBody = ''
      mailboxStatus = `已读取 ${mailboxMessages.length} 封邮件`
    } catch (e) {
      mailboxStatus = '读取邮件失败'
      mailboxError = String(e)
    } finally {
      mailboxBusy = false
    }
  }

  async function openMailbox(email: string) {
    const row = rows.find((candidate) => candidate.email.toLowerCase() === email.toLowerCase())
    mailboxEmail = email
    mailboxAliasParent =
      row?.receive_mailbox && row.receive_mailbox.toLowerCase() !== email.toLowerCase()
        ? row.receive_mailbox
        : ''
    mailboxFolders = []
    mailboxFolder = 'INBOX'
    mailboxMessages = []
    mailboxSelectedMessage = ''
    mailboxBody = ''
    mailboxError = ''
    mailboxStatus = '准备读取邮箱'
    mailboxOpen = true
    await refreshMailboxFolders()
    if (!mailboxError) await refreshMailboxMessages(false)
  }

  async function readMailboxMessage(id: string, folder: string) {
    mailboxBusy = true
    mailboxError = ''
    mailboxStatus = '正在读取正文'
    try {
      const message = await GetMailboxMessage({ email: mailboxEmail, folder, id })
      const inviteLink = await ExtractMailboxInviteLink(message.body || '')
      mailboxBody = [
        `主题：${message.subject || ''}`,
        `发件人：${message.from || ''}`,
        `收件人：${message.to || ''}`,
        `时间：${message.mailTimeIso || message.date || ''}`,
        '',
        message.body || '',
      ].join('\n')
      mailboxStatus = inviteLink ? '正文读取完成 · 已识别 Team/K12 邀请链接' : '正文读取完成'
      const at = mailboxMessages.findIndex((item) => item.id === id)
      if (at >= 0) {
        mailboxMessages = [
          ...mailboxMessages.slice(0, at),
          { ...mailboxMessages[at], code: message.code, body: message.body },
          ...mailboxMessages.slice(at + 1),
        ]
      }
    } catch (e) {
      mailboxStatus = '读取正文失败'
      mailboxError = String(e)
    } finally {
      mailboxBusy = false
    }
  }

  function handleMailboxAction(action: MailboxDialogAction) {
    if (mailboxBusy) return
    if (action.kind === 'refresh_folders') {
      void refreshMailboxFolders()
      return
    }
    if (action.kind === 'refresh_messages') {
      void refreshMailboxMessages(action.openaiOnly)
      return
    }
    if (action.kind === 'read_body') {
      void readMailboxMessage(action.id, action.folder)
      return
    }
    if (action.kind === 'copy_body') {
      void copyText(mailboxBody, '当前没有可复制的邮件正文', '邮件正文已复制')
      return
    }
    void (async () => {
      const selected = mailboxMessages.find((message) => message.id === action.id)
      const code = selected?.code || (await ExtractMailboxCode(mailboxBody))
      await copyText(code, '当前邮件未识别到验证码', '验证码已复制')
    })()
  }

  let logRequestSequence = 0

  function mergeLogRecords(
    snapshot: readonly LogRecord[],
    streamed: readonly LogRecord[],
    limit: number,
  ): LogRecord[] {
    const bySequence = new Map<number, LogRecord>()
    for (const record of [...snapshot, ...streamed]) bySequence.set(record.seq, record)
    return [...bySequence.values()]
      .sort((left, right) => left.seq - right.seq)
      .slice(-limit)
  }

  async function selectStructuredLogAccount(email: string) {
    const request = ++logRequestSequence
    try {
      const snapshot = await SelectLogAccount(email)
      if (request !== logRequestSequence || (detailAccount?.email ?? '') !== email) return
      snapshot.global = mergeLogRecords(snapshot.global ?? [], streamedLogRecords, 10_000)
      const key = email.toLowerCase()
      const streamedAccount = streamedLogRecords.filter(
        (record) => record.email?.toLowerCase() === key,
      )
      snapshot.account = mergeLogRecords(snapshot.account ?? [], streamedAccount, 2_000)
      logSnapshot = snapshot
    } catch (e) {
      if (request === logRequestSequence) error = `读取账户日志失败：${String(e)}`
    }
  }

  async function loadInitialLogs() {
    const request = ++logRequestSequence
    try {
      const snapshot = await LoadLogs()
      if (request !== logRequestSequence) return
      snapshot.global = mergeLogRecords(snapshot.global ?? [], streamedLogRecords, 10_000)
      const selectedEmail = detailAccount?.email?.toLowerCase() ?? ''
      const streamedAccount = selectedEmail
        ? streamedLogRecords.filter((record) => record.email?.toLowerCase() === selectedEmail)
        : []
      snapshot.account = mergeLogRecords(snapshot.account ?? [], streamedAccount, 2_000)
      logSnapshot = snapshot
    } catch (e) {
      if (request === logRequestSequence) error = `读取日志失败：${String(e)}`
    }
  }

  let lastLogAccount = ''
  $effect(() => {
    const email = detailAccount?.email ?? ''
    if (email === lastLogAccount) return
    lastLogAccount = email
    detailSnapshot = null
    detailError = ''
    void loadAccountDetails(email)
    void selectStructuredLogAccount(email)
  })

  // -- Events ----------------------------------------------------------------

  const NETWORK_JOB_KINDS = new Set<string>([
    'refresh_account_type',
    'team_invite',
    'team_leave',
    'k12_request_invite',
    'trial_eligibility',
    'deactivation_scan',
    'turnstile_probe',
    'smsbower_read',
    'cloud_mail_probe',
    'cloud_mail_token',
    'cpa_export_refresh',
    'sub2api_export',
    'manual_phone_code',
    'payment_window',
    'proxy_pool_precheck',
    'proxy_pool_cleanup',
    'session_refresh',
    'k12_session_refresh',
    'workspace_invite_accept',
    'protocol_register',
    'protocol_register_batch',
    'oauth_authorize',
    'oauth_authorize_batch',
    'keep_login',
    'session_reader',
    'external_oauth',
    'manual_login_code',
    'team_invite_scan_join_batch',
    'team_invite_scan_join',
    'k12_accept_refresh_batch',
    'k12_accept_refresh',
    'k12_register_join_batch',
    'k12_register_join',
  ])

  function objectValue(value: unknown): Record<string, unknown> {
    return value !== null && typeof value === 'object' && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {}
  }

  function mergeNetworkPayload(kind: string, current: SessionPayload, raw: unknown): SessionPayload {
    const result = objectValue(raw)
    switch (kind) {
      case 'trial_eligibility':
        return {
          ...current,
          plus_trial_eligible: result.eligible,
          plus_trial_status: result.status,
          plus_trial_amount: result.amount,
          plus_trial_amount_source: result.amountSource,
          plus_trial_currency: result.currency,
          plus_trial_country: result.country,
          plus_trial_checked_at: result.checkedAt,
          plus_trial_detail: result.detail,
        }
      case 'k12_request_invite':
        return {
          ...current,
          k12_workspace_id: result.workspaceId,
          k12_status: result.status,
          k12_response: result.response,
        }
      case 'team_invite':
        return {
          ...current,
          team_invite_target_email: result.targetEmail,
          team_invite_status: result.status,
          team_invite_response: result.response,
          team_invite_sent_at: result.sentAt,
          team_workspace_id: result.accountId,
        }
      case 'team_leave':
        return {
          ...current,
          team_leave_status: result.status,
          team_leave_response: result.response,
          team_leave_at: result.leftAt,
        }
      case 'deactivation_scan': {
        const latest = objectValue(result.latest)
        return {
          ...current,
          openai_deactivation_found: result.found,
          openai_deactivation_status: result.status,
          openai_deactivation_count: result.count,
          openai_deactivation_checked_at: result.checked_at,
          openai_deactivation_alias_mismatch_count: result.alias_mismatch_count,
          openai_deactivation_folder: latest.folder,
          openai_deactivation_date: latest.date,
          openai_deactivation_from: latest.from,
          openai_deactivation_to: latest.to,
          openai_deactivation_subject: latest.subject,
          openai_deactivation_snippet: latest.snippet,
        }
      }
      case 'session_refresh':
      case 'k12_session_refresh':
        return {
          ...current,
          access_token: result.accessToken,
          session_json: result.sessionJson,
          storage_state_json: result.storageStateJson,
          access_summary: result.accessSummary,
          target_workspace_id: result.workspaceId,
          session_refreshed_at: result.refreshedAt,
        }
      default:
        return { ...current, [`network_${kind}`]: raw }
    }
  }

  async function loadNetworkResult(view: JobView) {
    if (!NETWORK_JOB_KINDS.has(view.kind) || view.status === 'running') return
    if (fetchedNetworkResults.has(view.id) || fetchingNetworkResults.has(view.id)) return
    fetchingNetworkResults.add(view.id)
    try {
      const result = await GetNetworkJobResult(view.id)
      fetchedNetworkResults.add(view.id)
      networkResults = { ...networkResults, [view.id]: result }
      if (view.kind === 'sub2api_export') presentSub2APIResult(view.id, result)
      if (view.email) {
        const key = view.email.toLowerCase()
        detailPayloads = {
          ...detailPayloads,
          [key]: mergeNetworkPayload(view.kind, detailPayloads[key] ?? {}, result.result),
        }
      }
      if (view.kind === 'smsbower_read') {
        const data = objectValue(result.result)
        if (result.job.status === 'succeeded') {
          smsStatus = data.balance ? `SMSBower 余额：${String(data.balance)}` : 'SMSBower 只读检测完成'
          smsError = ''
        } else {
          smsStatus = ''
          smsError = result.job.error || 'SMSBower 只读检测失败'
        }
      }
      if (view.kind === 'turnstile_probe') {
        const data = objectValue(result.result)
        if (result.job.status === 'succeeded') {
          turnstileStatus = `Turnstile Solver 可达：${String(data.url ?? '')} · HTTP ${String(data.status ?? '')}`
          turnstileError = ''
        } else {
          turnstileStatus = ''
          turnstileError = result.job.error || 'Turnstile Solver 探测失败'
        }
      }
      if (view.kind === 'cloud_mail_probe') {
        if (result.job.status === 'succeeded') {
          cloudMailStatus = 'Cloud Mail API 检测通过'
          cloudMailError = ''
        } else {
          cloudMailStatus = ''
          cloudMailError = result.job.error || 'Cloud Mail API 检测失败'
        }
      }
      if (view.kind === 'cloud_mail_token') {
        const data = objectValue(result.result)
        if (result.job.status === 'succeeded') {
          const token = typeof data.token === 'string' ? data.token : ''
          const baseUrl = typeof data.baseUrl === 'string' ? data.baseUrl : cloudMail.baseUrl
          cloudMail = { enabled: true, baseUrl, token: token || cloudMail.token }
          cloudMailStatus = 'Cloud Mail 程序 Token 已生成并保存'
          cloudMailError = ''
        } else {
          cloudMailStatus = ''
          cloudMailError = result.job.error || 'Cloud Mail Token 生成失败'
        }
      }
    } catch (e) {
      fetchedNetworkResults.delete(view.id)
      error = `读取任务 ${view.id} 的结构化结果失败：${String(e)}`
    } finally {
      fetchingNetworkResults.delete(view.id)
    }
  }

  function settleJobUI(view: JobView) {
    if (view.status === 'running') return
    promptQueue = promptQueue.filter((request) => request.jobId !== view.id)
    if (answeringPromptId === view.id) answeringPromptId = ''
    resolveTerminalJob(view.id)
    void completePendingJobAction(view)
  }

  const DETAIL_REFRESH_JOB_KINDS = new Set<string>([
    'session_refresh',
    'k12_session_refresh',
    'workspace_invite_accept',
    'protocol_register',
    'protocol_register_batch',
    'oauth_authorize',
    'oauth_authorize_batch',
    'keep_login',
    'session_reader',
    'external_oauth',
    'manual_login_code',
    'team_invite_scan_join',
    'k12_accept_refresh',
    'k12_register_join',
    'payment_window',
    'refresh_account_type',
    'team_invite',
    'team_leave',
    'k12_request_invite',
    'trial_eligibility',
    'deactivation_scan',
  ])

  function refreshTerminalSideEffects(view: JobView) {
    if (view.kind === 'manual_phone_code') void loadPhones()
    if (view.kind === 'payment_window') {
      void loadPaymentCards()
      void loadPhones()
    }
    if (view.kind === 'proxy_pool_cleanup') void loadSettings()
    if (DETAIL_REFRESH_JOB_KINDS.has(view.kind)) {
      const email = detailAccount?.email ?? ''
      if (!view.email || view.email.toLowerCase() === email.toLowerCase()) {
        void loadAccountDetails(email)
      }
    }
  }

  function upsertJob(view: JobView, refreshAccounts = true) {
    const at = jobs.findIndex((job) => job.id === view.id)
    if (at < 0) {
      jobs = [...jobs, view]
      if (view.status !== 'running') {
        settleJobUI(view)
        if (refreshAccounts) {
          if (!view.batchId) void loadAccounts()
          refreshTerminalSideEffects(view)
        }
        void loadNetworkResult(view)
      }
      return
    }
    const before = jobs[at]
    // ListJobs 的启动快照可能晚于事件到达；完成态绝不能被旧 running 快照降级。
    if (before.status !== 'running' && view.status === 'running') return
    jobs = [...jobs.slice(0, at), view, ...jobs.slice(at + 1)]
    // A job that just finished has almost certainly rewritten status /
    // attempts / link, and nothing else tells the table that.
    if (before.status === 'running' && view.status !== 'running') {
      settleJobUI(view)
      // 批量子任务很多；只让父任务完成时做一次全量状态读取。
      if (refreshAccounts && !view.batchId) void loadAccounts()
      if (refreshAccounts) refreshTerminalSideEffects(view)
      void loadNetworkResult(view)
    } else if (view.status !== 'running') {
      settleJobUI(view)
    }
  }

  async function answerPrompt(value: string) {
    const pending = prompt
    if (!pending || answeringPromptId === pending.jobId) return
    answeringPromptId = pending.jobId
    try {
      await AnswerPrompt(pending.jobId, value)
      promptQueue = promptQueue.filter((request) => request.jobId !== pending.jobId)
    } catch (e) {
      error = `提交人工输入失败：${String(e)}`
    } finally {
      if (answeringPromptId === pending.jobId) answeringPromptId = ''
    }
  }

  onMount(async () => {
    // §4: everything streaming arrives as events, never by polling.
    EventsOn(EVENT_LOG, (line: string) => {
      logs = [...logs.slice(-1999), line]
    })
    EventsOn(EVENT_LOG_RECORD, (record: LogRecord) => {
      streamedLogRecords = [...streamedLogRecords.slice(-9_999), record]
      if (!logSnapshot) return
      logSnapshot.global = mergeLogRecords(logSnapshot.global ?? [], [record], 10_000)
      const selectedEmail = detailAccount?.email?.toLowerCase() ?? ''
      if (selectedEmail && record.email?.toLowerCase() === selectedEmail) {
        logSnapshot.account = mergeLogRecords(logSnapshot.account ?? [], [record], 2_000)
      }
    })
    EventsOn(EVENT_JOB, (view: JobView) => upsertJob(view))
    EventsOn(EVENT_PROMPT, (request: PromptRequest) => {
      // 任务完成事件与 prompt 事件可能跨 goroutine 交错；已知终态任务绝不
      // 重新排入人工输入队列。
      if (isTerminalJob(jobs.find((view) => view.id === request.jobId))) return
      const at = promptQueue.findIndex((item) => item.jobId === request.jobId)
      if (at < 0) promptQueue = [...promptQueue, request]
      else promptQueue = [...promptQueue.slice(0, at), request, ...promptQueue.slice(at + 1)]
    })
    EventsOn(EVENT_PROVIDER_PROXY_STATUS, (event: ProviderProxyStatusEvent) => {
      if (event.role !== 'create' && event.role !== 'followup' && event.role !== 'approve') return
      providerStatuses = {
        ...providerStatuses,
        [event.role]: {
          ready: event.status?.ready ?? 0,
          target: event.status?.target ?? 0,
          checking: event.status?.inflight ?? 0,
          message: event.text ?? '',
        },
      }
    })

    try {
      env = await Environment()
      summary = await LoadSummary()
    } catch (e) {
      error = String(e)
    }
    // Settings first: they seed the filter the table is about to render with.
    await loadSettings(true)
    await Promise.all([
      loadAccounts(),
      loadPaymentCards(),
      loadProviderStatuses(),
      loadInitialLogs(),
      ListJobs()
        .then((views) => {
          for (const view of views ?? []) {
            // Promise.all 已经统一读取一次账户。历史终态任务只合并快照，
            // 不能每个任务再触发一次 state + 全部 session 文件重读。
            upsertJob(view, false)
          }
        })
        .catch((e) => {
          error = `读取任务列表失败：${String(e)}`
        }),
    ])
  })
</script>

<div class="shell">
  <Sidebar {active} summary={accountSummary} onselect={select} />

  <main>
    <div class="content">
      {#if error}
        <p class="banner err">{error}</p>
      {/if}
      {#if notice}
        <p class="banner ok">{notice}</p>
      {/if}
      {#if settingsError}
        <p class="banner err">{settingsError}</p>
      {/if}

      {#if activePane === 'workbench'}
        <Workbench
          {rows}
          {total}
          {knownGroups}
          {selected}
          {group}
          {status}
          {sortColumn}
          {sortDirection}
          {accountsError}
          {accountsLoading}
          {busy}
          {env}
          {summary}
          onselect={(keys) => (selected = keys)}
          ongroupchange={setGroup}
          onstatuschange={setStatus}
          onsortchange={setSort}
          onvisiblechange={(count) => (shown = count)}
          onreload={loadAccounts}
          onimport={runImport}
          onregister={(emails: string[]) => {
            confirmThen(
              {
                label: '注册取 Session',
                source: 'app.py:13670',
                detail: '注册或登录后读取 Session（access_token / session_json）',
                emails,
                costs: [
                  '每个账号可能租用一个 SMSBower 手机号（真实计费）',
                  '每个账号会真实登录 OpenAI，并写回 state.json',
                ],
                notes: teamNotes(emails),
              },
              () => startBatch(emails, true, '注册取 Session'),
            )
          }}
          onauthonly={(emails: string[]) =>
            confirmThen(
              {
                label: '注册或登录',
                source: 'app.py:13726',
                detail: '只注册或登录，不读取 Session，浏览器窗口保持打开',
                emails,
                costs: ['每个账号可能租用一个 SMSBower 手机号（真实计费）'],
              },
              () => startBatch(emails, false, '注册或登录'),
            )}
          onrelink={(emails: string[]) =>
            confirmThen(
              {
                label: '批量提链',
                source: 'internal/ui/linkbatch.go',
                detail: '从已保存 Session 为所选账号批量生成支付长链接',
                emails,
                costs: ['每个账号会创建真实 checkout / 支付链接，并消耗代理流量'],
              },
              () => startLinkBatch(emails, '批量提链'),
            )}
          onstop={stopAll}
        />

        <section class="card management-toolbar">
          <h2>账户与分组管理</h2>
          <div class="management-buttons">
            <button
              disabled={selected.length === 0 || busy}
              title="设置所选账号类型、移动分组或删除所选账号。"
              onclick={() => {
                accountDialogError = ''
                accountDialogOpen = true
              }}>管理所选账号</button
            >
            <button disabled={busy} title="新建一个 1–32 字符的账号分组。" onclick={() => openGroupDialog('create')}
              >新建分组</button
            >
            <button
              disabled={busy || group === GROUP_ALL || group === '未分组'}
              title="重命名当前筛选中的自定义分组。"
              onclick={() => openGroupDialog('rename')}>重命名当前分组</button
            >
            <button
              class="danger"
              disabled={busy || group === GROUP_ALL || group === '未分组'}
              title="删除当前自定义分组；组内账号回到默认分组，不删除账号或 Token。"
              onclick={() => openGroupDialog('delete')}>删除当前分组</button
            >
          </div>
        </section>

        <AccountDetailsFull
          activeTab={detailTab}
          account={detailAccount}
          selectedCount={selected.length}
          payload={detailPayload}
          link={detailAccount?.link ?? ''}
          linkProxySummary={detailProxySummary(detailPayload)}
          accountLogs={accountLogLines}
          globalLogs={globalLogLines}
          {busy}
          ontabchange={(tab) => (detailTab = tab)}
          onaction={handleDetailAction}
        />
      {:else if activePane === 'jobs'}
        <JobPane {jobs} {logs} oncancel={cancelJob} onstop={stopAll} />
      {:else if activePane === 'export'}
        <ExportPane
          {selected}
          busy={exportBusy}
          convertFormat={settingsReady ? convertFormat : ''}
          missing={exportMissing}
          sub2api={exportSub2API}
          onpreview={requestExportPreview}
          onmissing={checkMissingRT}
          onsub2api={startSub2APIExport}
        />
      {:else if activePane === 'mail'}
        <ImportAccounts
          text={importText}
          {group}
          existing={accountKeys}
          {busy}
          {importError}
          {lastImport}
          {cloudMail}
          cloudBusy={cloudMailBusy}
          cloudStatus={cloudMailStatus}
          cloudError={cloudMailError}
          canloadfile
          onchange={(value) => (importText = value)}
          onloadfile={loadAccountFile}
          onimport={runImport}
          oncloudchange={setCloudMail}
          oncloudsave={saveCloudMail}
          oncloudprobe={probeCloudMail}
          oncloudtoken={generateCloudMailToken}
        />
      {:else if activePane === 'phone'}
        <PhoneSms
          value={smsbower}
          busy={smsSaving || !settingsReady}
          cantestbalance
          error={smsError}
          status={smsStatus}
          {turnstile}
          {turnstileBusy}
          {turnstileError}
          {turnstileStatus}
          onchange={(next) => {
            smsbower = next
            // The banner described the values as they were when 保存设置 ran.
            smsError = ''
            smsStatus = ''
          }}
          ontestbalance={testSmsBower}
          onsave={saveSmsBower}
          onturnstilechange={setTurnstile}
          onturnstilesave={saveTurnstile}
          onturnstileprobe={probeTurnstile}
        />

        {#if phonePoolError}
          <p class="banner warn">{phonePoolError}</p>
        {/if}
        <PhonePoolFull
          {phones}
          inputText={phoneInput}
          maxReceiveCount={phoneMaxReceiveCount}
          selectedKey={selectedPhoneKey}
          busy={phonePoolBusy || !settingsReady}
          oninputchange={(value) => (phoneInput = value)}
          onmaxreceivechange={setPhoneMaxReceiveCount}
          onselect={(key) => (selectedPhoneKey = key)}
          onaction={handlePhonePoolAction}
        />
      {:else if activePane === 'proxy'}
        <ProxySettingsFull
          {localProxy}
          {routeMode}
          {pools}
          counts={proxyPoolCounts}
          {providers}
          {providerStatuses}
          region={proxyRegion}
          {raceConcurrency}
          {precheckLimit}
          {precheckConcurrency}
          reuse={reuseProxies}
          {requireJapan}
          {registerWithPaymentProxy}
          {forceLegacyPaypal}
          extensionDir={paypal.extensionDir}
          busy={providerBusy || !settingsReady}
          onlocalproxychange={setLocalProxy}
          onroutemodechange={setRouteMode}
          onpoolchange={setPool}
          onchange={setProxyFull}
          onaction={handleProxyFullAction}
        />
      {:else if activePane === 'payment'}
        <PaymentProfile
          value={paypal}
          busy={paypalSaving || !settingsReady}
          canpickdir
          status={paypalStatus}
          onchange={setPaypal}
          onpickdir={choosePaymentExtensionDirectory}
          onsave={savePaypal}
        />

        {#if paymentCardStatus}
          <p class="banner">{paymentCardStatus}</p>
        {/if}
        <PaymentCardPoolFull
          cards={paymentCards}
          inputText={paymentCardInput}
          busy={paymentCardBusy}
          oninputchange={(value) => (paymentCardInput = value)}
          onaction={handlePaymentCardAction}
        />
      {:else if activePane === 'actions'}
        <ActionsFull
          activeGroup={activeActionGroup}
          {manualEmailOtp}
          {convertFormat}
          {k12WorkspaceId}
          {k12Concurrency}
          busy={busy || !settingsReady}
          ongroupchange={setActionGroup}
          onmanualemailotpchange={setManualEmailOtp}
          onconvertformatchange={setConvertFormat}
          onk12workspaceidchange={setK12WorkspaceId}
          onk12concurrencychange={setK12Concurrency}
          onaction={handleFullAction}
        />
      {:else if activePane === 'settings'}
        <GeneralSettings
          value={sound}
          busy={audioBusy || !settingsReady}
          devices={audioDevices}
          canrefreshdevices={typeof navigator !== 'undefined' && Boolean(navigator.mediaDevices?.enumerateDevices)}
          cantestsound={typeof window !== 'undefined' && Boolean(window.AudioContext)}
          onchange={setSound}
          onrefreshdevices={refreshAudioDevices}
          ontestsound={testSuccessSound}
        />
      {:else}
        <section class="card">
          <h2>{active}</h2>
          <p class="muted">无法识别此工作区页面，已保留当前账户选择。</p>
        </section>
      {/if}
    </div>

    <footer class="taskbar">
      <span>{taskSummary}</span>
      <button title="打开任务与日志页面。" onclick={() => navigate('jobs')}>查看日志</button>
    </footer>
  </main>

  <!-- S4 选择账户 dock. app.py 12790-12843: header + summary, 搜索 + ×, a
       邮箱 / 类型 / 状态 list, then 全选结果 / 删除所选 / 清空选择. -->
  <aside class="dock">
    <header>
      <strong>选择账户</strong>
      <!-- app.py 19253: `显示 N/M · 已选 K` — NOT §S4's `账户 N · 已选 N`. -->
      <span class="muted">显示 {dockRows.length}/{rows.length} · 已选 {selected.length}</span>
    </header>

    <div class="dock-search">
      <input placeholder="搜索" bind:value={dockSearch} />
      <button class="icon" title="清空右侧账户搜索。" onclick={() => (dockSearch = '')}>×</button>
    </div>

    <div class="dock-list">
      {#if accountsError}
        <p class="muted">{accountsError}</p>
      {:else if dockRows.length === 0}
        <p class="muted">{rows.length === 0 ? '请先导入邮箱' : '当前分组没有可见邮箱'}</p>
      {:else}
        {#each dockRows as row (row.key)}
          <button
            class="dock-row"
            class:selected={selectedKeys.has(row.key)}
            title={row.email}
            onclick={(e) => {
              if (e.ctrlKey || e.metaKey) {
                const next = new Set(selected)
                if (next.has(row.key)) next.delete(row.key)
                else next.add(row.key)
                selected = [...next]
              } else {
                selected = [row.key]
              }
            }}
          >
            <span class="dock-email">{row.email}</span>
            <!-- app.py 19240: `{account_type or 'unknown'} · {status}`. -->
            <span class="dock-detail muted">{row.account_type || 'unknown'} · {row.statusText}</span>
          </button>
        {/each}
      {/if}
    </div>

    <footer>
      <button
        disabled={dockRows.length === 0}
        title="选中右侧搜索结果中的全部账户。"
        onclick={() => (selected = dockRows.map((row) => row.key))}>全选结果</button
      >
      <button
        class="danger"
        disabled={selected.length === 0 || busy || accountDialogBusy}
        title="删除当前选中的账户；执行前会再次显示数量和邮箱进行确认。"
        onclick={() => void deleteSelectedAccounts()}>删除所选</button
      >
      <button disabled={selected.length === 0} title="清空当前账户选择。" onclick={() => (selected = [])}
        >清空选择</button
      >
    </footer>
  </aside>
</div>

<AccountManagementDialog
  open={accountDialogOpen}
  emails={selectedEmails}
  currentType={selectedRows.length > 0 &&
  selectedRows.every((row) => row.account_type === selectedRows[0].account_type)
    ? selectedRows[0].account_type
    : ''}
  currentGroup={selectedRows.length > 0 &&
  selectedRows.every((row) => row.group === selectedRows[0].group)
    ? selectedRows[0].group
    : ''}
  groups={knownGroups}
  busy={accountDialogBusy}
  error={accountDialogError}
  onclose={() => {
    if (!accountDialogBusy) accountDialogOpen = false
  }}
  onsettype={setSelectedAccountType}
  onmovetogroup={moveSelectedToGroup}
  ondelete={deleteSelectedAccounts}
/>

<GroupManagementDialog
  open={groupDialogOpen}
  mode={groupDialogMode}
  currentGroup={group}
  groups={knownGroups}
  affectedCount={rows.filter((row) => row.group === group).length}
  busy={groupDialogBusy}
  error={groupDialogError}
  onclose={() => {
    if (!groupDialogBusy) groupDialogOpen = false
  }}
  onsubmit={submitGroupOperation}
/>

<AutoClassifyDialog
  open={autoClassifyOpen}
  accountCount={rows.length}
  selectedCount={selected.length}
  currentCount={actionVisibleRows.length}
  busy={autoClassifyBusy}
  error={autoClassifyError}
  onclose={() => {
    if (!autoClassifyBusy) autoClassifyOpen = false
  }}
  onsubmit={submitAutoClassify}
/>

<ManualSessionDialog
  open={manualSessionOpen}
  mode={manualSessionMode}
  email={selectedEmails[0] ?? ''}
  busy={manualSessionBusy}
  error={manualSessionError}
  onclose={() => {
    if (!manualSessionBusy) manualSessionOpen = false
  }}
  onsubmit={submitManualSession}
/>

<MailboxManagerDialog
  open={mailboxOpen}
  email={mailboxEmail}
  aliasParent={mailboxAliasParent}
  status={mailboxStatus}
  folders={mailboxFolders}
  folder={mailboxFolder}
  limit={mailboxLimit}
  search={mailboxSearch}
  messages={mailboxMessages}
  selectedMessageId={mailboxSelectedMessage}
  body={mailboxBody}
  busy={mailboxBusy}
  error={mailboxError}
  onclose={() => (mailboxOpen = false)}
  onfolderchange={(value) => {
    mailboxFolder = value
    void refreshMailboxMessages(false)
  }}
  onlimitchange={(value) => (mailboxLimit = value)}
  onsearchchange={(value) => (mailboxSearch = value)}
  onselect={(value) => (mailboxSelectedMessage = value)}
  onaction={handleMailboxAction}
/>

<ProviderProxyDialog
  open={providerDialogOpen}
  role={providerDialogRole}
  value={providers[providerDialogRole]}
  busy={providerBusy}
  error={providerError}
  onclose={() => {
    if (!providerBusy) providerDialogOpen = false
  }}
  onsave={saveProvider}
/>

<!-- S26 Prompt 输入. app.py 19002-19015 raises a modal from the event pump and
     pushes the answer into the queue the worker blocks on; here the worker
     blocks on a channel and AnswerPrompt(jobId, …) releases it. An empty answer
     reads as "cancelled" — the same thing 停止 pushes. Without this dialog a
     flow that needs a manual code just hangs for the 10-minute Go timeout. -->

<PromptDialog request={prompt} onanswer={answerPrompt} />

<!-- S25 导出预览. Not behind ConfirmAction: it writes a file, it does not spend. -->
<ExportPreviewDialog
  preview={exportPreview}
  busy={exportBusy}
  oncancel={closeExportPreview}
  oncopy={copyExportPreview}
  onsave={saveExport}
/>

<!-- The gate in front of every spending button. It is deliberately the LAST
     thing in the tree so it paints over everything, and its cancel button takes
     focus on arrival, so Enter-on-arrival aborts instead of spending. -->
{#key confirmRequest}
  <ConfirmAction request={confirmRequest} oncancel={cancelConfirm} onconfirm={runConfirmed} />
{/key}

<style>
  .shell {
    display: flex;
    height: 100%;
  }
  main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .content {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 14px;
    overflow: auto;
  }
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
  }
  h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0 0 10px;
  }
  .banner {
    margin: 0;
    padding: 8px 12px;
    border-radius: 4px;
    background: #fee4e2;
    border: 1px solid #fda29b;
  }
  .err {
    color: var(--err);
  }
  .banner.ok {
    color: #067647;
    background: #ecfdf3;
    border-color: #6ce9a6;
  }
  .banner.warn {
    color: #7a2e0e;
    background: #fff4e5;
    border-color: #fdb022;
  }
  .management-toolbar {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .management-toolbar h2 {
    margin: 0;
  }
  .management-buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  .taskbar {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    background: var(--taskbar);
    border-top: 1px solid var(--border);
    padding: 6px 14px;
  }
  .dock {
    width: var(--dock-w);
    flex: 0 0 var(--dock-w);
    background: var(--panel);
    border-left: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
  }
  .dock header {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .dock-search {
    display: flex;
    gap: 4px;
  }
  .dock-search input {
    flex: 1;
    min-width: 0;
  }
  button.icon {
    padding: 4px 8px;
  }
  .dock-list {
    flex: 1;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface);
    padding: 4px;
    overflow: auto;
  }
  .dock-list p {
    margin: 6px;
  }
  button.dock-row {
    display: flex;
    width: 100%;
    gap: 8px;
    align-items: baseline;
    background: transparent;
    border: none;
    border-radius: 3px;
    padding: 4px 6px;
    text-align: left;
  }
  button.dock-row:hover {
    background: var(--head-bg);
  }
  button.dock-row.selected {
    background: var(--sel-bg);
    color: var(--sel-fg);
  }
  /* app.py 12818-12819: 邮箱 188px, 类型 / 状态 108px. */
  .dock-email {
    flex: 1 1 188px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dock-detail {
    flex: 0 1 108px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dock footer {
    display: flex;
    justify-content: space-between;
    gap: 8px;
  }
  .dock footer button {
    flex: 1 1 0;
    min-width: 0;
    padding-inline: 8px;
  }

  /* The two modals (PromptDialog, ConfirmAction) carry their own scoped
     styles — Svelte scoping means a `.modal` rule here would not reach them
     anyway. */
</style>
