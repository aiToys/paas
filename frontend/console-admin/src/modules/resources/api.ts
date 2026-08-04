// 资源总览：跨租户查看所有租户的应用/工作负载/数据服务（super_admin 平台运维视角）。
// 对接 core /api/admin/applications|workloads|dataservices（跨租户，返回对象带 tenantId）。
// 仅读：平台总览不强加写操作（跨租户写越权风险高，资源运维仍在 console-user 租户内进行）。
import { api } from '@/lib/http/client'

export interface AdminApplication {
  id: string
  tenantId: string
  name: string
  env: string
  status: string
  desc?: string
}

export interface AdminWorkload {
  id: string
  tenantId: string
  appId?: string
  envId?: string
  name: string
  type: string
  status: string
  replicas?: number
  ready?: number
  image?: string
}

export interface AdminDataservice {
  id: string
  tenantId: string
  kind: string
  name: string
  status: string
}

export interface ResSearchRequest {
  keyword?: string
  tenantId?: string
  page: number
  size: number
}

export interface ResSearchResponse<T> {
  records: T[]
  total: number
  current: number
  size: number
}

// 假分页 + keyword/tenantId 过滤（core 返全量跨租户列表，前端切片适配 useCrud）。
const filterPage = <T>(
  list: T[] | undefined,
  params: ResSearchRequest,
  match: (item: T) => boolean
): ResSearchResponse<T> => {
  const all = (list ?? []).filter(match)
  const total = all.length
  const start = (params.page - 1) * params.size
  return {
    records: all.slice(start, start + params.size),
    total,
    current: params.page,
    size: params.size
  }
}

const has = (s: string | undefined, kw: string) => !kw || (s ?? '').toLowerCase().includes(kw)

export const fetchAppList = (params: ResSearchRequest) =>
  api.get<AdminApplication[]>('/api/admin/applications').then((list) =>
    filterPage(list, params, (a) =>
      (!params.tenantId || a.tenantId === params.tenantId) &&
      (has(a.id, params.keyword ?? '') || has(a.name, params.keyword ?? ''))
    )
  )

export const fetchWorkloadList = (params: ResSearchRequest) =>
  api.get<AdminWorkload[]>('/api/admin/workloads').then((list) =>
    filterPage(list, params, (w) =>
      (!params.tenantId || w.tenantId === params.tenantId) &&
      (has(w.id, params.keyword ?? '') || has(w.name, params.keyword ?? '') || has(w.type, params.keyword ?? ''))
    )
  )

export const fetchDataserviceList = (params: ResSearchRequest) =>
  api.get<AdminDataservice[]>('/api/admin/dataservices').then((list) =>
    filterPage(list, params, (d) =>
      (!params.tenantId || d.tenantId === params.tenantId) &&
      (has(d.id, params.keyword ?? '') || has(d.name, params.keyword ?? '') || has(d.kind, params.keyword ?? ''))
    )
  )

// -- 环境 --
export interface AdminEnvironment {
  id: string
  tenantId: string
  name: string
  type: string
  cluster?: string
  desc?: string
  createdAt?: string
}
export const fetchEnvironmentList = (params: ResSearchRequest) =>
  api.get<AdminEnvironment[]>('/api/admin/environments').then((list) =>
    filterPage(list, params, (e) =>
      (!params.tenantId || e.tenantId === params.tenantId) &&
      (has(e.id, params.keyword ?? '') || has(e.name, params.keyword ?? '') || has(e.type, params.keyword ?? ''))
    )
  )

// -- DevOps：构建/镜像/发布 --
export interface AdminBuildRun {
  id: string
  tenantId: string
  appId: string
  repoId: string
  trigger: string
  commit: string
  branch: string
  message: string
  status: string
  imageId?: string
  startedAt: string
  finishedAt?: string
}
export const fetchBuildRunList = (params: ResSearchRequest) =>
  api.get<AdminBuildRun[]>('/api/admin/buildruns').then((list) =>
    filterPage(list, params, (b) =>
      (!params.tenantId || b.tenantId === params.tenantId) &&
      (has(b.id, params.keyword ?? '') || has(b.appId, params.keyword ?? '') || has(b.status, params.keyword ?? ''))
    )
  )

export interface AdminImage {
  id: string
  tenantId: string
  appId: string
  registry: string
  tag: string
  digest: string
  source: string
  branch: string
  buildRunId: string
  builtAt: string
  status: string
}
export const fetchImageList = (params: ResSearchRequest) =>
  api.get<AdminImage[]>('/api/admin/images').then((list) =>
    filterPage(list, params, (im) =>
      (!params.tenantId || im.tenantId === params.tenantId) &&
      (has(im.id, params.keyword ?? '') || has(im.tag, params.keyword ?? '') || has(im.appId, params.keyword ?? ''))
    )
  )

