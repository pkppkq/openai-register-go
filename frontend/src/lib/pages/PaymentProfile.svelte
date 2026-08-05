<script module lang="ts">
  /**
   * 发布版刻意不继承 app.py 66 的开发机 D 盘路径。用户必须选择或填写自己的
   * 扩展目录；空默认值与 Go 的 settings.DefaultPaypalExtensionDir 保持一致。
   */
  export const DEFAULT_PAYPAL_EXTENSION_DIR = ''

  /**
   * The five settings keys S16 owns (UI_SPEC §3). `paypal_phone_pool_index` is
   * NOT here on purpose: it is the round-robin cursor
   * `_take_paypal_phone_config` advances (app.py 17593), never a control, and
   * the load-patch-save chain preserves it untouched.
   */
  export type PaypalSettings = {
    /** `paypal_phone` */
    phone: string
    /** `paypal_card` — the base card; the first three segments are replaced per use. */
    card: string
    /** `paypal_sms_url` */
    smsUrl: string
    /** `paypal_phone_pool` — one `+手机号----https://接码链接` per line. */
    phonePool: string
    /** `payment_extension_dir` — shared with S17 (app.py 13640 is a second Entry on the same var). */
    extensionDir: string
  }

  /** 发布版新状态文件的安全初值：不携带开发机路径或支付资料。 */
  export const PAYPAL_DEFAULTS: PaypalSettings = {
    phone: '',
    card: '',
    smsUrl: '',
    phonePool: '',
    extensionDir: DEFAULT_PAYPAL_EXTENSION_DIR,
  }
</script>

<script lang="ts">
  /*
    S16 支付资料 — ported from app.py 13454–13494 (Tk tab title `PayPal扩展`,
    sidebar entry `支付资料`; the nav key maps onto `paypal_frame`, app.py 14017).
    Labels, hints and button captions are verbatim; `title=` strings are the Tk
    tooltips (UI_SPEC §0.7).

    SCOPE. §6's slice-1 table does not list S16 at all, and slice 2 keeps the
    payment WINDOW (G20) out by an explicit decision — it completes real charges
    unattended. What is built here is the settings/profile block ONLY: the five
    `paypal_*` / `payment_extension_dir` keys, saved to state.json. Nothing on
    this pane starts a task, opens a payment link, or spends money, and nothing
    here may grow such a button before G20 lands.

    CUT here (still in app.py):
      - the 支付卡池 Treeview 卡号(220) / 有效期(100) / CVV(80) / 状态(80), height 3
        (13485–13494): it renders `self.payment_cards`, a state.json TOP-LEVEL
        list with no binding at all, so it could only ever render empty.

    `选择目录` 由 OpenPaymentExtensionDirectory 打开原生目录对话框。支付卡池
    已拆成独立的 PaymentCardPoolFull，由父层在本资料块下方渲染，避免同一页
    出现两个互不一致的卡池编辑器。

    Pure presentational: data in through props, changes out through callbacks,
    nothing imported from ../../wailsjs.
  */

  let {
    value,
    busy = false,
    canpickdir = false,
    status = '',
    onchange,
    onpickdir,
    onsave,
  }: {
    /** Current settings; the parent owns them. */
    value: PaypalSettings
    /** Disables 保存 while a save is in flight, or before settings have loaded. */
    busy?: boolean
    /**
     * Whether `选择目录` can do anything. It needs a bound directory dialog and
     * there is none — false renders the button disabled rather than leaving a
     * control that looks live and silently does nothing. Same treatment as
     * `从文件导入` in MailImport.svelte and `测试余额` in PhoneSms.svelte.
     */
    canpickdir?: boolean
    /** Whether the 支付卡池 block can do anything — see the header comment. */
    /**
     * Success line for the last save. Tk logs it instead (app.py 14364
     * `PayPal 扩展资料已保存`), but this pane has no log view. Failures are not a
     * prop: `save_paypal_settings` validates nothing, so the only way a save can
     * fail is the write itself, which the shell already reports in its banner.
     */
    status?: string
    onchange: (next: PaypalSettings) => void
    /** `选择目录` — app.py 14355 `filedialog.askdirectory(title="选择解压后的 Chrome 扩展目录")`. */
    onpickdir: () => void
    /** `保存` — app.py 14362: `save_state()` then log. No validation, no coercion. */
    onsave: () => void
  } = $props()

  function patch(part: Partial<PaypalSettings>) {
    onchange({ ...value, ...part })
  }

  // app.py 13474: `self._scrolled_text(..., height=3)`.
  const POOL_TEXT_ROWS = 3
