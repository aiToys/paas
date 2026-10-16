<script setup lang="ts">
// 可观测三支柱（v2 原生，功能对等迁移 2026-10-16）：
// - 告警：触发中事件 + 规则启停闭环
// - 日志：应用/级别/关键字过滤 + traceId 复制（排障入口）
// - 链路：trace 列表 + 行展开 span 瀑布（useSpanTree 树形缩进 + 时间轴甘特条）
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listAlerts, listAlertRules, listAlertEvents, listLogs, listTraces,
  type AlertRule, type AlertEvent, type LogEntry,
} from '@/api/observability'
import { buildSpanTree, flattenSpanTree, type Trace } from '@/composables/useSpanTree'
import { apiError, fetchList } from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'

const tab = ref<'alerts' | 'logs' | 'traces'>('alerts')
const errors = ref<Record<string, string>>({})

// —— 告警 ——
const alerts = ref<(AlertEvent & { live?: boolean })[]>([])
const rules = ref<AlertRule[]>([])
const loading = ref(true)

async function loadAlerts() {
  loading.value = true
  errors.value.alerts = ''
  try {
    const [al, rl, ev] = await Promise.all([
      listAlerts(),
      listAlertRules(),
      listAlertEvents(30),
    ])
    const liveKeys = new Set(al.map((a) => `${a.ruleId}:${a.targetId}`))
    alerts.value = ev.map((e) => ({ ...e, live: liveKeys.has(`${e.ruleId}:${e.targetId}`) && e.status === 'firing' }))
    rules.value = rl
  } catch (e) {
    errors.value.alerts = apiError(e, '加载告警失败')
  } finally {
    loading.value = false
  }
}

async function toggleRule(r: AlertRule) {
  try {
    const { updateAlertRule } = await import('@/api/observability')
    await updateAlertRule(r.id, { enabled: !r.enabled })
    ElMessage.success(r.enabled ? '规则已停用' : '规则已启用')
    await loadAlerts()
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  }
}

// —— 应用过滤公共项（日志/链路共用） ——
interface AppLite { id: string; name: string }
const apps = ref<AppLite[]>([])
const appFilter = ref('')

// —— 日志 ——
const logs = ref<LogEntry[]>([])
const logsLoading = ref(false)
const logLevel = ref('')
const logQ = ref('')
const rangeSel = ref('1h')

async function loadLogs() {
  logsLoading.value = true
  errors.value.logs = ''
  try {
    const params: Record<string, string> = { range: rangeSel.value }
    if (appFilter.value) params.appId = appFilter.value
    if (logLevel.value) params.level = logLevel.value
    if (logQ.value.trim()) params.q = logQ.value.trim()
    logs.value = await listLogs(params)
  } catch (e) { errors.value.logs = apiError(e, '日志加载失败') } finally {
    logsLoading.value = false
  }
}

async function copyText(t: string) {
  try {
    await navigator.clipboard.writeText(t)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败')
  }
}

// —— 链路 ——
const traces = ref<Trace[]>([])
const tracesLoading = ref(false)
const traceStatus = ref('')
const expandedTrace = ref('') // 行展开：当前展开的 traceID

async function loadTraces() {
  tracesLoading.value = true
  errors.value.traces = ''
  try {
    const params: Record<string, string> = { range: rangeSel.value }
    if (appFilter.value) params.appId = appFilter.value
    if (traceStatus.value) params.status = traceStatus.value
    traces.value = await listTraces<Trace>(params)
  } catch (e) { errors.value.traces = apiError(e, '链路加载失败') } finally {
    tracesLoading.value = false
  }
}

// 展开 trace 时才构建瀑布（flatten：depth 缩进 + startMs/durationMs 甘特几何）。
const flatSpansOf = (t: Trace) => flattenSpanTree(buildSpanTree(t.spans ?? []))
const traceDuration = (t: Trace) => t.durationMs || Math.max(0, ...(t.spans ?? []).map((s) => s.startMs + s.durationMs), 1)

