/*
  S9 账户工作台 — the filter, search and sort half.

  This is the densest port in the frontend and the one with the most ways to be
  subtly wrong, so almost nothing here is asserted against a hand-written
  expectation. `oracle.rows` is a table of ten accounts as app.py's own
  `_account_status_text` / `session_results` / `results` derived them, and
  `oracle.visible` / `oracle.sorted` / `oracle.toggle` are what
  `_account_visible_indices` (19108), `_account_display_indices` (19726) and
  `_toggle_account_sort` (19045) did to that table under CPython.

  The composition matters as much as the pieces. Python evaluates group AND
  status AND search as one comprehension; the port splits it into three exported
  predicates so the pane can reuse them, and the split is only correct if
  re-ANDing them reproduces the comprehension. The first test does exactly that.

  Two known, deliberate divergences that these tests do NOT try to erase:
    - `fold()` is `.toLowerCase()`, Python is `.casefold()`. They differ on
      ß/ﬁ-class characters and on nothing in an email, an account type or the
      status vocabulary. accounts/pyvalue.go carries a replacer because Go's
      ToLower is weaker still; JS does not need one.
    - string ordering is UTF-16 code-unit in JS and code-point in Python, so a
      sort key containing an astral character could differ. Every value in the
      table — ASCII addresses, ASCII types, CJK status text — is BMP.
*/
import { describe, expect, it } from 'vitest'
import {
  FAILURE_WORDS,
  GROUP_ALL,
  GROUP_DEFAULT,
  SORT_COLUMNS,
  SORT_LABELS,
  SORT_WIDTHS,
  STATUS_FILTER_ALL,
  STATUS_FILTER_OPTIONS,
  containsFailureWord,
  hasLink,
  matchesGroup,
  matchesSearch,
  matchesStatusFilter,
  nextSort,
  searchTerms,
  sortRows,
  type SortColumn,
  type SortDirection,
} from '../pages/Workbench.svelte'
import type { AccountRow } from '../api'
import { accountRow, accountRows, oracle } from './oracle'

/** The three predicates re-ANDed into `_account_visible_indices`'s comprehension. */
function visibleIndices(group: string, statusFilter: string, search: string): number[] {
  const terms = searchTerms(search)
  const out: number[] = []
  accountRows.forEach((row, index) => {
    if (matchesGroup(row, group) && matchesStatusFilter(row, statusFilter) && matchesSearch(row, terms)) {
      out.push(index)
    }
  })
  return out
}

describe('group and status constants', () => {
  it('are app.py 296-297 and 316-325 verbatim', () => {
    expect(GROUP_ALL).toBe(oracle.constants.ACCOUNT_ALL_GROUP)
    expect(GROUP_DEFAULT).toBe(oracle.constants.ACCOUNT_DEFAULT_GROUP)
    expect(STATUS_FILTER_ALL).toBe(oracle.constants.ACCOUNT_STATUS_FILTER_ALL)
  })

  it('keep the status filter options in app.py 317-325 order, which is the combo order', () => {
    expect([...STATUS_FILTER_OPTIONS]).toEqual(oracle.constants.ACCOUNT_STATUS_FILTER_OPTIONS)
  })

  it('lead the status options with 全部状态, the default selection', () => {
    expect(STATUS_FILTER_OPTIONS[0]).toBe(STATUS_FILTER_ALL)
  })

  it('carry app.py 19153 failure words, in order', () => {
    expect(FAILURE_WORDS).toEqual(oracle.constants.FAILURE_WORDS)
  })
})

describe('sort column metadata', () => {
  it('is app.py 309 ACCOUNT_SORT_COLUMNS, left to right', () => {
    expect([...SORT_COLUMNS]).toEqual(oracle.constants.ACCOUNT_SORT_COLUMNS)
  })

  it('labels every column with app.py 310-315, including 撞链次数 rather than 次数', () => {
    // UI_SPEC S9 calls the fourth column 次数; 7.1 corrects it and app.py agrees.
    expect(SORT_LABELS).toEqual(oracle.constants.ACCOUNT_SORT_LABELS)
    expect(SORT_LABELS.attempts).toBe('撞链次数')
  })

  it('keeps app.py 13885-13888 Tk pixel widths', () => {
    expect(SORT_WIDTHS).toEqual(oracle.constants.ACCOUNT_COLUMN_WIDTHS)
  })

  it('has a label and a width for each column and no extras', () => {
    expect(Object.keys(SORT_LABELS).sort()).toEqual([...SORT_COLUMNS].sort())
    expect(Object.keys(SORT_WIDTHS).sort()).toEqual([...SORT_COLUMNS].sort())
  })
})

