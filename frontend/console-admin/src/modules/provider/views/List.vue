<template>
  <el-alert
    type="info"
    :closable="false"
    show-icon
    style="margin-bottom: 12px"
  >
    {{ t('common.platformScope') }}
  </el-alert>

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
import { t } from '@/lib/i18n'
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
import { fetchModelList } from '@/modules/model/api'
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

// 删除供应商：引用预检（spec D2b）列出引用通道；不级联清通道（vendor_id 变空引用），
// 无安全同步能力——不提供一键传播。
const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  let refLines = ''
  try {
    const models = (await fetchModelList()) ?? []
    const refs: string[] = []
    for (const m of models) {
      for (const ch of m.channels ?? []) {
        if (ch.vendorId === id) refs.push(`  · 模型 ${m.id} / 通道 ${ch.id}`)
      }
    }
    if (refs.length > 0) {
      const more = refs.length > 8 ? `\n  …共 ${refs.length} 条` : ''
      refLines = `\n\n该供应商被 ${refs.length} 个通道引用：\n${refs.slice(0, 8).join('\n')}${more}\n（删除后通道不受影响，其供应商引用变空；新建通道不再可选该供应商）`
    }
  } catch {
    refLines = '\n\n（引用预检加载失败，无法确认通道引用数量）'
  }
  try {
    await ElMessageBox.prompt(
      `请输入供应商 ID "${id}" 确认删除：${refLines}`,
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
