// 广场共享 API 与常量（Skill/Prompt/Tool/Agent 发布-浏览-安装）。
import { fetchJSON, fetchAuth } from '@/api'

// 广场分类（后端透传不校验死，前端常量作展示真源）
export const CATEGORIES = [
  { value: 'writing', label: '写作' },
  { value: 'coding', label: '代码' },
  { value: 'data', label: '数据分析' },
  { value: 'service', label: '客服' },
  { value: 'general', label: '通用' },
] as const

export const ENTITY_TYPES = [
  { value: '', label: '全部' },
  { value: 'skill', label: 'Skill' },
  { value: 'prompt', label: 'Prompt' },
  { value: 'tool', label: '工具' },
  { value: 'agent', label: 'Agent' },
] as const

export const catLabel = (v?: string) =>
  CATEGORIES.find(c => c.value === v)?.label ?? (v || '未分类')

export interface MarketItem {
  id: string; entityType: string; name: string; description: string; category: string
  publisherTenant: string; publisherName: string; installs: number; createdAt: string
  snapshot?: unknown
}

export interface InstallResult { entityType: string; entityId: string; name: string }

export function listMarket(entityType = '', category = '', q = '') {
  const params = new URLSearchParams()
  if (entityType) params.set('entityType', entityType)
  if (category) params.set('category', category)
  if (q) params.set('q', q)
  const qs = params.toString()
  return fetchJSON<MarketItem[]>(`/api/marketplace${qs ? '?' + qs : ''}`)
}

export function getMarketItem(id: string) {
  return fetchJSON<MarketItem>(`/api/marketplace/${id}`)
}

export function publishToMarket(entityType: string, entityId: string, category: string) {
  return fetchAuth('/api/marketplace', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ entityType, entityId, category }),
  })
}

export function installFromMarket(id: string) {
  return fetchJSON<InstallResult>(`/api/marketplace/${id}/install`, { method: 'POST' })
}
