<script module lang="ts">
  export type ManagedAccountType = 'free' | 'plus' | 'team'

  export function accountSelectionPreview(emails: readonly string[], limit = 20): string {
    const safeLimit = Math.max(1, Math.trunc(limit || 20))
    const shown = emails.slice(0, safeLimit)
    const remaining = Math.max(0, emails.length - shown.length)
    return [...shown, ...(remaining ? [`……另有 ${remaining} 个账户`] : [])].join('\n')
  }
</script>

<script lang="ts">
  let {
    open,
    emails,
    currentType = '',
    currentGroup = '',
    groups,
    busy = false,
    error = '',
    onclose,
    onsettype,
    onmovetogroup,
    ondelete,
  }: {
    open: boolean
    emails: readonly string[]
    currentType?: string
    currentGroup?: string
    groups: readonly string[]
    busy?: boolean
    error?: string
    onclose: () => void
    onsettype: (accountType: ManagedAccountType) => void
    onmovetogroup: (group: string) => void
    ondelete: () => void
  } = $props()

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
      aria-labelledby="account-management-title"
    >
      <header>
        <div>
          <h2 id="account-management-title">账户管理</h2>
          <p>已选 {emails.length} 个账户</p>
        </div>
        <button disabled={busy} title="关闭账户管理窗口。" onclick={onclose}>关闭</button>
      </header>

      <label for="managed-account-preview">当前选择</label>
      <textarea
        id="managed-account-preview"
        class="mono"
        rows="8"
        readonly
        value={accountSelectionPreview(emails)}
        title="最多预览前 20 个选中邮箱；实际操作仍作用于全部选中账户。"
      ></textarea>

      <fieldset>
        <legend>设置类型</legend>
        <div class="button-grid">
          {#each ['free', 'plus', 'team'] as accountType (accountType)}
            <button
              class:active={currentType.toLowerCase() === accountType}
              disabled={busy || emails.length === 0}
              title={accountType === 'free'
                ? '把全部选中账户设为 Free；为保持 Python 行为一致，会清空状态和 OpenAI RT，上层会再次确认。'
                : `把全部选中账户类型设置为 ${accountType}；不会清空 OpenAI RT。`}
              onclick={() => onsettype(accountType as ManagedAccountType)}
              >{accountType === 'free' ? 'Free' : accountType === 'plus' ? 'Plus' : 'Team'}</button
            >
          {/each}
        </div>
      </fieldset>

      <fieldset>
        <legend>移动到分组</legend>
        <div class="group-list">
          {#each groups as group (group)}
            <button
              class:active={currentGroup === group}
              disabled={busy || emails.length === 0}
              title={`把全部选中账户移动到“${group}”；不会删除账户或修改 Token。`}
              onclick={() => onmovetogroup(group)}>{group}</button
            >
          {/each}
        </div>
        {#if groups.length === 0}
          <p class="muted">（没有可用分组）</p>
        {/if}
      </fieldset>

      <fieldset class="danger-zone">
        <legend>删除</legend>
        <p>删除会同时移除所选账户、本地支付链接结果和撞链次数；应由上层再次确认。</p>
        <button
          class="danger"
          disabled={busy || emails.length === 0}
          title="请求删除全部选中账户及其本地结果；上层必须显示确认预览后再执行。"
          onclick={ondelete}>删除选中</button
        >
      </fieldset>

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}
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
    width: 620px;
    max-width: calc(100% - 32px);
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
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 13px;
  }
  header p,
  .muted {
    color: var(--muted);
  }
  textarea {
    width: 100%;
    resize: vertical;
  }
  fieldset {
    margin: 0;
    padding: 8px 10px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
  }
  .button-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 6px;
  }
  .group-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    max-height: 140px;
    overflow: auto;
  }
  button.active {
    color: var(--primary);
    border-color: var(--primary);
    background: var(--sel-bg);
  }
  .danger-zone {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    border-color: #fda29b;
  }
  .danger-zone p {
    flex: 1;
    color: var(--danger);
  }
  .error {
    padding: 8px 10px;
    color: var(--err);
    background: #fee4e2;
    border: 1px solid #fda29b;
    border-radius: 4px;
  }
</style>
