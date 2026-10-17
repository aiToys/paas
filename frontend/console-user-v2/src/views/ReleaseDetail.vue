<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchList, fetchCommand, apiError } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { getWorkload, type Workload } from '@/api/workload'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
interface Release {
  id: string
  appId: string
  envId: string
  imageId: string
  imageDigest: string
  strategy: string
  status: string
  workloadId: string
  previousImageId?: string
  sourceRunId?: string
  laneId?: string
  createdAt: string
  version?: string
  isRollback?: boolean
}
const route = useRoute(),
  env = useEnvStore(),
  release = ref<Release>(),
  workload = ref<Workload>(),
  error = ref(''),
  runtimeError = ref(''),
  loading = ref(true),
  acting = ref(false)
const envName = computed(
  () =>
    env.envs.find((e) => e.id === release.value?.envId)?.name ||
    release.value?.envId ||
    '',
)
let seq = 0
async function load() {
  const rev = ++seq
  loading.value = true
  error.value = ''
  runtimeError.value = ''
  release.value = undefined
  workload.value = undefined
  try {
    const rows = await fetchList<Release>('/api/releases')
    if (rev !== seq) return
    release.value = rows.find((r) => r.id === route.params.id)
    if (!release.value) throw new Error('发布记录不存在或当前账号无权访问')
    await env.loadEnvs()
    if (release.value.workloadId) {
      try {
        const d = await getWorkload(release.value.workloadId)
        if (rev === seq) workload.value = d.workload
      } catch (e) {
        runtimeError.value = apiError(e)
      }
    }
  } catch (e) {
    if (rev === seq) error.value = apiError(e)
  } finally {
    if (rev === seq) loading.value = false
  }
}
watch(() => route.params.id, load, { immediate: true })
async function rollback() {
  const r = release.value
  if (!r) return
  const isProd = env.envs.find((e) => e.id === r.envId)?.type === 'prod'
  if (
    !(await confirmDangerous({
      action: '回滚发布',
      target: r.id,
      isProd,
      requireNameConfirm: isProd,
      message: `目标环境：${envName.value}，泳道：${r.laneId || 'default'}，工作负载：${r.workloadId}。将恢复上一镜像。`,
    }))
  )
    return
  acting.value = true
  try {
    await fetchCommand(`/api/releases/${r.id}/rollback`, { method: 'POST' })
    ElMessage.success('回滚请求已提交')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    acting.value = false
  }
}
</script>
<template>
  <div class="page">
    <RouterLink to="/delivery" class="text-button">← 交付中心</RouterLink><PageHeader title="发布详情" :description="release?.id">
<button
        v-if="release?.status === 'succeeded' && release.previousImageId"
        class="act danger"
        :disabled="acting"
        @click="rollback"
      >
        回滚到上一镜像
      </button>
</PageHeader><StatePanel
      v-if="error"
      title="发布记录加载失败"
      :description="error"
      error
      >
<button class="act" @click="load">重试</button>
</StatePanel>
    <div v-else-if="loading" v-loading="true" style="height: 200px" />
    <template v-else-if="release">
<section class="card">
        <h2>发布记录</h2>
        <dl>
          <dt>产品</dt>
          <dd>
            <RouterLink :to="'/products/' + release.appId">
{{
              release.appId
            }}
</RouterLink>
          </dd>
          <dt>目标环境</dt>
          <dd>{{ envName }} / {{ release.laneId || 'default' }}</dd>
          <dt>状态</dt>
          <dd>
            {{ release.status }}{{ release.isRollback ? '（回滚单）' : '' }}
          </dd>
          <dt>镜像</dt>
          <dd>
            {{ release.imageId }}<code>{{ release.imageDigest }}</code>
          </dd>
          <dt>版本</dt>
          <dd>{{ release.version || '未打版本号' }}</dd>
          <dt>策略</dt>
          <dd>{{ release.strategy }}</dd>
          <dt>创建时间</dt>
          <dd>{{ release.createdAt }}</dd>
          <dt v-if="release.sourceRunId">来源运行</dt>
          <dd v-if="release.sourceRunId">
            <RouterLink :to="'/runs/' + release.sourceRunId">
{{
              release.sourceRunId
            }}
</RouterLink>
          </dd>
          <dt v-if="release.previousImageId">上一镜像</dt>
          <dd v-if="release.previousImageId">{{ release.previousImageId }}</dd>
        </dl>
      </section>
      <section class="card">
        <h2>关联工作负载的当前运行态</h2>
        <StatePanel
          v-if="runtimeError"
          title="运行态暂不可用"
          :description="runtimeError"
          error
        /><template v-else-if="workload">
<p>
            {{ workload.name }} · {{ workload.status }} · {{ workload.ready }} /
            {{ workload.replicas }} 就绪
          </p>
          <code>{{ workload.imageRef || workload.image }}</code>
          <p
            v-if="
              release.imageDigest &&
              !(workload.imageRef || workload.image).includes(
                release.imageDigest,
              )
            "
            class="runtime-note"
          >
            当前镜像与此记录不同，可能已有后续部署或回滚；此记录保留历史快照。
          </p>
          <RouterLink
            :to="{
              path: '/products/' + release.appId,
              query: { workload: workload.id, tab: 'services' },
            }"
            class="act"
            >
实例与日志 →
</RouterLink>
</template>
        <p v-else>此历史记录未关联可查询的工作负载。</p>
      </section>
</template>
  </div>
</template>
<style scoped>
h2 {
  font-size: 16px;
  margin: 0 0 20px;
}
.card {
  margin: 20px 0;
}
dl {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 16px;
  font-size: 13px;
}
dt {
  color: var(--text-faint);
}
dd {
  margin: 0;
  overflow-wrap: anywhere;
}
code {
  display: block;
  font-size: 12px;
  overflow-wrap: anywhere;
  color: var(--text-dim);
}
.runtime-note {
  color: var(--warning);
  font-size: 12px;
}
a {
  color: var(--brand);
}
</style>
