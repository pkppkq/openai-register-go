/*
  The money gate's list arithmetic.

  ConfirmAction is NOT a port: app.py 13670 / 13726 / 13746 call their handlers
  directly with no confirmation at all, and this dialog is added on top of the
  Tk behaviour because a webview toolbar is reachable by a stray click or a held
  key in a way a Tk toolbar is not. There is therefore nothing in app.py to
  check the numbers against — what is pinned instead is that the dialog cannot
  understate what is about to run.

  The failure this guards against is specific: a user selects nine accounts,
  the dialog lists eight and says nothing about the ninth, and they confirm nine
  paid runs having read eight addresses. So both halves are asserted — the slice
  never exceeds the cap, and the overflow count is non-zero exactly when
  something is hidden.
*/
import { describe, expect, it } from 'vitest'
import {
  MAX_LISTED_EMAILS,
  listEmails,
  paymentAutoConfirmChoice,
  type ConfirmRequest,
} from '../ConfirmAction.svelte'

/** N addresses, distinguishable in a failure message. */
function emails(n: number): string[] {
  return Array.from({ length: n }, (_, i) => `user${i + 1}@example.com`)
}

describe('MAX_LISTED_EMAILS', () => {
  it('is eight', () => {
    // A cap on the reading, not a fit: the box scrolls at 132px regardless. The
    // value only has to stay small enough that 确认执行 is still on screen.
    expect(MAX_LISTED_EMAILS).toBe(8)
  })
})

describe('listEmails', () => {
  it('lists everything and reports no overflow at or below the cap', () => {
    for (const n of [0, 1, 7, MAX_LISTED_EMAILS]) {
      const { listed, overflow } = listEmails(emails(n))
      expect(listed, `${n} selected`).toEqual(emails(n))
      expect(overflow, `${n} selected`).toBe(0)
    }
  })

  it('reports the exact remainder above the cap', () => {
    // The off-by-one that matters: at nine selected, one address is hidden and
    // the dialog must know it.
    expect(listEmails(emails(9)).overflow).toBe(1)
    expect(listEmails(emails(20)).overflow).toBe(12)
    expect(listEmails(emails(100)).overflow).toBe(92)
  })

  it('never lists more than the cap', () => {
    for (const n of [0, 8, 9, 200]) {
      expect(listEmails(emails(n)).listed.length, `${n} selected`).toBeLessThanOrEqual(MAX_LISTED_EMAILS)
    }
  })

  it('keeps listed + overflow equal to the selection, so nothing is unaccounted for', () => {
    for (const n of [0, 1, 8, 9, 17, 63]) {
      const { listed, overflow } = listEmails(emails(n))
      expect(listed.length + overflow, `${n} selected`).toBe(n)
    }
  })

  it('lists the FIRST addresses, in selection order', () => {
    const { listed } = listEmails(emails(12))
    expect(listed[0]).toBe('user1@example.com')
    expect(listed[MAX_LISTED_EMAILS - 1]).toBe(`user${MAX_LISTED_EMAILS}@example.com`)
  })

  it('never returns a negative overflow', () => {
    // `Math.max(0, …)` is load-bearing: the template renders the tail line on
    // `overflow > 0`, and a negative would be silently falsy in a way that
    // happens to work — until someone renders the number.
    expect(listEmails([]).overflow).toBe(0)
    expect(listEmails(emails(3)).overflow).toBe(0)
  })

  it('does not mutate or alias the caller array', () => {
    // The dialog is handed the shell's live selection.
    const selection = emails(10)
    const { listed } = listEmails(selection)
    expect(listed).not.toBe(selection)
    expect(selection).toHaveLength(10)
  })
})

describe('paymentAutoConfirmChoice', () => {
  const baseRequest: ConfirmRequest = {
    label: '打开支付窗口',
    source: 'internal/ui/paymentwindow.go',
    detail: '打开真实支付页',
    emails: ['user@example.com'],
    costs: ['可能产生真实扣款'],
  }

  it('普通确认弹窗即使收到 checked=true 也不能授权自动扣款', () => {
    expect(paymentAutoConfirmChoice(baseRequest, true)).toBe(false)
    expect(paymentAutoConfirmChoice(null, true)).toBe(false)
  })

  it('支付弹窗必须本次主动勾选才返回 true', () => {
    const paymentRequest = { ...baseRequest, allowPaymentAutoConfirm: true }
    expect(paymentAutoConfirmChoice(paymentRequest, false)).toBe(false)
    expect(paymentAutoConfirmChoice(paymentRequest, true)).toBe(true)
  })
})
