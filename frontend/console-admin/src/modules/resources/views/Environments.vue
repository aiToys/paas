<template>
  <SearchTable
    title="环境总览（跨租户）"
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
      <el-button type="primary" :icon="Plus" @click="createVisible = true">新建环境</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

    <template #col-name="{ row }">
      <el-button link type="primary" @click="openDetail(row.id)">{{ row.name }}</el-button>
    </template>

        <template #col-type="{ row }">
      <el-tag :type="row.type === 'prod' ? 'danger' : 'success'" size="small">
        {{ row.type === 'prod' ? '生产' : '测试' }}
      </el-tag>
    </template>
  </SearchTable>

  <EnvironmentCreateDrawer v-model="createVisible" @created="fetchList" />
  <EnvironmentDrawer v-model="drawerVisible" :id="detailId" />
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchEnvironmentList, type AdminEnvironment, type ResSearchRequest } from '../api'
import EnvironmentCreateDrawer from './EnvironmentCreateDrawer.vue'
import EnvironmentDrawer from './EnvironmentDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminEnvironment>({
    fetch: (params) => fetchEnvironmentList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])
const createVisible = ref(false)
const drawerVisible = ref(false)
const detailId = ref('')
const openDetail = (id: string) => {
  detailId.value = id
  drawerVisible.value = true
}

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '环境 ID', minWidth: 150 },
  { prop: 'name', label: '名称', minWidth: 140, slot: 'name' },
  { prop: 'type', label: '类型', width: 100, slot: 'type' },
  { prop: 'cluster', label: '集群', width: 140 }
])

fetchList()
</script>
