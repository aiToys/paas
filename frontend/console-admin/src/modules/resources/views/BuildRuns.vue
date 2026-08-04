<template>
  <SearchTable
    title="构建总览（跨租户）"
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
        placeholder="搜索 ID / 应用 / 状态"
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

    <template #col-status="{ row }">
      <el-tag :type="buildStatusType(row.status)" size="small">{{ row.status }}</el-tag>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
import { computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchBuildRunList, type AdminBuildRun, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminBuildRun>({
    fetch: (params) => fetchBuildRunList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const buildStatusType = (s: string) =>
  (({ success: 'success', running: 'warning', failed: 'danger', pending: 'info' }) as Record<string, string>)[s] ?? 'info'

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130 },
  { prop: 'id', label: '构建 ID', minWidth: 160 },
  { prop: 'appId', label: '应用', width: 130 },
  { prop: 'status', label: '状态', width: 110, slot: 'status' },
  { prop: 'commit', label: 'Commit', width: 110 },
  { prop: 'branch', label: '分支', width: 120 },
  { prop: 'startedAt', label: '开始时间', width: 180 }
])

onMounted(() => fetchList())
</script>
