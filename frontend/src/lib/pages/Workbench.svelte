<script module lang="ts">
  /*
    Constants and pure helpers for the account table, exported so the shell can
    reuse them and so they can be read next to their Go twins. Every one is a
    port of a named symbol in internal/accounts (itself a port of app.py) —
    where the two disagree, app.py wins and the Go file is cited for the rule.

    AccountRow is imported here rather than in the instance script below: module
    scope is visible from there, and importing the same name twice in one
    component is a redeclaration.
  */
  import type { AccountRow } from '../api'

  /** app.py 296-297 / accounts.GroupAll, GroupDefault. */
  export const GROUP_ALL = '全部'
  export const GROUP_DEFAULT = '未分组'

  /** app.py 316-325 / accounts.StatusFilterOptions. Order is user-visible. */
  export const STATUS_FILTER_ALL = '全部状态'
  export const STATUS_FILTER_OPTIONS: readonly string[] = [
    STATUS_FILTER_ALL,
    '待处理',
    '有 Session',
    'Plus',
    'Team',
    '提链成功',
    '失败',
  ]

  /**
   * app.py 19153 / accounts.failureWords. UI_SPEC §7.1: the 失败 filter is a
   * SUBSTRING match over the rendered status, not an enum test — 登录失败,
   * 代理耗尽 and 疑似已封禁 all match.
   */
  export const FAILURE_WORDS = ['失败', '错误', '耗尽', '停用', '封禁', '不可用', '拒绝', '超时']

  /** app.py 309 / accounts.SortColumns — left-to-right column order. */
  export const SORT_COLUMNS = ['email', 'type', 'status', 'attempts'] as const
  export type SortColumn = (typeof SORT_COLUMNS)[number]

  /**
   * app.py 310-315 / accounts.SortLabels. UI_SPEC §S9 calls the fourth column
   * 次数; §7.1 corrects it to 撞链次数 and app.py agrees — 撞链次数 it is.
   */
  export const SORT_LABELS: Record<SortColumn, string> = {
    email: '邮箱',
    type: '类型',
    status: '状态',
    attempts: '撞链次数',
  }

  /** app.py 13885-13888: Tk column widths, in pixels. */
  export const SORT_WIDTHS: Record<SortColumn, number> = {
    email: 260,
    type: 72,
    status: 160,
    attempts: 70,
  }

  /** app.py 305-307 / accounts.SortCustom|Asc|Desc. */
  export type SortDirection = 'custom' | 'asc' | 'desc'

  /**
   * JS `.toLowerCase()` stands in for Python `.casefold()`. They differ only on
   * ß/ﬁ-class characters; internal/accounts/pyvalue.go carries a replacer for
   * those because Go's ToLower is weaker still. Emails, account types and the
   * status vocabulary contain none of them.
   */
  function fold(value: string): string {
    return value.toLowerCase()
  }

  /** accounts.ContainsFailureWord. */
  export function containsFailureWord(statusText: string): boolean {
    return FAILURE_WORDS.some((word) => statusText.includes(word))
  }

  /**
   * `accounts.Lookups.HasLink` (accounts.go:182): the row's link, trimmed, is
   * non-empty. The Go row ships the link itself rather than a boolean because
   * S10 needs the value too.
   */
  export function hasLink(row: AccountRow): boolean {
    return row.link.trim() !== ''
  }

  /**
   * `_account_matches_status_filter` (app.py 19136-19158) /
   * accounts.MatchesStatusFilter. An unknown filter passes everything, as in
   * Python. Note 待处理 is "no session AND no link AND not failed", NOT
   * "status_text === 待处理".
   */
  export function matchesStatusFilter(row: AccountRow, statusFilter: string): boolean {
    const filter = statusFilter || STATUS_FILTER_ALL
    if (filter === STATUS_FILTER_ALL) return true
    const accountType = fold(row.account_type.trim())
    switch (filter) {
      case '有 Session':
        return row.hasSession
      // app.py 19148: the Plus filter also admits pro.
      case 'Plus':
        return accountType === 'plus' || accountType === 'pro'
      case 'Team':
        return accountType === 'team'
      case '提链成功':
        return hasLink(row)
      case '失败':
        return containsFailureWord(row.statusText)
      case '待处理':
        return !row.hasSession && !hasLink(row) && !containsFailureWord(row.statusText)
      default:
        return true
    }
  }

  /**
   * `_account_visible_indices`'s tokenizer (app.py 19116): strip, casefold,
   * split on whitespace, drop empties. JS `\s` covers NBSP and U+3000, which
   * Python's `\s` also matches — so no extra class is needed here (Go needs
   * one; see accounts/pyvalue.go reWhitespace).
   */
  export function searchTerms(search: string): string[] {
    return fold(search.trim())
      .split(/\s+/)
      .filter((term) => term !== '')
  }

  /** The AND-ed multi-term match over email + type + status + group (app.py 19122-19133). */
  export function matchesSearch(row: AccountRow, terms: string[]): boolean {
    if (terms.length === 0) return true
    const haystack = fold([row.email, row.account_type, row.statusText, row.group || GROUP_DEFAULT].join(' '))
    return terms.every((term) => haystack.includes(term))
  }

  /** Group predicate (app.py 19120): 全部 passes, otherwise an exact match on the group with its 未分组 fallback. */
  export function matchesGroup(row: AccountRow, group: string): boolean {
    return group === GROUP_ALL || (row.group || GROUP_DEFAULT) === group
  }

  /** `_account_sort_key` (app.py 19098-19106) / accounts.SortKeyOf. */
  function sortKey(row: AccountRow, column: SortColumn): string | number {
    switch (column) {
      case 'type':
        return fold(row.account_type)
      case 'status':
        return fold(row.statusText)
      case 'attempts':
        return row.attempts
      default:
        return fold(row.email)
    }
  }

  /**
   * accounts.SortAccounts. `custom` keeps list order, which is how a manual
   * drag-reorder survives (not ported — UI_SPEC §6 cuts drag-reorder from
   * slice 1 — but the direction still exists as the third state of the
   * heading toggle). The sort is STABLE in both directions: Python's sorted()
   * is stable even with reverse=True, and Array.prototype.sort is required to
   * be stable, so ties keep list order.
   */
  export function sortRows(rows: AccountRow[], column: SortColumn, direction: SortDirection): AccountRow[] {
    if (direction !== 'asc' && direction !== 'desc') return rows
    const sign = direction === 'desc' ? -1 : 1
    return [...rows].sort((a, b) => {
      const ka = sortKey(a, column)
      const kb = sortKey(b, column)
      if (ka === kb) return 0
      return (ka < kb ? -1 : 1) * sign
    })
  }

  /**
   * `_toggle_account_sort` (app.py 19045-19053): a different column, or the
   * same one while custom, goes to asc; asc→desc; desc→custom.
   */
  export function nextSort(
    current: { column: SortColumn; direction: SortDirection },
    clicked: SortColumn,
  ): { column: SortColumn; direction: SortDirection } {
    if (current.column !== clicked || current.direction === 'custom') {
      return { column: clicked, direction: 'asc' }
    }
    if (current.direction === 'asc') return { column: clicked, direction: 'desc' }
    return { column: clicked, direction: 'custom' }
  }
