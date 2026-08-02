<template>
  <SearchTable
    title="租户管理"
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
        placeholder="搜索租户 ID / 名称"
        clearable
        style="width: 240px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openDrawer">新建租户</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
    </template>
  </SearchTable>

  <TenantFormDrawer v-model="drawerVisible" @success="onFormSuccess" />
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchTenantList,
  deleteTenant,
  type TenantInfo,
  type TenantSearchRequest
} from '../api'
import TenantFormDrawer from './TenantFormDrawer.vue'

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<TenantInfo>({
  fetch: (params) => fetchTenantList(params as unknown as TenantSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '租户 ID', minWidth: 160 },
  { prop: 'name', label: '租户名称', minWidth: 160 },
  { prop: 'actions', label: '操作', width: 120, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const openDrawer = () => {
  drawerVisible.value = true
}
const onFormSuccess = () => {
  drawerVisible.value = false
  fetchList()
}

// 删除租户：高危操作，输入租户 ID 确认（不可恢复）；core 有用户时返 409 引导先清用户。
const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(`请输入租户 ID "${id}" 确认删除（不可恢复）：`, '删除租户', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      inputValidator: (v: string) => v === id || `输入与租户 ID "${id}" 不一致`
    })
  } catch {
    return // 用户取消
  }
  try {
    await deleteTenant(id)
    ElMessage.success('删除成功')
    fetchList()
  } catch {
    // 409（有用户）等由 http 拦截器提示
  }
}

onMounted(() => {
  fetchList()
})
</script>
