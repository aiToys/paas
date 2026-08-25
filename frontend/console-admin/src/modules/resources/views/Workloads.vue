<template>
  <SearchTable
    title="工作负载管理（跨租户：详情 / 运维）"
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
        placeholder="搜索 ID / 名称 / 类型"
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
      <el-button :loading="reconciling" @click="doReconcile">Drift 修复</el-button>
    </template>

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

        <template #col-type="{ row }">
      <el-tag size="small" type="info">{{ typeLabel(row.type) }}</el-tag>
    </template>
    <template #col-status="{ row }">
      <el-tag :type="statusType(row.status)" size="small">{{ row.status || '-' }}</el-tag>
    </template>
    <template #col-replicas="{ row }">
      <span class="mono">{{ row.ready ?? 0 }} / {{ row.replicas ?? 0 }}</span>
    </template>
    <template #col-detail="{ row }">
      <el-button type="primary" link size="small" @click="openDetail(row)">详情 / 运维</el-button>
    </template>
  </SearchTable>

  <WorkloadDrawer v-model="detailVisible" :id="detailId" @refresh="fetchList" />
</template>

<script lang="ts" setup>
import { computed, ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchWorkloadList, reconcileWorkloads, type AdminWorkload, type ResSearchRequest } from '../api'
import WorkloadDrawer from './WorkloadDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminWorkload>({
    fetch: (params) => fetchWorkloadList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const reconciling = ref(false)
// drift 修复：PG 有行无 CRD 的 Workload 补投影（后端返回各分类计数）。
const doReconcile = async () => {
  try {
    await ElMessageBox.confirm(
      '将扫描全部租户工作负载，对「数据库有记录但 K8s 缺 CRD」的补投影（drift 修复）。继续？',
      'Drift 修复',
      { type: 'warning', confirmButtonText: '执行' }
    )
  } catch {
    return
  }
  reconciling.value = true
  try {
    const res = await reconcileWorkloads()
    ElMessage.success(`Drift 修复完成：${JSON.stringify(res)}`)
    fetchList()
  } finally {
    reconciling.value = false
  }
}

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '工作负载 ID', minWidth: 150 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'type', label: '类型', width: 100, slot: 'type' },
  { prop: 'appId', label: '所属应用', minWidth: 130 },
  { prop: 'replicas', label: '就绪/期望', width: 110, slot: 'replicas' },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'detail', label: '操作', width: 120, slot: 'detail', hideable: false }
])

const detailVisible = ref(false)
const detailId = ref('')
const openDetail = (row: AdminWorkload) => {
  detailId.value = row.id
  detailVisible.value = true
}

const typeLabel = (t: string) => (({ service: '服务', job: '任务', cronjob: '定时' }) as Record<string, string>)[t] ?? t
const statusType = (s: string) =>
  (({ running: 'success', deploying: 'warning', stopped: 'info', failed: 'danger' }) as Record<string, string>)[s] ??
  'info'

onMounted(() => fetchList())
</script>

<style scoped>
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 13px;
}
</style>
