<script module lang="ts">
  /** 全部操作页的六个固定分组，顺序与 Python 版一致。 */
  export type ActionGroupKey = 'account' | 'auth' | 'link' | 'export' | 'team' | 'k12'

  export type FullActionKey =
    | 'import_accounts'
    | 'open_mailbox'
    | 'create_plus_alias'
    | 'create_domain_mail'
    | 'select_visible'
    | 'invert_visible'
    | 'clear_selection'
    | 'refresh_account_type'
    | 'auto_classify'
    | 'delete_selected'
    | 'clear_accounts'
    | 'keep_login'
    | 'auth_only'
    | 'register_session'
    | 'protocol_register_session'
    | 'refresh_session'
    | 'fill_session'
    | 'open_session_reader'
    | 'oauth_authorize_rt'
    | 'external_oauth'
    | 'manual_login_code'
    | 'deactivation_check'
    | 'domain_random_rt'
    | 'generate_from_session'
    | 'paste_session'
    | 'select_sessions'
    | 'trial_check'
    | 'batch_generate'
    | 'relink'
    | 'batch_relink'
    | 'switch_payment_proxy'
    | 'export_authorized'
    | 'export_email_rt'
    | 'export_sub2api'
    | 'export_sessions'
    | 'export_raw'
    | 'copy_conversion'
    | 'export_conversion'
    | 'export_zip'
    | 'team_invite'
    | 'team_leave'
    | 'team_scan_join'
    | 'k12_register_join'
    | 'k12_request_invite'
    | 'k12_accept_refresh'
    | 'k12_refresh'

  export type ActionTone = 'normal' | 'primary' | 'danger'

  export type FullActionDefinition = {
    key: FullActionKey
    label: string
    help: string
    tone?: ActionTone
  }

  export type FullActionGroup = {
    key: ActionGroupKey
    label: string
    actions: readonly FullActionDefinition[]
  }

  export const SESSION_CONVERT_FORMATS = [
    'sub2api',
    'cpa',
    'cockpit',
    '9router',
    'codex',
    'axonhub',
    'codexmanager',
  ] as const
  export type SessionConvertFormat = (typeof SESSION_CONVERT_FORMATS)[number]

  export const DEFAULT_K12_WORKSPACE_ID = 'workspace-example'

  /**
   * 标签和用途均逐字来自 app.py 的 `_button_grid` 调用。超过四项用表格，
   * 四项及以下用等宽按钮，这是原界面唯一的渲染分界。
   */
  export const FULL_ACTION_GROUPS: readonly FullActionGroup[] = [
    {
      key: 'account',
      label: '账号',
      actions: [
        { key: 'import_accounts', label: '导入', help: '把导入框中的邮箱加入当前分组。' },
        {
          key: 'open_mailbox',
          label: '邮箱管理',
          help: '打开选中邮箱的只读管理窗口，可查看文件夹、最近邮件和正文。',
        },
        {
          key: 'create_plus_alias',
          label: '别名注册',
          help: '为选中邮箱生成 +随机数字 别名账号；收信仍使用主邮箱 IMAP。',
        },
        {
          key: 'create_domain_mail',
          label: '生成域名邮箱',
          help: '为 @mail.example.com 批量生成随机地址并归入“域名邮箱分”；未配置 Outlook 主邮箱时自动使用 Cloud Mail API。',
        },
        { key: 'select_visible', label: '全选可见', help: '选中当前分组/筛选下所有可见邮箱。' },
        { key: 'invert_visible', label: '反选可见', help: '把当前分组/筛选下的可见邮箱选择状态反转。' },
        { key: 'clear_selection', label: '清空选择', help: '取消当前邮箱列表选择。' },
        { key: 'refresh_account_type', label: '刷新类型', help: '使用 OpenAI RT 刷新选中账号类型。' },
        {
          key: 'auto_classify',
          label: '自动分类',
          help: '按试用资格、提链结果或 Plus/Free 自动移动到分组。',
        },
        {
          key: 'delete_selected',
          label: '删除选中',
          help: '删除当前选中的邮箱和本地结果。',
          tone: 'danger',
        },
        {
          key: 'clear_accounts',
          label: '清空列表',
          help: '清空全部邮箱及其结果。',
          tone: 'danger',
        },
      ],
    },
    {
      key: 'auth',
      label: '注册Session',
      actions: [
        {
          key: 'keep_login',
          label: '登录并保留',
          help: '用选中邮箱完成 ChatGPT 登录并保留浏览器；默认自动收邮箱验证码，不获取 Session。勾选手动验证码时弹窗输入。',
        },
        {
          key: 'auth_only',
          label: '注册或登录',
          help: '完成选中账号认证并保留浏览器，不获取 Session。',
        },
        {
          key: 'register_session',
          label: '注册并取Session',
          help: '完成选中账号认证并保存 Session/Access Token。勾选手动验证码时不调用邮箱令牌。',
          tone: 'primary',
        },
        {
          key: 'protocol_register_session',
          label: '协议注册取Session',
          help: '不打开浏览器，走 OpenAI OAuth 请求链路；默认自动收邮箱验证码。勾选手动验证码时弹窗输入。',
        },
        {
          key: 'refresh_session',
          label: '刷新 Session',
          help: '使用已保存登录态重新获取 Access Token 和 Session JSON。',
        },
        {
          key: 'fill_session',
          label: '填入Session',
          help: '把自己复制的 ChatGPT Session JSON 或 Access Token 保存到当前选中邮箱。',
        },
        {
          key: 'open_session_reader',
          label: '辅助登录Session',
          help: '打开 ChatGPT 登录页并填入选中邮箱，后续手动完成后访问 /api/auth/session 复制 JSON。',
        },
        {
          key: 'oauth_authorize_rt',
          label: 'OAuth授权取RT',
          help: '为选中 Free/Plus/Team 账号执行 OAuth 授权并获取 OpenAI refresh_token；有 Team workspace 时优先选择 Team。',
        },
        {
          key: 'external_oauth',
          label: '打开OAuth链接',
          help: '粘贴外部生成的 OAuth 链接并自动登录；窗口不自动关闭。',
        },
        {
          key: 'manual_login_code',
          label: '手动登录取码',
          help: '打开 ChatGPT 登录页并监听选中邮箱验证码，收到后弹窗显示。',
        },
        {
          key: 'deactivation_check',
          label: '查封禁邮件',
          help: '扫描选中邮箱是否收到 OpenAI Access Deactivated / 停用通知；支持 +别名 精确匹配。',
        },
        {
          key: 'domain_random_rt',
          label: '域名邮箱随机取RT',
          help: '生成随机 @mail.example.com 地址，通过 Cloud Mail 收码注册并获取 RT。',
        },
      ],
    },
    {
      key: 'link',
      label: '支付链接',
      actions: [
        { key: 'generate_from_session', label: 'Session 生成', help: '使用选中账号 Session 生成支付链接。' },
        { key: 'paste_session', label: '粘贴 Session', help: '粘贴并保存 Session JSON 或 Access Token。' },
        { key: 'select_sessions', label: '全选有 Session', help: '选择当前分组中已有 Access Token 的账号。' },
        {
          key: 'trial_check',
          label: '检测试用',
          help: '检测选中 Session 是否有 Plus 免费试用资格，不打开支付窗口。',
        },
        { key: 'batch_generate', label: '批量生成', help: '为选中 Session 批量生成支付链接。' },
        { key: 'relink', label: '重新获取', help: '重新登录选中账号并获取支付链接。' },
        { key: 'batch_relink', label: '批量重新获取', help: '并发重新登录多个选中账号并获取支付链接。' },
        { key: 'switch_payment_proxy', label: '切换支付代理', help: '试用流程中切换当前页面代理。' },
      ],
    },
    {
      key: 'export',
      label: '导出转换',
      actions: [
        { key: 'export_authorized', label: '已授权', help: '导出已授权账号。' },
        { key: 'export_email_rt', label: '邮箱 RT', help: '导出邮箱 refresh_token。' },
        { key: 'export_sub2api', label: 'sub2api', help: '导出 sub2api 格式。' },
        { key: 'export_sessions', label: '选中 Session', help: '导出当前选中的 Session JSON。' },
        { key: 'export_raw', label: '选中 Raw', help: '导出当前选中账号的 Raw 内容。' },
        {
          key: 'copy_conversion',
          label: '复制转换',
          help: '把选中 Session 转成所选格式并复制到剪贴板。',
        },
        {
          key: 'export_conversion',
          label: '导出转换',
          help: '把选中 Session 转成所选格式并保存为 JSON 文件。',
        },
        {
          key: 'export_zip',
          label: '导出ZIP',
          help: '把选中账号按当前格式分别导出为独立 JSON，并打包成 ZIP。',
        },
      ],
    },
    {
      key: 'team',
      label: 'Team',
      actions: [
        {
          key: 'team_invite',
          label: '邀请成员',
          help: '使用选中账号的 Team Access Token 邀请指定邮箱；只发邀请，不自动接受或踢人。',
          tone: 'primary',
        },
        {
          key: 'team_leave',
          label: '退出 Team',
          help: '使用选中成员自己的 Access Token 退出当前 Team；Owner 会被拦截。',
          tone: 'danger',
        },
        {
          key: 'team_scan_join',
          label: '扫描邀请加入',
          help: '扫描选中邮箱中的 ChatGPT Team/Business 邀请；需要时自动登录，接受邀请、切换 Team workspace 并刷新 Session。',
        },
      ],
    },
    {
      key: 'k12',
      label: 'K12',
      actions: [
        {
          key: 'k12_register_join',
          label: '一键注册加入',
          help: '按“注册或登录并获取 Session -> 请求邀请 -> 接受邀请 -> 刷新 Session”顺序执行；支持多选并发。',
          tone: 'primary',
        },
        {
          key: 'k12_request_invite',
          label: '请求邀请',
          help: '使用选中账号 Access Token 请求 K12 workspace invite；支持多选并发。',
        },
        {
          key: 'k12_accept_refresh',
          label: '接受并刷新',
          help: '请求 K12 邀请、等待邮箱邀请链接、打开接受后刷新 Session；支持多选并发。',
        },
        {
          key: 'k12_refresh',
          label: '刷新K12',
          help: '强制切到 K12 workspace 后刷新 Session；支持多选并发，普通 Plus 账号不要点这个。',
        },
      ],
    },
  ]

  export function actionGroup(key: ActionGroupKey): FullActionGroup {
    return FULL_ACTION_GROUPS.find((group) => group.key === key) ?? FULL_ACTION_GROUPS[0]
  }

  export function actionRenderMode(group: FullActionGroup): 'table' | 'buttons' {
    return group.actions.length > 4 ? 'table' : 'buttons'
  }

  export function actionTableHeight(group: FullActionGroup): number {
    return Math.min(6, Math.max(3, group.actions.length))
  }
