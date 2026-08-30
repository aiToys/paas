<script setup lang="ts">
// 流水线运行详情独立页（三层结构）：
//   ① 流水线 tab（该应用的 CI/CD 流水线切换）
//   ② 运行列表（选中流水线的历史运行，点选切换）
//   ③ 选中运行的横向阶段轨道（PipelineRunView 渲染）
// 独立路由 /devops/runs/:runId —— DevOps 中心运行记录、应用详情触发、外部深链接统一入口。
// 页面壳只负责返回导航 + 应用归属 + tab/列表编排，运行渲染全交 PipelineRunView（单一真源，DRY）。
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import PipelineRunView from './app-tabs/PipelineRunView.vue'
import { getRun, listRuns, listPipelines, type Pipeline, type PipelineRun } from '@/api/pipeline'
import { fetchAuth, apiError } from '@/api'

const route = useRoute()
const router = useRouter()
// runId 跟随路由参数（单一真源）：行点击/通知跳转走 router.replace，
// 同路由复用组件时 watch params 切换——本地 ref 方案两个方向都有 bug
// （点行不更新 URL 刷新即错位；push 新 runId 页面不响应）。
const runId = computed(() => (route.params.runId as string) || '')
const appId = ref('')
const appName = ref('')

// ① 流水线 tab：run 反查 appId 后拉该应用全部流水线，选中 run 所属的为当前 tab
const pipelines = ref<Pipeline[]>([])
const activePid = ref('')

// ② 运行列表：当前 tab 流水线的历史运行（新在前），选中的渲染轨道
const runs = ref<PipelineRun[]>([])
const runsLoading = ref(false)

function shortCommit(c?: string): string { return c ? c.slice(0, 8) : '-' }
function fmtTime(t?: string): string { return t ? new Date(t).toLocaleString() : '' }

async function loadPipelines() {
  if (!appId.value) return
  try {
    pipelines.value = await listPipelines(appId.value)
  } catch { /* 无流水线时仅显示运行 */ }
}

async function loadRuns() {
  if (!activePid.value) return
  runsLoading.value = true
  try {
    runs.value = await listRuns({ appId: appId.value, pipelineId: activePid.value })
  } catch (e) {
    ElMessage.error(apiError(e, '加载运行列表失败'))
  } finally {
    runsLoading.value = false
  }
}

// 切流水线 tab：默认选该流水线最新一次运行（经路由，保持 URL 可分享）
async function switchPipeline(pid: string) {
  activePid.value = pid
  await loadRuns()
  const latest = runs.value[0]
  if (latest && latest.id !== runId.value) router.replace(`/devops/runs/${latest.id}`)
  else if (!latest) router.replace('/devops')
}

async function bootstrap() {
  try {
    const r = await getRun(runId.value)
    appId.value = r.appId || ''
    if (appId.value) {
      const resp = await fetchAuth(`/api/applications/${appId.value}`)
      if (resp.ok) {
        const j = await resp.json()
        appName.value = j?.data?.name ?? appId.value
      } else {
        appName.value = appId.value
      }
      await loadPipelines()
      activePid.value = r.pipelineId
      await loadRuns()
    }
  } catch {
    /* run 不存在时 PipelineRunView 自显 empty，无需处理 */
  }
}

onMounted(bootstrap)

// runId 变化（路由参数：点运行列表行/通知跳转/深链）时同步 tab 归属（跨 tab 场景）
watch(runId, async (id, old) => {
  if (!id || id === old) return
  const r = runs.value.find((x) => x.id === id)
  if (r && r.pipelineId !== activePid.value) {
    activePid.value = r.pipelineId
    await loadRuns()
  }
  // 新 run 可能属另一应用（深链直入）：appId 未初始化时补 bootstrap
  if (!appId.value) await bootstrap()
})

function goBack() {
  // 返回应用详情（应用为主线，运行归属应用）；无应用归属兜底 DevOps 中心
  if (appId.value) router.push(`/applications/${appId.value}`)
  else router.push('/devops')
}
</script>

