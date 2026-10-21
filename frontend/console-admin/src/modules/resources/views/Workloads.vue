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
    </template>

    <template #actions>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      <el-button :loading="previewing" @click="openReconcilePreview">Drift 对账</el-button>
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

  <WorkloadDrawer
    :key="detailId"
    v-model="detailVisible"
    :id="detailId"
    @refresh="fetchList"
  />

  <!-- Drift 两段式：先只读预览「将补建哪些」，确认后才执行 -->
  <el-dialog v-model="previewVisible" title="Drift 对账预览（只读）" width="760px">
    <template v-if="preview">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        title="本次为只读预览：未修改任何 K8s 资源，确认后才执行补建。"
        description="只处理「平台有记录、K8s 缺 CRD」的条目；补建为仅创建不覆盖，不会改动既有运行态。"
      />
      <div class="reconcile-stats">
        <span>扫描 <b>{{ preview.scanned }}</b></span>
        <span>已存在 <b>{{ preview.existing ?? 0 }}</b></span>
        <span :class="{ warn: (preview.missingCount ?? 0) > 0 }">将补建 <b>{{ preview.missingCount ?? 0 }}</b></span>
        <span v-if="preview.failed" class="warn">探测失败 <b>{{ preview.failed }}</b></span>
      </div>
      <el-alert
        v-if="preview.failed"
        type="warning"
        :closable="false"
        show-icon
        class="reconcile-alert"
        :title="`${preview.failed} 条探测失败（权限/连接）——已排除在补建清单外`"
        :description="`失败 ID：${(preview.failedIds ?? []).join('、') || '（未列出）'}`"
      />
      <el-table
        v-if="(preview.missingCount ?? 0) > 0"
        :data="preview.missing ?? []"
        size="small"
        max-height="300"
        border
      >
        <el-table-column prop="tenantId" label="租户" width="140" />
        <el-table-column prop="id" label="工作负载 ID" min-width="170" />
        <el-table-column prop="name" label="名称" min-width="150" />
      </el-table>
      <el-empty v-else description="没有缺失的 CRD，无需补建" :image-size="72" />
      <p v-if="preview.truncated" class="reconcile-note">
        清单仅显示前 {{ preview.missing?.length ?? 0 }} 条（共 {{ preview.missingCount }} 条），执行时会处理全部。
      </p>
    </template>
    <template #footer>
      <el-button @click="previewVisible = false">取消</el-button>
      <el-button
        type="primary"
        :loading="reconciling"
        :disabled="!preview || (preview.missingCount ?? 0) === 0"
        @click="doReconcile"
      >
        执行补建{{ preview?.missingCount ? `（${preview.missingCount}）` : '' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { computed, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange, useResourceFocus } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchWorkloadList, reconcileWorkloads, type AdminWorkload, type ReconcileResult, type ResSearchRequest } from '../api'
import WorkloadDrawer from './WorkloadDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminWorkload>({
    fetch: (params) => fetchWorkloadList({ ...params, tenantId: scopeStore.tenantId } as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const previewVisible = ref(false)
const previewing = ref(false)
const reconciling = ref(false)
const preview = ref<ReconcileResult | null>(null)

// 第一步：只读预览（后端 dryRun=true 不写数据面、不记执行审计）。
const openReconcilePreview = async () => {
  previewing.value = true
  try {
    preview.value = await reconcileWorkloads(true)
    previewVisible.value = true
  } finally {
    previewing.value = false
  }
}

// 第二步：确认后执行补建（独立动作，不在预览里顺手写）。
const doReconcile = async () => {
  reconciling.value = true
  try {
    const res = await reconcileWorkloads(false)
    const extra = res.failed ? `，失败 ${res.failed} 条` : ''
    ElMessage.success(`补建 ${res.created} 条，跳过 ${res.skipped} 条${extra}`)
    previewVisible.value = false
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
const focusDetail = useResourceFocus({ path: '/resources/workloads', visible: detailVisible, id: detailId })
const openDetail = (row: AdminWorkload) => focusDetail(row.id, { envId: row.envId, appId: row.appId })

const typeLabel = (t: string) => (({ service: '服务', job: '任务', cronjob: '定时' }) as Record<string, string>)[t] ?? t
const statusType = (s: string) =>
  (({ running: 'success', deploying: 'warning', stopped: 'info', failed: 'danger' }) as Record<string, string>)[s] ??
  'info'

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
const scopeStore = useScopeStore()
useScopeChange(() => fetchList(), () => {
  detailVisible.value = false
  detailId.value = ''
})

onMounted(() => fetchList())
</script>

<style scoped>
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 13px;
}

.reconcile-stats {
  display: flex;
  gap: 24px;
  margin: 16px 0;
  color: var(--el-text-color-regular);
  font-size: 14px;
}

.reconcile-stats b {
  font-family: var(--font-mono, monospace);
  font-size: 16px;
  color: var(--el-text-color-primary);
}

.reconcile-stats .warn b {
  color: var(--el-color-warning);
}

.reconcile-alert {
  margin-bottom: 12px;
}

.reconcile-note {
  margin: 12px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
