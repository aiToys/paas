<template>
  <SearchTable
    title="告警规则总览（跨租户：详情 / 删）"
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
        placeholder="搜索 ID / 名称 / 指标"
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

    <template #col-severity="{ row }">
      <el-tag :type="row.severity === 'critical' ? 'danger' : 'warning'" size="small">{{ row.severity }}</el-tag>
    </template>
    <template #col-enabled="{ row }">
      <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '停用' }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情</el-button>
    </template>
  </SearchTable>

  <AlertRuleDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchAlertRuleList, type AdminAlertRule, type ResSearchRequest } from '../api'
import AlertRuleDrawer from './AlertRuleDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminAlertRule>({
    fetch: (params) => fetchAlertRuleList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130 },
  { prop: 'name', label: '规则名称', minWidth: 140 },
  { prop: 'metricName', label: '指标', width: 120 },
  { prop: 'targetType', label: '目标类型', width: 110 },
  { prop: 'operator', label: '操作符', width: 90 },
  { prop: 'threshold', label: '阈值', width: 90 },
  { prop: 'severity', label: '级别', width: 100, slot: 'severity' },
  { prop: 'enabled', label: '状态', width: 90, slot: 'enabled' },
  { prop: 'detail', label: '操作', width: 90, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminAlertRule) => {
  detailId.value = row.id
  detailVisible.value = true
}

fetchList()
</script>
