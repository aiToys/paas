// 平台资源 API（v2 简版列表页用）：模型目录 / 数据服务实例 / 环境 / API 密钥。
// 契约对齐老版对应页——掩码字段（key/connection）原样展示，明文仅创建时一次性返回。
import { fetchJSON, fetchList } from '@/api'

// ---------- 模型（MaaS catalog 富信息） ----------
export interface ModelChannel {
  id: string
  status: string
  endpoint: string
  vendor: string
  upstreamModel?: string
}

export interface ModelInfo {
  id: string
  name: string
  vendor: string
  contextWindow?: number
  capabilities?: string[]
  inputPrice?: number
  outputPrice?: number
  description?: string
  channels?: ModelChannel[]
}

export const listModels = () => fetchList<ModelInfo>('/api/models')

// ---------- 数据服务实例 ----------
export interface DataServiceEntity {
  id: string
  kind: 'db' | 'cache' | 'mq' | 'storage' | 'vector' | 'search'
  name: string
  spec?: { engine?: string; size_gb?: string }
  status: string
  source?: string
  engineId?: string
  envId?: string
  connection?: { host?: string; port?: string; uri?: string }
  createdAt?: string
}

export const listDataServices = () => fetchList<DataServiceEntity>('/api/dataservices')

// ---------- 环境 ----------
export interface EnvironmentEntity {
  id: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
  promoteOrder?: number
  createdAt?: string
}

export const listEnvironments = () => fetchList<EnvironmentEntity>('/api/environments')

// ---------- API 密钥（创建空 body：后端 ctx 补全 + roles 封顶自身，零提权） ----------
// 密钥派生状态（后端 KeyState 回填，前端不重复实现「7 天内到期」规则）。
export type ApiKeyState = 'active' | 'expiring' | 'expired' | 'revoked'

export interface ApiKeyEntity {
  id: string
  userId: string
  appId?: string // 可选：绑定应用则用量归因到应用（后端 identity.APIKey.AppID）
  roles: string[]
  key: string // 列表=掩码；创建响应=完整值（仅一次）
  state?: ApiKeyState
  expiresAt?: string
  lastUsedAt?: string
  revokedAt?: string
  rotatedFrom?: string
  createdAt?: string
}

export const listApiKeys = () => fetchList<ApiKeyEntity>('/api/api-keys')
export const createApiKey = () => fetchJSON<ApiKeyEntity>('/api/api-keys', { method: 'POST', body: '{}' })
export const deleteApiKey = (id: string) => fetchJSON<void>(`/api/api-keys/${id}`, { method: 'DELETE' })
// 吊销（软吊销保留记录）与轮换（新建同权限 Key + 吊销旧 Key）；非幂等破坏性操作由页面二次确认。
export const revokeApiKey = (id: string) =>
  fetchJSON<ApiKeyEntity>(`/api/api-keys/${id}/revoke`, { method: 'POST', body: '{}' })
export const rotateApiKey = (id: string) =>
  fetchJSON<ApiKeyEntity>(`/api/api-keys/${id}/rotate`, { method: 'POST', body: '{"expiresAt":""}' })
