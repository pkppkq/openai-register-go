<script module lang="ts">
  export type AccountDetailTab = 'result' | 'session' | 'mail' | 'logs'

  export type AccountDetailAction =
    | 'copy_link'
    | 'open_saved_proxy'
    | 'open_new_proxy'
    | 'open_selected_links'
    | 'copy_proxy'
    | 'clear_workflow'
    | 'copy_access_token'
    | 'copy_session_json'
    | 'open_mailbox'
    | 'check_deactivation'
    | 'manual_login_code'

  export type WorkflowStepKey = 'email' | 'proxy' | 'auth' | 'session' | 'trial' | 'link' | 'export'
  export type WorkflowState = '未开始' | '进行中' | '成功' | '失败' | '需要人工' | '跳过'

  export type WorkflowEntry = {
    state: WorkflowState
    detail: string
    updatedAt: string
  }

  export type AccountDetailAccount = {
    email: string
    account_type?: string
    status?: string
    group?: string
    client_id?: string
    refresh_token?: string
    openai_rt?: string
    receive_mailbox?: string
    mail_provider?: string
    browser_fingerprint?: Record<string, unknown> | null
  }

  export type SessionPayload = Record<string, unknown>

  export type DetailLogLevel = 'error' | 'success' | 'attention' | 'normal'
  export type DetailLogLine = {
    message: string
    level?: DetailLogLevel
  }

  export type SessionSection = {
    title: string
    lines: readonly string[]
  }

  export const WORKFLOW_STEPS: readonly { key: WorkflowStepKey; label: string }[] = [
    { key: 'email', label: '邮箱' },
    { key: 'proxy', label: '代理' },
    { key: 'auth', label: '注册/登录' },
    { key: 'session', label: 'Session' },
    { key: 'trial', label: '试用资格' },
    { key: 'link', label: '支付长链' },
    { key: 'export', label: '导出' },
  ]

  export const WORKFLOW_STATES: readonly WorkflowState[] = ['未开始', '进行中', '成功', '失败', '需要人工', '跳过']

  const WORKFLOW_STATE_SET = new Set<string>(WORKFLOW_STATES)

  function text(value: unknown): string {
    if (value === null || value === undefined) return ''
    if (typeof value === 'string') return value
    if (typeof value === 'number' || typeof value === 'boolean') return String(value)
    try {
      return JSON.stringify(value)
    } catch {
      return String(value)
    }
  }

  function record(value: unknown): Record<string, unknown> {
    return value !== null && typeof value === 'object' && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {}
  }

  function clipped(value: unknown, limit = 1000): string {
    return text(value).slice(0, limit)
  }

  function entryOf(value: unknown): WorkflowEntry {
    const item = record(value)
    const rawState = text(item.state)
    return {
      state: (WORKFLOW_STATE_SET.has(rawState) ? rawState : '未开始') as WorkflowState,
      detail: clipped(item.detail, 500),
      updatedAt: text(item.updated_at),
    }
  }

  /**
   * 读取结构化 workflow，并仅用可靠字段补全 Session、试用和长链状态。
   * 未知试用状态保持“进行中”，避免 Python 版把未知值误画成绿色成功。
   */
  export function deriveWorkflow(
    payload: SessionPayload,
    hasLink: boolean,
    hasAccount = true,
  ): Record<WorkflowStepKey, WorkflowEntry> {
    const out = Object.fromEntries(
      WORKFLOW_STEPS.map(({ key }) => [key, { state: '未开始', detail: '', updatedAt: '' }]),
    ) as Record<WorkflowStepKey, WorkflowEntry>
    const saved = record(payload.workflow)
    for (const { key } of WORKFLOW_STEPS) {
      if (key in saved) out[key] = entryOf(saved[key])
    }

    if (hasAccount && out.email.state === '未开始') {
      out.email = { state: '未开始', detail: '已导入账号，尚未检查邮箱', updatedAt: '' }
    }

    if (text(payload.access_token).trim() !== '' && out.session.state !== '失败') {
      const summary = record(payload.access_summary)
      const plan =
        text(summary.plan_type || payload.plan_type || payload.chatgpt_plan_type).trim() || 'unknown'
      const expiresAt = text(summary.expires_at).trim()
      out.session = {
        state: '成功',
        detail: `plan=${plan}${expiresAt ? `，到期=${expiresAt}` : ''}`,
        updatedAt: out.session.updatedAt,
      }
    }

    if (hasLink && out.link.state !== '失败') {
      out.link = { state: '成功', detail: '长链已保存', updatedAt: out.link.updatedAt }
    }

    const trialStatus = text(payload.plus_trial_status).trim()
    if (trialStatus) {
      const eligible = text(payload.plus_trial_eligible).trim()
      const state: WorkflowState =
        trialStatus === 'eligible'
          ? '成功'
          : trialStatus === 'not_eligible' || trialStatus === 'error'
            ? '失败'
            : '进行中'
      out.trial = {
        state,
        detail: `${trialStatus} eligible=${eligible}`,
        updatedAt: out.trial.updatedAt,
      }
    }
    return out
  }

  function push(
    sections: SessionSection[],
    title: string,
    lines: readonly string[],
    visible = lines.some((line) => line.trim() !== ''),
  ) {
    if (visible) sections.push({ title, lines })
  }

  /** 按 Python `_show_session_result` 的固定顺序构造只读 Session 文本。 */
  export function sessionSections(
    payload: SessionPayload,
    fingerprint: Record<string, unknown> | null = null,
  ): SessionSection[] {
    const sections: SessionSection[] = []
    const summary = record(payload.access_summary)
    if (Object.keys(summary).length > 0) {
      const lines = [
        `Plan: ${text(summary.plan_type) || '-'}`,
        `Session Plan: ${text(summary.session_plan_type) || '-'}`,
        `Backend Plan: ${text(summary.backend_plan_type) || '-'}`,
        `Account Tail: ${text(summary.account_id_tail) || '-'}`,
        `Token Expires: ${text(summary.expires_at) || '-'}`,
      ]
      if (text(summary.backend_plan_error)) {
        lines.push(`Backend Check Error: ${clipped(summary.backend_plan_error, 300)}`)
      }
      push(sections, 'Session Summary', lines, true)
    }

    if (fingerprint && Object.keys(fingerprint).length > 0) {
      const userAgent = text(fingerprint.user_agent || fingerprint.userAgent)
      push(
        sections,
        'Saved Browser Fingerprint',
        [JSON.stringify(fingerprint, null, 2), userAgent ? `UA: ${userAgent}` : ''],
        true,
      )
    }

    push(sections, 'Access Token', [text(payload.access_token)])
    push(sections, 'Checkout URL', [text(payload.checkout_url)])
    push(sections, 'Payment Link Type', [text(payload.payment_link_type)])

    const amountLines = [
      `Stripe Amount: ${text(payload.stripe_amount)}`,
      `Target Amount: ${text(payload.target_amount)}`,
      `Source: ${text(payload.stripe_amount_source)}`,
      `Status: ${text(payload.amount_check)}`,
    ]
    push(
      sections,
      'Amount Check',
      amountLines,
      ['stripe_amount', 'target_amount', 'stripe_amount_source', 'amount_check'].some(
        (key) => text(payload[key]).trim() !== '',
      ),
    )

    push(
      sections,
      'K12',
      [
        `Workspace ID: ${text(payload.k12_workspace_id)}`,
        `HTTP Status: ${text(payload.k12_status)}`,
        `Response: ${clipped(payload.k12_response)}`,
        `Invite URL: ${clipped(payload.k12_invite_url)}`,
        `Accept Result: ${clipped(payload.k12_accept_result)}`,
        `Switch Result: ${clipped(payload.k12_switch_result)}`,
      ],
      ['k12_workspace_id', 'k12_status', 'k12_response', 'k12_invite_url', 'k12_accept_result', 'k12_switch_result'].some(
        (key) => text(payload[key]).trim() !== '',
      ),
    )

    push(
      sections,
      'Team Invite',
      [
        `Target Email: ${text(payload.team_invite_target_email) || '-'}`,
        `Send Status: ${text(payload.team_invite_status) || '-'}`,
        `Sent At: ${text(payload.team_invite_sent_at) || '-'}`,
        `Send Response: ${clipped(payload.team_invite_response)}`,
        `Workspace ID: ${text(payload.team_workspace_id) || '-'}`,
        `Invite URL: ${clipped(payload.team_invite_url)}`,
        `Accept Result: ${clipped(payload.team_accept_result)}`,
      ],
      [
        'team_invite_target_email',
        'team_invite_status',
        'team_invite_response',
        'team_invite_url',
        'team_workspace_id',
        'team_accept_result',
      ].some((key) => text(payload[key]).trim() !== ''),
    )

    push(
      sections,
      'Plus Trial',
      [
        `Eligible: ${text(payload.plus_trial_eligible)}`,
        `Status: ${text(payload.plus_trial_status)}`,
        `Amount: ${text(payload.plus_trial_amount)} ${text(payload.plus_trial_currency)}`.trim(),
        `Country: ${text(payload.plus_trial_country)}`,
        `Source: ${text(payload.plus_trial_amount_source)}`,
        `Checked At: ${text(payload.plus_trial_checked_at)}`,
        `Detail: ${clipped(payload.plus_trial_detail)}`,
      ],
      text(payload.plus_trial_status).trim() !== '' || text(payload.plus_trial_eligible).trim() !== '',
    )

    push(
      sections,
      'OpenAI Deactivation Mail',
      [
        `Found: ${text(payload.openai_deactivation_found) || '-'}`,
        `Status: ${text(payload.openai_deactivation_status) || '-'}`,
        `Count: ${text(payload.openai_deactivation_count) || '0'}`,
        `Checked At: ${text(payload.openai_deactivation_checked_at) || '-'}`,
        `Folder: ${text(payload.openai_deactivation_folder) || '-'}`,
        `Date: ${text(payload.openai_deactivation_date) || '-'}`,
        `From: ${text(payload.openai_deactivation_from) || '-'}`,
        `To: ${text(payload.openai_deactivation_to) || '-'}`,
        `Subject: ${text(payload.openai_deactivation_subject) || '-'}`,
        `Alias Mismatch Count: ${text(payload.openai_deactivation_alias_mismatch_count) || '0'}`,
        `Snippet: ${clipped(payload.openai_deactivation_snippet)}`,
      ],
      text(payload.openai_deactivation_checked_at).trim() !== '' ||
        text(payload.openai_deactivation_found).trim() !== '',
    )

    for (const proxy of [
      { title: 'Long Link Proxy', key: 'link_proxy', label: 'link_proxy_label' },
      { title: 'Create Step Proxy', key: 'link_create_proxy', label: 'link_create_proxy_label' },
      { title: 'Followup Proxy', key: 'link_followup_proxy', label: 'link_followup_proxy_label' },
      { title: 'Approve Proxy', key: 'link_approve_proxy', label: 'link_approve_proxy_label' },
    ] as const) {
      const value = text(payload[proxy.key]) || text(payload[proxy.label])
      push(sections, proxy.title, [value])
    }

    const createExit = text(payload.link_create_proxy_exit || payload.link_proxy_exit)
    const followupExit = text(payload.link_followup_proxy_exit) || createExit
    const approveExit = text(payload.link_approve_proxy_exit) || followupExit
    if (createExit || followupExit || approveExit) {
      push(
        sections,
        'Long Link Proxy Exits',
        [
          `第一步=${createExit || '未记录'}`,
          `后续=${followupExit || createExit || '未记录'}`,
          `Approve=${approveExit || followupExit || createExit || '未记录'}`,
        ],
        true,
      )
    }

    push(sections, 'Session JSON', [text(payload.session_json)])
    return sections
  }

  export function sessionText(
    payload: SessionPayload,
    fingerprint: Record<string, unknown> | null = null,
  ): string {
    return sessionSections(payload, fingerprint)
      .map((section) => `${section.title}:\n${section.lines.filter((line) => line !== '').join('\n')}`)
      .join('\n\n')
  }

  export function mailInspectorText(account: AccountDetailAccount | null, selectedCount: number): string {
    if (!account || selectedCount <= 0) return '请选择一个或多个账户。'
    if (selectedCount > 1) return `已选择 ${selectedCount} 个账户；邮箱配置汇总由账户列表提供。`
    const provider =
      text(account.mail_provider).trim() ||
      (text(account.client_id).trim() && text(account.refresh_token).trim() ? 'Outlook OAuth' : '未配置')
    return [
      `账户：${account.email}`,
      `接收邮箱：${text(account.receive_mailbox).trim() || account.email || '-'}`,
      `邮箱方式：${provider}`,
      `分组：${text(account.group).trim() || '未分组'}`,
      `状态：${text(account.status).trim() || '待处理'}`,
      `邮箱 OAuth：${text(account.client_id).trim() && text(account.refresh_token).trim() ? '已配置' : '未配置'}`,
      `OpenAI RT：${text(account.openai_rt).trim() ? '已保存' : '未保存'}`,
    ].join('\n')
  }
