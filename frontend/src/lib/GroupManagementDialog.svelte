<script module lang="ts">
  export type GroupDialogMode = 'create' | 'rename' | 'delete'

  export type GroupOperation =
    | { kind: 'create'; name: string }
    | { kind: 'rename'; oldName: string; newName: string }
    | { kind: 'delete'; name: string }

  export type GroupNameValidation = {
    name: string
    error: string
  }

  export function validateGroupName(
    value: string,
    groups: readonly string[],
    oldName = '',
  ): GroupNameValidation {
    const name = value.trim()
    if (name.length < 1 || [...name].length > 32) {
      return { name, error: '分组名称长度必须为 1–32 个字符' }
    }
    if (name === '全部' || name === '未分组') {
      return { name, error: `“${name}”是保留名称` }
    }
    if (groups.some((group) => group.toLocaleLowerCase() === name.toLocaleLowerCase() && group !== oldName)) {
      return { name, error: '已有同名分组' }
    }
    return { name, error: '' }
  }
</script>

<script lang="ts">
  let {
    open,
    mode,
    currentGroup = '',
    groups,
    affectedCount = 0,
    busy = false,
    error = '',
    onclose,
    onsubmit,
  }: {
    open: boolean
    mode: GroupDialogMode
    currentGroup?: string
    groups: readonly string[]
    affectedCount?: number
    busy?: boolean
    error?: string
    onclose: () => void
    onsubmit: (operation: GroupOperation) => void
  } = $props()

  let name = $state('')
  let input = $state<HTMLInputElement | null>(null)
  let localError = $state('')

  $effect(() => {
    if (!open) return
    name = mode === 'rename' ? currentGroup : ''
    localError = ''
  })

  $effect(() => {
    if (open && input && mode !== 'delete') input.focus()
  })

  const TITLES: Record<GroupDialogMode, string> = {
    create: '新建邮箱分组',
    rename: '重命名邮箱分组',
    delete: '删除邮箱分组',
  }

  function submit() {
    if (busy) return
    if (mode === 'delete') {
      onsubmit({ kind: 'delete', name: currentGroup })
      return
    }
    const result = validateGroupName(name, groups, mode === 'rename' ? currentGroup : '')
    localError = result.error
    if (result.error) return
    if (mode === 'create') onsubmit({ kind: 'create', name: result.name })
    else onsubmit({ kind: 'rename', oldName: currentGroup, newName: result.name })
  }

  function handleKeydown(event: KeyboardEvent) {
    if (!open || busy) return
    if (event.key === 'Escape') onclose()
    if (event.key === 'Enter') submit()
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <div class="backdrop">
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="group-dialog-title">
      <h2 id="group-dialog-title">{TITLES[mode]}</h2>

      {#if mode === 'delete'}
        <p>
          删除分组“{currentGroup}”？组内 {affectedCount} 个邮箱将移回“未分组”，账户、Session 和 Token
          均不会删除。
        </p>
      {:else}
        <label for="group-name">{mode === 'create' ? '分组名称（1–32 个字符）' : '新的分组名称'}</label>
        <input
          id="group-name"
          bind:this={input}
          bind:value={name}
          maxlength="32"
          title="分组名称为 1–32 个字符；“全部”和“未分组”是保留名称，且不可与现有分组重名。"
        />
      {/if}

      {#if localError || error}
        <p class="error" role="alert">{localError || error}</p>
      {/if}

      <footer>
        <button disabled={busy} title="关闭窗口并放弃本次分组修改。" onclick={onclose}>取消</button>
        <button
          class:danger={mode === 'delete'}
          class:primary={mode !== 'delete'}
          disabled={busy || (mode === 'delete' && (!currentGroup || currentGroup === '全部' || currentGroup === '未分组'))}
          title={mode === 'delete'
            ? '删除当前自定义分组，并把组内邮箱移回“未分组”。'
            : mode === 'create'
              ? '创建新分组并切换到该分组。'
              : '重命名当前分组，并同步更新组内账户。'}
          onclick={submit}>{mode === 'delete' ? '确认删除' : '保存'}</button
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
    width: 430px;
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
</style>
