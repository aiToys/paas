<script setup lang="ts">
// 平台能力 → 可观测（综合大屏，多维度）。
// 设计原则（对标 Datadog/Grafana/Jaeger）：入口全局，维度是过滤器不是门槛。
// 漏斗式：告警总览（置顶入口）→ 实体健康矩阵（全部视图）→ 指标/日志/trace 下钻。
// 维度过滤器：全部（租户总览）/ 环境 / 应用 / 数据服务；空参数=后端租户全局查询。
// 惰性时序：每次加载后端补点；前端 10s 轮询刷新（页面不可见自动暂停）。
import { computed, onMounted, ref } from 'vue'
import { usePolling } from '@/composables/usePolling'
import { fmtMetric, sparkHeights as sparkHeightsRaw } from '@/composables/useMetricFormat'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import {
  listMetrics, listAlertRules, createAlertRule, deleteAlertRule, listAlerts, listLogs,
  listTraces, getTrace,
  type MetricPoint, type MetricSeries, type AlertRule, type Alert, type LogEntry,
} from '@/api/observability'
import { listLanes } from '@/api/workload'
import {
  type Span, type Trace,
  buildSpanTree, flattenSpanTree, spanWidth, spanLeft, spanChips, errSpanCount,
  spanKindBadge, spanServiceLabel,
} from '@/composables/useSpanTree'
import { useEnvStore } from '@/stores/env'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface App { id: string; name: string; envId?: string }
interface DataService { id: string; kind: string; name: string; envId?: string; status?: string }

// ---- 维度状态：dim = all | env | app | dataservice；all 视图为租户全局聚合 ----
type Dim = 'all' | 'env' | 'app' | 'dataservice'
const dim = ref<Dim>('all')
const dimEnv = ref('') // env 维度选中值
const dimApp = ref('') // app 维度选中值
const dimDS = ref('') // dataservice 维度选中值

const apps = ref<App[]>([])
const dataServices = ref<DataService[]>([])
const metrics = ref<MetricSeries[]>([])
const rules = ref<AlertRule[]>([])
const alerts = ref<Alert[]>([])
const logs = ref<LogEntry[]>([])
const logLevel = ref('')
const logQ = ref('')
const logLane = ref('')
const traces = ref<Trace[]>([])
const traceStatus = ref('')
const loading = ref(false)
// 泳道候选（/api/workloads/lanes 聚合，权威来源；手输仍允许 allow-create）
const laneOptions = ref<{ laneId: string; workloadCount: number }[]>([])

async function loadLanes() {
  try {
    laneOptions.value = await listLanes()
  } catch {
    // 泳道候选加载失败不阻断主流程（下拉空仍可 allow-create 手输）
  }
}

const traceStatusLabel: Record<string, string> = { success: '成功', error: '错误' }
const traceStatusType: Record<string, TagType> = { success: 'success', error: 'danger' }

