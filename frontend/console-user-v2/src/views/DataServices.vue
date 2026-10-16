<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { listDataServices, type DataServiceEntity } from '@/api/platform'
import { apiError } from '@/api'
import { statusOf, tagTypeToCls, DATASERVICE_STATUS } from '@/composables/useStatus'
import { useListFilters } from '@/composables/useListFilters'
import { useEnvStore } from '@/stores/env'
import { copyText } from '@/utils/clipboard'
import DataServiceCreateDialog from '@/components/DataServiceCreateDialog.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import Icon from '@/components/Icon.vue'
const envStore = useEnvStore()
const items = ref<DataServiceEntity[]>([])
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)
const page = ref(1)
const { filters, reset } = useListFilters({ q: '', kind: '', status: '' })
const KIND_LABEL: Record<string, string> = { db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量数据库', search: '搜索引擎' }
const scoped = computed(() => items.value.filter((d) => !envStore.currentEnvId || d.envId === envStore.currentEnvId))
const filtered = computed(() => scoped.value.filter((d) =>
  (!filters.q || (d.name + d.id).toLowerCase().includes(filters.q.trim().toLowerCase())) &&
  (!filters.kind || d.kind === filters.kind) && (!filters.status || d.status === filters.status),
))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
const statuses = computed(() => [...new Set(scoped.value.map((d) => d.status))])
watch([filters, () => envStore.currentEnvId], () => { page.value = 1 })
async function load() {
  loading.value = true
  error.value = ''
  try { items.value = await listDataServices() }
  catch (e) { error.value = apiError(e, '无法加载数据服务') }
  finally { loading.value = false }
}
onMounted(async () => { await Promise.all([load(), envStore.loadEnvs()]) })
function endpointOf(d: DataServiceEntity) {
  return d.connection?.host ? d.connection.host + (d.connection.port ? ':' + d.connection.port : '') : ''
}
const envName = (id?: string) => envStore.envs.find((e) => e.id === id)?.name ?? id ?? '未指定'
</script>
<template>
  <div class="page">
    <PageHeader title="数据服务" description="统一管理数据库、缓存、消息和存储实例。进入详情查看连接信息、运行状态与资源绑定。" eyebrow="基础设施">
      <button class="act primary" @click="showCreate = true">＋ 新建实例</button>
    </PageHeader>
    <div class="filter-bar">
      <label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" aria-label="搜索数据服务" placeholder="搜索实例名称或 ID" /></label>
      <select v-model="filters.kind" class="filter-select" aria-label="数据服务类型"><option value="">全部类型</option><option v-for="(label, kind) in KIND_LABEL" :key="kind" :value="kind">{{ label }}</option></select>
      <select v-model="filters.status" class="filter-select" aria-label="数据服务状态"><option value="">全部状态</option><option v-for="s in statuses" :key="s" :value="s">{{ statusOf(DATASERVICE_STATUS, s).label }}</option></select>
      <button class="act" :disabled="loading" @click="load">刷新</button>
      <button v-if="filters.q || filters.kind || filters.status" class="text-button" @click="reset">清除筛选</button>
      <span class="result-count">{{ filtered.length }} 个实例</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true" aria-label="正在加载数据服务"></div>
    <StatePanel v-else-if="error" title="数据服务加载失败" :description="error" error><button class="act" @click="load">重新加载</button></StatePanel>
    <StatePanel v-else-if="!items.length" title="还没有数据服务" description="创建实例后，将它绑定到产品即可为服务提供数据库、缓存或存储。"><button class="act primary" @click="showCreate = true">新建实例</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="没有匹配的实例" description="调整类型、状态或顶栏环境范围。"><button class="act" @click="reset">清除列表筛选</button></StatePanel>
    <template v-else>
      <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table data-table">
<colgroup><col style="width:22%" /><col style="width:13%" /><col style="width:12%" /><col style="width:14%" /><col style="width:13%" /><col style="width:26%" /></colgroup>
        <thead><tr><th scope="col">实例名称</th><th scope="col">类型</th><th scope="col">状态</th><th scope="col">环境</th><th scope="col">引擎 / 容量</th><th scope="col">连接地址</th></tr></thead>
        <tbody>
<tr v-for="d in rows" :key="d.id">
          <td><RouterLink :to="'/dataservices/' + d.id" class="instance-name overflow-text" :title="d.name">{{ d.name }}</RouterLink></td>
          <td><span class="type-label">{{ KIND_LABEL[d.kind] ?? d.kind }}</span></td>
          <td><span class="status" :class="tagTypeToCls(statusOf(DATASERVICE_STATUS, d.status).type)">{{ statusOf(DATASERVICE_STATUS, d.status).label }}</span></td>
          <td><span class="overflow-text" :title="d.envId">{{ envName(d.envId) }}</span></td>
          <td><div class="name-cell"><span>{{ d.spec?.engine ?? d.engineId ?? '—' }}</span><span v-if="d.spec?.size_gb" class="s-desc">{{ d.spec.size_gb }} GiB</span></div></td>
          <td><div v-if="endpointOf(d)" class="endpoint-cell"><code class="overflow-text" :title="endpointOf(d)">{{ endpointOf(d) }}</code><button class="act small" :aria-label="'复制 ' + d.name + ' 的连接地址'" @click="copyText(endpointOf(d), '连接地址已复制')">复制</button></div><span v-else class="dim">尚未分配</span></td>
        </tr>
</tbody>
      </table>
</div>
      <TablePager v-model="page" :total="filtered.length" />
    </template>
    <DataServiceCreateDialog v-model="showCreate" @created="load" />
  </div>
</template>
<style scoped>
@import './list-page.css';
.data-table { table-layout: fixed; min-width: 880px; }
.instance-name { color: var(--brand); font-weight: 600; text-decoration: none; }
.instance-name:hover { text-decoration: underline; }
.type-label { white-space: nowrap; color: var(--text-dim); }
.endpoint-cell { display: flex; align-items: center; gap: 10px; min-width: 0; }
.endpoint-cell code { flex: 1; min-width: 0; color: var(--text-faint); font-size: 12px; }
.endpoint-cell button { flex-shrink: 0; }
</style>
