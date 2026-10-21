<template>
  <SearchTable
    title="配置命名空间总览（跨租户，只读：草稿掩码 / 发布 / 引用）"
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
    </template>

    <template #actions>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>
    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>
    <template #col-version="{ row }">
      <el-tag v-if="row.activeVersion" size="small" type="success">v{{ row.activeVersion }}</el-tag>
      <el-tag v-else size="small" type="info">未发布</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情</el-button>
    </template>
  </SearchTable>

  <ConfigcenterDrawer v-model="detailVisible" :id="detailId" />
</template>

<script lang="ts" setup>
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchConfigcenterList, type AdminConfigcenterRow, type ResSearchRequest } from '../api'
import ConfigcenterDrawer from './ConfigcenterDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminConfigcenterRow>({
    fetch: (params) => fetchConfigcenterList({ ...params, tenantId: scopeStore.tenantId } as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '命名空间 ID', minWidth: 160 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'scope', label: 'scope', width: 80 },
  { prop: 'draftItems', label: '草稿项', width: 80 },
  { prop: 'activeVersion', label: '生效版本', width: 100, slot: 'version' },
  { prop: 'refCount', label: '引用数', width: 80 },
  { prop: 'updatedAt', label: '更新时间', width: 170, formatter: tableTimeFormatter },
  { prop: 'detail', label: '操作', width: 80, slot: 'detail' }
  ])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminConfigcenterRow) => {
  detailId.value = row.id
  detailVisible.value = true
}

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
const scopeStore = useScopeStore()
useScopeChange(() => fetchList(), () => {
  detailVisible.value = false
  detailId.value = ''
})

onMounted(() => fetchList())
</script>
