<script module lang="ts">
  import type { ProviderProxyConfig, ProviderProxyRole } from '../ProviderProxyDialog.svelte'
  import type { ProxyPoolKey, ProxyPools, ProxyRouteMode } from './ProxySettings.svelte'

  export type { ProviderProxyConfig, ProviderProxyRole, ProxyPoolKey, ProxyPools, ProxyRouteMode }

  export type ProviderProxyStageStatus = {
    ready: number
    target: number
    checking: number
    message?: string
  }

  export type LinkReuseProxies = {
    create: string
    followup: string
    approve: string
  }

  export type ProxyFullChange =
    | { field: 'region'; value: string }
    | { field: 'raceConcurrency'; value: number }
    | { field: 'precheckLimit'; value: number }
    | { field: 'precheckConcurrency'; value: number }
    | { field: 'reuse'; role: ProviderProxyRole; value: string }
    | { field: 'requireJapan'; value: boolean }
    | { field: 'registerWithPaymentProxy'; value: boolean }
    | { field: 'forceLegacyPaypal'; value: boolean }
    | { field: 'extensionDir'; value: string }

  export type ProxyFullAction =
    | { kind: 'edit_provider'; role: ProviderProxyRole }
    | { kind: 'apply_providers' }
    | { kind: 'precheck' }
    | { kind: 'cleanup' }
    | { kind: 'choose_extension_dir' }

  export const PROVIDER_PROXY_ROLES: readonly ProviderProxyRole[] = ['create', 'followup', 'approve']
  export const PROVIDER_PROXY_ROLE_LABELS: Record<ProviderProxyRole, string> = {
    create: '第一步',
    followup: '后续',
    approve: 'Approve',
  }

  export const LINK_PROXY_REGION_NAMES = {
    US: '美国',
    BR: '巴西',
    JP: '日本',
    NL: '荷兰',
    DE: '德国',
    FR: '法国',
    GB: '英国',
    CA: '加拿大',
    AU: '澳洲',
    ID: '印尼',
    SG: '新加坡',
    TH: '泰国',
    KR: '韩国',
    TW: '台湾',
    HK: '香港',
    MX: '墨西哥',
    ES: '西班牙',
    IT: '意大利',
    PL: '波兰',
    SE: '瑞典',
    NO: '挪威',
  } as const

  export const LINK_PROXY_REGION_OPTIONS: readonly string[] = [
    '自动(跟随支付地区)',
    '不限',
    ...Object.entries(LINK_PROXY_REGION_NAMES).map(([code, name]) => `${code} ${name}`),
  ]

  export function clampProxyInteger(value: number, min: number, max: number, fallback: number): number {
    if (!Number.isFinite(value)) return fallback
    return Math.min(max, Math.max(min, Math.trunc(value)))
  }

  export function providerStageStatusText(status: ProviderProxyStageStatus | undefined): string {
    if (!status) return '未预热'
    if (status.message?.trim()) return status.message.trim()
    const ready = Math.max(0, Math.trunc(status.ready || 0))
    const target = Math.max(0, Math.trunc(status.target || 0))
    const checking = Math.max(0, Math.trunc(status.checking || 0))
    return `可用 ${ready}/${target}${checking ? ` 检测中 ${checking}` : ''}`
  }
</script>

<script lang="ts">
  import ProxySettings from './ProxySettings.svelte'

  let {
    localProxy,
    routeMode,
    pools,
    counts,
    providers,
    providerStatuses = {},
    region,
    raceConcurrency,
    precheckLimit,
    precheckConcurrency,
    reuse,
    requireJapan,
    registerWithPaymentProxy,
    forceLegacyPaypal,
    extensionDir,
    busy = false,
    onlocalproxychange,
    onroutemodechange,
    onpoolchange,
    onchange,
    onaction,
  }: {
    localProxy: string
    routeMode: string
    pools: ProxyPools
    counts?: Partial<Record<ProxyPoolKey, number>>
    providers: Record<ProviderProxyRole, ProviderProxyConfig>
    providerStatuses?: Partial<Record<ProviderProxyRole, ProviderProxyStageStatus>>
    region: string
    raceConcurrency: number
    precheckLimit: number
    precheckConcurrency: number
    reuse: LinkReuseProxies
    requireJapan: boolean
    registerWithPaymentProxy: boolean
    forceLegacyPaypal: boolean
    extensionDir: string
    busy?: boolean
    onlocalproxychange: (value: string) => void
    onroutemodechange: (mode: ProxyRouteMode) => void
    onpoolchange: (key: ProxyPoolKey, text: string) => void
    onchange: (change: ProxyFullChange) => void
    onaction: (action: ProxyFullAction) => void
  } = $props()

  let selectedRole = $state<ProviderProxyRole>('create')

  function parseInteger(raw: string, min: number, max: number, fallback: number): number {
    return clampProxyInteger(Number.parseInt(raw, 10), min, max, fallback)
  }
</script>

