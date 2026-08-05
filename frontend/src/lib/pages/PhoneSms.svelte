<script module lang="ts">
  /** The five SMSBower settings keys slice 1 needs (UI_SPEC §2, `smsbower_*`). */
  export type SmsBowerSettings = {
    /** `smsbower_enabled` */
    enabled: boolean
    /** `smsbower_api_key` — required when enabled (app.py 14391). */
    apiKey: string
    /** `smsbower_service` — `[A-Za-z0-9_]+` (app.py 14370). */
    service: string
    /** `smsbower_country` — digits only (app.py 14372). */
    country: string
    /** `smsbower_max_price` — empty = no cap, else > 0 (app.py 14374). */
    maxPrice: string
  }

  /** app.py 82–84 + 12383–12387: the StringVar seeds for a fresh state file. */
  export const SMSBOWER_DEFAULTS: SmsBowerSettings = {
    enabled: false,
    apiKey: '',
    service: 'dr',
    country: '33',
    maxPrice: '0.07',
  }

  export type TurnstileSettings = {
    enabled: boolean
    url: string
  }

  export const TURNSTILE_DEFAULTS: TurnstileSettings = {
    enabled: false,
    url: 'http://127.0.0.1:8888',
  }

  /**
   * `_smsbower_settings`' normalisation half (app.py 14367–14369): strip every
   * field, and substitute the default for a blank 服务代码 / 国家 ID.
   *
   * `settings.ToSnapshot` does the same on write (snapshot.go:434-436), so this
   * is what the state file ends up holding either way — the reason to repeat it
   * here is app.py 14396-14397, which pushes the two substituted values straight
   * back into the entries so the user can see that an empty box became `dr`/`33`.
   *
   * JS `trim()` and Python `str.strip()` disagree on a couple of exotic code
   * points (U+FEFF, U+001C..U+001F). It does not reach disk: Go re-applies its
   * exact `pyStrip` on save, so this only ever affects the echoed value.
   */
  export function normalizeSmsBower(value: SmsBowerSettings): SmsBowerSettings {
    return {
      enabled: value.enabled,
      apiKey: value.apiKey.trim(),
      service: value.service.trim() || SMSBOWER_DEFAULTS.service,
      country: value.country.trim() || SMSBOWER_DEFAULTS.country,
      // Empty is meaningful — it is "no cap" — so it is never defaulted.
      maxPrice: value.maxPrice.trim(),
    }
  }

  /** app.py 14370 `re.fullmatch(r"[A-Za-z0-9_]+", service)` — an ASCII class. */
  const SERVICE_RE = /^[A-Za-z0-9_]+$/
  /**
   * app.py 14372 `country.isdigit()`. Python's test is Unicode-aware, so this
   * is `\p{Nd}` rather than `[0-9]` — the same call `settings.pyIsDigit` makes.
   */
  const COUNTRY_RE = /^\p{Nd}+$/u
  /** Python's `float()` grammar, restricted to finite decimals (see below). */
  const FLOAT_RE = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/

  /**
   * `settings.pyDecimalASCII`: Python's `float()` folds Unicode decimal digits
   * to ASCII first, so `float("１.５")` is 1.5 (app.py 14376).
   *
   * Nd code points come in contiguous runs of ten, so a digit's value is how far
   * it sits above the first code point of its run — found by walking back while
   * the neighbour is still Nd, at most nine places.
   */
  function foldDecimalDigits(text: string): string {
    return text.replace(/\p{Nd}/gu, (ch) => {
      const cp = ch.codePointAt(0) as number
      for (let value = 0; value < 9; value += 1) {
        if (!COUNTRY_RE.test(String.fromCodePoint(cp - value - 1))) return String(value)
      }
      return '9'
    })
  }

  /**
   * `_smsbower_settings`' validation half plus save_smsbower_settings' API-key
   * check (app.py 14370–14391). Returns app.py's own message, or '' when valid.
   *
   * This has to live on the client: `SaveSettings` does NOT validate
   * (bindings.go:371 only calls `ToSnapshot`), so without it the UI would write
   * a value into the real state.json that the Tk app itself refuses to write.
   *
   * ONE DELIBERATE DIVERGENCE: `float()` also accepts `inf` / `nan` / `1_0`, and
   * because `float("nan") <= 0` is False, app.py — and `ValidateSMSBower`, which
   * documents it — let `nan` through as a price cap. FLOAT_RE rejects all three.
   * Refusing nonsense in the editor is the safe direction, and it cannot
   * desynchronise anything: the value simply never reaches disk.
   */
  export function validateSmsBower(value: SmsBowerSettings): string {
    const next = normalizeSmsBower(value)
    if (!SERVICE_RE.test(next.service)) return 'SMSBower 服务代码格式不正确'
    if (!COUNTRY_RE.test(next.country)) return 'SMSBower 国家 ID 必须是数字'
    if (next.maxPrice !== '') {
      const ascii = foldDecimalDigits(next.maxPrice)
      // app.py 14375-14379: unparseable and `<= 0` raise the same message.
      if (!FLOAT_RE.test(ascii) || Number(ascii) <= 0) {
        return 'SMSBower 最高单价必须是大于 0 的数字，或留空'
      }
    }
    // app.py 14390 — checked last, after the field formats.
    if (next.enabled && next.apiKey === '') return '启用 SMSBower 前请填写 API Key'
    return ''
  }