</script>

<script lang="ts">
  let {
    activeTab = 'result',
    account,
    selectedCount = account ? 1 : 0,
    payload,
    link,
    linkProxySummary,
    accountLogs = [],
    globalLogs = [],
    busy = false,
    disabledActions = [],
    ontabchange,
    onaction,
  }: {
    activeTab?: AccountDetailTab
    account: AccountDetailAccount | null
    selectedCount?: number
    payload: SessionPayload
    link: string
    linkProxySummary: string
    accountLogs?: readonly DetailLogLine[]
    globalLogs?: readonly DetailLogLine[]
    busy?: boolean
    disabledActions?: readonly AccountDetailAction[]
    ontabchange: (tab: AccountDetailTab) => void
    onaction: (action: AccountDetailAction) => void
  } = $props()

  const TABS: readonly { key: AccountDetailTab; label: string; help: string }[] = [
    { key: 'result', label: '结果概览', help: '查看支付链接、使用代理和七步流程状态。' },
    { key: 'session', label: 'Session', help: '查看并复制 Access Token 与完整 Session JSON。' },
    { key: 'mail', label: '邮箱', help: '查看所选账户的收件配置并打开只读邮箱管理。' },
    { key: 'logs', label: '日志', help: '查看当前账户日志和全局日志。' },
  ]

  let disabledSet = $derived(new Set(disabledActions))
  let workflow = $derived(deriveWorkflow(payload, link.trim() !== '', account !== null))
  let renderedSession = $derived(sessionText(payload, account?.browser_fingerprint ?? null))
  let renderedMail = $derived(mailInspectorText(account, selectedCount))
  let accessToken = $derived(text(payload.access_token))
  let sessionJSON = $derived(text(payload.session_json))

  function act(action: AccountDetailAction) {
    if (busy || disabledSet.has(action)) return
    onaction(action)
  }

  function levelClass(level: DetailLogLevel | undefined): string {
    return level ?? 'normal'
  }
