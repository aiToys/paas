// 工作负载 API（Workload + lanes 聚合 + 实例/日志），集中端点定义（审计第 10 轮：视图层端点收敛）。
// 模式照抄 change.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface Workload {
  id: string; appId: string; envId: string; laneId: string; service?: string
  type: 'service' | 'job' | 'cronjob'; name: string
  image: string; imageRef?: string; replicas: number; ready: number; status: string; createdAt?: string
  schedule?: string; command?: string; port?: number; containerPort?: number; domain?: string
}

export interface Instance {
  name: string; status: string; ready: string; restarts: number
  node?: string; ip?: string; startedAt?: string; message?: string
}

export interface WorkloadDetail { workload: Workload; instances: Instance[] }

export interface LaneSummary { laneId: string; workloadCount: number; applicationCount: number }

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listWorkloads = (params: Record<string, string>) => {
  const q = new URLSearchParams(params).toString()
  return fetchAuth(`/api/workloads${q ? `?${q}` : ''}`).then(r => unwrap<Workload[]>(r))
}

export const listLanes = () =>
  fetchAuth('/api/workloads/lanes').then(r => unwrap<LaneSummary[]>(r))

export const getWorkload = (id: string) =>
  fetchAuth(`/api/workloads/${id}`).then(r => unwrap<WorkloadDetail>(r))

export const updateWorkload = (id: string, body: { replicas?: number; status?: string }) =>
  fetchAuth(`/api/workloads/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<Workload>(r))

export const updateSchedule = (id: string, schedule: string) =>
  fetchAuth(`/api/workloads/${id}/schedule`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ schedule }),
  }).then(r => unwrap<Workload>(r))

export const deleteWorkload = (id: string) =>
  fetchAuth(`/api/workloads/${id}`, { method: 'DELETE' })

export const getWorkloadLogs = (id: string, params: Record<string, string>) => {
  const q = new URLSearchParams(params).toString()
  return fetchAuth(`/api/workloads/${id}/logs?${q}`)
}

export const createWorkload = (appId: string, body: Partial<Workload>) =>
  fetchAuth(`/api/applications/${appId}/workloads`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<Workload>(r))
