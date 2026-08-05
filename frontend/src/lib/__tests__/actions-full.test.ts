import { describe, expect, it } from 'vitest'
import {
  DEFAULT_K12_WORKSPACE_ID,
  FULL_ACTION_GROUPS,
  SESSION_CONVERT_FORMATS,
  actionGroup,
  actionRenderMode,
  actionTableHeight,
} from '../pages/ActionsFull.svelte'

describe('ActionsFull action catalogue', () => {
  it('keeps the six Python groups, order and item counts', () => {
    expect(FULL_ACTION_GROUPS.map((group) => group.key)).toEqual([
      'account',
      'auth',
      'link',
      'export',
      'team',
      'k12',
    ])
    expect(FULL_ACTION_GROUPS.map((group) => group.actions.length)).toEqual([11, 12, 8, 8, 3, 4])
  })

  it('keeps every action key unique and every Chinese help string non-empty', () => {
    const actions = FULL_ACTION_GROUPS.flatMap((group) => group.actions)
    expect(actions).toHaveLength(46)
    expect(new Set(actions.map((action) => action.key)).size).toBe(actions.length)
    expect(actions.every((action) => action.label.trim() !== '' && action.help.trim() !== '')).toBe(true)
  })

  it('keeps the exact visible labels in each group', () => {
    expect(actionGroup('account').actions.map((action) => action.label)).toEqual([
      '导入',
      '邮箱管理',
      '别名注册',
      '生成域名邮箱',
      '全选可见',
      '反选可见',
      '清空选择',
      '刷新类型',
      '自动分类',
      '删除选中',
      '清空列表',
    ])
    expect(actionGroup('team').actions.map((action) => action.label)).toEqual([
      '邀请成员',
      '退出 Team',
      '扫描邀请加入',
    ])
    expect(actionGroup('k12').actions.map((action) => action.label)).toEqual([
      '一键注册加入',
      '请求邀请',
      '接受并刷新',
      '刷新K12',
    ])
  })

  it('uses the original <=4 button / >4 table rendering rule', () => {
    expect(actionRenderMode(actionGroup('account'))).toBe('table')
    expect(actionRenderMode(actionGroup('auth'))).toBe('table')
    expect(actionRenderMode(actionGroup('link'))).toBe('table')
    expect(actionRenderMode(actionGroup('export'))).toBe('table')
    expect(actionRenderMode(actionGroup('team'))).toBe('buttons')
    expect(actionRenderMode(actionGroup('k12'))).toBe('buttons')
    expect(actionTableHeight(actionGroup('account'))).toBe(6)
    expect(actionTableHeight(actionGroup('team'))).toBe(3)
  })

  it('pins the seven conversion formats and K12 default', () => {
    expect([...SESSION_CONVERT_FORMATS]).toEqual([
      'sub2api',
      'cpa',
      'cockpit',
      '9router',
      'codex',
      'axonhub',
      'codexmanager',
    ])
    expect(DEFAULT_K12_WORKSPACE_ID).toBe('workspace-example')
  })
})
