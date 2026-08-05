/*
  S25 导出预览 — the ZIP member list.

  The dialog itself is a port of `_preview_and_save_text` (app.py:24055-24095),
  but its member list is not: Tk's ZIP export skips the preview entirely
  (app.py:24386 goes straight to `asksaveasfilename`), so there is no Tk
  behaviour to match and the cap is the port's own. What is pinned is the same
  invariant as ConfirmAction's — the count on screen and the list on screen
  cannot disagree about how many files the archive will hold.
*/
import { describe, expect, it } from 'vitest'
import { MAX_LISTED_ENTRIES, listEntries } from '../ExportPreviewDialog.svelte'
import { MAX_LISTED_EMAILS } from '../ConfirmAction.svelte'

/** N ZIP member names, as session_conversion_zip_entry_name would build them. */
function entries(n: number): string[] {
  return Array.from({ length: n }, (_, i) => `user${i + 1}-sub2api.json`)
}

describe('MAX_LISTED_ENTRIES', () => {
  it('is twelve', () => {
    expect(MAX_LISTED_ENTRIES).toBe(12)
  })

  it('is larger than the confirmation dialog cap, because this list costs nothing', () => {
    // A wider dialog, and no money rides on reading it — the export is already
    // decided by the time the member names are shown.
    expect(MAX_LISTED_ENTRIES).toBeGreaterThan(MAX_LISTED_EMAILS)
  })
})

describe('listEntries', () => {
  it('lists everything and reports no overflow at or below the cap', () => {
    for (const n of [0, 1, 11, MAX_LISTED_ENTRIES]) {
      const { listed, overflow } = listEntries(entries(n))
      expect(listed, `${n} entries`).toEqual(entries(n))
      expect(overflow, `${n} entries`).toBe(0)
    }
  })

  it('reports the exact remainder above the cap', () => {
    expect(listEntries(entries(13)).overflow).toBe(1)
    expect(listEntries(entries(50)).overflow).toBe(38)
  })

  it('keeps listed + overflow equal to the archive size', () => {
    for (const n of [0, 12, 13, 99]) {
      const { listed, overflow } = listEntries(entries(n))
      expect(listed.length + overflow, `${n} entries`).toBe(n)
    }
  })

  it('never lists more than the cap and never goes negative', () => {
    for (const n of [0, 12, 13, 300]) {
      const { listed, overflow } = listEntries(entries(n))
      expect(listed.length).toBeLessThanOrEqual(MAX_LISTED_ENTRIES)
      expect(overflow).toBeGreaterThanOrEqual(0)
    }
  })

  it('does not alias the preview array', () => {
    const names = entries(20)
    expect(listEntries(names).listed).not.toBe(names)
  })
})
