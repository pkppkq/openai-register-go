<script module lang="ts">
  /** app.py 282–284: PROXY_ROUTE_MODE_DEFAULT / PROXY_ROUTE_MODE_LOCAL_ONLY. */
  export const PROXY_ROUTE_MODE_DEFAULT = '照旧'
  export const PROXY_ROUTE_MODE_LOCAL_ONLY = '全走本地代理'
  export type ProxyRouteMode = typeof PROXY_ROUTE_MODE_DEFAULT | typeof PROXY_ROUTE_MODE_LOCAL_ONLY
  /** Tuple order is the combo's display order — do not sort. */
  export const PROXY_ROUTE_MODE_OPTIONS: readonly ProxyRouteMode[] = [
    PROXY_ROUTE_MODE_DEFAULT,
    PROXY_ROUTE_MODE_LOCAL_ONLY,
  ]

  /** The four manual pools. Keys are app.py's own (`proxy_pool_title_bases`, 12443). */
  export type ProxyPoolKey = 'register' | 'create' | 'followup' | 'approve'

  /** Raw textarea contents per pool; one proxy per line is the common case. */
  export type ProxyPools = Record<ProxyPoolKey, string>

  /**
   * Display order of the four pools, app.py 13520–13549. Kept as an explicit
   * array, not as `Object.keys`, because the pool set is a Python dict whose
   * order is part of the layout — the same reason the Go side must not iterate
   * a map here.
   */
  export const PROXY_POOL_ORDER: readonly ProxyPoolKey[] = ['register', 'create', 'followup', 'approve']

  /** app.py 12443–12448. `（剩余 N）` is appended at render time (13211). */
  export const PROXY_POOL_TITLE_BASES: Record<ProxyPoolKey, string> = {
    register: '注册/获取 Session 动态代理池',
    create: '创建长链第一步代理池',
    followup: '创建长链后续代理池',
    approve: 'Approve 代理池',
  }

  /** Settings key each pool persists to (UI_SPEC §2). */
  export const PROXY_POOL_SETTINGS_KEYS: Record<ProxyPoolKey, string> = {
    register: 'dynamic_proxies',
    create: 'payment_dynamic_proxy',
    followup: 'followup_dynamic_proxy',
    approve: 'approve_dynamic_proxy',
  }

  /** app.py 12340. */
  export const DEFAULT_LOCAL_PROXY = 'http://127.0.0.1:7890'

  /**
   * `_local_proxy_only_enabled` (app.py 16712-16715), which is the only place
   * Tk ever reads the mode: `str(mode or "").strip() == PROXY_ROUTE_MODE_LOCAL_ONLY`.
   *
   * Everything that is not exactly 全走本地代理 after a strip is 照旧 — including
   * an empty state file, a value written by an older build, and anything a hand
   * edit of state.json put there. Anchoring on the LOCAL_ONLY string rather than
   * on the default is what makes that true: the failure direction of a typo is
   * then "keeps using the proxy pools", not "silently ignores every pool".
   */
  export function coerceRouteMode(mode: string): ProxyRouteMode {
    return mode.trim() === PROXY_ROUTE_MODE_LOCAL_ONLY ? PROXY_ROUTE_MODE_LOCAL_ONLY : PROXY_ROUTE_MODE_DEFAULT
  }

  /**
   * `_proxy_pool_title_text` (app.py 13211-13213):
   * `f"{base}（剩余 {max(0, int(count or 0))}）"`.
   *
   * The clamp and the truncation are both app.py's. A pool count arrives from
   * the backend as a number and from the textarea as a line count, and neither
   * should ever be able to render 剩余 -1 or 剩余 3.5 in a heading.
   */
  export function proxyPoolTitle(key: ProxyPoolKey, count: number): string {
    const n = Number.isFinite(count) ? Math.max(0, Math.trunc(count)) : 0
    return `${PROXY_POOL_TITLE_BASES[key]}（剩余 ${n}）`
  }
</script>

