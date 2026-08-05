import { describe, expect, it } from 'vitest'
import {
  WORKFLOW_STEPS,
  WORKFLOW_STATES,
  deriveWorkflow,
  mailInspectorText,
  sessionSections,
  sessionText,
  type AccountDetailAccount,
} from '../pages/AccountDetailsFull.svelte'

describe('AccountDetailsFull workflow', () => {
  it('keeps all seven workflow steps and six state values in source order', () => {
    expect(WORKFLOW_STEPS).toEqual([
      { key: 'email', label: '邮箱' },
      { key: 'proxy', label: '代理' },
      { key: 'auth', label: '注册/登录' },
      { key: 'session', label: 'Session' },
      { key: 'trial', label: '试用资格' },
      { key: 'link', label: '支付长链' },
      { key: 'export', label: '导出' },
    ])
    expect([...WORKFLOW_STATES]).toEqual(['未开始', '进行中', '成功', '失败', '需要人工', '跳过'])
  })

  it('derives imported, Session, trial and link state without discarding saved fields', () => {
    const workflow = deriveWorkflow(
      {
        access_token: 'token',
        access_summary: { plan_type: 'plus', expires_at: '2030-01-02' },
        plus_trial_status: 'eligible',
        plus_trial_eligible: true,
        workflow: {
          proxy: { state: '成功', detail: '出口检查通过', updated_at: '2026-01-01T00:00:00' },
        },
      },
      true,
    )
    expect(workflow.email.detail).toBe('已导入账号，尚未检查邮箱')
    expect(workflow.proxy).toEqual({
      state: '成功',
      detail: '出口检查通过',
      updatedAt: '2026-01-01T00:00:00',
    })
    expect(workflow.session).toMatchObject({ state: '成功', detail: 'plan=plus，到期=2030-01-02' })
    expect(workflow.trial).toMatchObject({ state: '成功', detail: 'eligible eligible=true' })
    expect(workflow.link).toMatchObject({ state: '成功', detail: '长链已保存' })
  })

  it('never overrides an explicit failed Session or link state', () => {
    const workflow = deriveWorkflow(
      {
        access_token: 'token',
        workflow: {
          session: { state: '失败', detail: 'Token 过期' },
          link: { state: '失败', detail: '金额不匹配' },
        },
      },
      true,
    )
    expect(workflow.session).toMatchObject({ state: '失败', detail: 'Token 过期' })
    expect(workflow.link).toMatchObject({ state: '失败', detail: '金额不匹配' })
  })

  it('renders an unknown trial status as running, fixing the Python green-success fallthrough', () => {
    expect(deriveWorkflow({ plus_trial_status: 'checking' }, false).trial.state).toBe('进行中')
    expect(deriveWorkflow({ plus_trial_status: 'not_eligible' }, false).trial.state).toBe('失败')
    expect(deriveWorkflow({ plus_trial_status: 'error' }, false).trial.state).toBe('失败')
  })
})

describe('AccountDetailsFull Session rendering', () => {
  it('keeps the fixed section order and proxy exit inheritance', () => {
    const sections = sessionSections({
      access_summary: { plan_type: 'team' },
      access_token: 'access',
      checkout_url: 'https://example.test/checkout',
      payment_link_type: 'paypal_approve',
      stripe_amount: '0',
      k12_status: '200',
      team_workspace_id: 'team-1',
      plus_trial_status: 'eligible',
      openai_deactivation_checked_at: '2026-01-01',
      link_proxy: 'http://proxy',
      link_create_proxy_exit: 'JP 1.2.3.4',
      session_json: '{"accessToken":"access"}',
    })
    expect(sections.map((section) => section.title)).toEqual([
      'Session Summary',
      'Access Token',
      'Checkout URL',
      'Payment Link Type',
      'Amount Check',
      'K12',
      'Team Invite',
      'Plus Trial',
      'OpenAI Deactivation Mail',
      'Long Link Proxy',
      'Long Link Proxy Exits',
      'Session JSON',
    ])
    expect(sections.find((section) => section.title === 'Long Link Proxy Exits')?.lines).toEqual([
      '第一步=JP 1.2.3.4',
      '后续=JP 1.2.3.4',
      'Approve=JP 1.2.3.4',
    ])
    expect(sessionText({ access_token: 'abc' })).toBe('Access Token:\nabc')
  })

  it('renders the mailbox inspector without exposing secrets', () => {
    const account: AccountDetailAccount = {
      email: 'alias+123@example.com',
      receive_mailbox: 'alias@example.com',
      mail_provider: 'cloudmail',
      group: '未分组',
      status: 'Session已获取',
      client_id: 'client',
      refresh_token: 'secret',
      openai_rt: 'rt-secret',
    }
    const inspector = mailInspectorText(account, 1)
    expect(inspector).toContain('接收邮箱：alias@example.com')
    expect(inspector).toContain('邮箱 OAuth：已配置')
    expect(inspector).toContain('OpenAI RT：已保存')
    expect(inspector).not.toContain('secret')
  })
})
