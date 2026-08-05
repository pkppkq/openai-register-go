/*
  任务 pane helpers.

  None of this is a port of app.py — the Tk app has no job list, one global
  `self.running` flag and one taskbar label (app.py 18625). It is a port of
  internal/ui/jobs.go, so the anchors are that file plus the Tk captions the
  labels quote. What is pinned:

    - the two label tables cover exactly the kinds and statuses jobs.go can
      emit, so the pane never renders a bare wire value where a Chinese caption
      belongs;
    - `routeLogLine` splits on the `[job-N] ` prefix jobs.go:151 writes and on
      nothing else, because a false positive routes a global line into one job's
      pane and a false negative loses a worker line into the global box;
    - `sortJobsNewestFirst` orders on the numeric sequence, not on the RFC3339
      timestamp, and does not disturb the caller's array.
*/
import { describe, expect, it } from 'vitest'
import {
  JOB_KIND_LABELS,
  JOB_STATUS_LABELS,
  LOG_JOB_PREFIX,
  clockOf,
  jobSeq,
  routeLogLine,
  sortJobsNewestFirst,
} from '../pages/JobPane.svelte'
import type { JobKind, JobStatus, JobView } from '../api'

/** A JobView with only the fields the sort reads; the rest are placeholders. */
function job(id: string, over: Partial<JobView> = {}): JobView {
  return {
    id,
    kind: 'register',
    email: `${id}@example.com`,
    status: 'running',
    started: '2026-07-26T10:00:00+08:00',
    finished: '',
    error: '',
    ...over,
  } as JobView
}

describe('JOB_KIND_LABELS', () => {
  it('names every ui.JobKind, so no run shows its wire value', () => {
    // jobs.go:36-42. The union in api.ts is the same five; `satisfies` here
    // makes the two lists fail together rather than drift.
    const kinds = ['register', 'auth_only', 'team', 'register_and_rt', 'relink'] as const satisfies readonly JobKind[]
    expect(Object.keys(JOB_KIND_LABELS).sort()).toEqual([...kinds].sort())
  })

  it('quotes the Tk button that starts each flow', () => {
    // app.py 13670 / 13726 / 13736 / 13746. A job's kind is the only thing
    // telling two runs on one address apart, so it is shown as the caption the
    // user actually clicked.
    expect(JOB_KIND_LABELS.register).toBe('注册取 Session')
    expect(JOB_KIND_LABELS.auth_only).toBe('注册或登录')
    expect(JOB_KIND_LABELS.register_and_rt).toBe('域名邮箱随机取RT')
    expect(JOB_KIND_LABELS.relink).toBe('重新获取')
  })

  it('marks team as 注册取 Session dispatched onto the Team flow', () => {
    // app.py:17718 dispatches a Team account off the same 注册取 Session button,
    // so the label has to say both.
    expect(JOB_KIND_LABELS.team).toBe('注册取 Session（Team）')
    expect(JOB_KIND_LABELS.team.startsWith(JOB_KIND_LABELS.register)).toBe(true)
  })
})

describe('JOB_STATUS_LABELS', () => {
  it('names every ui.JobStatus', () => {
    const statuses = ['running', 'succeeded', 'failed', 'cancelled'] as const satisfies readonly JobStatus[]
    expect(Object.keys(JOB_STATUS_LABELS).sort()).toEqual([...statuses].sort())
  })

  it('uses app.py status vocabulary rather than translating the Go lifecycle', () => {
    expect(JOB_STATUS_LABELS).toEqual({
      running: '处理中',
      succeeded: '成功',
      failed: '失败',
      cancelled: '已取消',
    })
  })

  it('keeps 失败 spelled the way the workbench failure filter looks for', () => {
    // Workbench's FAILURE_WORDS (app.py:19153) is a substring test over the
    // rendered status. Nothing routes a JobView through it today, but the two
    // vocabularies have to stay one vocabulary.
    expect(JOB_STATUS_LABELS.failed).toBe('失败')
  })
})

describe('LOG_JOB_PREFIX', () => {
  it('is anchored and carries no global flag, so exec() cannot go stateful', () => {
    // A /g regex keeps `lastIndex` between calls, and routeLogLine calls exec on
    // a shared instance once per log line — with /g it would match every other
    // line. The anchor is the other half: see routeLogLine's tests.
    expect(LOG_JOB_PREFIX.global).toBe(false)
    expect(LOG_JOB_PREFIX.source.startsWith('^')).toBe(true)
  })

  it('stays put across repeated matches', () => {
    const line = '[job-1] x'
    expect(LOG_JOB_PREFIX.exec(line)?.[1]).toBe('job-1')
    expect(LOG_JOB_PREFIX.exec(line)?.[1]).toBe('job-1')
  })
})

