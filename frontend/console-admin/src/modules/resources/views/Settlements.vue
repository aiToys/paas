<template>
  <div class="settlement-page">
    <!-- 汇总卡：平台估算 vs 人工登记 vs 差异（三个数都来自服务端，前端不做二次推算） -->
    <el-card
      shadow="never"
      class="summary-card"
    >
      <div class="summary-row">
        <div class="summary-item">
          <div class="summary-label">
            平台估算（{{ period }}）
          </div>
          <div class="summary-value">
            ¥{{ Number(overview.estimatedTotal).toFixed(2) }}
          </div>
          <div class="summary-hint">
            按模型输出单价 × token
          </div>
        </div>
        <div class="summary-item">
          <div class="summary-label">
            人工登记
          </div>
          <div class="summary-value">
            ¥{{ Number(overview.recordedTotal).toFixed(2) }}
          </div>
          <div class="summary-hint">
            {{ overview.records.length }} 条对账记录
          </div>
        </div>
        <div class="summary-item">
          <div class="summary-label">
            差异（登记 − 估算）
          </div>
          <div
            class="summary-value"
            :class="diffClass(overview.recordedTotal - overview.estimatedTotal)"
          >
            {{ signedMoney(overview.recordedTotal - overview.estimatedTotal) }}
          </div>
          <div class="summary-hint">
            差异为正=登记高于估算
          </div>
        </div>
        <div class="summary-actions">
          <el-date-picker
            v-model="period"
            type="month"
            value-format="YYYY-MM"
            placeholder="账期"
            :clearable="false"
            style="width: 140px"
            @change="fetchList"
          />
          <el-button
            type="primary"
            :icon="Plus"
            @click="openCreate"
          >
            登记对账
          </el-button>
          <el-button
            :icon="Refresh"
            @click="fetchList"
          >
            刷新
          </el-button>
        </div>
      </div>
      <el-alert
        v-if="overview.unpricedModels?.length"
        type="warning"
        :closable="false"
        show-icon
        class="summary-alert"
        :title="`以下模型在目录中未定价，估算成本按 0 计：${overview.unpricedModels.join('、')}`"
      />
    </el-card>

    <SearchTable
      title="按供应商汇总（估算来自平台用量；登记来自人工录入）"
      :loading="loading"
      :data="tableData"
      :columns="columns"
      :pagination="pagination"
      row-key="vendor"
      @search="handleSearch"
      @reset="handleReset"
      @page-change="handlePageChange"
    >
      <template #search>
        <el-input
          v-model="searchForm.keyword"
          placeholder="搜索供应商 / 模型"
          clearable
          style="width: 240px"
          @keyup.enter="handleSearch"
        />
      </template>

      <template #col-vendor="{ row }">
        <span v-if="row.vendor">{{ row.vendor }}</span>
        <el-tag
          v-else
          size="small"
          type="info"
        >
          未归属
        </el-tag>
      </template>

      <template #col-models="{ row }">
        <span v-if="row.models?.length">
          <el-tooltip
            :content="row.models.join('、')"
            placement="top"
          >
            <span>{{ row.models.length }} 个模型</span>
          </el-tooltip>
        </span>
        <span
          v-else
          class="muted"
        >无定价模型</span>
        <el-tag
          v-if="row.unpricedModels?.length"
          size="small"
          type="warning"
          style="margin-left: 6px"
          :title="`未定价：${row.unpricedModels.join('、')}`"
        >
          {{ row.unpricedModels.length }} 个未定价
        </el-tag>
      </template>

      <template #col-tokens="{ row }">
        {{ formatTokens(row.tokens) }}
      </template>

      <template #col-estimated="{ row }">
        ¥{{ Number(row.estimatedCost).toFixed(2) }}
      </template>

      <template #col-recorded="{ row }">
        <span v-if="row.recordCount">¥{{ Number(row.recordedAmount).toFixed(2) }}</span>
        <span
          v-else
          class="muted"
        >未登记</span>
        <span
          v-if="row.recordCount"
          class="muted"
        >· {{ row.recordCount }} 条</span>
      </template>

      <template #col-diff="{ row }">
        <span :class="diffClass(row.diff)">{{ signedMoney(row.diff) }}</span>
      </template>

      <template #footer>
        <div class="basis-note">
          <el-icon><InfoFilled /></el-icon>
          <span>{{ overview.settlementBasis }}</span>
        </div>
      </template>
    </SearchTable>

    <el-card
      shadow="never"
      class="records-card"
    >
      <template #header>
        <div class="records-header">
          <span>对账记录（{{ period }}）</span>
          <el-tag
            size="small"
            type="info"
          >
            人工登记，非付款凭证
          </el-tag>
        </div>
      </template>
      <el-table
        :data="overview.records"
        size="small"
        empty-text="该账期还没有对账记录"
      >
        <el-table-column
          prop="vendor"
          label="供应商"
          width="160"
        >
          <template #default="{ row }">
            <span v-if="row.vendor">{{ row.vendor }}</span>
            <el-tag
              v-else
              size="small"
              type="info"
            >
              未归属
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column
          prop="period"
          label="账期"
          width="100"
        />
        <el-table-column
          label="金额"
          width="130"
        >
          <template #default="{ row }">
            {{ row.currency || 'CNY' }} {{ Number(row.amount).toFixed(2) }}
          </template>
        </el-table-column>
        <el-table-column
          prop="invoiceNo"
          label="发票号"
          width="160"
        >
          <template #default="{ row }">
            {{ row.invoiceNo || '—' }}
          </template>
        </el-table-column>
        <el-table-column
          prop="note"
          label="备注"
          min-width="160"
        >
          <template #default="{ row }">
            {{ row.note || '—' }}
          </template>
        </el-table-column>
        <el-table-column
          prop="recordedBy"
          label="登记人"
          width="140"
        >
          <template #default="{ row }">
            {{ row.recordedBy || '—' }}
          </template>
        </el-table-column>
        <el-table-column
          prop="createdAt"
          label="登记时间"
          width="170"
        >
          <template #default="{ row }">
            {{ formatTime(row.createdAt) }}
          </template>
        </el-table-column>
        <el-table-column
          label="操作"
          width="90"
          fixed="right"
        >
          <template #default="{ row }">
            <el-button
              type="danger"
              link
              size="small"
              @click="doDelete(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div
        class="basis-note"
        style="margin-top: 10px"
      >
        <el-icon><InfoFilled /></el-icon>
        <span>{{ overview.costBasis }}</span>
      </div>
    </el-card>
  </div>

  <el-dialog
    v-model="dialogVisible"
    title="登记供应商对账"
    width="520px"
  >
    <el-form
      label-width="100px"
      size="small"
    >
      <el-form-item
        label="供应商"
        required
      >
        <el-select
          v-model="form.vendor"
          filterable
          allow-create
          default-first-option
          placeholder="选择已有供应商或直接输入名称"
          style="width: 100%"
        >
          <el-option
            v-for="v in vendorOptions"
            :key="v"
            :label="v"
            :value="v"
          />
        </el-select>
        <div class="muted form-hint">
          未归属模型的账单请填「未归属」对应的供应商名称，或留空后在列表手工核对
        </div>
      </el-form-item>
      <el-form-item label="账期">
        <el-input
          v-model="form.period"
          placeholder="YYYY-MM，缺省=当前账期"
        />
      </el-form-item>
      <el-form-item
        label="登记金额"
        required
      >
        <el-input-number
          v-model="form.amount"
          :min="0.01"
          :precision="2"
          :step="10"
        />
      </el-form-item>
      <el-form-item label="币种">
        <el-input
          v-model="form.currency"
          placeholder="CNY"
          style="width: 120px"
        />
      </el-form-item>
      <el-form-item label="发票号">
        <el-input
          v-model="form.invoiceNo"
          placeholder="可选"
        />
      </el-form-item>
      <el-form-item label="备注">
        <el-input
          v-model="form.note"
          type="textarea"
          :rows="2"
          placeholder="可选：账单来源、差异原因等"
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
        登记
      </el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { computed, reactive, ref } from 'vue'
