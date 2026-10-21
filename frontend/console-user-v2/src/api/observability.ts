import { asList } from './response'
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
  webhookUrl?: string // 直连地址（存量兼容）；channelIds 非空时不再使用
  channelIds?: string[] // 引用通知渠道（非空时忽略 webhookUrl）
}
// 通知渠道（租户级）：告警派发目标 + 抑制策略（停用/静默截止/重复提醒冷却）。
// 服务端返回的 url 与 headers 一律掩码，编辑时回传掩码占位表示保留原值。
export interface NotificationChannel {
  id: string; name: string; kind: string
  url: string; headers?: Record<string, string>
  enabled: boolean; cooldownMinutes: number; muteUntil?: string | null
  createdAt?: string; updatedAt?: string
}
export interface Alert {
  ruleId: string; ruleName: string; targetType?: string; targetId: string
  metricName: string; value: number; threshold: number; operator?: string
  severity: string; status: string
}
// 告警历史事件（firing/resolved 转变落库，时间倒序；PG 持久化路径）。
export interface AlertEvent {
  id: string; ruleId: string; ruleName: string; targetType: string; targetId: string
  metricName: string; value: number; threshold: number; operator: string
  severity: string; status: string; firedAt: string; occurredAt: string
}
export interface LogEntry {
  id: string; appId: string; targetType?: string; targetId?: string
  level: string; message: string; traceId: string; timestamp: string
}

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => null)
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return (j && typeof j === 'object' && 'data' in j ? j.data : j) as T
}

export const listMetrics = (params: Record<string, string>) =>
  fetchAuth(`/api/observability/metrics?${new URLSearchParams(params).toString()}`).then(r => unwrap<unknown>(r)).then(v => asList<MetricSeries>(v))

// 采集质量诊断（spec 专项04 §5）：逐 metric 即时探测 ok/empty/failed。
// status=empty 表示查询成功无数据（检查时间范围/目标/埋点——非平台故障）；
// failed 才是采集源问题。source=memory 表示未接真实采集源（演示数据）。
export interface MetricQuality {
  metric: string; status: 'ok' | 'empty' | 'failed'; sampleSeries?: number
  error?: string; checkedAt: string
}
export interface QualityReport {
  source: 'prometheus' | 'memory'; note?: string; metrics: MetricQuality[]
}
export const probeMetricsQuality = (targetType: string, targetId: string) =>
  fetchAuth(`/api/observability/metrics/quality?targetType=${encodeURIComponent(targetType)}&targetId=${encodeURIComponent(targetId)}`).then(r => unwrap<QualityReport>(r))

export const listAlertRules = () =>
  fetchAuth('/api/observability/alert-rules').then(r => unwrap<unknown>(r)).then(v => asList<AlertRule>(v))

export const createAlertRule = (body: Partial<AlertRule>) =>
  fetchAuth('/api/observability/alert-rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<AlertRule>(r))

// 更新规则（PUT 保持规则 ID 不变——删了重建会断开告警历史关联）。
export const updateAlertRule = (id: string, body: Partial<AlertRule>) =>
  fetchAuth(`/api/observability/alert-rules/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<AlertRule>(r))

export const deleteAlertRule = (id: string) =>
  fetchAuth(`/api/observability/alert-rules/${id}`, { method: 'DELETE' })

// ---- 通知渠道（URL/Headers 服务端掩码；PUT 回传掩码占位=保留原值） ----
export const listChannels = () =>
  fetchAuth('/api/observability/channels').then(r => unwrap<unknown>(r)).then(v => asList<NotificationChannel>(v))

export const createChannel = (body: Partial<NotificationChannel>) =>
  fetchAuth('/api/observability/channels', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<NotificationChannel>(r))

export const updateChannel = (id: string, body: Partial<NotificationChannel>) =>
  fetchAuth(`/api/observability/channels/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(r => unwrap<NotificationChannel>(r))

export const deleteChannel = (id: string) =>
  fetchAuth(`/api/observability/channels/${id}`, { method: 'DELETE' })

export const listAlerts = (params: Record<string, string> = {}) => {
  const q = new URLSearchParams(params).toString()
  return fetchAuth(`/api/observability/alerts${q ? `?${q}` : ''}`).then(r => unwrap<unknown>(r)).then(v => asList<Alert>(v))
}

export const listAlertEvents = (limit = 50) =>
  fetchAuth(`/api/observability/alerts/events?limit=${limit}`).then(r => unwrap<unknown>(r)).then(v => asList<AlertEvent>(v))

export const listLogs = (params: Record<string, string>) =>
  fetchAuth(`/api/observability/logs?${new URLSearchParams(params).toString()}`).then(r => unwrap<unknown>(r)).then(v => asList<LogEntry>(v))

// traces 列表与单条按 ID 查（形状由调用方声明：Trace 含 spans 树）。
export const listTraces = <T>(params: Record<string, string>) =>
  fetchAuth(`/api/observability/traces?${new URLSearchParams(params).toString()}`).then(r => unwrap<unknown>(r)).then(v => asList<T>(v))

export const getTrace = <T>(id: string) =>
  fetchAuth(`/api/observability/traces/${encodeURIComponent(id)}`).then(r => unwrap<T>(r))
