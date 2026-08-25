<template>
  <SearchTable
    title="密钥总览（跨租户，掩码：详情 / 删）"
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
        placeholder="搜索 ID / 名称 / 类型"
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

        <template #col-type="{ row }">
      <el-tag :type="row.type === 'certificate' ? 'warning' : 'info'" size="small">{{ row.type }}</el-tag>
    </template>
    <template #col-scope="{ row }">
      <el-tag :type="row.scope === 'platform' ? 'danger' : 'info'" size="small">
        {{ row.scope === 'platform' ? '平台级' : '租户级' }}
      </el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情</el-button>
    </template>
  </SearchTable>

  <SecretDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchSecretList, type AdminSecret, type ResSearchRequest } from '../api'
import SecretDrawer from './SecretDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminSecret>({
    fetch: (params) => fetchSecretList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '密钥 ID', minWidth: 160 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'type', label: '类型', width: 110, slot: 'type' },
  { prop: 'scope', label: '范围', width: 100, slot: 'scope' },
  { prop: 'updatedAt', label: '更新时间', width: 180, formatter: tableTimeFormatter },
  { prop: 'detail', label: '操作', width: 90, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminSecret) => {
  detailId.value = row.id
  detailVisible.value = true
}

fetchList()
</script>
