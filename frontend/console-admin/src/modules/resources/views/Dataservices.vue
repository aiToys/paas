<template>
  <SearchTable
    title="数据服务管理（跨租户：详情 / 运维 / 代建）"
    :loading="loading"
    :data="tableData"
    :columns="columns"
    :pagination="pagination"
    row-key="id"
    @search="handleSearch"
    @reset="handleReset"
    @page-change="handlePageChange"
  >
    <template #search>
      <el-input
        v-model="searchForm.keyword"
        placeholder="搜索 ID / 名称 / 类型"
        clearable
        style="width: 240px"
        @keyup.enter="handleSearch"
      />
      <el-input
        v-model="searchForm.tenantId"
        placeholder="租户 ID 过滤"
        clearable
        style="width: 160px; margin-left: 8px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="createVisible = true">新建数据服务</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

        <template #col-kind="{ row }">
      <el-tag size="small" type="info">{{ kindLabel(row.kind) }}</el-tag>
    </template>
    <template #col-engine="{ row }">
      <span v-if="row.engineId">{{ row.engineId }}</span>
      <span v-else style="color: var(--el-text-color-secondary)">-</span>
    </template>
    <template #col-status="{ row }">
      <el-tag :type="statusType(row.status)" size="small">{{ row.status || '-' }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情 / 运维</el-button>
    </template>
  </SearchTable>

  <DataserviceDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
  <DataserviceCreateDrawer v-model="createVisible" @created="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchDataserviceList, type AdminDataservice, type ResSearchRequest } from '../api'
import DataserviceDrawer from './DataserviceDrawer.vue'
import DataserviceCreateDrawer from './DataserviceCreateDrawer.vue'

// 引擎互链：从引擎目录「查看实例」跳入时带 ?engine=<id> 预过滤。
const route = useRoute()
const engineFilter = () => (typeof route.query.engine === 'string' ? route.query.engine : '')

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminDataservice>({
    fetch: (params) =>
      fetchDataserviceList(params as unknown as ResSearchRequest).then((res) => {
        const eng = engineFilter()
        if (!eng) return res
        const filtered = (res.records ?? []).filter((d) => d.engineId === eng)
        return { ...res, records: filtered, total: filtered.length }
      }),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '实例 ID', minWidth: 150 },
  { prop: 'engineId', label: '引擎', width: 120, slot: 'engine' },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'kind', label: '类型', width: 110, slot: 'kind' },
  { prop: 'status', label: '状态', width: 110, slot: 'status' },
  { prop: 'detail', label: '操作', width: 120, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const createVisible = ref(false)
const openDetail = (row: AdminDataservice) => {
  detailId.value = row.id
  detailVisible.value = true
}

const kindLabel = (k: string) =>
  (({ db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量库', search: '搜索引擎' }) as Record<
    string,
    string
  >)[k] ?? k
const statusType = (s: string) =>
  (({ running: 'success', creating: 'warning', stopped: 'info', failed: 'danger' }) as Record<string, string>)[s] ?? 'info'

fetchList()
</script>
