<template>
  <SearchTable
    title="API 密钥管理"
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
      <el-select
        v-model="searchForm.tenantId"
        placeholder="按租户过滤"
        clearable
        style="width: 200px"
        @change="handleSearch"
      >
        <el-option v-for="t in tenants" :key="t.id" :label="`${t.name} (${t.id})`" :value="t.id" />
      </el-select>
      <el-input
        v-model="searchForm.keyword"
        placeholder="搜索 ID / 用户 / 租户"
        clearable
        style="width: 240px; margin-left: 8px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="openDrawer">新建密钥</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-roles="{ row }">
      <el-tag v-for="r in (row.roles as string[])" :key="r" size="small" style="margin-right: 4px">
        {{ r }}
      </el-tag>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="danger" size="small" @click="handleDelete(row)">吊销</el-button>
    </template>
  </SearchTable>

  <ApiKeyFormDrawer v-model="drawerVisible" @created="onCreated" />

  <!-- 创建后明文仅展示一次 -->
  <el-dialog v-model="showPlaintext" title="密钥已创建" width="560px" :close-on-click-modal="false">
    <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
      完整密钥仅显示这一次，关闭后无法再次查看。请立即复制保存。
    </el-alert>
    <div style="display: flex; align-items: center; gap: 8px">
      <code style="flex: 1; padding: 8px 10px; background: var(--el-fill-color-light); border-radius: 6px; word-break: break-all; font-size: 12.5px">{{ plaintext }}</code>
      <el-button type="primary" size="small" @click="copy(plaintext)">复制</el-button>
    </div>
    <template #footer>
      <el-button type="primary" @click="showPlaintext = false">我已保存</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchApiKeyList,
  deleteApiKey,
  fetchAllTenants,
  type ApiKeyInfo,
  type ApiKeySearchRequest,
  type TenantInfo
} from '../api'
import ApiKeyFormDrawer from './ApiKeyFormDrawer.vue'

const tenants = ref<TenantInfo[]>([])

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<ApiKeyInfo>({
  fetch: (params) => fetchApiKeyList(params as unknown as ApiKeySearchRequest),
  defaultSearchForm: { keyword: '', tenantId: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '密钥 ID', minWidth: 180 },
  { prop: 'tenantId', label: '租户', minWidth: 120 },
  { prop: 'userId', label: '用户', minWidth: 140 },
  { prop: 'roles', label: '角色', minWidth: 160, slot: 'roles' },
  { prop: 'key', label: '密钥（掩码）', minWidth: 200 },
  { prop: 'createdAt', label: '创建时间', minWidth: 180 },
  { prop: 'actions', label: '操作', width: 100, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const openDrawer = () => {
  drawerVisible.value = true
}

// 创建成功：弹明文展示框 + 刷新列表。
const showPlaintext = ref(false)
const plaintext = ref('')
const onCreated = (key: string) => {
  plaintext.value = key
  showPlaintext.value = true
  fetchList()
}

const copy = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.error('复制失败，请手动选择复制')
  }
}

const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.confirm(
      `确认吊销密钥「${id}」？此操作不可逆，关联的程序化调用将立即失效。`,
      '吊销密钥',
      { confirmButtonText: '确认吊销', cancelButtonText: '取消', type: 'warning' }
    )
  } catch {
    return
  }
  try {
    await deleteApiKey(id)
    ElMessage.success('已吊销')
    fetchList()
  } catch {
    // 拦截器提示
  }
}

onMounted(async () => {
  fetchList()
  try {
    tenants.value = await fetchAllTenants()
  } catch {
    // 静默
  }
})
</script>
