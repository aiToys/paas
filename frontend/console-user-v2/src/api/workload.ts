import { asList } from './response'
// 工作负载 API（Workload + lanes 聚合 + 实例/日志），集中端点定义（审计第 10 轮：视图层端点收敛）。
// 模式照抄 change.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface Workload {
  id: string; appId: string; envId: string; laneId: string; service?: string; serviceId?: string
  type: 'service' | 'job' | 'cronjob'; name: string
  image: string; imageRef?: string; replicas: number; ready: number; status: string; createdAt?: string
  schedule?: string; command?: string; port?: number; containerPort?: number; domain?: string
  // metricsPath 非空时平台给 Pod 注入 prometheus.io/scrape 注解（Prometheus 自动发现抓取该路径）。
  // 默认空 = 不注入：应用不暴露 /metrics 时留空，否则会产生永久 down target。
  metricsPath?: string
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
  return (j && typeof j === 'object' && 'data' in j ? j.data : j) as T
}

export const listWorkloads = (params: Record<string, string>) => {
  const q = new URLSearchParams(params).toString()
  return fetchAuth(`/api/workloads${q ? `?${q}` : ''}`).then(r => unwrap<unknown>(r)).then(v => asList<Workload>(v))
}

export const listLanes = () =>
  fetchAuth('/api/workloads/lanes').then(r => unwrap<unknown>(r)).then(v => asList<LaneSummary>(v))

export const getWorkload = (id: string) =>
  fetchAuth(`/api/workloads/${id}`).then(r => unwrap<WorkloadDetail>(r))

export const updateWorkload = (id: string, body: { replicas?: number; status?: string; domain?: string }) =>
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

// ---- 运行态运维（spec 2026-10-17：观察 / 操作 / 终端）----

// 容器三态（waiting/running/terminated + 重启计数与最近退出信息）。
export interface ContainerState {
  name: string; state: 'waiting' | 'running' | 'terminated'; ready: boolean; restarts: number
  reason?: string; message?: string; lastExitCode?: number; lastTerminationReason?: string
}

// 运行实例：Instance 字段 + Pod UID（操作防错目标）+ 容器明细。
export interface RuntimeInstance extends Instance {
  uid?: string; reason?: string; containers?: ContainerState[]; deletionTimestamp?: string
}

// 观察元数据：来源（kubernetes/external/unavailable）+ 新鲜度 + 读取错误。
export interface Observation { source: string; state: string; observedAt?: string; error?: string }

export interface Capability { allowed: boolean; reasonCode?: string; message?: string }
export interface RuntimeCapabilities { logs: Capability; events: Capability; restart: Capability; recreate: Capability; exec: Capability }

// 运维操作记录（restart/recreate 异步状态机：202 受理 ≠ 完成）。
export interface Operation {
  id: string; workloadId: string; kind: 'restart' | 'recreate'; status: string
  reason?: string; actor?: string; podName?: string; replicas?: number; evidence?: string
  acceptedAt: string; updatedAt: string; finishedAt?: string
}

export interface PodEvent { type: string; reason: string; message: string; count?: number; at: string }

export interface Runtime {
  workload: Workload; observation: Observation; instances: RuntimeInstance[]
  capabilities: RuntimeCapabilities; activeOperation?: Operation
}

export const getWorkloadRuntime = (id: string) =>
  fetchAuth(`/api/workloads/${id}/runtime`).then(r => unwrap<Runtime>(r))

export const listWorkloadOperations = (id: string, limit = 20) =>
  fetchAuth(`/api/workloads/${id}/operations?limit=${limit}`).then(r => unwrap<unknown>(r)).then(v => asList<Operation>(v))

export const getWorkloadOperation = (id: string, opId: string) =>
  fetchAuth(`/api/workloads/${id}/operations/${opId}`).then(r => unwrap<Operation>(r))

// 滚动重启：202 只表受理；完成情况由操作记录（activeOperation/operations）轮询。
export const restartWorkload = (id: string, reason: string, idempotencyKey: string) =>
  fetchAuth(`/api/workloads/${id}/restart`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ reason }),
  }).then(r => unwrap<Operation>(r))

export const getPodEvents = (id: string, pod: string) =>
  fetchAuth(`/api/workloads/${id}/instances/${encodeURIComponent(pod)}/events`).then(r => unwrap<unknown>(r)).then(v => asList<PodEvent>(v))

// 实例重建：podUid 不匹配（实例已变化）时后端拒 409，前端提示刷新。
export const recreateInstance = (id: string, pod: string, podUid: string, reason: string, idempotencyKey: string) =>
  fetchAuth(`/api/workloads/${id}/instances/${encodeURIComponent(pod)}/recreate`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ podUid, reason }),
  }).then(r => unwrap<Operation>(r))

export interface TerminalSessionCreated { sessionId: string; expiresAt: string; streamPath: string }

// 终端会话受理：60s 握手窗口内建立 WS（streamPath + cookie 鉴权）。
export const createTerminalSession = (id: string, pod: string, body: { podUid: string; container?: string; shell?: string; reason: string }) =>
  fetchAuth(`/api/workloads/${id}/instances/${encodeURIComponent(pod)}/terminal-sessions`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<TerminalSessionCreated>(r))
