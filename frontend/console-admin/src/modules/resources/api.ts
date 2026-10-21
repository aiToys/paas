// 资源总览：跨租户查看所有租户的应用/工作负载/数据服务（super_admin 平台运维视角）。
// 对接 core /api/admin/applications|workloads|dataservices（跨租户，返回对象带 tenantId）。
// 仅读：平台总览不强加写操作（跨租户写越权风险高，资源运维仍在 console-user 租户内进行）。
import { api, http } from '@/lib/http/client'
import { withScope } from '@/app/stores/scope'

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
export const filterPage = <T>(
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

// 工作负载/当前告警返回完整数组；流水线先服务端筛失败和租户，再取最近运行。
// 后续服务端分页迁移时须同时调整此契约，禁止把分页对象当作“无异常”。
async function fetchAdminArray<T>(path: string, tenantId = '', filters: Record<string, string> = {}): Promise<T[]> {
  const list = await api.get<T[] | null>(path, { params: { ...withScope(tenantId), ...filters } })
  if (list === null) return []
  if (!Array.isArray(list)) throw new Error('管理列表响应格式错误，无法确认完整数据范围')
  return list
}

export const fetchWorkloadListAll = (tenantId = '') =>
  fetchAdminArray<AdminWorkload>('/api/admin/workloads', tenantId)

export const fetchFailedPipelineRunList = (tenantId = '') =>
  fetchAdminArray<AdminPipelineRun>('/api/admin/pipelineruns', tenantId, { status: 'failed' })

export const fetchAdminAlertListAll = (tenantId = '') =>
  fetchAdminArray<AdminAlert>('/api/admin/observability/alerts', tenantId)

export const fetchAppList = (params: ResSearchRequest) =>
  api.get<AdminApplication[]>('/api/admin/applications', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminWorkload[]>('/api/admin/workloads', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (w) =>
      (!params.tenantId || w.tenantId === params.tenantId) &&
      (has(w.id, params.keyword ?? '') || has(w.name, params.keyword ?? '') || has(w.type, params.keyword ?? ''))
    )
  )

export const fetchDataserviceList = (params: ResSearchRequest) =>
  api.get<AdminDataservice[]>('/api/admin/dataservices', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (d) =>
      (!params.tenantId || d.tenantId === params.tenantId) &&
      (has(d.id, params.keyword ?? '') || has(d.name, params.keyword ?? '') || has(d.kind, params.keyword ?? ''))
    )
  )

// 全量（不分页）：引擎删除前引用预检用（engine 模块跨域引用）。
export const fetchDataserviceListAll = () =>
  api.get<AdminDataservice[]>('/api/admin/dataservices')

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
  api.get<AdminEnvironment[]>('/api/admin/environments', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (e) =>
      (!params.tenantId || e.tenantId === params.tenantId) &&
      (has(e.id, params.keyword ?? '') || has(e.name, params.keyword ?? '') || has(e.type, params.keyword ?? ''))
    )
  )

// 环境依赖预检（D4）：与租户侧删除前置检查同源；非 0 时租户侧删除返回 409。
export interface AdminEnvUsage {
  workloads: number
  dataservices: number
  lanes: number
}
export const fetchEnvUsage = (id: string) => api.get<AdminEnvUsage>(`/api/admin/environments/${id}/usage`)