export interface AdminRelease {
  id: string
  tenantId: string
  appId: string
  envId: string
  imageId: string
  imageDigest: string
  strategy: string
  status: string
  workloadId: string
  previousImageId?: string
  isRollback: boolean
  createdAt: string
  createdBy: string
}
export const fetchReleaseList = (params: ResSearchRequest) =>
  api.get<AdminRelease[]>('/api/admin/releases').then((list) =>
    filterPage(list, params, (r) =>
      (!params.tenantId || r.tenantId === params.tenantId) &&
      (has(r.id, params.keyword ?? '') || has(r.appId, params.keyword ?? '') || has(r.status, params.keyword ?? ''))
    )
  )

// -- 配置中心 --
export interface AdminNamespace {
  id: string
  tenantId: string
  name: string
  desc?: string
  updatedAt: string
}
export const fetchNamespaceList = (params: ResSearchRequest) =>
  api.get<AdminNamespace[]>('/api/admin/namespaces').then((list) =>
    filterPage(list, params, (n) =>
      (!params.tenantId || n.tenantId === params.tenantId) &&
      (has(n.id, params.keyword ?? '') || has(n.name, params.keyword ?? ''))
    )
  )

// -- 服务治理 --
export interface AdminService {
  id: string
  tenantId: string
  name: string
  appId?: string
  envId: string
  protocol: string
  port: number
  desc?: string
  updatedAt: string
}
export const fetchServiceList = (params: ResSearchRequest) =>
  api.get<AdminService[]>('/api/admin/services').then((list) =>
    filterPage(list, params, (s) =>
      (!params.tenantId || s.tenantId === params.tenantId) &&
      (has(s.id, params.keyword ?? '') || has(s.name, params.keyword ?? '') || has(s.protocol, params.keyword ?? ''))
    )
  )

// -- 可观测：告警规则 --
export interface AdminAlertRule {
  id: string
  tenantId: string
  name: string
  metricName: string
  targetType: string
  targetId?: string
  operator: string
  threshold: number
  severity: string
  enabled: boolean
  updatedAt: string
}
export const fetchAlertRuleList = (params: ResSearchRequest) =>
  api.get<AdminAlertRule[]>('/api/admin/alert-rules').then((list) =>
    filterPage(list, params, (a) =>
      (!params.tenantId || a.tenantId === params.tenantId) &&
      (has(a.id, params.keyword ?? '') || has(a.name, params.keyword ?? '') || has(a.metricName, params.keyword ?? ''))
    )
  )

// -- 安全：密钥/审计 --
export interface AdminSecret {
  id: string
  tenantId: string
  name: string
  type: string
  scope: string
  value: string
  desc?: string
  updatedAt: string
}
export const fetchSecretList = (params: ResSearchRequest) =>
  api.get<AdminSecret[]>('/api/admin/secrets').then((list) =>
    filterPage(list, params, (s) =>
      (!params.tenantId || s.tenantId === params.tenantId) &&
      (has(s.id, params.keyword ?? '') || has(s.name, params.keyword ?? '') || has(s.type, params.keyword ?? ''))
    )
  )

export interface AdminAuditLog {
  id: string
  tenantId: string
  actor: string
  action: string
  resourceType: string
  resourceId: string
  detail?: string
  at: string
}
export const fetchAuditLogList = (params: ResSearchRequest) =>
  api.get<AdminAuditLog[]>('/api/admin/audit-logs').then((list) =>
    filterPage(list, params, (l) =>
      (!params.tenantId || l.tenantId === params.tenantId) &&
      (has(l.actor, params.keyword ?? '') || has(l.action, params.keyword ?? '') || has(l.resourceType, params.keyword ?? ''))
    )
  )

// -- 计费：配额/账单 --
export interface AdminQuota {
  id: string // tenantId 映射，供 useCrud row-key
  tenantId: string
  limits: Record<string, number>
  updatedAt: string
}
export const fetchQuotaList = (params: ResSearchRequest) =>
  api.get<AdminQuota[]>('/api/admin/quotas').then((list) =>
    filterPage(
      list.map((q) => ({ ...q, id: q.tenantId })),
      params,
      (q) => (!params.tenantId || q.tenantId === params.tenantId) && has(q.tenantId, params.keyword ?? '')
    )
  )

export interface AdminBill {
  id: string
  tenantId: string
  period: string
  items?: Array<{ resource: string; quantity: number; unitPrice: number; amount: number }>
  total: number
  status: string
  createdAt: string
  paidAt?: string
}
export const fetchBillList = (params: ResSearchRequest) =>
  api.get<AdminBill[]>('/api/admin/bills').then((list) =>
    filterPage(list, params, (b) =>
      (!params.tenantId || b.tenantId === params.tenantId) &&
      (has(b.id, params.keyword ?? '') || has(b.period, params.keyword ?? '') || has(b.status, params.keyword ?? ''))
    )
  )
