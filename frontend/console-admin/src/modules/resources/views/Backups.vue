<template>
  <div class="backups">
    <!-- 语义边界（防止把「记录已创建」或「校验和已写」读成「恢复可用/恢复成功」） -->
    <el-alert type="info" :closable="false" show-icon class="semantic-note">
      <template #title>
        记录模式（record）只登记备份意图，无产物、不可恢复；快照（snapshot）的「校验时间」是校验和记录时间，
        <b>不等于恢复演练通过</b>；恢复是否生效看 restore 记录自身的状态（accepted → running → completed | failed）。
      </template>
    </el-alert>

    <SearchTable
      title="数据服务备份/恢复（跨租户）"
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
          placeholder="搜索 ID / 资源 / 租户"
          clearable
          style="width: 200px"
          @keyup.enter="handleSearch"
        />
        <el-select v-model="searchForm.mode" placeholder="模式" clearable style="width: 140px" @change="handleSearch">
          <el-option label="记录（无产物）" value="record" />
          <el-option label="真实快照" value="snapshot" />
          <el-option label="恢复作业" value="restore" />
        </el-select>
        <el-select v-model="searchForm.status" placeholder="状态" clearable style="width: 130px" @change="handleSearch">
          <el-option v-for="s in STATUSES" :key="s" :label="s" :value="s" />
        </el-select>
        <el-input
          v-model="searchForm.resourceId"
          placeholder="数据服务资源 ID（精确）"
          clearable
          style="width: 210px"
          @keyup.enter="handleSearch"
        />
      </template>

      <template #actions>
        <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      </template>

      <template #col-tenantId="{ row }">
        <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
      </template>
      <template #col-mode="{ row }">
        <el-tag size="small" :type="modeType(row)">{{ modeLabel(row) }}</el-tag>
      </template>
      <template #col-status="{ row }">
        <el-tag size="small" :type="statusType(String(row.status))">{{ row.status }}</el-tag>
      </template>
      <template #col-artifact="{ row }">
        <el-tooltip v-if="row.artifactRef" :content="row.artifactRef" placement="top" :show-after="300">
          <span class="mono">{{ shortRef(String(row.artifactRef)) }}</span>
        </el-tooltip>
        <span v-else class="dim">无产物</span>
      </template>
      <template #col-size="{ row }">
        <span class="mono">{{ sizeText(row) }}</span>
      </template>
      <template #col-restorable="{ row }">
        <el-tag size="small" :type="row.restorable ? 'success' : 'info'">{{ row.restorable ? '可恢复' : '不可恢复' }}</el-tag>
      </template>
      <template #col-actions="{ row }">
        <el-button link type="primary" size="small" @click="openDetail(row)">详情</el-button>
      </template>
    </SearchTable>

    <el-drawer v-model="drawerVisible" title="备份/恢复详情" size="560px">
      <template v-if="detail">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="记录 ID">
            <span class="mono">{{ detail.id }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="数据服务">{{ detail.resourceId }}</el-descriptions-item>
          <el-descriptions-item label="模式">{{ modeLabel(detail) }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag size="small" :type="statusType(String(detail.status))">{{ detail.status }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ fmtTime(detail.createdAt) }}</el-descriptions-item>
          <el-descriptions-item label="结束时间">{{ fmtTime(detail.finishedAt) }}</el-descriptions-item>
          <el-descriptions-item label="引擎">{{ detail.engine || '—' }}</el-descriptions-item>
          <el-descriptions-item label="产物">{{ detail.artifactRef || '无产物（记录模式）' }}</el-descriptions-item>
          <el-descriptions-item label="产物大小">{{ sizeText(detail) }}</el-descriptions-item>
          <el-descriptions-item label="校验和">
            <span class="mono break">{{ detail.checksum || '—' }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="校验时间">
            <span v-if="detail.verifiedAt">{{ fmtTime(detail.verifiedAt) }}</span>
            <span v-else>—</span>
            <div class="hint">校验和已记录 ≠ 恢复演练通过</div>
          </el-descriptions-item>
          <el-descriptions-item label="可恢复">{{ detail.restorable ? '是（平台会受理恢复）' : '否' }}</el-descriptions-item>
          <el-descriptions-item v-if="detail.error" label="错误">
            <span class="err">{{ detail.error }}</span>
          </el-descriptions-item>
        </el-descriptions>

        <template v-if="detail.mode === 'restore'">
          <div class="section-title">恢复作业链</div>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="源快照">
              <span class="mono">{{ detail.sourceBackupId || '—' }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="新实例">
              <span class="mono">{{ detail.restoredResourceId || '—' }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="实例名">{{ detail.restoreName || '—' }}</el-descriptions-item>
          </el-descriptions>
          <el-alert
            v-if="!detail.finishedAt"
            type="warning"
            :closable="false"
            show-icon
            class="section-note"
            title="恢复作业尚未结束，正在由 Pacer 推进；当前状态不代表恢复成功。"
          />
        </template>

        <el-alert
          v-if="detail.mode === 'record' || !detail.mode"
          type="info"
          :closable="false"
          show-icon
          class="section-note"
          title="记录模式无产物与恢复证明：该条记录只能证明「备份意图已登记」，不能用于恢复。"
        />
      </template>
    </el-drawer>
  </div>
</template>

<script lang="ts" setup>
// 数据服务备份/恢复跨租户总览（D4）。只读：平台总览不代租户发起备份/恢复
// （跨租户写越权风险高，操作仍走 console-user 租户内入口）。
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchBackupList, type AdminBackup, type BackupSearchRequest } from '../api'

const STATUSES = ['accepted', 'running', 'verifying', 'completed', 'failed']
const scopeStore = useScopeStore()

const drawerVisible = ref(false)
const detail = ref<AdminBackup>()

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminBackup>({
    fetch: (params) =>
      fetchBackupList({ ...params, tenantId: scopeStore.tenantId } as unknown as BackupSearchRequest),
    defaultSearchForm: { keyword: '', mode: '', status: '', resourceId: '', tenantId: '' },
    pageSize: 20
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 120, slot: 'tenantId' },
  { prop: 'id', label: '记录 ID', minWidth: 200 },
  { prop: 'resourceId', label: '数据服务', minWidth: 150 },
  { prop: 'mode', label: '模式', width: 110, slot: 'mode' },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'artifactRef', label: '产物', minWidth: 160, slot: 'artifact' },
  { prop: 'sizeBytes', label: '大小', width: 100, slot: 'size' },
  { prop: 'restorable', label: '可恢复', width: 100, slot: 'restorable' },
  { prop: 'createdAt', label: '创建时间', width: 170, formatter: tableTimeFormatter }
])

const modeLabel = (b: Pick<AdminBackup, 'mode' | 'type'>) => {
  const m = b.mode || 'record'
  return m === 'snapshot' ? '真实快照' : m === 'restore' ? '恢复作业' : '记录（无产物）'
}
const modeType = (b: Pick<AdminBackup, 'mode' | 'type'>) => {
  const m = b.mode || 'record'
  return m === 'snapshot' ? 'success' : m === 'restore' ? 'warning' : 'info'
}
const statusType = (s: string) =>
  ({ completed: 'success', failed: 'danger', running: 'primary', accepted: 'info', verifying: 'warning' } as Record<string, string>)[s] ??
  'info'
const sizeText = (b: AdminBackup) =>
  b.sizeBytes ? `${(b.sizeBytes / 1024 / 1024).toFixed(2)} MiB` : b.sizeMB ? `${b.sizeMB} MB（历史模拟值）` : '—'
const shortRef = (ref: string) => (ref.length > 28 ? `${ref.slice(0, 28)}…` : ref)
const fmtTime = (t?: string) => (t ? tableTimeFormatter(undefined, undefined, t) : '—')

function openDetail(row: Record<string, unknown>) {
  detail.value = row as unknown as AdminBackup
  drawerVisible.value = true
}

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
useScopeChange(() => fetchList(), () => {
  drawerVisible.value = false
})

onMounted(() => fetchList())
</script>

<style scoped>
.semantic-note {
  margin-bottom: 12px;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.break {
  word-break: break-all;
}
.dim {
  color: var(--el-text-color-placeholder);
}
.err {
  color: var(--el-color-danger);
}
.hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.section-title {
  margin: 16px 0 8px;
  font-weight: 600;
}
.section-note {
  margin-top: 12px;
}
</style>
