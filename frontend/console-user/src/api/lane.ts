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
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listLanes = (envId?: string) =>
  fetchAuth(`/api/lanes${envId ? `?envId=${encodeURIComponent(envId)}` : ''}`).then(r => unwrap<Lane[]>(r))

export const getLane = (id: string) =>
  fetchAuth(`/api/lanes/${id}`).then(r => unwrap<LaneDetail>(r))

export const createLane = (body: Pick<Lane, 'envId' | 'name' | 'mode'> & Partial<Pick<Lane, 'description' | 'externalLink'>>) =>
  fetchAuth('/api/lanes', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<Lane>(r))

export const updateLane = (id: string, body: Partial<Pick<Lane, 'mode' | 'description' | 'externalLink'>>) =>
  fetchAuth(`/api/lanes/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<Lane>(r))

export const closeLane = (id: string) =>
  fetchAuth(`/api/lanes/${id}`, { method: 'DELETE' }).then(r => unwrap<Lane>(r))
