<template>
  <SearchTable
    title="月度预算（费用上限：估算口径，不是结算账单）"
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
        placeholder="搜索租户 / 应用"
        clearable
        style="width: 220px"
        @keyup.enter="handleSearch"
      />
      <el-date-picker
        v-model="period"
        type="month"
        value-format="YYYY-MM"
        placeholder="账期"
        :clearable="false"
        style="width: 140px; margin-left: 8px"
        @change="fetchList"
      />
    </template>

    <template #actions>
      <el-button
        type="primary"
        :icon="Plus"
        @click="openCreate"
      >
        新建预算
      </el-button>
      <el-button
        :icon="Refresh"
        @click="fetchList"
      >
        刷新
      </el-button>
    </template>

    <template #col-tenant="{ row }">
      <el-tag
        size="small"
        type="info"
      >
        {{ row.tenantId }}
      </el-tag>
    </template>

    <template #col-scope="{ row }">
      <el-tag
        size="small"
        :type="row.scope === 'app' ? 'warning' : 'info'"
      >
        {{ row.scope === 'app' ? '应用' : '租户' }}
      </el-tag>
      <span
        v-if="row.scope === 'app'"
        class="muted"
      >{{ row.appId }}</span>
    </template>

    <template #col-usage="{ row }">
      <div class="usage-cell">
        <span>¥{{ Number(row.estimatedCost).toFixed(2) }} / ¥{{ Number(row.monthlyAmount).toFixed(2) }}</span>
        <el-progress
          :percentage="Math.min(Number(row.ratio) * 100, 100)"
          :status="progressStatus(row.state)"
          :stroke-width="6"
          :show-text="false"
        />
        <span class="muted">{{ (Number(row.ratio) * 100).toFixed(1) }}% · {{ formatTokens(row.tokens) }} token</span>
      </div>
    </template>

    <template #col-state="{ row }">
      <el-tag
        size="small"
        :type="stateType(row.state)"
      >
        {{ stateLabel(row.state) }}
      </el-tag>
      <el-tooltip
        v-if="row.unpricedModels?.length"
        content="这些模型在目录中未定价，成本按 0 计（token 仍计入用量）"
      >
        <el-tag
          size="small"
          type="info"
          style="margin-left: 4px"
        >
          {{ row.unpricedModels.length }} 个未定价
        </el-tag>
      </el-tooltip>
    </template>

    <template #col-enforce="{ row }">
      <el-tag
        size="small"
        :type="row.enforce ? 'danger' : 'info'"
      >
        {{ row.enforce ? '超限拒绝推理' : '仅展示' }}
      </el-tag>
    </template>

    <template #col-action="{ row }">
      <el-button
        type="primary"
        link
        size="small"
        @click="openEdit(row)"
      >
        编辑
      </el-button>
      <el-button
        type="danger"
        link
        size="small"
        @click="doDelete(row)"
      >
        删除
      </el-button>
    </template>

    <template #footer>
      <div class="basis-note">
        <el-icon><InfoFilled /></el-icon>
        <span>{{ costBasis }}</span>
      </div>
    </template>
  </SearchTable>

  <el-dialog
    v-model="dialogVisible"
    :title="editing ? '编辑预算' : '新建预算'"
    width="520px"
  >
    <el-form
      label-width="120px"
      size="small"
    >
      <el-form-item
        label="租户"
        required
      >
        <el-input
          v-model="form.tenantId"
          :disabled="editing"
          placeholder="目标租户 ID"
        />
      </el-form-item>
      <el-form-item
        label="范围"
        required
      >
        <el-radio-group
          v-model="form.scope"
          :disabled="editing"
        >
          <el-radio value="tenant">
            整个租户
          </el-radio>
          <el-radio value="app">
            单个应用
          </el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item
        v-if="form.scope === 'app'"
        label="应用 ID"
        required
      >
        <el-input
          v-model="form.appId"
          :disabled="editing"
          placeholder="应用 ID"
        />
      </el-form-item>
      <el-form-item
        label="月上限（元）"
        required
      >
        <el-input-number
          v-model="form.monthlyAmount"
          :min="0.01"
          :precision="2"
          :step="100"
        />
      </el-form-item>
      <el-form-item label="预警阈值">
        <el-input-number
          v-model="form.warnRatio"
          :min="0.1"
          :max="1"
          :step="0.05"
          :precision="2"
        />
        <span class="muted">达到该比例转「预警」</span>
      </el-form-item>
      <el-form-item label="超限强制">
        <el-switch v-model="form.enforce" />
        <span class="muted">开启后当期估算费用达上限即拒绝推理（429）</span>
      </el-form-item>
      <el-form-item label="备注">
        <el-input
          v-model="form.note"
          type="textarea"
          :rows="2"
        />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="dialogVisible = false">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="saving"
        @click="doSave"
      >
        保存
      </el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { InfoFilled, Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  createBudget,
  deleteBudget,
  fetchBudgetOverview,
  updateBudget,
  type BudgetState,
  type BudgetStatusRow
} from '../api'

const scopeStore = useScopeStore()
const period = ref(currentPeriod())

