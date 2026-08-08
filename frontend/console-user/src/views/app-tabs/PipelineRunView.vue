<script setup lang="ts">
// 流水线运行视图：拉取 PipelineRun 详情 + 5s 轮询 + approve/abort。
// 轮询仅运行中/暂停时持续；终态自动停止。断连静默重试下次。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getRun, approveStage, abortRun, type PipelineRun, type StageRun } from '@/api/pipeline'

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
}

watch(() => props.runId, async () => {
  await load()
  if (!isTerminal.value) startPolling()
})

onMounted(async () => {
  await load()
  if (!isTerminal.value) startPolling()
})
onUnmounted(stopPolling)

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
          <el-button v-if="canApprove" size="small" type="primary" @click="approve">批准继续</el-button>
        </div>
      </div>

      <!-- stage 时间线 -->
      <el-timeline class="run-timeline">
        <el-timeline-item v-for="s in run.stageRuns" :key="s.index"
          :type="stageStatusType(s.status)" :hollow="s.status === 'pending' || s.status === 'skipped'"
          :timestamp="stageDuration(s)" placement="top">
          <div class="stage-card" :class="{ current: s.index === run.currentStage && !isTerminal }">
            <div class="stage-head">
              <span class="stage-icon">{{ stageIcon(s.status) }}</span>
              <span class="stage-name">{{ s.name }}</span>
              <el-tag size="small" :type="stageStatusType(s.status)">{{ s.status }}</el-tag>
            </div>
            <div v-if="s.error" class="stage-error">⚠ {{ s.error }}</div>
            <div v-if="outputEntries(s).length" class="stage-output">
              <div v-for="[k, v] in outputEntries(s)" :key="k" class="out-item">
                <span class="out-key">{{ k }}：</span><span class="out-val">{{ v }}</span>
              </div>
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
.stage-head { display: flex; align-items: center; gap: 8px; }
.stage-icon { font-weight: 600; width: 16px; text-align: center; }
.stage-name { font-weight: 500; flex: 1; }
.stage-error { margin-top: 6px; padding: 6px 8px; font-size: 12px; color: var(--el-color-danger); background: var(--el-color-danger-light-9); border-radius: 4px; word-break: break-all; }
.stage-output { margin-top: 8px; padding: 8px; background: var(--el-fill-color-lighter); border-radius: 4px; }
.out-item { font-size: 12px; line-height: 1.8; }
.out-key { color: var(--el-text-color-secondary); }
.out-val { font-family: monospace; color: var(--el-text-color-primary); word-break: break-all; }
</style>
