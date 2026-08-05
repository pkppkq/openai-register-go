/*
  The three non-generated values in api.ts.

  This file imports `../api` for real, which means it also loads the module that
  re-exports the Wails bindings. That import is safe only because
  `vite.config.ts` aliases the generated bridge onto `wails-bridge-refusal.ts`
  for test runs; nothing here calls a binding, and the alias guarantees that a
  future edit which does cannot reach the backend.

  设置更新现在直接走 Go 的原子 PatchSettings，不再测试前端整份对象改写。
*/
import { describe, expect, it } from 'vitest'
import * as api from '../api'
import {
  ALL_ACCOUNTS,
  EVENT_JOB,
  EVENT_LOG,
  EVENT_LOG_RECORD,
  EVENT_PROMPT,
  EVENT_PROVIDER_PROXY_STATUS,
  PROMPT_TIMEOUT_MS,
  type JobKind,
  type SettingsPatch,
} from '../api'

// 与当前生成绑定保持逐字一致；每个名字都必须经过测试拒绝墙。
const BOUND_METHOD_NAMES = [
  'AnswerPrompt',
  'ApplyProviderProxySettings',
  'AutoClassifyAccounts',
  'CancelJob',
  'ClearAccountWorkflow',
  'ClearAccounts',
  'ClearPhones',
  'ConsumePaymentCard',
  'CountProxyPoolText',
  'CopyExportPreview',
  'CopySessionConversion',
  'CreateAccountGroup',
  'CreateDomainMailAccounts',
  'CreatePlusAliasAccounts',
  'DeleteAccountGroup',
  'DeleteAccounts',
  'Environment',
  'ExportMissingRT',
  'ExtractMailboxCode',
  'ExtractMailboxInviteLink',
  'GenerateLinks',
  'GetAccountDetails',
  'GetMailboxMessage',
  'GetNetworkJobResult',
  'ImportAccounts',
  'ImportPaymentCards',
  'ImportPhones',
  'ListAccounts',
  'ListJobs',
  'ListMailboxFolders',
  'ListMailboxMessages',
  'ListPaymentCards',
  'ListPhones',
  'LoadLogs',
  'LoadSettings',
  'LoadSummary',
  'Log',
  'MergeManualSession',
  'MoveAccountsToGroup',
  'OpenAccountFile',
  'OpenPaymentExtensionDirectory',
  'PatchSettings',
  'PrepareSub2APIExport',
  'PreviewExport',
  'ProviderProxyStatuses',
  'RenameAccountGroup',
  'ReplaceManualSession',
  'ReplacePayPalCardHead',
  'ResetPaymentCards',
  'ResetPhones',
  'SaveExport',
  'SaveSettings',
  'SaveSub2APIExport',
  'SelectLogAccount',
  'SetAccountType',
  'StartAcceptWorkspaceInvite',
  'StartBatchGenerateLinks',
  'StartBatchRelink',
  'StartBatchRegister',
  'StartCPAExportRefresh',
  'StartCloudMailProbe',
  'StartCloudMailTokenGeneration',
  'StartDeactivationScan',
  'StartDomainRandomRT',
  'StartExternalOAuth',
  'StartK12AcceptAndRefresh',
  'StartK12RegisterAndJoin',
  'StartK12RequestInvite',
  'StartKeepLogin',
  'StartManualLoginCode',
  'StartManualPhoneCode',
  'StartOAuthAuthorizeRT',
  'StartOpenPaymentWindow',
  'StartOpenPaymentWindows',
  'StartOpenSessionReader',
  'StartProtocolRegisterSession',
  'StartProxyPoolCleanup',
  'StartProxyPoolPrecheck',
  'StartRefreshAccountType',
  'StartRefreshSession',
  'StartRegister',
  'StartSMSBowerReadTest',
  'StartSub2APIExport',
  'StartTeamInvite',
  'StartTeamInviteScanJoin',
  'StartTeamLeave',
  'StartTrialEligibility',
  'StartTurnstileProbe',
  'StopAll',
  'StopProviderProxyPools',
  'SwitchPaymentWindowProxy',
] as const satisfies readonly (keyof typeof api)[]

