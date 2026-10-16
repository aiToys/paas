// 导航、命令搜索共用一份能力目录；分组只用于找路，不增加业务实体层级。
export interface NavItem { label: string; to: string; icon: string; keywords?: string }
export const navigation: { label: string; items: NavItem[] }[] = [
  { label: '应用交付', items: [
    { label: '产品', to: '/products', icon: 'deploy', keywords: '应用 application' },
    { label: '交付中心', to: '/delivery', icon: 'pipeline', keywords: 'DevOps CI CD 构建 发布 审批 变更' },
    { label: '运行实例', to: '/workloads', icon: 'server', keywords: '工作负载 日志 服务 任务 定时' },
  ] },
  { label: 'AI 平台', items: [
    { label: '模型目录', to: '/models', icon: 'market', keywords: '推理 MaaS 价格 通道' },
    { label: 'Playground', to: '/playground', icon: 'playground', keywords: '试玩 试用 对话' },
    { label: 'AI 开发', to: '/ai', icon: 'bot', keywords: '智能体 Agent 工作流 知识库 提示词 工具 技能 广场' },
  ] },
  { label: '基础设施', items: [
    { label: '数据服务', to: '/dataservices', icon: 'database', keywords: '数据库 缓存 消息 队列 存储 向量 搜索' },
    { label: '可观测', to: '/observability', icon: 'activity', keywords: '告警 日志 链路 trace' },
    { label: '流量治理', to: '/governance', icon: 'service', keywords: '路由 域名 服务发现 熔断' },
    { label: '配置中心', to: '/config-center', icon: 'sliders', keywords: '动态配置 共享命名空间 发布 回滚' },
    { label: '环境', to: '/environments', icon: 'env', keywords: '生产 测试 集群' },
  ] },
  { label: '组织与访问', items: [
    { label: '密钥与凭证', to: '/secrets', icon: 'shield' },
    { label: 'API 密钥', to: '/api-keys', icon: 'key' },
    { label: '用量与配额', to: '/billing', icon: 'usage', keywords: '组织 账单 token' },
  ] },
]
export const aiSections = [
  { id: 'agents', label: '智能体', icon: 'bot' },
  { id: 'workflows', label: '工作流', icon: 'pipeline' },
  { id: 'knowledgebases', label: '知识库', icon: 'book' },
  { id: 'tools', label: '工具', icon: 'tool' },
  { id: 'prompts', label: '提示词', icon: 'prompt' },
  { id: 'skills', label: '技能', icon: 'skill' },
  { id: 'explore', label: '资源广场', icon: 'market' },
  { id: 'runs', label: '执行记录', icon: 'clock' },
]
export function notificationLink(n: { targetType: string; targetId: string; appId: string }): string {
  const id = encodeURIComponent(n.targetId)
  if (n.targetType === 'run') return '/runs/' + id
  if (n.targetType === 'build') return '/builds/' + id
  if (n.targetType === 'alert') return '/observability'
  const section = n.targetType === 'batch' || n.targetType === 'change' ? 'changes' : 'runs'
  return '/products/' + encodeURIComponent(n.appId) + '?tab=delivery&section=' + section + '&focus=' + id
}
// 精确名称优先于父级关键词；搜索“知识库”必须直达知识库而非笼统 AI 首页。
export function searchEntries<T extends { label: string; keywords?: string; group?: string }>(entries: T[], query: string): T[] {
  const term = query.trim().toLowerCase()
  if (!term) return entries
  const score = (item: T) => {
    const label = item.label.toLowerCase()
    if (label === term) return 0
    if (label.startsWith(term)) return 1
    if (label.includes(term)) return 2
    if (item.keywords?.toLowerCase().includes(term)) return 3
    if (item.group?.toLowerCase().includes(term)) return 4
    return 5
  }
  return entries.filter((item) => score(item) < 5).sort((a, b) => score(a) - score(b))
}
