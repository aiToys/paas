import { api } from '@/lib/http/client'

// -- 平台只读诊断（D4）：版本 / 部署 / 存储迁移 / 依赖可达性 / K8s RBAC / 安全配置状态 --
// 后端契约 internal/diagnostics：安全配置只返回「已配置 / 未配置」状态，绝不回显配置值。
export interface DiagVersion {
  module: string
  version: string
  revision?: string
  modified?: string
  goVersion: string
  platform: string
}

export interface DiagRuntime {
  startedAt: string
  uptimeSeconds: number
  goroutines: number
}

export interface DiagDeploy {
  mode: string // kubernetes | local
  kubeconfigSource: string // in-cluster | PAAS_KUBECONFIG | KUBECONFIG | none
  namespaceScope: string
  serverVersion?: string
  connectivity: string // ok | failed | not_configured
  error?: string
  rbacListWorkloads: string // allowed | denied | unknown
  rbacListNamespaces: string
}

export interface DiagStorage {
  backend: string // postgres | memory
  pingOk: boolean
  latencyMs: number
  migrationVersion: string // unknown = 无法确认
  migrationDirty: boolean
  error?: string
}

export interface DiagDependency {
  name: string
  kind: string
  configured: boolean
  reachable: string // ok | failed | not_configured
  note?: string
}

export interface DiagConfigCheck {
  key: string
  label: string
  status: string
  severity: string // info | warn
  note?: string
}

export interface DiagnosticSnapshot {
  version: DiagVersion
  runtime: DiagRuntime
  deploy: DiagDeploy
  storage: DiagStorage
  dependencies: DiagDependency[]
  security: DiagConfigCheck[]
  notes: string[]
  generatedAt: string
}

export const fetchDiagnostics = () => api.get<DiagnosticSnapshot>('/api/admin/diagnostics')

// -- 智能诊断（只读根因诊断 Agent，2026-10-27）--
// 后端契约 internal/diagnose：LLM 工具循环跨全 ns 采集证据，返回结构化根因报告。
// 同步接口（服务端整体超时 3 分钟），前端超时 200s；degraded 时 verdict.category=unknown
// 且 rawOutput 保留 LLM 原文。
export interface DiagnoseTarget {
  namespace: string
  workload: string
  pod?: string
  service?: string
  symptom?: string // 未知 | CrashLoop | 延迟 | 错误率
}

export interface DiagnoseVerdict {
  severity: string // critical | warning | info
  category: string // code_bug | config_error | resource_exhaustion | dependency_failure | infra | unknown
  summary: string
}

export interface DiagnoseEvidence {
  source: string // 工具名（get_pod_logs/prometheus/...）
  detail: string
  weight: string // high | medium | low
}

export interface DiagnoseHypothesis {
  hypothesis: string
  confidence: number
  evidence: DiagnoseEvidence[]
}

export interface DiagnoseAction {
  action: string
  type: string // config | code
  risk: string // low | medium | high
  requiresApproval: boolean
}

export interface DiagnoseReport {
  verdict: DiagnoseVerdict
  rootCauseHypotheses: DiagnoseHypothesis[]
  recommendedActions: DiagnoseAction[]
  dataSourceCoverage: Record<string, string> // 源名 → ok/empty/missing/error
  confidenceNote: string
  rawOutput?: string
  tokensUsed: number
  durationSeconds: number
}

export interface DiagnoseRequest {
  target: DiagnoseTarget
  timeWindow: { lookbackMinutes: number }
}

// runDiagnose 触发诊断（约 1-3 分钟，同步返回报告；超时单独放行覆盖全局 15s）。
export const runDiagnose = (req: DiagnoseRequest) =>
  api.post<DiagnoseReport>('/api/admin/diagnose/run', req, { timeout: 200_000 })
