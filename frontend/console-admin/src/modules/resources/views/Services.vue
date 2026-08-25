<template>
  <SearchTable
    title="服务治理总览（跨租户：详情 / 注销实例 / 删）"
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
        placeholder="搜索 ID / 名称 / 协议"
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

        <template #col-protocol="{ row }">
      <el-tag :type="row.protocol === 'grpc' ? 'success' : 'primary'" size="small">{{ row.protocol }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情 / 运维</el-button>
    </template>
  </SearchTable>

  <ServiceDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchServiceList, type AdminService, type ResSearchRequest } from '../api'
import ServiceDrawer from './ServiceDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminService>({
    fetch: (params) => fetchServiceList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '服务 ID', minWidth: 160 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'protocol', label: '协议', width: 100, slot: 'protocol' },
  { prop: 'port', label: '端口', width: 90 },
  { prop: 'envId', label: '环境', minWidth: 130 },
  { prop: 'appId', label: '应用', minWidth: 130 },
  { prop: 'detail', label: '操作', width: 120, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminService) => {
  detailId.value = row.id
  detailVisible.value = true
}

fetchList()
</script>
