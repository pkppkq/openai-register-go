/*
  UI 与 Go 的唯一桥接入口。

  组件不得直接导入 `frontend/wailsjs`。绑定方法在这里完整重导出；数据结构
  则始终复用代码生成的模型，避免前端手写副本与 Go JSON 字段静默漂移。
  只有纯事件 payload（不会出现在绑定签名里）允许在下方手写声明。
*/

// -- Wails 绑定 -------------------------------------------------------------

export {
  AnswerPrompt,
  ApplyProviderProxySettings,
  AutoClassifyAccounts,
  CancelJob,
  ClearAccountWorkflow,
  ClearAccounts,
  ClearPhones,
  ConsumePaymentCard,
  CountProxyPoolText,
  CopyExportPreview,
  CopySessionConversion,
  CreateAccountGroup,
  CreateDomainMailAccounts,
  CreatePlusAliasAccounts,
  DeleteAccountGroup,
  DeleteAccounts,
  Environment,
  ExportMissingRT,
  ExtractMailboxCode,
  ExtractMailboxInviteLink,
  GenerateLinks,
  GetAccountDetails,
  GetMailboxMessage,
  GetNetworkJobResult,
  ImportAccounts,
  ImportPaymentCards,
  ImportPhones,
  ListAccounts,
  ListJobs,
  ListMailboxFolders,
  ListMailboxMessages,
  ListPaymentCards,
  ListPhones,
  LoadLogs,
  LoadSettings,
  LoadSummary,
  Log,
  MergeManualSession,
  MoveAccountsToGroup,
  OpenAccountFile,
  OpenPaymentExtensionDirectory,
  PrepareSub2APIExport,
  PreviewExport,
  ProviderProxyStatuses,
  RenameAccountGroup,
  ReplaceManualSession,
  ReplacePayPalCardHead,
  ResetPaymentCards,
  ResetPhones,
  SaveExport,
  SaveSettings,
  SaveSub2APIExport,
  SelectLogAccount,
  SetAccountType,
  StartAcceptWorkspaceInvite,
  StartBatchGenerateLinks,
  StartBatchRelink,
  StartBatchRegister,
  StartCPAExportRefresh,
  StartCloudMailProbe,
  StartCloudMailTokenGeneration,
  StartDeactivationScan,
  StartDomainRandomRT,
  StartExternalOAuth,
  StartK12AcceptAndRefresh,
  StartK12RegisterAndJoin,
  StartK12RequestInvite,
  StartKeepLogin,
  StartManualLoginCode,
  StartManualPhoneCode,
  StartOAuthAuthorizeRT,
  StartOpenPaymentWindow,
  StartOpenPaymentWindows,
  StartOpenSessionReader,
  StartProtocolRegisterSession,
  StartProxyPoolCleanup,
  StartProxyPoolPrecheck,
  StartRefreshAccountType,
  StartRefreshSession,
  StartRegister,
  StartSMSBowerReadTest,
  StartSub2APIExport,
  StartTeamInvite,
  StartTeamInviteScanJoin,
  StartTeamLeave,
  StartTrialEligibility,
  StartTurnstileProbe,
  StopAll,
  StopProviderProxyPools,
  SwitchPaymentWindowProxy,
} from '../../wailsjs/go/ui/App'

import { PatchSettings as PatchSettingsBinding } from '../../wailsjs/go/ui/App'
import type { logs, providerproxy, settings, ui } from '../../wailsjs/go/models'

// -- 代码生成模型 -----------------------------------------------------------

export type LogRecord = logs.Record
export type ProviderProxyStatus = providerproxy.Status
export type ProviderProxyConfig = settings.ProviderProxyConfig
export type Settings = settings.Settings
type DataKey<T> = {
  [K in keyof T]-?: T[K] extends (...args: never[]) => unknown ? never : K
}[keyof T]
/** 仅允许设置模型中的数据字段；生成类的 convertValues 方法不会进入 patch。 */
export type SettingsPatch = Partial<Pick<Settings, DataKey<Settings>>>

