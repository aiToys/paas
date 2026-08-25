<template>
  <SearchTable
    title="镜像总览（跨租户）"
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
        placeholder="搜索 ID / Tag / 应用"
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

        <template #col-digest="{ row }">
      <span :title="row.digest" style="font-family: monospace; font-size: 12px">
        {{ String(row.digest).substring(0, 19) }}...
      </span>
    </template>
    <template #col-status="{ row }">
      <el-tag :type="row.status === 'ready' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情</el-button>
    </template>
  </SearchTable>

  <ImageDrawer v-model="detailVisible" :id="detailId" />
</template>

<script lang="ts" setup>
import { computed, ref, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchImageList, type AdminImage, type ResSearchRequest } from '../api'
import ImageDrawer from './ImageDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminImage>({
    fetch: (params) => fetchImageList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '镜像 ID', minWidth: 160 },
  { prop: 'appId', label: '应用', width: 130 },
  { prop: 'tag', label: 'Tag', minWidth: 120 },
  { prop: 'digest', label: 'Digest', width: 180, slot: 'digest' },
  { prop: 'builtAt', label: '构建时间', width: 180, formatter: tableTimeFormatter },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'detail', label: '操作', width: 100, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminImage) => {
  detailId.value = row.id
  detailVisible.value = true
}

onMounted(() => fetchList())
</script>
