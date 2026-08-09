<template>
  <SearchTable
    title="流水线模板管理"
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
        placeholder="搜索 ID / 名称 / 分类"
        clearable
        style="width: 280px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openDrawer">新建模板</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-builtin="{ row }">
      <el-tag v-if="row.builtin" size="small" type="warning">内置</el-tag>
      <el-tag v-else size="small" type="success">自定义</el-tag>
    </template>

    <template #col-stages="{ row }">
      <span class="mono">{{ (row.stages || []).length }}</span>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="primary" size="small" :disabled="row.builtin" @click="openEdit(row)">
        编辑
      </el-button>
      <el-button link type="danger" size="small" :disabled="row.builtin" @click="handleDelete(row)">
        删除
      </el-button>
      <el-tooltip v-if="row.builtin" content="内置模板不可改删（走代码发版）" placement="top">
        <el-icon class="lock-tip"><Lock /></el-icon>
      </el-tooltip>
    </template>
  </SearchTable>

  <TemplateFormDrawer v-model="drawerVisible" :mode="drawerMode" :data="editingRow" @success="onFormSuccess" />
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Plus, Refresh, Lock } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchTemplateListPage,
  deleteTemplate,
  type PipelineTemplate,
  type TemplateSearchRequest
} from '../api'
import TemplateFormDrawer from './TemplateFormDrawer.vue'

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<PipelineTemplate>({
  fetch: (params) => fetchTemplateListPage(params as unknown as TemplateSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '模板 ID', minWidth: 140 },
  { prop: 'name', label: '名称', minWidth: 160 },
  { prop: 'kind', label: '分类', width: 120 },
  { prop: 'builtin', label: '类型', width: 100, slot: 'builtin' },
  { prop: 'stages', label: '阶段数', width: 90, slot: 'stages' },
  { prop: 'description', label: '描述', minWidth: 180 },
  { prop: 'actions', label: '操作', width: 180, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const drawerMode = ref<'add' | 'edit'>('add')
const editingRow = ref<PipelineTemplate | null>(null)
const openDrawer = () => {
  drawerMode.value = 'add'
  editingRow.value = null
  drawerVisible.value = true
}
const openEdit = (row: Record<string, unknown>) => {
  drawerMode.value = 'edit'
  editingRow.value = row as unknown as PipelineTemplate
  drawerVisible.value = true
}
const onFormSuccess = () => {
  drawerVisible.value = false
  fetchList()
}

const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(
      `请输入模板 ID "${id}" 确认删除（已绑定该模板的应用流水线不受影响，后续 run 仍按旧 stage 执行）：`,
      '删除模板',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: 'warning',
        inputValidator: (v: string) => v === id || `输入与模板 ID "${id}" 不一致`
      }
    )
  } catch {
    return
  }
  try {
    await deleteTemplate(id)
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

<style scoped>
.lock-tip {
  margin-left: 4px;
  color: var(--el-text-color-placeholder);
  vertical-align: middle;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
