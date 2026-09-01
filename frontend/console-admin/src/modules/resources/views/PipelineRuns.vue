<template>
  <SearchTable
    title="流水线运行总览（跨租户）"
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
      <el-tag :type="runStatusType(row.status)" size="small">{{ row.status }}</el-tag>
    </template>
    <template #col-version="{ row }">
      <span v-if="row.version" style="font-family: monospace">{{ row.version }}</span>
      <span v-else>-</span>
    </template>
    <template #col-action="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row.id)">详情</el-button>
    </template>
  </SearchTable>

  <PipelineRunDrawer v-model="drawerVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { visibleTick } from '@/app/composables/useAdminPolling'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchPipelineRunList, type AdminPipelineRun, type ResSearchRequest } from '../api'
import PipelineRunDrawer from './PipelineRunDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminPipelineRun>({
    fetch: (params) => fetchPipelineRunList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const drawerVisible = ref(false)
const detailId = ref('')
const openDetail = (id: string) => {
  detailId.value = id
  drawerVisible.value = true
}

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const runStatusType = (s: string) =>
  (
    {
      running: 'primary',
      paused: 'warning',
      succeeded: 'success',
      failed: 'danger',
      aborted: 'info'
    } as Record<string, string>
  )[s] ?? 'info'

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '运行 ID', minWidth: 160 },
  { prop: 'appId', label: '应用', width: 130 },
  { prop: 'pipelineId', label: '流水线', width: 160 },
  { prop: 'branch', label: '分支', width: 130 },
  { prop: 'status', label: '状态', width: 110, slot: 'status' },
  { prop: 'currentStage', label: '当前阶段', width: 130 },
  { prop: 'version', label: '版本', width: 130, slot: 'version' },
  { prop: 'createdAt', label: '创建时间', width: 180, formatter: tableTimeFormatter },
  { prop: 'action', label: '操作', width: 90, slot: 'action', hideable: false }
  ])

// 10s 轮询（运行态实时刷新，与 console-user DevOps 运行记录同款）。
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  fetchList()
  timer = setInterval(visibleTick(fetchList), 10000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>
