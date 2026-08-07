<template>
  <SearchTable
    title="配额总览（跨租户：调整配额）"
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
        placeholder="搜索租户 ID"
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
    </template>

    <template #col-limits="{ row }">
      <span
        v-for="[k, v] in Object.entries((row.limits || {}) as Record<string, number>)"
        :key="k"
        style="display: inline-block; margin: 2px 4px 2px 0"
      >
        <el-tag size="small" type="info">{{ k }}: {{ v === -1 ? '∞' : v }}</el-tag>
      </span>
    </template>
    <template #col-action="{ row }">
      <el-button type="primary" link size="small" @click="openAdjust(row)">调整</el-button>
    </template>
  </SearchTable>

  <!-- 配额调整弹窗 -->
  <el-dialog v-model="adjustVisible" :title="`调整配额（${adjustTenant}）`" width="520px">
    <el-form label-width="140px" size="small">
      <el-form-item v-for="key in resourceKeys" :key="key" :label="key">
        <el-input-number v-model="adjustForm[key]" :min="-1" :step="1" />
        <span style="margin-left: 8px; color: var(--el-text-color-secondary)">-1 = 无限</span>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="adjustVisible = false">取消</el-button>
      <el-button type="primary" :loading="adjusting" @click="doAdjust">保存</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchQuotaList, setQuotaForTenant, type AdminQuota, type ResSearchRequest } from '../api'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminQuota>({
    fetch: (params) => fetchQuotaList(params as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 160 },
  { prop: 'limits', label: '配额上限', minWidth: 400, slot: 'limits' },
  { prop: 'updatedAt', label: '更新时间', width: 180 },
  { prop: 'action', label: '操作', width: 90, slot: 'action', hideable: false }
])

// 6 资源维度（与 billing.ResXxx 对齐）。
const resourceKeys = ['applications', 'workloads', 'models', 'gpu', 'tokens', 'storage_gb']

const adjustVisible = ref(false)
const adjustTenant = ref('')
const adjustForm = ref<Record<string, number>>({})
const adjusting = ref(false)

const openAdjust = (row: AdminQuota) => {
  adjustTenant.value = row.tenantId
  const form: Record<string, number> = {}
  for (const k of resourceKeys) {
    form[k] = row.limits?.[k] ?? -1
  }
  adjustForm.value = form
  adjustVisible.value = true
}

const doAdjust = async () => {
  adjusting.value = true
  try {
    await setQuotaForTenant({ tenantId: adjustTenant.value, limits: adjustForm.value })
    ElMessage.success('配额已更新')
    adjustVisible.value = false
    fetchList()
  } finally {
    adjusting.value = false
  }
}

fetchList()
</script>
