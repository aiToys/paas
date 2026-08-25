<template>
  <SearchTable
    title="发布总览（跨租户）"
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

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

        <template #col-status="{ row }">
      <el-tag :type="releaseStatusType(row.status)" size="small">{{ row.status }}</el-tag>
    </template>
    <template #col-isRollback="{ row }">
      <el-tag v-if="row.isRollback" type="warning" size="small">回滚</el-tag>
      <span v-else>-</span>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情 / 回滚</el-button>
    </template>
  </SearchTable>

  <ReleaseDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchReleaseList, type AdminRelease, type ResSearchRequest } from '../api'
import ReleaseDrawer from './ReleaseDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminRelease>({
    fetch: (params) => fetchReleaseList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const releaseStatusType = (s: string) =>
  (
    {
      succeeded: 'success',
      failed: 'danger',
      'rolled-back': 'info',
      deploying: 'warning',
      pending: 'warning'
    } as Record<string, string>
  )[s] ?? 'info'

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '发布 ID', minWidth: 160 },
  { prop: 'appId', label: '应用', width: 130 },
  { prop: 'envId', label: '环境', width: 130 },
  { prop: 'strategy', label: '策略', width: 100 },
  { prop: 'status', label: '状态', width: 110, slot: 'status' },
  { prop: 'isRollback', label: '回滚', width: 80, slot: 'isRollback' },
  { prop: 'createdAt', label: '发布时间', width: 180, formatter: tableTimeFormatter },
  { prop: 'detail', label: '操作', width: 110, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminRelease) => {
  detailId.value = row.id
  detailVisible.value = true
}

onMounted(() => fetchList())
</script>
