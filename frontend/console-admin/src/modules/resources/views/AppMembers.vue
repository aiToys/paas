<template>
  <SearchTable
    title="应用成员（跨租户：应用级权限观测）"
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
        placeholder="搜索应用 / 用户"
        clearable
        style="width: 220px"
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

    <template #col-role="{ row }">
      <el-tag :type="roleType(row.role)" size="small">{{ ROLE_LABEL[row.role] || row.role }}</el-tag>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
// 应用成员跨租户总览（super_admin 只读）：观测各租户应用的成员角色分布（应用级权限运营面）。
// 成员/角色的增删在 console-user 租户内自助完成（owner 治理），此处仅观测。
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchAppMemberList, fetchAppList, type AdminAppMember, type AdminApplication, type ResSearchRequest } from '../api'

// 应用名索引（appId -> name，跨租户展示友好）
const appName = ref<Record<string, string>>({})

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminAppMember>({
    fetch: (params) => loadMembers(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

async function loadMembers(params: ResSearchRequest) {
  try {
    const [members, apps] = await Promise.all([fetchAppMemberList(), fetchAppList({ page: 1, size: 1000 } as ResSearchRequest)])
    const nameMap: Record<string, string> = {}
    for (const a of (apps as unknown as AdminApplication[])) nameMap[a.id] = a.name
    appName.value = nameMap
    const kw = (params.keyword ?? '').toLowerCase()
    const filtered = members.filter(
      (m) =>
        (!params.tenantId || m.tenantId === params.tenantId) &&
        (!kw ||
          m.appId.toLowerCase().includes(kw) ||
          (nameMap[m.appId] ?? '').toLowerCase().includes(kw) ||
          m.userId.toLowerCase().includes(kw) ||
          (m.userName ?? '').toLowerCase().includes(kw))
    )
    const page = params.page ?? 1
    const size = params.size ?? 10
    return { records: filtered.slice((page - 1) * size, page * size), total: filtered.length, current: page, size }
  } catch (e) {
    ElMessage.error('加载应用成员失败：' + (e as Error).message)
    return { records: [], total: 0, current: params.page ?? 1, size: params.size ?? 10 }
  }
}

const ROLE_LABEL: Record<string, string> = {
  'app-owner': '所有者',
  'app-maintainer': '维护者',
  'app-developer': '开发者',
  'app-viewer': '只读',
}
const roleType = (r: string) =>
  (({ 'app-owner': 'danger', 'app-maintainer': 'warning', 'app-developer': 'primary', 'app-viewer': 'info' }) as Record<string, string>)[r] ?? 'info'

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])
const appLabel = (id: string) => appName.value[id] ? `${appName.value[id]}（${id}）` : id

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'appId', label: '应用', minWidth: 200, formatter: (row) => appLabel(String(row.appId ?? '')) },
  { prop: 'userName', label: '用户', minWidth: 150, formatter: (row) => String(row.userName || row.userId || '') },
  { prop: 'userId', label: '用户 ID', minWidth: 140 },
  { prop: 'role', label: '应用角色', width: 110, slot: 'role' },
])

onMounted(() => fetchList())
</script>
