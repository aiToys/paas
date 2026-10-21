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