const JOB_KINDS = [
  'register',
  'auth_only',
  'team',
  'register_and_rt',
  'relink',
  'batch_register',
  'batch_link',
  'batch_relink',
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
] as const satisfies readonly JobKind[]

type MissingJobKind = Exclude<JobKind, (typeof JOB_KINDS)[number]>
const JOB_KIND_LIST_IS_EXHAUSTIVE: [MissingJobKind] extends [never] ? true : false = true

describe('SettingsPatch', () => {
  it('accepts generated Settings data keys without requiring a complete settings object', () => {
    const patch: SettingsPatch = {
      smsbower_service: 'ot',
      smsbower_enabled: false,
      payment_extension_dir: '',
    }
    expect(patch).toEqual({
      smsbower_service: 'ot',
      smsbower_enabled: false,
      payment_extension_dir: '',
    })
  })
})

describe('ALL_ACCOUNTS', () => {
  it('is the zero filter: every row, stored order, no paging', () => {
    // `ui.AccountFilter`'s zero value. Empty group/status/search filter nothing,
    // an empty sortColumn/sortDirection is accounts.SortCustom (app.py's
    // ACCOUNT_SORT_CUSTOM, 305), and limit <= 0 returns everything.
    expect(ALL_ACCOUNTS).toEqual({
      group: '',
      status: '',
      search: '',
      sortColumn: '',
      sortDirection: '',
      offset: 0,
      limit: 0,
    })
  })

  it('carries all seven keys, so it can be spread over without holes', () => {
    expect(Object.keys(ALL_ACCOUNTS).sort()).toEqual(
      ['group', 'limit', 'offset', 'search', 'sortColumn', 'sortDirection', 'status'].sort(),
    )
  })
})

describe('the event names', () => {
  it('are the Go constants verbatim', () => {
    expect(EVENT_LOG).toBe('log')
    expect(EVENT_LOG_RECORD).toBe('log-record')
    expect(EVENT_JOB).toBe('job')
    expect(EVENT_PROMPT).toBe('prompt')
    expect(EVENT_PROVIDER_PROXY_STATUS).toBe('provider-proxy-status')
  })

  it('are five distinct names', () => {
    expect(
      new Set([EVENT_LOG, EVENT_LOG_RECORD, EVENT_JOB, EVENT_PROMPT, EVENT_PROVIDER_PROXY_STATUS]).size,
    ).toBe(5)
  })
})

describe('the complete Wails bridge surface', () => {
  it('re-exports all 91 generated methods', () => {
    expect(BOUND_METHOD_NAMES).toHaveLength(91)
    for (const name of BOUND_METHOD_NAMES) {
      expect(api[name], name).toBeTypeOf('function')
    }
  })

  it('refuses every binding call in Vitest before any backend can run', () => {
    for (const name of BOUND_METHOD_NAMES) {
      const binding = api[name] as unknown as (...args: unknown[]) => unknown
      expect(() => binding(), name).toThrow(`wails binding ${name} was called from a test`)
    }
  })
})

describe('JobKind', () => {
  it('contains every worker, batch and network job wire value', () => {
    expect(JOB_KIND_LIST_IS_EXHAUSTIVE).toBe(true)
    expect(new Set(JOB_KINDS).size).toBe(41)
  })
})

describe('PROMPT_TIMEOUT_MS', () => {
  it('is the ten minutes ui.promptTimeout gives a manual input', () => {
    // bindings.go:38-42. The dialog counts down against this; if the Go
    // constant moves and this does not, the modal lies about how long the user
    // has before `等待人工输入超时，已按取消处理`.
    expect(PROMPT_TIMEOUT_MS).toBe(600_000)
  })
})