// 泳道跨租户只读总览：只含生命周期终态（status），回收任务结果平台不持久化。
export interface AdminLane {
  id: string
  tenantId: string
  envId: string
  name: string
  mode: string
  status: string
  weight?: number
  externalLink?: string
  description?: string
  createdAt?: string
  updatedAt?: string
}
export interface LaneSearchRequest extends ResSearchRequest {
  envId?: string
  status?: string
  mode?: string
}
export const fetchLaneList = (params: LaneSearchRequest) =>
  api
    .get<AdminLane[]>('/api/admin/lanes', {
      params: Object.fromEntries(
        Object.entries({
          ...withScope(params.tenantId ?? ''),
          envId: params.envId,
          status: params.status,
          mode: params.mode
        }).filter(([, v]) => v !== undefined && v !== '')
      )
    })
    .then((list) =>
      filterPage(list, params, (l) => has(l.name, params.keyword ?? '') || has(l.envId, params.keyword ?? '') || has(l.id, params.keyword ?? ''))
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
  api.get<AdminBuildRun[]>('/api/admin/buildruns', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminImage[]>('/api/admin/images', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminRelease[]>('/api/admin/releases', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminPipelineRun[]>('/api/admin/pipelineruns', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (r) =>
      (!params.tenantId || r.tenantId === params.tenantId) &&
      (has(r.id, params.keyword ?? '') || has(r.appId, params.keyword ?? '') ||
        has(r.status, params.keyword ?? '') || has(r.pipelineId, params.keyword ?? ''))
    )
  )

// Agent 运行（agentrun 执行域全量总览）：上游 ns=agent 单一命名空间无租户维度，
// source 按名称前缀归类（auto-review/auto-test=agentrun 侧 webhook 自动触发，paas=委派/agent stage）。
export interface AdminAgentRun {
  id: string // = name（useCrud 列表主键约定）
  name: string
  runtime: string
  stage: string
  phase: string // Pending|Running|Succeeded|Failed
  verdict?: string
  tokens: number
  podName?: string
  createdAt?: string
  age?: string
  request?: string // 服务端截断 200 字
  summary?: string
  error?: string
  source: 'auto-review' | 'auto-test' | 'paas'
  workspaceFrom?: string
  producedBranch?: string
}
export interface AdminAgentRunStats {
  total: number
  succeeded: number
  failed: number
  running: number
  pending: number
  tokensTotal: number
  tokensAuto: number
  tokensPaas: number
}
export interface AdminAgentRunOverview {
  runs: AdminAgentRun[]
  stats: AdminAgentRunStats
}
export const fetchAgentRunList = (
  params: ResSearchRequest,
  onOverview?: (ov: AdminAgentRunOverview) => void
) =>
  api.get<AdminAgentRunOverview>('/api/admin/agentrun/runs').then((ov) => {
    onOverview?.(ov) // 汇总卡片用全量统计（不受表格过滤影响）
    const runs = ov.runs.map((r) => ({ ...r, id: r.name }))
    return filterPage(runs, params, (r) =>
      has(r.name, params.keyword ?? '') || has(r.stage, params.keyword ?? '') ||
      has(r.phase, params.keyword ?? '') || has(r.source, params.keyword ?? ''))
  })

// -- 配置中心 --
export interface AdminNamespace {
  id: string
  tenantId: string
  name: string
  desc?: string
  updatedAt: string
}
export const fetchNamespaceList = (params: ResSearchRequest) =>
  api.get<AdminNamespace[]>('/api/admin/namespaces', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminService[]>('/api/admin/services', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
  api.get<AdminAlertRule[]>('/api/admin/alert-rules', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (a) =>
      (!params.tenantId || a.tenantId === params.tenantId) &&
      (has(a.id, params.keyword ?? '') || has(a.name, params.keyword ?? '') || has(a.metricName, params.keyword ?? ''))
    )
  )

// -- D3 配置/治理/观测 admin 只读扩展（spec 2026-10-19 §8.6）--
// 配置中心：聚合列表（draft 条数/active 版本/引用数）+ 详情四区（草稿掩码/active 键清单/键 diff/引用/发布记录）。
export interface AdminConfigcenterRow {
  id: string
  tenantId: string
  name: string
  scope: string
  appId?: string
  envId?: string
  serviceId?: string
  desc?: string
  updatedAt: string
  draftItems: number
  activeVersion: number
  refCount: number
}
export interface AdminConfigItem {
  id: string
  key: string
  value: string // 恒为掩码 ••••••
  type: string
  updatedAt: string
}
export interface AdminPublishView {
  id: string
  version: number
  status: string
  keys: string[]
  createdAt: string
}
export interface AdminNSDetail {
  namespace: AdminConfigcenterRow & { scope: string }
  draft: AdminConfigItem[]
  active: AdminPublishView | null
  diff: { added: string[]; removed: string[] }
  refs: { id: string; appNsId: string; sharedNsId: string; createdAt: string }[]
  refsError?: string
  publishes: AdminPublishView[]
}
export const fetchConfigcenterList = (params: ResSearchRequest) =>
  api.get<AdminConfigcenterRow[]>('/api/admin/configcenter/namespaces', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (n) =>
      (!params.tenantId || n.tenantId === params.tenantId) &&
      (has(n.id, params.keyword ?? '') || has(n.name, params.keyword ?? '') || has(n.appId ?? '', params.keyword ?? ''))
    )
  )
export const fetchConfigcenterDetail = (id: string) =>
  api.get<AdminNSDetail>(`/api/admin/configcenter/namespaces/${id}`)

// 治理路由：下发证据链（applyStatus ''=未下发/applied/failed + applyError + appliedAt）。
export interface AdminRoute {
  id: string
  tenantId: string
  name: string
  host?: string
  path: string
  serviceId: string
  methods: string[]
  stripPath: boolean
  enabled: boolean
  updatedAt: string
  applyStatus?: string
  applyError?: string
  appliedAt?: string
}
export const fetchAdminRouteList = (params: ResSearchRequest) =>
  api.get<AdminRoute[]>('/api/admin/governance/routes', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (r) =>
      (!params.tenantId || r.tenantId === params.tenantId) &&
      (has(r.name, params.keyword ?? '') || has(r.host ?? '', params.keyword ?? '') || has(r.path, params.keyword ?? ''))
    )
  )

// 治理熔断器：state 由后端即时评估填充（closed/open/half-open）。
export interface AdminBreaker {
  id: string
  tenantId: string
  name: string
  serviceId: string
  strategy: string
  threshold: number
  minRequests: number
  windowSecs: number
  enabled: boolean
  state?: string
  updatedAt: string
}
export const fetchAdminBreakerList = (params: ResSearchRequest) =>
  api.get<AdminBreaker[]>('/api/admin/governance/breakers', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (b) =>
      (!params.tenantId || b.tenantId === params.tenantId) &&
      (has(b.name, params.keyword ?? '') || has(b.serviceId, params.keyword ?? ''))
    )
  )

// 可观测：当前告警（跨租户即时评估，firing）+ 告警历史事件（PG 持久化）。
// 规则配置与告警事件分列——有规则 ≠ 发生告警。
export interface AdminAlert {
  ruleId: string
  ruleName: string
  tenantId: string
  targetType: string
  targetId: string
  metricName: string
  value: number
  threshold: number
  operator: string
  severity: string
  status: string
  firedAt: string
}
export interface AdminAlertEvent {
  id: string
  tenantId: string
  ruleId: string
  ruleName: string
  targetType: string
  targetId: string
  metricName: string
  value: number
  threshold: number
  severity: string
  status: string
  occurredAt: string
}
export const fetchAdminAlertList = (params: ResSearchRequest) =>
  api.get<AdminAlert[]>('/api/admin/observability/alerts', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (a) =>
      (!params.tenantId || a.tenantId === params.tenantId) &&
      (has(a.ruleName, params.keyword ?? '') || has(a.targetId, params.keyword ?? ''))
    )
  )