describe('containsFailureWord', () => {
  it('is a substring test over the rendered status, not an enum test', () => {
    // UI_SPEC 7.1. 登录失败 / 代理耗尽 / 疑似已封禁 are all 失败 to the filter.
    expect(containsFailureWord('登录失败')).toBe(true)
    expect(containsFailureWord('代理耗尽')).toBe(true)
    expect(containsFailureWord('疑似已封禁')).toBe(true)
    expect(containsFailureWord('待处理')).toBe(false)
    expect(containsFailureWord('')).toBe(false)
  })

  it('matches every word in app.py 19153 on its own', () => {
    for (const word of oracle.constants.FAILURE_WORDS) {
      expect(containsFailureWord(`前缀${word}后缀`), word).toBe(true)
    }
  })
})

describe('hasLink', () => {
  it('is the trimmed link being non-empty (accounts.go:182)', () => {
    const base = accountRow(oracle.rows[0])
    expect(hasLink({ ...base, link: 'https://pay/1' })).toBe(true)
    expect(hasLink({ ...base, link: '' })).toBe(false)
    expect(hasLink({ ...base, link: '   ' })).toBe(false)
  })
})

describe('searchTerms', () => {
  it('strips, folds and splits on runs of whitespace (app.py:19116)', () => {
    expect(searchTerms('  AMY  Plus ')).toEqual(['amy', 'plus'])
    expect(searchTerms('a\tb\nc')).toEqual(['a', 'b', 'c'])
  })

  it('yields no terms for blank input, which is how "no filter" is expressed', () => {
    expect(searchTerms('')).toEqual([])
    expect(searchTerms('   ')).toEqual([])
    expect(searchTerms('\t\n')).toEqual([])
  })

  it('treats NBSP and ideographic space as separators, like Python re.split', () => {
    // JS \s covers U+00A0 and U+3000 and so does Python's; Go needed an
    // explicit class (accounts/pyvalue.go reWhitespace), the webview does not.
    expect(searchTerms('a b')).toEqual(['a', 'b'])
    expect(searchTerms('a　b')).toEqual(['a', 'b'])
  })
})

describe('matchesSearch', () => {
  it('ANDs the terms, so order does not matter but every term must hit', () => {
    // app.py 19122-19133 is `all(term in haystack for term in terms)`.
    const amy = accountRows[1]
    expect(matchesSearch(amy, searchTerms('amy plus'))).toBe(true)
    expect(matchesSearch(amy, searchTerms('plus amy'))).toBe(true)
    expect(matchesSearch(amy, searchTerms('amy team'))).toBe(false)
  })

  it('searches email, type, status and group joined by one space', () => {
    const amy = accountRows[1]
    expect(matchesSearch(amy, searchTerms('example.com'))).toBe(true) // email
    expect(matchesSearch(amy, searchTerms('plus'))).toBe(true) // account_type
    expect(matchesSearch(amy, searchTerms('session已获取'))).toBe(true) // statusText
    expect(matchesSearch(amy, searchTerms('组A'))).toBe(true) // group
  })

  it('substitutes 未分组 for an empty group, as the row would display it', () => {
    // app.py 19129 `str(account.group or ACCOUNT_DEFAULT_GROUP)`: an ungrouped
    // account is findable by typing what the table shows.
    const ungrouped = accountRows.find((row) => row.group === '') as AccountRow
    expect(ungrouped).toBeDefined()
    expect(matchesSearch(ungrouped, searchTerms(GROUP_DEFAULT))).toBe(true)
  })

  it('can match across the join, which is a property of the Python haystack too', () => {
    // The four fields are joined before the test, so a term spanning the space
    // between email and type matches. Pinned because it looks like a bug and is
    // not: removing the join would change behaviour away from app.py.
    const amy = accountRows[1]
    expect(matchesSearch(amy, searchTerms('example.com plus'))).toBe(true)
    expect(matchesSearch(amy, [`${amy.email.toLowerCase()} ${amy.account_type.toLowerCase()}`])).toBe(true)
  })

  it('passes everything when there are no terms', () => {
    for (const row of accountRows) expect(matchesSearch(row, [])).toBe(true)
  })

  it('is case-insensitive on both sides', () => {
    const hal = accountRows.find((row) => row.email.startsWith('HAL')) as AccountRow
    expect(matchesSearch(hal, searchTerms('hal'))).toBe(true)
    expect(matchesSearch(hal, searchTerms('HAL'))).toBe(true)
  })
})

