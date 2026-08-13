<script setup lang="ts">
// 应用详情 - 可观测 tab：聚合应用全部相关监控，内部 tab 分类型。
//   - 应用实例：计算（CPU/内存）+ 流量健康（RPS/延迟）+ 副本就绪 + 日志 + 链路
//   - 依赖资源：应用绑定的数据服务（DB/cache/mq/vector）各自 CPU/内存负载
//
// 数据源：
//   /api/observability/{metrics,logs,traces}?appId=        （应用实例维度，cAdvisor+Loki+Tempo）
//   /api/dataservices                                        （绑定名→ID/类型/状态 解析）
//   /api/observability/metrics?targetType=dataservice&targetId=<id> （依赖资源负载）
//   /api/applications/{id}/workloads                         （副本就绪）
//
// 设计抉择（不造伪指标）：平台无法获知应用业务 KPI（订单/收入等需应用自定义埋点），
// 故不设独立「业务监控」tab；推理 token/请求消耗已归「用量」tab，避免重复。
// 流量健康（RPS/延迟/副本就绪）即业务相关的运行视图，归「应用实例」。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'
import {
  type Span, type Trace,
  buildSpanTree, flattenSpanTree, spanWidth, spanLeft, spanChips, errSpanCount,
} from '@/composables/useSpanTree'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

const props = defineProps<{ appId: string; bindings: { type: string; name: string }[] }>()
const router = useRouter()

interface MetricPoint { ts: string; value: number }
interface MetricSeries {
  targetType: string; targetId: string; name: string; unit: string
  current: number; points: MetricPoint[]
}
interface LogEntry { id: string; appId: string; level: string; message: string; traceId?: string; timestamp: string }
interface Workload { id: string; name: string; type: string; replicas: number; ready: number; status: string }
interface DataService { id: string; kind: string; name: string; status: string }
interface DepMetric { id: string; kind: string; name: string; status: string; cpu?: MetricSeries; mem?: MetricSeries; disk?: MetricSeries; net?: MetricSeries }

const activeTab = ref<'instance' | 'deps'>('instance')
const metrics = ref<MetricSeries[]>([])
const logs = ref<LogEntry[]>([])
const traces = ref<Trace[]>([])
const workloads = ref<Workload[]>([])
const deps = ref<DepMetric[]>([])
const loading = ref(false)
const depsLoading = ref(false)