<div class="full-stack">
  <ProxySettings
    {localProxy}
    {routeMode}
    {pools}
    {counts}
    {onlocalproxychange}
    {onroutemodechange}
    {onpoolchange}
  />

  <section class="card advanced" aria-label="高级代理配置">
    <h2>高级代理配置</h2>

    <fieldset>
      <legend>代理提供商配置（后台预检池）</legend>
      <div class="table-wrap">
        <table>
          <colgroup>
            <col class="role-col" />
            <col class="enabled-col" />
            <col />
            <col class="region-col" />
            <col class="status-col" />
          </colgroup>
          <thead>
            <tr>
              <th>阶段</th>
              <th>启用</th>
              <th>主机:端口</th>
              <th>地区</th>
              <th>状态</th>
            </tr>
          </thead>
          <tbody>
            {#each PROVIDER_PROXY_ROLES as role (role)}
              {@const config = providers[role]}
              <tr class:selected={selectedRole === role}>
                <td>
                  <button
                    class="row-pick"
                    aria-pressed={selectedRole === role}
                    title={`选择${PROVIDER_PROXY_ROLE_LABELS[role]}阶段；双击打开编辑。`}
                    onclick={() => (selectedRole = role)}
                    ondblclick={() => onaction({ kind: 'edit_provider', role })}
                    >{PROVIDER_PROXY_ROLE_LABELS[role]}</button
                  >
                </td>
                <td>{config.enabled ? '是' : '否'}</td>
                <td title={config.endpoint}>{config.endpoint || '-'}</td>
                <td title={config.regions}>{config.regions || '-'}</td>
                <td title={providerStageStatusText(providerStatuses[role])}
                  >{providerStageStatusText(providerStatuses[role])}</td
                >
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <div class="button-row">
        <button
          disabled={busy}
          title="编辑选中的第一步、后续或 Approve 提供商配置；保存后不会自动预热。"
          onclick={() => onaction({ kind: 'edit_provider', role: selectedRole })}>编辑选中阶段</button
        >
        <button
          class="primary"
          disabled={busy}
          title="验证三阶段配置，应用并在后台预热各启用池。此操作会消耗代理提供商额度。"
          onclick={() => onaction({ kind: 'apply_providers' })}>应用配置并预热</button
        >
        <span class="hint">各启用池达到 200 后开跑；降到 200 自动补至 500</span>
      </div>
    </fieldset>

    <div class="settings-grid">
      <label for="full-proxy-region">撞链代理地区</label>
      <select
        id="full-proxy-region"
        value={LINK_PROXY_REGION_OPTIONS.includes(region) ? region : '不限'}
        title="限制支付链接三阶段代理地区；自动会跟随当前支付模式国家。"
        onchange={(event) => onchange({ field: 'region', value: event.currentTarget.value })}
      >
        {#each LINK_PROXY_REGION_OPTIONS as option (option)}
          <option value={option}>{option}</option>
        {/each}
      </select>
      <span class="hint">自动会在支付模式变化时派生地区；不限不做地区过滤。</span>

      <label for="full-race-concurrency">单账号撞链并发数</label>
      <input
        id="full-race-concurrency"
        type="number"
        min="1"
        max="30"
        value={raceConcurrency}
        title="同一账号同时竞速的代理三元组数量，范围 1–30；首个成功会取消同账号其余尝试。"
        onchange={(event) =>
          onchange({
            field: 'raceConcurrency',
            value: parseInteger(event.currentTarget.value, 1, 30, 1),
          })}
      />
      <span class="hint">首个成功会取消同账号剩余尝试。</span>

      <label for="full-precheck-limit">预检上限/池</label>
      <input
        id="full-precheck-limit"
        type="number"
        min="1"
        max="10000"
        value={precheckLimit}
        title="每个支付代理池本轮最多检测的代理数，范围 1–10000。"
        onchange={(event) =>
          onchange({
            field: 'precheckLimit',
            value: parseInteger(event.currentTarget.value, 1, 10000, 500),
          })}
      />
      <span></span>

      <label for="full-precheck-concurrency">预检并发</label>
      <input
        id="full-precheck-concurrency"
        type="number"
        min="1"
        max="300"
        value={precheckConcurrency}
        title="支付代理池预检并发，范围 1–300；过大会增加本地端口和提供商压力。"
        onchange={(event) =>
          onchange({
            field: 'precheckConcurrency',
            value: parseInteger(event.currentTarget.value, 1, 300, 100),
          })}
      />
      <div class="button-row compact">
        <button
          disabled={busy}
          title="并发检测各支付代理池；本轮跳过失败项，但不会修改持久代理池。"
          onclick={() => onaction({ kind: 'precheck' })}>预检支付代理池</button
        >
        <button
          disabled={busy}
          title="低并发、每条连续检测两次；确认后才会从四个手工池移除传输失败代理，403 不删除。"
          onclick={() => onaction({ kind: 'cleanup' })}>清理无效代理</button
        >
      </div>
    </div>

    <fieldset>
      <legend>固定复用代理</legend>
      {#each PROVIDER_PROXY_ROLES as role (role)}
        <div class="reuse-row">
          <label for={`full-reuse-${role}`}>{PROVIDER_PROXY_ROLE_LABELS[role]}复用代理</label>
          <input
            id={`full-reuse-${role}`}
            class="mono"
            value={reuse[role]}
            title={role === 'approve'
              ? '配置后 Approve 优先使用，不取用或移除 Approve 代理池；保留其原地区。'
              : `配置后${PROVIDER_PROXY_ROLE_LABELS[role]}优先使用，不取用或移除对应代理池。`}
            oninput={(event) => onchange({ field: 'reuse', role, value: event.currentTarget.value })}
          />
          <span class="hint">
            {role === 'approve'
              ? '配置后 approve 优先使用，不取用/移除 Approve 代理池'
              : `配置后${PROVIDER_PROXY_ROLE_LABELS[role]}优先使用，不取用/移除对应代理池`}
          </span>
        </div>
      {/each}
    </fieldset>

    <div class="checks">
      <label title="勾选后，第一步代理必须是日本出口；提供商第一步国家代码也只能填写 JP。">
        <input
          type="checkbox"
          checked={requireJapan}
          onchange={(event) => onchange({ field: 'requireJapan', value: event.currentTarget.checked })}
        />
        提取长链强制日本出口（不勾选=只记录出口，不限制）
      </label>
      <label title="特殊情况下让注册流程使用支付链接动态代理；默认使用注册/获取 Session 动态代理池。">
        <input
          type="checkbox"
          checked={registerWithPaymentProxy}
          onchange={(event) =>
            onchange({ field: 'registerWithPaymentProxy', value: event.currentTarget.checked })}
        />
        注册时使用支付链接动态代理（特殊情况勾选；不勾选则用上方动态代理池）
      </label>
      <label title="忽略 checkout 支付方式列表，直接尝试 PayPal confirm；仅用于兼容旧流程。">
        <input
          type="checkbox"
          checked={forceLegacyPaypal}
          onchange={(event) => onchange({ field: 'forceLegacyPaypal', value: event.currentTarget.checked })}
        />
        旧版强撞 PayPal（忽略 checkout 支付方式列表，直接尝试 PayPal confirm）
      </label>
    </div>

    <div class="extension-row">
      <label for="full-payment-extension">支付链接扩展目录</label>
      <input
        id="full-payment-extension"
        class="mono"
        value={extensionDir}
        title="打开支付链接时加载的解压后 Chrome 扩展目录；与支付资料页使用同一个设置。"
        oninput={(event) => onchange({ field: 'extensionDir', value: event.currentTarget.value })}
      />
      <button
        disabled={busy}
        title="选择打开支付链接时加载的 Chrome 扩展目录；需为解压后的扩展文件夹。"
        onclick={() => onaction({ kind: 'choose_extension_dir' })}>选择目录</button
      >
      <span class="hint">需选择解压后的 Chrome 扩展目录</span>
    </div>
  </section>
</div>

<style>
  .full-stack {
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  h2 {
    margin: 0;
    font-size: 12px;
    color: var(--muted);
  }
  fieldset {
    min-width: 0;
    margin: 0;
    padding: 6px 10px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
  }
  .table-wrap {
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  .role-col {
    width: 90px;
  }
  .enabled-col {
    width: 64px;
  }
  .region-col {
    width: 130px;
  }
  .status-col {
    width: 190px;
  }
  th,
  td {
    height: var(--row-h);
    padding: 0 8px;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  th {
    background: var(--head-bg);
    color: var(--head-fg);
    border-bottom: 1px solid var(--border);
  }
  td:first-child {
    padding: 0;
  }
  tr.selected {
    color: var(--sel-fg);
    background: var(--sel-bg);
  }
  .row-pick {
    width: 100%;
    height: var(--row-h);
    border: none;
    border-radius: 0;
    padding: 0 8px;
    text-align: left;
    color: inherit;
    background: transparent;
  }
  .button-row,
  .extension-row,
  .reuse-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
    margin-top: 8px;
  }
  .button-row .hint {
    margin-left: auto;
  }
  .settings-grid {
    display: grid;
    grid-template-columns: max-content minmax(110px, 240px) minmax(180px, 1fr);
    align-items: center;
    gap: 8px;
  }
  .settings-grid input[type='number'] {
    width: 11ch;
  }
  .compact {
    margin: 0;
  }
  .reuse-row input,
  .extension-row input {
    min-width: 240px;
    flex: 1;
  }
  .reuse-row label {
    width: 112px;
  }
  .checks {
    display: flex;
    flex-direction: column;
    gap: 7px;
  }
  .checks label {
    display: flex;
    align-items: flex-start;
    gap: 6px;
  }
  .hint {
    color: var(--muted);
    overflow-wrap: anywhere;
  }
  @media (max-width: 800px) {
    .settings-grid {
      grid-template-columns: max-content minmax(0, 1fr);
    }
    .settings-grid > .hint,
    .settings-grid > .compact,
    .settings-grid > span {
      grid-column: 1 / -1;
    }
  }
</style>
