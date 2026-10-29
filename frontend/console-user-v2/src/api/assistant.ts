// AI 助手 API（spec 2026-10-27）：官方 Agent 目录、会话、异步任务与事件。
// 模式照抄 workload.ts：fetchAuth + {data:T} 解包；202 受理也走 {data}。
import { fetchAuth } from '@/api'
import { asList } from './response'

import type { components } from './assistant.gen'
type Schemas = components['schemas']
export type AgentDesc = Schemas['AgentDesc']
export type AuthorizedTarget = Schemas['AuthorizedTarget']
export type DiagnosticContext = Schemas['DiagnosticContext']
export type ContextCandidate = Schemas['ContextCandidate']
// draft 会话无 target（服务端不下发零值 AuthorizedTarget）；resolved 会话带 target+context。
export type AssistantSession = Omit<Schemas['Session'], 'target'> & {
  target?: AuthorizedTarget
  context?: DiagnosticContext
  candidates?: ContextCandidate[]
  resolutionStatus?: string
  resolutionReason?: string
}
// scoped.Evidence：跨包同名类型经 registry 限定名分离（diagnose.Evidence 保短名）。
export type Evidence = Omit<Schemas['scoped.Evidence'], 'status'> & { status: 'ok' | 'empty' | 'missing' | 'error' }

// —— 上下文解析（spec 2026-10-29）：409 target_resolution_required 结构化负载 ——

export interface AssistantContextInput { kind: 'workload' | 'alert' | 'trace' | 'log'; id: string }

export interface ResolutionRequired {
  status: 'need_choice' | 'need_clarification'
  question: string
  candidates?: ContextCandidate[]
  anchor?: { kind: string; id: string }
}

// 从 createRun/createSession 抛出的错误里提取 409 解析要求（非 409 结构返回 null）。
export const asResolutionRequired = (e: unknown): ResolutionRequired | null => {
  const err = e as Error & { status?: number; body?: { error?: string; resolutionStatus?: string; question?: string; candidates?: ContextCandidate[]; anchor?: { kind: string; id: string } } }
  if (err?.status !== 409 || err.body?.error !== 'target_resolution_required') return null
  return { status: err.body.resolutionStatus === 'need_choice' ? 'need_choice' : 'need_clarification', question: err.body.question || '', candidates: err.body.candidates, anchor: err.body.anchor }
}

export interface UserReport {
  kind: string
  version: string
  verdict: { severity: 'critical' | 'warning' | 'info'; summary: string }
  hypotheses: { hypothesis: string; confidence: number; evidenceIds: string[] }[]
  suggestions: { text: string; destination?: string }[]
  confidenceNote?: string
 dataSourceCoverage?: Record<string, string>
}

export type AssistantRun = Omit<Schemas['RunView'], 'status' | 'quality' | 'report' | 'evidence' | 'usage'> & {
 status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'timed_out'
 quality?: 'sufficient' | 'partial' | 'inconclusive' | 'invalid_output'
 report?: UserReport
 evidence?: Evidence[]
 usage?: {tokens: number; usageSource: 'provider' | 'estimated'}
}
export type AssistantEvent = Omit<Schemas['Event'], 'payload'> & {payload?: Record<string, unknown>}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => null)
  if (!resp.ok) throw Object.assign(new Error(j?.error || `HTTP ${resp.status}`), { status: resp.status, body: j })
  if (!j || typeof j !== 'object' || !('data' in j)) throw new Error('助手响应格式异常，请稍后重试')
  return j.data as T
}

export const listAgents = () =>
  fetchAuth('/api/assistant/agents').then(r => unwrap<unknown>(r)).then(v => asList<AgentDesc>(v))

// target（旧路径兼容）/ context（告警/Trace 深链）都可选：都空 = draft 自然语言会话。
export const createSession = (body: { agentKey: string; target?: { type: string; id: string }; context?: AssistantContextInput; focus?: {podName?: string;podUID?:string;container?: string} }) =>
  fetchAuth('/api/assistant/sessions', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<AssistantSession>(r))

