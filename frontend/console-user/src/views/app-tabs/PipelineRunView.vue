<script setup lang="ts">
// 流水线运行视图：拉取 PipelineRun 详情 + 5s 轮询 + approve/abort + stage 展开日志区。
// 轮询仅运行中/暂停时持续；终态自动停止。断连静默重试下次。
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
const stageStatusType = (s: string): string => {
  const m: Record<string, string> = { succeeded: 'success', failed: 'danger', running: 'warning', waiting: 'warning', pending: 'info', skipped: 'info' }
  return m[s] || 'info'
}
const stageIcon = (s: string): string => {
  const m: Record<string, string> = { succeeded: '✓', failed: '✗', running: '◌', pending: '·', waiting: '⏸', skipped: '–' }
  return m[s] || '·'
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
  expandedIdx.value = null
  logCache.value = {}
  await load()
  if (!isTerminal.value) startPolling()
})

onMounted(async () => {
  await load()
  if (!isTerminal.value) startPolling()
})
onUnmounted(stopPolling)

// stage 展开日志区：同一时间只展开一个 stage（KISS）。
// expandedIdx=当前展开的 stage.index；logCache 缓存已拉取的日志（key=stage.index）。
// build stage 展开时：终态拉 BuildRun.Log 全量；运行中开 EventSource 实时流（SSE follow Pod logs）。
const expandedIdx = ref<number | null>(null)
const logLoading = ref(false)
const logCache = ref<Record<number, string>>({})
const logBoxRef = ref<HTMLDivElement | null>(null)
// build 实时日志 EventSource（构建中 follow Pod logs；折叠/切走/卸载时 close 防泄漏）
let buildLogES: EventSource | null = null

function closeBuildLogStream() {
  if (buildLogES) {
    buildLogES.close()
    buildLogES = null
  }
}

async function toggleExpand(s: StageRun) {
  if (expandedIdx.value === s.index) {
    expandedIdx.value = null
    closeBuildLogStream()
    return
  }
  // 切到新 stage，先关旧流
  closeBuildLogStream()
  expandedIdx.value = s.index
  // build stage：有 buildRunId 时拉日志（运行中开 EventSource 实时流；终态拉全量）
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
  es.addEventListener('error', (ev) => {
    // EventSource error：降级提示（保留已收到的片段）；不重连（避免构建已完成还反复重连）
    if (es.readyState === EventSource.CLOSED) return
    // 非集群部署/超时返 error 事件 -> 关流 + 提示
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

// run 切换时清缓存（已在上方 watch 内处理：closeBuildLogStream + 清 expandedIdx/logCache）

// stage 输出已知 key 的中文标签
const OUTPUT_LABELS: Record<string, string> = {
  imageId: '镜像',
  releaseId: '发布单',
  workloadDomain: '访问地址',
  version: '版本',
  mergeSha: '合并 SHA',
}

function outputEntries(s: StageRun): Array<[string, string]> {
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

      <!-- stage 时间线 -->
      <el-timeline class="run-timeline">
        <el-timeline-item v-for="s in run.stageRuns" :key="s.index"
          :type="stageStatusType(s.status)" :hollow="s.status === 'pending' || s.status === 'skipped'"
          :timestamp="stageDuration(s)" placement="top">
          <div class="stage-card" :class="{ current: s.index === run.currentStage && !isTerminal }">
            <div class="stage-head" @click="toggleExpand(s)">
              <span class="stage-icon">{{ stageIcon(s.status) }}</span>
              <span class="expand-icon">{{ expandedIdx === s.index ? '▾' : '▸' }}</span>
              <span class="stage-name">{{ s.name }}</span>
              <el-tag v-if="laneOf(s)" size="small" :type="laneOf(s) === 'default' ? 'info' : 'warning'">
                lane: {{ laneOf(s) }}
              </el-tag>
              <el-tag size="small" :type="stageStatusType(s.status)">{{ s.status }}</el-tag>
            </div>
            <div v-if="s.error" class="stage-error">⚠ {{ s.error }}</div>
            <div v-if="outputEntries(s).length" class="stage-output">
              <div v-for="[k, v] in outputEntries(s)" :key="k" class="out-item">
                <span class="out-key">{{ k }}：</span><span class="out-val">{{ v }}</span>
              </div>
            </div>
            <!-- 展开日志区：build stage 拉 BuildRun 全量日志；其它 stage 用 stage.log -->
            <div v-if="expandedIdx === s.index" class="stage-log" v-loading="logLoading && s.type === 'build'">
              <div ref="logBoxRef" class="log-block">{{ logTextOf(s) }}</div>
            </div>
          </div>
        </el-timeline-item>
      </el-timeline>
    </template>
    <el-empty v-else-if="!loading" description="运行不存在或已删除" />
  </div>
</template>

<style scoped>
.run-view { padding: 16px 20px; }
.run-summary {
  display: flex; justify-content: space-between; align-items: center;
  padding: 12px 16px; margin-bottom: 16px;
  background: var(--el-fill-color-light); border-radius: 6px;
}
.summary-left { display: flex; align-items: center; gap: 12px; }
.summary-left .branch { font-family: monospace; font-size: 13px; color: var(--el-text-color-regular); }
.summary-left .version { font-weight: 600; color: var(--el-color-success); }
.summary-right { display: flex; align-items: center; gap: 12px; }
.summary-right .time { font-size: 12px; color: var(--el-text-color-secondary); }

.run-timeline { padding-left: 4px; }
.stage-card {
  padding: 8px 12px; border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  background: var(--el-bg-color);
}
.stage-card.current { border-color: var(--el-color-primary); box-shadow: 0 0 0 2px var(--el-color-primary-light-8); }
.stage-head { display: flex; align-items: center; gap: 8px; cursor: pointer; user-select: none; }
.stage-icon { font-weight: 600; width: 16px; text-align: center; }
.expand-icon { width: 12px; color: var(--el-text-color-secondary); font-size: 12px; }
.stage-name { font-weight: 500; flex: 1; }
.stage-error { margin-top: 6px; padding: 6px 8px; font-size: 12px; color: var(--el-color-danger); background: var(--el-color-danger-light-9); border-radius: 4px; word-break: break-all; }
.stage-output { margin-top: 8px; padding: 8px; background: var(--el-fill-color-lighter); border-radius: 4px; }
.out-item { font-size: 12px; line-height: 1.8; }
.out-key { color: var(--el-text-color-secondary); }
.out-val { font-family: monospace; color: var(--el-text-color-primary); word-break: break-all; }
.stage-log { margin-top: 8px; }
.log-block {
  max-height: 320px; overflow-y: auto;
  padding: 8px 10px; font-family: monospace; font-size: 12px; line-height: 1.6;
  white-space: pre-wrap; word-break: break-all;
  background: var(--el-fill-color-darker); color: var(--el-text-color-primary);
  border-radius: 4px;
}
</style>