export type AccountDetailAccount = ui.AccountDetailAccount
export type AccountDetails = ui.AccountDetails
export type AccountFilter = ui.AccountFilter
export type AccountMutationResult = ui.AccountMutationResult
export type AccountPage = ui.AccountPage
export type AccountRow = ui.AccountRow
export type AutoClassifyRequest = ui.AutoClassifyRequest
export type AutoClassifyResult = ui.AutoClassifyResult
export type AuthBatchRequest = ui.AuthBatchRequest
export type BatchRelinkRequest = ui.BatchRelinkRequest
export type BatchRelinkSummary = ui.BatchRelinkSummary
export type BatchSummary = ui.BatchSummary
export type CloudMailProbeRequest = ui.CloudMailProbeRequest
export type CloudMailTokenRequest = ui.CloudMailTokenRequest
export type DeactivationScanRequest = ui.DeactivationScanRequest
export type DomainMailRequest = ui.DomainMailRequest
export type DomainRandomRTRequest = ui.DomainRandomRTRequest
export type DomainRandomRTResult = ui.DomainRandomRTResult
export type Env = ui.Env
export type ExportPreview = ui.ExportPreview
export type ExportResult = ui.ExportResult
export type ExternalOAuthRequest = ui.ExternalOAuthRequest
export type GenerateLinksRequest = ui.GenerateLinksRequest
export type GeneratedAccountsResult = ui.GeneratedAccountsResult
export type GroupMutationResult = ui.GroupMutationResult
export type ImportResult = ui.ImportResult
export type JobView = ui.JobView
export type K12InviteFlowRequest = ui.K12InviteFlowRequest
export type K12RequestInviteRequest = ui.K12RequestInviteRequest
export type LinkBatchSummary = ui.LinkBatchSummary
export type LogSnapshot = ui.LogSnapshot
export type MailboxMessage = ui.MailboxMessage
export type MailboxMessageRequest = ui.MailboxMessageRequest
export type MailboxMessagesRequest = ui.MailboxMessagesRequest
export type MissingRTView = ui.MissingRTView
export type NetworkJobResult = ui.NetworkJobResult
export type OpenPaymentWindowRequest = ui.OpenPaymentWindowRequest
export type OpenPaymentWindowsRequest = ui.OpenPaymentWindowsRequest
export type OpenPaymentWindowsResult = ui.OpenPaymentWindowsResult
export type PaymentCardConsumeResult = ui.PaymentCardConsumeResult
export type PaymentCardsResult = ui.PaymentCardsResult
export type PaymentCardView = ui.PaymentCardView
export type PaymentProxySwitchResult = ui.PaymentProxySwitchResult
export type PhonesResult = ui.PhonesResult
export type PhoneView = ui.PhoneView
export type PlusAliasRequest = ui.PlusAliasRequest
export type ProviderProxyStatusView = ui.ProviderProxyStatusView
export type ProxyPoolOperationRequest = ui.ProxyPoolOperationRequest
export type RefreshAccountTypeRequest = ui.RefreshAccountTypeRequest
export type SessionRefreshRequest = ui.SessionRefreshRequest
export type SessionSaveResult = ui.SessionSaveResult
export type SMSBowerReadRequest = ui.SMSBowerReadRequest
export type StartBatchRequest = ui.StartBatchRequest
export type StartLinkBatchRequest = ui.StartLinkBatchRequest
export type StartRegisterRequest = ui.StartRegisterRequest
export type StateSummary = ui.StateSummary
export type Sub2APIPlan = ui.Sub2APIPlan // gitleaks:allow，类型名不是凭据
export type Sub2APISaveResult = ui.Sub2APISaveResult // gitleaks:allow，类型名不是凭据
export type TeamInviteRequest = ui.TeamInviteRequest
export type TeamInviteScanJoinRequest = ui.TeamInviteScanJoinRequest
export type TeamLeaveRequest = ui.TeamLeaveRequest
export type TrialEligibilityRequest = ui.TrialEligibilityRequest
export type TurnstileProbeRequest = ui.TurnstileProbeRequest
export type WorkspaceInviteRequest = ui.WorkspaceInviteRequest
export type WorkflowClearResult = ui.WorkflowClearResult

/** 代码生成器把 Go 的字符串枚举生成为 string，因此在这里保留六个线值。 */
export type ExportKind = 'raw' | 'authorized' | 'email_rt' | 'sessions' | 'conversion' | 'conversion_zip'