describe('routeLogLine', () => {
  it('peels the [job-N] prefix jobs.go:151 writes', () => {
    expect(routeLogLine('[job-1] 开始注册')).toEqual({ jobId: 'job-1', text: '开始注册' })
    expect(routeLogLine('[job-42] x')).toEqual({ jobId: 'job-42', text: 'x' })
  })

  it('treats an unprefixed line as global', () => {
    // StopAll's `已请求停止当前任务` (bindings.go:555) belongs to no job.
    expect(routeLogLine('已请求停止当前任务')).toEqual({ jobId: null, text: '已请求停止当前任务' })
    expect(routeLogLine('')).toEqual({ jobId: null, text: '' })
  })

  it('anchors at the start, so a job id inside the text is not a route', () => {
    // The failure this prevents: a worker line quoting another job's id would
    // otherwise be filed under that job.
    const line = '重试 [job-2] 失败'
    expect(routeLogLine(line)).toEqual({ jobId: null, text: line })
  })

  it('only matches jobs.go:132 id shape, job-<digits>', () => {
    for (const line of ['[job-] x', '[job-abc] x', '[JOB-1] x', '[ job-1 ] x', '{job-1} x']) {
      expect(routeLogLine(line).jobId, line).toBeNull()
    }
  })

  it('strips at most one space after the bracket and keeps the rest of the text verbatim', () => {
    // The prefix is `[id] ` with one space; leading indentation in the worker's
    // own line is content and must survive.
    expect(routeLogLine('[job-1]  两个空格')).toEqual({ jobId: 'job-1', text: ' 两个空格' })
    expect(routeLogLine('[job-1]')).toEqual({ jobId: 'job-1', text: '' })
    expect(routeLogLine('[job-1] a [job-2] b')).toEqual({ jobId: 'job-1', text: 'a [job-2] b' })
  })
})

describe('jobSeq', () => {
  it('reads the sequence out of a jobs.go:132 id', () => {
    expect(jobSeq('job-0')).toBe(0)
    expect(jobSeq('job-7')).toBe(7)
    expect(jobSeq('job-1024')).toBe(1024)
  })

  it('sorts anything unrecognisable to the bottom rather than to NaN', () => {
    // A NaN comparator return is a silent no-op in Array.sort; -1 is an order.
    for (const id of ['', 'job-', 'job-x', 'JOB-1', 'job-1x', ' job-1']) {
      expect(jobSeq(id), id).toBe(-1)
    }
  })
})

describe('sortJobsNewestFirst', () => {
  it('orders by sequence descending, matching ListJobs (jobs.go:289)', () => {
    const sorted = sortJobsNewestFirst([job('job-2'), job('job-10'), job('job-1')])
    expect(sorted.map((entry) => entry.id)).toEqual(['job-10', 'job-2', 'job-1'])
  })

  it('sorts numerically, not lexically', () => {
    // The bug this catches: 'job-10' < 'job-2' as strings, so a string sort
    // would bury the tenth job of a batch under the second.
    const sorted = sortJobsNewestFirst([job('job-9'), job('job-10')])
    expect(sorted.map((entry) => entry.id)).toEqual(['job-10', 'job-9'])
  })

  it('ignores the timestamp, because a fanned-out batch shares one second', () => {
    // RFC3339 with second precision: every child of one 批量注册 click stamps
    // the same value, which is why the Go registry sorts on seq too.
    const same = '2026-07-26T10:00:00+08:00'
    const sorted = sortJobsNewestFirst([
      job('job-1', { started: same }),
      job('job-3', { started: same }),
      job('job-2', { started: same }),
    ])
    expect(sorted.map((entry) => entry.id)).toEqual(['job-3', 'job-2', 'job-1'])
  })

  it('does not mutate the caller array', () => {
    // It is called from a $derived over a prop; sorting in place would write
    // through to the shell's state.
    const input = [job('job-1'), job('job-3'), job('job-2')]
    const before = input.map((entry) => entry.id)
    sortJobsNewestFirst(input)
    expect(input.map((entry) => entry.id)).toEqual(before)
  })

  it('handles the empty list the pane starts with', () => {
    expect(sortJobsNewestFirst([])).toEqual([])
  })
})

describe('clockOf', () => {
  it('renders HH:MM:SS with padding', () => {
    // Built from the LOCAL time of the stamp, so the offset is pinned into the
    // input rather than left to the runner's zone.
    const at = new Date('2026-07-26T10:00:00Z')
    const pad = (n: number) => String(n).padStart(2, '0')
    expect(clockOf(at.toISOString())).toBe(
      `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`,
    )
  })

  it('shows a dash for a job that has not finished', () => {
    // JobView.finished is '' while running; `Invalid Date` in the 结束 tooltip
    // would read as a bug in the job rather than an unfinished job.
    expect(clockOf('')).toBe('—')
    expect(clockOf('   ')).toBe('—')
  })

  it('echoes an unparseable stamp instead of rendering Invalid Date', () => {
    expect(clockOf('not-a-time')).toBe('not-a-time')
  })
})
