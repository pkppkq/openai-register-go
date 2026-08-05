<script module lang="ts">
  export type ProviderProxyRole = 'create' | 'followup' | 'approve'

  export type ProviderProxyConfig = {
    enabled: boolean
    username: string
    password: string
    endpoint: string
    duration: number
    regions: string
  }

  export const PROVIDER_PROXY_ROLE_LABELS: Record<ProviderProxyRole, string> = {
    create: '第一步',
    followup: '后续',
    approve: 'Approve',
  }

  export const EMPTY_PROVIDER_PROXY_CONFIG: ProviderProxyConfig = {
    enabled: false,
    username: '',
    password: '',
    endpoint: '',
    duration: 5,
    regions: '',
  }

  export function clampProviderDuration(value: number): number {
    if (!Number.isFinite(value)) return 5
    return Math.min(120, Math.max(1, Math.trunc(value)))
  }

  export function normalizeProviderProxyConfig(value: ProviderProxyConfig): ProviderProxyConfig {
    return {
      enabled: value.enabled,
      username: value.username.trim(),
      password: value.password,
      endpoint: value.endpoint.trim(),
      duration: clampProviderDuration(value.duration),
      regions: value.regions.trim(),
    }
  }
</script>

<script lang="ts">
  let {
    open,
    role,
    value,
    busy = false,
    error = '',
    onclose,
    onsave,
  }: {
    open: boolean
    role: ProviderProxyRole
    value: ProviderProxyConfig
    busy?: boolean
    error?: string
    onclose: () => void
    onsave: (role: ProviderProxyRole, value: ProviderProxyConfig) => void
  } = $props()

  let enabled = $state(false)
  let username = $state('')
  let password = $state('')
  let endpoint = $state('')
  let duration = $state(5)
  let regions = $state('')

  $effect(() => {
    if (!open) return
    enabled = value.enabled
    username = value.username
    password = value.password
    endpoint = value.endpoint
    duration = clampProviderDuration(value.duration)
    regions = value.regions
  })

  function submit() {
    if (busy) return
    onsave(
      role,
      normalizeProviderProxyConfig({
        enabled,
        username,
        password,
        endpoint,
        duration,
        regions,
      }),
    )
  }

  function handleKeydown(event: KeyboardEvent) {
    if (!open || event.key !== 'Escape' || busy) return
    onclose()
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <div class="backdrop">
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="provider-dialog-title"
    >
      <h2 id="provider-dialog-title">提供商代理配置 - {PROVIDER_PROXY_ROLE_LABELS[role]}</h2>

      <label class="check" title="启用后，应用主页面配置时会为这个阶段启动后台预检池。">
        <input type="checkbox" bind:checked={enabled} />
        启用此阶段
      </label>

      <div class="fields">
        <label for="provider-username">用户名</label>
        <input
          id="provider-username"
          bind:value={username}
          autocomplete="off"
          title="代理提供商账号用户名；保存本身不会启动预热。"
        />

        <label for="provider-password">密码</label>
        <input
          id="provider-password"
          type="password"
          bind:value={password}
          autocomplete="off"
          title="代理提供商账号密码；仅保存在本机状态文件。"
        />

        <label for="provider-endpoint">主机:端口</label>
        <input
          id="provider-endpoint"
          bind:value={endpoint}
          placeholder="example.com:12345"
          title="代理提供商入口，格式为主机:端口。"
        />

        <label for="provider-regions">国家代码</label>
        <input
          id="provider-regions"
          bind:value={regions}
          placeholder="JP,US,DE"
          title="可用英文逗号分隔多个大写国家代码，例如 JP,US,DE。"
        />

        <label for="provider-duration">会话时长 t</label>
        <input
          id="provider-duration"
          type="number"
          min="1"
          max="120"
          bind:value={duration}
          title="提供商用户名中的会话时长参数，保存时限制在 1–120。"
        />
      </div>

      <p class="hint">国家代码可用英文逗号分隔，例如 JP,US,DE</p>
      <p class="hint">保存只更新配置；返回代理页点击“应用配置并预热”后才会启动后台预检。</p>

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}

      <footer>
        <button
          type="button"
          disabled={busy}
          title="关闭窗口并放弃本次编辑。"
          onclick={onclose}>取消</button
        >
        <button
          class="primary"
          type="button"
          disabled={busy}
          onclick={submit}
          title="保存当前阶段配置；点击主页面的应用按钮后开始预热。">保存</button
        >
      </footer>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgb(17 24 39 / 45%);
  }
  .dialog {
    width: 560px;
    max-width: calc(100% - 32px);
    min-height: 300px;
    max-height: calc(100% - 32px);
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 13px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .fields {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    align-items: center;
    gap: 8px;
  }
  .fields input[type='number'] {
    width: 10ch;
  }
  .hint {
    color: var(--muted);
  }
  .error {
    padding: 8px 10px;
    color: var(--err);
    background: #fee4e2;
    border: 1px solid #fda29b;
    border-radius: 4px;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: auto;
  }
</style>
