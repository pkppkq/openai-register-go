/*
  S13 导出 — the six buttons.

  EXPORT_BUTTONS is a table of Tk captions, so it is checked against the Tk
  captions: `oracle.exportGroup` is the 导出转换 group's own `_button_grid` call
  (app.py 13765-13773), read out of the source by the harness. That distinction
  is what these tests were written to catch and did catch — see below.

  FIXED WHILE WRITING THIS FILE. Two labels were the PREVIEW DIALOG titles, not
  the button captions:
      raw       was 导出选中Raw  (the title at app.py:24412) — the button is 选中 Raw  (13770)
      email_rt  was 邮箱----RT   (the title at app.py:24132) — the button is 邮箱 RT   (13767)
  and the list order did not match the grid order it claimed to follow. Both are
  corrected in ExportPane.svelte. It matters because the pane's whole argument
  for existing outside 全部操作 is that these are the same six actions the Tk
  user already knows; a caption they cannot find in the Tk app is not that.

  Nothing here calls a binding. PreviewExport, SaveExport and the two Copy*
  build a document from state.json in memory — but this file does not reach them
  at all, it only checks the table that decides which kind each button asks for.
*/
import { describe, expect, it } from 'vitest'
import { EXPORT_BUTTONS } from '../pages/ExportPane.svelte'
import type { ExportKind } from '../api'
import { oracle } from './oracle'

/**
 * The six wire values of `ui.ExportKind` (export.go:47-62). Written out so the
 * `satisfies` fails to compile if the union in api.ts changes without this
 * table changing with it — codegen types the argument as a bare string, so
 * nothing else would catch a rename.
 */
const KINDS = ['raw', 'authorized', 'email_rt', 'sessions', 'conversion', 'conversion_zip'] as const satisfies
  readonly ExportKind[]

describe('EXPORT_BUTTONS', () => {
  it('offers each ExportKind exactly once', () => {
    expect(EXPORT_BUTTONS).toHaveLength(6)
    expect(EXPORT_BUTTONS.map((button) => button.kind).sort()).toEqual([...KINDS].sort())
  })

  it('has no duplicate kinds, because kind is the each-block key and the call argument', () => {
    const kinds = EXPORT_BUTTONS.map((button) => button.kind)
    expect(new Set(kinds).size).toBe(kinds.length)
  })

  it('labels every button with its verbatim Tk caption from app.py 13765-13773', () => {
    // The assertion that found the two wrong labels. `oracle.exportGroup` is
    // the grid call itself, so this cannot be satisfied by a title, a tooltip
    // or a paraphrase.
    const captions = new Set(oracle.exportGroup.map((row) => row.label))
    for (const button of EXPORT_BUTTONS) {
      expect(captions, `${button.kind}: ${button.label}`).toContain(button.label)
    }
  })

  it('keeps the 导出转换 grid order for the six it ports', () => {
    const ported = new Set(EXPORT_BUTTONS.map((button) => button.label))
    expect(EXPORT_BUTTONS.map((button) => button.label)).toEqual(
      oracle.exportGroup.map((row) => row.label).filter((label) => ported.has(label)),
    )
  })

  it('leaves out exactly the two grid entries that are not a document build', () => {
    // sub2api is a background job (it refreshes every access token through a
    // proxy chain first) and 复制转换 is a clipboard action with no ExportKind.
    // Everything else in the group is safe to reach: memory plus one file write.
    const missing = oracle.exportGroup
      .map((row) => row.label)
      .filter((label) => !EXPORT_BUTTONS.some((button) => button.label === label))
    expect(missing).toEqual(['sub2api', '复制转换'])
  })

  it('cites the app.py line the handler is defined on', () => {
    // The line is rendered into each button's title attribute, so a wrong one
    // sends a reader to unrelated code.
    const byLabel = new Map(oracle.exportGroup.map((row) => [row.label, row]))
    for (const button of EXPORT_BUTTONS) {
      const row = byLabel.get(button.label)
      expect(row, button.label).toBeDefined()
      expect(button.source, button.label).toBe(`app.py:${row?.handlerLine}`)
    }
  })

  it('gives every button a non-empty detail for its tooltip', () => {
    for (const button of EXPORT_BUTTONS) {
      expect(button.detail.trim(), button.kind).not.toBe('')
    }
  })

  it('spells 导出ZIP without a space, as app.py 13773 does', () => {
    // UI_SPEC row 102 writes it 导出 ZIP; app.py does not, and app.py wins.
    const zip = EXPORT_BUTTONS.find((button) => button.kind === 'conversion_zip')
    expect(zip?.label).toBe('导出ZIP')
  })
})
