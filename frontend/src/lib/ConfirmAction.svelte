<script module lang="ts">
  /**
   * One pending confirmation. Built by the shell at click time and cleared when
   * the user answers, so `request === null` is the closed state and there is no
   * second `open` flag to keep in sync with it.
   */
  export type ConfirmRequest = {
    /** The button's Tk caption, verbatim — the user must recognise what they clicked. */
    label: string
    /** Where that caption lives in the Tk source, e.g. `app.py:13670`. */
    source: string
    /** The Tk tooltip: what the action actually does. */
    detail: string
    /** The accounts it will run against — one job per address. */
    emails: string[]
    /**
     * What this can cost, in concrete terms. Never a generic "this may incur
     * charges": the point of the dialog is that the user can tell a rented phone
     * number apart from a created payment link before they agree to it.
     */
    costs: string[]
    /** Anything else about THIS invocation (e.g. a Team account being re-dispatched). */
    notes?: string[]
    /**
     * 仅支付窗口确认可设置。为 true 时显示“本次自动确认支付”复选框；
     * 其他高风险操作不显示，也不能通过 onconfirm 传出自动扣款许可。
     */
    allowPaymentAutoConfirm?: boolean
  }

  /**
   * How many addresses are listed before the tail is summarised.
   *
   * A cap, not a fit: the `.emails` box scrolls at 132px whatever this is. What
   * the number has to protect is the reading — a modal that pushes 确认执行 off
   * the bottom of a 460px dialog gets confirmed unread, which is the one
   * outcome this dialog exists to prevent. The tail line quotes the FULL total
   * rather than the remainder, so the count the user is agreeing to is on
   * screen whether or not the list is complete.
   */
  export const MAX_LISTED_EMAILS = 8

  /**
   * The addresses to show, and how many the list is hiding.
   *
   * Exported and pure because it is the arithmetic behind a money gate: an
   * off-by-one that drops the last address off a list of nine, or an overflow
   * that reads 0 when one is hidden, both understate what the user is about to
   * pay for. Nothing else in this component decides anything.
   */
  export function listEmails(emails: string[]): { listed: string[]; overflow: number } {
    return {
      listed: emails.slice(0, MAX_LISTED_EMAILS),
      overflow: Math.max(0, emails.length - MAX_LISTED_EMAILS),
    }
  }

  /**
   * 自动确认支付必须同时满足“本弹窗明确允许”和“用户本次主动勾选”。
   * 普通确认弹窗即使误传 checked=true，也只能得到 false。
   */
  export function paymentAutoConfirmChoice(
    request: ConfirmRequest | null,
    checked: boolean,
  ): boolean {
    return request?.allowPaymentAutoConfirm === true && checked
  }
</script>

<script lang="ts">
  /*
    Money gate. NOT a port — the Tk app has no confirmation on any of these
    buttons (app.py 13670 / 13726 / 13746 all call their handler directly; the
    only checks are `请先选中邮箱` and `任务正在运行`), and this dialog is
    deliberately added on top of it.

    Why the port diverges here. In Tk these are 20-pixel-tall buttons in a
    toolbar you reach with a deliberate mouse move, on a window that is not
    focus-stealing and has no keyboard-activatable rows. In a webview the same
    three actions sit in a flex row that reflows with the window, every table row
    is focusable and answers Enter/Space, and a stray click or a held key can
    reach a button that rents a billable SMSBower number (jobs.go:93) and creates
    a real payment link (bindings.go:496). One accidental click is real money and
    a real OpenAI account state change, and neither is undoable from this UI.
    UI_SPEC §5 already flags the same class of problem for 清空列表 (row 18:
    "NO CONFIRMATION in Tk … Add a confirmation in the port"); this applies that
    ruling to the spending actions.

    Safety details, all deliberate:
      - focus lands on 取消, not on the confirm button, so a stray Enter aborts;
      - Enter is NOT bound to confirm anywhere in this dialog;
      - Escape cancels;
      - the backdrop is inert — a misclick outside must not resolve the dialog in
        either direction;
      - the confirm button carries the action's verbatim label, so the second
        click is a second reading of what is about to run, not a bare 确定.

    Pure presentational: data in through props, decisions out through callbacks,
    nothing imported from ../wailsjs. The shell does not call a binding until
    `onconfirm` fires.
  */

  let {
    request,
    oncancel,
    onconfirm,
  }: {
    /** The pending confirmation, or null when nothing is being confirmed. */
    request: ConfirmRequest | null
    oncancel: () => void
    onconfirm: (autoConfirmPayment: boolean) => void
  } = $props()

  let cancelButton = $state<HTMLButtonElement | null>(null)
  // 组件由主壳按 request 重新创建，因此每次执行都从未勾选开始。
  let autoConfirmPayment = $state(false)

  // The safe answer takes focus, so Enter-on-arrival aborts instead of spending.
  $effect(() => {
    if (request && cancelButton) cancelButton.focus()
  })

  let summary = $derived(listEmails(request?.emails ?? []))
