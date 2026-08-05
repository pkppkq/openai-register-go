<script module lang="ts">
  /*
    Constants and pure helpers for the job pane, exported so the shell can reuse
    them (the taskbar renders the same status vocabulary) and so they can be read
    next to their Go twins in internal/ui/jobs.go.
  */
  import type { JobView } from '../api'

  /**
   * `ui.JobKind` → the Tk button that starts that flow, verbatim. A job's kind is
   * the only thing distinguishing two runs on the same address, so it is shown
   * with the caption the user actually clicked rather than the wire value.
   *
   *   register        Worker.Run                      注册取 Session   app.py:13670
   *   auth_only       Worker.RunAuthOnly              注册或登录       app.py:13726
   *   team            Worker.RunTeam                  app.py:17718 dispatches a Team
   *                                                   account off 注册取 Session
   *   register_and_rt Worker.RunRegisterAndAuthorizeRT 域名邮箱随机取RT app.py:13736
   *   relink          Worker.Relink                   重新获取         app.py:13746
   */
  export const JOB_KIND_LABELS: Record<string, string> = {
    register: '注册取 Session',
    auth_only: '注册或登录',
    team: '注册取 Session（Team）',
    register_and_rt: '域名邮箱随机取RT',
    relink: '重新获取',
  }

  /**
   * `ui.JobStatus` (jobs.go:47-52) in app.py's own status vocabulary. JobView
   * carries the lifecycle, not the worker's Chinese per-account status — UI_SPEC
   * §4.2's `status` event is not emitted yet — so these four are a mapping, not
   * an invention.
   */
  export const JOB_STATUS_LABELS: Record<string, string> = {
    running: '处理中',
    succeeded: '成功',
    failed: '失败',
    cancelled: '已取消',
  }

  /**
   * The routing prefix. jobs.go:151-155 wraps every worker line as
   * `[<id>] <line>` and jobs.go:132 builds the id as `job-<seq>`, so this anchors
   * on that exact shape — a line whose own TEXT happens to contain `[job-2]`
   * further along is not a match, and an unprefixed line (StopAll's
   * `已请求停止当前任务`, bindings.go:555) is global by construction.
   */
  export const LOG_JOB_PREFIX = /^\[(job-\d+)\] ?/

  /** Splits a log line into its job id (or null) and the text without the prefix. */
  export function routeLogLine(line: string): { jobId: string | null; text: string } {
    const match = LOG_JOB_PREFIX.exec(line)
    if (!match) return { jobId: null, text: line }
    return { jobId: match[1], text: line.slice(match[0].length) }
  }

  /**
   * Newest first, like ListJobs (jobs.go:289). It sorts on the numeric half of
   * `job-<seq>` rather than on `started`, because the timestamps are RFC3339 with
   * second precision and two jobs fanned out from one click share a second —
   * which is exactly why the Go registry sorts on `seq` too.
   */
  export function jobSeq(id: string): number {
    const match = /^job-(\d+)$/.exec(id)
    return match ? Number(match[1]) : -1
  }

  export function sortJobsNewestFirst(jobs: JobView[]): JobView[] {
    return [...jobs].sort((a, b) => jobSeq(b.id) - jobSeq(a.id))
  }

  /**
   * `HH:MM:SS` out of JobView's RFC3339 stamp. Anything unparseable is shown
   * verbatim rather than as `Invalid Date`, and an empty stamp (a job that has
   * not finished) is a dash.
   */
  export function clockOf(stamp: string): string {
    if (stamp.trim() === '') return '—'
    const at = new Date(stamp)
    if (Number.isNaN(at.getTime())) return stamp
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`
  }
</script>

<script lang="ts">
  /*
    任务 + S13 日志 — the job half of the workbench.

    The Tk app has no job list: one global `self.running` flag, one shared
    `stop_event`, and a taskbar label (app.py 18625 `当前任务：{email} · {status}`).
    That is only workable because `_start_worker` (app.py:16684) refuses to start
    anything while that flag is set, so there is exactly one batch to talk about.
    This port is one job per account (bindings.go:446, batch orchestration is gap
    G7), so a single label cannot name what is running and the list is what
    replaces it. UI_SPEC §4.2 also flags Python's single global stop_event as a
    real bug — the first task to finish clears it for everyone — which is why
    cancellation here is per job as well as global.

    The log half IS a port: app.py 13990-14003 splits the 日志 tab horizontally
    into `选中账户日志：{email}` (left) and `全局日志` (right). The same split is
    kept, with the left pane keyed on the selected JOB rather than the selected
    account — that is the finer key of the two (a job carries its email, an
    address can have several runs over a session) and it is what the backend's
    `[job-N] ` prefix actually identifies.

    CUT from S13 (still in app.py): the three colour tags `log_error #b91c1c`,
    `log_success #15803d`, `log_attention #1d4ed8` and the classifier that assigns
    them (app.py 20107-20132). That is UI_SPEC gap G25 and it is a backend
    decision — classifying here would mean a second, drifting copy of the Python
    keyword lists.

    No binding is imported: jobs and log lines arrive as props, 取消 and 停止 leave
    through callbacks, so the shell owns every call and this pane stays a view.
  */

  let {
    jobs,
    logs,
    oncancel,
    onstop,
  }: {
    /** Every job this session, in any order — the pane sorts newest first itself. */
    jobs: JobView[]
    /** Raw `log` event lines, prefix and all. */
    logs: string[]
    /** `CancelJob(id)` — cancels one run's context (jobs.go:240). */
    oncancel: (id: string) => void
    /** `StopAll()` — app.py 13675 停止 / stop_current_task (app.py:15091). */
    onstop: () => void
  } = $props()

  /** The job whose log fills the left pane; null follows the newest job. */
  let picked = $state<string | null>(null)

  let ordered = $derived(sortJobsNewestFirst(jobs))
  let runningCount = $derived(jobs.filter((job) => job.status === 'running').length)

  /**
   * The focused job. An explicit pick wins as long as that job still exists;
   * otherwise the newest one, so the pane is never blank while something runs.
   */
  let focused = $derived.by(() => {
    const chosen = ordered.find((job) => job.id === picked)
    return chosen ?? ordered[0] ?? null
  })

  /** job id → its lines, prefix stripped. Built once per log change. */
  let byJob = $derived.by(() => {
    const out = new Map<string, string[]>()
    for (const line of logs) {
      const { jobId, text } = routeLogLine(line)
      if (jobId === null) continue
      const bucket = out.get(jobId)
      if (bucket) bucket.push(text)
      else out.set(jobId, [text])
    }
    return out
  })

  let focusedLines = $derived(focused ? (byJob.get(focused.id) ?? []) : [])

  // app.py 13996: `选中账户日志：未选择账户` when nothing is selected.
  let focusedTitle = $derived(focused ? `任务日志：${focused.id} · ${focused.email}` : '任务日志：未选择任务')

  let globalText = $derived(logs.length ? logs.join('\n') : '（暂无）')
  let focusedText = $derived(
    focused === null ? '（暂无）' : focusedLines.length ? focusedLines.join('\n') : '（该任务暂无日志）',
  )

  let focusedBox = $state<HTMLPreElement | null>(null)
  let globalBox = $state<HTMLPreElement | null>(null)

  /**
   * Pin a log pane to its tail, as the Tk widgets do (`see(END)`).
   *
   * `text` is unused on purpose: passing it is what subscribes the calling
   * $effect to new lines, and it must be read as a dependency BEFORE the DOM is
   * measured.
   */
  function follow(box: HTMLPreElement | null, text: string): void {
    if (box === null || text === '') return
    box.scrollTop = box.scrollHeight
  }

  $effect(() => follow(focusedBox, focusedText))
  $effect(() => follow(globalBox, globalText))
</script>

<section class="card jobs">
  <header class="jobs-header">
    <h2>任务</h2>
    <span class="muted">共 {jobs.length} 个 · 运行中 {runningCount}</span>
    <!-- The same StopAll as the toolbar's 停止 (app.py 13675). Duplicated on
         purpose: this pane is where a user watching a run looks first, and
         stop_current_task is safe to call with nothing running. -->
    <button class="danger push-right" title="停止当前注册、提链或支付窗口任务。" onclick={onstop}>停止</button>
  </header>

  <div class="table-wrap">
    <table>
      <colgroup>
        <col style="width: 84px" />
        <col style="width: 170px" />
        <col />
        <col style="width: 76px" />
        <col style="width: 80px" />
        <col style="width: 64px" />
      </colgroup>
      <thead>
        <tr>
          <th>任务</th>
          <th>类型</th>
          <th>邮箱</th>
          <th>状态</th>
          <th>开始</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        {#each ordered as job (job.id)}
          {@const isRunning = job.status === 'running'}
          <tr class:selected={focused?.id === job.id}>
            <td class="pick">
              <button
                class="link"
                title={`只看 ${job.id} 的日志。`}
                onclick={() => (picked = job.id)}
                aria-pressed={focused?.id === job.id}>{job.id}</button
              >
            </td>
            <td title={job.kind}>{JOB_KIND_LABELS[job.kind] ?? job.kind}</td>
            <td title={job.email}>{job.email}</td>
            <td
              class:err={job.status === 'failed'}
              class:ok={job.status === 'succeeded'}
              title={job.finished ? `结束 ${clockOf(job.finished)}` : ''}
              >{JOB_STATUS_LABELS[job.status] ?? job.status}</td
            >
            <td class="num">{clockOf(job.started)}</td>
            <td class="pick">
              <!-- CancelJob refuses a finished job (jobs.go:258 `任务已结束`), so
                   the button is disabled rather than left to fail. -->
              <button
                disabled={!isRunning}
                title={isRunning ? `取消 ${job.id}，结束这次运行。` : '任务已结束。'}
                onclick={() => oncancel(job.id)}>取消</button
              >
            </td>
          </tr>
          {#if job.error}
            <tr class="error-row">
              <td colspan="6" class="err" title={job.error}>{job.error}</td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>

    {#if jobs.length === 0}
      <p class="empty muted">（暂无任务）</p>
    {/if}
  </div>
</section>

<!-- app.py 13990-14003: the 日志 tab is a horizontal split, per-account on the
     left and 全局日志 on the right. -->
<section class="card logs">
  <div class="log-col">
    <h2>{focusedTitle}</h2>
    <pre bind:this={focusedBox} class="mono">{focusedText}</pre>
  </div>
  <div class="log-col">
    <h2>全局日志</h2>
    <pre bind:this={globalBox} class="mono">{globalText}</pre>
  </div>
</section>

<style>
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
  }
  .card.jobs {
    display: flex;
    flex-direction: column;
    gap: 8px;
    flex: 0 1 auto;
    max-height: 260px;
  }
  .card.logs {
    display: flex;
    flex-direction: row;
    gap: 12px;
    flex: 1;
    min-height: 180px;
  }
  .log-col {
    flex: 1 1 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .jobs-header {
    display: flex;
    align-items: baseline;
    gap: 12px;
  }
  .push-right {
    margin-left: auto;
  }

  .table-wrap {
    flex: 1;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
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
    padding: 0 8px;
    border-bottom: 1px solid var(--border);
    height: var(--row-h);
  }
  tbody tr {
    height: var(--row-h);
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
  td.num {
    text-align: center;
  }
  td.pick {
    padding: 0 4px;
    text-align: center;
  }
  tr.error-row td {
    padding-bottom: 4px;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  button.link {
    background: transparent;
    border: none;
    border-radius: 0;
    padding: 0;
    color: var(--primary);
    text-decoration: underline;
  }
  button.link:hover:not(:disabled) {
    background: transparent;
    color: var(--primary-hover);
  }
  .ok {
    color: var(--ok);
  }
  .err {
    color: var(--err);
  }
  .empty {
    margin: 0;
    padding: 12px;
    text-align: center;
  }
  pre {
    flex: 1;
    min-height: 0;
    margin: 0;
    overflow: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 10px;
    font-size: 12px;
    line-height: 1.6;
  }
</style>
