// 可观测 API（metrics/alerts/logs/traces + 维度过滤），集中端点定义（审计第 10 轮）。
// 模式照抄 change.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface MetricPoint { ts: string; value: number }
export interface MetricSeries {
  id: string; targetType: string; targetId: string; name: string
  unit?: string; current: number; points: MetricPoint[]
}
export interface AlertRule {
  id: string; name: string; metricName: string; targetType: string; targetId: string
  operator: string; threshold: number; severity: string; enabled: boolean
}
export interface Alert {
  ruleId: string; ruleName: string; targetType?: string; targetId: string
  metricName: string; value: number; threshold: number; operator?: string
  severity: string; status: string
}
export interface LogEntry {
  id: string; appId: string; targetType?: string; targetId?: string
  level: string; message: string; traceId: string; timestamp: string
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listMetrics = (params: Record<string, string>) =>
  fetchAuth(`/api/observability/metrics?${new URLSearchParams(params).toString()}`).then(r => unwrap<MetricSeries[]>(r))

export const listAlertRules = () =>
  fetchAuth('/api/observability/alert-rules').then(r => unwrap<AlertRule[]>(r))

export const createAlertRule = (body: Partial<AlertRule>) =>
  fetchAuth('/api/observability/alert-rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<AlertRule>(r))

export const deleteAlertRule = (id: string) =>
  fetchAuth(`/api/observability/alert-rules/${id}`, { method: 'DELETE' })

export const listAlerts = (params: Record<string, string> = {}) => {
  const q = new URLSearchParams(params).toString()
  return fetchAuth(`/api/observability/alerts${q ? `?${q}` : ''}`).then(r => unwrap<Alert[]>(r))
}

export const listLogs = (params: Record<string, string>) =>
  fetchAuth(`/api/observability/logs?${new URLSearchParams(params).toString()}`).then(r => unwrap<LogEntry[]>(r))

// traces 列表与单条按 ID 查（形状由调用方声明：Trace 含 spans 树）。
export const listTraces = <T>(params: Record<string, string>) =>
  fetchAuth(`/api/observability/traces?${new URLSearchParams(params).toString()}`).then(r => unwrap<T[]>(r))

export const getTrace = <T>(id: string) =>
  fetchAuth(`/api/observability/traces/${encodeURIComponent(id)}`).then(r => unwrap<T>(r))