</script>

<script lang="ts">
  /*
    S6 + S8 + S9 账户工作台 — ported from app.py 13664–13675 (常用操作 toolbar)
    and 13816–13893 (account pane header, filters, Treeview). Labels, tooltips
    and warning strings are verbatim from the Tk source.

    TOOLBAR ACTIONS, and where each caption comes from:
      - `导入账号`        app.py:13669 → import_accounts
      - `注册取 Session`  app.py:13670 → start_selected (app.py:15115),
                          `_start_worker(..., collect_session=True)`
                          → StartRegister{collectSession: true}
      - `注册或登录`      app.py:13726 → start_auth_selected (app.py:15131),
                          `_start_worker(..., collect_session=False)`
                          → StartRegister{collectSession: false}
      - `重新获取`        app.py:13746 → refetch_selected_link (app.py:15205)
                          → GenerateLinks
      - `停止`            app.py:13675 → stop_current_task (app.py:15091)
                          → StopAll
    All four act on the TABLE SELECTION, as their Tk originals do
    (`self.account_list.selection()`), and the first three fan the selection out
    one job per address — bindings.go:446, batch orchestration is gap G7.

    Two deliberate divergences:

    1. The label on the link button is `重新获取` (app.py:13746), NOT the
       `批量提链` that UI_SPEC §6's toolbar row asks for. §6 predates the
       bindings; `GenerateLinks` runs `Worker.Relink`, which is §2 #51 重新获取
       — re-login on the register proxy, carry the state to the extract proxy,
       pull one fresh link. 批量提链 is §2 #50, a different Python function
       (`generate_links_from_selected_sessions`, app.py:13673) that reuses the
       stored Session without logging in, zeroes `link_attempt_counts` and races
       a proxy triple per attempt — all of which is gap G7 and unbound.
       bindings.go:487 states the same thing from the Go side. app.py wins over
       §6: a button captioned 批量提链 that silently re-logs in and burns a
       payment link per click would be the worst kind of wrong label.

    2. `注册或登录` and `重新获取` sit on the 全部操作 tabs in Tk (13726 / 13746),
       not on the 常用操作 quick bar. §6 does not build the 全部操作 catalogue
       page, and both have real bindings, so they are surfaced on this toolbar
       rather than left unreachable. Captions and tooltips are still the ones
       from their Tk home.

    NO PER-ROW BUTTONS. Tk has none — its Treeview rows carry a context menu
    (S27, cut by §6) and nothing else — and a spending button repeated on every
    row of a scrolling list in a webview is precisely the misclick this port
    guards against. The three spending actions are also gated by a confirmation
    the Tk app does not have; see lib/ConfirmAction.svelte for why.

    §6 trims the pane to: the 4-button toolbar, the 4-column table, the 分组
    filter and 搜索. CUT here (all still in app.py — restore when their
    bindings land):
      - toolbar `刷新 Session`, `检测试用`, `导出 ZIP` (13671–13674) — G16/G15/G23;
      - toolbar `批量提链` (13673) — G7, see divergence 1 above;
      - S7 任务参数 (支付模式 / 目标金额 / 提链重试 / 认证并发 / 无头浏览器):
        §6 keeps it, but nothing READS those five yet — the register/link flows
        that consume payment_mode, headless and link_attempt_limit are G7, so
        the form would persist values that change no behaviour;
      - `分组管理` menubutton → 新建/重命名/删除分组 (13845–13851) — §6: "no group CRUD";
      - the `可用操作` context strip (13863–13867, specified in §7.2);
      - the right-click context menu (S27), middle-drag reorder and the
        rubber-band drag select (13895–13901) — §6: "no context menu, no
        drag-reorder".
    Sorting is NOT cut: §6 says "no sorting" but §7.1 documents the heading
    arrows in detail and the toggle is eight lines of pure function, so it is
    ported rather than stubbed.

    The 运行环境 / 状态概览 cards below the table are this port's own — they show
    which state.json is being driven, which is worth keeping while the app is
    still pointed at the Python tool's live data. S13's log panes are NOT here:
    they moved into JobPane.svelte, which owns the 任务 list and routes each line
    to the job that produced it — this pane is about accounts.

    No binding is imported here: data arrives through props and changes leave
    through callbacks, so the shell owns every call and this pane stays a pure
    view. The four persisted view fields (分组 / 状态 / sort column / direction,
    UI_SPEC §3) live in the shell for the same reason — it is what writes them
    back through SaveSettings.
  */

  import type { Env, StateSummary } from '../api'

  let {
    rows,
    total,
    knownGroups = [],
    selected,
    group,
    status,
    sortColumn,
    sortDirection,
    accountsError = '',
    accountsLoading = false,
    busy = false,
    env,
    summary,
    onselect,
    ongroupchange,
    onstatuschange,
    onsortchange,
    onvisiblechange,
    onreload,
    onimport,
    onregister,
    onauthonly,
    onrelink,
    onstop,
  }: {
    /** Every account, unsorted and unfiltered, exactly as ListAccounts returned it. */
    rows: AccountRow[]
    /** `AccountPage.total` — the 账户 N of the summary line. */
    total: number
    /** `AccountPage.groups`: settings.account_groups folded with the groups in use. */
    knownGroups?: string[]
    /** Selected row keys (lowercased emails). Shared with the right-hand dock (§0.4). */
    selected: string[]
    /** `account_group_filter`. Owned by the shell because 导入账号 needs it too. */
    group: string
    /** `account_status_filter` — one of STATUS_FILTER_OPTIONS. */
    status: string
    /** `account_sort_column`. */
    sortColumn: SortColumn
    /** `account_sort_direction`. */
    sortDirection: SortDirection
    /** Non-empty when ListAccounts failed. */
    accountsError?: string
    accountsLoading?: boolean
    /** A task is running: the money buttons are disabled, 停止 is not. */
    busy?: boolean
    env: Env | null
    summary: StateSummary | null
    /** Replaces the whole selection (extended-select is computed here). */
    onselect: (keys: string[]) => void
    /** Each of these three persists — the shell debounces nothing, they are click-driven. */
    ongroupchange: (group: string) => void
    onstatuschange: (status: string) => void
    onsortchange: (column: SortColumn, direction: SortDirection) => void
    /** Reports the 显示 N count up for the sidebar's account_summary_var. */
    onvisiblechange: (count: number) => void
    /** Retry after a ListAccounts failure. */
    onreload: () => void
    onimport: () => void
    /*
      The three spending actions. Each hands UP the selected addresses and does
      nothing else — the shell raises the confirmation and only then calls a
      binding, so no click inside this component can reach money on its own.
    */
    /** `注册取 Session` → StartRegister{collectSession: true}. */
    onregister: (emails: string[]) => void
    /** `注册或登录` → StartRegister{collectSession: false}. */
    onauthonly: (emails: string[]) => void
    /** `重新获取` → GenerateLinks (Worker.Relink). */
    onrelink: (emails: string[]) => void
    onstop: () => void
  } = $props()

  // The one view field app.py does NOT persist (§3 has no account_search key),
  // so it is the one that stays local.
  let search = $state('')
  /** Anchor row for shift-range selection, as an index into `visible`. */
  let anchor = $state<number | null>(null)

  // app.py 13831 seeds the combo with [全部, 未分组] and adds the persisted
  // account_groups (which AccountPage.groups carries). The groups actually in
  // use are folded in too, so a row can never sit in a group the combo cannot
  // reach.
  let groups = $derived.by(() => {
    const seen = new Set<string>([GROUP_ALL, GROUP_DEFAULT])
    const out = [GROUP_ALL, GROUP_DEFAULT]
    for (const name of [...knownGroups, ...rows.map((row) => row.group || GROUP_DEFAULT)]) {
      if (name !== '' && !seen.has(name)) {
        seen.add(name)
        out.push(name)
      }
    }
    // A saved filter naming a group nobody is in must still show, or the combo
    // would silently jump to another value.
    if (!seen.has(group)) out.push(group)
    return out
  })

  let terms = $derived(searchTerms(search))
  let visible = $derived(
    sortRows(
      rows.filter((row) => matchesGroup(row, group) && matchesStatusFilter(row, status) && matchesSearch(row, terms)),
      sortColumn,
      sortDirection,
    ),
  )
  let selectedKeys = $derived(new Set(selected))
  let visibleSelectedCount = $derived(visible.filter((row) => selectedKeys.has(row.key)).length)
  let allVisibleSelected = $derived(visible.length > 0 && visibleSelectedCount === visible.length)

  // app.py 19563. The same string feeds the sidebar label (12773) and this
  // header (13821), which is why the count is reported upward.
  let accountSummary = $derived(`账户 ${total} · 显示 ${visible.length} · 已选 ${selected.length}`)

  $effect(() => {
    onvisiblechange(visible.length)
  })

  /** Emails of the current selection, in visible order — what the task bindings take. */
  let selectedEmails = $derived(visible.filter((row) => selectedKeys.has(row.key)).map((row) => row.email))

  function heading(column: SortColumn): string {
    // app.py 19039-19042: the arrow is appended to the active column only.
    if (sortColumn !== column) return SORT_LABELS[column]
    if (sortDirection === 'asc') return `${SORT_LABELS[column]}↑`
    if (sortDirection === 'desc') return `${SORT_LABELS[column]}↓`
    return SORT_LABELS[column]
  }

  function toggleSort(column: SortColumn) {
    const next = nextSort({ column: sortColumn, direction: sortDirection }, column)
    onsortchange(next.column, next.direction)
  }

  /** Tk `selectmode="extended"`: plain click replaces, ctrl toggles, shift extends. */
  function selectRow(index: number, modifiers: { ctrl: boolean; shift: boolean }) {
    const row = visible[index]
    if (!row) return
    if (modifiers.shift && anchor !== null) {
      const from = Math.min(anchor, index)
      const to = Math.max(anchor, index)
      onselect(visible.slice(from, to + 1).map((item) => item.key))
      return
    }
    if (modifiers.ctrl) {
      const next = new Set(selected)
      if (next.has(row.key)) next.delete(row.key)
      else next.add(row.key)
      anchor = index
      onselect([...next])
      return
    }
    anchor = index
    onselect([row.key])
  }

  function toggleAllVisible() {
    anchor = null
    onselect(allVisibleSelected ? [] : visible.map((row) => row.key))
  }
