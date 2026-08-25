<template>
  <SearchTable
    title="配置命名空间总览（跨租户）"
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
        placeholder="搜索 ID / 名称"
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
    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

      </SearchTable>
</template>

<script lang="ts" setup>
import { computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchNamespaceList, type AdminNamespace, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminNamespace>({
    fetch: (params) => fetchNamespaceList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '命名空间 ID', minWidth: 160 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'desc', label: '描述', minWidth: 180 },
  { prop: 'updatedAt', label: '更新时间', width: 180, formatter: tableTimeFormatter }
  ])

onMounted(() => fetchList())
</script>
