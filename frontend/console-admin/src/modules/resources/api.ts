// 资源总览：跨租户查看所有租户的应用/工作负载/数据服务（super_admin 平台运维视角）。
// 对接 core /api/admin/applications|workloads|dataservices（跨租户，返回对象带 tenantId）。
// 仅读：平台总览不强加写操作（跨租户写越权风险高，资源运维仍在 console-user 租户内进行）。
import { api, http } from '@/lib/http/client'

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
  engineId?: string
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

// 应用成员（应用级权限）：跨租户总览（super_admin 只读观测）
export interface AdminAppMember {
  id: string
  tenantId: string
  appId: string
  userId: string
  userName?: string
  role: string
  createdAt?: string
}

export const fetchAppMemberList = () => api.get<AdminAppMember[]>('/api/admin/app-members')

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
  log?: string
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

// -- 流水线运行（PipelineRun 跨租户总览）--
export interface AdminPipelineRun {
  id: string
  tenantId: string
  appId: string
  pipelineId: string
  branch: string
  commit: string
  status: string // running / paused / succeeded / failed / aborted
  currentStage: string
  version?: string
  createdAt: string
  finishedAt?: string
}
export const fetchPipelineRunList = (params: ResSearchRequest) =>
  api.get<AdminPipelineRun[]>('/api/admin/pipelineruns').then((list) =>
    filterPage(list, params, (r) =>
      (!params.tenantId || r.tenantId === params.tenantId) &&
      (has(r.id, params.keyword ?? '') || has(r.appId, params.keyword ?? '') ||
        has(r.status, params.keyword ?? '') || has(r.pipelineId, params.keyword ?? ''))
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

// ============================================================================
// admin 管理 API（L1 详情+实例 / L2 运维+删 / L3 代建）—— 数据服务 + 环境样板
// 对接 /api/admin/dataservices* + /api/admin/environments（POST 代建）。
// ============================================================================

// -- 租户下拉（代建选归属租户）--
export interface AdminTenant {
  id: string
  name: string
}
export const fetchAllTenants = () => api.get<AdminTenant[]>('/api/admin/tenants')

// -- 数据服务详情（资源 + 运行实例）--
export interface DataserviceInstance {
  name: string
  ip: string
  port: number
}
export interface AdminDataserviceDetail {
  resource: AdminDataservice & {
    engineId?: string
    source?: string
    replicas?: number
    cpu?: string
    memory?: string
    storageGb?: number
    envId?: string
    spec?: Record<string, string>
    connection?: Record<string, string>
    createdAt?: string
  }
  instances: DataserviceInstance[]
}

export const fetchDataserviceDetail = (id: string) =>
  api.get<AdminDataserviceDetail>(`/api/admin/dataservices/${id}`)

// start/stop/restart 统一 action
export const dataserviceAction = (id: string, action: 'start' | 'stop' | 'restart') =>
  api.post<unknown>(`/api/admin/dataservices/${id}/${action}`, {})

export const scaleDataservice = (
  id: string,
  body: { replicas?: number; cpu?: string; memory?: string; storageGb?: number }
) => api.put<unknown>(`/api/admin/dataservices/${id}/scale`, body)

export const deleteDataservice = (id: string) => api.del<unknown>(`/api/admin/dataservices/${id}`)

export const createDataserviceForTenant = (body: {
  tenantId: string
  id?: string
  name: string
  engineId: string
  envId?: string
  replicas?: number
  cpu?: string
  memory?: string
  storageGb?: number
  connection?: Record<string, string>
}) => api.post<unknown>('/api/admin/dataservices', body)

// -- 环境代建 --
export const createEnvironmentForTenant = (body: {
  tenantId: string
  id?: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
  desc?: string
}) => api.post<unknown>('/api/admin/environments', body)

// ============================================================================
// admin 工作负载管理（详情+实例+日志 / 扩缩容 / 删）
// 对接 /api/admin/workloads*。
// ============================================================================

export interface WorkloadInstance {
  name: string
  status?: string
  ready?: string
  restarts?: number
  node?: string
  ip?: string
  startedAt?: string
  message?: string
}

export interface AdminWorkloadDetail {
  workload: AdminWorkload & {
    laneId?: string
    imageRef?: string
    schedule?: string
    command?: string
    port?: number
    containerPort?: number
    createdAt?: string
  }
  instances: WorkloadInstance[]
}

export const fetchWorkloadDetail = (id: string) =>
  api.get<AdminWorkloadDetail>(`/api/admin/workloads/${id}`)

export const scaleWorkload = (id: string, body: { replicas: number; status?: string }) =>
  api.put<unknown>(`/api/admin/workloads/${id}/scale`, body)

export const deleteWorkload = (id: string) => api.del<unknown>(`/api/admin/workloads/${id}`)

// 实例日志（text/plain，非 {data:T} 契约，需 responseType 支持）。返回纯文本。
export const fetchWorkloadLogs = (id: string, params: { pod: string; tail?: number; previous?: boolean }) =>
  http
    .get<string>(`/api/admin/workloads/${id}/logs`, {
      params,
      responseType: 'text',
      transformResponse: (data) => data
    })
    .then((res) => res.data)

// ============================================================================
// admin 应用管理（详情 / 删）
// 对接 /api/admin/applications*。
// ============================================================================

export interface AdminApplicationDetail extends AdminApplication {
  initial?: string
  gradient?: string
  resources?: { models: number; mq: number; dal: number }
  bindings?: Array<{ type: string; name: string; note?: string }>
  replicas?: string
  rps?: string
}

export const fetchApplicationDetail = (id: string) =>
  api.get<AdminApplicationDetail>(`/api/admin/applications/${id}`)

export const deleteApplication = (id: string) => api.del<unknown>(`/api/admin/applications/${id}`)

// ============================================================================
// admin DevOps 管理（构建/镜像/发布 详情 + 回滚）
// 对接 /api/admin/buildruns|images|releases/{id}*。
// BuildRun/Image/Release Repository 无 Delete 方法 -> 不提供删除；
// BuildRun 重试涉及异步构建流转，admin 路径不干净复用，YAGNI 跳过。
// ============================================================================

// -- 构建详情（含 Log）--
export type AdminBuildRunDetail = AdminBuildRun

export const fetchBuildRunDetail = (id: string) =>
  api.get<AdminBuildRunDetail>(`/api/admin/buildruns/${id}`)

// -- 镜像详情 --
export type AdminImageDetail = AdminImage

export const fetchImageDetail = (id: string) =>
  api.get<AdminImageDetail>(`/api/admin/images/${id}`)

// -- 发布详情 + 回滚 --
export type AdminReleaseDetail = AdminRelease

export const fetchReleaseDetail = (id: string) =>
  api.get<AdminReleaseDetail>(`/api/admin/releases/${id}`)

// 回滚发布（绕过 prod:write，记审计；返回新建的回滚 release）。
export const rollbackRelease = (id: string) =>
  api.post<AdminRelease>(`/api/admin/releases/${id}/rollback`, {})

// ============================================================================
// admin 治理/可观测/计费/安全 管理（P4：详情 + 运维操作 + 删）
// 对接 /api/admin/services|alert-rules|quotas|bills|secrets/{id}*。
// 全挂 adminGuard(super_admin)；绕过 prod:write；写操作记审计；密钥掩码。
// ============================================================================

// -- 服务治理：服务详情（含实例）+ 注销实例 + 删服务 --
export interface AdminServiceInstance {
  id: string
  tenantId: string
  serviceId: string
  addr: string
  status: string
  laneId: string
  updatedAt: string
}
export interface AdminServiceDetail {
  service: AdminService
  instances: AdminServiceInstance[]
}
export const fetchServiceDetail = (id: string) =>
  api.get<AdminServiceDetail>(`/api/admin/services/${id}`)
export const deregisterServiceInstance = (serviceId: string, instanceId: string) =>
  api.del<unknown>(`/api/admin/services/${serviceId}/instances/${instanceId}`)
// -- 工作负载 drift 修复：PG 有行无 CRD 的 Workload 补投影 --
export const reconcileWorkloads = () =>
  api.post<Record<string, number>>('/api/admin/workloads/reconcile')

// -- 环境详情（跨租户）--
export const fetchEnvironmentDetail = (id: string) =>
  api.get<AdminEnvironment>(`/api/admin/environments/${id}`)

// -- 流水线运行详情（跨租户；复用 GET /api/admin/pipelineruns/{id}）--
export interface AdminPipelineRunDetail {
  id: string
  tenantId?: string
  appId: string
  pipelineId: string
  branch: string
  commit: string
  status: string
  currentStage: string
  version?: string
  stageRuns?: Array<{
    stage: string
    name?: string
    status: string
    input?: Record<string, unknown>
    output?: Record<string, unknown>
    log?: string
    error?: string
    startedAt?: string
    finishedAt?: string
  }>
  createdAt: string
  finishedAt?: string
}
export const fetchPipelineRunDetail = (id: string) =>
  api.get<AdminPipelineRunDetail>(`/api/admin/pipelineruns/${id}`)
export const deleteService = (id: string) => api.del<unknown>(`/api/admin/services/${id}`)

// -- 可观测：告警规则详情 + 删 --
export type AdminAlertRuleDetail = AdminAlertRule
export const fetchAlertRuleDetail = (id: string) =>
  api.get<AdminAlertRuleDetail>(`/api/admin/alert-rules/${id}`)
export const deleteAlertRule = (id: string) => api.del<unknown>(`/api/admin/alert-rules/${id}`)

// -- 计费：配额调整 + 账单详情 + 标记已付 --
export const setQuotaForTenant = (body: { tenantId: string; limits: Record<string, number> }) =>
  api.put<unknown>('/api/admin/quotas', body)
export type AdminBillDetail = AdminBill
export const fetchBillDetail = (id: string) => api.get<AdminBillDetail>(`/api/admin/bills/${id}`)
export const payBill = (id: string) => api.post<unknown>(`/api/admin/bills/${id}/pay`, {})

// -- 安全：密钥详情（掩码）+ 删 --
export type AdminSecretDetail = AdminSecret
export const fetchSecretDetail = (id: string) =>
  api.get<AdminSecretDetail>(`/api/admin/secrets/${id}`)
export const deleteSecret = (id: string) => api.del<unknown>(`/api/admin/secrets/${id}`)


// -- AI 编排：跨租户只读总览（Agent/工具/知识库/提示词/Skill）--
export interface AdminAgent {
  id: string
  tenantId: string
  name: string
  description?: string
  model: string
  systemPrompt?: string
  promptRef?: string
  tools?: string[] | null
  knowledgeBases?: string[] | null
  skills?: string[] | null
  maxSteps?: number
  enabled: boolean
  createdAt?: string
}
export interface AdminTool {
  id: string
  tenantId: string
  name: string
  description?: string
  type: string
  enabled: boolean
  createdAt?: string
}
export interface AdminKnowledgeBase {
  id: string
  tenantId: string
  name: string
  embeddingModel: string
  embeddingDim?: number
  createdAt?: string
}
export interface AdminPrompt {
  id: string
  tenantId: string
  name: string
  template?: string
  version?: number
  active?: boolean
  createdAt?: string
}
export interface AdminSkill {
  id: string
  tenantId: string
  name: string
  description?: string
  instructions?: string
  enabled: boolean
  createdAt?: string
}

export const fetchAiAgentList = (params: ResSearchRequest) =>
  api.get<AdminAgent[]>('/api/admin/ai/agents').then((list) =>
    filterPage(list, params, (a) =>
      (!params.tenantId || a.tenantId === params.tenantId) &&
      (has(a.id, params.keyword ?? '') || has(a.name, params.keyword ?? '') || has(a.model, params.keyword ?? ''))
    )
  )
export const fetchAiToolList = (params: ResSearchRequest) =>
  api.get<AdminTool[]>('/api/admin/ai/tools').then((list) =>
    filterPage(list, params, (t) =>
      (!params.tenantId || t.tenantId === params.tenantId) &&
      (has(t.id, params.keyword ?? '') || has(t.name, params.keyword ?? '') || has(t.type, params.keyword ?? ''))
    )
  )
export const fetchAiKnowledgeBaseList = (params: ResSearchRequest) =>
  api.get<AdminKnowledgeBase[]>('/api/admin/ai/knowledgebases').then((list) =>
    filterPage(list, params, (k) =>
      (!params.tenantId || k.tenantId === params.tenantId) &&
      (has(k.id, params.keyword ?? '') || has(k.name, params.keyword ?? ''))
    )
  )
export const fetchAiPromptList = (params: ResSearchRequest) =>
  api.get<AdminPrompt[]>('/api/admin/ai/prompts').then((list) =>
    filterPage(list, params, (p) =>
      (!params.tenantId || p.tenantId === params.tenantId) &&
      (has(p.id, params.keyword ?? '') || has(p.name, params.keyword ?? ''))
    )
  )
export const fetchAiSkillList = (params: ResSearchRequest) =>
  api.get<AdminSkill[]>('/api/admin/ai/skills').then((list) =>
    filterPage(list, params, (s) =>
      (!params.tenantId || s.tenantId === params.tenantId) &&
      (has(s.id, params.keyword ?? '') || has(s.name, params.keyword ?? ''))
    )
  )

// —— AI 编排广场总览（super_admin 只读；下架走 DELETE /api/marketplace/{id} 需登录用户端凭证，admin 页一期只读发现）——
export interface AdminMarketItem {
  id: string
  entityType: string
  name: string
  description: string
  category: string
  publisherTenant: string
  publisherName?: string
  installs: number
  createdAt: string
}

export const fetchAiMarketList = (params: ResSearchRequest) =>
  api.get<AdminMarketItem[]>('/api/admin/ai/marketplace').then((list) =>
    filterPage(list, params, (m) =>
      (!params.tenantId || m.publisherTenant === params.tenantId) &&
      (has(m.id, params.keyword ?? '') || has(m.name, params.keyword ?? '') || has(m.entityType, params.keyword ?? ''))
    )
  )
