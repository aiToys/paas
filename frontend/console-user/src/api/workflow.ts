// 工作流编排 API（智能体工作流：定义 CRUD + 运行触发/历史/审批/中止）。
// 模式与 lane.ts 一致：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface NodeConfig {
  agentId?: string
  inputTemplate?: string
  toolId?: string
  toolName?: string
  args?: Record<string, string>
  message?: string
}

export interface Branch {
  when: string
  nextId: string
}

export interface NodeDef {
  id: string
  type: 'start' | 'llm' | 'tool' | 'condition' | 'approve' | 'end'
  name?: string
  nextId?: string
  config?: NodeConfig
  branches?: Branch[]
  elseId?: string
}

export interface WorkflowDef {
  id: string
  name: string
  desc?: string
  nodes: NodeDef[]
  enabled: boolean
  createdAt?: string
  updatedAt?: string
}

export interface NodeRun {
  nodeId: string
  status: string
  output?: string
  error?: string
  startedAt: string
  finishedAt?: string
}

export interface WorkflowRun {
  id: string
  workflowId: string
  status: 'running' | 'paused' | 'succeeded' | 'failed' | 'aborted'
  inputs: Record<string, string>
  nodeRuns: NodeRun[]
  createdAt: string
  finishedAt?: string
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

const unwrapNoContent = async (resp: Response): Promise<void> => {
  if (resp.status === 204) return
  const j = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
}

export const listWorkflows = () =>
  fetchAuth('/api/workflows').then(r => unwrap<WorkflowDef[]>(r))

export const getWorkflow = (id: string) =>
  fetchAuth(`/api/workflows/${id}`).then(r => unwrap<WorkflowDef>(r))

export const createWorkflow = (body: Omit<WorkflowDef, 'id'>) =>
  fetchAuth('/api/workflows', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<WorkflowDef>(r))

export const updateWorkflow = (id: string, body: Omit<WorkflowDef, 'id'>) =>
  fetchAuth(`/api/workflows/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<WorkflowDef>(r))

export const deleteWorkflow = (id: string) =>
  fetchAuth(`/api/workflows/${id}`, { method: 'DELETE' }).then(r => unwrap<{ deleted: string }>(r))

export const triggerRun = (id: string, inputs: Record<string, string>) =>
  fetchAuth(`/api/workflows/${id}/runs`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(inputs),
  }).then(r => unwrap<WorkflowRun>(r))

export const listRuns = (workflowId: string) =>
  fetchAuth(`/api/workflows/${workflowId}/runs`).then(r => unwrap<WorkflowRun[]>(r))

export const getRun = (runId: string) =>
  fetchAuth(`/api/workflows/runs/${runId}`).then(r => unwrap<WorkflowRun>(r))

export const approveRun = (runId: string, node: string) =>
  fetchAuth(`/api/workflows/runs/${runId}/approve?node=${encodeURIComponent(node)}`, { method: 'POST' })
    .then(unwrapNoContent)

export const abortRun = (runId: string) =>
  fetchAuth(`/api/workflows/runs/${runId}/abort`, { method: 'POST' }).then(unwrapNoContent)