</script>

<section class="card toolbar">
  <h2>常用操作</h2>
  <div class="row">
    <!-- Captions and title strings are the Tk labels and tooltips, verbatim
         (§0.7). `请先选中邮箱` is app.py 15118/15134/15208's own warning, shown
         as the reason the button is disabled instead of as a popup after the
         click. -->
    <button disabled={busy} title="把导入框中的邮箱加入当前分组。" onclick={onimport}>导入账号</button>

    <!-- app.py:13670 → start_selected (15115). -->
    <button
      class="primary"
      disabled={busy || selectedEmails.length === 0}
      title={selectedEmails.length === 0 ? '请先选中邮箱' : '完成选中账号认证并保存 Session/Access Token。'}
      onclick={() => onregister(selectedEmails)}>注册取 Session</button
    >

    <!-- app.py:13726 → start_auth_selected (15131). -->
    <button
      disabled={busy || selectedEmails.length === 0}
      title={selectedEmails.length === 0 ? '请先选中邮箱' : '完成选中账号认证并保留浏览器，不获取 Session。'}
      onclick={() => onauthonly(selectedEmails)}>注册或登录</button
    >

    <!-- app.py:13746 → refetch_selected_link (15205). -->
    <button
      disabled={busy || selectedEmails.length === 0}
      title={selectedEmails.length === 0 ? '请先选中邮箱' : '重新登录选中账号并获取支付链接。'}
      onclick={() => onrelink(selectedEmails)}>重新获取</button
    >

    <!-- app.py:13675 → stop_current_task (15091). Never disabled: the Tk button
         is not either, and it is what a user reaches for when they are not sure
         what is running. StopAll is a no-op with nothing in flight. -->
    <button class="danger spacer-left" title="停止当前注册、提链或支付窗口任务。" onclick={onstop}>停止</button>
  </div>