export const fetchAdminAlertEventList = (params: ResSearchRequest) =>
  api.get<AdminAlertEvent[]>('/api/admin/observability/alert-events', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (e) =>
      (!params.tenantId || e.tenantId === params.tenantId) &&
      (has(e.ruleName, params.keyword ?? '') || has(e.targetId, params.keyword ?? ''))
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
  api.get<AdminSecret[]>('/api/admin/secrets', { params: withScope(params.tenantId ?? '') }).then((list) =>
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
// 客户端过滤版（资源详情抽屉的「审计」tab 用：单资源小范围，量级小无需服务端分页）。
export const fetchAuditLogList = (params: ResSearchRequest) =>
  api.get<unknown>('/api/admin/audit-logs', { params: withScope(params.tenantId ?? '') }).then((res) => {
    const page = res as AdminAuditPage | AdminAuditLog[]
    const list = Array.isArray(page) ? page : page.items
    return filterPage(list, params, (l: AdminAuditLog) =>
      (!params.tenantId || l.tenantId === params.tenantId) &&
      (has(l.actor, params.keyword ?? '') || has(l.action, params.keyword ?? '') || has(l.resourceType, params.keyword ?? ''))
    )
  })

// 审计查询条件（D4：后端过滤 + 分页，不在浏览器下载全量再过滤）。
export interface AuditQueryParams {
  tenantId?: string
  actor?: string
  action?: string
  resourceType?: string
  resourceId?: string
  keyword?: string
  /** RFC3339，闭区间起点 */
  from?: string
  /** RFC3339，开区间终点 */
  to?: string
  offset: number
  limit: number
}
export interface AdminAuditPage {
  items: AdminAuditLog[]
  total: number
  offset: number
  limit: number
}
// useCrud 适配：page/size → offset/limit，响应包成 {records,total,current,size}。
export const fetchAuditLogPage = (params: Record<string, unknown>) => {
  const page = Number(params.page ?? 1)
  const size = Number(params.size ?? 50)
  const q: AuditQueryParams = {
    tenantId: (params.tenantId as string) || undefined,
    actor: (params.actor as string) || undefined,
    action: (params.action as string) || undefined,
    resourceType: (params.resourceType as string) || undefined,
    resourceId: (params.resourceId as string) || undefined,
    keyword: (params.keyword as string) || undefined,
    from: (params.from as string) || undefined,
    to: (params.to as string) || undefined,
    offset: (page - 1) * size,
    limit: size
  }
  return api
    .get<AdminAuditPage>('/api/admin/audit-logs', {
      params: Object.fromEntries(Object.entries(q).filter(([, v]) => v !== undefined && v !== ''))
    })
    .then((p) => ({ records: p.items, total: p.total, current: page, size: p.limit }))
}

// -- 数据服务备份/恢复（D4 跨租户总览） --
// restorable 由后端与租户侧 restore 端点同源判定：record 恒 false；snapshot 的 verifiedAt
// 只是校验和记录时间，不等于恢复演练通过；恢复是否生效看 restore 记录自身 status。
export interface AdminBackup {
  id: string
  tenantId: string
  resourceId: string
  type: string
  status: string
  mode?: string
  sizeMB?: number
  createdAt?: string
  engine?: string
  provider?: string
  artifactRef?: string
  sizeBytes?: number
  checksum?: string
  verifiedAt?: string
  error?: string
  sourceBackupId?: string
  restoredResourceId?: string
  restoreName?: string
  finishedAt?: string
  restorable: boolean
}
export interface BackupSearchRequest extends ResSearchRequest {
  mode?: string
  status?: string
  resourceId?: string
}
export const fetchBackupList = (params: BackupSearchRequest) =>
  api
    .get<AdminBackup[]>('/api/admin/backups', {
      params: Object.fromEntries(
        Object.entries({
          ...withScope(params.tenantId ?? ''),
          mode: params.mode,
          status: params.status,
          resourceId: params.resourceId
        }).filter(([, v]) => v !== undefined && v !== '')
      )
    })
    .then((list) =>
      // 服务端已按 mode/status/resourceId 过滤；关键字在返回集内匹配。
      filterPage(list, params, (b) => has(b.id, params.keyword ?? '') || has(b.resourceId, params.keyword ?? '') || has(b.tenantId, params.keyword ?? ''))
    )

// -- 计费：配额/账单 --
export interface AdminQuota {
  id: string // tenantId 映射，供 useCrud row-key
  tenantId: string
  limits: Record<string, number>
  updatedAt: string
}
export const fetchQuotaList = (params: ResSearchRequest) =>
  api.get<AdminQuota[]>('/api/admin/quotas', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(
      list.map((q) => ({ ...q, id: q.tenantId })),
      params,
      (q) => (!params.tenantId || q.tenantId === params.tenantId) && has(q.tenantId, params.keyword ?? '')
    )
  )

// -- 用量总览（跨租户当前快照；估算金额 ≠ 结算金额）--
export interface AdminUsageRow {
  tenantId: string
  counts: Record<string, number>
  limits: Record<string, number>
  byApp?: Record<string, Record<string, number>>
  estimatedAmount: number
  over: string[]
  updatedAt: string
}
export interface AdminUsageDimension {
  key: string
  label: string
  available: boolean
  note: string
}
export interface AdminUsageOverview {
  rows: AdminUsageRow[]
  dimensions: AdminUsageDimension[]
  estimatedTotal: number
}
export const fetchUsageOverview = (tenantId?: string) =>
  api.get<AdminUsageOverview>('/api/admin/usage', { params: withScope(tenantId ?? '') })

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
  api.get<AdminBill[]>('/api/admin/bills', { params: withScope(params.tenantId ?? '') }).then((list) =>
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

// -- 后台运维操作（滚动重启 + 操作记录）--
// 语义：POST 返回 202 只表示「受理」，是否生效必须看操作记录 status（succeeded 才算确认完成，
// unknown = 观察超时无法确认，superseded = 被新发布覆盖）。页面不把 HTTP 202 当生效证据。
export interface WorkloadOperation {
  id: string
  workloadId: string
  kind: 'restart' | 'recreate'
  status: 'accepted' | 'reconciling' | 'running' | 'succeeded' | 'failed' | 'superseded' | 'unknown'
  reason?: string
  actor?: string
  evidence?: string
  replicas?: number
  acceptedAt: string
  updatedAt: string
  finishedAt?: string
}

export const restartWorkload = (id: string, reason: string) =>
  api.post<WorkloadOperation>(`/api/admin/workloads/${id}/restart`, { reason })

export const fetchWorkloadOperations = (id: string, params?: { limit?: number; offset?: number }) =>
  api.get<WorkloadOperation[]>(`/api/admin/workloads/${id}/operations`, { params })

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
export interface ReconcileMissing {
  id: string
  name: string
  tenantId: string
}
export interface ReconcileResult {
  dryRun: boolean
  scanned: number
  created: number
  skipped: number
  failed: number
  /** 仅 dry-run：CRD 已存在的条数 */
  existing?: number
  /** 仅 dry-run：将补建清单（最多 200 条，truncated 时看 missingCount） */
  missing?: ReconcileMissing[]
  missingCount?: number
  /** 仅 dry-run：探测失败（权限/连接）的 ID——不算缺失 */
  failedIds?: string[]
  truncated?: boolean
}
/** dryRun=true 只读预览（不写数据面、不记审计）；false 执行补建。 */
export const reconcileWorkloads = (dryRun = false) =>
  api.post<ReconcileResult>(
    '/api/admin/workloads/reconcile',
    undefined,
    dryRun ? { params: { dryRun: 'true' } } : undefined
  )

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

// -- 财务深化（D5-4）：月度预算 / 周期 token 用量 / 供应商人工对账 --
// 费用一律是估算（未拆分 prompt/completion，未定价模型成本记 0），
// 对账记录是人工登记凭据，不代表已付款——页面必须原样展示 costBasis/settlementBasis。
export type BudgetState = 'ok' | 'warn' | 'exceeded'

export interface AdminBudget {
  id: string
  tenantId: string
  scope: 'tenant' | 'app'
  appId?: string
  monthlyAmount: number
  warnRatio: number
  enforce: boolean
  note?: string
  createdAt: string
  updatedAt: string
}

export interface BudgetStatusRow {
  budget: AdminBudget
  period: string
  tokens: number
  estimatedCost: number
  ratio: number
  state: BudgetState
  unpricedModels?: string[]
  costBasis: string
}

export interface BudgetOverview {
  period: string
  rows: BudgetStatusRow[]
  costBasis: string
}

export const fetchBudgetOverview = (tenantId?: string, period?: string) =>
  api.get<BudgetOverview>('/api/admin/budgets', {
    params: { ...withScope(tenantId ?? ''), ...(period ? { period } : {}) }
  })

export const createBudget = (body: Partial<AdminBudget> & { tenantId: string }) =>
  api.post<AdminBudget>('/api/admin/budgets', body)

export const updateBudget = (id: string, body: Partial<AdminBudget>) =>
  api.put<AdminBudget>(`/api/admin/budgets/${id}`, body)

export const deleteBudget = (id: string) => api.del<unknown>(`/api/admin/budgets/${id}`)

export interface TokenUsageRow {
  tenantId?: string
  period: string
  appId?: string
  model: string
  tokens: number
}

export interface TokenUsageOverview {
  period: string
  rows: TokenUsageRow[]
  totalTokens: number
  estimatedCost: number
  unpricedModels?: string[]
  costBasis: string
}

export const fetchTokenUsage = (tenantId?: string, period?: string) =>
  api.get<TokenUsageOverview>('/api/admin/token-usage', {
    params: { ...withScope(tenantId ?? ''), ...(period ? { period } : {}) }
  })

export interface VendorSettlementRow {
  vendor: string
  tokens: number
  estimatedCost: number
  unpricedModels?: string[]
  models: string[] | null
  recordedAmount: number
  recordCount: number
  diff: number
}

export interface SettlementRecord {
  id: string
  vendor: string
  period: string
  amount: number
  currency: string
  invoiceNo?: string
  note?: string
  recordedBy?: string
  createdAt: string
}

export interface SettlementOverview {
  period: string
  rows: VendorSettlementRow[]
  records: SettlementRecord[]
  estimatedTotal: number
  recordedTotal: number
  unpricedModels?: string[]
  costBasis: string
  settlementBasis: string
}

export const fetchSettlements = (tenantId?: string, period?: string) =>
  api.get<SettlementOverview>('/api/admin/settlements', {
    params: { ...withScope(tenantId ?? ''), ...(period ? { period } : {}) }
  })

export const createSettlement = (body: {
  vendor: string
  period?: string
  amount: number
  currency?: string
  invoiceNo?: string
  note?: string
}) => api.post<SettlementRecord>('/api/admin/settlements', body)

export const deleteSettlement = (id: string) => api.del<unknown>(`/api/admin/settlements/${id}`)

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

// —— AI 编排广场总览（super_admin；下架 = 独立治理契约 DELETE /api/admin/ai/marketplace/{id}?reason=，spec 2026-10-19 D3）——
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

// 广场下架（平台治理写操作）：reason 必填入审计；响应如实标注已安装副本不受影响。
export const unpublishMarketItem = (id: string, reason: string) =>
  api.del<{ deleted: string; installs: number; installedNote: string }>(
    `/api/admin/ai/marketplace/${id}`,
    { params: { reason } }
  )

// —— AI 工作流 admin 总览（spec 2026-10-19 D3：定义与运行跨租户只读；运行含节点失败/等待确认状态）——
export interface AdminWorkflowDef {
  id: string
  tenantId: string
  name: string
  desc?: string
  nodes?: { id: string; type: string; name?: string }[]
  enabled: boolean
  createdAt?: string
  updatedAt?: string
}
export interface AdminWorkflowNodeRun {
  nodeId: string
  status: string
  error?: string
  startedAt?: string
  finishedAt?: string
}
export interface AdminWorkflowRun {
  id: string
  tenantId: string
  workflowId: string
  status: string
  nodeRuns?: AdminWorkflowNodeRun[]
  createdAt?: string
  finishedAt?: string
}

export const fetchAdminWorkflowList = (params: ResSearchRequest) =>
  api.get<AdminWorkflowDef[]>('/api/admin/ai/workflows', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (w) =>
      (!params.tenantId || w.tenantId === params.tenantId) &&
      (has(w.id, params.keyword ?? '') || has(w.name, params.keyword ?? ''))
    )
  )

export const fetchAdminWorkflowRunList = (params: ResSearchRequest) =>
  api.get<AdminWorkflowRun[]>('/api/admin/ai/workflow-runs', { params: withScope(params.tenantId ?? '') }).then((list) =>
    filterPage(list, params, (r) =>
      (!params.tenantId || r.tenantId === params.tenantId) &&
      (has(r.id, params.keyword ?? '') || has(r.workflowId, params.keyword ?? ''))
    )
  )
