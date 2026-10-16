<script setup lang="ts">
// 流水线运行详情（v2 简版）：状态/阶段进度/阶段事件日志 + 高频三操作
// （通过审批/终止/重试）在 v2 内闭环——待办横幅与交付面的 run 深链落这里，
// 不再跳老版巨型详情页（SSE 全量构建日志等深度排障仍可去老版）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { buildRunIdOf, getRun, approveStage, abortRun, retryRun, type PipelineRun, type StageRun } from '@/api/pipeline'
import { statusOf, tagTypeToCls, RUN_STATUS, STAGE_STATUS } from '@/composables/useStatus'
import { apiError } from '@/api'

const route = useRoute()
const router = useRouter()

const run = ref<PipelineRun | null>(null)
const loading = ref(true)
const notFound = ref(false)
const acting = ref('')
// 展开日志的阶段 index（单开；点击行切换）。
const expandedIdx = ref<number | null>(null)

let timer: number | undefined
const ACTIVE = new Set(['pending', 'running', 'paused'])

async function load() {
  try {
    run.value = await getRun(route.params.runId as string)
    if (expandedIdx.value === null) expandedIdx.value = run.value.stageRuns.find((s) => s.status === 'failed' || s.status === 'waiting')?.index ?? null
    document.title = `运行 ${run.value.id.slice(0, 12)} · 控制台`
  } catch (e) {
    const msg = apiError(e)
    if (msg.includes('not found') || msg.includes('404')) notFound.value = true
    else ElMessage.error(apiError(e, '加载运行失败'))
  } finally {
    loading.value = false
  }
}
function schedule() {
  if (timer) { window.clearInterval(timer); timer = undefined }
  // 进行中 5s 轮询；终态停（页面隐藏跳过当次）。
  if (run.value && ACTIVE.has(run.value.status)) {
    timer = window.setInterval(() => { if (!document.hidden) void load() }, 5000)
  }
}
onMounted(async () => { await load(); schedule() })
onUnmounted(() => { if (timer) window.clearInterval(timer) })

const runMeta = computed(() => statusOf(RUN_STATUS, run.value?.status ?? ''))
function stageMeta(s: StageRun) {
  return statusOf(STAGE_STATUS, s.status)
}
function fmtDuration(s: StageRun): string {
  if (!s.startedAt) return '—'
  const end = s.finishedAt ? new Date(s.finishedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - new Date(s.startedAt).getTime()) / 1000))
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

// —— 高频三操作（v2 内闭环的关键：待办「等待审批」直达此页即可处理）——
async function doApprove() {
  if (!run.value) return
  acting.value = 'approve'
  try {
    await approveStage(run.value.id, run.value.currentStage)
    ElMessage.success('已通过审批')
    await load(); schedule()
  } catch (e) {
    ElMessage.error(apiError(e, '审批失败'))
  } finally {
    acting.value = ''
  }
}
async function doAbort() {
  if (!run.value) return
  acting.value = 'abort'
  try {
    await abortRun(run.value.id)
    ElMessage.success('已终止运行')
    await load(); schedule()
  } catch (e) {
    ElMessage.error(apiError(e, '终止失败'))
  } finally {
    acting.value = ''
  }
}
async function doRetry() {
  if (!run.value) return
  acting.value = 'retry'
  try {
    await retryRun(run.value.id)
    ElMessage.success('已重试，从失败阶段续跑')
    expandedIdx.value = null
    await load(); schedule()
  } catch (e) {
    ElMessage.error(apiError(e, '重试失败'))
  } finally {
    acting.value = ''
  }
}
</script>

