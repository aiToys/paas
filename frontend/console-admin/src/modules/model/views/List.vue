<template>
  <SearchTable
    title="模型管理"
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
        placeholder="搜索 ID / 名称 / 供应商"
        clearable
        style="width: 260px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openDrawer">新建模型</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-channels="{ row }">
      <el-tag size="small" type="info">{{ channelCount(row) }} 个通道</el-tag>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="primary" size="small" @click="openChannels(row)">通道</el-button>
      <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
    </template>
  </SearchTable>

  <ModelFormDrawer v-model="drawerVisible" mode="add" :data="null" @success="onFormSuccess" />
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchModelListPage,
  deleteModel,
  type ModelInfo,
  type ModelSearchRequest
} from '../api'
import ModelFormDrawer from './ModelFormDrawer.vue'

const router = useRouter()

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<ModelInfo>({
  fetch: (params) => fetchModelListPage(params as unknown as ModelSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const channelCount = (row: Record<string, unknown>) =>
  Array.isArray(row.channels) ? row.channels.length : 0

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '模型 ID', minWidth: 150 },
  { prop: 'name', label: '名称', minWidth: 150 },
  { prop: 'vendor', label: '供应商', minWidth: 110 },
  { prop: 'contextWindow', label: '上下文', width: 100 },
  { prop: 'channels', label: '通道', width: 110, slot: 'channels' },
  { prop: 'actions', label: '操作', width: 140, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const openDrawer = () => {
  drawerVisible.value = true
}
const onFormSuccess = () => {
  drawerVisible.value = false
  fetchList()
}

const openChannels = (row: Record<string, unknown>) => {
  router.push(`/model/${row.id}`)
}

// 删除模型：高危，输入 ID 确认（级联清通道）。
const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(`请输入模型 ID "${id}" 确认删除（将级联清通道，不可恢复）：`, '删除模型', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      inputValidator: (v: string) => v === id || `输入与模型 ID "${id}" 不一致`
    })
  } catch {
    return
  }
  try {
    await deleteModel(id)
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
