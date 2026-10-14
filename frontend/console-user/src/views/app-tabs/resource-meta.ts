// 资源类型元信息（单一真源）：AppBindings（绑定分组卡片）与 AppOverview（依赖拓扑）共用。
// 资源中心 = 数据服务；dal 兼容历史 seed，gov 兼容「应用接入治理」语义。
export type TypeKey = 'models' | 'db' | 'cache' | 'mq' | 'storage' | 'vector' | 'search' | 'dal' | 'gov'

export interface Binding {
  type: string
  name: string
  note?: string
}

// 应用实体（主文件壳与 AppBindings 共享：绑定/解绑回传完整实体，主文件更新 tab 徽标等）。
export interface App {
  id: string
  name: string
  initial: string
  env: string
  status: 'healthy' | 'degraded' | 'idle'
  gradient: string
  desc: string
  bindings: Binding[]
  resourceTemplate?: { cpuRequest?: string; cpuLimit?: string; memRequest?: string; memLimit?: string }
}

export interface TypeMeta {
  label: string
  icon: string
  color: string
}

export const typeMeta: Record<string, TypeMeta> = {
  models: { label: '模型推理', icon: 'market', color: '#6366f1' },
  db: { label: '数据库', icon: 'database', color: '#f59e0b' },
  cache: { label: '缓存', icon: 'zap', color: '#ef4444' },
  mq: { label: '消息队列', icon: 'message', color: '#10b981' },
  storage: { label: '对象存储', icon: 'storage', color: '#0ea5e9' },
  vector: { label: '向量数据库', icon: 'layers', color: '#8b5cf6' },
  search: { label: '搜索引擎', icon: 'search', color: '#06b6d4' },
  dal: { label: '数据访问层', icon: 'database', color: '#f59e0b' },
  gov: { label: '服务治理', icon: 'service', color: '#ec4899' },
}

// 数据服务 kind 集合：绑定后后端自动注入连接信息到 appconfig；前端据此提示 + placeholder。
export const DS_KINDS: TypeKey[] = ['db', 'cache', 'mq', 'storage', 'vector', 'search']

// 绑定展示固定顺序：数据服务全集。
export const TYPE_ORDER = ['models', 'db', 'cache', 'mq', 'storage', 'vector', 'search', 'dal', 'gov']