describe('matchesStatusFilter', () => {
  it('agrees with _account_matches_status_filter for every row and option', () => {
    // Cross-checked through the composed comprehension below as well; this one
    // isolates the predicate so a failure names the filter.
    for (const c of oracle.visible) {
      if (c.group !== GROUP_ALL || c.search !== '') continue
      const expected = new Set(c.visible)
      accountRows.forEach((row, index) => {
        expect(matchesStatusFilter(row, c.statusFilter), `${c.statusFilter} / ${row.email}`).toBe(
          expected.has(index),
        )
      })
    }
  })

  it('admits pro under the Plus filter (app.py:19148)', () => {
    const pro = accountRows.find((row) => row.account_type === 'pro') as AccountRow
    expect(matchesStatusFilter(pro, 'Plus')).toBe(true)
  })

  it('passes everything for an unknown filter, as app.py 19158 does', () => {
    for (const row of accountRows) {
      expect(matchesStatusFilter(row, '无此过滤器')).toBe(true)
      expect(matchesStatusFilter(row, '')).toBe(true)
    }
  })

  it('reads 待处理 as "no session AND no link AND not failed", not as a status string', () => {
    // The row whose statusText literally IS 待处理 passes; so does an unrelated
    // one with no session, no link and no failure word. A row whose status text
    // is 待获取RT(带授权手机号) also passes — which is the point.
    const pending = accountRows.filter((row) => matchesStatusFilter(row, '待处理'))
    expect(pending.map((row) => row.email)).toContain('HAL@example.com')
    expect(pending.every((row) => !row.hasSession && !hasLink(row) && !containsFailureWord(row.statusText))).toBe(
      true,
    )
  })
})

describe('matchesGroup', () => {
  it('passes everything under 全部', () => {
    for (const row of accountRows) expect(matchesGroup(row, GROUP_ALL)).toBe(true)
  })

  it('folds an empty group into 未分组 (app.py:19120)', () => {
    const ungrouped = accountRows.find((row) => row.group === '') as AccountRow
    expect(matchesGroup(ungrouped, GROUP_DEFAULT)).toBe(true)
    expect(matchesGroup(ungrouped, '组A')).toBe(false)
  })

  it('matches a stored 未分组 and an empty group identically', () => {
    const stored = accountRows.find((row) => row.group === GROUP_DEFAULT) as AccountRow
    const empty = accountRows.find((row) => row.group === '') as AccountRow
    expect(matchesGroup(stored, GROUP_DEFAULT)).toBe(true)
    expect(matchesGroup(empty, GROUP_DEFAULT)).toBe(true)
  })

  it('is an exact match, not a prefix or a fold', () => {
    const grouped = accountRows.find((row) => row.group === '组A') as AccountRow
    expect(matchesGroup(grouped, '组')).toBe(false)
    expect(matchesGroup(grouped, '组A ')).toBe(false)
  })
})

describe('the three predicates re-ANDed', () => {
  it('reproduce _account_visible_indices for the whole corpus', () => {
    for (const c of oracle.visible) {
      expect(
        visibleIndices(c.group, c.statusFilter, c.search),
        `group=${c.group} status=${c.statusFilter} search=${JSON.stringify(c.search)}`,
      ).toEqual(c.visible)
    }
  })

  it('checks a corpus big enough to be worth checking', () => {
    // Guards the loop: 5 groups x 9 status filters, 15 searches, 8 combined.
    expect(oracle.visible.length).toBeGreaterThanOrEqual(60)
  })

  it('keeps app.py list order rather than the filter order', () => {
    // The comprehension enumerates self.accounts, so the visible list is always
    // ascending indices whatever the filters are.
    for (const c of oracle.visible) {
      expect([...c.visible].sort((a, b) => a - b)).toEqual(c.visible)
    }
  })
})