const metricOrder = ['cpu', 'mem', 'rps', 'latency', 'errorRate']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟', errorRate: '错误率' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, TagType> = { info: 'info', warn: 'warning', error: 'danger' }
const dsKindLabel: Record<string, string> = { db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量库', search: '搜索引擎' }

// 当前维度的查询参数：metrics 用 targetType/targetId；logs/traces 用 appId（dataservice 用 targetType）
const metricsParams = computed((): Record<string, string> => {
  if (dim.value === 'app' && dimApp.value) return { targetType: 'app', targetId: dimApp.value }
  if (dim.value === 'dataservice' && dimDS.value) return { targetType: 'dataservice', targetId: dimDS.value }
  return {} // all / env 维度：租户全局（env 聚合视图由健康矩阵承担）
})
const logsAppId = computed(() => (dim.value === 'app' ? dimApp.value : ''))

// 维度标题（区块标题展示当前过滤上下文）
const dimTitle = computed(() => {
  if (dim.value === 'app') return apps.value.find((a) => a.id === dimApp.value)?.name ?? '应用'
  if (dim.value === 'dataservice') return dataServices.value.find((d) => d.id === dimDS.value)?.name ?? '数据服务'
  if (dim.value === 'env') return envStore.envs.find((e) => e.id === dimEnv.value)?.name ?? '环境'
  return '租户总览'
})

// 全部/环境视图：实体健康矩阵 = 各应用 + 各数据服务的 CPU/内存/RPS 当前值卡片
interface HealthRow {
  key: string; type: 'app' | 'dataservice'; id: string; name: string
  sub: string; cpu?: MetricSeries; mem?: MetricSeries; rps?: MetricSeries; errorRate?: MetricSeries
  abnormal: boolean // 异常：errorRate>0（应用 5xx）或 cpu>0.8 核（数据服务单核打满）
}
const healthRows = computed<HealthRow[]>(() => {
  const envFilter = dim.value === 'env' ? dimEnv.value : ''
  const rows: HealthRow[] = []
  const find = (t: string, id: string, name: string) =>
    metrics.value.find((m) => m.targetType === t && m.targetId === id && m.name === name)
  for (const a of apps.value) {
    if (envFilter && a.envId !== envFilter) continue
    const r: HealthRow = { key: 'app:' + a.id, type: 'app', id: a.id, name: a.name, sub: '应用',
      cpu: find('app', a.id, 'cpu'), mem: find('app', a.id, 'mem'), rps: find('app', a.id, 'rps'),
      errorRate: find('app', a.id, 'errorRate'), abnormal: false }
    r.abnormal = (r.errorRate?.current ?? 0) > 0
    rows.push(r)
  }
  for (const d of dataServices.value) {
    if (envFilter && d.envId !== envFilter) continue
    const r: HealthRow = { key: 'ds:' + d.id, type: 'dataservice', id: d.id, name: d.name, sub: dsKindLabel[d.kind] ?? d.kind,
      cpu: find('dataservice', d.id, 'cpu'), mem: find('dataservice', d.id, 'mem'), abnormal: false }
    r.abnormal = (r.cpu?.current ?? 0) > 0.8
    rows.push(r)
  }
  // 异常置顶（一眼看出哪里红了），其后有指标的实体；无任何指标的过滤
  return rows
    .filter((r) => r.cpu || r.mem || r.rps)
    .sort((a, b) => Number(b.abnormal) - Number(a.abnormal))
})

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

// 指标值格式化（公共 composable，Grafana 式单位自适应 + isFinite 守卫）。
const fmtVal = (v: number, unit = '') => fmtMetric(v, unit)

// spanRows：trace 的 span 树形 flatten（带 depth），驱动 v-for 树形缩进渲染。
// 每次展开调用一次（非 computed，因 row 是 el-table 展开行动态对象）。
function spanRows(row: Trace) {
  return flattenSpanTree(buildSpanTree(row.spans || []))
}

// trace 行 class：错误 trace 整行红色高亮（el-table row-class-name 回调）。
function traceRowClass({ row }: { row: Trace }): string {
  return row.status === 'error' || errSpanCount(row) ? 'trace-err-row' : ''
}

// sparkline 高度（公共 composable：基线钉 0 防平坦线 min-max 拉伸失真）
const sparkHeights = (points: MetricPoint[]) => sparkHeightsRaw(points.map((p) => p.value), 100)

async function loadApps() {
  const resp = await fetchAuth('/api/applications')
  if (resp.ok) apps.value = (await resp.json()).data ?? []
  const resp2 = await fetchAuth('/api/dataservices')
  if (resp2.ok) dataServices.value = (await resp2.json()).data ?? []
  // URL 深链恢复维度上下文：?app=（应用详情「监控」入口）/ ?env= / ?ds=
  const qs = (k: string) => (typeof route.query[k] === 'string' ? (route.query[k] as string) : '')
  const app = qs('app'), env = qs('env'), ds = qs('ds')
  if (app && apps.value.some((a) => a.id === app)) {
    dim.value = 'app'; dimApp.value = app
  } else if (ds && dataServices.value.some((d) => d.id === ds)) {
    dim.value = 'dataservice'; dimDS.value = ds
  } else if (env && envStore.envs.some((e) => e.id === env)) {
    dim.value = 'env'; dimEnv.value = env
  }
  restoreFiltersFromUrl()
}

async function loadMetrics() {
  metrics.value = await listMetrics(metricsParams.value)
}

async function loadRules() {
  rules.value = await listAlertRules()
}

async function loadAlerts() {
  alerts.value = await listAlerts()
}

async function loadLogs() {
  const params: Record<string, string> = {}
  if (logsAppId.value) params.appId = logsAppId.value
  if (dim.value === 'dataservice' && dimDS.value) {
    params.targetType = 'dataservice'
    params.targetId = dimDS.value
  }
  if (logLevel.value) params.level = logLevel.value
  if (logLane.value.trim()) params.lane = logLane.value.trim()
  if (logQ.value.trim()) params.q = logQ.value.trim()
  logs.value = await listLogs(params)
}

const traceIdQuery = ref('')
async function loadTraces() {
  // 直查态：轮询/刷新保持直查结果（否则 10s 轮询全量列表会静默覆盖刚定位的 trace）
  if (traceIdQuery.value.trim()) {
    await searchTraceById()
    return
  }
  const params: Record<string, string> = {}
  if (logsAppId.value) params.appId = logsAppId.value
  if (traceStatus.value) params.status = traceStatus.value
  traces.value = await listTraces<Trace>(params)
}

// traceId 精确直查：日志/告警拿到 traceId 后定位完整链路（命中即单条展示）。
// focusTraces=true（页头入口/日志联动触发）时滚动定位到链路区块。
const tracesRef = ref<HTMLElement | null>(null)
async function searchTraceById(focusTraces = false) {
  if (focusTraces) tracesRef.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  const id = traceIdQuery.value.trim()
  if (!id) { loadTraces(); return }
  try {
    traces.value = [await getTrace<Trace>(id)]
  } catch {
    traces.value = []
    ElMessage.warning(`未找到 TraceID: ${id}`)
  }
}

// 日志行 TraceID 点击联动：填充直查框 + 触发查询 + 滚动到链路区块（最短排障路径）
function clickTraceId(traceId?: string) {
  if (!traceId) return
  traceIdQuery.value = traceId
  searchTraceById(true)
}

// 维度状态同步 URL（router.replace 不留历史）：刷新/分享保留「我正在看什么」上下文。
// 全部视图不带参数；app 维度兼容既有 ?app= 深链格式。
// 排障过滤器（级别/traceId/状态）一并入 URL——排障现场可分享。
function syncUrl() {
  const q: Record<string, string> = {}
  if (dim.value === 'env' && dimEnv.value) q.env = dimEnv.value
  if (dim.value === 'app' && dimApp.value) q.app = dimApp.value
  if (dim.value === 'dataservice' && dimDS.value) q.ds = dimDS.value
  if (logLevel.value) q.level = logLevel.value
  if (traceStatus.value) q.traceStatus = traceStatus.value
  if (traceIdQuery.value.trim()) q.traceId = traceIdQuery.value.trim()
  router.replace({ query: q })
}

// URL 恢复排障过滤器（深链/share 场景）：与 syncUrl 对偶。
function restoreFiltersFromUrl() {
  const qs = (k: string) => (typeof route.query[k] === 'string' ? (route.query[k] as string) : '')
  const level = qs('level'), ts = qs('traceStatus'), tid = qs('traceId')
  if (['info', 'warn', 'error'].includes(level)) logLevel.value = level
  if (['success', 'error'].includes(ts)) traceStatus.value = ts
  if (tid) traceIdQuery.value = tid
}

// 维度切换：重置下级选择并整页重载（env 维度级联过滤应用下拉）
function switchDim() {
  dimEnv.value = ''
  dimApp.value = ''
  dimDS.value = ''
  traceIdQuery.value = '' // 清直查态（防 chip 残留 + 列表被直查语义劫持）
  syncUrl()
  loadAll()
}

// 告警点击下钻：按告警 target 维度切换过滤器（Datadog 式「从告警进入排障」）
function drillAlert(a: Alert) {
  const tt = a.targetType || 'app'
  if (tt === 'app' && apps.value.some((x) => x.id === a.targetId)) {
    dim.value = 'app'; dimApp.value = a.targetId
  } else if (tt === 'dataservice' && dataServices.value.some((x) => x.id === a.targetId)) {
    dim.value = 'dataservice'; dimDS.value = a.targetId
  } else if (tt === 'env' && envStore.envs.some((e) => e.id === a.targetId)) {
    dim.value = 'env'; dimEnv.value = a.targetId
  } else {
    // workload 等无对应过滤维度：留在全部视图，仅提示
    ElMessage.info(`告警目标 ${a.targetId}（${tt}）`)
    return
  }
  syncUrl()
  loadAll()
}

// 健康矩阵点击下钻：切到对应实体维度
function drillRow(r: HealthRow) {
  dim.value = r.type
  if (r.type === 'app') dimApp.value = r.id
  else dimDS.value = r.id
  syncUrl()
  loadAll()
}

async function loadAll(silent = false) {
  // 首次加载设 loading（骨架）；10s 轮询 silent=true 不设 loading，避免 v-loading 闪烁。
  if (!silent) loading.value = true
  try {
    await Promise.all([loadMetrics(), loadRules(), loadAlerts(), loadLogs(), loadTraces()])
  } finally {
    if (!silent) loading.value = false
  }
}

const showRule = ref(false)
const ruleForm = ref({
  name: '', metricName: 'cpu', targetType: 'app' as 'app' | 'env' | 'dataservice' | 'workload', targetId: '',
  operator: '>', threshold: 80, severity: 'warning', enabled: true,
})
const ruleSubmitting = ref(false)

const metricsOpts = [
  { value: 'cpu', label: 'CPU (%)' },
  { value: 'mem', label: '内存 (%)' },
  { value: 'rps', label: '请求/秒' },
  { value: 'latency', label: 'P95 延迟 (ms)' },
  { value: 'errorRate', label: '错误率 (%)' },
]
const ops = [
  { value: '>', label: '> 大于' },
  { value: '>=', label: '≥ 大于等于' },
  { value: '<', label: '< 小于' },
  { value: '<=', label: '≤ 小于等于' },
]
const severities = [
  { value: 'critical', label: '严重' },
  { value: 'warning', label: '警告' },
]
// 规则目标维度：app/dataservice 提供实体下拉；env/workload 手填 ID（简单覆盖，不造选择器）
const ruleTargetTypes = [
  { value: 'app', label: '应用' },
  { value: 'dataservice', label: '数据服务' },
  { value: 'env', label: '环境' },
  { value: 'workload', label: '工作负载' },
]
const ruleTargetOpts = computed(() => {
  if (ruleForm.value.targetType === 'app') return apps.value.map((a) => ({ value: a.id, label: a.name }))
  if (ruleForm.value.targetType === 'dataservice') return dataServices.value.map((d) => ({ value: d.id, label: `${d.name}（${dsKindLabel[d.kind] ?? d.kind}）` }))
  return []
})

function openRule() {
  // 默认目标跟随当前维度（全部视图默认 app 维度第一个）
  const tt = dim.value === 'dataservice' ? 'dataservice' : dim.value === 'app' ? 'app' : 'app'
  const tid = tt === 'dataservice' ? dimDS.value : tt === 'app' ? dimApp.value : ''
  ruleForm.value = {
    name: '', metricName: 'cpu', targetType: tt as typeof ruleForm.value.targetType, targetId: tid,
    operator: '>', threshold: 80, severity: 'warning', enabled: true,
  }
  showRule.value = true
}

async function saveRule() {
  if (!ruleForm.value.name.trim()) {
    ElMessage.warning('请填写规则名称')
    return
  }
  ruleSubmitting.value = true
  try {
    await createAlertRule({
      name: ruleForm.value.name,
      metricName: ruleForm.value.metricName,
      targetType: ruleForm.value.targetType,
      targetId: ruleForm.value.targetId,
      operator: ruleForm.value.operator,
      threshold: ruleForm.value.threshold,
      severity: ruleForm.value.severity,
      enabled: ruleForm.value.enabled,
    })
    ElMessage.success('规则已创建')
    showRule.value = false
    loadRules()
    loadAlerts()
  } catch (e) {
    ElMessage.error((e as Error).message || '创建失败')
  } finally {
    ruleSubmitting.value = false
  }
}

async function deleteRule(r: AlertRule) {
  try {
    const resp = await deleteAlertRule(r.id)
    if (resp.ok) {
      ElMessage.success('已删除')
      loadRules()
      loadAlerts()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '删除失败')
    }
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

onMounted(async () => {
  // 各加载独立 catch：单项网络失败不拖垮整页（Promise.all 一个 reject 会让其余结果丢失）
  await Promise.allSettled([loadApps(), envStore.loadEnvs(), loadLanes()])
  await loadAll()
})
// 10s 轮询刷新指标/告警（silent 不闪烁；页面不可见自动暂停）
usePolling(() => loadAll(true), 10000)
</script>

<template>
  <div class="obs-page">
    <div class="page-head">
      <div>
        <h2>可观测</h2>
        <p class="sub">指标 · 日志 · 链路 · 告警 · 10s 自动刷新</p>
      </div>
      <div class="head-actions">
        <el-button type="primary" @click="openRule">+ 告警规则</el-button>
      </div>
    </div>

    <!-- 维度过滤器条：全部（默认）/ 环境 / 应用 / 数据服务。维度是过滤器不是门槛 -->
    <section class="block dim-bar">
      <el-radio-group :model-value="dim" @update:model-value="dim = $event as Dim; switchDim()">
        <el-radio-button value="all">全部</el-radio-button>
        <el-radio-button value="env">环境</el-radio-button>
        <el-radio-button value="app">应用</el-radio-button>
        <el-radio-button value="dataservice">数据服务</el-radio-button>
      </el-radio-group>
      <el-select v-if="dim === 'env'" v-model="dimEnv" placeholder="选择环境" style="width: 180px" @change="syncUrl(); loadAll()">
        <el-option v-for="e in envStore.envs" :key="e.id" :label="e.name" :value="e.id" />
      </el-select>
      <el-select v-if="dim === 'app'" v-model="dimApp" placeholder="选择应用" style="width: 200px" @change="syncUrl(); loadAll()">
        <el-option v-for="a in apps" :key="a.id" :label="a.name" :value="a.id" />
      </el-select>
      <el-select v-if="dim === 'dataservice'" v-model="dimDS" placeholder="选择数据服务" style="width: 200px" @change="syncUrl(); loadAll()">
        <el-option v-for="d in dataServices" :key="d.id" :label="d.name" :value="d.id" />
      </el-select>
      <!-- TraceID 直查（页头显著位置，Jaeger 式；回车查询并滚动定位链路区块） -->
      <el-input v-model="traceIdQuery" placeholder="🔍 TraceID 直查" style="width: 240px; margin-left: auto" clearable
        @keyup.enter="searchTraceById(true)" @clear="loadTraces">
        <template #append>
          <el-button @click="searchTraceById(true)">查</el-button>
        </template>
      </el-input>
    </section>

    <!-- 当前告警（置顶入口：先看哪里红了，点击下钻到对应实体） -->
    <section class="block">
      <div class="block-title">
        当前告警
        <span class="cnt" :class="{ firing: alerts.length }">{{ alerts.length }} 条 firing</span>
      </div>
      <el-empty v-if="!alerts.length" description="无活跃告警" :image-size="48" />
      <div v-else class="alert-list">
        <div v-for="(a, i) in alerts" :key="i" class="alert-row clickable" :class="a.severity" @click="drillAlert(a)">
          <span class="sev-tag" :class="a.severity">{{ a.severity === 'critical' ? '严重' : '警告' }}</span>
          <span class="alert-name">{{ a.ruleName }}</span>
          <span class="alert-target mono">{{ a.targetId }} · {{ a.metricName }}</span>
          <span class="alert-val mono">{{ a.value.toFixed(1) }} {{ a.operator }} {{ a.threshold }}</span>
          <span class="drill-hint">下钻 →</span>
        </div>
      </div>
    </section>

    <!-- 实体健康矩阵（全部/环境视图：租户全局总览，异常定位入口） -->
    <section v-if="dim === 'all' || dim === 'env'" class="block" v-loading="loading">
      <div class="block-title">实体健康 · {{ dimTitle }}</div>
      <div v-if="healthRows.length" class="health-grid">
        <div v-for="r in healthRows" :key="r.key" class="health-card clickable" :class="{ 'health-abnormal': r.abnormal }" @click="drillRow(r)">
          <div class="h-head">
            <span class="h-name">{{ r.name }}</span>
            <el-tag v-if="r.abnormal" type="danger" size="small" effect="dark">异常</el-tag>
            <el-tag v-else size="small" :type="r.type === 'app' ? 'primary' : 'success'" effect="plain">{{ r.sub }}</el-tag>
          </div>
          <div class="h-metrics">
            <div v-if="r.cpu" class="h-item">
              <span class="h-k">CPU</span>
              <span class="h-v mono">{{ fmtVal(r.cpu.current, r.cpu.unit ?? '') }}{{ r.cpu.unit === 'cores' ? ' 核' : r.cpu.unit }}</span>
            </div>
            <div v-if="r.mem" class="h-item">
              <span class="h-k">内存</span>
              <span class="h-v mono">{{ fmtVal(r.mem.current, r.mem.unit ?? '') }}{{ r.mem.unit }}</span>
            </div>
            <div v-if="r.rps" class="h-item">
              <span class="h-k">RPS</span>
              <span class="h-v mono">{{ fmtVal(r.rps.current) }}{{ r.rps.unit }}</span>
            </div>
            <div v-if="(r.errorRate?.current ?? 0) > 0" class="h-item">
              <span class="h-k">错误率</span>
              <span class="h-v mono h-err">{{ fmtVal(r.errorRate!.current) }}%</span>
            </div>
          </div>
          <div v-if="r.cpu && sparkHeights(r.cpu.points).length" class="spark">
            <span v-for="(h, idx) in sparkHeights(r.cpu.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
          </div>
        </div>
      </div>
      <el-empty v-else :description="dim === 'env' ? '该环境暂无有指标的实体' : '暂无实体指标'" :image-size="48" />
    </section>

    <!-- 指标卡（单实体维度：app/dataservice） -->
    <section v-else class="block" v-loading="loading">
      <div class="block-title">关键指标 · {{ dimTitle }}</div>
      <div v-if="cards.length" class="metric-grid">
        <div v-for="c in cards" :key="c.name" class="metric-card">
          <div class="m-label">{{ c.label }}</div>
          <div class="m-value mono">
            {{ fmtVal(c.current, c.unit ?? '') }}<span class="m-unit">{{ c.unit }}</span>
          </div>
          <div class="spark">
            <span
              v-for="(h, idx) in sparkHeights(c.points)"
              :key="idx"
              class="spark-bar"
              :style="{ height: h + '%' }"
            />
          </div>
        </div>
      </div>
      <el-empty v-else description="该实体暂无指标" :image-size="48" />
    </section>

    <!-- 告警规则 -->
    <section class="block">
      <div class="block-title">告警规则</div>
      <el-table :data="rules" size="small" empty-text="暂无规则">
        <el-table-column prop="name" label="规则名" min-width="140" />
        <el-table-column label="目标" min-width="160">
          <template #default="{ row }">
            <span class="mono faint">{{ row.targetType }}{{ row.targetId ? ' · ' + row.targetId : ' · 全部' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="条件" min-width="160">
          <template #default="{ row }">
            <span class="mono">{{ row.metricName }} {{ row.operator }} {{ row.threshold }}</span>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="row.severity === 'critical' ? 'danger' : 'warning'" size="small">
              {{ row.severity === 'critical' ? '严重' : '警告' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '停用' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button text type="danger" size="small" @click="deleteRule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 日志（可观测 Logs）：跟随维度过滤器，全部=租户全局日志流 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">日志 · {{ dimTitle }}</span>
        <div class="log-filter">
          <el-select v-model="logLevel" placeholder="全部级别" clearable size="small" style="width: 120px" @change="loadLogs">
            <el-option label="信息" value="info" />
            <el-option label="警告" value="warn" />
            <el-option label="错误" value="error" />
          </el-select>
          <el-select v-model="logLane" placeholder="泳道" size="small" style="width: 150px" clearable filterable allow-create
            @change="loadLogs">
            <el-option v-for="l in laneOptions" :key="l.laneId" :label="`${l.laneId}（${l.workloadCount}）`" :value="l.laneId" />
          </el-select>
          <el-input v-model="logQ" placeholder="关键字…" size="small" style="width: 160px" clearable @change="loadLogs" />
          <el-button size="small" @click="loadLogs">刷新</el-button>
        </div>
      </div>
      <el-table :data="logs" size="small" height="360" empty-text="暂无日志">
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ new Date(row.timestamp).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="(logLevelType[row.level]) || 'info'" size="small">
              {{ logLevelLabel[row.level] || row.level }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="140">
          <template #default="{ row }">
            <span class="mono">{{ apps.find((a) => a.id === row.appId)?.name ?? row.targetId ?? row.appId ?? '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="消息" min-width="280" show-overflow-tooltip />
        <el-table-column label="TraceID" width="140">
          <template #default="{ row }">
            <span v-if="row.traceId" class="mono trace-link" :title="row.traceId" @click="clickTraceId(row.traceId)">
              {{ row.traceId.slice(0, 12) }} →
            </span>
            <span v-else class="mono faint">—</span>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 链路追踪（可观测 Traces）：跟随维度过滤器。直查入口在页头（滚动定位到此） -->
    <section ref="tracesRef" class="block traces-anchor">
      <div class="block-head">
        <span class="block-title">链路追踪 · {{ dimTitle }}
          <span v-if="traceIdQuery.trim()" class="cnt">直查: {{ traceIdQuery.trim().slice(0, 16) }}…</span>
        </span>
        <div class="log-filter">
          <el-select v-model="traceStatus" placeholder="全部状态" clearable size="small" style="width: 120px" @change="loadTraces">
            <el-option label="成功" value="success" />
            <el-option label="错误" value="error" />
          </el-select>
          <el-button size="small" @click="loadTraces">刷新</el-button>
          <el-button v-if="traceIdQuery.trim()" size="small" type="info" plain @click="traceIdQuery = ''; loadTraces()">清除直查</el-button>
        </div>
      </div>
      <el-table :data="traces" size="small" row-key="id" empty-text="暂无链路"
            :row-class-name="traceRowClass">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="span-list">
              <!-- 时间轴刻度（相对 0 → trace.durationMs），让瀑布条左右位置可读 -->
              <div class="span-axis">
                <span class="span-mono">0</span>
                <span class="span-mono">{{ Math.round(row.durationMs / 2) }}ms</span>
                <span class="span-mono">{{ row.durationMs }}ms</span>
              </div>
              <div v-for="node in spanRows(row)" :key="node.span.id"
                class="span-card" :class="{ 'span-err': node.span.isError }"
                :style="{ paddingLeft: 10 + node.depth * 18 + 'px' }">
                <div class="span-row">
                  <span class="span-bar" :style="{ width: spanWidth(node.span, row) + '%', left: spanLeft(node.span, row) + '%' }" />
                  <span v-if="node.depth > 0" class="span-tree-line" />
                  <span v-if="spanKindBadge(node.span.kind)" class="span-kind" :title="spanKindBadge(node.span.kind)!.title">{{ spanKindBadge(node.span.kind)!.text }}</span>
                  <span class="mono span-svc">{{ spanServiceLabel(node.span) }}</span>
                  <span class="span-op">{{ node.span.operation }}</span>
                  <span class="mono span-dur">{{ node.span.durationMs }}ms</span>
                  <el-tag v-if="node.span.isError" type="danger" size="small" effect="dark">
                    异常<span v-if="node.span.errorType"> · {{ node.span.errorType }}</span>
                  </el-tag>
                </div>
                <!-- 关键元数据 chips（HTTP 方法/路径/状态码、客户端 IP） -->
                <div v-if="spanChips(node.span).length" class="span-chips">
                  <span v-for="c in spanChips(node.span)" :key="c.label" class="chip" :class="{ 'chip-err': c.err }">
                    <b class="chip-k">{{ c.label }}</b> <code>{{ c.v }}</code>
                  </span>
                </div>
                <!-- 全部 OTel 属性（可折叠） -->
                <details v-if="node.span.tags && Object.keys(node.span.tags).length" class="span-attrs">
                  <summary>全部属性 ({{ Object.keys(node.span.tags).length }})</summary>
                  <table class="attr-table"><tbody>
                    <tr v-for="(v, k) in node.span.tags" :key="k">
                      <td class="mono ak">{{ k }}</td>
                      <td class="mono av">{{ v }}</td>
                    </tr>
                  </tbody></table>
                </details>
                <!-- 异常信息 + 堆栈 -->
                <div v-if="node.span.errorMessage || node.span.tags?.['exception.stacktrace']" class="span-exc">
                  <div v-if="node.span.errorMessage" class="exc-msg">⚠ {{ node.span.errorMessage }}</div>
                  <pre v-if="node.span.tags?.['exception.stacktrace']" class="exc-stack">{{ node.span.tags['exception.stacktrace'] }}</pre>
                </div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="开始时间" width="180">
          <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" min-width="220">
          <template #default="{ row }"><span class="mono">{{ row.operation }}</span></template>
        </el-table-column>
        <el-table-column label="应用" width="120">
          <template #default="{ row }">
            <span class="mono">{{ apps.find((a) => a.id === row.appId)?.name ?? row.appId }}</span>
          </template>
        </el-table-column>
        <el-table-column label="时长" width="90">
          <template #default="{ row }"><span class="mono">{{ row.durationMs }}ms</span></template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="(traceStatusType[row.status]) || 'info'" size="small">
              {{ traceStatusLabel[row.status] || row.status }}
            </el-tag>
            <el-tag v-if="errSpanCount(row)" type="danger" size="small" effect="dark" style="margin-left:4px">
              异常 {{ errSpanCount(row) }}/{{ row.spans.length }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="Span 数" width="80">
          <template #default="{ row }">{{ row.spans.length }}</template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 规则创建弹窗 -->
    <el-dialog v-model="showRule" title="创建告警规则" width="480px">
      <el-form label-width="80px">
        <el-form-item label="规则名">
          <el-input v-model="ruleForm.name" placeholder="如 CPU 偏高" />
        </el-form-item>
        <el-form-item label="目标维度">
          <el-select v-model="ruleForm.targetType" style="width: 100%" @change="ruleForm.targetId = ''">
            <el-option v-for="t in ruleTargetTypes" :key="t.value" :label="t.label" :value="t.value" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="ruleTargetOpts.length" label="目标">
          <el-select v-model="ruleForm.targetId" placeholder="全部（不限定）" clearable style="width: 100%">
            <el-option v-for="o in ruleTargetOpts" :key="o.value" :label="o.label" :value="o.value" />
          </el-select>
        </el-form-item>
        <el-form-item v-else label="目标 ID">
          <el-input v-model="ruleForm.targetId" placeholder="留空 = 该维度全部" />
        </el-form-item>
        <el-form-item label="指标">
          <el-select v-model="ruleForm.metricName" style="width: 100%">
            <el-option v-for="m in metricsOpts" :key="m.value" :label="m.label" :value="m.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="条件">
          <div class="cond-row">
            <el-select v-model="ruleForm.operator" style="width: 140px">
              <el-option v-for="o in ops" :key="o.value" :label="o.label" :value="o.value" />
            </el-select>
            <el-input-number v-model="ruleForm.threshold" :min="0" style="flex: 1" />
          </div>
        </el-form-item>
        <el-form-item label="级别">
          <el-radio-group v-model="ruleForm.severity">
            <el-radio v-for="s in severities" :key="s.value" :value="s.value">{{ s.label }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="ruleForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRule = false">取消</el-button>
        <el-button type="primary" :disabled="ruleSubmitting" @click="saveRule">
          {{ ruleSubmitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.obs-page { max-width: 1100px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.head-actions { display: flex; gap: 10px; }
.block { margin-bottom: 24px; }
.block-title { display: flex; align-items: center; gap: 10px; font-size: 14px; font-weight: 600; margin-bottom: 10px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-head .block-title { margin-bottom: 0; }
.log-filter { display: flex; gap: 8px; align-items: center; }
.faint { color: var(--text-faint); }
/* 维度过滤器条 */
.dim-bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.trace-link { color: var(--brand); cursor: pointer; }
.trace-link:hover { text-decoration: underline; }
.traces-anchor { scroll-margin-top: 20px; }
.clickable { cursor: pointer; transition: border-color 0.15s; }
.clickable:hover { border-color: var(--brand); }
.drill-hint { font-size: 11.5px; color: var(--brand); opacity: 0; transition: opacity 0.15s; }
.alert-row.clickable:hover .drill-hint { opacity: 1; }
.span-list { padding: 8px 24px; display: flex; flex-direction: column; gap: 8px; }
:deep(.trace-err-row) { background: var(--danger-soft) !important; }
/* 时间轴刻度（相对 0 → durationMs，与 span-bar left/width 同坐标系） */
.span-axis { display: flex; justify-content: space-between; padding: 0 6px 4px; border-bottom: 1px dashed var(--border); margin-bottom: 4px; }
.span-mono { font-family: var(--font-mono); font-size: 11px; color: var(--text-faint); }
.span-card { padding: 8px 10px; border-radius: 6px; background: var(--surface); border: 1px solid var(--border); position: relative; }
.span-card.span-err { border-color: var(--danger); background: var(--danger-soft); }
/* 树形层级：每层左缩进 18px + 竖线表父子关系（depth>0 才显） */
.span-tree-line { position: absolute; left: 4px; top: 0; bottom: 0; width: 1px; background: var(--border); }
.span-row { display: flex; align-items: center; gap: 10px; position: relative; padding: 2px 6px; }
/* 瀑布条：绝对定位甘特条贴行底，left=startMs%，width=durationMs%（时间轴对齐，一眼看串行/并行/等待） */
.span-bar { position: absolute; left: 0; bottom: 0; height: 4px; width: 0; background: rgba(99, 102, 241, 0.5); border-radius: 3px; z-index: 0; }
.span-err .span-bar { background: rgba(239, 68, 68, 0.55); }
.span-kind { position: relative; z-index: 1; flex-shrink: 0; font-size: 11px; padding: 0 5px; border-radius: 3px; background: rgba(99, 102, 241, 0.12); color: var(--brand); cursor: help; }
.span-svc { position: relative; min-width: 120px; font-size: 12px; color: var(--brand); z-index: 1; }
.span-op { position: relative; flex: 1; font-size: 12px; color: var(--text-dim); z-index: 1; }
.span-dur { position: relative; font-size: 12px; color: var(--text-faint); z-index: 1; }
.span-chips { display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 6px; }
.chip { font-size: 11.5px; padding: 1px 7px; background: var(--surface-2); border-radius: 4px; color: var(--text-dim); }
.chip code { color: var(--text); font-family: var(--font-mono); }
.chip-err { background: var(--danger-soft); color: var(--danger); }
.chip-err code { color: var(--danger); font-weight: 600; }
.chip-k { color: var(--text-faint); font-weight: 400; margin-right: 3px; }
.span-attrs { padding: 2px 6px; font-size: 12px; }
.span-attrs summary { cursor: pointer; color: var(--brand); font-size: 11.5px; }
.attr-table { border-collapse: collapse; margin-top: 4px; width: 100%; }
.attr-table td { border: 1px solid var(--border); padding: 2px 8px; font-size: 11.5px; vertical-align: top; word-break: break-all; }
.attr-table .ak { color: var(--text-faint); white-space: nowrap; width: 1%; }
.attr-table .av { color: var(--text); }
.span-exc { margin: 4px 6px; padding: 6px 8px; border-left: 3px solid var(--danger); background: var(--danger-soft); border-radius: 4px; }
.exc-msg { font-size: 12px; color: var(--danger); font-weight: 600; }
.exc-stack { margin: 4px 0 0; padding: 6px; font-size: 11px; color: var(--text-dim); white-space: pre-wrap; word-break: break-all; max-height: 200px; overflow: auto; }
.cnt { font-size: 12px; font-weight: 400; color: var(--text-faint); padding: 1px 8px; background: var(--surface-2); border-radius: 8px; }
.cnt.firing { color: var(--danger); background: var(--danger-soft); }

.alert-list { display: flex; flex-direction: column; gap: 6px; }
.alert-row { display: flex; align-items: center; gap: 12px; padding: 10px 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); border-left: 3px solid var(--warning); font-size: 13px; }
.alert-row.critical { border-left-color: var(--danger); background: var(--danger-soft); }
.sev-tag { padding: 2px 8px; border-radius: 4px; font-size: 11px; background: var(--warning-soft); color: var(--warning); }
.sev-tag.critical { background: var(--danger-soft); color: var(--danger); }
.alert-name { font-weight: 600; }
.alert-target { color: var(--text-dim); flex: 1; }
.alert-val { color: var(--text); }

/* 实体健康矩阵 */
.health-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; }
.health-card { padding: 12px 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.health-abnormal { border-color: var(--danger); background: var(--danger-soft); }
.h-err { color: var(--danger); }
.h-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.h-name { font-size: 13.5px; font-weight: 600; }
.h-metrics { display: flex; gap: 16px; }
.h-item { display: flex; flex-direction: column; gap: 1px; }
.h-k { font-size: 11px; color: var(--text-faint); }
.h-v { font-size: 13.5px; font-weight: 600; }
.health-card .spark { margin-top: 8px; }

.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.metric-card { padding: 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 26px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 13px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 36px; margin-top: 8px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.cond-row { display: flex; gap: 8px; width: 100%; }
</style>
