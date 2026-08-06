<script setup lang="ts">
// 应用详情 - 可观测 tab：4 指标卡 + 最近日志 + 最近 trace（预选当前应用）。
// 复用 /api/observability/{metrics,logs,traces}?appId=（已按应用过滤）。
// 10s 轮询指标；深度排查去 /platform/observability?app=。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

const props = defineProps<{ appId: string }>()
const router = useRouter()

interface MetricPoint { ts: string; value: number }
interface MetricSeries {
  targetType: string; targetId: string; name: string; unit: string
  current: number; points: MetricPoint[]
}
interface LogEntry { id: string; appId: string; level: string; message: string; traceId?: string; timestamp: string }
interface Span { id: string; parentId?: string; operation: string; service: string; startMs: number; durationMs: number }
interface Trace { id: string; appId: string; operation: string; status: string; durationMs: number; startedAt: string; spans: Span[] }

const metrics = ref<MetricSeries[]>([])
const logs = ref<LogEntry[]>([])
const traces = ref<Trace[]>([])
const loading = ref(false)

const metricOrder = ['cpu', 'mem', 'rps', 'latency']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, TagType> = { info: 'info', warn: 'warning', error: 'danger' }
const traceStatusLabel: Record<string, string> = { success: '成功', error: '错误' }
const traceStatusType: Record<string, TagType> = { success: 'success', error: 'danger' }

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// sparkline 高度（最近 24 点映射到 20-100% 区间）。
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

async function loadAll(silent = false) {
  if (!silent) loading.value = true
  try {
    await Promise.all([loadMetrics(), loadLogs(), loadTraces()])
  } finally {
    if (!silent) loading.value = false
  }
}

function goDashboard() {
  router.push(`/platform/observability?app=${props.appId}`)
}

let timer: number | undefined
onMounted(() => {
  loadAll()
  timer = window.setInterval(() => loadAll(true), 10000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })
watch(() => props.appId, () => loadAll())
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

    <!-- 指标卡 -->
    <section v-loading="loading">
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

    <!-- 日志 -->
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

    <!-- trace -->
    <section class="sub-block">
      <div class="sub-title">最近链路</div>
      <el-table :data="traces" size="small" row-key="id" empty-text="暂无链路">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="span-list">
              <div v-for="sp in row.spans" :key="sp.id" class="span-row">
                <span class="mono span-svc">{{ sp.service }}</span>
                <span class="span-op">{{ sp.operation }}</span>
                <span class="mono span-dur">{{ sp.durationMs }}ms</span>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="开始" width="170">
          <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" min-width="200">
          <template #default="{ row }"><span class="mono">{{ row.operation }}</span></template>
        </el-table-column>
        <el-table-column label="时长" width="80">
          <template #default="{ row }"><span class="mono">{{ row.durationMs }}ms</span></template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="(traceStatusType[row.status]) || 'info'" size="small">{{ traceStatusLabel[row.status] || row.status }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.empty { padding: 32px 0; text-align: center; color: var(--text-faint); font-size: 13px; }
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
.span-list { padding: 6px 20px; display: flex; flex-direction: column; gap: 4px; }
.span-row { display: flex; align-items: center; gap: 10px; font-size: 12px; }
.span-svc { color: var(--brand); min-width: 100px; }
.span-op { flex: 1; color: var(--text-dim); }
.span-dur { color: var(--text-faint); }
</style>
