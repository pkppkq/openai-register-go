<script module lang="ts">
  export type ManualSessionMode = 'merge' | 'replace'
  export type SessionPlanOverride = 'auto' | 'plus' | 'free' | 'team' | 'k12' | 'pro'

  export type ManualSessionSubmission = {
    mode: ManualSessionMode
    email: string
    text: string
    planOverride: SessionPlanOverride
  }

  export const SESSION_PLAN_OVERRIDES: readonly SessionPlanOverride[] = [
    'auto',
    'plus',
    'free',
    'team',
    'k12',
    'pro',
  ]

  export function normalizeSessionPlanOverride(value: string): SessionPlanOverride {
    return SESSION_PLAN_OVERRIDES.includes(value as SessionPlanOverride)
      ? (value as SessionPlanOverride)
      : 'auto'
  }
</script>

<script lang="ts">
  let {
    open,
    mode,
    email = '',
    initialText = '',
    initialPlan = 'auto',
    busy = false,
    error = '',
    onclose,
    onsubmit,
  }: {
    open: boolean
    mode: ManualSessionMode
    email?: string
    initialText?: string
    initialPlan?: SessionPlanOverride
    busy?: boolean
    error?: string
    onclose: () => void
    onsubmit: (submission: ManualSessionSubmission) => void
  } = $props()

  let value = $state('')
  let planOverride = $state<SessionPlanOverride>('auto')
  let localError = $state('')
  let textarea = $state<HTMLTextAreaElement | null>(null)

  $effect(() => {
    if (!open) return
    value = initialText
    planOverride = normalizeSessionPlanOverride(initialPlan)
    localError = ''
  })

  $effect(() => {
    if (open && textarea) textarea.focus()
  })

  function submit() {
    if (busy) return
    if (!value.trim()) {
      localError = '请粘贴 ChatGPT Session JSON 或 Access Token'
      return
    }
    localError = ''
    onsubmit({
      mode,
      email,
      text: value,
      planOverride: mode === 'merge' ? planOverride : 'auto',
    })
  }

  function handleKeydown(event: KeyboardEvent) {
    if (!open || busy) return
    if (event.key === 'Escape') onclose()
    if (event.key === 'Enter' && event.ctrlKey) {
      event.preventDefault()
      submit()
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <div class="backdrop">
    <div
      class:replace={mode === 'replace'}
      class="dialog"
      role="dialog"
      aria-modal={mode === 'merge'}
      aria-labelledby="manual-session-title"
    >
      <h2 id="manual-session-title">{mode === 'merge' ? '填入 Session' : '粘贴 Session JSON'}</h2>

      {#if mode === 'merge'}
        <p class="email">当前邮箱: {email}</p>
        <p>粘贴 ChatGPT /api/auth/session JSON、导出的 session_json，或单独 Access Token</p>
        <div class="options">
          <label for="manual-session-plan">套餐覆盖</label>
          <select
            id="manual-session-plan"
            bind:value={planOverride}
            title="auto 按粘贴内容判断；如果确认网页套餐，可手工覆盖为 plus/free/team/k12/pro。"
          >
            {#each SESSION_PLAN_OVERRIDES as option (option)}
              <option value={option}>{option}</option>
            {/each}
          </select>
          <span>auto=按粘贴内容自动判断；如果你确认网页是 Plus，可选 plus</span>
        </div>

        <div class="buttons top">
          <button
            class="primary grow"
            disabled={busy}
            title="把粘贴的 Session/Token 合并保存到当前选中的邮箱；保留未被新内容覆盖的现有字段。"
            onclick={submit}>确认保存Session</button
          >
          <button disabled={busy} title="关闭窗口，不保存输入内容。" onclick={onclose}>取消</button>
        </div>
      {:else}
        <p>粘贴 ChatGPT Session JSON / Access Token</p>
        <p class="note">
          保存后会新建 pasted-session-YYYYMMDD-HHMMSS 临时账户并替换其 Session 数据，不会生成支付链接。
        </p>
      {/if}

      <textarea
        bind:this={textarea}
        bind:value
        class="mono"
        rows={mode === 'merge' ? 18 : 16}
        spellcheck="false"
        title="可粘贴 /api/auth/session JSON、导出的 session_json，或单独 Access Token；Ctrl+Enter 提交。"
      ></textarea>

      {#if localError || error}
        <p class="error" role="alert">{localError || error}</p>
      {/if}

      <div class="buttons bottom">
        {#if mode === 'merge'}
          <button
            class="primary grow"
            disabled={busy}
            title="把粘贴的 Session/Token 合并保存到当前选中的邮箱；保留未被新内容覆盖的现有字段。"
            onclick={submit}>确认保存Session</button
          >
          <button disabled={busy} title="关闭窗口，不保存输入内容。" onclick={onclose}>取消</button>
        {:else}
          <button disabled={busy} title="关闭粘贴窗口，不保存当前输入内容。" onclick={onclose}>取消</button>
          <button
            class="primary"
            disabled={busy}
            title="解析粘贴内容中的 Access Token，保存为新的临时 Session 账号。"
            onclick={submit}>保存Session</button
          >
        {/if}
      </div>
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
    width: 820px;
    height: 620px;
    max-width: calc(100% - 32px);
    max-height: calc(100% - 32px);
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .dialog.replace {
    width: 720px;
    height: 420px;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 13px;
  }
  .email {
    font-weight: 600;
  }
  .options {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  .options span,
  .note {
    color: var(--muted);
  }
  textarea {
    min-height: 140px;
    flex: 1;
    resize: none;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .buttons .grow {
    flex: 1;
  }
  .error {
    padding: 8px 10px;
    color: var(--err);
    background: #fee4e2;
    border: 1px solid #fda29b;
    border-radius: 4px;
  }
</style>
