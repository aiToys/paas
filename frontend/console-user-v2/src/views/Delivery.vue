<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { fetchJSON, apiError } from '@/api'
import { listRuns, type PipelineRun } from '@/api/pipeline'
import { listNotifications, listAllChanges, listAllBatches, type Notification, type Change, type IntegrationBatch } from '@/api/change'
import { notificationLink } from '@/navigation'
import { useListFilters } from '@/composables/useListFilters'
import { usePolling } from '@/composables/usePolling'
import { statusOf, tagTypeToCls, RUN_STATUS, CHANGE_STATUS, BATCH_STATUS } from '@/composables/useStatus'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import Icon from '@/components/Icon.vue'
const { filters, reset } = useListFilters({ view: 'attention', q: '', app: '', status: '' })
const tabs = [{ id: 'attention', label: '待处理' }, { id: 'runs', label: '运行记录' }, { id: 'changes', label: '变更' }, { id: 'batches', label: '集成批次' }]
const currentView = computed(() => tabs.some((t) => t.id === filters.view) ? filters.view : 'attention')
const apps = ref<{ id: string; name: string }[]>([])
const runs = ref<PipelineRun[]>([])
const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const notifications = ref<Notification[]>([])
const loading = ref(true)
const error = ref('')
const page = ref(1)
const appName = (id: string) => apps.value.find((a) => a.id === id)?.name ?? id
let seq = 0
async function load() {
  const cur = ++seq
  const view = currentView.value
  error.value = ''
  try {
    if (view === 'attention') { const data = await listNotifications(); if (cur === seq) notifications.value = data }
    else if (view === 'runs') { const data = await listRuns(); if (cur === seq) runs.value = data }
    else if (view === 'changes') { const data = await listAllChanges(); if (cur === seq) changes.value = data }
    else { const data = await listAllBatches(); if (cur === seq) batches.value = data }
  } catch (e) { if (cur === seq) error.value = apiError(e, '无法加载交付记录') }
  finally { if (cur === seq) loading.value = false }
}
onMounted(async () => { try { apps.value = await fetchJSON('/api/applications') } catch { /* 主列表保留产品 ID，查询错误独立展示 */ } })
usePolling(load, 30000)
watch(currentView, () => { page.value = 1; filters.status = ''; loading.value = true; void load() })
watch([() => filters.q, () => filters.app, () => filters.status], () => { page.value = 1 })
const entries = computed(() => {
  if (currentView.value === 'attention') return notifications.value.filter((n) => n.severity !== 'info').map((n) => ({ id: n.id, title: n.title, appId: n.appId, status: n.severity, statusLabel: n.severity === 'error' ? '失败待处理' : '等待决策', cls: n.severity === 'error' ? 'err' : 'warn', branch: '', createdAt: n.at, to: notificationLink(n) }))
  if (currentView.value === 'runs') return runs.value.map((r) => ({ id: r.id, title: r.id, appId: r.appId, status: r.status, statusLabel: statusOf(RUN_STATUS, r.status).label, cls: tagTypeToCls(statusOf(RUN_STATUS, r.status).type), branch: r.branch, createdAt: r.createdAt, to: '/runs/' + r.id }))
  if (currentView.value === 'changes') return changes.value.map((c) => ({ id: c.id, title: c.title, appId: c.appId, status: c.status, statusLabel: statusOf(CHANGE_STATUS, c.status).label, cls: tagTypeToCls(statusOf(CHANGE_STATUS, c.status).type), branch: c.branch, createdAt: c.createdAt, to: notificationLink({ targetType: 'change', targetId: c.id, appId: c.appId }) }))
  return batches.value.map((b) => ({ id: b.id, title: b.title, appId: b.appId, status: b.status, statusLabel: statusOf(BATCH_STATUS, b.status).label, cls: tagTypeToCls(statusOf(BATCH_STATUS, b.status).type), branch: b.branch, createdAt: b.createdAt, to: notificationLink({ targetType: 'batch', targetId: b.id, appId: b.appId }) }))
})
const statuses = computed(() => [...new Map(entries.value.map((r) => [r.status, r.statusLabel])).entries()])
const filtered = computed(() => entries.value.filter((r) => (!filters.app || r.appId === filters.app) && (!filters.status || r.status === filters.status) && (!filters.q || (r.title + r.id + r.branch + appName(r.appId)).toLowerCase().includes(filters.q.trim().toLowerCase()))).sort((a, b) => b.createdAt.localeCompare(a.createdAt)))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
</script>
<template>
  <div class="page">
    <PageHeader title="交付中心" description="跨产品跟进失败、审批与交付进度。具体的流水线和部署配置归属各自产品。" eyebrow="应用交付"><RouterLink to="/products" class="act">进入产品发起交付</RouterLink></PageHeader>
    <nav class="section-nav" aria-label="交付视图"><button v-for="t in tabs" :key="t.id" :class="{ active: currentView === t.id }" :aria-current="currentView === t.id ? 'page' : undefined" @click="filters.view = t.id">{{ t.label }}</button></nav>
    <div class="filter-bar">
      <label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" aria-label="搜索交付记录" placeholder="搜索标题、分支或运行 ID" /></label>
      <select v-model="filters.app" class="filter-select" aria-label="按产品筛选"><option value="">全部产品</option><option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option></select>
      <select v-model="filters.status" class="filter-select" aria-label="按状态筛选"><option value="">全部状态</option><option v-for="[value, label] in statuses" :key="value" :value="value">{{ label }}</option></select>
      <button class="act" @click="load">刷新</button>
      <button v-if="filters.q || filters.app || filters.status" class="text-button" @click="reset">重置筛选</button>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="交付记录加载失败" :description="error" error><button class="act" @click="load">重新加载</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" :title="currentView === 'attention' ? '当前没有待处理事项' : '没有匹配的记录'" description="可调整筛选条件，或进入产品查看完整的交付配置。" />
    <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table delivery-table"><thead><tr><th scope="col">{{ currentView === 'runs' ? '运行 ID' : '事项' }}</th><th scope="col">产品</th><th scope="col">状态</th><th v-if="currentView !== 'attention'" scope="col">分支</th><th scope="col">时间</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="r in rows" :key="r.id"><td><RouterLink :to="r.to" class="entry-link">{{ r.title }}</RouterLink></td><td><RouterLink :to="'/products/' + r.appId" class="text-button">{{ appName(r.appId) }}</RouterLink></td><td><span class="status" :class="r.cls">{{ r.statusLabel }}</span></td><td v-if="currentView !== 'attention'" class="mono">{{ r.branch || '—' }}</td><td class="dim">{{ new Date(r.createdAt).toLocaleString('zh-CN') }}</td><td><RouterLink :to="r.to" class="act small">查看 →</RouterLink></td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
  </div>
</template>
<style scoped>
@import './list-page.css';
.section-nav { margin-bottom: 0; }
.delivery-table { min-width: 900px; }
.entry-link { color: var(--brand); text-decoration: none; overflow-wrap: anywhere; font-weight: 500; }
.entry-link:hover { text-decoration: underline; }
</style>
