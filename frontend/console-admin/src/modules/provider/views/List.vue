<template>
  <SearchTable
    title="供应商管理"
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
        placeholder="搜索 ID / 名称 / BaseURL"
        clearable
        style="width: 280px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openDrawer">新建供应商</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-credentialRef="{ row }">
      <el-tag size="small" type="info">{{ row.credentialRef || '-' }}</el-tag>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
      <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
    </template>
  </SearchTable>

  <ProviderFormDrawer
    v-model="drawerVisible"
    :mode="drawerMode"
    :data="editingRow"
    @success="onFormSuccess"
  />
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchVendorListPage,
  deleteVendor,
  type Vendor,
  type VendorSearchRequest
} from '../api'
import ProviderFormDrawer from './ProviderFormDrawer.vue'

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<Vendor>({
  fetch: (params) => fetchVendorListPage(params as unknown as VendorSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '供应商 ID', minWidth: 120 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'type', label: '类型', width: 140 },
  { prop: 'baseUrl', label: 'BaseURL', minWidth: 260 },
  { prop: 'credentialRef', label: '凭证', width: 180, slot: 'credentialRef' },
  { prop: 'description', label: '描述', minWidth: 160 },
  { prop: 'actions', label: '操作', width: 120, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const drawerMode = ref<'add' | 'edit'>('add')
const editingRow = ref<Vendor | null>(null)
const openDrawer = () => {
  drawerMode.value = 'add'
  editingRow.value = null
  drawerVisible.value = true
}
const openEdit = (row: Record<string, unknown>) => {
  drawerMode.value = 'edit'
  editingRow.value = row as unknown as Vendor
  drawerVisible.value = true
}
const onFormSuccess = () => {
  drawerVisible.value = false
  fetchList()
}

// 删除供应商：输入 ID 确认（不级联清通道，但通道 vendor_id 变空引用）。
const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(
      `请输入供应商 ID "${id}" 确认删除（已绑定该供应商的通道不受影响，其 vendor_id 变空引用）：`,
      '删除供应商',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: 'warning',
        inputValidator: (v: string) => v === id || `输入与供应商 ID "${id}" 不一致`
      }
    )
  } catch {
    return
  }
  try {
    await deleteVendor(id)
    ElMessage.success('删除成功')
    fetchList()
  } catch {
    // 拦截器提示
  }
}

onMounted(() => {
  fetchList()
})
</script>