</script>

<svelte:window
  onkeydown={(e) => {
    if (request && e.key === 'Escape') oncancel()
  }}
/>

{#if request}
  <div class="modal-backdrop">
    <div class="modal" role="dialog" aria-modal="true" aria-label={`确认执行：${request.label}`}>
      <h2>确认执行：{request.label}</h2>

      <p class="detail">{request.detail}</p>

      <p class="warn">
        此操作会花钱，且无法撤销。Tk 版本没有这一步确认（{request.source}），这里是移植时特意加的。
      </p>

      <ul class="costs">
        {#each request.costs as cost (cost)}
          <li>{cost}</li>
        {/each}
      </ul>

      {#if request.notes && request.notes.length > 0}
        <ul class="notes">
          {#each request.notes as note (note)}
            <li>{note}</li>
          {/each}
        </ul>
      {/if}

      {#if request.allowPaymentAutoConfirm}
        <div class="auto-confirm">
          <label>
            <input type="checkbox" bind:checked={autoConfirmPayment} />
            <span>本次允许程序自动点击支付页最终确认按钮（可能立即产生真实扣款）</span>
          </label>
          <p>
            默认关闭且仅对本次执行有效。即使勾选，也必须再点击下方“确认执行”按钮才会启动；
            不勾选时只打开支付页，不会自动提交。
          </p>
        </div>
      {/if}

      <!-- One job per address either way — the batch parent dispatches a child
           per account (batch.go) — so the count is the number of runs that are
           about to start, whether or not they are bounded by a window. -->
      <p class="count">将对 {request.emails.length} 个账号各启动一个任务：</p>
      <ul class="emails mono">
        {#each summary.listed as email (email)}
          <li>{email}</li>
        {/each}
        {#if summary.overflow > 0}
          <li class="muted">…等共 {request.emails.length} 个</li>
        {/if}
      </ul>

      <footer>
        <button bind:this={cancelButton} title="不执行，关闭本窗口。（Esc）" onclick={oncancel}>取消</button>
        <button
          class="danger"
          title={`确认执行：${request.detail}`}
          onclick={() => onconfirm(paymentAutoConfirmChoice(request, autoConfirmPayment))}
        >
          确认执行 {request.label}
        </button>
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
    width: 460px;
    max-width: calc(100% - 32px);
    max-height: calc(100% - 32px);
    overflow: auto;
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
  .detail {
    color: var(--muted);
  }
  .warn {
    padding: 8px 12px;
    border-radius: 4px;
    background: #fee4e2;
    border: 1px solid #fda29b;
    color: var(--err);
  }
  ul {
    margin: 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .costs li {
    color: var(--err);
  }
  .notes li {
    color: var(--manual);
  }
  .auto-confirm {
    padding: 9px 10px;
    border: 1px solid #f79009;
    border-radius: 4px;
    background: #fffaeb;
    color: #93370d;
  }
  .auto-confirm label {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-weight: 700;
    cursor: pointer;
  }
  .auto-confirm input {
    flex: 0 0 auto;
    margin-top: 2px;
  }
  .auto-confirm p {
    margin-top: 6px;
    color: #b54708;
  }
  .emails {
    max-height: 132px;
    overflow: auto;
    list-style: none;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface);
  }
  .emails li {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
</style>