</script>

<script lang="ts">
  let {
    activeGroup = 'account',
    manualEmailOtp,
    convertFormat,
    k12WorkspaceId,
    k12Concurrency,
    busy = false,
    disabledActions = [],
    ongroupchange,
    onmanualemailotpchange,
    onconvertformatchange,
    onk12workspaceidchange,
    onk12concurrencychange,
    onaction,
  }: {
    activeGroup?: ActionGroupKey
    manualEmailOtp: boolean
    convertFormat: SessionConvertFormat
    k12WorkspaceId: string
    k12Concurrency: number
    busy?: boolean
    disabledActions?: readonly FullActionKey[]
    ongroupchange: (group: ActionGroupKey) => void
    onmanualemailotpchange: (enabled: boolean) => void
    onconvertformatchange: (format: SessionConvertFormat) => void
    onk12workspaceidchange: (workspaceId: string) => void
    onk12concurrencychange: (concurrency: number) => void
    onaction: (action: FullActionKey) => void
  } = $props()

  let selectedRows = $state<Partial<Record<ActionGroupKey, FullActionKey>>>({})
  let currentGroup = $derived(actionGroup(activeGroup))
  let disabledSet = $derived(new Set(disabledActions))
  let selectedAction = $derived(selectedRows[activeGroup] ?? currentGroup.actions[0]?.key)

  function selectAction(key: FullActionKey) {
    selectedRows = { ...selectedRows, [activeGroup]: key }
  }

  function run(action: FullActionDefinition | undefined) {
    if (!action || busy || disabledSet.has(action.key)) return
    onaction(action.key)
  }

  function clampConcurrency(raw: string): number {
    const parsed = Number.parseInt(raw, 10)
    if (!Number.isFinite(parsed)) return 1
    return Math.min(30, Math.max(1, parsed))
  }
