/*
  S17 代理设置.

  The route mode is the highest-consequence single setting on this pane:
  全走本地代理 makes every button ignore the dynamic pools, the reuse proxies and
  the provider pools (app.py 16720's own log line says so). So what is pinned is
  not just the two strings but the DIRECTION of the coercion — anything that is
  not exactly 全走本地代理 is 照旧, which is what app.py's
  `_local_proxy_only_enabled` (16712-16715) computes.

  Constants are checked against `oracle.constants`, which the harness read out
  of app.py 282-283 and 12340-12448 rather than out of this component.
*/
import { describe, expect, it } from 'vitest'
import {
  DEFAULT_LOCAL_PROXY,
  PROXY_POOL_ORDER,
  PROXY_POOL_SETTINGS_KEYS,
  PROXY_POOL_TITLE_BASES,
  PROXY_ROUTE_MODE_DEFAULT,
  PROXY_ROUTE_MODE_LOCAL_ONLY,
  PROXY_ROUTE_MODE_OPTIONS,
  coerceRouteMode,
  proxyPoolTitle,
} from '../pages/ProxySettings.svelte'
import { oracle } from './oracle'

describe('route mode constants', () => {
  it('are app.py 282-283 verbatim', () => {
    expect(PROXY_ROUTE_MODE_DEFAULT).toBe(oracle.constants.PROXY_ROUTE_MODE_DEFAULT)
    expect(PROXY_ROUTE_MODE_LOCAL_ONLY).toBe(oracle.constants.PROXY_ROUTE_MODE_LOCAL_ONLY)
  })

  it('list the two options in app.py 284 order, which is the combo order', () => {
    expect([...PROXY_ROUTE_MODE_OPTIONS]).toEqual([PROXY_ROUTE_MODE_DEFAULT, PROXY_ROUTE_MODE_LOCAL_ONLY])
  })

  it('default to 照旧, the app.py 12341 StringVar seed', () => {
    expect(PROXY_ROUTE_MODE_OPTIONS[0]).toBe(PROXY_ROUTE_MODE_DEFAULT)
  })
})

describe('coerceRouteMode', () => {
  it('recognises 全走本地代理, with or without surrounding whitespace', () => {
    expect(coerceRouteMode(PROXY_ROUTE_MODE_LOCAL_ONLY)).toBe(PROXY_ROUTE_MODE_LOCAL_ONLY)
    expect(coerceRouteMode(`  ${PROXY_ROUTE_MODE_LOCAL_ONLY}\t`)).toBe(PROXY_ROUTE_MODE_LOCAL_ONLY)
  })

  it('falls back to 照旧 for anything else, which is the safe direction', () => {
    // A state file written by another build, a hand edit, an empty string: the
    // failure mode of a mismatch has to be "keep using the proxy pools", never
    // "silently ignore every pool the user configured".
    for (const value of ['', '   ', '照旧', 'local_only', '全走本地代理x', '全走本地', 'LOCAL']) {
      expect(coerceRouteMode(value), JSON.stringify(value)).toBe(PROXY_ROUTE_MODE_DEFAULT)
    }
  })

  it('always returns one of the two combo options, so the select is never blank', () => {
    for (const value of ['', 'junk', PROXY_ROUTE_MODE_LOCAL_ONLY, PROXY_ROUTE_MODE_DEFAULT]) {
      expect(PROXY_ROUTE_MODE_OPTIONS, value).toContain(coerceRouteMode(value))
    }
  })
})

describe('proxy pools', () => {
  it('keep the app.py 13520-13549 display order rather than a map iteration', () => {
    // The pool set is a Python dict whose order is part of the layout; the Go
    // side must not range a map here either.
    expect([...PROXY_POOL_ORDER]).toEqual(['register', 'create', 'followup', 'approve'])
  })

  it('name every pool with app.py 12443-12448 titles', () => {
    expect(PROXY_POOL_TITLE_BASES).toEqual({
      register: '注册/获取 Session 动态代理池',
      create: '创建长链第一步代理池',
      followup: '创建长链后续代理池',
      approve: 'Approve 代理池',
    })
  })

  it('map every pool to a distinct settings key', () => {
    // A duplicate here would make two textareas write the same state.json key
    // and silently lose one pool.
    const keys = PROXY_POOL_ORDER.map((pool) => PROXY_POOL_SETTINGS_KEYS[pool])
    expect(keys).toEqual(['dynamic_proxies', 'payment_dynamic_proxy', 'followup_dynamic_proxy', 'approve_dynamic_proxy'])
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('has a title and a settings key for every ordered pool and no extras', () => {
    expect(Object.keys(PROXY_POOL_TITLE_BASES).sort()).toEqual([...PROXY_POOL_ORDER].sort())
    expect(Object.keys(PROXY_POOL_SETTINGS_KEYS).sort()).toEqual([...PROXY_POOL_ORDER].sort())
  })
})

describe('proxyPoolTitle', () => {
  it('renders app.py 13213 format', () => {
    expect(proxyPoolTitle('register', 3)).toBe('注册/获取 Session 动态代理池（剩余 3）')
    expect(proxyPoolTitle('approve', 0)).toBe('Approve 代理池（剩余 0）')
  })

  it('clamps a negative count to zero, as max(0, …) does', () => {
    expect(proxyPoolTitle('create', -1)).toBe('创建长链第一步代理池（剩余 0）')
  })

  it('truncates toward zero, as int() does', () => {
    expect(proxyPoolTitle('followup', 2.9)).toBe('创建长链后续代理池（剩余 2）')
    expect(proxyPoolTitle('followup', -0.5)).toBe('创建长链后续代理池（剩余 0）')
  })

  it('renders a non-finite count as zero rather than as NaN in a heading', () => {
    expect(proxyPoolTitle('register', Number.NaN)).toContain('剩余 0')
    expect(proxyPoolTitle('register', Number.POSITIVE_INFINITY)).toContain('剩余 0')
  })
})

describe('DEFAULT_LOCAL_PROXY', () => {
  it('is the app.py 12340 seed', () => {
    expect(DEFAULT_LOCAL_PROXY).toBe('http://127.0.0.1:7890')
  })

  it('is a loopback address, so a fresh install proxies nothing off-box', () => {
    expect(new URL(DEFAULT_LOCAL_PROXY).hostname).toBe('127.0.0.1')
  })
})