function currentPeriod(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

const costBasis = ref('估算：按模型输出单价 × token 计算；未定价模型成本按 0 计')

// 服务端返回「预算 + 当期状态」的扁平行（BudgetStatus 内嵌 budget），表格按扁平字段渲染。
type Row = BudgetStatusRow & {
  id: string
  tenantId: string
  scope: 'tenant' | 'app'
  appId?: string
  monthlyAmount: number
  enforce: boolean
}

const flatten = (r: BudgetStatusRow): Row => ({
  ...r,
  id: r.budget.id,
  tenantId: r.budget.tenantId,
  scope: r.budget.scope,
  appId: r.budget.appId,
  monthlyAmount: r.budget.monthlyAmount,
  enforce: r.budget.enforce
})

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<Row>({
    fetch: async (params) => {
      const ov = await fetchBudgetOverview(scopeStore.tenantId, period.value)
      costBasis.value = ov.costBasis || costBasis.value
      const rows = ov.rows.map(flatten)
      const keyword = String(params.keyword ?? '').toLowerCase()
      const filtered = rows.filter(
        (r) =>
          (!scopeStore.tenantId || r.tenantId === scopeStore.tenantId) &&
          (!keyword ||
            r.tenantId.toLowerCase().includes(keyword) ||
            (r.appId ?? '').toLowerCase().includes(keyword))
      )
      const page = Number(params.page)
      const size = Number(params.size)
      const start = (page - 1) * size
      return {
        records: filtered.slice(start, start + size),
        total: filtered.length,
        current: page,
        size
      }
    },
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 150, slot: 'tenant' },
  { prop: 'scope', label: '范围', width: 190, slot: 'scope' },
  { prop: 'usage', label: `当期费用（${period.value}）`, minWidth: 240, slot: 'usage' },
  { prop: 'state', label: '状态', width: 160, slot: 'state' },
  { prop: 'enforce', label: '超限行为', width: 130, slot: 'enforce' },
  { prop: 'action', label: '操作', width: 130, slot: 'action', hideable: false }
])

const stateType = (s: BudgetState) => (s === 'exceeded' ? 'danger' : s === 'warn' ? 'warning' : 'success')
const stateLabel = (s: BudgetState) => (s === 'exceeded' ? '已超限' : s === 'warn' ? '预警' : '正常')
const progressStatus = (s: BudgetState) => (s === 'exceeded' ? 'exception' : s === 'warn' ? 'warning' : 'success')
const formatTokens = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(2)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}K` : String(n))

const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const form = ref({
  id: '',
  tenantId: '',
  scope: 'tenant' as 'tenant' | 'app',
  appId: '',
  monthlyAmount: 100,
  warnRatio: 0.8,
  enforce: false,
  note: ''
})

const openCreate = () => {
  editing.value = false
  form.value = {
    id: '',
    tenantId: scopeStore.tenantId || '',
    scope: 'tenant',
    appId: '',
    monthlyAmount: 100,
    warnRatio: 0.8,
    enforce: false,
    note: ''
  }
  dialogVisible.value = true
}

const openEdit = (row: Record<string, unknown>) => {
  const r = row as unknown as Row
  editing.value = true
  form.value = {
    id: r.id,
    tenantId: r.tenantId,
    scope: r.scope,
    appId: r.appId ?? '',
    monthlyAmount: r.monthlyAmount,
    warnRatio: r.budget.warnRatio ?? 0.8,
    enforce: r.enforce,
    note: r.budget.note ?? ''
  }
  dialogVisible.value = true
}

const doSave = async () => {
  if (!form.value.tenantId.trim()) {
    ElMessage.warning('请填写租户 ID')
    return
  }
  if (form.value.scope === 'app' && !form.value.appId.trim()) {
    ElMessage.warning('应用级预算必须填写应用 ID')
    return
  }
  saving.value = true
  try {
    const body = {
      scope: form.value.scope,
      appId: form.value.scope === 'app' ? form.value.appId.trim() : '',
      monthlyAmount: form.value.monthlyAmount,
      warnRatio: form.value.warnRatio,
      enforce: form.value.enforce,
      note: form.value.note
    }
    if (editing.value) {
      // 归属字段（租户/范围/应用）不可变：后端以库内为准
      await updateBudget(form.value.id, body)
    } else {
      await createBudget({ ...body, tenantId: form.value.tenantId.trim() })
    }
    ElMessage.success('已保存')
    dialogVisible.value = false
    fetchList()
  } finally {
    saving.value = false
  }
}

const doDelete = async (row: Record<string, unknown>) => {
  const r = row as unknown as Row
  try {
    await ElMessageBox.confirm(
      `确认删除租户 ${r.tenantId} 的${r.scope === 'app' ? `应用 ${r.appId} ` : ''}预算？删除后该范围不再受预算约束。`,
      '删除预算',
      { type: 'warning' }
    )
  } catch {
    return
  }
  await deleteBudget(r.id)
  ElMessage.success('已删除')
  fetchList()
}

// 浏览范围（顶栏）联动：切租户重拉列表（范围是纯过滤器，不改写目标租户）
useScopeChange(() => fetchList())

fetchList()
</script>

<style scoped>
.usage-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.muted {
  margin-left: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.basis-note {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
