<template>
  <SearchTable
    title="应用管理（跨租户：详情 / 删除）"
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
        style="width: 220px"
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

        <template #col-status="{ row }">
      <el-tag :type="statusType(row.status)" size="small">{{ row.status || '-' }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情</el-button>
    </template>
  </SearchTable>

  <ApplicationDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchAppList, type AdminApplication, type ResSearchRequest } from '../api'
import ApplicationDrawer from './ApplicationDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminApplication>({
    fetch: (params) => fetchAppList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '应用 ID', minWidth: 140 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'env', label: '环境', width: 100 },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'desc', label: '描述', minWidth: 180 },
  { prop: 'detail', label: '操作', width: 100, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminApplication) => {
  detailId.value = row.id
  detailVisible.value = true
}

const statusType = (s: string) =>
  (({ healthy: 'success', degraded: 'warning', idle: 'info' }) as Record<string, string>)[s] ?? 'info'

onMounted(() => fetchList())
</script>
