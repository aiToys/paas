<template>
  <SearchTable
    title="配额总览（跨租户）"
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
        placeholder="搜索租户 ID"
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

    <template #col-limits="{ row }">
      <span
        v-for="[k, v] in Object.entries((row.limits || {}) as Record<string, number>)"
        :key="k"
        style="display: inline-block; margin: 2px 4px 2px 0"
      >
        <el-tag size="small" type="info">{{ k }}: {{ v === -1 ? '∞' : v }}</el-tag>
      </span>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
import { computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchQuotaList, type AdminQuota, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminQuota>({
    fetch: (params) => fetchQuotaList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 160 },
  { prop: 'limits', label: '配额上限', minWidth: 400, slot: 'limits' },
  { prop: 'updatedAt', label: '更新时间', width: 180 }
])

onMounted(() => fetchList())
</script>
