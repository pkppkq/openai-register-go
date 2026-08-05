import { describe, expect, it } from 'vitest'
import { accountSelectionPreview } from '../AccountManagementDialog.svelte'
import {
  autoClassifyModeHint,
  autoClassifyScopeHint,
  AUTO_CLASSIFY_MODES,
  AUTO_CLASSIFY_SCOPES,
} from '../AutoClassifyDialog.svelte'
import { validateGroupName } from '../GroupManagementDialog.svelte'
import { clampMailboxLimit, mailboxDateText } from '../MailboxManagerDialog.svelte'
import {
  normalizeSessionPlanOverride,
  SESSION_PLAN_OVERRIDES,
} from '../ManualSessionDialog.svelte'
import {
  clampProviderDuration,
  normalizeProviderProxyConfig,
  PROVIDER_PROXY_ROLE_LABELS,
} from '../ProviderProxyDialog.svelte'

describe('account and group dialogs', () => {
  it('caps account previews at 20 while reporting the hidden count', () => {
    const emails = Array.from({ length: 23 }, (_, index) => `user${index + 1}@example.com`)
    const lines = accountSelectionPreview(emails).split('\n')
    expect(lines).toHaveLength(21)
    expect(lines.at(-1)).toBe('……另有 3 个账户')
  })

  it('validates group names exactly at the UI boundary', () => {
    expect(validateGroupName('', []).error).toBe('分组名称长度必须为 1–32 个字符')
    expect(validateGroupName('a'.repeat(33), []).error).toBe('分组名称长度必须为 1–32 个字符')
    expect(validateGroupName(' 全部 ', []).error).toBe('“全部”是保留名称')
    expect(validateGroupName('未分组', []).error).toBe('“未分组”是保留名称')
    expect(validateGroupName('alpha', ['Alpha']).error).toBe('已有同名分组')
    expect(validateGroupName(' Alpha ', ['Alpha'], 'Alpha')).toEqual({ name: 'Alpha', error: '' })
    expect(validateGroupName(' 新分组 ', ['Alpha'])).toEqual({ name: '新分组', error: '' })
  })
})

describe('auto classify and manual Session dialogs', () => {
  it('keeps all backend keys and their live hints', () => {
    expect(AUTO_CLASSIFY_MODES.map((item) => item.key)).toEqual(['trial', 'link', 'plan'])
    expect(AUTO_CLASSIFY_SCOPES.map((item) => item.key)).toEqual(['all', 'current', 'selected'])
    expect(autoClassifyModeHint('link')).toContain('提链成功 / 提链失败 / 未提链')
    expect(autoClassifyScopeHint('selected')).toBe('只处理当前选中的账号')
  })

  it('pins all plan overrides and safely falls back to auto', () => {
    expect([...SESSION_PLAN_OVERRIDES]).toEqual(['auto', 'plus', 'free', 'team', 'k12', 'pro'])
    expect(normalizeSessionPlanOverride('team')).toBe('team')
    expect(normalizeSessionPlanOverride('enterprise')).toBe('auto')
  })
})

describe('provider and mailbox dialogs', () => {
  it('clamps provider duration and trims only non-secret fields', () => {
    expect(clampProviderDuration(0)).toBe(1)
    expect(clampProviderDuration(121)).toBe(120)
    expect(clampProviderDuration(Number.NaN)).toBe(5)
    expect(
      normalizeProviderProxyConfig({
        enabled: true,
        username: ' user ',
        password: ' pass ',
        endpoint: ' host:123 ',
        duration: 999,
        regions: ' JP,US ',
      }),
    ).toEqual({
      enabled: true,
      username: 'user',
      password: ' pass ',
      endpoint: 'host:123',
      duration: 120,
      regions: 'JP,US',
    })
    expect(PROVIDER_PROXY_ROLE_LABELS).toEqual({
      create: '第一步',
      followup: '后续',
      approve: 'Approve',
    })
  })

  it('clamps mailbox limits and formats the Python 19-character timestamp', () => {
    expect(clampMailboxLimit(1)).toBe(10)
    expect(clampMailboxLimit(999)).toBe(500)
    expect(clampMailboxLimit(Number.NaN)).toBe(80)
    expect(mailboxDateText({ id: '1', mailTimeIso: '2026-07-27T12:34:56+08:00' })).toBe(
      '2026-07-27T12:34:56',
    )
  })
})