</section>

<section class="card accounts">
  <header class="account-header">
    <h2>账户</h2>
    <span class="muted">{accountSummary}</span>
  </header>

  <!-- app.py 13823-13861: 分组 combo, 状态 combo, then the 搜索 row. -->
  <div class="row">
    <label for="account-group">分组</label>
    <select id="account-group" class="w14" value={group} onchange={(e) => ongroupchange(e.currentTarget.value)}>
      {#each groups as name (name)}
        <option value={name}>{name}</option>
      {/each}
    </select>

    <label for="account-status">状态</label>
    <select id="account-status" class="w12" value={status} onchange={(e) => onstatuschange(e.currentTarget.value)}>
      {#each STATUS_FILTER_OPTIONS as option (option)}
        <option value={option}>{option}</option>
      {/each}
    </select>
  </div>

  <div class="row">
    <label for="account-search">搜索</label>
    <!-- Tk debounces this by 120 ms; filtering an in-memory array does not need it. -->
    <input id="account-search" class="grow" bind:value={search} />
    <button class="icon" title="清空账户搜索条件。" onclick={() => (search = '')}>×</button>
  </div>

  {#if accountsError}
    <p class="banner err">{accountsError}</p>
    <div class="row end">
      <button onclick={onreload}>重试</button>
    </div>
  {/if}

  <div class="table-wrap">
    <table>
      <colgroup>
        <col style="width: 34px" />
        {#each SORT_COLUMNS as column (column)}
          <col style={`width: ${SORT_WIDTHS[column]}px`} />
        {/each}
      </colgroup>
      <thead>
        <tr>
          <th class="pick">
            <input
              type="checkbox"
              checked={allVisibleSelected}
              disabled={visible.length === 0}
              title="选中/取消当前列表中的全部账户。"
              onchange={toggleAllVisible}
            />
          </th>
          {#each SORT_COLUMNS as column (column)}
            <th class:num={column === 'attempts'}>
              <button class="sort" onclick={() => toggleSort(column)}>{heading(column)}</button>
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each visible as row, index (row.key)}
          {@const isSelected = selectedKeys.has(row.key)}
          <!-- A table row is not an interactive element, but Tk's Treeview row
               is; the checkbox in the first cell is the accessible equivalent
               and the row click is the mouse affordance on top of it. -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions a11y_no_noninteractive_tabindex -->
          <tr
            class:selected={isSelected}
            tabindex="0"
            onclick={(e) => selectRow(index, { ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey })}
            onkeydown={(e) => {
              if (e.key !== 'Enter' && e.key !== ' ') return
              e.preventDefault()
              selectRow(index, { ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey })
            }}
          >
            <td class="pick">
              <input type="checkbox" checked={isSelected} tabindex="-1" aria-label={row.email} onclick={(e) => e.stopPropagation()} onchange={() => selectRow(index, { ctrl: true, shift: false })} />
            </td>
            <td title={row.email}>{row.email}</td>
            <td>{row.account_type}</td>
            <td title={row.statusText}>{row.statusText}</td>
            <td class="num">{row.attempts}</td>
          </tr>
        {/each}
      </tbody>
    </table>

    {#if visible.length === 0}
      <p class="empty muted">
        {#if accountsLoading}
          读取中…
        {:else if accountsError}
          账户列表未加载。
        {:else if rows.length === 0}
          请先导入邮箱
        {:else}
          当前分组没有可见邮箱
        {/if}
      </p>
    {/if}
  </div>
</section>

<section class="card">
  <h2>运行环境</h2>
  {#if env}
    <dl>
      <dt>Go</dt>
      <dd>{env.goVersion}</dd>
      <dt>state.json</dt>
      <dd class:err={!env.stateOK}>{env.stateFile} {env.stateOK ? '✓' : '✗ 不可读'}</dd>
      <dt>数据目录</dt>
      <dd>{env.dataDir}</dd>
    </dl>
  {:else}
    <p class="muted">读取中…</p>
  {/if}
</section>

<section class="card">
  <h2>状态概览</h2>
  {#if summary}
    <dl>
      <dt>账号</dt>
      <dd>{summary.accounts}</dd>
      <dt>Session</dt>
      <dd>{summary.sessions}</dd>
      <dt>设置项</dt>
      <dd>{summary.settingsKeys.length}</dd>
    </dl>
  {:else}
    <p class="muted">读取中…</p>
  {/if}
</section>

<style>
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
  }
  .card.toolbar,
  .card.accounts {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  /* The table must not push the job pane off-screen on a short window. */
  .card.accounts {
    flex: 2 1 260px;
    min-height: 200px;
  }
  h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0 0 10px;
  }
  .card.toolbar h2,
  .account-header h2 {
    margin: 0;
  }
  .account-header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  .row.end {
    justify-content: flex-end;
  }
  /* app.py packs 停止 with side=RIGHT, alone against the other buttons. */
  .spacer-left {
    margin-left: auto;
  }
  .grow {
    flex: 1;
    min-width: 120px;
  }
  .w14 {
    width: 16ch;
  }
  .w12 {
    width: 14ch;
  }
  button.icon {
    padding: 4px 8px;
  }

  .table-wrap {
    flex: 1;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--panel);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  thead th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--head-bg);
    color: var(--head-fg);
    font-weight: 600;
    text-align: left;
    padding: 0;
    border-bottom: 1px solid var(--border);
    height: var(--row-h);
  }
  th.num,
  td.num {
    text-align: center;
  }
  button.sort {
    width: 100%;
    height: 100%;
    background: transparent;
    border: none;
    border-radius: 0;
    padding: 0 8px;
    text-align: inherit;
    color: inherit;
    font-weight: inherit;
  }
  button.sort:hover {
    background: var(--toolbar);
  }
  tbody tr {
    height: var(--row-h);
    cursor: default;
  }
  tbody tr:hover {
    background: var(--head-bg);
  }
  tbody tr.selected {
    background: var(--sel-bg);
    color: var(--sel-fg);
  }
  td {
    padding: 0 8px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  th.pick,
  td.pick {
    padding: 0;
    text-align: center;
  }
  .empty {
    margin: 0;
    padding: 12px;
    text-align: center;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin: 0;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .banner {
    margin: 0;
    padding: 8px 12px;
    border-radius: 4px;
    background: #fee4e2;
    border: 1px solid #fda29b;
  }
  .err {
    color: var(--err);
  }
</style>
