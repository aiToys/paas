import { asList } from './response'
// 泳道实体 API（一等实体：创建/关闭/详情聚合）。模式照抄 workload.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'
import type { Workload } from '@/api/workload'

export interface Lane {
  id: string
  envId: string
  name: string
  mode: 'standard' | 'permanent'
  status: 'active' | 'closed'
  weight: number
  externalLink?: string
  description?: string
  createdAt?: string
  updatedAt?: string
}

export interface RunSummary {
  id: string
  appId: string
  pipelineId: string
  branch: string
  lane?: string // 触发时固化的显式部署泳道（真实目标关联）
  status: string
  createdAt: string
  finishedAt?: string
}

export interface LaneDetail {
  lane: Lane
  workloads: Workload[]
  recentRuns: RunSummary[]
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => null)
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return (j && typeof j === 'object' && 'data' in j ? j.data : j) as T
}

export const listLanes = (envId?: string) =>
  fetchAuth(`/api/lanes${envId ? `?envId=${encodeURIComponent(envId)}` : ''}`).then(r => unwrap<unknown>(r)).then(v => asList<Lane>(v))

export const getLane = (id: string) =>
  fetchAuth(`/api/lanes/${id}`).then(r => unwrap<LaneDetail>(r))

export const createLane = (body: Pick<Lane, 'envId' | 'name' | 'mode'> & Partial<Pick<Lane, 'description' | 'externalLink'>>) =>
  fetchAuth('/api/lanes', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<Lane>(r))

/** 关闭泳道结构化结果：生命周期与清理分离——部分回收失败不上报假成功，failedIds 可重试。 */
export interface CloseResult {
  lane: Lane
  reclaimed: number
  failedIds?: string[]
  error?: string
}

export const closeLane = (id: string) =>
  fetchAuth(`/api/lanes/${id}`, { method: 'DELETE' }).then(r => unwrap<CloseResult>(r))

/** 访问地址分类（business 业务入口 / preview 泳道联调入口 / internal 集群内 FQDN）。 */
export interface AccessEndpoint {
  kind: 'business' | 'preview' | 'internal'
  url: string
  target: string
  name?: string
  lane: string
  port?: number
  source: 'domain' | 'fqdn'
  note?: string
}

export interface LaneAccess {
  lane: Lane
  endpoints: AccessEndpoint[]
}

export const getLaneAccess = (id: string) =>
  fetchAuth(`/api/lanes/${id}/access`).then(r => unwrap<LaneAccess>(r))
