<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, fetchList, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { formatDateTime } from '@/utils/format'
import { statusOf, RELEASE_STATUS } from '@/composables/useStatus'
import type { Release, PipelineRun } from '@/api/pipeline'
import CdDeployDialog from './CdDeployDialog.vue'
import StatePanel from './StatePanel.vue'
const props = defineProps<{ appId: string; pickedImageId?: string }>()
const router = useRouter()
const releases = ref<(Release & { isRollback?: boolean })[]>([]), envs = ref<{ id: string; name: string; type: string }[]>([])
const loading = ref(true), error = ref(''), showCreate = ref(false)
let seq = 0
async function load() {
  const cur = ++seq; loading.value = true
  try {
    const [rs, es] = await Promise.all([fetchList<Release>(`/api/applications/${props.appId}/releases`), fetchList<{ id: string; name: string; type: string }>('/api/environments')])
    if (cur === seq) { releases.value = rs; envs.value = es; error.value = '' }
  } catch (e) { if (cur === seq) error.value = apiError(e) }
  finally { if (cur === seq) loading.value = false }
}
watch(() => props.appId, load, { immediate: true })
watch(() => props.pickedImageId, id => { if (id) showCreate.value = true }, { immediate: true })
function opened(run: PipelineRun) { void router.push('/runs/' + run.id) }
async function rollback(r: Release) {
  const env = envs.value.find(e => e.id === r.envId)
  if (!(await confirmDangerous({ action: '回滚部署', target: `${env?.name || r.envId} / ${r.laneId || 'default'} / ${r.workloadId}`, requireNameConfirm: env?.type === 'prod', isProd: env?.type === 'prod' }))) return
  try {
    const resp = await fetchAuth(`/api/releases/${r.id}/rollback`, { method: 'POST' })
    if (!resp.ok) { const json = await resp.json(); throw new Error(json.error || '回滚失败') }
    ElMessage.success('已提交回滚，请检查工作负载运行状态'); await load()
  } catch (e) { ElMessage.error(apiError(e, '回滚失败')) }
}
</script>
<template>
  <div class="devops-tab">
    <div class="tab-head"><span class="tab-title">部署记录</span><el-button type="primary" size="small" @click="showCreate = true">部署已有镜像</el-button></div>
    <p class="note">创建部署将运行标准流水线，完成后可查看部署记录与实际工作负载状态。</p>
    <StatePanel v-if="error" title="部署记录加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <el-table v-else :data="releases" v-loading="loading" size="small" empty-text="尚无部署记录">
      <el-table-column label="状态" width="130"><template #default="{ row }"><el-tag :type="statusOf(RELEASE_STATUS, row.status).type" size="small">{{ statusOf(RELEASE_STATUS, row.status).label }}</el-tag><el-tag v-if="row.isRollback" type="warning" size="small">回滚</el-tag></template></el-table-column>
      <el-table-column label="环境 / 泳道" min-width="150"><template #default="{ row }">{{ envs.find(e => e.id === row.envId)?.name || row.envId }} / {{ row.laneId || 'default' }}</template></el-table-column>
      <el-table-column prop="imageId" label="镜像" min-width="160" />
      <el-table-column label="来源" min-width="180"><template #default="{ row }"><RouterLink v-if="row.sourceRunId" :to="'/runs/' + row.sourceRunId" class="text-button">{{ row.sourceRunId }}</RouterLink><span v-else>独立部署 · 未关联流水线</span></template></el-table-column>
      <el-table-column label="时间" width="170"><template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template></el-table-column>
      <el-table-column label="操作" width="130"><template #default="{ row }"><RouterLink :to="'/releases/' + row.id" class="text-button">详情</RouterLink><el-button v-if="row.status === 'succeeded' && row.previousImageId" text type="warning" size="small" @click="rollback(row)">回滚</el-button></template></el-table-column>
    </el-table>
    <CdDeployDialog v-model="showCreate" :app-id="appId" action="deploy" :image-id="pickedImageId" @deployed="opened" />
  </div>
</template>
<style scoped>
.note { color: var(--text-faint); font-size: 12px; }
.tab-head { display: flex; justify-content: space-between; align-items: center; margin: 18px 0; }
</style>