import { InfoFilled, Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  createSettlement,
  deleteSettlement,
  fetchSettlements,
  type SettlementOverview,
  type SettlementRecord,
  type VendorSettlementRow
} from '../api'

const scopeStore = useScopeStore()
const period = ref(currentPeriod())
const overview = ref<SettlementOverview>({
  period: '',
  rows: [],
  records: [],
  estimatedTotal: 0,
  recordedTotal: 0,
  costBasis: '',
  settlementBasis: ''
})

function currentPeriod(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

// 供应商筛选下拉取当前账期全量行（不受表格分页/筛选影响）
let rowsCache: VendorSettlementRow[] = []

// 表格 row-key 需要稳定 id：供应商名即聚合键（空串=未归属，用哨兵占位）。
type Row = VendorSettlementRow & { id: string }

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<Row>({
    fetch: async (params) => {
      const ov = await fetchSettlements(scopeStore.tenantId, period.value)
      overview.value = ov
      rowsCache = ov.rows
      const rows: Row[] = ov.rows.map((r) => ({ ...r, id: r.vendor || '__unassigned__' }))
      const keyword = String(params.keyword ?? '').toLowerCase()
      const filtered = keyword
        ? rows.filter(
            (r) =>
              r.vendor.toLowerCase().includes(keyword) ||
              (r.models ?? []).some((m) => m.toLowerCase().includes(keyword))
          )
        : rows
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
    defaultSearchForm: { keyword: '' },
    pageSize: 10
  })

const vendorOptions = computed(() => rowsCache.filter((r) => r.vendor).map((r) => r.vendor))

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns: ColumnDef[] = [
  { prop: 'vendor', label: '供应商', width: 170, slot: 'vendor' },
  { prop: 'models', label: '模型', minWidth: 170, slot: 'models' },
  { prop: 'tokens', label: 'Token 用量', width: 110, slot: 'tokens' },
  { prop: 'estimated', label: '平台估算', width: 120, slot: 'estimated' },
  { prop: 'recorded', label: '人工登记', width: 140, slot: 'recorded' },
  { prop: 'diff', label: '差异', width: 120, slot: 'diff' }
]

const formatTokens = (n: number) =>
  n >= 1e6 ? `${(n / 1e6).toFixed(2)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}K` : String(n)
const signedMoney = (n: number) => `${n > 0 ? '+' : ''}${n.toFixed(2)}`
const diffClass = (n: number) => (Math.abs(n) < 0.005 ? 'diff-zero' : n > 0 ? 'diff-up' : 'diff-down')
const formatTime = (s: string) => (s ? new Date(s).toLocaleString('zh-CN', { hour12: false }) : '—')

const dialogVisible = ref(false)
const saving = ref(false)
const form = reactive({
  vendor: '',
  period: '',
  amount: 0,
  currency: 'CNY',
  invoiceNo: '',
  note: ''
})

const openCreate = () => {
  form.vendor = ''
  form.period = period.value
  form.amount = 0
  form.currency = 'CNY'
  form.invoiceNo = ''
  form.note = ''
  dialogVisible.value = true
}

const doSave = async () => {
  if (!form.vendor.trim()) {
    ElMessage.warning('请填写供应商名称')
    return
  }
  if (!(form.amount > 0)) {
    ElMessage.warning('登记金额必须大于 0')
    return
  }
  saving.value = true
  try {
    await createSettlement({
      vendor: form.vendor.trim(),
      period: form.period.trim() || period.value,
      amount: form.amount,
      currency: form.currency.trim() || 'CNY',
      invoiceNo: form.invoiceNo.trim(),
      note: form.note.trim()
    })
    ElMessage.success('已登记')
    dialogVisible.value = false
    fetchList()
  } finally {
    saving.value = false
  }
}

const doDelete = async (row: SettlementRecord) => {
  try {
    await ElMessageBox.confirm(
      `确认删除 ${row.vendor || '未归属'} 账期 ${row.period} 的登记记录（${row.currency || 'CNY'} ${row.amount}）？删除后差异会重新变大。`,
      '删除对账记录',
      { type: 'warning' }
    )
  } catch {
    return
  }
  await deleteSettlement(row.id)
  ElMessage.success('已删除')
  fetchList()
}

useScopeChange(() => fetchList())

fetchList()
</script>

<style scoped>
.settlement-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.summary-card :deep(.el-card__body) {
  padding: 16px 20px;
}
.summary-row {
  display: flex;
  align-items: flex-end;
  gap: 40px;
  flex-wrap: wrap;
}
.summary-item {
  min-width: 160px;
}
.summary-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.summary-value {
  font-size: 22px;
  font-weight: 600;
  margin-top: 4px;
}
.summary-hint {
  margin-top: 2px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.summary-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.summary-alert {
  margin-top: 12px;
}
.records-card :deep(.el-card__body) {
  padding: 12px 20px 16px;
}
.records-header {
  display: flex;
  align-items: center;
  gap: 8px;
}
.muted {
  margin-left: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.form-hint {
  margin-left: 0;
  line-height: 1.4;
}
.basis-note {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.diff-zero {
  color: var(--el-text-color-secondary);
}
.diff-up {
  color: var(--el-color-warning);
}
.diff-down {
  color: var(--el-color-success);
}
</style>
