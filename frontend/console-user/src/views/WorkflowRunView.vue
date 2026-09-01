<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
import { apiError } from '@/api'
// 工作流运行视图：节点时间线（状态着色 + 输出查看）+ 触发（inputs KV）+ approve/abort。
// 复用 PipelineRunView 的视觉模式；运行中 5s 轮询（终态自停）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listRuns, getRun, triggerRun, approveRun, abortRun,
  type WorkflowDef, type WorkflowRun, type NodeRun,
} from '@/api/workflow'

const props = defineProps<{
  workflow: WorkflowDef
  agents?: { id: string; name: string }[]
}>()
const emit = defineEmits<{ changed: [] }>()

const runs = ref<WorkflowRun[]>([])
const cur = ref<WorkflowRun | null>(null)
const inputsText = ref('{}')
const expanded = ref<string>('') // 展开输出的 nodeId
let timer: number | undefined

const statusTag = (s: string) =>
  ({ running: 'warning', paused: 'danger', succeeded: 'success', failed: 'danger', aborted: 'info' } as Record<string, string>)[s] ?? 'info'
const statusLabel = (s: string) =>
  ({ running: '运行中', paused: '等待确认', succeeded: '成功', failed: '失败', aborted: '已中止' } as Record<string, string>)[s] ?? s

const nodeTypeName = (id: string) => props.workflow.nodes.find(n => n.id === id)?.name || props.workflow.nodes.find(n => n.id === id)?.type || id
const nodeMeta = computed(() => new Map(props.workflow.nodes.map(n => [n.id, n])))
const waitingNode = computed(() => {
  // 最后一个 paused 节点（approve 等待中）
  if (cur.value?.status !== 'paused' || !cur.value.nodeRuns.length) return null
  const last = cur.value.nodeRuns[cur.value.nodeRuns.length - 1]
  return last.status === 'paused' ? last : null
})

async function loadRuns() {
  try {
    runs.value = await listRuns(props.workflow.id)
    if (!cur.value && runs.value.length) cur.value = runs.value[0]
  } catch (e) {
    ElMessage.error(apiError(e, '加载运行历史失败'))
  }
}

async function refreshCur() {
  if (!cur.value || refreshing) return
  refreshing = true
  const rid = cur.value.id
  try {
    const run = await getRun(rid)
    // 响应回来时用户可能已 pick 其它 run——仅同 ID 才赋值（防旧响应覆盖）
    if (cur.value?.id === rid) cur.value = run
  } catch { /* 已被清理（定义删除级联）静默 */ } finally {
    refreshing = false
  }
}
let refreshing = false

function pick(r: WorkflowRun) {
  cur.value = r
  expanded.value = ''
}

async function trigger() {
  let inputs: Record<string, string>
  try {
    const parsed = JSON.parse(inputsText.value || '{}')
    if (typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('须为对象')
    inputs = Object.fromEntries(Object.entries(parsed).map(([k, v]) => [k, String(v)]))
  } catch (e) {
    ElMessage.error(apiError(e, '输入 JSON 无效'))
    return
  }
  try {
    const run = await triggerRun(props.workflow.id, inputs)
    ElMessage.success('已触发')
    cur.value = run
    await loadRuns()
  } catch (e) {
    ElMessage.error(apiError(e))
  }
}

async function approve() {
  const w = waitingNode.value
  if (!w || !cur.value) return
  const def = nodeMeta.value.get(w.nodeId)
  try {
    if (def?.config?.message) {
      await ElMessageBox.confirm(def.config.message, '人工确认', { type: 'warning', confirmButtonText: '确认继续' })
    }
    await approveRun(cur.value.id, w.nodeId)
    ElMessage.success('已确认，继续执行')
    await refreshCur()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e))
  }
}

async function doAbort() {
  if (!cur.value) return
  try {
    await ElMessageBox.confirm('中止当前运行？不可恢复。', '中止确认', { type: 'warning' })
    await abortRun(cur.value.id)
    ElMessage.success('已中止')
    await refreshCur()
    emit('changed')
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e))
  }
}

onMounted(async () => {
  await loadRuns()
  // 运行中/等待确认 5s 轮询；终态自停
  timer = window.setInterval(() => {
    if (cur.value && (cur.value.status === 'running' || cur.value.status === 'paused')) refreshCur()
  }, 5000)
})
onUnmounted(() => window.clearInterval(timer))

const fmtTime = (t?: string) => (t ? formatDateTime(t) : '-')
function dur(nr: NodeRun) {
  if (!nr.finishedAt) return ''
  const ms = new Date(nr.finishedAt).getTime() - new Date(nr.startedAt).getTime()
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}
</script>

<template>
  <div class="runview">
    <!-- 触发区 -->
    <div class="trigger card">
      <div class="card-title">触发运行</div>
      <div class="trig-row">
        <el-input