<template>
  <div class="run-page">
    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载运行…"></div>

    <div v-else-if="notFound" class="empty">
      <p>运行不存在或已清理</p>
      <a class="ghost-btn" @click="router.push('/')">← 返回首页</a>
    </div>

    <template v-else-if="run">
      <div class="crumb">
        <a v-if="run.appId" @click="router.push(`/products/${run.appId}?tab=delivery`)">← 产品交付面</a>
        <a v-else @click="router.push('/')">← 首页</a>
        <span>/</span><span class="mono run-id">{{ run.id }}</span>
      </div>

      <div class="head-card">
        <div class="head-main">
          <span class="r-status" :class="tagTypeToCls(runMeta.type)">{{ runMeta.label }}</span>
          <span class="h-kv">分支 <b class="mono">{{ run.branch }}</b></span>
          <span class="h-kv">触发 <b>{{ run.trigger }}</b></span>
          <span v-if="run.version" class="h-kv">版本 <b class="mono">{{ run.version }}</b></span>
        </div>
        <div class="head-acts">
          <button v-if="run.status === 'paused'" class="act primary" :disabled="acting === 'approve'" @click="doApprove">
            {{ acting === 'approve' ? '审批中…' : '通过审批' }}
          </button>
          <button v-if="ACTIVE.has(run.status)" class="act danger" :disabled="acting === 'abort'" @click="doAbort">
            {{ acting === 'abort' ? '终止中…' : '终止' }}
          </button>
          <button v-if="run.status === 'failed' || run.status === 'aborted'" class="act" :disabled="acting === 'retry'" @click="doRetry">
            {{ acting === 'retry' ? '重试中…' : '重试' }}
          </button>
        </div>
      </div>

      <div class="table-scroll" data-table-scroll tabindex="0">
<table class="stage-table">
        <thead>
          <tr>
            <th class="col-idx">#</th>
            <th>阶段</th>
            <th>状态</th>
            <th>耗时</th>
            <th>说明</th>
            <th class="col-log"></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="s in run.stageRuns" :key="s.index">
            <tr class="clickable" @click="expandedIdx = expandedIdx === s.index ? null : s.index">
              <td class="col-idx mono">{{ s.index + 1 }}</td>
              <td><span class="s-name">{{ s.name }}</span><span class="s-type">{{ s.type }}</span></td>
              <td><span class="status" :class="tagTypeToCls(stageMeta(s).type)">{{ stageMeta(s).label }}</span></td>
              <td class="mono">{{ fmtDuration(s) }}</td>
              <td class="s-note">
                <span v-if="s.error" class="err-text">{{ s.error }}</span>
                <span v-else-if="s.output?.branch" class="mono dim">→ {{ s.output.branch }}</span>
                <span v-else-if="s.status === 'waiting'">等待人工决策</span>
              </td>
              <td class="col-log"><span class="log-toggle">{{ expandedIdx === s.index ? '收起' : (s.log ? '日志' : '') }}</span></td>
            </tr>
            <tr v-if="expandedIdx === s.index" class="log-row">
              <td colspan="6">
                <RouterLink v-if="buildRunIdOf(s)" :to="'/builds/' + buildRunIdOf(s)" class="text-button">打开完整构建日志 →</RouterLink>
                <pre v-if="s.log" class="log-pre mono">{{ s.log }}</pre>
                <p v-else class="log-empty">该阶段暂无事件日志。若为构建阶段，可打开完整构建日志。</p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
</div>
    </template>
  </div>
</template>

<style scoped>
.run-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.loading-block { height: 240px; }
.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--text-faint);
}
.crumb a {
  color: var(--brand);
  cursor: pointer;
}
.run-id { font-size: 12px; }
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 64px 0;
  color: var(--text-faint);
}
.ghost-btn {
  padding: 7px 14px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}
.head-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.head-main {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  min-width: 0;
}
.h-kv {
  font-size: 12.5px;
  color: var(--text-faint);
}
.h-kv b {
  color: var(--text);
  font-weight: 500;
}
.head-acts {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
.stage-table th {
  text-align: left;
  padding: 8px 10px;
  color: var(--text-faint);
  font-weight: 500;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
  background: var(--surface-2);
}
.stage-table td {
  padding: 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.stage-table tr.clickable { cursor: pointer; }
.stage-table tr.clickable:hover td { background: var(--surface-2); }
.col-idx { width: 36px; color: var(--text-faint); }
.s-name { font-weight: 600; margin-right: 8px; }
.s-type {
  padding: 1px 6px;
  border: 1px solid var(--border);
  border-radius: 3px;
  font-size: 10.5px;
  color: var(--text-faint);
}
.err-text {
  color: var(--danger, #f43f5e);
  font-size: 12px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.dim { color: var(--text-dim); font-size: 12px; }
.col-log { text-align: right; }
.log-toggle {
  font-size: 11.5px;
  color: var(--brand);
}
.log-row td {
  background: var(--surface-2);
  padding: 0;
}
.log-pre {
  max-height: 320px;
  overflow: auto;
  margin: 0;
  padding: 12px;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--text);
}
.log-empty {
  margin: 0;
  padding: 12px;
  font-size: 12px;
  color: var(--text-faint);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
