import { describe, expect, it } from 'vitest'
import {
  LINK_PROXY_REGION_NAMES,
  LINK_PROXY_REGION_OPTIONS,
  PROVIDER_PROXY_ROLES,
  PROVIDER_PROXY_ROLE_LABELS,
  clampProxyInteger,
  providerStageStatusText,
} from '../pages/ProxySettingsFull.svelte'

describe('ProxySettingsFull constants', () => {
  it('keeps exactly three provider roles in display order', () => {
    expect([...PROVIDER_PROXY_ROLES]).toEqual(['create', 'followup', 'approve'])
    expect(PROVIDER_PROXY_ROLE_LABELS).toEqual({
      create: '第一步',
      followup: '后续',
      approve: 'Approve',
    })
  })

  it('keeps auto, any and the 21 Python region options in order', () => {
    expect(Object.keys(LINK_PROXY_REGION_NAMES)).toHaveLength(21)
    expect(LINK_PROXY_REGION_OPTIONS).toHaveLength(23)
    expect(LINK_PROXY_REGION_OPTIONS.slice(0, 5)).toEqual([
      '自动(跟随支付地区)',
      '不限',
      'US 美国',
      'BR 巴西',
      'JP 日本',
    ])
    expect(LINK_PROXY_REGION_OPTIONS.at(-1)).toBe('NO 挪威')
  })
})

describe('ProxySettingsFull helpers', () => {
  it('clamps integer settings to their declared bounds', () => {
    expect(clampProxyInteger(0, 1, 30, 1)).toBe(1)
    expect(clampProxyInteger(31, 1, 30, 1)).toBe(30)
    expect(clampProxyInteger(2.9, 1, 30, 1)).toBe(2)
    expect(clampProxyInteger(Number.NaN, 1, 30, 7)).toBe(7)
  })

  it('formats provider pool progress without negative or fractional counts', () => {
    expect(providerStageStatusText(undefined)).toBe('未预热')
    expect(providerStageStatusText({ ready: 217.9, target: 500, checking: 8.7 })).toBe(
      '可用 217/500 检测中 8',
    )
    expect(providerStageStatusText({ ready: -1, target: -2, checking: 0 })).toBe('可用 0/0')
    expect(providerStageStatusText({ ready: 0, target: 500, checking: 0, message: ' 已启用，未预热 ' })).toBe(
      '已启用，未预热',
    )
  })
})
