<script setup lang="ts">
import { computed, ref, watch, toRefs } from 'vue'
import { fetchList, apiError } from '@/api'
import { listRuns, listBuildRuns, listReleases, type BuildRun, type Release } from '@/api/pipeline'
import { deliveryRecords } from '@/domain/delivery'
import { usePolling } from '@/composables/usePolling'
import { useListFilters } from '@/composables/useListFilters'
import { formatDateTime } from '@/utils/format'
import { statusOf, tagTypeToCls, RUN_STATUS, BUILD_STATUS, RELEASE_STATUS } from '@/composables/useStatus'
import StatePanel from './StatePanel.vue'
import TablePager from './TablePager.vue'
const props = defineProps<{ appId?: string }>()
const records = ref<ReturnType<typeof deliveryRecords>>([]), apps = ref<{ id: string; name: string }[]>([]), envs = ref<{ id: string; name: string }[]>([])
const loading = ref(true), error = ref(''), page = ref(1)
const { filters, reset } = useListFilters({ q: '', kind: '', app: '', status: '' })
const { q, kind, app, status } = toRefs(filters)
const names = { pipeline: '流水线', build: '构建', deploy: '部署' }
const appName = (id: string) => apps.value.find(a => a.id === id)?.name ?? id
const envName = (id?: string) => envs.value.find(e => e.id === id)?.name ?? id
const tag = (r: typeof records.value[number]) => statusOf(r.kind === 'build' ? BUILD_STATUS : r.kind === 'deploy' ? RELEASE_STATUS : RUN_STATUS, r.status)
let seq = 0
async function load() {
  const cur = ++seq
  try {
    const [runs, builds, releases] = await Promise.all([listRuns({ appId: props.appId }), props.appId ? listBuildRuns(props.appId) : fetchList<BuildRun>('/api/buildruns'), props.appId ? listReleases(props.appId) : fetchList<Release>('/api/releases')])
    if (cur !== seq) return
    records.value = deliveryRecords(runs, builds, releases); error.value = ''
  } catch (e) { if (cur === seq) error.value = apiError(e, '交付记录加载失败') }
  finally { if (cur === seq) loading.value = false }
}
watch(() => props.appId, () => { loading.value = true; page.value = 1; void load() })
usePolling(load, 10000, { active: () => records.value.some(r => ['pending', 'running', 'paused', 'deploying'].includes(r.status)) })
void Promise.allSettled([fetchList<{ id: string; name: string }>('/api/applications').then(v => { apps.value = v }), fetchList<{ id: string; name: string }>('/api/environments').then(v => { envs.value = v })])
const filtered = computed(() => records.value.filter(r => (!kind.value || r.kind === kind.value) && (!!props.appId || !app.value || r.appId === app.value) && (!status.value || r.kind + ':' + r.status === status.value || r.status === status.value) && (!q.value || [r.id, r.sourceRunId, r.source, r.branch, r.imageId, r.service, envName(r.envId), r.lane, appName(r.appId)].join(' ').toLowerCase().includes(q.value.trim().toLowerCase()))))
const statuses = computed(() => {
  const options = new Map(records.value.map(r => [r.kind + ':' + r.status, names[r.kind] + ' · ' + tag(r).label]))
  if (status.value && !status.value.includes(':')) options.set(status.value, '所有类型 · ' + statusOf(status.value === 'success' ? BUILD_STATUS : RUN_STATUS, status.value).label)
  return [...options.entries()]
})
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch([q, kind, app, status], () => { page.value = 1 })
</script>
<template>
  <div class="history">
    <p class="dim">统一查看流水线、构建和部署记录。独立调用产生的历史记录保留实际来源。部署配置更新与运行就绪分别展示。</p>
    <div class="filter-bar">
      <input v-model="q" class="filter-select" aria-label="搜索交付记录" placeholder="搜索记录、服务、镜像、泳道" />
      <select v-model="kind" class="filter-select" aria-label="交付记录类型"><option value="">全部类型</option><option v-for="(name, id) in names" :key="id" :value="id">{{ name }}</option></select>
      <select v-if="!appId" v-model="app" class="filter-select" aria-label="交付记录产品"><option value="">全部产品</option><option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option></select>
      <select v-model="status" class="filter-select" aria-label="交付状态"><option value="">全部状态</option><option v-for="[id, name] in statuses" :key="id" :value="id">{{ name }}</option></select>
      <button class="act" @click="load">刷新</button>
      <button v-if="q || kind || app || status" class="text-button" @click="reset">重置筛选</button>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="交付记录加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="没有匹配的交付记录" description="可以发起构建或部署，也可以调整筛选条件。" />
    <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table"><thead><tr><th scope="col">记录</th><th v-if="!appId" scope="col">产品</th><th scope="col">状态</th><th scope="col">来源</th><th scope="col">目标 / 分支</th><th scope="col">时间</th></tr></thead><tbody><tr v-for="r in rows" :key="r.id"><td><span class="dim">{{ names[r.kind] }}</span><br /><RouterLink :to="r.to" class="text-button mono">{{ r.id }}</RouterLink></td><td v-if="!appId"><RouterLink :to="'/products/' + r.appId" class="text-button">{{ appName(r.appId) }}</RouterLink></td><td><span class="status" :class="tagTypeToCls(tag(r).type)">{{ tag(r).label }}</span></td><td>{{ r.source }}<br /><RouterLink v-if="r.sourceRunId" :to="'/runs/' + r.sourceRunId" class="text-button mono">{{ r.sourceRunId }}</RouterLink></td><td><span v-if="r.envId">{{ r.service ? r.service + ' · ' : '' }}{{ envName(r.envId) }} / {{ r.lane }}</span><span v-else class="mono">{{ r.branch || '—' }}</span><small v-if="r.imageId" class="mono">{{ r.imageId }}</small></td><td class="dim">{{ formatDateTime(r.createdAt) }}</td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
  </div>
</template>
<style scoped>
@import '../views/list-page.css';
.dim { color: var(--text-faint); font-size: 12px; }
table { min-width: 900px; }
td { overflow-wrap: anywhere; max-width: 300px; }
small { display: block; }
.loading-block { min-height: 150px; }
</style>
