<script setup lang="ts">
// 流水线运行视图（横向阶段轨道版）：运行摘要条 + 横向 stage 轨道 + 选中阶段详情面板。
// 轨道节点圆图标 + 连线表达「流」：已完成实线着色（绿/红）、未到灰虚线——一眼看出走到哪、卡在哪。
// 点节点切换下方详情面板（错误/输出物/日志）；build 日志保留 SSE 实时流 + 终态全量。
// 5s 轮询仅运行中/暂停持续，终态自动停止。断连静默重试下次。
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getRun, approveStage, abortRun, retryRun, getBuildRun,
  type PipelineRun, type StageRun, laneOf, buildRunIdOf,
} from '@/api/pipeline'

const props = defineProps<{ runId: string }>()

const run = ref<PipelineRun | null>(null)
const loading = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const POLL_INTERVAL = 5000

const runStatusLabel = (s?: string): string => {
  const m: Record<string, string> = { running: '运行中', paused: '等待审批', succeeded: '成功', failed: '失败', aborted: '已中止' }
  return m[s || ''] || s || '-'
}
const runStatusType = (s?: string): string => {
  const m: Record<string, string> = { succeeded: 'success', failed: 'danger', aborted: 'info', running: 'warning', paused: 'warning' }
  return m[s || ''] || 'info'
}
const stageIcon = (s: string): string => {
  const m: Record<string, string> = { succeeded: '✓', failed: '✗', aborted: '⏹', running: '◌', pending: '·', waiting: '⏸', skipped: '–' }
  return m[s] || '·'
}
// 节点圆 + 已完成连线段的状态色（Element 色彩变量）
const stageColorVar = (s: string): string => {
  const m: Record<string, string> = {
    succeeded: 'var(--el-color-success)',
    failed: 'var(--el-color-danger)',
    aborted: 'var(--el-color-info)',
    running: 'var(--el-color-primary)',
    waiting: 'var(--el-color-warning)',
  }
  return m[s] || 'var(--el-text-color-disabled)'
}
// 节点状态小字
const stageStatusLabel = (s: StageRun): string => {
  const m: Record<string, string> = { running: '运行中', waiting: '等待审批', pending: '等待', skipped: '已跳过', aborted: '已中止' }
  if (m[s.status]) return m[s.status]
  return stageDuration(s)
}

const isTerminal = computed(() => {
  const s = run.value?.status
  return s === 'succeeded' || s === 'failed' || s === 'aborted'
})

// 当前 stage 是否可 approve（paused + 当前 stage 为 approve/test-manual）
const canApprove = computed(() => {
  const r = run.value
  if (!r || r.status !== 'paused') return false
  const cur = r.stageRuns[r.currentStage]
  if (!cur) return false
  if (cur.type === 'approve') return true
  if (cur.type === 'test' && cur.input?.mode === 'manual') return true
  return false
})

const canAbort = computed(() => {
  const s = run.value?.status
  return s === 'running' || s === 'paused'
})
// 失败 run 可重试（从失败 stage 重新推进，调试闭环）
const canRetry = computed(() => run.value?.status === 'failed')

async function load() {
  try {
    loading.value = true
    run.value = await getRun(props.runId)
  } catch (e: any) {
    ElMessage.error(e?.message || '加载运行失败')
  } finally {
    loading.value = false
  }
}

async function approve() {
  const r = run.value
  if (!r) return
  try {
    await ElMessageBox.confirm('确认批准当前阶段、继续执行后续阶段？', '审批确认', { type: 'warning' })
    await approveStage(r.id, r.currentStage)
    ElMessage.success('已批准')
    await load()
  } catch (e: any) {
    if (e === 'cancel') return
    ElMessage.error(e?.message || '审批失败')
  }
}

async function abort() {
  const r = run.value
  if (!r) return
  try {
    await ElMessageBox.confirm('确认中止本次运行？已完成阶段保留，未执行阶段标记为已跳过。', '中止确认', { type: 'warning' })
    await abortRun(r.id)
    ElMessage.success('已中止')
    await load()
  } catch (e: any) {
    if (e === 'cancel') return
    ElMessage.error(e?.message || '中止失败')
  }
}

async function retry() {
  const r = run.value
  if (!r) return
  try {
    await retryRun(r.id)
    ElMessage.success('已从失败阶段重试')
    await load()
  } catch (e: any) {
    ElMessage.error(e?.message || '重试失败')
  }
}