async function toggleTrace(t: Trace) {
  expandedTrace.value = expandedTrace.value === t.id ? '' : t.id
}

// tab 惰性加载：首次切入才拉对应支柱数据。
const loadedTabs = new Set<string>(['alerts'])
async function switchTab(t: 'alerts' | 'logs' | 'traces') {
  tab.value = t
  if (loadedTabs.has(t)) return
  loadedTabs.add(t)
  if (t === 'logs') await loadLogs()
  if (t === 'traces') await loadTraces()
}

// 进行中告警时 30s 轮询刷新（页面不可见跳过）。
let timer: number | undefined
onMounted(async () => {
  const list = await fetchList<AppLite>('/api/applications').catch(() => [])
  apps.value = list
  await loadAlerts()
  timer = window.setInterval(() => {
    if (document.hidden || tab.value !== 'alerts') return
    void loadAlerts()
  }, 30000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })

const SEVERITY_LABEL: Record<string, string> = { critical: '严重', warning: '警告', info: '提示' }
function fmtTime(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const diff = Math.max(0, Date.now() - t)
  const m = Math.floor(diff / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  if (m < 1440) return `${Math.floor(m / 60)} 小时前`
  return `${Math.floor(m / 1440)} 天前`
}
const fmtClock = (iso: string) => (iso ?? '').replace('T', ' ').slice(11, 19)
const liveCount = computed(() => alerts.value.filter((a) => a.live).length)
</script>

<template>
  <div class="page">
<PageHeader title="可观测" description="从告警开始排查，使用日志与调用链定位具体原因。" eyebrow="基础设施" />

    <!-- tab 切换 + 公共过滤（应用/时间窗） -->
    <div class="toolbar">
      <div class="tabs">
        <button v-for="t in (['alerts','logs','traces'] as const)" :key="t" class="tab" :class="{ on: tab === t }" @click="switchTab(t)">
          {{ t === 'alerts' ? `告警${liveCount ? ' · ' + liveCount : ''}` : t === 'logs' ? '日志' : '链路' }}
        </button>
      </div>
      <div v-if="tab !== 'alerts'" class="filters">
        <select v-model="appFilter" class="f-select" @change="tab === 'logs' ? loadLogs() : loadTraces()">
          <option value="">全部产品</option>
          <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
        </select>
        <select v-model="rangeSel" class="f-select" @change="tab === 'logs' ? loadLogs() : loadTraces()">
          <option value="15m">15 分钟</option>
          <option value="1h">1 小时</option>
          <option value="6h">6 小时</option>
          <option value="24h">24 小时</option>
        </select>
        <button class="act" @click="tab === 'logs' ? loadLogs() : loadTraces()">刷新</button>
      </div>
    </div>

<StatePanel v-if="errors[tab]" title="观测数据加载失败" :description="errors[tab]" error><button class="act" @click="tab === 'alerts' ? loadAlerts() : tab === 'logs' ? loadLogs() : loadTraces()">重新加载</button></StatePanel>
    <template v-else>
    <!-- 告警 -->
    <template v-if="tab === 'alerts'">
      <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载告警…"></div>
      <template v-else>
        <div class="section-title">告警事件（{{ liveCount }} 触发中）</div>
        <div v-if="!alerts.length" class="empty small-empty">暂无告警记录</div>
        <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table">
          <thead><tr><th>规则</th><th>级别</th><th>状态</th><th>值/阈值</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="(a, i) in alerts" :key="a.id ?? i" :class="{ live: a.live }">
              <td><span class="s-name">{{ a.ruleName }}</span></td>
              <td><span class="cap-tag" :class="a.severity">{{ SEVERITY_LABEL[a.severity] ?? a.severity }}</span></td>
              <td><span class="status" :class="a.status === 'firing' ? 'err' : 'ok'">{{ a.status === 'firing' ? '触发中' : '已恢复' }}</span></td>
              <td class="mono dim">{{ a.value }} {{ a.operator }} {{ a.threshold }}</td>
              <td class="dim">{{ fmtTime(a.occurredAt ?? a.firedAt) }}</td>
            </tr>
          </tbody>
        </table>
</div>

        <div class="section-title">告警规则（{{ rules.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table">
          <thead><tr><th>规则</th><th>指标</th><th>条件</th><th>级别</th><th class="col-act"></th></tr></thead>
          <tbody>
            <tr v-for="r in rules" :key="r.id">
              <td><span class="s-name">{{ r.name }}</span></td>
              <td class="mono dim">{{ r.metricName }}</td>
              <td class="mono dim">{{ r.operator }} {{ r.threshold }}</td>
              <td><span class="cap-tag">{{ SEVERITY_LABEL[r.severity] ?? r.severity }}</span></td>
              <td class="col-act"><button class="act" @click="toggleRule(r)">{{ r.enabled ? '停用' : '启用' }}</button></td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
    </template>

    <!-- 日志 -->
    <template v-else-if="tab === 'logs'">
      <div class="filters row">
        <select v-model="logLevel" class="f-select" @change="loadLogs">
          <option value="">全部级别</option>
          <option value="error">ERROR</option>
          <option value="warn">WARN</option>
          <option value="info">INFO</option>
        </select>
        <input v-model="logQ" class="f-input" placeholder="关键字…" @keyup.enter="loadLogs" />
        <button class="act" @click="loadLogs">查询</button>
      </div>
      <div v-if="logsLoading" class="loading-block" v-loading="true"></div>
      <div v-else-if="!logs.length" class="empty small-empty">时间窗内无日志</div>
      <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table log-table">
        <thead><tr><th style="width:80px">时间</th><th style="width:70px">级别</th><th style="width:120px">应用</th><th>消息</th><th style="width:80px">trace</th></tr></thead>
        <tbody>
          <tr v-for="(l, i) in logs.slice(0, 200)" :key="l.id ?? i">
            <td class="mono dim">{{ fmtClock(l.timestamp) }}</td>
            <td>
              <span class="status" :class="l.level === 'error' ? 'err' : l.level === 'warn' ? 'warn' : 'idle'">
                {{ l.level.toUpperCase() }}
              </span>
            </td>
            <td class="dim">{{ apps.find((a) => a.id === l.appId)?.name ?? l.appId.slice(0, 8) }}</td>
            <td><span class="log-msg mono">{{ l.message }}</span></td>
            <td>
              <button v-if="l.traceId" class="act tid" :title="l.traceId" @click="copyText(l.traceId)">⧉ 复制</button>
            </td>
          </tr>
        </tbody>
      </table>
</div>
    </template>

    <!-- 链路 -->
    <template v-else>
      <div class="filters row">
        <select v-model="traceStatus" class="f-select" @change="loadTraces">
          <option value="">全部状态</option>
          <option value="error">含错误</option>
          <option value="ok">正常</option>
        </select>
      </div>
      <div v-if="tracesLoading" class="loading-block" v-loading="true"></div>
      <div v-else-if="!traces.length" class="empty small-empty">时间窗内无链路</div>
      <template v-else>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table">
          <thead><tr><th style="width:170px">traceID</th><th>服务 / 操作</th><th style="width:80px">耗时</th><th style="width:70px">状态</th><th style="width:80px">时间</th></tr></thead>
          <tbody>
            <template v-for="t in traces.slice(0, 50)" :key="t.id">
              <tr class="clickable" @click="toggleTrace(t)">
                <td>
                  <span class="mono dim tid-text">{{ t.id.slice(0, 16) }}…</span>
                  <button class="act tid" title="复制完整 traceID" @click.stop="copyText(t.id)">⧉</button>
                </td>
                <td><span class="s-name">{{ t.service || t.appId.slice(0, 12) }}</span> <span class="dim">· {{ t.operation }}</span></td>
                <td class="mono dim">{{ t.durationMs }}ms</td>
                <td><span class="status" :class="t.status === 'error' ? 'err' : 'ok'">{{ t.status === 'error' ? '错误' : '正常' }}</span></td>
                <td class="dim">{{ fmtTime(t.startedAt) }}</td>
              </tr>
              <!-- 行展开：span 瀑布（depth 缩进 + 时间轴甘特条） -->
              <tr v-if="expandedTrace === t.id" class="span-row">
                <td colspan="5">
                  <div class="waterfall">
                    <div class="wf-scale">
                      <span class="mono">0</span>
                      <span class="mono">{{ Math.round(traceDuration(t) / 2) }}ms</span>
                      <span class="mono">{{ traceDuration(t) }}ms</span>
                    </div>
                    <div
                      v-for="f in flatSpansOf(t)" :key="f.span.id"
                      class="wf-row" :class="{ err: f.span.isError }"
                      :title="`${f.span.service} · ${f.span.operation}${f.span.errorMessage ? ' · ' + f.span.errorMessage : ''}`"
                    >
                      <span class="wf-op" :style="{ paddingLeft: f.depth * 14 + 'px' }">
                        <span v-if="f.depth > 0" class="wf-branch">└</span>
                        <span class="mono">{{ f.span.operation }}</span>
                        <span v-if="f.span.peer" class="dim wf-peer">→ {{ f.span.peer }}</span>
                      </span>
                      <span class="wf-track">
                        <span
                          class="wf-bar"
                          :style="{
                            left: (f.span.startMs / traceDuration(t)) * 100 + '%',
                            width: Math.max(0.6, (f.span.durationMs / traceDuration(t)) * 100) + '%',
                          }"
                        />
                      </span>
                      <span class="wf-dur mono dim">{{ f.span.durationMs }}ms</span>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
</div>
      </template>
    </template>
    </template>
  </div>
</template>

<style scoped>
@import './list-page.css';
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 4px;
}
.tabs { display: flex; gap: 4px; }
.tab {
  padding: 6px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
}
.tab.on {
  border-color: var(--brand);
  color: var(--brand);
  background: var(--brand-soft);
  font-weight: 600;
}
.filters { display: flex; align-items: center; gap: 8px; }
.filters.row { margin: 10px 0 4px; }
.f-select, .f-input {
  padding: 6px 10px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-family: inherit;
  font-size: 12.5px;
}
.f-input { width: 220px; }
.section-title { margin: 18px 0 4px; font-size: 13.5px; font-weight: 600; }
.small-empty { padding: 24px 0; }
tr.live td { background: rgba(244, 63, 94, 0.06); }
.cap-tag.critical, .cap-tag.critical2 { border-color: rgba(244, 63, 94, 0.4); color: var(--danger, #f43f5e); }
.cap-tag.warning { border-color: rgba(245, 158, 11, 0.4); color: var(--warning, #f59e0b); }
.col-act { text-align: right; }
/* 日志 */
.log-msg { word-break: break-all; font-size: 12px; color: var(--text-dim); }
/* 链路瀑布 */
.tid-text { font-size: 12px; }
.span-row td { background: var(--surface-2); padding: 12px 16px; }
.waterfall { display: flex; flex-direction: column; gap: 2px; }
.wf-scale {
  display: flex;
  justify-content: space-between;
  font-size: 10.5px;
  color: var(--text-faint);
  margin-left: 42%;
  margin-bottom: 4px;
  border-bottom: 1px dashed var(--border);
  padding-bottom: 2px;
}
.wf-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  padding: 2px 0;
}
.wf-op {
  width: 42%;
  flex-shrink: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-branch { color: var(--text-faint); margin-right: 2px; }
.wf-peer { font-size: 11px; }
.wf-track {
  flex: 1;
  position: relative;
  height: 10px;
  background: rgba(127, 127, 127, 0.08);
  border-radius: 2px;
}
.wf-bar {
  position: absolute;
  top: 1px;
  height: 8px;
  border-radius: 2px;
  background: var(--brand-solid);
  opacity: 0.75;
}
.wf-row.err .wf-bar { background: var(--danger, #f43f5e); }
.wf-row.err .wf-op { color: var(--danger, #f43f5e); }
.wf-dur { width: 52px; flex-shrink: 0; text-align: right; font-size: 11px; }
</style>
