<template>
  <div class="page detail-page">
    <header class="crumb">
      <button class="back" @click="goBack">←</button>
      <span>发布</span>
      <span class="sep">/</span>
      <span class="mono">{{ release?.id }}</span>
      <el-tag v-if="release" :type="release.status === 'succeeded' ? 'success' : release.status === 'rolled-back' ? 'info' : 'warning'" size="small">
        {{ release.status }}{{ release.isRollback ? '（回滚单）' : '' }}
      </el-tag>
    </header>

    <div v-if="release" class="body">
      <section class="card">
        <div class="grid">
          <div class="kv"><span>应用</span><a class="link" @click="router.push(`/applications/${release.appId}`)">{{ release.appId }}</a></div>
          <div class="kv"><span>环境</span><a class="link" @click="router.push(`/environments/${release.envId}`)">{{ release.envId }}</a></div>
          <div class="kv"><span>镜像</span>
            <a class="link mono" @click="router.push(imageLink(release.appId, release.imageId))">{{ release.imageId }}</a>
          </div>
          <div class="kv"><span>策略</span><span>{{ release.strategy }}</span></div>
          <div class="kv"><span>时间</span><span>{{ release.createdAt }}</span></div>
          <div class="kv"><span>操作人</span><span>{{ release.createdBy || '—' }}</span></div>
          <div v-if="release.previousImageId" class="kv"><span>回滚指针</span><code class="mono">{{ release.previousImageId }}</code></div>
          <div v-if="release.sourceRunId" class="kv"><span>来源运行</span>
            <a class="link mono" @click="router.push(`/devops/runs/${release.sourceRunId}`)">{{ release.sourceRunId }}</a>
          </div>
        </div>
        <div class="actions">
          <el-button v-if="canRollback" size="small" type="danger" plain @click="rollback">回滚到此版本之前</el-button>
        </div>
      </section>

      <!-- 当前运行态（D 阶段对账） -->
      <section class="card">
        <h3>当前运行态</h3>
        <template v-if="workload">
          <div class="grid">
            <div class="kv"><span>工作负载</span>
              <a class="link mono" @click="router.push(deployLink(release.appId))">{{ workload.id }}</a>
            </div>
            <div class="kv"><span>状态</span><el-tag size="small">{{ workload.status }}</el-tag></div>
            <div class="kv"><span>副本</span><span>{{ workload.ready ?? 0 }} / {{ workload.replicas }}</span></div>
            <div class="kv"><span>当前镜像</span><code class="mono">{{ workload.imageRef?.slice(0, 60) || '—' }}</code></div>
          </div>
          <div v-if="imageMismatch" class="mismatch">⚠️ 实际运行镜像与本发布记录不一致（可能已被后续发布/回滚覆盖）</div>
        </template>
        <div v-else class="dim">未找到关联工作负载</div>
      </section>
    </div>
    <el-skeleton v-else :rows="5" animated />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'
import { imageLink, deployLink } from '@/composables/useDevopsLinks'

interface ReleaseFull {
  id: string; appId: string; envId: string; imageId: string; imageDigest: string
  strategy: string; status: string; isRollback: boolean
  previousImageId?: string; sourceRunId?: string; createdAt: string; createdBy: string
}
interface WorkloadBrief {
  id: string; status: string; replicas: number; ready: number; imageRef?: string
  type?: string; laneId?: string
}

const route = useRoute()
const router = useRouter()
const release = ref<ReleaseFull>()
const workload = ref<WorkloadBrief>()

const canRollback = computed(() => release.value?.status === 'succeeded' && !!release.value.previousImageId)
// 对账：实际镜像 digest 与发布记录是否一致
const imageMismatch = computed(() => {
  if (!release.value?.imageDigest || !workload.value?.imageRef) return false
  return !workload.value.imageRef.includes(release.value.imageDigest)
})

function goBack() {
  if (history.length > 1) history.back()
  else router.push('/devops')
}

async function rollback() {
  if (!release.value) return
  try {
    await ElMessageBox.confirm(`回滚发布 ${release.value.id.slice(0, 12)}？工作负载将恢复上一镜像。`, '回滚确认', { type: 'warning' })
    const resp = await fetchAuth(`/api/releases/${release.value.id}/rollback`, { method: 'POST' })
    const j = await resp.json().catch(() => ({}))
    if (!resp.ok) throw new Error(j.error || '回滚失败')
    ElMessage.success('已回滚')
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

async function load() {
  // 无单条 GET 端点：跨应用列表定位
  const resp = await fetchAuth('/api/releases')
  if (!resp.ok) return
  const list = (await resp.json())?.data ?? []
  release.value = list.find((r: ReleaseFull) => r.id === route.params.id)
  if (!release.value) {
    ElMessage.error('发布记录不存在')
    return
  }
  // 对账：按 (appId, envId) 找基线 service 工作负载（列表端点是路径参数 /api/workloads/{appId}?envId=）
  try {
    const wResp = await fetchAuth(`/api/workloads/${release.value.appId}?envId=${release.value.envId}`)
    if (wResp.ok) {
      const wls = (await wResp.json())?.data ?? []
      // 基线 lane 的 service（发布编排找/建的即此）
      workload.value = wls.find((w: WorkloadBrief) => w.type === 'service' && (w.laneId ?? 'default') === 'default')
    }
  } catch { /* 非关键 */ }
}

onMounted(load)
// 同路由不同 id 复用组件（详情互链点另一单据）时重载
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
.dim { color: var(--el-text-color-placeholder); font-size: 13px; }
.link { color: var(--el-color-primary); cursor: pointer; }
.actions { margin-top: 12px; }
.mismatch { margin-top: 10px; padding: 8px 12px; background: var(--el-color-warning-light-9); border-radius: 6px; font-size: 13px; color: var(--el-color-warning); }
</style>