</script>

<script lang="ts">
  /*
    S15 手机与接码 — ported from app.py 13388–13452 (Tk tab title 手机号池).
    Labels, hints and button captions verbatim; `title=` strings are the Tk
    tooltips (UI_SPEC §0.7).

    §6 trims S15 to "SMSBower block only (启用 / API Key / 服务代码 / 国家 ID /
    最高单价 / 测试余额 / 保存)". §6's `保存` is S15's button, whose real caption is
    `保存设置` (app.py 13405) — the verbatim caption wins.
    Turnstile Solver 的启用、URL 保存与只读健康探测已接到 Go；测试连接不会
    创建验证码任务，只按顺序读取 /health、/v1/health 和根路径。
    CUT here (all still in app.py):
      - the per-line phone-pool hint `每行：+手机号https://短信链接 …` (13427);
      - `每个手机号最多接码次数（0=不限制）` Entry(8) (13428–13431);
      - the phone textarea (h3) and its vertical button stack `导入手机号` /
        `重置手机号` / `清空手机号` / `手动取码` (13432–13441);
      - the `手机号状态` Treeview 手机号(180)/接码次数(80)/状态(120)/最近验证码(120),
        height 3 (13442–13452).
    Slice 1 keeps this screen only because registration hits phone verification.

    Pure presentational: data in through props, changes out through callbacks,
    nothing imported from ../../wailsjs. `保存设置` now reaches the real
    SaveSettings binding through the shell；`测试余额` 使用只读绑定查询余额与
    价格，接口形状不包含租号方法。
  */

  let {
    value,
    busy = false,
    cantestbalance = false,
    error = '',
    status = '',
    turnstile,
    turnstileBusy = false,
    turnstileError = '',
    turnstileStatus = '',
    onchange,
    ontestbalance,
    onsave,
    onturnstilechange,
    onturnstilesave,
    onturnstileprobe,
  }: {
    /** Current settings; the parent owns them. */
    value: SmsBowerSettings
    /** Disables 测试余额 / 保存设置 while a probe or save is in flight. */
    busy?: boolean
    /**
     * Whether `测试余额` can do anything. It needs a bound balance call, and
     * there is none — false renders the button disabled rather than leaving a
     * control that looks live and silently does nothing. Same treatment as
     * `从文件导入` in MailImport.svelte.
     */
    cantestbalance?: boolean
    /**
     * Validation / probe failure to show inline. Tk raises these through
     * `messagebox.showwarning` (app.py 14394, 14440); a modal has no place in
     * the web shell, so the parent passes the same text down as a banner.
     */
    error?: string
    /**
     * Success line for the last save. Tk logs it instead (app.py 14400
     * `SMSBower 接码设置已保存`), but this pane has no log view.
     */
    status?: string
    turnstile: TurnstileSettings
    turnstileBusy?: boolean
    turnstileError?: string
    turnstileStatus?: string
    onchange: (next: SmsBowerSettings) => void
    /** `测试余额` — app.py 14434 validates, saves, then calls GetBalance. */
    ontestbalance: () => void
    /**
     * `保存设置` — app.py 14388. NOTE for the wiring agent: saving does not just
     * persist. `_smsbower_settings` (14366) substitutes the defaults for a blank
     * 服务代码/国家 ID and 14396–14397 writes the normalised values *back into the
     * entries*, so the parent must echo the normalised settings into `value`.
     */
    onsave: () => void
    onturnstilechange: (next: TurnstileSettings) => void
    onturnstilesave: () => void
    onturnstileprobe: () => void
  } = $props()

  function patch(part: Partial<SmsBowerSettings>) {
    onchange({ ...value, ...part })
  }

  function patchTurnstile(part: Partial<TurnstileSettings>) {
    onturnstilechange({ ...turnstile, ...part })
  }
