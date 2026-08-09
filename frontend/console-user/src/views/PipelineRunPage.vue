<script setup lang="ts">
// 流水线运行详情独立页（GitHub Actions 式）：全屏渲染 PipelineRunView（stage 时间线 + 节点日志 + build SSE 实时流）。
// 独立路由 /devops/runs/:runId —— DevOps 中心运行记录、应用详情触发、外部深链接统一入口。
// 页面壳只负责返回导航 + 应用归属，运行渲染全交 PipelineRunView（单一真源，DRY）。
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PipelineRunView from './app-tabs/PipelineRunView.vue'
import { getRun } from '@/api/pipeline'
import { fetchAuth } from '@/api'

const route = useRoute()
const router = useRouter()
const runId = route.params.runId as string
const appId = ref('')
const appName = ref('')

onMounted(async () => {
  try {
    const r = await getRun(runId)
    appId.value = r.appId || ''
    if (appId.value) {
      const resp = await fetchAuth(`/api/applications/${appId.value}`)
      if (resp.ok) {
        const j = await resp.json()
        appName.value = j?.data?.name ?? appId.value
      } else {
        appName.value = appId.value
      }
    }
  } catch {
    /* run 不存在时 PipelineRunView 自显 empty，无需处理 */
  }
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
    <PipelineRunView :run-id="runId" />
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
</style>
