<template>
  <SearchTable
    title="引擎目录管理"
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
        placeholder="搜索 ID / 名称 / Kind / Engine"
        clearable
        style="width: 280px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openAdd">新建引擎</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-kind="{ row }">
      {{ kindLabel(row.kind) }}
    </template>
    <template #col-mode="{ row }">
      <el-tag size="small" :type="modeType(row.mode)">{{ modeLabel(row.mode) }}</el-tag>
    </template>
    <template #col-enabled="{ row }">
      <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
        {{ row.enabled ? '已启用' : '未启用' }}
      </el-tag>
    </template>
    <template #col-actions="{ row }">
      <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
      <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
    </template>
  </SearchTable>

  <EngineFormDrawer
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
  fetchEngineListPage,
  deleteEngine,
  KINDS,
  ENGINE_MODES,
  type Engine,
  type EngineSearchRequest,
} from '../api'
import EngineFormDrawer from './EngineFormDrawer.vue'

const {
  listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange,
} = useCrud<Engine>({
  fetch: (params) => fetchEngineListPage(params as unknown as EngineSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10,
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const kindLabel = (k: string) => KINDS.find((x) => x.value === k)?.label ?? k
const modeLabel = (m: string) => ENGINE_MODES.find((x) => x.value === m)?.label ?? m
const modeType = (m: string) => (m === 'managed' ? 'success' : m === 'external-shared' ? 'warning' : 'info')

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '引擎 ID', minWidth: 140 },
  { prop: 'label', label: '展示名', minWidth: 160 },
  { prop: 'kind', label: 'Kind', width: 110, slot: 'kind' },
  { prop: 'engine', label: 'Engine', width: 130 },
  { prop: 'mode', label: '模式', minWidth: 180, slot: 'mode' },
  { prop: 'enabled', label: '状态', width: 100, slot: 'enabled' },
  { prop: 'order', label: '排序', width: 80 },
  { prop: 'actions', label: '操作', width: 120, fixed: 'right', slot: 'actions' },
])

const drawerVisible = ref(false)
const drawerMode = ref<'add' | 'edit'>('add')
const editingRow = ref<Engine | null>(null)
const openAdd = () => { drawerMode.value = 'add'; editingRow.value = null; drawerVisible.value = true }
const openEdit = (row: Record<string, unknown>) => {
  drawerMode.value = 'edit'; editingRow.value = row as unknown as Engine; drawerVisible.value = true
}
const onFormSuccess = () => { drawerVisible.value = false; fetchList() }

const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(
      `请输入引擎 ID "${id}" 确认删除（已用该引擎创建的实例不受影响，但无法再新建同引擎实例）：`,
      '删除引擎', {
        confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning',
        inputValidator: (v: string) => v === id || `输入与引擎 ID "${id}" 不一致`,
      },
    )
  } catch { return }
  try {
    await deleteEngine(id)
    ElMessage.success('删除成功')
    fetchList()
  } catch { /* 拦截器提示 */ }
}

onMounted(() => { fetchList() })
</script>
