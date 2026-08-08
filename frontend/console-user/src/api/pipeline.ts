// pipeline.ts 流水线类型 + API（对接 Plan 1 已交付的 8 端点）。
// fetchJSON 自动解包 {data:T}；写操作用 fetchAuth + 手动取 .data（需处理 4xx 错误体）。
import { fetchJSON, fetchAuth } from '@/api'

// ---------- 枚举常量 ----------
export type StageType = 'build' | 'deploy' | 'test' | 'approve' | 'promote' | 'baseline'
export type ImageSource = 'priorBuild' | 'selected' | 'latestReady'
export type TestMode = 'smoke' | 'manual'
export type RunStatus = 'running' | 'paused' | 'succeeded' | 'failed' | 'aborted'
export type StageStatus = 'pending' | 'running' | 'success' | 'failed' | 'waiting' | 'skipped'
export type MergeMode = 'squash' | 'ff' | 'rebase'

// stage Params（与后端 model.go 对齐；用宽松 record，设计器按 type 动态渲染字段）
export type StageParams = Record<string, unknown>

// ---------- 领域类型 ----------
export interface StageDef {
  name: string
  type: StageType
  params?: StageParams
}

export interface PipelineTrigger {
  type?: string // 'manual' | 'webhook' | 'cron'（webhook/cron 留 Plan 3）
  branch?: string
}

export interface Pipeline {
  id: string
  tenantId?: string
  appId: string
  name: string
  kind: 'ci' | 'cd'
  templateId?: string
  paramOverrides?: Record<string, unknown>
  trigger: PipelineTrigger
  disabled?: boolean
  createdAt: string
}

export interface PipelineTemplate {
  id: string
  name: string
  kind: 'ci' | 'cd'
  builtin: boolean
  description?: string
  stages: StageDef[]
  params?: ParamDef[]
}

export interface ParamDef {
  name: string
  type?: string
  default?: unknown
  description?: string
}

export interface StageRun {
  index: number
  type: StageType
  name: string
  status: StageStatus
  startedAt?: string
  finishedAt?: string
  input?: Record<string, unknown>
  output?: Record<string, unknown>
  error?: string
}

export interface PipelineRun {
  id: string
  pipelineId: string
  appId: string
  branch: string
  commit?: string
  version?: string
  repoId?: string
  trigger: string
  status: RunStatus
  currentStage: number
  stageRuns: StageRun[]
  createdAt: string
  finishedAt?: string
}

// ---------- API ----------
const listPipelines = (appId: string) =>
  fetchJSON<Pipeline[]>(`/api/applications/${appId}/pipelines`)

const getPipeline = (appId: string, pid: string) =>
  fetchJSON<Pipeline>(`/api/applications/${appId}/pipelines/${pid}`)

async function createPipeline(appId: string, body: {
  name: string; kind: 'ci' | 'cd'; templateId?: string; paramOverrides?: Record<string, unknown>; trigger?: PipelineTrigger
}): Promise<Pipeline> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '创建失败')
  return json.data as Pipeline
}

async function updatePipeline(appId: string, pid: string, p: Pipeline): Promise<Pipeline> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '保存失败')
  return json.data as Pipeline
}

async function deletePipeline(appId: string, pid: string): Promise<void> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}`, { method: 'DELETE' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '删除失败')
  }
}

const listTemplates = () => fetchJSON<PipelineTemplate[]>('/api/pipeline-templates')

async function triggerRun(appId: string, pid: string, body: {
  branch: string; commit?: string; version?: string
}): Promise<PipelineRun> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}/run`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '触发失败')
  return json.data as PipelineRun
}

const getRun = (id: string) => fetchJSON<PipelineRun>(`/api/pipelineruns/${id}`)

const listRuns = (q: { appId?: string; pipelineId?: string; status?: string } = {}) => {
  const qs = new URLSearchParams()
  if (q.appId) qs.set('appId', q.appId)
  if (q.pipelineId) qs.set('pipelineId', q.pipelineId)
  if (q.status) qs.set('status', q.status)
  const s = qs.toString()
  return fetchJSON<PipelineRun[]>(`/api/pipelineruns${s ? '?' + s : ''}`)
}

async function approveStage(runId: string, stageIdx: number): Promise<void> {
  const resp = await fetchAuth(`/api/pipelineruns/${runId}/stages/${stageIdx}/approve`, { method: 'POST' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '审批失败')
  }
}

async function abortRun(runId: string): Promise<void> {
  const resp = await fetchAuth(`/api/pipelineruns/${runId}/abort`, { method: 'POST' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '取消失败')
  }
}

export {
  listPipelines, getPipeline, createPipeline, updatePipeline, deletePipeline,
  listTemplates, triggerRun, getRun, listRuns, approveStage, abortRun,
}
