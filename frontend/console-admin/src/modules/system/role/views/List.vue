<template>
  <div>
    <el-alert
      type="info"
      :closable="false"
      show-icon
      title="系统内置角色为只读"
      description="平台预置 tenant-admin / developer / viewer 三个角色及其权限集合，不可新建、编辑或删除。"
      style="margin-bottom: 12px"
    />
    <SearchTable
    :title="t('role.title')"
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
        :placeholder="t('role.searchKeyword')"
        clearable
        style="width: 220px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button
        :icon="Refresh"
        @click="fetchList"
      >
        {{ t('common.action.refresh') }}
      </el-button>
    </template>

    <template #col-status="{ row }">
      <StatusTag :status="row.status" />
    </template>

    <template #col-actions="{ row }">
      <el-button
        link
        type="primary"
        size="small"
        @click="openDrawer('view', row)"
      >
        {{ t('common.action.view') }}
      </el-button>
    </template>
  </SearchTable>

  <RoleFormDrawer
    v-model="drawerVisible"
    :mode="drawerMode"
    :data="editingRow"
  />
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable, StatusTag } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { t } from '@/lib/i18n'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchRoleList,
  type RoleInfo,
  type RoleSearchRequest,
} from '../../role/api'
import RoleFormDrawer from './RoleFormDrawer.vue'

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange,
} = useCrud<RoleInfo>({
  fetch: (params) => fetchRoleList(params as unknown as RoleSearchRequest),
  defaultSearchForm: { keyword: '', status: '' },
  pageSize: 10,
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'name', label: t('role.field.name'), minWidth: 140 },
  { prop: 'code', label: t('role.field.code'), minWidth: 140 },
  { prop: 'description', label: t('common.column.description'), minWidth: 240 },
  { prop: 'status', label: t('common.column.status'), minWidth: 90, slot: 'status' },
  { prop: 'actions', label: t('common.column.actions'), width: 100, fixed: 'right', slot: 'actions' },
])

const drawerVisible = ref(false)
const drawerMode = ref<'add' | 'edit' | 'view'>('view')
const editingRow = ref<RoleInfo | null>(null)

const openDrawer = (mode: 'add' | 'edit' | 'view', row?: RoleInfo) => {
  drawerMode.value = mode
  editingRow.value = row ?? null
  drawerVisible.value = true
}

onMounted(fetchList)
</script>
