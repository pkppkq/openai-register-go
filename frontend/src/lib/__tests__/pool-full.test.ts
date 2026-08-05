import { describe, expect, it } from 'vitest'
import {
  displayPhoneStatus,
  phoneEntryKey,
  type PhonePoolEntry,
} from '../pages/PhonePoolFull.svelte'
import {
  paymentCardEntryKey,
  paymentCardExpiry,
  type PaymentCardPoolEntry,
} from '../pages/PaymentCardPoolFull.svelte'

describe('PhonePoolFull view model', () => {
  it('uses the number as the stable key', () => {
    expect(
      phoneEntryKey({
        number: ' +15550000001 ',
        receiveCount: 0,
        status: '可用',
      }),
    ).toBe('+15550000001')
  })

  it('derives frozen status without mutating the model', () => {
    const phone: PhonePoolEntry = {
      number: '+15550000001',
      receiveCount: 3,
      status: '可用',
      lastCode: '123456',
    }
    expect(displayPhoneStatus(phone, 3)).toBe('冻结')
    expect(phone.status).toBe('可用')
    expect(displayPhoneStatus(phone, 0)).toBe('可用')
  })

  it('preserves explicit unusable and frozen status', () => {
    expect(
      displayPhoneStatus({ number: '+1', receiveCount: 99, status: '不可用' }, 1),
    ).toBe('不可用')
    expect(displayPhoneStatus({ number: '+2', receiveCount: 99, status: '冻结' }, 1)).toBe('冻结')
  })
})

describe('PaymentCardPoolFull view model', () => {
  const card: PaymentCardPoolEntry = {
    card: '4111111111111111',
    month: '07',
    year: '2028',
    cvv: '123',
    status: '未用',
  }

  it('uses card number as the stable key', () => {
    expect(paymentCardEntryKey({ ...card, card: ` ${card.card} ` })).toBe(card.card)
  })

  it('renders year/month exactly like the Python table', () => {
    expect(paymentCardExpiry(card)).toBe('2028/07')
  })
})
