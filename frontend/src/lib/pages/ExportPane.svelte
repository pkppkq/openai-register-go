<script module lang="ts">
  import type { ExportKind } from '../api'

  /** One export button: its Tk caption, what it writes, and its app.py row. */
  export type ExportButton = {
    kind: ExportKind
    /**
     * The Tk BUTTON caption, verbatim — the strings in the 导出转换 group's
     * `_button_grid` call at app.py 13765-13773, not the titles the preview
     * Toplevel shows. The two differ for two of the six (`选中 Raw` opens a
     * dialog titled 导出选中Raw, `邮箱 RT` opens one titled 导出邮箱----RT), and
     * the caption is what a user of the Tk app is looking for on screen.
     */
    label: string
    /** The Tk tooltip / UI_SPEC §5.L effect column. */
    detail: string
    /** app.py line of the HANDLER, so what a button does can be traced. */
    source: string
  }

  /**
   * UI_SPEC §5.L rows 95-102, in the order the 导出转换 group lists them
   * (app.py 13765-13773), with the two entries that are not document builds
   * removed:
   *   - `sub2api` (row 97) refreshes every access token through a proxy chain
   *     before it has anything to write — a background job; see the gate at the
   *     bottom of the pane.
   *   - `复制转换` (row 100) is a clipboard action, not an ExportKind; it goes
   *     through CopySessionConversion.
   * Removing two from the middle is why this list is not literally contiguous
   * with the Tk grid, but the surviving six keep its left-to-right order.
   */
  export const EXPORT_BUTTONS: ExportButton[] = [
    {
      kind: 'authorized',
      label: '已授权',
      detail: '只导出有 openai_rt 的邮箱，同样是 account_export_line 格式',
      source: 'app.py:24098',
    },
    {
      kind: 'email_rt',
      label: '邮箱 RT',
      detail: '每行 {email}----{openai_rt}，只含已授权邮箱',
      source: 'app.py:24118',
    },
    {
      kind: 'sessions',
      label: '选中 Session',
      detail: '{email, session_json} 的 JSON 数组；没有 Session 的邮箱只计数',
      source: 'app.py:24138',
    },
    {
      kind: 'raw',
      label: '选中 Raw',
      detail: '选中的每个邮箱一行 account_export_line，不要求已授权',
      source: 'app.py:24406',
    },
    {
      kind: 'conversion',
      label: '导出转换',
      detail: '按“Session 转换格式”设置转换后导出为 .json',
      source: 'app.py:24355',
    },
    {
      kind: 'conversion_zip',
      label: '导出ZIP',
      detail: '每个账号一个 JSON 打进 session-conversion-{格式}-{时间}.zip',
      source: 'app.py:24377',
    },
  ]
</script>

<script lang="ts">
  /*
    S13 导出 — the export half of the 账户 screen (app.py:24055-24533), UI_SPEC
    gap G23.

    In Tk these are buttons in the 全部操作 catalogue's 导出转换 group (S19), which
    is out of slice 1 as a whole. They get their own pane instead of waiting for
    the catalogue because none of them starts a run, spends money, or touches the
    network: every one is a document built in memory from state.json plus, for
    保存, a single os.WriteFile. That makes them the only 全部操作 entries that are
    safe to reach directly.

    文本类导出先预览再保存；ZIP 对齐 Python，校验后直接打开保存对话框。已授权 /
    邮箱 RT 缺少 RT 时会在显式确认后完成 OAuth，再自动继续原导出；sub2api
    同样先补齐 RT、刷新 Access Token，再显示可信后端文档的预览。

    Pure presentational: props in, callbacks out, no ../wailsjs import.
  */

  let {
    selected,
    busy = false,
    convertFormat = '',
    missing = null,
    sub2api = null,
    onpreview,
    onmissing,
    onsub2api,
  }: {
    /** The workbench selection, lowercased row keys. */
    selected: string[]
    /** A preview/save/copy is in flight. */
    busy?: boolean
    /** `session_convert_format`, shown so 导出转换 is not a blind button. */
    convertFormat?: string
    /** The result of the last ExportMissingRT, or null. */
    missing?: { emails: string[]; prompt: string } | null
    /** The result of the last PrepareSub2APIExport, or null. */
    sub2api?: { emails: string[]; exportEmails: string[] } | null
    onpreview: (kind: ExportKind) => void
    onmissing: () => void
    onsub2api: () => void
  } = $props()

  let empty = $derived(selected.length === 0)
  let disabled = $derived(empty || busy)
</script>

<section class="pane">
  <div class="card">
    <h2>导出</h2>
    <p class="hint">
      导出内容来自本机状态。已授权、邮箱 RT、CPA 和 sub2api 在数据缺失或过期时会先弹出确认，
      再启动联网刷新；其余格式只读取本地数据。已选 {selected.length} 个账号；
      {#if empty}请先在“账户工作台”里选择邮箱。{:else}文本先预览再保存，ZIP 校验后直接选择保存位置。{/if}
    </p>

    <div class="grid">
      {#each EXPORT_BUTTONS as button (button.kind)}
        <button {disabled} title={`${button.detail}（${button.source}）`} onclick={() => onpreview(button.kind)}>
          {button.label}
        </button>
      {/each}
    </div>

    <p class="hint">
      导出转换 / 导出ZIP 使用的格式是设置里的
      <span class="mono">{convertFormat || '（未设置，按 sub2api 处理）'}</span>。
    </p>
  </div>

  <div class="card">
    <h2>需要先授权的账号</h2>
    <p class="hint">
      点击上方“已授权”或“邮箱 RT”时，缺少 RT 的账号会先显示完整确认，再执行 OAuth；
      批量授权成功后会自动回到原导出。下面的按钮只做本地预检，不联网。
    </p>
    <div class="row">
      <button disabled={empty || busy} title="列出选中账号里没有 openai_rt 的那些" onclick={onmissing}>
        检查缺少 RT 的账号
      </button>
    </div>
    {#if missing}
      {#if missing.emails.length === 0}
        <p class="ok">选中的账号都已有 RT，可以直接导出。</p>
      {:else}
        <p class="warn">{missing.prompt}</p>
        <ul class="emails mono">
          {#each missing.emails as email (email)}
            <li>{email}</li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>

  <div class="card">
    <h2>sub2api</h2>
    <p class="hint">
      执行时会先为缺少 RT 的账号完成 OAuth，再通过代理链刷新每个账号的 Access Token；
      完成后显示后端保留文档的预览，确认保存时不会信任或回写 WebView 文本。
    </p>
    <div class="row">
      <button disabled={empty || busy} title="确认后补齐 RT、刷新 Access Token，并显示 sub2api 导出预览" onclick={onsub2api}>
        授权 / 刷新并导出 sub2api
      </button>
    </div>
    {#if sub2api}
      <p class="hint">{sub2api.emails.length} 个已授权账号会进入文件：</p>
      <ul class="emails mono">
        {#each sub2api.exportEmails as email (email)}
          <li>{email}</li>
        {/each}
      </ul>
    {/if}
  </div>
</section>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    gap: 12px;
    overflow: auto;
  }
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
  .grid {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  p {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .hint {
    color: var(--muted);
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--manual);
  }
  .emails {
    max-height: 180px;
    overflow: auto;
    margin: 0;
    list-style: none;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .emails li {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
