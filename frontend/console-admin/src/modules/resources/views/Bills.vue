<template>
  <SearchTable
    title="账单总览（跨租户：详情 / 标记已付）"
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
        placeholder="搜索 ID / 周期 / 状态"
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
      <el-tag :type="row.status === 'paid' ? 'success' : 'danger'" size="small">
        {{ row.status === 'paid' ? '已支付' : '未支付' }}
      </el-tag>
    </template>
    <template #col-total="{ row }">
      ¥{{ Number(row.total).toFixed(2) }}
    </template>
    <template #col-action="{ row }">
      <el-button
        v-if="row.status === 'unpaid'"
        type="success"
        link
        size="small"
        :loading="payingId === row.id"
        @click="doPay(row.id)"
      >标记已付</el-button>
      <span v-else style="color: var(--el-text-color-secondary)">-</span>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchBillList, payBill, type AdminBill, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminBill>({
    fetch: (params) => fetchBillList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130 },
  { prop: 'id', label: '账单 ID', minWidth: 160 },
  { prop: 'period', label: '周期', width: 110 },
  { prop: 'total', label: '金额', width: 110, slot: 'total' },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'createdAt', label: '创建时间', width: 180 },
  { prop: 'action', label: '操作', width: 110, slot: 'action', hideable: false }
])

const payingId = ref('')
const doPay = async (id: string) => {
  try {
    await ElMessageBox.confirm('确认标记该账单为已支付？', '提示', { type: 'warning' })
  } catch {
    return
  }
  payingId.value = id
  try {
    await payBill(id)
    ElMessage.success('已标记为已支付')
    fetchList()
  } finally {
    payingId.value = ''
  }
}

fetchList()
</script>