</script>

<section class="card detail-full" aria-label="账户详情">
  <header>
    <div>
      <h2>账户详情</h2>
      <p>{account ? `${account.email} · ${account.account_type || 'free'} · ${account.status || '待处理'}` : '未选择账户'}</p>
    </div>
    <span class="selection">已选 {selectedCount}</span>
  </header>

  <div class="tabs" role="tablist" aria-label="账户详情页签">
    {#each TABS as tab (tab.key)}
      <button
        role="tab"
        aria-selected={activeTab === tab.key}
        class:active={activeTab === tab.key}
        title={tab.help}
        onclick={() => ontabchange(tab.key)}>{tab.label}</button
      >
    {/each}
  </div>

  {#if activeTab === 'result'}
    <div class="pane result-pane">
      <fieldset>
        <legend>支付链接</legend>
        <div class="toolbar">
          <span class="muted">当前选择：{account?.email ?? '未选择账户'} · 共 {selectedCount} 个</span>
          <button
            disabled={!link.trim() || busy || disabledSet.has('copy_link')}
            title="复制当前账号已保存的长链接。"
            onclick={() => act('copy_link')}>复制长链接</button
          >
          <button
            disabled={!link.trim() || busy || disabledSet.has('open_saved_proxy')}
            title="使用提链时保存的后续代理打开当前长链接。"
            onclick={() => act('open_saved_proxy')}>提链代理打开</button
          >
          <button
            disabled={!link.trim() || busy || disabledSet.has('open_new_proxy')}
            title="取一个新的支付代理和支付资料打开当前长链接。"
            onclick={() => act('open_new_proxy')}>新代理打开</button
          >
          <button
            disabled={selectedCount <= 0 || busy || disabledSet.has('open_selected_links')}
            title="为每个已选且已有长链接的账户打开独立支付窗口。"
            onclick={() => act('open_selected_links')}>批量打开选中</button
          >
        </div>
        <input class="mono full" readonly value={link} title="当前账号保存的支付长链接。" />
        <div class="proxy-row">
          <label for="detail-link-proxy">长链使用代理</label>
          <textarea
            id="detail-link-proxy"
            class="mono"
            rows="3"
            readonly
            value={linkProxySummary}
            title="第一步、后续和 Approve 三阶段代理或出口摘要。"
          ></textarea>
          <button
            disabled={!linkProxySummary.trim() || busy || disabledSet.has('copy_proxy')}
            title="复制当前账号长链使用的三阶段代理摘要。"
            onclick={() => act('copy_proxy')}>复制代理</button
          >
        </div>
      </fieldset>

      <fieldset class="workflow">
        <div class="legend-row">
          <legend>流程状态</legend>
          <button
            disabled={!account || busy || disabledSet.has('clear_workflow')}
            title="清空当前账号保存的七步流程状态，不删除 Session、Token 或支付链接。"
            onclick={() => act('clear_workflow')}>清空流程</button
          >
        </div>
        <div class="table-wrap">
          <table>
            <colgroup>
              <col class="step-col" />
              <col class="state-col" />
              <col />
            </colgroup>
            <thead>
              <tr>
                <th>步骤</th>
                <th>状态</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              {#each WORKFLOW_STEPS as step (step.key)}
                {@const item = workflow[step.key]}
                <tr>
                  <td>{step.label}</td>
                  <td class:success={item.state === '成功'} class:error={item.state === '失败'} class:running={item.state === '进行中'} class:manual={item.state === '需要人工'}>{item.state}</td>
                  <td title={item.updatedAt ? `${item.detail} · ${item.updatedAt}` : item.detail}>{item.detail}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </fieldset>
    </div>
  {:else if activeTab === 'session'}
    <div class="pane text-pane">
      <div class="toolbar">
        <button
          disabled={!accessToken.trim() || busy || disabledSet.has('copy_access_token')}
          title="复制当前邮箱的 Access Token；没有 Token 时不可用。"
          onclick={() => act('copy_access_token')}>复制 Access Token</button
        >
        <button
          disabled={!sessionJSON.trim() || busy || disabledSet.has('copy_session_json')}
          title="复制当前邮箱保存的 Session JSON；没有 Session JSON 时不可用。"
          onclick={() => act('copy_session_json')}>复制 Session JSON</button
        >
      </div>
      <pre class="mono">{renderedSession || '（暂无 Session 数据）'}</pre>
    </div>
  {:else if activeTab === 'mail'}
    <div class="pane text-pane">
      <div class="toolbar">
        <button
          disabled={!account || busy || disabledSet.has('open_mailbox')}
          title="打开当前邮箱的只读管理窗口，可查看文件夹、最近邮件和正文。"
          onclick={() => act('open_mailbox')}>邮箱管理</button
        >
        <button
          disabled={!account || busy || disabledSet.has('check_deactivation')}
          title="扫描当前邮箱是否收到 OpenAI Access Deactivated / 停用通知。"
          onclick={() => act('check_deactivation')}>查封禁邮件</button
        >
        <button
          disabled={!account || busy || disabledSet.has('manual_login_code')}
          title="打开 ChatGPT 登录页并监听当前邮箱验证码，收到后弹窗显示。"
          onclick={() => act('manual_login_code')}>手动登录取码</button
        >
      </div>
      <pre class="mono">{renderedMail}</pre>
    </div>
  {:else}
    <div class="pane log-grid">
      <section>
        <h3>选中账户日志：{account?.email ?? '未选择账户'}</h3>
        <div class="log mono">
          {#if accountLogs.length}
            {#each accountLogs as line, index (`account-${index}`)}
              <div class={levelClass(line.level)}>{line.message}</div>
            {/each}
          {:else}
            <span class="muted">（暂无）</span>
          {/if}
        </div>
      </section>
      <section>
        <h3>全局日志</h3>
        <div class="log mono">
          {#if globalLogs.length}
            {#each globalLogs as line, index (`global-${index}`)}
              <div class={levelClass(line.level)}>{line.message}</div>
            {/each}
          {:else}
            <span class="muted">（暂无）</span>
          {/if}
        </div>
      </section>
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
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
  }
  h2,
  h3,
  p {
    margin: 0;
  }
  h2 {
    font-size: 13px;
  }
  h3 {
    color: var(--muted);
    font-size: 12px;
  }
  header p,
  .selection {
    color: var(--muted);
    margin-top: 3px;
  }
  .tabs {
    display: grid;
    grid-template-columns: repeat(4, minmax(96px, 1fr));
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    border: none;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 8px;
  }
  .tabs button.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
    background: var(--head-bg);
    font-weight: 600;
  }
  .pane {
    min-height: 0;
    flex: 1;
  }
  .result-pane,
  .text-pane {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  fieldset {
    min-width: 0;
    margin: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 10px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
  }
  .toolbar,
  .proxy-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  .toolbar .muted {
    margin-right: auto;
  }
  .full {
    width: 100%;
    margin-top: 8px;
  }
  .proxy-row {
    margin-top: 8px;
  }
  .proxy-row textarea {
    min-width: 220px;
    flex: 1;
    resize: vertical;
  }
  .workflow {
    display: flex;
    flex-direction: column;
    min-height: 245px;
  }
  .legend-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: -5px 0 6px;
  }
  .legend-row legend {
    float: none;
  }
  .table-wrap {
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  .step-col {
    width: 112px;
  }
  .state-col {
    width: 92px;
  }
  th {
    background: var(--head-bg);
    color: var(--head-fg);
    text-align: left;
    font-weight: 600;
  }
  th,
  td {
    height: var(--row-h);
    padding: 0 8px;
    border-bottom: 1px solid var(--border);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .success,
  .log .success {
    color: var(--ok);
  }
  .error,
  .log .error {
    color: var(--err);
  }
  .running,
  .log .attention {
    color: var(--attention);
  }
  .manual {
    color: var(--manual);
  }
  pre,
  .log {
    flex: 1;
    min-height: 180px;
    margin: 0;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 10px;
    line-height: 1.6;
  }
  .log-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .log-grid section {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  @media (max-width: 760px) {
    .log-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
