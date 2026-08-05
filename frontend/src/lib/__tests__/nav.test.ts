import { describe, expect, it } from 'vitest'
import {
  ENABLED_NAV_KEYS,
  NAV,
  NAV_KEYS,
  SLICE1_KEYS,
  type NavActionGroupKey,
  type PaneKey,
} from '../nav'

/**
 * App.svelte 能渲染的九个页面。这里不从 NAV 派生，避免错误页面键同时污染两边。
 */
const PANES = [
  'workbench',
  'jobs',
  'mail',
  'phone',
  'proxy',
  'payment',
  'export',
  'actions',
  'settings',
] as const satisfies readonly PaneKey[]

describe('NAV', () => {
  it('gives every entry a pane the shell knows how to render', () => {
    for (const entry of NAV) {
      expect(PANES, `${entry.key} -> ${entry.pane}`).toContain(entry.pane)
    }
  })

  it('has no duplicate keys, because the key is the selection and the each-block key', () => {
    const keys = NAV.map((entry) => entry.key)
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('exports the eleven sidebar keys in display order', () => {
    expect(NAV_KEYS).toEqual([
      'workbench',
      'jobs',
      'mail',
      'phone',
      'proxy',
      'payment',
      'export',
      'team',
      'k12',
      'actions',
      'settings',
    ])
    expect(NAV.map((entry) => entry.key)).toEqual(NAV_KEYS)
  })

  it('lands team, k12 and actions on the one 全部操作 pane', () => {
    const onActions = NAV.filter((entry) => entry.pane === 'actions')
    expect(onActions.map((entry) => entry.key)).toEqual(['team', 'k12', 'actions'])
    expect(onActions.map((entry) => entry.actionGroup)).toEqual(['team', 'k12', 'account'])
  })

  it('uses typed string action-group keys only on the actions pane', () => {
    const groups = new Set<NavActionGroupKey>(['account', 'team', 'k12'])
    for (const entry of NAV) {
      if (entry.actionGroup === undefined) continue
      expect(entry.pane).toBe('actions')
      expect(groups.has(entry.actionGroup)).toBe(true)
    }
  })

  it('labels every entry', () => {
    for (const entry of NAV) expect(entry.label.trim()).not.toBe('')
  })
})

describe('ENABLED_NAV_KEYS', () => {
  it('names only keys that exist in NAV', () => {
    const keys = new Set(NAV.map((entry) => entry.key))
    for (const key of ENABLED_NAV_KEYS) expect(keys, `enabled key ${key}`).toContain(key)
  })

  it('enables every integrated sidebar entry, including Team, K12 and 全部操作', () => {
    expect([...ENABLED_NAV_KEYS]).toEqual(NAV_KEYS)
    for (const key of ['team', 'k12', 'actions'] as const) {
      expect(ENABLED_NAV_KEYS.has(key), `${key} should be enabled`).toBe(true)
    }
  })

  it('keeps the former slice export as a compatibility alias', () => {
    expect(SLICE1_KEYS).toBe(ENABLED_NAV_KEYS)
  })

  it('preserves NAV order when used as a filter', () => {
    expect(NAV.filter((entry) => ENABLED_NAV_KEYS.has(entry.key)).map((entry) => entry.key)).toEqual(
      NAV_KEYS,
    )
  })
})
