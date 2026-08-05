// 侧栏是工作区的唯一一级导航；Team、K12 与全部操作共用 actions 页面，
// 通过 actionGroup 决定进入页面时默认显示的操作分组。

export type PaneKey =
  | 'workbench'
  | 'jobs'
  | 'mail'
  | 'phone'
  | 'proxy'
  | 'payment'
  | 'export'
  | 'actions'
  | 'settings'

export const NAV_KEYS = [
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
] as const

export type NavKey = (typeof NAV_KEYS)[number]

/** 侧栏能直接预选的全部操作分组。 */
export type NavActionGroupKey = 'account' | 'team' | 'k12'

export type NavEntry = {
  key: NavKey
  label: string
  pane: PaneKey
  /** 进入全部操作页面时预选的字符串分组键。 */
  actionGroup?: NavActionGroupKey
}

export const NAV: NavEntry[] = [
  { key: 'workbench', label: '账户工作台', pane: 'workbench' },
  { key: 'jobs', label: '任务', pane: 'jobs' },
  { key: 'mail', label: '邮箱', pane: 'mail' },
  { key: 'phone', label: '手机与接码', pane: 'phone' },
  { key: 'proxy', label: '代理', pane: 'proxy' },
  { key: 'payment', label: '支付资料', pane: 'payment' },
  { key: 'export', label: '导出', pane: 'export' },
  { key: 'team', label: 'Team', pane: 'actions', actionGroup: 'team' },
  { key: 'k12', label: 'K12', pane: 'actions', actionGroup: 'k12' },
  { key: 'actions', label: '全部操作', pane: 'actions', actionGroup: 'account' },
  { key: 'settings', label: '设置', pane: 'settings' },
]

/** 当前已接入页面的侧栏项；保留集合形式，便于未来按能力关闭单项。 */
export const ENABLED_NAV_KEYS: ReadonlySet<NavKey> = new Set(NAV_KEYS)

/**
 * 兼容旧集成代码；新代码应使用 ENABLED_NAV_KEYS。
 * @deprecated 全量页面已经接入，不再存在 slice 1 边界。
 */
export const SLICE1_KEYS = ENABLED_NAV_KEYS