<script lang="ts">
  /*
    S17 代理设置 — ported from app.py 13496–13549 (Tk tab title 代理设置, sidebar
    entry 代理). Labels and hints are verbatim.

    §6 trims S17 to "本地代理, 代理模式, and the four plain pool textareas".
    CUT here (all still in app.py):
      - LabelFrame `代理提供商配置（后台预检池）`: the 阶段/启用/主机:端口/地区/状态
        Treeview, `编辑选中阶段`, `应用配置并预热` and the note
        `各启用池达到 200 后开跑；降到 200 自动补至 500` (13516–13584) — G19;
      - `撞链代理地区` combo + hint (13586–13598) — G22;
      - `单账号撞链并发数` Spin 1–30 + hint (13600–13604);
      - `预检上限/池` Spin, `预检并发` Spin, `预检支付代理池`, `清理无效代理`
        and their hint (13605–13618) — G21;
      - reuse Entries `第一步复用代理` / `后续复用代理` / `Approve 复用代理` (72 each)
        with their hints (13619–13633);
      - checks `提取长链强制日本出口…`, `注册时使用支付链接动态代理…`,
        `旧版强撞 PayPal…` (13634–13636);
      - the second copy of `支付链接扩展目录` Entry(72) + `选择目录` (13637–13642).
    The horizontal PanedWindow disappears with the provider pane, so 手工代理池
    takes the full width.

    Pure presentational: data in through props, changes out through callbacks,
    nothing imported from ../../wailsjs. The shell now persists every one of
    these through the real SaveSettings binding. The shell obtains the displayed
    counts from Go's Python-compatible proxy parser.
  */

  let {
    localProxy,
    routeMode,
    pools,
    counts,
    onlocalproxychange,
    onroutemodechange,
    onpoolchange,
  }: {
    /** `local_proxy`; empty means direct connection. */
    localProxy: string
    /**
     * `proxy_route_mode`. Typed as a plain string because the state file can
     * hold anything: app.py coerces an unknown value to 照旧 on load (14089)
     * *and* on save (14239), and the runtime gate `_local_proxy_only_enabled`
     * (16712) only ever tests `== 全走本地代理` after a strip.
     */
    routeMode: string
    pools: ProxyPools
    /**
     * `剩余 N` per pool. app.py 13211 computes N as
     * `len(parse_proxy_pool_text(text))`, a full parse that can yield MORE than
     * one proxy from a single line (13437: several URLs on one line) and fewer
     * when a line does not normalise. While an asynchronous Go parse is pending,
     * the component temporarily falls back to counting non-blank lines.
     */
    counts?: Partial<Record<ProxyPoolKey, number>>
    onlocalproxychange: (value: string) => void
    /** Tk persists immediately on selection (13511 → 16717 → save_state). */
    onroutemodechange: (mode: ProxyRouteMode) => void
    onpoolchange: (key: ProxyPoolKey, text: string) => void
  } = $props()

  // Mirrors the Tk coercion so a junk state value still renders as 照旧 rather
  // than as an empty combo. JS String.trim() strips the same Unicode spaces
  // Python's str.strip() does, so the comparison matches 16715.
  let selectedMode = $derived<ProxyRouteMode>(coerceRouteMode(routeMode))

  function approximateCount(text: string): number {
    let n = 0
    for (const line of text.split('\n')) {
      if (line.trim() !== '') n += 1
    }
    return n
  }

  function poolTitle(key: ProxyPoolKey): string {
    const supplied = counts?.[key]
    // Go 的异步结果返回前暂时估算；正常显示始终会被完整解析结果替换。
    // parse_proxy_pool_text (app.py:2421) can yield several proxies from one
    // line, so a non-blank line count is a lower bound, not the real 剩余.
    return proxyPoolTitle(key, supplied === undefined ? approximateCount(pools[key]) : supplied)
  }

  let countHint = $derived(
    PROXY_POOL_ORDER.some((key) => counts?.[key] === undefined)
      ? '代理池数量正在解析，当前暂按非空行估算。'
      : '',
  )

  // app.py 13523 etc: `self._scrolled_text(..., height=3)`.
  const POOL_TEXT_ROWS = 3
</script>

<section class="card">
  <h2>代理设置</h2>
  <p class="hint">链式：本地代理 -&gt; 动态代理 -&gt; 目标站点</p>

  <div class="row">
    <label for="local-proxy">本地代理</label>
    <!-- app.py 13501: Entry(width=36). Tk widths are in characters → CSS `ch`. -->
    <input
      id="local-proxy"
      class="w36"
      value={localProxy}
      oninput={(e) => onlocalproxychange(e.currentTarget.value)}
    />

    <label for="proxy-route-mode">代理模式</label>
    <select
      id="proxy-route-mode"
      class="w12"
      value={selectedMode}
      onchange={(e) => onroutemodechange(e.currentTarget.value as ProxyRouteMode)}
    >
      {#each PROXY_ROUTE_MODE_OPTIONS as option (option)}
        <option value={option}>{option}</option>
      {/each}
    </select>

    <span class="hint">本地留空=直连；全走本地代理会忽略所有动态/支付/提供商代理池</span>
  </div>

  <fieldset>
    <legend>手工代理池</legend>
    {#each PROXY_POOL_ORDER as key (key)}
      <div class="pool">
        <label for={`proxy-pool-${key}`} title={countHint}>{poolTitle(key)}</label>
        <textarea
          id={`proxy-pool-${key}`}
          class="mono"
          rows={POOL_TEXT_ROWS}
          spellcheck="false"
          value={pools[key]}
          oninput={(e) => onpoolchange(key, e.currentTarget.value)}
        ></textarea>
      </div>
    {/each}
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
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  .row label + input,
  .row label + select {
    margin-right: 8px;
  }
  .w36 {
    width: 36ch;
    max-width: 100%;
  }
  .w12 {
    /* +2ch so the dropdown arrow does not eat a character. */
    width: 14ch;
  }
  fieldset {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin: 0;
    padding: 4px 12px 12px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
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
</style>
