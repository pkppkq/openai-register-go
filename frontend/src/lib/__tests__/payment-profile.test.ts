/*
  S16 支付资料 — the five persisted PayPal keys.

  发布版五项全部默认为空。Python 历史基准里的扩展目录是开发机 D 盘路径，
  不能写进可分发 EXE；Go 与前端因此刻意采用更安全的空值。

  `paypal_phone_pool_index` is deliberately absent from the type: it is the
  round-robin cursor `_take_paypal_phone_config` advances (app.py:17593), never
  a control. The load-patch-save chain has to carry it through untouched, which
  is pinned over in api.test.ts.
*/
import { describe, expect, it } from 'vitest'
import { DEFAULT_PAYPAL_EXTENSION_DIR, PAYPAL_DEFAULTS } from '../pages/PaymentProfile.svelte'
import { oracle } from './oracle'

describe('DEFAULT_PAYPAL_EXTENSION_DIR', () => {
  it('发布版为空，不携带 Python 开发机的 D 盘路径', () => {
    expect(DEFAULT_PAYPAL_EXTENSION_DIR).toBe('')
    expect(oracle.constants.DEFAULT_PAYPAL_EXTENSION_DIR).not.toBe('')
    expect(oracle.constants.DEFAULT_PAYPAL_EXTENSION_DIR).toMatch(/^D:\\/)
  })
})

describe('PAYPAL_DEFAULTS', () => {
  it('发布版五个支付资料初值均为空', () => {
    expect(PAYPAL_DEFAULTS).toEqual({
      phone: '',
      card: '',
      smsUrl: '',
      phonePool: '',
      extensionDir: DEFAULT_PAYPAL_EXTENSION_DIR,
    })
  })

  it('ships no phone number, card or SMS link', () => {
    // The assertion that keeps a real credential from being committed as a
    // "convenient" default.
    expect(PAYPAL_DEFAULTS.phone).toBe('')
    expect(PAYPAL_DEFAULTS.card).toBe('')
    expect(PAYPAL_DEFAULTS.smsUrl).toBe('')
    expect(PAYPAL_DEFAULTS.phonePool).toBe('')
    expect(PAYPAL_DEFAULTS.extensionDir).toBe('')
  })

  it('covers exactly the five keys S16 owns, without the pool cursor', () => {
    // paypal_phone_pool_index is state, not a control; adding it here would
    // give the pane a way to reset a user's rotation to zero on every save.
    expect(Object.keys(PAYPAL_DEFAULTS).sort()).toEqual(['card', 'extensionDir', 'phone', 'phonePool', 'smsUrl'])
  })
})
