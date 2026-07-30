<script setup lang="ts">
// 平台能力 → 可观测（指标监控 + 告警规则）。
// target 选择（应用下拉）+ 4 指标卡（CPU/内存/RPS/延迟 当前值 + CSS sparkline 趋势）
// + 告警规则列表（增删）+ 当前告警列表（即时评估，severity 着色）。
// 惰性时序：每次加载后端补点；前端 10s 轮询刷新指标。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'

interface App { id: string; name: string }
interface MetricPoint { ts: string; value: number }
interface MetricSeries {
  targetType: string; targetId: string; name: string; unit: string
  current: number; points: MetricPoint[]
}
interface AlertRule {
  id: string; name: string; metricName: string; targetType: string; targetId?: string
  operator: string; threshold: number; severity: string; enabled: boolean
}
interface Alert {
  ruleId: string; ruleName: string; targetId: string; metricName: string
  value: number; threshold: number; operator: string; severity: string
}
interface LogEntry {
  id: string; appId: string; level: string; message: string; traceId?: string; timestamp: string
}
interface Span {
  id: string; parentId?: string; operation: string; service: string
  startMs: number; durationMs: number; tags?: Record<string, string>
}
interface Trace {
  id: string; appId: string; operation: string; status: string
  durationMs: number; startedAt: string; spans: Span[]
}

const apps = ref<App[]>([])
const targetApp = ref('')
const metrics = ref<MetricSeries[]>([])
const rules = ref<AlertRule[]>([])
const alerts = ref<Alert[]>([])
const logs = ref<LogEntry[]>([])
const logLevel = ref('')
const logQ = ref('')
const traces = ref<Trace[]>([])
const traceStatus = ref('')
const loading = ref(false)

const traceStatusLabel: Record<string, string> = { success: '成功', error: '错误' }
const traceStatusType: Record<string, string> = { success: 'success', error: 'danger' }

const metricOrder = ['cpu', 'mem', 'rps', 'latency']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, string> = { info: 'info', warn: 'warning', error: 'danger' }

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// span 宽度（按占 trace 时长比例，最小 8%）
function spanWidth(sp: Span, row: Trace): number {
  if (!row.durationMs) return 8
  return Math.max(8, Math.round((sp.durationMs / row.durationMs) * 100))
}