</script>

<section class="card">
  <h2>PayPal扩展</h2>
  <p class="hint">支付 PP 用；这里的手机号不是授权接码手机号</p>

  <div class="row">
    <label for="paypal-phone">PP手机号</label>
    <!-- app.py 13459: Entry(width=24). Tk widths are in characters → CSS `ch`. -->
    <input
      id="paypal-phone"
      class="w24"
      value={value.phone}
      oninput={(e) => patch({ phone: e.currentTarget.value })}
    />

    <label for="paypal-card">卡信息</label>
    <!-- app.py 13461: Entry with no width, packed fill=X expand=True. -->
    <input
      id="paypal-card"
      class="mono grow"
      spellcheck="false"
      value={value.card}
      oninput={(e) => patch({ card: e.currentTarget.value })}
    />

    <button disabled={busy} title="保存当前 PayPal 手机号、卡信息和取码链接到本地状态文件。" onclick={onsave}
      >保存</button
    >
  </div>

  <div class="row">
    <label for="payment-extension-dir">支付链接扩展目录</label>
    <!-- app.py 13466: Entry(width=72) packed fill=X expand=True — 72ch is the
         basis, not a minimum, or the row would force the pane to scroll. -->
    <input
      id="payment-extension-dir"
      class="mono grow72"
      spellcheck="false"
      value={value.extensionDir}
      oninput={(e) => patch({ extensionDir: e.currentTarget.value })}
    />
    <button
      disabled={busy || !canpickdir}
      title={canpickdir
        ? '选择解压后的 Chrome PayPal 支付扩展目录；打开支付链接时会加载它。'
        : '当前宿主未启用目录选择器；可直接把解压后的扩展目录路径填进左侧输入框。'}
      onclick={onpickdir}>选择目录</button
    >
  </div>

  <p class="hint">卡信息格式：卡号----有效期----CVV----电话----sms-token----姓名----街道,城市 邮编,国家</p>

  <div class="row">
    <label for="paypal-sms-url">PP取码链接</label>
    <input
      id="paypal-sms-url"
      class="mono grow"
      spellcheck="false"
      value={value.smsUrl}
      oninput={(e) => patch({ smsUrl: e.currentTarget.value })}
    />
  </div>

  <div class="pool">
    <label for="paypal-phone-pool"
      >PP手机号+接码池（每行一个：+手机号----https://接码链接；打开支付链接优先取第一行，用后移除）</label
    >
    <textarea
      id="paypal-phone-pool"
      class="mono"
      rows={POOL_TEXT_ROWS}
      spellcheck="false"
      value={value.phonePool}
      oninput={(e) => patch({ phonePool: e.currentTarget.value })}
    ></textarea>
  </div>

  {#if status}
    <p class="ok">{status}</p>
  {/if}
</section>

<style>
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
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  .row label {
    color: var(--text);
  }
  .row label + input {
    margin-right: 8px;
  }
  /* Tk Entry(width=N) is N characters wide. */
  .w24 {
    width: 24ch;
  }
  /* pack(fill=X, expand=True). */
  .grow {
    flex: 1 1 24ch;
    min-width: 0;
  }
  .grow72 {
    flex: 1 1 72ch;
    min-width: 0;
  }
  .pool {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .pool textarea {
    width: 100%;
    resize: vertical;
    line-height: 1.5;
  }
  .hint {
    margin: 0;
    color: var(--muted);
    overflow-wrap: anywhere;
  }
  .ok {
    margin: 0;
    color: var(--ok);
  }
</style>