describe('sortRows', () => {
  /** The oracle's expected emails for one column/direction. */
  function expected(column: string, direction: string): string[] {
    const found = oracle.sorted.find((c) => c.column === column && c.direction === direction)
    expect(found, `${column}/${direction} missing from the oracle`).toBeDefined()
    return (found as { emails: string[] }).emails
  }

  it('reproduces _account_display_indices for every column and direction', () => {
    for (const column of SORT_COLUMNS) {
      for (const direction of ['custom', 'asc', 'desc'] as SortDirection[]) {
        const rows = sortRows(accountRows, column, direction)
        expect(rows.map((row) => row.email), `${column}/${direction}`).toEqual(expected(column, direction))
      }
    }
  })

  it('leaves list order alone under custom, which is how a manual reorder survives', () => {
    // app.py 19728 returns the unsorted indices; drag-reorder itself is cut from
    // slice 1 but custom is still the third state of the heading toggle.
    for (const column of SORT_COLUMNS) {
      expect(sortRows(accountRows, column, 'custom')).toBe(accountRows)
    }
  })

  it('falls back to email for a column app.py does not know (19731)', () => {
    // Python coerces an unrecognised sort column to "email"; the TS default arm
    // of sortKey does the same. The cast is what a corrupted persisted value
    // would look like at runtime.
    const rows = sortRows(accountRows, 'nope' as SortColumn, 'asc')
    expect(rows.map((row) => row.email)).toEqual(expected('nope', 'asc'))
    expect(rows.map((row) => row.email)).toEqual(expected('email', 'asc'))
  })

  it('sorts attempts numerically, not as text', () => {
    // 12 must not land between 1 and 2. This is the reason sortKey returns a
    // number for that column instead of folding it like the other three.
    const rows = sortRows(accountRows, 'attempts', 'asc')
    const counts = rows.map((row) => row.attempts)
    expect(counts).toEqual([...counts].sort((a, b) => a - b))
    expect(counts).toContain(12)
  })

  it('is stable in both directions, so ties keep list order', () => {
    // Python's sorted() is stable even with reverse=True and
    // Array.prototype.sort is required to be; a comparator returning ±1 for
    // equal keys would reverse ties and diverge on desc.
    const tied = accountRows.filter((row) => row.attempts === 0).map((row) => row.email)
    for (const direction of ['asc', 'desc'] as SortDirection[]) {
      const seen = sortRows(accountRows, 'attempts', direction)
        .filter((row) => row.attempts === 0)
        .map((row) => row.email)
      expect(seen, direction).toEqual(tied)
    }
  })

  it('does not mutate the caller array when it does sort', () => {
    const input = [...accountRows]
    const before = input.map((row) => row.email)
    sortRows(input, 'email', 'desc')
    expect(input.map((row) => row.email)).toEqual(before)
  })

  it('folds case in the three string columns', () => {
    // HAL@example.com sorts among the lowercase addresses, not before them all
    // the way an unfolded UTF-16 compare would put it.
    const emails = sortRows(accountRows, 'email', 'asc').map((row) => row.email)
    expect(emails.indexOf('HAL@example.com')).toBeGreaterThan(emails.indexOf('gus@example.com'))
  })
})

describe('nextSort', () => {
  it('reproduces _toggle_account_sort for every state and click', () => {
    for (const c of oracle.toggle) {
      const from = { column: c.from.column as SortColumn, direction: c.from.direction as SortDirection }
      expect(nextSort(from, c.clicked as SortColumn), `${c.from.column}/${c.from.direction} -> ${c.clicked}`).toEqual(
        { column: c.to.column as SortColumn, direction: c.to.direction as SortDirection },
      )
    }
  })

  it('cycles asc -> desc -> custom on the same heading', () => {
    let state = { column: 'email' as SortColumn, direction: 'custom' as SortDirection }
    state = nextSort(state, 'email')
    expect(state).toEqual({ column: 'email', direction: 'asc' })
    state = nextSort(state, 'email')
    expect(state).toEqual({ column: 'email', direction: 'desc' })
    state = nextSort(state, 'email')
    expect(state).toEqual({ column: 'email', direction: 'custom' })
  })

  it('starts a different heading at asc whatever the old direction was', () => {
    for (const direction of ['custom', 'asc', 'desc'] as SortDirection[]) {
      expect(nextSort({ column: 'email', direction }, 'status')).toEqual({ column: 'status', direction: 'asc' })
    }
  })

  it('returns a new object rather than editing the current state', () => {
    const state = { column: 'email' as SortColumn, direction: 'asc' as SortDirection }
    const next = nextSort(state, 'email')
    expect(next).not.toBe(state)
    expect(state).toEqual({ column: 'email', direction: 'asc' })
  })
})
