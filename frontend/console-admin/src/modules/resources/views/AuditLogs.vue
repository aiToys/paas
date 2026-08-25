<template>
  <SearchTable
    title="审计日志总览（跨租户）"
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
        placeholder="搜索 操作人 / 动作 / 资源类型"
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

        <template #col-action="{ row }">
      <el-tag size="small" type="info">{{ row.action }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-popover v-if="row.detail" placement="left" width="420" trigger="click">
        <template #reference>
          <el-button link type="primary" size="small">查看详情</el-button>
        </template>
        <pre class="detail-view">{{ row.detail }}</pre>
      </el-popover>
      <span v-else style="color: var(--el-text-color-secondary)">-</span>
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
import { fetchAuditLogList, type AdminAuditLog, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminAuditLog>({
    fetch: (params) => fetchAuditLogList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'actor', label: '操作人', width: 130 },
  { prop: 'action', label: '动作', width: 120, slot: 'action' },
  { prop: 'resourceType', label: '资源类型', width: 130 },
  { prop: 'resourceId', label: '资源 ID', minWidth: 150 },
  { prop: 'detail', label: '详情', width: 110, slot: 'detail' },
  { prop: 'at', label: '时间', width: 180 , formatter: tableTimeFormatter }
])

onMounted(() => fetchList())
</script>

<style scoped>
.detail-view {
  margin: 0;
  max-height: 320px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