<template>
  <div class="run-page">
    <div class="page-bar">
      <button class="back" @click="goBack">← 返回</button>
      <span class="title">流水线运行</span>
      <span v-if="appName" class="app">应用：<b>{{ appName }}</b></span>
    </div>

    <!-- ① 流水线 tab（CI/CD） -->
    <el-tabs v-if="pipelines.length" v-model="activePid" class="pipe-tabs" @tab-change="switchPipeline">
      <el-tab-pane v-for="p in pipelines" :key="p.id" :name="p.id">
        <template #label>
          <span class="tab-label">
            <span class="tab-kind" :class="p.kind">{{ p.kind.toUpperCase() }}</span>{{ p.name }}
          </span>
        </template>
      </el-tab-pane>
    </el-tabs>

    <!-- ② 运行列表（横向 chips，选中高亮）+ ③ 运行轨道 -->
    <div v-if="activePid" class="run-chips" v-loading="runsLoading">
      <button
v-for="r in runs" :key="r.id" class="run-chip" :class="{ active: r.id === runId }"
        @click="router.replace(`/devops/runs/${r.id}`)"
>
        <span class="chip-dot" :class="r.status" />
        <span class="chip-text">{{ r.branch }}@{{ shortCommit(r.commit) }}</span>
        <span v-if="r.version" class="chip-ver">{{ r.version }}</span>
        <span class="chip-time">{{ fmtTime(r.createdAt) }}</span>
      </button>
      <span v-if="!runsLoading && !runs.length" class="chip-empty">暂无运行记录</span>
    </div>

    <PipelineRunView v-if="runId" :run-id="runId" />
    <el-empty v-else description="该流水线暂无运行" />
  </div>
</template>

<style scoped>
.run-page { max-width: 1100px; margin: 0 auto; }
.page-bar {
  display: flex; align-items: center; gap: 14px;
  padding: 4px 0 14px; margin-bottom: 4px;
  border-bottom: 1px solid var(--border, #eee);
}
.back {
  border: none; background: transparent; color: var(--text-faint, #999);
  font-family: inherit; font-size: 13px; cursor: pointer; padding: 4px 8px;
}
.back:hover { color: var(--text, #333); }
.title { font-size: 16px; font-weight: 600; }
.app { font-size: 12.5px; color: var(--text-dim, #888); }
.app b { color: var(--text, #333); }

.pipe-tabs { margin-bottom: 4px; }
.tab-label { display: inline-flex; align-items: center; gap: 6px; }
.tab-kind {
  font-size: 10px; font-weight: 700; padding: 1px 5px; border-radius: 3px; letter-spacing: 0.5px;
}
.tab-kind.ci { background: var(--el-color-primary-light-8); color: var(--el-color-primary); }
.tab-kind.cd { background: var(--el-color-success-light-8); color: var(--el-color-success); }

/* 运行 chips：横向一排（溢出换行），选中高亮 */
.run-chips {
  display: flex; flex-wrap: wrap; gap: 8px;
  padding: 10px 0 6px; margin-bottom: 6px;
}
.run-chip {
  display: inline-flex; align-items: center; gap: 8px;
  border: 1px solid var(--el-border-color-lighter); border-radius: 16px;
  background: var(--el-bg-color); cursor: pointer;
  padding: 4px 12px; font-family: inherit; font-size: 12px;
  color: var(--el-text-color-regular);
}
.run-chip:hover { border-color: var(--el-color-primary-light-5); }
.run-chip.active { border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); color: var(--el-color-primary); }
.chip-dot { width: 8px; height: 8px; border-radius: 50%; }
.chip-dot.succeeded { background: var(--el-color-success); }
.chip-dot.failed { background: var(--el-color-danger); }
.chip-dot.running, .chip-dot.paused { background: var(--el-color-warning); }
.chip-dot.aborted { background: var(--el-color-info); }
.chip-text { font-family: monospace; }
.chip-ver { color: var(--el-color-success); font-weight: 600; }
.chip-time { color: var(--el-text-color-secondary); font-size: 11px; }
.chip-empty { font-size: 12px; color: var(--el-text-color-secondary); padding: 6px 2px; }
</style>