// sparkline：把 points 映射成 100% 内的高度数组
function sparkHeights(points: MetricPoint[]): number[] {
  if (points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  // 取最近 24 点
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}

async function loadApps() {
  const resp = await fetchAuth('/api/applications')
  if (resp.ok) {
    const json = await resp.json()
    apps.value = (json.data ?? []) as App[]
    if (!targetApp.value && apps.value.length) targetApp.value = apps.value[0].id
  }
}

async function loadMetrics() {
  if (!targetApp.value) return
  const resp = await fetchAuth(`/api/observability/metrics?targetType=app&targetId=${targetApp.value}`)
  if (resp.ok) metrics.value = (await resp.json()).data ?? []
}

async function loadRules() {
  const resp = await fetchAuth('/api/observability/alert-rules')
  if (resp.ok) rules.value = (await resp.json()).data ?? []
}

async function loadAlerts() {
  const resp = await fetchAuth('/api/observability/alerts')
  if (resp.ok) alerts.value = (await resp.json()).data ?? []
}

async function loadLogs() {
  const params = new URLSearchParams()
  if (targetApp.value) params.set('appId', targetApp.value)
  if (logLevel.value) params.set('level', logLevel.value)
  if (logQ.value.trim()) params.set('q', logQ.value.trim())
  const resp = await fetchAuth(`/api/observability/logs?${params.toString()}`)
  if (resp.ok) logs.value = (await resp.json()).data ?? []
}

async function loadTraces() {
  const params = new URLSearchParams()
  if (targetApp.value) params.set('appId', targetApp.value)
  if (traceStatus.value) params.set('status', traceStatus.value)
  const resp = await fetchAuth(`/api/observability/traces?${params.toString()}`)
  if (resp.ok) traces.value = (await resp.json()).data ?? []
}

async function loadAll() {
  loading.value = true
  try {
    await Promise.all([loadMetrics(), loadRules(), loadAlerts(), loadLogs(), loadTraces()])
  } finally {
    loading.value = false
  }
}

const showRule = ref(false)
const ruleForm = ref({
  name: '', metricName: 'cpu', operator: '>', threshold: 80, severity: 'warning', enabled: true,
})
const ruleSubmitting = ref(false)

const metricsOpts = [
  { value: 'cpu', label: 'CPU (%)' },
  { value: 'mem', label: '内存 (%)' },
  { value: 'rps', label: '请求/秒' },
  { value: 'latency', label: 'P95 延迟 (ms)' },
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

function openRule() {
  ruleForm.value = {
    name: '', metricName: 'cpu', operator: '>', threshold: 80,
    severity: 'warning', enabled: true,
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
    const resp = await fetchAuth('/api/observability/alert-rules', {
      method: 'POST',
      body: JSON.stringify({
        name: ruleForm.value.name,
        metricName: ruleForm.value.metricName,
        targetType: 'app',
        targetId: targetApp.value,
        operator: ruleForm.value.operator,
        threshold: ruleForm.value.threshold,
        severity: ruleForm.value.severity,
        enabled: ruleForm.value.enabled,
      }),
    })
    if (resp.ok) {
      ElMessage.success('规则已创建')
      showRule.value = false
      loadRules()
      loadAlerts()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败')
    }
  } finally {
    ruleSubmitting.value = false
  }
}

async function deleteRule(r: AlertRule) {
  const resp = await fetchAuth(`/api/observability/alert-rules/${r.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已删除')
    loadRules()
    loadAlerts()
  }
}

let timer: number | undefined
onMounted(async () => {
  await loadApps()
  await loadAll()
  timer = window.setInterval(loadAll, 10000) // 10s 轮询刷新指标/告警
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="obs-page">
    <div class="page-head">
      <div>
        <h2>可观测</h2>
        <p class="sub">指标监控 + 告警规则 · 惰性时序模拟采集（10s 自动刷新）</p>
      </div>
      <div class="head-actions">
        <el-select v-model="targetApp" placeholder="选择应用" style="width: 200px" @change="loadMetrics(); loadAlerts(); loadLogs(); loadTraces()">
          <el-option v-for="a in apps" :key="a.id" :label="a.name" :value="a.id" />
        </el-select>
        <el-button type="primary" @click="openRule">+ 告警规则</el-button>
      </div>
    </div>

    <!-- 当前告警 -->
    <section class="block">
      <div class="block-title">
        当前告警
        <span class="cnt" :class="{ firing: alerts.length }">{{ alerts.length }} 条 firing</span>
      </div>
      <el-empty v-if="!alerts.length" description="无活跃告警" :image-size="48" />
      <div v-else class="alert-list">
        <div v-for="(a, i) in alerts" :key="i" class="alert-row" :class="a.severity">
          <span class="sev-tag" :class="a.severity">{{ a.severity === 'critical' ? '严重' : '警告' }}</span>
          <span class="alert-name">{{ a.ruleName }}</span>
          <span class="alert-target mono">{{ a.targetId }} · {{ a.metricName }}</span>
          <span class="alert-val mono">{{ a.value.toFixed(1) }} {{ a.operator }} {{ a.threshold }}</span>
        </div>
      </div>
    </section>

    <!-- 指标卡 -->
    <section class="block" v-loading="loading">
      <div class="block-title">关键指标 · {{ apps.find((a) => a.id === targetApp)?.name }}</div>
      <div v-if="cards.length" class="metric-grid">
        <div v-for="c in cards" :key="c.name" class="metric-card">
          <div class="m-label">{{ c.label }}</div>
          <div class="m-value mono">
            {{ fmtVal(c.current) }}<span class="m-unit">{{ c.unit }}</span>
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
      <el-empty v-else description="该应用暂无指标" :image-size="48" />
    </section>

    <!-- 告警规则 -->
    <section class="block">
      <div class="block-title">告警规则（当前应用）</div>
      <el-table :data="rules.filter((r) => !targetApp || !r.targetId || r.targetId === targetApp)" size="small" empty-text="该应用暂无规则">
        <el-table-column prop="name" label="规则名" min-width="160" />
        <el-table-column label="条件" min-width="180">
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

    <!-- 应用日志（可观测 Logs） -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">应用日志 · {{ apps.find((a) => a.id === targetApp)?.name ?? '全部' }}</span>
        <div class="log-filter">
          <el-select v-model="logLevel" placeholder="全部级别" clearable size="small" style="width: 120px" @change="loadLogs">
            <el-option label="信息" value="info" />
            <el-option label="警告" value="warn" />
            <el-option label="错误" value="error" />
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
            <el-tag :type="(logLevelType[row.level] as any) || 'info'" size="small">
              {{ logLevelLabel[row.level] || row.level }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="应用" width="120">
          <template #default="{ row }">
            <span class="mono">{{ apps.find((a) => a.id === row.appId)?.name ?? row.appId }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="消息" min-width="280" show-overflow-tooltip />
        <el-table-column label="TraceID" width="140">
          <template #default="{ row }"><span class="mono faint">{{ row.traceId ? row.traceId.slice(0, 12) : '—' }}</span></template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 链路追踪（可观测 Traces） -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">链路追踪 · {{ apps.find((a) => a.id === targetApp)?.name ?? '全部' }}</span>
        <div class="log-filter">
          <el-select v-model="traceStatus" placeholder="全部状态" clearable size="small" style="width: 120px" @change="loadTraces">
            <el-option label="成功" value="success" />
            <el-option label="错误" value="error" />
          </el-select>
          <el-button size="small" @click="loadTraces">刷新</el-button>
        </div>
      </div>
      <el-table :data="traces" size="small" row-key="id" empty-text="暂无链路">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="span-list">
              <div v-for="sp in row.spans" :key="sp.id" class="span-row">
                <span class="span-bar" :style="{ width: spanWidth(sp, row) + '%' }" />
                <span class="mono span-svc">{{ sp.service }}</span>
                <span class="span-op">{{ sp.operation }}</span>
                <span class="mono span-dur">{{ sp.durationMs }}ms</span>
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
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="(traceStatusType[row.status] as any) || 'info'" size="small">
              {{ traceStatusLabel[row.status] || row.status }}
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
.span-list { padding: 8px 24px; display: flex; flex-direction: column; gap: 6px; }
.span-row { display: flex; align-items: center; gap: 10px; position: relative; padding: 4px 8px; border-radius: 4px; background: var(--surface); overflow: hidden; }
.span-bar { position: absolute; left: 0; top: 0; bottom: 0; width: 0; background: rgba(99, 102, 241, 0.18); }
.span-svc { position: relative; min-width: 120px; font-size: 12px; color: var(--brand); }
.span-op { position: relative; flex: 1; font-size: 12px; color: var(--text-dim); }
.span-dur { position: relative; font-size: 12px; color: var(--text-faint); }
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

.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.metric-card { padding: 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 26px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 13px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 36px; margin-top: 8px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.cond-row { display: flex; gap: 8px; width: 100%; }
</style>
