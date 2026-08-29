// 配置中心 API（ns 共享维度 + 应用维度动态配置）。
// 应用维度（scope=app）是主路径：配置归属应用，经 /api/applications/{id}/dynamic-configs 消费，
// 后端自动 EnsureByApp 懒建应用专属 namespace，前端无需感知 namespace 实体。
// 模式照抄 change.ts：fetchAuth + 智能解包（{data:T} 取 data，否则原样——published 端点是裸 JSON）。
import { fetchAuth } from '@/api'

export interface DynamicConfigItem {
  id: string; namespaceId: string; key: string; value: string
  type: 'text' | 'json' | 'yaml'; updatedAt: string
}
export interface ConfigPublish {
  id: string; namespaceId: string; version: number
  snapshot: Record<string, string>; status: 'active' | 'rolled-back'; createdAt: string
}
export interface ConfigPublished {
  published: boolean; version?: number
  snapshot?: Record<string, string>; publishId?: string
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

// ---------- 应用维度动态配置（scope=app，主路径） ----------

// 列 draft 项（GET 列表同时触发后端 EnsureByApp 懒建，发布历史/当前生效用只读端点）
export const fetchAppDynamicConfigs = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs`).then(r => unwrap<DynamicConfigItem[]>(r))
export const upsertAppDynamicConfig = (appId: string, body: { key: string; value: string; type?: string }) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs`, { method: 'POST', body: JSON.stringify(body) }).then(r => unwrap<DynamicConfigItem>(r))
export const deleteAppDynamicConfig = (appId: string, itemId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/items/${itemId}`, { method: 'DELETE' }).then(r => unwrap<unknown>(r))
export const publishAppDynamicConfigs = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publish`, { method: 'POST' }).then(r => unwrap<ConfigPublish>(r))
export const fetchAppPublishes = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publishes`).then(r => unwrap<ConfigPublish[]>(r))
// 当前生效是裸 JSON {published,version,snapshot,publishId}（发现协议 shape），unwrap 兼容
export const fetchAppPublished = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/published`).then(r => unwrap<ConfigPublished>(r))

// 回滚走既有 ns 维度端点（路径不含 nsID，publishId 全局唯一）
export const rollbackPublish = (publishId: string) =>
  fetchAuth(`/api/configcenter/publishes/${publishId}/rollback`, { method: 'POST' }).then(r => unwrap<unknown>(r))
