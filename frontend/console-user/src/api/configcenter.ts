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
  overrideHash?: string // lane 覆盖集指纹（无覆盖时省略；version 或 overrideHash 任一变化即热替换）
}
// LaneOverride 泳道配置覆盖（无版本链，即时生效，随泳道回收消失）
export interface LaneOverride {
  id: string; appId: string; envId: string; laneId: string
  key: string; value: string; updatedAt: string
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

// ---------- 应用维度动态配置（scope=app，主路径） ----------

// envId query 串（envId 空 = 基线 ns，向后兼容）
const qs = (envId?: string) => (envId ? `?envId=${encodeURIComponent(envId)}` : '')

// 列 draft 项（GET 列表同时触发后端 EnsureByAppEnv 懒建，发布历史/当前生效用只读端点）
export const fetchAppDynamicConfigs = (appId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs${qs(envId)}`).then(r => unwrap<DynamicConfigItem[]>(r))
export const upsertAppDynamicConfig = (appId: string, body: { key: string; value: string; type?: string }, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs${qs(envId)}`, { method: 'POST', body: JSON.stringify(body) }).then(r => unwrap<DynamicConfigItem>(r))
export const deleteAppDynamicConfig = (appId: string, itemId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/items/${itemId}${qs(envId)}`, { method: 'DELETE' }).then(r => unwrap<unknown>(r))
export const publishAppDynamicConfigs = (appId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publish${qs(envId)}`, { method: 'POST' }).then(r => unwrap<ConfigPublish>(r))
export const fetchAppPublishes = (appId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publishes${qs(envId)}`).then(r => unwrap<ConfigPublish[]>(r))
// 当前生效是裸 JSON {published,version,snapshot[,overrideHash]}（发现协议 shape），unwrap 兼容。
// 注意：本端点 query 名为 envId（与 dynamic-configs 全家一致）；
// 按应用名发现端点 /api/configcenter/apps/{name}/published 才是 env（发现协议约定）。
export const fetchAppPublished = (appId: string, opts?: { envId?: string; lane?: string }) => {
  const p = new URLSearchParams()
  if (opts?.envId) p.set('envId', opts.envId)
  if (opts?.lane) p.set('lane', opts.lane)
  const q = p.toString()
  return fetchAuth(`/api/applications/${appId}/dynamic-configs/published${q ? `?${q}` : ''}`).then(r => unwrap<ConfigPublished>(r))
}

// 回滚走应用维度端点（后端校验 pid 属本应用派生 ns，防跨应用回滚；权限域 application:write + AppGuard）
export const rollbackAppPublish = (appId: string, publishId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/rollback/${publishId}${qs(envId)}`, { method: 'POST' }).then(r => unwrap<unknown>(r))

// ---------- 泳道覆盖（lane-overrides，即时生效随泳道回收消失） ----------
// lane 必填（后端 400 兜底）；写权限 application:write + 生产闸门（prod env 需 prod:write）
export const fetchLaneOverrides = (appId: string, envId: string, lane: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/lane-overrides?envId=${encodeURIComponent(envId)}&lane=${encodeURIComponent(lane)}`)
    .then(r => unwrap<LaneOverride[]>(r))
export const upsertLaneOverride = (appId: string, envId: string, lane: string, body: { key: string; value: string }) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/lane-overrides?envId=${encodeURIComponent(envId)}&lane=${encodeURIComponent(lane)}`, { method: 'POST', body: JSON.stringify(body) })
    .then(r => unwrap<LaneOverride>(r))
export const deleteLaneOverride = (appId: string, envId: string, lane: string, key: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/lane-overrides/${encodeURIComponent(key)}?envId=${encodeURIComponent(envId)}&lane=${encodeURIComponent(lane)}`, { method: 'DELETE' })
    .then(r => unwrap<unknown>(r))
// 提升：覆盖合并进基线草稿 + 发新版本 + 清覆盖（灰度验证 → 全量生效的单步操作）
export const promoteLaneOverrides = (appId: string, envId: string, lane: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/lane-overrides/promote?envId=${encodeURIComponent(envId)}&lane=${encodeURIComponent(lane)}`, { method: 'POST' })
    .then(r => unwrap<ConfigPublish>(r))

// ---------- 共享配置引用（shared ns → 应用派生 ns，发现时作为三层 merge 基础层） ----------

// 共享配置引用（富化视图：含 shared ns 名/active 版本/key 数）
export interface SharedRef {
  id: string; appNsId: string; sharedNsId: string; createdAt: string
  sharedName?: string; sharedVersion?: number; sharedKeys?: number
}
// 影响面反查（shared 管理侧：被哪些应用引用）
export interface RefUser {
  id: string; appNsId: string; sharedNsId: string; createdAt: string
  appNsName?: string
}
export const fetchSharedRefs = (appId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/shared-refs${qs(envId)}`).then(r => unwrap<SharedRef[]>(r))
export const addSharedRef = (appId: string, sharedNsId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/shared-refs${qs(envId)}`, { method: 'POST', body: JSON.stringify({ sharedNsId }) }).then(r => unwrap<SharedRef>(r))
export const deleteSharedRef = (appId: string, refId: string, envId?: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/shared-refs/${refId}${qs(envId)}`, { method: 'DELETE' }).then(r => unwrap<unknown>(r))
export const fetchRefUsers = (nsId: string) =>
  fetchAuth(`/api/configcenter/namespaces/${nsId}/ref-users`).then(r => unwrap<RefUser[]>(r))

// 租户内 shared ns 列表（引用选择器数据源；app 派生 ns 归应用详情管理，不在此返回）
export interface ConfigNamespace { id: string; name: string; scope: string }
export const fetchNamespaces = () =>
  fetchAuth('/api/configcenter/namespaces')
    .then(async r => { const list = await unwrap<ConfigNamespace[]>(r); return (list ?? []).filter(n => n.scope !== 'app') })
