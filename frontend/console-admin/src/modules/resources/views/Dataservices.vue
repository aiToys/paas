<template>
  <SearchTable
    title="数据服务总览（跨租户）"
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
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-kind="{ row }">
      <el-tag size="small" type="info">{{ kindLabel(row.kind) }}</el-tag>
    </template>
    <template #col-status="{ row }">
      <el-tag :type="statusType(row.status)" size="small">{{ row.status || '-' }}</el-tag>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
import { computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchDataserviceList, type AdminDataservice, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminDataservice>({
    fetch: (params) => fetchDataserviceList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130 },
  { prop: 'id', label: '实例 ID', minWidth: 150 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'kind', label: '类型', width: 110, slot: 'kind' },
  { prop: 'status', label: '状态', width: 110, slot: 'status' }
])

const kindLabel = (k: string) =>
  (({ db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量库', search: '搜索引擎' }) as Record<
    string,
    string
  >)[k] ?? k
const statusType = (s: string) =>
  (({ running: 'success', creating: 'warning', stopped: 'info', failed: 'danger' }) as Record<string, string>)[s] ?? 'info'

onMounted(() => fetchList())
</script>
