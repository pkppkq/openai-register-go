<script module lang="ts">
  export type AutoClassifyMode = 'trial' | 'link' | 'plan'
  export type AutoClassifyScope = 'all' | 'current' | 'selected'

  export const AUTO_CLASSIFY_MODES: readonly {
    key: AutoClassifyMode
    label: string
    hint: string
  }[] = [
    {
      key: 'trial',
      label: '按试用资格',
      hint: '按已检测到的 Plus 试用资格分到“有Plus试用 / 无Plus试用 / 试用未检测”等组',
    },
    {
      key: 'link',
      label: '按提链结果',
      hint: '按长链结果分到“提链成功 / 提链失败 / 未提链”等组',
    },
    {
      key: 'plan',
      label: '按 Plus/Free',
      hint: '按账号类型分到“plus / free / team / k12 / 类型未知”等组',
    },
  ]

  export const AUTO_CLASSIFY_SCOPES: readonly {
    key: AutoClassifyScope
    label: string
    hint: string
  }[] = [
    { key: 'all', label: '全部账号', hint: '处理列表里的全部账号' },
    { key: 'current', label: '当前分组', hint: '只处理当前分组筛选下看得到的账号' },
    { key: 'selected', label: '仅选中账号', hint: '只处理当前选中的账号' },
  ]

  export function autoClassifyModeHint(mode: AutoClassifyMode): string {
    return AUTO_CLASSIFY_MODES.find((item) => item.key === mode)?.hint ?? ''
  }

  export function autoClassifyScopeHint(scope: AutoClassifyScope): string {
    return AUTO_CLASSIFY_SCOPES.find((item) => item.key === scope)?.hint ?? ''
  }
</script>

<script lang="ts">
  let {
    open,
    accountCount,
    selectedCount = 0,
    currentCount = 0,
    busy = false,
    error = '',
    onclose,
    onsubmit,
  }: {
    open: boolean
    accountCount: number
    selectedCount?: number
    currentCount?: number
    busy?: boolean
    error?: string
    onclose: () => void
    onsubmit: (mode: AutoClassifyMode, scope: AutoClassifyScope) => void
  } = $props()

  let mode = $state<AutoClassifyMode>('trial')
  let scope = $state<AutoClassifyScope>('all')

  $effect(() => {
    if (!open) return
    mode = 'trial'
    scope = 'all'
  })

  let scopeCount = $derived(scope === 'all' ? accountCount : scope === 'current' ? currentCount : selectedCount)

  function handleKeydown(event: KeyboardEvent) {
    if (open && event.key === 'Escape' && !busy) onclose()
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <div class="backdrop">
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="auto-classify-title"
    >
      <h2 id="auto-classify-title">自动分类账号</h2>

      <div class="field">
        <label for="auto-classify-mode">分类方式</label>
        <select
          id="auto-classify-mode"
          bind:value={mode}
          title="选择根据试用资格、长链结果或账号类型移动账户分组。"
        >
          {#each AUTO_CLASSIFY_MODES as option (option.key)}
            <option value={option.key}>{option.label}</option>
          {/each}
        </select>
        <span>{autoClassifyModeHint(mode)}</span>
      </div>

      <div class="field">
        <label for="auto-classify-scope">作用范围</label>
        <select
          id="auto-classify-scope"
          bind:value={scope}
          title="选择处理全部账户、当前分组可见账户或当前选中账户。"
        >
          {#each AUTO_CLASSIFY_SCOPES as option (option.key)}
            <option value={option.key}>{option.label}</option>
          {/each}
        </select>
        <span>{autoClassifyScopeHint(scope)}（{scopeCount} 个）</span>
      </div>

      <p class="note">说明：只移动账号分组，不删除账号，也不修改 token。</p>

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}

      <footer>
        <button disabled={busy} title="关闭窗口，不修改任何账户分组。" onclick={onclose}>取消</button>
        <button
          class="primary"
          disabled={busy || scopeCount <= 0}
          title="按所选方式和范围开始分类；只移动分组，不删除账户或修改 Token。"
          onclick={() => onsubmit(mode, scope)}>开始分类</button
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
  .field {
    display: grid;
    grid-template-columns: 90px 160px minmax(0, 1fr);
    align-items: center;
    gap: 8px;
  }
  .field span,
  .note {
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
  }
  @media (max-width: 560px) {
    .field {
      grid-template-columns: 90px 1fr;
    }
    .field span {
      grid-column: 1 / -1;
    }
  }
</style>