</script>

<section class="card actions-full" aria-label="全部操作">
  <header>
    <div>
      <h2>全部操作</h2>
      <p>按功能分组执行；表格可双击，按钮和每一行均保留原版用途提示。</p>
    </div>
  </header>

  <div class="group-tabs" role="tablist" aria-label="操作分组">
    {#each FULL_ACTION_GROUPS as group (group.key)}
      <button
        role="tab"
        aria-selected={activeGroup === group.key}
        class:active={activeGroup === group.key}
        title={`切换到${group.label}操作。`}
        onclick={() => ongroupchange(group.key)}>{group.label}</button
      >
    {/each}
  </div>

  {#if activeGroup === 'auth'}
    <label class="option" title="启用后，注册和登录流程不自动调用邮箱令牌或 IMAP，而是弹窗等待人工输入验证码。">
      <input
        type="checkbox"
        checked={manualEmailOtp}
        disabled={busy}
        onchange={(event) => onmanualemailotpchange(event.currentTarget.checked)}
      />
      手动输入邮箱验证码（不自动调用邮箱令牌/IMAP）
    </label>
  {:else if activeGroup === 'export'}
    <div class="settings-row">
      <label for="full-action-format">转换格式</label>
      <select
        id="full-action-format"
        value={convertFormat}
        disabled={busy}
        title="选择复制转换、导出转换和导出 ZIP 使用的 Session 转换格式。"
        onchange={(event) => onconvertformatchange(event.currentTarget.value as SessionConvertFormat)}
      >
        {#each SESSION_CONVERT_FORMATS as format (format)}
          <option value={format}>{format}</option>
        {/each}
      </select>
    </div>
  {:else if activeGroup === 'k12'}
    <div class="settings-row">
      <label for="full-k12-workspace">Workspace ID</label>
      <input
        id="full-k12-workspace"
        class="workspace"
        value={k12WorkspaceId}
        disabled={busy}
        title="K12 邀请请求、接受和刷新流程使用的目标 Workspace ID。"
        oninput={(event) => onk12workspaceidchange(event.currentTarget.value)}
      />
      <label for="full-k12-concurrency">并发</label>
      <input
        id="full-k12-concurrency"
        type="number"
        min="1"
        max="30"
        value={k12Concurrency}
        disabled={busy}
        title="K12 多账号任务并发数，范围 1–30。"
        onchange={(event) => onk12concurrencychange(clampConcurrency(event.currentTarget.value))}
      />
    </div>
  {/if}

  {#if actionRenderMode(currentGroup) === 'table'}
    <div class="table-wrap" style={`--action-rows:${actionTableHeight(currentGroup)}`}>
      <table>
        <colgroup>
          <col class="action-column" />
          <col />
        </colgroup>
        <thead>
          <tr>
            <th>操作</th>
            <th>用途</th>
          </tr>
        </thead>
        <tbody>
          {#each currentGroup.actions as action (action.key)}
            {@const isDisabled = busy || disabledSet.has(action.key)}
            <tr class:selected={selectedAction === action.key} class:disabled={isDisabled}>
              <td>
                <button
                  class="row-pick"
                  aria-pressed={selectedAction === action.key}
                  disabled={isDisabled}
                  title={action.help}
                  onclick={() => selectAction(action.key)}
                  ondblclick={() => run(action)}>{action.label}</button
                >
              </td>
              <td title={action.help}>{action.help}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="execute-row">
      <button
        class="primary"
        disabled={busy || !selectedAction || disabledSet.has(selectedAction)}
        title="执行列表中当前选中的操作；也可以双击操作行。"
        onclick={() => run(currentGroup.actions.find((action) => action.key === selectedAction))}
        >执行选中操作</button
      >
    </div>
  {:else}
    <div class="button-grid">
      {#each currentGroup.actions as action (action.key)}
        <button
          class:primary={action.tone === 'primary'}
          class:danger={action.tone === 'danger'}
          disabled={busy || disabledSet.has(action.key)}
          title={action.help}
          onclick={() => run(action)}>{action.label}</button
        >
      {/each}
    </div>
  {/if}
</section>

<style>
  .card {
    min-height: 0;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }
  h2 {
    margin: 0;
    font-size: 13px;
  }
  p {
    margin: 4px 0 0;
    color: var(--muted);
  }
  .group-tabs {
    display: grid;
    grid-template-columns: repeat(6, minmax(92px, 1fr));
    border-bottom: 1px solid var(--border);
  }
  .group-tabs button {
    border: none;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 8px 10px;
  }
  .group-tabs button.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
    background: var(--head-bg);
    font-weight: 600;
  }
  .option,
  .settings-row {
    min-height: 32px;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
    padding: 6px 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .option input {
    vertical-align: middle;
  }
  .settings-row .workspace {
    width: 44ch;
    max-width: 100%;
    flex: 1 1 280px;
  }
  .settings-row input[type='number'] {
    width: 7ch;
  }
  .table-wrap {
    min-height: calc(var(--action-rows) * var(--row-h) + var(--row-h) + 2px);
    max-height: calc(6 * var(--row-h) + var(--row-h) + 2px);
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  col.action-column {
    width: 150px;
  }
  th {
    position: sticky;
    top: 0;
    z-index: 1;
    height: var(--row-h);
    padding: 0 8px;
    text-align: left;
    background: var(--head-bg);
    color: var(--head-fg);
    border-bottom: 1px solid var(--border);
  }
  td {
    height: var(--row-h);
    padding: 0 8px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  td:first-child {
    padding: 0;
  }
  tr.selected {
    background: var(--sel-bg);
    color: var(--sel-fg);
  }
  tr.disabled {
    opacity: 0.55;
  }
  .row-pick {
    width: 100%;
    height: var(--row-h);
    padding: 0 8px;
    border: none;
    border-radius: 0;
    text-align: left;
    background: transparent;
    color: inherit;
  }
  .row-pick:hover:not(:disabled) {
    background: var(--toolbar);
  }
  .execute-row {
    display: flex;
    justify-content: flex-end;
  }
  .button-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 6px;
  }
  @media (max-width: 900px) {
    .group-tabs {
      grid-template-columns: repeat(3, minmax(92px, 1fr));
    }
    .button-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