function startPolling() {
  stopPolling()
  timer = setInterval(async () => {
    if (isTerminal.value) { stopPolling(); return }
    try {
      run.value = await getRun(props.runId)
    } catch { /* 静默重试 */ }
  }, POLL_INTERVAL)
}
function stopPolling() {
  if (timer) { clearInterval(timer); timer = null }
  closeBuildLogStream() // 轮询停时一并关实时流（run 终态/组件卸载）
}

watch(() => props.runId, async () => {
  closeBuildLogStream()
  selectedIdx.value = null
  logCache.value = {}
  await load()
  if (!isTerminal.value) startPolling()
})

onMounted(async () => {
  await load()
  if (!isTerminal.value) startPolling()
})
onUnmounted(stopPolling)

// ---- 选中阶段详情面板（点轨道节点切换，同一时间一个；KISS） ----
const selectedIdx = ref<number | null>(null)
const logLoading = ref(false)
const logCache = ref<Record<number, string>>({})
const logBoxRef = ref<HTMLDivElement | null>(null)
// build 实时日志 EventSource（构建中 follow Pod logs；切走/卸载时 close 防泄漏）
let buildLogES: EventSource | null = null

function closeBuildLogStream() {
  if (buildLogES) {
    buildLogES.close()
    buildLogES = null
  }
}

// 选中态默认跟随当前执行 stage（run 加载后自动定位到「正在发生」的节点）
function autoSelect() {
  if (selectedIdx.value !== null) return
  const r = run.value
  if (!r) return
  // 优先失败/运行中节点，否则最后一个已完成
  const fail = r.stageRuns.find((s) => s.status === 'failed')
  if (fail) { selectedIdx.value = fail.index; selectStage(fail); return }
  if (!isTerminal.value && r.currentStage < r.stageRuns.length) {
    selectedIdx.value = r.currentStage
    selectStage(r.stageRuns[r.currentStage])
  }
}

async function toggleNode(s: StageRun) {
  if (selectedIdx.value === s.index) { selectedIdx.value = null; return }
  selectedIdx.value = s.index
  await selectStage(s)
}

// selectStage 拉日志：build stage 有 buildRunId 时（运行中开 SSE 实时流；终态拉全量）。
async function selectStage(s: StageRun) {
  closeBuildLogStream()
  const bid = buildRunIdOf(s)
  if (s.type === 'build' && bid && logCache.value[s.index] === undefined) {
    logLoading.value = true
    try {
      const br = await getBuildRun(bid)
      const terminal = br.status === 'success' || br.status === 'failed'
      if (terminal) {
        logCache.value[s.index] = br.log || ''
        logLoading.value = false
        await scrollLogToBottom()
      } else {
        // 运行中：开 SSE 实时流逐行追加
        logCache.value[s.index] = ''
        logLoading.value = false
        startBuildLogStream(bid, s.index)
      }
    } catch {
      logCache.value[s.index] = '' // 失败 fallback stage.log
      logLoading.value = false
    }
  } else {
    await scrollLogToBottom()
  }
}

// startBuildLogStream 开 EventSource 拉 /api/buildruns/{id}/logs/stream（SSE follow）。
// onmessage 逐行 append + 自动滚底；event:end 时关流 + 拉终态全量（Pod 完成后 Log 落库）。
function startBuildLogStream(bid: string, stageIdx: number) {
  closeBuildLogStream()
  const es = new EventSource(`/api/buildruns/${bid}/logs/stream`)
  buildLogES = es
  es.onmessage = (ev) => {
    const cur = logCache.value[stageIdx] ?? ''
    logCache.value[stageIdx] = cur + ev.data + '\n'
    scrollLogToBottom()
  }
  es.addEventListener('end', () => {
    closeBuildLogStream()
    // 流结束：拉终态全量日志（覆盖实时片段，保证完整）
    getBuildRun(bid).then((br) => {
      logCache.value[stageIdx] = br.log || logCache.value[stageIdx] || ''
      scrollLogToBottom()
    }).catch(() => { /* 保留已收到的实时片段 */ })
  })
  es.addEventListener('error', () => {
    // EventSource error：统一关流 + 降级提示（保留已收到的实时片段）。
    // 不判 readyState：无论 CLOSED（503 终态）还是 CONNECTING（抖动重连中）都应降级，
    // 因为 closeBuildLogStream 会 es.close() 停止自动重连（避免构建已完成还反复重连）。
    closeBuildLogStream()
    if (!logCache.value[stageIdx]) {
      logCache.value[stageIdx] = '（实时日志不可用，显示已有片段；构建完成后可刷新拉全量）'
    }
  })
}

async function scrollLogToBottom() {
  await nextTick()
  const el = logBoxRef.value
  if (el) el.scrollTop = el.scrollHeight
}

