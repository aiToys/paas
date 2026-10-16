<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { listWorkloads, type Workload } from '@/api/workload'
import { statusOf, tagTypeToCls, WORKLOAD_STATUS } from '@/composables/useStatus'
import { useListFilters } from '@/composables/useListFilters'
import { useEnvStore } from '@/stores/env'
import { fetchJSON, apiError } from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import ServiceRunDrawer from '@/components/ServiceRunDrawer.vue'
import Icon from '@/components/Icon.vue'
const envStore = useEnvStore()
const items = ref<Workload[]>([])
const apps = ref<{ id: string; name: string }[]>([])
const loading = ref(true)
const error = ref('')
const page = ref(1)
const selected = ref<Workload>()
const showRun = ref(false)
const { filters, reset } = useListFilters({ q: '', type: '', app: '', status: '' })
const appName = (id: string) => apps.value.find((a) => a.id === id)?.name ?? id
const envName = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id
const types: Record<string, string> = { service: '服务', job: '任务', cronjob: '定时任务' }
async function load() {
  loading.value = true
  error.value = ''
  try { items.value = await listWorkloads({}) }
  catch (e) { error.value = apiError(e, '无法加载运行实例') }
  finally { loading.value = false }
}
onMounted(async () => { await Promise.all([load(), envStore.loadEnvs()]); try { apps.value = await fetchJSON('/api/applications') } catch { /* ID 降级展示，不影响实例操作 */ } })
const filtered = computed(() => items.value.filter((w) => (!envStore.currentEnvId || w.envId === envStore.currentEnvId) && (!filters.type || w.type === filters.type) && (!filters.app || w.appId === filters.app) && (!filters.status || w.status === filters.status) && (!filters.q || (w.name + w.id + w.image + w.laneId).toLowerCase().includes(filters.q.trim().toLowerCase()))))
const statuses = computed(() => [...new Set(items.value.map((w) => w.status))])
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch([filters, () => envStore.currentEnvId], () => { page.value = 1 })
function open(w: Workload) { selected.value = w; showRun.value = true }
</script>
<template>
  <div class="page">
    <PageHeader title="运行实例" description="跨产品查看服务、任务与定时任务，直接进入具体环境和泳道的实例日志。" eyebrow="应用交付" />
    <div class="filter-bar"><label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" aria-label="搜索运行实例" placeholder="搜索名称、镜像或泳道" /></label><select v-model="filters.type" class="filter-select" aria-label="实例类型"><option value="">全部类型</option><option v-for="(label, type) in types" :key="type" :value="type">{{ label }}</option></select><select v-model="filters.app" class="filter-select" aria-label="所属产品"><option value="">全部产品</option><option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option></select><select v-model="filters.status" class="filter-select" aria-label="实例状态"><option value="">全部状态</option><option v-for="s in statuses" :key="s" :value="s">{{ statusOf(WORKLOAD_STATUS, s).label }}</option></select><button class="act" @click="load">刷新</button><button v-if="filters.q || filters.app || filters.status || filters.type" class="text-button" @click="reset">清除筛选</button></div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="运行实例加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="没有匹配的运行实例" description="调整列表筛选或顶栏环境；尚未部署的服务请从产品内发起交付。"><RouterLink to="/products" class="act">查看产品</RouterLink></StatePanel>
    <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table workloads-table"><thead><tr><th scope="col">运行实例</th><th scope="col">产品</th><th scope="col">环境 / 泳道</th><th scope="col">状态</th><th scope="col">副本</th><th scope="col">镜像</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="w in rows" :key="w.id"><td><button class="text-button instance-link" @click="open(w)">{{ w.name }}</button><div class="s-desc">{{ types[w.type] }}{{ w.schedule ? ' · ' + w.schedule : '' }}</div></td><td><RouterLink :to="'/products/' + w.appId" class="text-button">{{ appName(w.appId) }}</RouterLink></td><td><div class="name-cell"><span>{{ envName(w.envId) }}</span><span class="s-desc">{{ w.laneId === 'default' ? '基线' : w.laneId }}</span></div></td><td><span class="status" :class="tagTypeToCls(statusOf(WORKLOAD_STATUS, w.status).type)">{{ statusOf(WORKLOAD_STATUS, w.status).label }}</span></td><td class="mono">{{ w.ready }}/{{ w.replicas }}</td><td><code class="overflow-text image-name" :title="w.imageRef || w.image">{{ (w.imageRef || w.image || '').split('/').pop() }}</code></td><td><button class="act small" @click="open(w)">实例与日志</button></td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
    <ServiceRunDrawer v-model="showRun" :workload-id="selected?.id" :app-id="selected?.appId" :app-name="selected ? appName(selected.appId) : undefined" @changed="load" @deployed="load" />
  </div>
</template>
<style scoped>
@import './list-page.css';
.workloads-table { min-width: 1100px; }
.instance-link { text-align: left; font-weight: 600; overflow-wrap: anywhere; }
.image-name { max-width: 210px; font-size: 12px; color: var(--text-faint); }
</style>