</script>

<section class="card">
  <h2>手机号池</h2>

  <fieldset>
    <legend>SMSBower 自动接码</legend>

    <div class="row">
      <label class="check">
        <input
          type="checkbox"
          checked={value.enabled}
          onchange={(e) => patch({ enabled: e.currentTarget.checked })}
        />
        启用
      </label>

      <label for="smsbower-api-key">API Key</label>
      <!-- app.py 13395: Entry(show="*", width=32). Tk widths are in characters,
           so they map onto CSS `ch`. -->
      <input
        id="smsbower-api-key"
        type="password"
        class="w32"
        autocomplete="off"
        value={value.apiKey}
        oninput={(e) => patch({ apiKey: e.currentTarget.value })}
      />

      <label for="smsbower-service">服务代码</label>
      <input
        id="smsbower-service"
        class="w7"
        value={value.service}
        oninput={(e) => patch({ service: e.currentTarget.value })}
      />

      <label for="smsbower-country">国家 ID</label>
      <input
        id="smsbower-country"
        class="w7"
        value={value.country}
        oninput={(e) => patch({ country: e.currentTarget.value })}
      />
    </div>

    <div class="row">
      <!-- app.py 13402 interpolates SMSBOWER_DEFAULT_MAX_PRICE into the label. -->
      <label for="smsbower-max-price">最高单价（建议 {SMSBOWER_DEFAULTS.maxPrice}；留空不限）</label>
      <input
        id="smsbower-max-price"
        class="w9"
        value={value.maxPrice}
        oninput={(e) => patch({ maxPrice: e.currentTarget.value })}
      />
      <button
        disabled={busy || !cantestbalance}
        title={cantestbalance
          ? '验证 SMSBower API Key，并显示当前余额。'
          : '当前宿主未启用 SMSBower 只读检测入口。'}
        onclick={ontestbalance}>测试余额</button
      >
      <button disabled={busy} title="保存 SMSBower 接码设置到本地状态文件。" onclick={onsave}
        >保存设置</button
      >
    </div>

    {#if error}
      <p class="banner">{error}</p>
    {/if}
    {#if status}
      <p class="ok">{status}</p>
    {/if}

    <p class="hint">
      OpenAI 服务默认 dr；协议流程使用所填国家，浏览器要求美国号时自动改用国家 ID 187。API Key
      保存在本机 state.json。
    </p>
  </fieldset>

  <fieldset>
    <legend>Turnstile Solver（协议过盾，可选）</legend>
    <div class="row">
      <label class="check">
        <input
          type="checkbox"
          checked={turnstile.enabled}
          disabled={turnstileBusy}
          onchange={(event) => patchTurnstile({ enabled: event.currentTarget.checked })}
        />
        启用
      </label>
      <label for="turnstile-solver-url">服务 URL</label>
      <input
        id="turnstile-solver-url"
        class="mono solver-url"
        value={turnstile.url}
        disabled={turnstileBusy}
        placeholder={TURNSTILE_DEFAULTS.url}
        oninput={(event) => patchTurnstile({ url: event.currentTarget.value })}
      />
      <button disabled={turnstileBusy} title="联网探测 Solver 健康接口，不提交验证码任务。" onclick={onturnstileprobe}
        >测试连接</button
      >
      <button disabled={turnstileBusy} title="保存 Turnstile Solver 启用状态和服务 URL。" onclick={onturnstilesave}
        >保存</button
      >
    </div>
    <p class="hint">协议注册遇到 Turnstile 时才使用；测试连接只检查服务是否可达。</p>
    {#if turnstileError}<p class="banner">{turnstileError}</p>{/if}
    {#if turnstileStatus}<p class="ok">{turnstileStatus}</p>{/if}
  </fieldset>
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
  fieldset {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin: 0;
    padding: 4px 12px 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
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
  .check {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-right: 8px;
  }
  /* Tk Entry(width=N) is N characters wide. */
  .w7 {
    width: 7ch;
  }
  .w9 {
    width: 9ch;
  }
  .w32 {
    width: 32ch;
    max-width: 100%;
  }
  .solver-url {
    width: 42ch;
    max-width: 100%;
  }
  .hint {
    margin: 0;
    color: var(--muted);
    line-height: 1.6;
  }
  .banner {
    margin: 0;
    padding: 6px 10px;
    border-radius: 4px;
    background: #fee4e2;
    border: 1px solid #fda29b;
    color: var(--err);
  }
  .ok {
    margin: 0;
    color: var(--ok);
  }
</style>