// -- 代码生成器无法生成的事件契约 -------------------------------------------

/**
 * Wails 只为绑定方法生成代码，事件名不会出现在方法签名中，因此必须与 Go
 * 常量逐字同步。改错事件名不会报错，只会导致对应订阅永远收不到消息。
 */
export const EVENT_LOG = 'log'
export const EVENT_LOG_RECORD = 'log-record'
export const EVENT_JOB = 'job'
export const EVENT_PROMPT = 'prompt'
export const EVENT_PROVIDER_PROXY_STATUS = 'provider-proxy-status'

/**
 * `ui.PromptRequest` 只作为事件 payload，不会被代码生成器发现。它按任务 ID
 * 路由；同一任务的单条 goroutine 同时最多等待一个人工输入。
 */
export type PromptRequest = {
  jobId: string
  /** email-code | phone | phone-code | … */
  kind: string
  email: string
  /** 后端给出的中文提示，应原样展示。 */
  prompt: string
}

/**
 * `ui.ProviderProxyStatusEvent` 同样只由事件发送。与完整快照相比，实时事件
 * 不携带配置，只更新指定阶段的库存状态。
 */
export type ProviderProxyStatusEvent = {
  role: string
  label: string
  status: ProviderProxyStatus
  text: string
}

/**
 * 与 Go 的 `ui.promptTimeout` 同步。前端倒计时只作提示，最终仍以
 * `AnswerPrompt` 是否接受回复为准。
 */
export const PROMPT_TIMEOUT_MS = 10 * 60 * 1000

/**
 * Go 的 `JobKind` 被生成为普通 string；这里汇总 worker、批量父任务与远程
 * 操作的全部线值。
 */
export type JobKind =
  | 'register'
  | 'auth_only'
  | 'team'
  | 'register_and_rt'
  | 'relink'
  | 'batch_register'
  | 'batch_link'
  | 'batch_relink'
  | 'refresh_account_type'
  | 'team_invite'
  | 'team_leave'
  | 'k12_request_invite'
  | 'trial_eligibility'
  | 'deactivation_scan'
  | 'turnstile_probe'
  | 'smsbower_read'
  | 'cloud_mail_probe'
  | 'cloud_mail_token'
  | 'cpa_export_refresh'
  | 'sub2api_export'
  | 'manual_phone_code'
  | 'payment_window'
  | 'proxy_pool_precheck'
  | 'proxy_pool_cleanup'
  | 'session_refresh'
  | 'k12_session_refresh'
  | 'workspace_invite_accept'
  | 'protocol_register'
  | 'protocol_register_batch'
  | 'oauth_authorize'
  | 'oauth_authorize_batch'
  | 'keep_login'
  | 'session_reader'
  | 'external_oauth'
  | 'manual_login_code'
  | 'team_invite_scan_join_batch'
  | 'team_invite_scan_join'
  | 'k12_accept_refresh_batch'
  | 'k12_accept_refresh'
  | 'k12_register_join_batch'
  | 'k12_register_join'

/** `ui.JobStatus` (jobs.go:47-52), for the same reason. */
export type JobStatus = 'running' | 'succeeded' | 'failed' | 'cancelled'

/**
 * The zero filter: every account, in stored order.
 *
 * An object literal is allowed here because the generated `AccountFilter` has
 * no methods — unlike `Settings` / `AccountPage`, which carry `convertValues`
 * and can therefore only be produced by the backend or by mutation.
 */
export const ALL_ACCOUNTS: AccountFilter = {
  group: '',
  status: '',
  search: '',
  sortColumn: '',
  sortDirection: '',
  offset: 0,
  limit: 0,
}

// -- Settings write discipline -----------------------------------------------

/**
 * 原子字段级更新。Go 会在持有状态锁时读取最新快照并只覆盖这些键；前端不再
 * 执行 LoadSettings → SaveSettings 整份回写，因此不会复活后台刚轮换的代理
 * 或手机号游标。这个薄包装只负责从生成类中排除方法键。
 */
export function PatchSettings(patch: SettingsPatch): Promise<Settings> {
  return PatchSettingsBinding(patch as Record<string, any>)
}