// 确认多候选（candidateId 服务端生成；伪造 404）。确认后会话 resolved。
export const resolveContext = (sessionId: string, candidateId: string) =>
  fetchAuth(`/api/assistant/sessions/${encodeURIComponent(sessionId)}/context/resolve`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ candidateId }),
  }).then(r => unwrap<AssistantSession>(r))

export const listSessions = (before = "") =>
  fetchAuth(`/api/assistant/sessions?limit=20${before ? `&before=${encodeURIComponent(before)}` : ""}`).then(r => unwrap<unknown>(r)).then(v => asList<AssistantSession>(v))

export const getSession = (id: string) =>
  fetchAuth(`/api/assistant/sessions/${id}`).then(r => unwrap<AssistantSession>(r))

// 会话任务列表（诊断历史；时间倒序）。
export const listSessionRuns = (id: string) =>
  fetchAuth(`/api/assistant/sessions/${id}/runs`).then(r => unwrap<unknown>(r)).then(v => asList<AssistantRun>(v))

export const createRun = (sessionId: string, body: { question: string; lookbackMinutes?: number; symptom?: string; mode?: string }, idemKey?: string) =>
  fetchAuth(`/api/assistant/sessions/${sessionId}/runs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(idemKey ? { 'Idempotency-Key': idemKey } : {}) },
    body: JSON.stringify(body),
  }).then(r => unwrap<AssistantRun>(r))

export const getRun = (id: string) =>
  fetchAuth(`/api/assistant/runs/${id}`).then(r => unwrap<AssistantRun>(r))

export const listEvents = (runId: string, afterSeq = 0) =>
  fetchAuth(`/api/assistant/runs/${runId}/events?afterSeq=${afterSeq}`).then(r => unwrap<unknown>(r)).then(v => asList<AssistantEvent>(v))

export const cancelRun = (id: string) =>
  fetchAuth(`/api/assistant/runs/${id}/cancel`, { method: 'POST' }).then(r => unwrap<AssistantRun>(r))

// —— 帮助：事件与状态的中文呈现 ——

export const isTerminal = (s: AssistantRun['status']) => ['completed', 'failed', 'cancelled', 'timed_out'].includes(s)

export const statusLabel: Record<string, string> = {
  queued: '排队中', running: '诊断中', completed: '已完成', failed: '失败', cancelled: '已停止', timed_out: '超时',
}

export const qualityLabel: Record<string, string> = {
  sufficient: '证据充分', partial: '部分证据', inconclusive: '证据不足', invalid_output: '输出无效',
}

export const eventLabel: Record<string, string> = {
  'run.queued': '任务已受理', 'run.started': '开始诊断',
  'tool.started': '采集证据', 'tool.completed': '完成采集',
  'report.ready': '报告就绪', 'run.failed': '诊断失败', 'run.cancelled': '已停止', 'run.timed_out': '诊断超时',
}

export const toolLabel: Record<string, string> = {
  get_workload_status: '运行状态', get_workload_spec: '部署配置', get_k8s_events: 'K8s 事件',
  get_pod_logs: '实例日志', query_loki: '日志检索', get_pod_metrics: '资源指标', get_recent_release: '发布记录',
}

// 所有 Agent 使用相同消息外壳；结构化结果由受控 renderer 按 resultKind 渲染。
export type AssistantMessage = Omit<Schemas['Message'], 'role' | 'status' | 'run'> & {role: 'user' | 'assistant'; status: AssistantRun['status']; run?: AssistantRun}
export type Conversation = Omit<Schemas['Conversation'], 'messages'> & {messages: AssistantMessage[]}
export const listMessages = (id: string, before = '') => fetchAuth(`/api/assistant/sessions/${encodeURIComponent(id)}/messages?limit=20${before ? `&before=${encodeURIComponent(before)}` : ''}`).then(r => unwrap<Conversation>(r))

export const getAssistantSettings = () => fetchAuth('/api/assistant/settings').then(r => unwrap<{persistent: boolean; historyDays: number; evidenceDays: number}>(r))
