<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { listWorkloads, type Workload } from '@/api/workload'
import { statusOf, tagTypeToCls, WORKLOAD_STATUS } from '@/composables/useStatus'
import { useListFilters } from '@/composables/useListFilters'
import { useEnvStore } from '@/stores/env'
import { fetchList, apiError } from '@/api'
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
const route = useRoute()
// lane 深链过滤（环境矩阵裸分支泳道跳入；读路径零副作用不创建实体）。
const { filters, reset } = useListFilters({ q: '', type: '', app: '', status: '', lane: (route.query.lane as string) || '' })
watch(() => route.query.lane, (v) => { filters.lane = (v as string) || '' })
const appName = (id: string) => apps.value.find((a) => a.id === id)?.name ?? id
const envName = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id
const types: Record<string, string> = { service: '服务', job: '任务', cronjob: '定时任务' }
async function load() {
  loading.value = true
  error.value = ''
  try { items.value = await listWorkloads({}) }
  catch (e) { error.value = apiError(e, '无法加载工作负载') }
  finally { loading.value = false }
}
onMounted(async () => { await Promise.all([load(), envStore.loadEnvs()]); try { apps.value = await fetchList('/api/applications') } catch { /* ID 降级展示，不影响实例操作 */ } })
const filtered = computed(() => items.value.filter((w) => (!envStore.currentEnvId || w.envId === envStore.currentEnvId) && (!filters.type || w.type === filters.type) && (!filters.app || w.appId === filters.app) && (!filters.status || w.status === filters.status) && (!filters.lane || (w.laneId || 'default') === filters.lane) && (!filters.q || (w.name + w.id + w.image + w.laneId).toLowerCase().includes(filters.q.trim().toLowerCase()))))
const statuses = computed(() => [...new Set(items.value.map((w) => w.status))])
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch([filters, () => envStore.currentEnvId], () => { page.value = 1 })
function open(w: Workload) { selected.value = w; showRun.value = true }
</script>
<template>
  <div class="page">
    <PageHeader title="工作负载" description="跨产品查看服务、任务和定时任务；进入运行视图后查看 Pod、日志与运行配置。" eyebrow="应用交付" />
    <div class="filter-bar"><label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" aria-label="搜索工作负载" placeholder="搜索名称、镜像或泳道" /></label><select v-model="filters.type" class="filter-select" aria-label="工作负载类型"><option value="">全部类型</option><option v-for="(label, type) in types" :key="type" :value="type">{{ label }}</option></select><select v-model="filters.app" class="filter-select" aria-label="所属产品"><option value="">全部产品</option><option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option></select><select v-model="filters.status" class="filter-select" aria-label="工作负载状态"><option value="">全部状态</option><option v-for="s in statuses" :key="s" :value="s">{{ statusOf(WORKLOAD_STATUS, s).label }}</option></select><button class="act" @click="load">刷新</button><span v-if="filters.lane" class="filter-chip">泳道：{{ filters.lane }}</span><button v-if="filters.q || filters.app || filters.status || filters.type || filters.lane" class="text-button" @click="reset">清除筛选</button></div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="工作负载加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="没有匹配的工作负载" description="调整列表筛选或顶栏环境；尚未部署的服务请从产品内发起交付。"><RouterLink to="/products" class="act">查看产品</RouterLink></StatePanel>
    <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table workloads-table"><thead><tr><th scope="col">工作负载</th><th scope="col">产品</th><th scope="col">环境 / 泳道</th><th scope="col">状态</th><th scope="col">副本</th><th scope="col">镜像</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="w in rows" :key="w.id"><td><RouterLink class="text-button instance-link" :to="'/workloads/' + encodeURIComponent(w.id)">{{ w.name }}</RouterLink><div class="s-desc">{{ types[w.type] }}{{ w.schedule ? ' · ' + w.schedule : '' }}</div></td><td><RouterLink :to="'/products/' + w.appId" class="text-button">{{ appName(w.appId) }}</RouterLink></td><td><div class="name-cell"><span>{{ envName(w.envId) }}</span><span class="s-desc">{{ w.laneId === 'default' ? '基线' : w.laneId }}</span></div></td><td><span class="status" :class="tagTypeToCls(statusOf(WORKLOAD_STATUS, w.status).type)">{{ statusOf(WORKLOAD_STATUS, w.status).label }}</span></td><td class="mono">{{ w.ready }}/{{ w.replicas }}</td><td><code class="overflow-text image-name" :title="w.imageRef || w.image">{{ (w.imageRef || w.image || '').split('/').pop() }}</code></td><td><div class="workload-actions"><RouterLink class="act small" :to="'/workloads/' + encodeURIComponent(w.id)">运行视图</RouterLink><button class="text-button" @click="open(w)">快速查看</button></div></td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
    <ServiceRunDrawer v-model="showRun" :workload-id="selected?.id" :app-id="selected?.appId" :app-name="selected ? appName(selected.appId) : undefined" @changed="load" @deployed="load" />
  </div>
</template>
<style scoped>
.filter-chip {
  padding: 3px 10px;
  border: 1px solid var(--border, #2a3142);
  border-radius: 999px;
  background: var(--surface-2, #171c2b);
  color: var(--brand, #4f7cff);
  font-size: 12px;
  white-space: nowrap;
}
@import './list-page.css';
.workloads-table { min-width: 1100px; }
.instance-link { text-align: left; font-weight: 600; overflow-wrap: anywhere; }
.workload-actions { display: flex; gap: 12px; align-items: center; white-space: nowrap; }
.image-name { max-width: 210px; font-size: 12px; color: var(--text-faint); }
</style>