const metricOrder = ['cpu', 'mem', 'rps', 'latency', 'errorRate']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟', errorRate: '错误率' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, TagType> = { info: 'info', warn: 'warning', error: 'danger' }
const traceStatusLabel: Record<string, string> = { success: '成功', error: '错误' }
const traceStatusType: Record<string, TagType> = { success: 'success', error: 'danger' }
const kindLabel: Record<string, string> = { db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量库', search: '搜索' }

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

// 副本就绪汇总：总期望 / 总就绪 / 是否全就绪。
const replicaSummary = computed(() => {
  const want = workloads.value.reduce((s, w) => s + (w.replicas || 0), 0)
  const ready = workloads.value.reduce((s, w) => s + (w.ready || 0), 0)
  return { want, ready, allReady: want > 0 && ready >= want }
})

// 依赖资源绑定（仅数据服务类，过滤 models/knowledgebase 等非数据服务）。
const dsKinds = new Set(['db', 'cache', 'mq', 'storage', 'vector', 'search'])
const depBindings = computed(() => props.bindings.filter((b) => dsKinds.has(b.type)))

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// spanRows：trace 的 span 树形 flatten（带 depth），驱动 v-for 树形缩进渲染。
function spanRows(row: Trace) {
  return flattenSpanTree(buildSpanTree(row.spans || []))
}

// trace 行 class：错误 trace 整行红色高亮（el-table row-class-name 回调）。
function traceRowClass({ row }: { row: Trace }): string {
  return row.status === 'error' || errSpanCount(row) ? 'trace-err-row' : ''
}

function sparkHeights(points: MetricPoint[]): number[] {
  if (points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}

async function loadMetrics() {
  const resp = await fetchAuth(`/api/observability/metrics?targetType=app&targetId=${props.appId}`)
  if (resp.ok) metrics.value = (await resp.json()).data ?? []
}
async function loadLogs() {
  const resp = await fetchAuth(`/api/observability/logs?appId=${props.appId}&limit=50`)
  if (resp.ok) logs.value = (await resp.json()).data ?? []
}
async function loadTraces() {
  const resp = await fetchAuth(`/api/observability/traces?appId=${props.appId}&limit=20`)
  if (resp.ok) traces.value = (await resp.json()).data ?? []
}
async function loadWorkloads() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/workloads`)
  if (resp.ok) workloads.value = (await resp.json()).data ?? []
}

async function loadAll(silent = false) {
  if (!silent) loading.value = true
  try {
    await Promise.all([loadMetrics(), loadLogs(), loadTraces(), loadWorkloads()])
  } finally {
    if (!silent) loading.value = false
  }
}

// 依赖资源：解析绑定名→数据服务 ID，逐个拉 CPU/内存。
async function loadDeps() {
  if (depBindings.value.length === 0) { deps.value = []; return }
  depsLoading.value = true
  try {
    const list = await fetchAuth(`/api/dataservices`)
    const all: DataService[] = list.ok ? (await list.json()).data ?? [] : []
    const byName = new Map(all.map((d) => [d.name, d]))
    const targets: DataService[] = []
    for (const b of depBindings.value) {
      const ds = byName.get(b.name)
      if (ds) targets.push(ds)
    }
    const out: DepMetric[] = []
    await Promise.all(targets.map(async (ds) => {
      const r = await fetchAuth(`/api/observability/metrics?targetType=dataservice&targetId=${ds.id}`)
      const series: MetricSeries[] = r.ok ? (await r.json()).data ?? [] : []
      out.push({
        id: ds.id, kind: ds.kind, name: ds.name, status: ds.status,
        cpu: series.find((m) => m.name === 'cpu'),
        mem: series.find((m) => m.name === 'mem'),
        disk: series.find((m) => m.name === 'disk_io'),
        net: series.find((m) => m.name === 'net_io'),
      })
    }))
    deps.value = out
  } finally {
    depsLoading.value = false
  }
}

function goDashboard() {
  router.push(`/platform/observability?app=${props.appId}`)
}

let timer: number | undefined
onMounted(() => {
  loadAll()
  loadDeps()
  timer = window.setInterval(() => { loadAll(true); if (activeTab.value === 'deps') loadDeps() }, 10000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })
watch(() => props.appId, () => { loadAll(); loadDeps() })
watch(() => props.bindings, () => loadDeps(), { deep: true })
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">可观测</span>
      <span class="tab-hint">指标 · 日志 · 链路（10s 自动刷新）</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goDashboard">
        在监控大屏中打开 →
      </el-button>
    </div>

    <el-tabs v-model="activeTab">
      <!-- 应用实例：计算 + 流量健康 + 副本就绪 + 日志 + 链路 -->
      <el-tab-pane label="应用实例" name="instance">
        <section v-loading="loading">
          <!-- 副本就绪状态条 -->
          <div v-if="replicaSummary.want > 0" class="replica-bar">
            <el-tag :type="replicaSummary.allReady ? 'success' : 'warning'" size="small" effect="dark">
              副本就绪 {{ replicaSummary.ready }} / {{ replicaSummary.want }}
            </el-tag>
            <span class="replica-hint">{{ workloads.length }} 个工作负载</span>
          </div>

          <div v-if="cards.length" class="metric-grid">
            <div v-for="c in cards" :key="c.name" class="metric-card">
              <div class="m-label">{{ c.label }}</div>
              <div class="m-value mono">{{ fmtVal(c.current) }}<span class="m-unit">{{ c.unit }}</span></div>
              <div class="spark">
                <span v-for="(h, idx) in sparkHeights(c.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
              </div>
            </div>
          </div>
          <div v-else class="empty">该应用暂无指标</div>
        </section>

        <section class="sub-block">
          <div class="sub-title">最近日志</div>
          <el-table :data="logs" size="small" height="260" empty-text="暂无日志">
            <el-table-column label="时间" width="170">
              <template #default="{ row }">{{ new Date(row.timestamp).toLocaleString() }}</template>
            </el-table-column>
            <el-table-column label="级别" width="80">
              <template #default="{ row }">
                <el-tag :type="(logLevelType[row.level]) || 'info'" size="small">{{ logLevelLabel[row.level] || row.level }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="message" label="消息" min-width="280" show-overflow-tooltip />
          </el-table>
        </section>

        <section class="sub-block">
          <div class="sub-title">最近链路</div>
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
                      <span class="mono span-svc">{{ node.span.service }}</span>
                      <span class="span-op">{{ node.span.operation }}</span>
                      <span class="mono span-dur">{{ node.span.durationMs }}ms</span>
                      <el-tag v-if="node.span.isError" type="danger" size="small" effect="dark">
                        异常<span v-if="node.span.errorType"> · {{ node.span.errorType }}</span>
                      </el-tag>
                    </div>
                    <div v-if="spanChips(node.span).length" class="span-chips">
                      <span v-for="c in spanChips(node.span)" :key="c.label" class="chip" :class="{ 'chip-err': c.err }">
                        <b class="chip-k">{{ c.label }}</b> <code>{{ c.v }}</code>
                      </span>
                    </div>
                    <details v-if="node.span.tags && Object.keys(node.span.tags).length" class="span-attrs">
                      <summary>全部属性 ({{ Object.keys(node.span.tags).length }})</summary>
                      <table class="attr-table"><tbody>
                        <tr v-for="(v, k) in node.span.tags" :key="k">
                          <td class="mono ak">{{ k }}</td>
                          <td class="mono av">{{ v }}</td>
                        </tr>
                      </tbody></table>
                    </details>
                    <div v-if="node.span.errorMessage || node.span.tags?.['exception.stacktrace']" class="span-exc">
                      <div v-if="node.span.errorMessage" class="exc-msg">⚠ {{ node.span.errorMessage }}</div>
                      <pre v-if="node.span.tags?.['exception.stacktrace']" class="exc-stack">{{ node.span.tags['exception.stacktrace'] }}</pre>
                    </div>
                  </div>
                </div>
              </template>
            </el-table-column>
            <el-table-column label="开始" width="170">
              <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
            </el-table-column>
            <el-table-column label="服务" min-width="140">
              <template #default="{ row }">
                <span v-if="row.service" class="mono">{{ row.service }}</span>
                <span v-else class="faint">-</span>
              </template>
            </el-table-column>
            <el-table-column label="操作" min-width="200">
              <template #default="{ row }"><span class="mono">{{ row.operation }}</span></template>
            </el-table-column>
            <el-table-column label="时长" width="80">
              <template #default="{ row }"><span class="mono">{{ row.durationMs }}ms</span></template>
            </el-table-column>
            <el-table-column label="状态" width="110">
              <template #default="{ row }">
                <el-tag :type="(traceStatusType[row.status]) || 'info'" size="small">{{ traceStatusLabel[row.status] || row.status }}</el-tag>
                <el-tag v-if="errSpanCount(row)" type="danger" size="small" effect="dark" style="margin-left:4px">
                  异常 {{ errSpanCount(row) }}/{{ row.spans.length }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="Span 数" width="80" align="center">
              <template #default="{ row }">{{ row.spans?.length ?? 0 }}</template>
            </el-table-column>
          </el-table>
        </section>
      </el-tab-pane>

      <!-- 依赖资源：绑定的数据服务各自负载 -->
      <el-tab-pane :label="`依赖资源 (${depBindings.length})`" name="deps">
        <section v-loading="depsLoading">
          <div v-if="deps.length" class="dep-grid">
            <div v-for="d in deps" :key="d.id" class="dep-card">
              <div class="dep-head">
                <span class="dep-kind">{{ kindLabel[d.kind] || d.kind }}</span>
                <span class="dep-name mono">{{ d.name }}</span>
                <el-tag :type="d.status === 'running' ? 'success' : 'info'" size="small">{{ d.status }}</el-tag>
              </div>
              <div class="dep-metrics">
                <div class="dep-metric">
                  <span class="dm-label">CPU</span>
                  <span class="mono dm-value">{{ d.cpu ? fmtVal(d.cpu.current) : '—' }}<span class="dm-unit">{{ d.cpu?.unit || '' }}</span></span>
                  <div v-if="d.cpu" class="spark">
                    <span v-for="(h, idx) in sparkHeights(d.cpu.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
                  </div>
                </div>
                <div class="dep-metric">
                  <span class="dm-label">内存</span>
                  <span class="mono dm-value">{{ d.mem ? fmtVal(d.mem.current) : '—' }}<span class="dm-unit">{{ d.mem?.unit || '' }}</span></span>
                  <div v-if="d.mem" class="spark">
                    <span v-for="(h, idx) in sparkHeights(d.mem.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
                  </div>
                </div>
                <div class="dep-metric">
                  <span class="dm-label">磁盘 IO</span>
                  <span class="mono dm-value">{{ d.disk ? fmtVal(d.disk.current) : '—' }}<span class="dm-unit">{{ d.disk?.unit || '' }}</span></span>
                  <div v-if="d.disk" class="spark">
                    <span v-for="(h, idx) in sparkHeights(d.disk.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
                  </div>
                </div>
                <div class="dep-metric">
                  <span class="dm-label">网络 IO</span>
                  <span class="mono dm-value">{{ d.net ? fmtVal(d.net.current) : '—' }}<span class="dm-unit">{{ d.net?.unit || '' }}</span></span>
                  <div v-if="d.net" class="spark">
                    <span v-for="(h, idx) in sparkHeights(d.net.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
                  </div>
                </div>
              </div>
            </div>
          </div>
          <div v-else-if="!depsLoading" class="empty">该应用未绑定数据服务，或数据服务暂无指标</div>
        </section>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.empty { padding: 32px 0; text-align: center; color: var(--text-faint); font-size: 13px; }
.replica-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.replica-hint { font-size: 12px; color: var(--text-faint); }
.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; margin-bottom: 20px; }
.metric-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 22px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 12px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 30px; margin-top: 6px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.sub-block { margin-top: 18px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.mono { font-family: var(--font-mono); }
:deep(.trace-err-row) { background: var(--danger-soft) !important; }
.span-list { padding: 6px 20px; display: flex; flex-direction: column; gap: 6px; }
/* 时间轴刻度（相对 0 → durationMs，与 span-bar left/width 同坐标系） */
.span-axis { display: flex; justify-content: space-between; padding: 0 6px 4px; border-bottom: 1px dashed var(--border); margin-bottom: 4px; }
.span-mono { font-family: var(--font-mono); font-size: 11px; color: var(--text-faint); }
.span-card { padding: 6px 8px; border-radius: 6px; background: var(--surface); border: 1px solid var(--border); position: relative; }
.span-card.span-err { border-color: var(--danger); background: var(--danger-soft); }
/* 树形层级：每层左缩进 18px + 竖线表父子关系（depth>0 才显） */
.span-tree-line { position: absolute; left: 4px; top: 0; bottom: 0; width: 1px; background: var(--border); }
.span-row { display: flex; align-items: center; gap: 10px; position: relative; padding: 2px 6px; font-size: 12px; }
/* 瀑布条：绝对定位甘特条贴行底，left=startMs%，width=durationMs%（时间轴对齐，一眼看串行/并行/等待） */
.span-bar { position: absolute; left: 0; bottom: 0; height: 4px; width: 0; background: rgba(99, 102, 241, 0.5); border-radius: 3px; z-index: 0; }
.span-err .span-bar { background: rgba(239, 68, 68, 0.55); }
.span-svc { color: var(--brand); min-width: 100px; position: relative; z-index: 1; }
.span-op { flex: 1; color: var(--text-dim); position: relative; z-index: 1; }
.span-dur { color: var(--text-faint); position: relative; z-index: 1; }
.span-chips { display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 2px; }
.chip { font-size: 11.5px; padding: 1px 7px; background: var(--surface-2, var(--surface)); border-radius: 4px; color: var(--text-dim); }
.chip code { color: var(--text); font-family: var(--font-mono); }
.chip-err { background: var(--danger-soft); color: var(--danger); }
.chip-err code { color: var(--danger); font-weight: 600; }
.chip-k { color: var(--text-faint); font-weight: 400; margin-right: 3px; }
.span-attrs { padding: 2px; font-size: 12px; }
.span-attrs summary { cursor: pointer; color: var(--brand); font-size: 11.5px; }
.attr-table { border-collapse: collapse; margin-top: 4px; width: 100%; }
.attr-table td { border: 1px solid var(--border); padding: 2px 8px; font-size: 11.5px; vertical-align: top; word-break: break-all; }
.attr-table .ak { color: var(--text-faint); white-space: nowrap; width: 1%; }
.attr-table .av { color: var(--text); }
.span-exc { margin: 4px 2px; padding: 6px 8px; border-left: 3px solid var(--danger); background: var(--danger-soft); border-radius: 4px; }
.exc-msg { font-size: 12px; color: var(--danger); font-weight: 600; }
.exc-stack { margin: 4px 0 0; padding: 6px; font-size: 11px; color: var(--text-dim); white-space: pre-wrap; word-break: break-all; max-height: 200px; overflow: auto; }
.dep-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.dep-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.dep-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.dep-kind { font-size: 12px; color: var(--text-dim); }
.dep-name { flex: 1; font-size: 13px; font-weight: 600; }
.dep-metrics { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 16px; }
.dep-metric { display: grid; grid-template-columns: 40px 90px 1fr; align-items: center; gap: 8px; }
.dm-label { font-size: 12px; color: var(--text-faint); }
.dm-value { font-size: 14px; font-weight: 600; }
.dm-unit { font-size: 11px; font-weight: 400; color: var(--text-faint); margin-left: 3px; }
.dep-metric .spark { height: 22px; margin-top: 0; }
</style>
