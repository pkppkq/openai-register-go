<script module lang="ts">
  /**
   * `_handle_prompt_event`'s title table, app.py 19004-19011. The default arm
   * (19011) is 输入验证码, so an unknown prompt_type still gets a sane heading
   * rather than a blank one.
   */
  export function promptTitle(kind: string | undefined): string {
    switch (kind) {
      case 'phone':
        return '输入手机号'
      case 'phone-code':
      case 'sms-code':
        return '输入短信验证码'
      case 'email-code':
      case 'email-otp':
        return '输入邮箱验证码'
      default:
        return '输入验证码'
    }
  }

  /** `m:ss` left on the clock; clamped at zero. */
  export function formatRemaining(ms: number): string {
    const total = Math.max(0, Math.ceil(ms / 1000))
    const minutes = Math.floor(total / 60)
    const seconds = total % 60
    return `${minutes}:${String(seconds).padStart(2, '0')}`
  }
</script>

<script lang="ts">
  /*
    S26 Prompt 输入 — ported from app.py 16401-16406 (`_request_user_input`) and
    19003-19015 (`_handle_prompt_event`). The Tk version is a
    `simpledialog.askstring(title, f"{email}\n{prompt}")` whose answer goes into
    the queue the worker thread blocks on; here the worker blocks on a channel
    and `AnswerPrompt(jobId, …)` releases it (bindings.go:623).

    Two things this dialog says that the Tk one does not:

    1. THE REQUEST EXPIRES. app.py 16406 is `result_queue.get()` with no timeout
       — the Python worker waits forever, which UI_SPEC §4 flags as a defect
       (a dialog nobody answers holds a browser and a proxy open indefinitely).
       The port bounds it at `promptTimeout` = 10 minutes (bindings.go:38-42),
       after which the worker logs `等待人工输入超时，已按取消处理` and gives up.
       A modal that hid that would be lying about how much time the user has, so
       the remaining time is on screen and the dialog switches to an expired
       state when it runs out.

    2. AN EMPTY ANSWER IS A CANCEL. True in Tk too — 19015 sends `value or ""`
       for a dismissed dialog — but there it is invisible. 取消 here sends the
       empty string explicitly, which is also exactly what 停止 pushes into every
       waiting prompt (bindings.go:548-553).

    The countdown is indicative, not authoritative: the Go timer starts when the
    event is emitted, this one when the webview receives it. The authority is
    AnswerPrompt, which rejects a late reply with `任务当前没有等待输入的请求` —
    so 确定 stays clickable after the countdown reaches zero and the shell reports
    whatever the backend says, instead of this component guessing.

    Pure presentational: the pending request comes in as a prop and the answer
    leaves through a callback; nothing here imports ../wailsjs.
  */

  import { PROMPT_TIMEOUT_MS, type PromptRequest } from './api'

  let {
    request,
    onanswer,
  }: {
    /** The manual-input request a worker is blocked on, or null. */
    request: PromptRequest | null
    /** Delivers the reply. An empty string means the user cancelled. */
    onanswer: (value: string) => void
  } = $props()

  let value = $state('')
  let input = $state<HTMLInputElement | null>(null)

  /** When this request reached the webview, and the current tick. */
  let arrivedAt = $state(0)
  let now = $state(0)

  $effect(() => {
    if (!request) return
    // A new request restarts the clock and clears whatever was half-typed for
    // the previous one. Reads `request` only, so the interval's writes below
    // cannot re-trigger it.
    const start = Date.now()
    arrivedAt = start
    now = start
    value = ''
    const timer = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(timer)
  })

  // Focus the box as it appears: the prompt is blocking a worker, and every
  // second of waiting holds a browser and a proxy open.
  $effect(() => {
    if (request && input) input.focus()
  })

  let remaining = $derived(Math.max(0, PROMPT_TIMEOUT_MS - (now - arrivedAt)))
  let expired = $derived(remaining <= 0)

  let title = $derived(promptTitle(request?.kind))
</script>

{#if request}
  <div class="modal-backdrop">
    <div class="modal" role="dialog" aria-modal="true" aria-label={title}>
      <h2>{title}</h2>
      <!-- app.py 19012 shows `{email}\n{prompt}` as the dialog body. -->
      <p class="mono">{request.email}</p>
      <p>{request.prompt}</p>

      <p class="expiry" class:expired>
        {#if expired}
          等待已超过 10 分钟，后端可能已按取消处理并放弃这一步（`等待人工输入超时，已按取消处理`）。仍可提交，若请求已失效会提示「任务当前没有等待输入的请求」。
        {:else}
          剩余 {formatRemaining(remaining)}：10 分钟内没有回复，后端会按取消处理，这一步会失败。
        {/if}
      </p>

      <label class="sr-only" for="prompt-answer">{title}</label>
      <input
        id="prompt-answer"
        bind:this={input}
        bind:value
        onkeydown={(e) => {
          if (e.key === 'Enter') onanswer(value)
          else if (e.key === 'Escape') onanswer('')
        }}
      />

      <footer>
        <button title="按取消处理：向任务提交空字符串，与「停止」推送给等待中提示的值相同。" onclick={() => onanswer('')}
          >取消</button
        >
        <button class="primary" onclick={() => onanswer(value)}>确定</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgb(17 24 39 / 45%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
  }
  .modal {
    width: 420px;
    max-width: calc(100% - 32px);
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  h2 {
    margin: 0;
    font-size: 13px;
    color: var(--text);
  }
  p {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .expiry {
    color: var(--manual);
  }
  .expiry.expired {
    color: var(--err);
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
  /* The heading already names the field; this keeps the input labelled for a
     screen reader without repeating it on screen. */
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