// stage 当前应展示的日志内容。
function logTextOf(s: StageRun): string {
  if (s.type === 'build' && buildRunIdOf(s) && logCache.value[s.index] !== undefined) {
    return logCache.value[s.index] || '（暂无日志）'
  }
  return s.log || '（暂无日志）'
}

const selectedStage = computed<StageRun | null>(() => {
  const r = run.value
  if (!r || selectedIdx.value === null) return null
  return r.stageRuns.find((s) => s.index === selectedIdx.value) || null
})

// stage 输出已知 key 的中文标签
const OUTPUT_LABELS: Record<string, string> = {
  imageId: '镜像',
  releaseId: '发布单',
  workloadDomain: '访问地址',
  version: '版本',
  mergeSha: '合并 SHA',
}

function outputEntries(s: StageRun): Array<[key: string, string]> {
  if (!s.output) return []
  return Object.entries(s.output).map(([k, v]) => [OUTPUT_LABELS[k] || k, String(v ?? '')])
}

function stageDuration(s: StageRun): string {
  if (!s.startedAt) return ''
  const end = s.finishedAt ? new Date(s.finishedAt).getTime() : Date.now()
  const ms = end - new Date(s.startedAt).getTime()
  if (ms < 0) return ''
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}min`
}

function shortCommit(c?: string): string {
  return c ? c.slice(0, 8) : '-'
}

// 连线状态：指向节点 i 的线段，i-1 与 i 都达终态（succeeded/failed/aborted/skipped）则实线着色
const FLOW_DONE = ['succeeded', 'failed', 'aborted', 'skipped']
function connectorDone(i: number): boolean {
  const r = run.value
  if (!r || i === 0) return false
  const prev = r.stageRuns[i - 1]
  return FLOW_DONE.includes(prev?.status || '')
}
function connectorColor(i: number): string {
  const r = run.value
  if (!r) return 'var(--el-border-color-lighter)'
  const prev = r.stageRuns[i - 1]
  // 上一节点失败，连线红（流到这里断了）；否则绿
  if (prev?.status === 'failed') return 'var(--el-color-danger)'
  return 'var(--el-color-success)'
}

// run 数据变化时自动定位选中节点（首次加载 + 轮询补选）
watch(() => run.value?.id, autoSelect)
</script>

<template>
  <div class="run-view" v-loading="loading">
    <template v-if="run">
      <!-- 状态摘要条 -->
      <div class="run-summary">
        <div class="summary-left">
          <el-tag :type="runStatusType(run.status)" effect="dark">{{ runStatusLabel(run.status) }}</el-tag>
          <span class="branch">{{ run.branch }}@{{ shortCommit(run.commit) }}</span>
          <span v-if="run.version" class="version">v{{ run.version }}</span>
        </div>
        <div class="summary-right">
          <span class="time">{{ new Date(run.createdAt).toLocaleString() }}</span>
          <el-button v-if="canAbort" size="small" type="danger" plain @click="abort">中止</el-button>
          <el-button v-if="canRetry" size="small" type="warning" plain @click="retry">重试失败阶段</el-button>
          <el-button v-if="canApprove" size="small" type="primary" @click="approve">批准继续</el-button>
        </div>
      </div>

      <!-- 横向阶段轨道：节点圆 + 连线（已完成实线着色 / 未到灰虚线） -->
      <div class="stage-rail">
        <template v-for="(s, i) in run.stageRuns" :key="s.index">
          <div v-if="i > 0" class="rail-connector"
            :class="{ done: connectorDone(i) }"
            :style="connectorDone(i) ? { background: connectorColor(i) } : {}" />
          <div class="rail-node" :class="{ selected: selectedIdx === s.index, current: s.index === run.currentStage && !isTerminal }"
            @click="toggleNode(s)">
            <div class="node-circle" :style="{ borderColor: stageColorVar(s.status), color: stageColorVar(s.status) }"
              :class="{ pulse: s.status === 'running' }">
              {{ stageIcon(s.status) }}
            </div>
            <div class="node-name">{{ s.name }}</div>
            <div class="node-status">{{ stageStatusLabel(s) }}</div>
            <el-tag v-if="laneOf(s) && laneOf(s) !== 'default'" size="small" type="warning" class="node-lane">
              {{ laneOf(s) }}
            </el-tag>
          </div>
        </template>
      </div>

      <!-- 选中阶段详情面板 -->
      <div v-if="selectedStage" class="stage-panel">
        <div class="panel-head">
          <span class="panel-icon" :style="{ color: stageColorVar(selectedStage.status) }">{{ stageIcon(selectedStage.status) }}</span>
          <span class="panel-name">{{ selectedStage.name }}</span>
          <span class="panel-duration">{{ stageDuration(selectedStage) }}</span>
        </div>
        <div v-if="selectedStage.error" class="stage-error">⚠ {{ selectedStage.error }}</div>
        <div v-if="outputEntries(selectedStage).length" class="stage-output">
          <div v-for="[k, v] in outputEntries(selectedStage)" :key="k" class="out-item">
            <span class="out-key">{{ k }}：</span><span class="out-val">{{ v }}</span>
          </div>
        </div>
        <div class="stage-log" v-loading="logLoading && selectedStage.type === 'build'">
          <div ref="logBoxRef" class="log-block">{{ logTextOf(selectedStage) }}</div>
        </div>
      </div>
    </template>
    <el-empty v-else-if="!loading" description="运行不存在或已删除" />
  </div>
</template>

<style scoped>
.run-view { padding: 16px 20px; }
.run-summary {
  display: flex; justify-content: space-between; align-items: center;
  padding: 12px 16px; margin-bottom: 20px;
  background: var(--el-fill-color-light); border-radius: 6px;
}
.summary-left { display: flex; align-items: center; gap: 12px; }
.summary-left .branch { font-family: monospace; font-size: 13px; color: var(--el-text-color-regular); }
.summary-left .version { font-weight: 600; color: var(--el-color-success); }
.summary-right { display: flex; align-items: center; gap: 12px; }
.summary-right .time { font-size: 12px; color: var(--el-text-color-secondary); }

/* ---- 横向阶段轨道 ---- */
.stage-rail {
  display: flex; align-items: flex-start;
  padding: 8px 4px 16px; margin-bottom: 16px;
  overflow-x: auto; /* 阶段多时横向滚动 */
}
.rail-node {
  display: flex; flex-direction: column; align-items: center;
  min-width: 96px; cursor: pointer; user-select: none;
  padding: 6px 8px; border-radius: 6px;
  border: 1px solid transparent;
}
.rail-node:hover { background: var(--el-fill-color-light); }
.rail-node.selected { border-color: var(--el-color-primary-light-5); background: var(--el-color-primary-light-9); }
.node-circle {
  width: 34px; height: 34px; border-radius: 50%;
  border: 2px solid; display: flex; align-items: center; justify-content: center;
  font-size: 15px; font-weight: 600; background: var(--el-bg-color);
  margin-bottom: 6px;
}
.node-circle.pulse { animation: node-pulse 1.4s ease-in-out infinite; }
@keyframes node-pulse {
  0%, 100% { box-shadow: 0 0 0 0 var(--el-color-primary-light-7); }
  50% { box-shadow: 0 0 0 7px var(--el-color-primary-light-9); }
}
.node-name { font-size: 13px; font-weight: 500; color: var(--el-text-color-primary); text-align: center; }
.node-status { font-size: 11.5px; color: var(--el-text-color-secondary); margin-top: 2px; min-height: 15px; }
.node-lane { margin-top: 4px; transform: scale(0.85); }
.rail-connector {
  flex: 1 1 0; min-width: 28px; height: 2px; margin-top: 23px; /* 6+34/2 对齐圆心 */
  background: var(--el-border-color-lighter);
  border-bottom: none;
}
.rail-connector:not(.done) {
  background: repeating-linear-gradient(90deg, var(--el-border-color-lighter) 0 6px, transparent 6px 12px);
}

/* ---- 选中阶段详情面板 ---- */
.stage-panel {
  border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  background: var(--el-bg-color); padding: 12px 16px;
}
.panel-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.panel-icon { font-weight: 600; }
.panel-name { font-weight: 600; font-size: 14px; }
.panel-duration { font-size: 12px; color: var(--el-text-color-secondary); margin-left: 4px; }
.stage-error { margin-bottom: 8px; padding: 6px 8px; font-size: 12px; color: var(--el-color-danger); background: var(--el-color-danger-light-9); border-radius: 4px; word-break: break-all; }
.stage-output { margin-bottom: 8px; padding: 8px; background: var(--el-fill-color-lighter); border-radius: 4px; }
.out-item { font-size: 12px; line-height: 1.8; }
.out-key { color: var(--el-text-color-secondary); }
.out-val { font-family: monospace; color: var(--el-text-color-primary); word-break: break-all; }
.log-block {
  max-height: 320px; overflow-y: auto;
  padding: 8px 10px; font-family: monospace; font-size: 12px; line-height: 1.6;
  white-space: pre-wrap; word-break: break-all;
  background: var(--el-fill-color-darker); color: var(--el-text-color-primary);
  border-radius: 4px;
}
</style>
