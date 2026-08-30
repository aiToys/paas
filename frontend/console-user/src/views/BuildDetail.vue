<template>
  <div class="page detail-page">
    <header class="crumb">
      <button class="back" @click="goBack">←</button>
      <span>构建</span>
      <span class="sep">/</span>
      <span class="mono">{{ build?.id }}</span>
      <el-tag v-if="build" :type="stType(build.status)" size="small">{{ build.status }}</el-tag>
    </header>

    <div v-if="build" class="body">
      <section class="card">
        <div class="grid">
          <div class="kv"><span>应用</span><a class="link" @click="router.push(`/applications/${build.appId}`)">{{ build.appId }}</a></div>
          <div class="kv"><span>分支</span><code class="mono">{{ build.branch }}</code></div>
          <div class="kv"><span>提交</span><code class="mono">{{ build.commit?.slice(0, 8) }}</code></div>
          <div class="kv"><span>信息</span><span>{{ build.message }}</span></div>
          <div class="kv"><span>开始</span><span>{{ build.startedAt }}</span></div>
          <div class="kv"><span>结束</span><span>{{ build.finishedAt || '—' }}</span></div>
          <div class="kv">
<span>仓库</span>
            <a class="link mono" @click="router.push(repoLink(build.appId, build.repoId))">浏览代码 →</a>
          </div>
          <div v-if="build.imageId" class="kv">
<span>产出镜像</span>
            <a class="link mono" @click="router.push(imageLink(build.appId, build.imageId))">{{ build.imageId }}</a>
          </div>
        </div>
      </section>
      <section class="card">
        <h3>构建日志</h3>
        <pre class="log mono">{{ build.log || '（无日志）' }}</pre>
      </section>
    </div>
    <el-skeleton v-else :rows="5" animated />
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { imageLink, repoLink } from '@/composables/useDevopsLinks'

interface BuildRunFull {
  id: string; appId: string; repoId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string; log?: string
}

const route = useRoute()
const router = useRouter()
const build = ref<BuildRunFull>()

function goBack() {
  if (history.length > 1) history.back()
  else router.push('/devops')
}

const stType = (s?: string) => ({ success: 'success', failed: 'danger', running: 'warning' } as Record<string, string>)[s ?? ''] ?? 'info'

async function load() {
  const resp = await fetchAuth(`/api/buildruns/${route.params.id}`)
  if (!resp.ok) {
    ElMessage.error('构建不存在')
    return
  }
  const j = await resp.json()
  build.value = j?.data ?? j
}
onMounted(load)
// 同路由不同 id 复用组件（详情互链点另一构建）时重载
watch(() => route.params.id, () => load())
</script>

<style scoped>
.detail-page { padding: 20px; max-width: 960px; margin: 0 auto; }
.crumb { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.crumb .sep { color: var(--el-text-color-placeholder); }
.back { border: none; background: none; cursor: pointer; font-size: 16px; color: var(--el-text-color-primary); }
.body { display: grid; gap: 14px; }
.card { background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 16px 20px; }
.card h3 { margin: 0 0 12px; font-size: 14px; color: var(--el-text-color-secondary); }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 4px 24px; }
.kv { display: flex; align-items: center; gap: 8px; margin: 4px 0; font-size: 13px; }
.kv > span:first-child { color: var(--el-text-color-secondary); min-width: 64px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.link { color: var(--el-color-primary); cursor: pointer; }
.log { background: var(--el-fill-color-darker); color: var(--el-text-color-primary); padding: 12px; border-radius: 6px; max-height: 480px; overflow: auto; white-space: pre-wrap; word-break: break-all; margin: 0; }
</style>
