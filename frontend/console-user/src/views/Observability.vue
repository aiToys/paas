<script setup lang="ts">
// 平台能力 → 可观测（指标监控 + 告警规则）。
// target 选择（应用下拉）+ 4 指标卡（CPU/内存/RPS/延迟 当前值 + CSS sparkline 趋势）
// + 告警规则列表（增删）+ 当前告警列表（即时评估，severity 着色）。
// 惰性时序：每次加载后端补点；前端 10s 轮询刷新指标。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import {
  type Span, type Trace,
  buildSpanTree, flattenSpanTree, spanWidth, spanLeft, spanChips, errSpanCount,
} from '@/composables/useSpanTree'

const route = useRoute()

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

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
const traceStatusType: Record<string, TagType> = { success: 'success', error: 'danger' }

const metricOrder = ['cpu', 'mem', 'rps', 'latency', 'errorRate']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟', errorRate: '错误率' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, TagType> = { info: 'info', warn: 'warning', error: 'danger' }

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// spanRows：trace 的 span 树形 flatten（带 depth），驱动 v-for 树形缩进渲染。
// 每次展开调用一次（非 computed，因 row 是 el-table 展开行动态对象）。
function spanRows(row: Trace) {
  return flattenSpanTree(buildSpanTree(row.spans || []))
}

// trace 行 class：错误 trace 整行红色高亮（el-table row-class-name 回调）。
function traceRowClass({ row }: { row: Trace }): string {
  return row.status === 'error' || errSpanCount(row) ? 'trace-err-row' : ''
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
    // 支持 ?app=<id> 深链预选（来自应用详情「监控」入口）；否则取首个。
    const q = route.query.app
    const pre = typeof q === 'string' && q ? q : ''
    if (!targetApp.value) {
      targetApp.value = pre && apps.value.some((a) => a.id === pre)
        ? pre
        : (apps.value[0]?.id ?? '')
    }
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
  name: '', metricName: 'cpu', operator: '>', threshold: 80, severity: 'warning', enabled: true,
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
  try {
    const resp = await fetchAuth(`/api/observability/alert-rules/${r.id}`, { method: 'DELETE' })
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

let timer: number | undefined
onMounted(async () => {
  await loadApps()
  await loadAll()
  timer = window.setInterval(() => loadAll(true), 10000) // 10s 轮询刷新指标/告警（silent 不闪烁）
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
        <el-select v-model="targetApp" placeholder="选择应用" style="width: 200px" @change="() => loadAll()">
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
            <el-tag :type="(logLevelType[row.level]) || 'info'" size="small">
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

.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.metric-card { padding: 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 26px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 13px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 36px; margin-top: 8px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.cond-row { display: flex; gap: 8px; width: 100%; }
</style>
