<template>
  <SearchTable
    title="审计日志（跨租户，后端过滤 + 分页）"
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
        placeholder="关键字（资源 ID / 详情）"
        clearable
        style="width: 200px"
        @keyup.enter="handleSearch"
      />
      <el-input
        v-model="searchForm.actor"
        placeholder="操作人（精确）"
        clearable
        style="width: 150px"
        @keyup.enter="handleSearch"
      />
      <el-input
        v-model="searchForm.action"
        placeholder="动作（精确，如 admin:delete）"
        clearable
        style="width: 210px"
        @keyup.enter="handleSearch"
      />
      <el-input
        v-model="searchForm.resourceType"
        placeholder="资源类型（精确）"
        clearable
        style="width: 150px"
        @keyup.enter="handleSearch"
      />
      <el-date-picker
        v-model="range"
        type="datetimerange"
        start-placeholder="起始时间"
        end-placeholder="截止时间"
        value-format="YYYY-MM-DDTHH:mm:ssZ"
        style="width: 340px"
        @change="handleSearch"
      />
    </template>

    <template #actions>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      <el-tooltip content="导出当前过滤条件的最新 500 条（CSV，敏感字段已脱敏）">
        <el-button :loading="exporting" @click="handleExport">导出 CSV</el-button>
      </el-tooltip>
    </template>

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

    <template #col-action="{ row }">
      <el-tag size="small" :type="String(row.action).startsWith('admin:') ? 'warning' : 'info'">{{ row.action }}</el-tag>
    </template>
    <template #col-detail="{ row }">
      <el-popover v-if="row.detail" placement="left" width="420" trigger="click">
        <template #reference>
          <el-button link type="primary" size="small">查看详情</el-button>
        </template>
        <pre class="detail-view">{{ row.detail }}</pre>
      </el-popover>
      <span v-else class="dim">-</span>
    </template>
  </SearchTable>
</template>

<script lang="ts" setup>
// 审计日志（D4）：过滤与分页全部走服务端（不在浏览器下载全量再过滤，千行后原实现失效）。
// 导出为当前过滤条件的前 500 条 CSV，敏感字段走 maskAuditDetail（token/明文凭证脱敏）。
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchAuditLogPage, type AdminAuditLog } from '../api'

const scopeStore = useScopeStore()
const range = ref<[string, string] | null>(null)
const exporting = ref(false)

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminAuditLog>({
    fetch: (params) => {
      const [from, to] = range.value ?? ['', '']
      return fetchAuditLogPage({ ...params, tenantId: scopeStore.tenantId, from, to })
    },
    defaultSearchForm: { keyword: '', actor: '', action: '', resourceType: '' },
    pageSize: 50
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

// CSV 下载（本地实现：仅本页用，不进 lib 公共层）。带 UTF-8 BOM 防 Excel 乱码。
function downloadCsv(content: string, filename: string) {
  const blob = new Blob(['\ufeff' + content], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'actor', label: '操作人', width: 140 },
  { prop: 'action', label: '动作', width: 170, slot: 'action' },
  { prop: 'resourceType', label: '资源类型', width: 120 },
  { prop: 'resourceId', label: '资源 ID', minWidth: 150 },
  { prop: 'detail', label: '详情', width: 100, slot: 'detail' },
  { prop: 'at', label: '时间', width: 180, formatter: tableTimeFormatter }
])

// 审计详情可能含 token/凭证片段——导出前脱敏（前缀保留便于排障，值本身不可用）。
function maskAuditDetail(detail = '') {
  return detail
    .replace(/(sk-[A-Za-z0-9_-]{4})[A-Za-z0-9_-]+/g, '$1…')
    .replace(/((?:token|secret|password|apikey|api_key)\s*[=:]\s*)\S+/gi, '$1••••••')
}

async function handleExport() {
  exporting.value = true
  try {
    const [from, to] = range.value ?? ['', '']
    const page = await fetchAuditLogPage({
      ...searchForm,
      tenantId: scopeStore.tenantId,
      from,
      to,
      page: 1,
      size: 500
    })
    const header = ['时间', '租户', '操作人', '动作', '资源类型', '资源 ID', '详情']
    const esc = (v: string) => `"${(v ?? '').replace(/"/g, '""')}"`
    const rows = page.records.map((l) =>
      [l.at, l.tenantId, l.actor, l.action, l.resourceType, l.resourceId, maskAuditDetail(l.detail)].map(esc).join(',')
    )
    downloadCsv([header.join(','), ...rows].join('\n'), `audit-logs-${new Date().toISOString().slice(0, 19)}.csv`)
    ElMessage.success(`已导出 ${page.records.length} 条（共 ${page.total} 条匹配，单次上限 500）`)
  } catch (e) {
    ElMessage.error('导出失败：' + ((e as Error).message || '未知错误'))
  } finally {
    exporting.value = false
  }
}

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
useScopeChange(() => fetchList())

onMounted(() => fetchList())
</script>

<style scoped>
.detail-view {
  margin: 0;
  max-height: 320px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
.dim {
  color: var(--el-text-color-placeholder);
}
</style>