v-model="inputsText" type="textarea" :rows="2" class="mono"
          placeholder="输入变量 JSON，如 {&quot;ticket&quot;: &quot;要求退款&quot;}（对应模板 {{inputs.ticket}}）"
/>
        <el-button type="primary" :disabled="!workflow.enabled" @click="trigger">运行</el-button>
      </div>
      <p v-if="!workflow.enabled" class="warn">工作流未启用</p>
    </div>

    <!-- 运行列表 -->
    <div class="card">
      <div class="card-title">运行历史（{{ runs.length }}）</div>
      <el-table :data="runs" size="small" highlight-current-row @row-click="pick">
        <el-table-column prop="id" label="运行" min-width="140">
          <template #default="{ row }"><span class="mono">{{ row.id.slice(0, 16) }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row.status)">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="节点进度" width="90">
          <template #default="{ row }">{{ row.nodeRuns.length }}</template>
        </el-table-column>
        <el-table-column label="开始时间" min-width="150">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 当前运行详情 -->
    <div v-if="cur" class="card">
      <div class="card-title">
        运行详情
        <el-tag size="small" :type="statusTag(cur.status)">{{ statusLabel(cur.status) }}</el-tag>
        <span class="grow" />
        <el-button v-if="waitingNode" size="small" type="primary" @click="approve">✓ 确认继续</el-button>
        <el-button v-if="cur.status === 'running' || cur.status === 'paused'" size="small" type="danger" plain @click="doAbort">中止</el-button>
      </div>
      <p v-if="waitingNode" class="wait-hint">
        ⏸ 等待人工确认：节点「{{ nodeTypeName(waitingNode.nodeId) }}」
        <span v-if="nodeMeta.get(waitingNode.nodeId)?.config?.message">——{{ nodeMeta.get(waitingNode.nodeId)?.config?.message }}</span>
      </p>

      <div class="timeline">
        <div v-for="(nr, i) in cur.nodeRuns" :key="i" class="tl-node" :class="nr.status">
          <div class="tl-head" @click="expanded = expanded === nr.nodeId ? '' : nr.nodeId">
            <span class="tl-dot" :class="nr.status" />
            <b>{{ nodeTypeName(nr.nodeId) }}</b>
            <span class="mono tl-id">{{ nr.nodeId }}</span>
            <el-tag size="small" :type="statusTag(nr.status === 'paused' ? 'paused' : nr.status)">
              {{ statusLabel(nr.status) || nr.status }}
            </el-tag>
            <span class="tl-dur">{{ dur(nr) }}</span>
            <span v-if="nr.output && expanded !== nr.nodeId" class="tl-out-hint">输出 ▾</span>
          </div>
          <div v-if="nr.error" class="tl-error">{{ nr.error }}</div>
          <pre v-if="nr.output && expanded === nr.nodeId" class="tl-output">{{ nr.output }}</pre>
        </div>
      </div>
    </div>
    <el-empty v-else description="暂无运行记录——在上方触发第一次运行" :image-size="60" />
  </div>
</template>

<style scoped>
.runview { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 14px 16px; }
.card-title { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; margin-bottom: 10px; }
.trig-row { display: flex; gap: 8px; align-items: flex-start; }
.trig-row .el-input { flex: 1; }
.warn { color: #e6a23c; font-size: 12px; margin: 6px 0 0; }
.grow { flex: 1; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.wait-hint { padding: 8px 12px; background: rgba(230, 162, 60, 0.1); border-radius: 6px; font-size: 13px; color: #e6a23c; margin: 0 0 10px; }
.timeline { display: flex; flex-direction: column; gap: 6px; }
.tl-node { border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; }
.tl-node.failed { border-color: rgba(244, 63, 94, 0.4); }
.tl-head { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.tl-dot { width: 8px; height: 8px; border-radius: 50%; }
.tl-dot.succeeded, .tl-dot.running { background: var(--el-color-success); }
.tl-dot.running { animation: blink 1.4s infinite; }
.tl-dot.failed, .tl-dot.paused { background: var(--el-color-danger); }
.tl-dot.aborted { background: var(--el-text-color-secondary); }
@keyframes blink { 50% { opacity: 0.3; } }
.tl-id { color: var(--text-faint); }
.tl-dur { color: var(--text-faint); font-size: 12px; }
.tl-out-hint { color: var(--text-faint); font-size: 12px; margin-left: auto; }
.tl-error { margin-top: 6px; padding: 6px 10px; background: rgba(244, 63, 94, 0.08); border-radius: 4px; color: #f43f5e; font-size: 12.5px; white-space: pre-wrap; }
.tl-output { margin: 8px 0 0; padding: 10px; background: var(--surface-2, var(--surface)); border-radius: 4px; font-size: 12px; white-space: pre-wrap; word-break: break-all; max-height: 240px; overflow-y: auto; }
</style>
