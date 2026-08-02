<script setup lang="ts">
// 设置 → 配额与账单（租户级资源配额 + 用量 + 账单，多租户商业化根基）。
// 配额用量卡（超限红色告警）+ 生成本期账单 + 账单列表（展开明细 + 支付）。
// 独立于物理环境，不接 prod:write。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'

interface UsageLine {
  resource: string; count: number; limit: number; over: boolean
}
interface UsageView {
  quota: { limits: Record<string, number> }
  usage: { counts: Record<string, number> }
  items: UsageLine[]
}
interface BillItem {
  resource: string; quantity: number; unitPrice: number; amount: number
}
interface Bill {
  id: string; period: string; items: BillItem[]; total: number
  status: string; createdAt: string; paidAt?: string
}

const RES_LABEL: Record<string, string> = {
  applications: '应用数',
  workloads: '工作负载',
  models: '模型部署',
  gpu: 'GPU（卡·时）',
  tokens: 'Token（千）',
  storage_gb: '存储（GB）',
}

const view = ref<UsageView | null>(null)
const bills = ref<Bill[]>([])
const loading = ref(false)
const generating = ref(false)
const periodInput = ref(currentPeriod())

const unlimited = -1

function currentPeriod(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

const quotaEditable = ref(false)
const quotaDraft = ref<Record<string, number>>({})

const hasOver = computed(() => view.value?.items.some((i) => i.over) ?? false)

function startEditQuota() {
  quotaDraft.value = { ...(view.value?.quota.limits ?? {}) }
  quotaEditable.value = true
}

async function saveQuota() {
  try {
    const resp = await fetchAuth('/api/billing/quota', {
      method: 'PUT',
      body: JSON.stringify({ limits: quotaDraft.value }),
    })
    if (resp.ok) {
      ElMessage.success('配额已更新')
      quotaEditable.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '更新失败')
    }
  } catch (e) {
    ElMessage.error('更新失败：' + (e as Error).message)
  }
}

async function loadUsage() {
  const resp = await fetchAuth('/api/billing/usage')
  if (resp.ok) view.value = await resp.json()
}

async function loadBills() {
  const resp = await fetchAuth('/api/billing/records')
  if (resp.ok) bills.value = (await resp.json()).data ?? []
}

async function load() {
  loading.value = true
  try {
    await Promise.all([loadUsage(), loadBills()])
  } finally {
    loading.value = false
  }
}

async function generate() {
  if (!/^\d{4}-\d{2}$/.test(periodInput.value)) {
    ElMessage.warning('周期格式应为 YYYY-MM')
    return
  }
  generating.value = true
  try {
    const resp = await fetchAuth(`/api/billing/records/generate?period=${periodInput.value}`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success(`已生成 ${periodInput.value} 账单`)
      loadBills()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '生成失败')
    }
  } finally {
    generating.value = false
  }
}

async function pay(bill: Bill) {
  try {
    await ElMessageBox.confirm(`确认支付账单 ${bill.period}（¥${bill.total.toFixed(2)}）？`, '支付确认', {
      confirmButtonText: '支付', cancelButtonText: '取消', type: 'warning',
    })
  } catch {
    return
  }
  try {
    const resp = await fetchAuth(`/api/billing/records/${bill.id}/pay`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success('已支付')
      loadBills()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '支付失败')
    }
  } catch (e) {
    ElMessage.error('支付失败：' + (e as Error).message)
  }
}

function pct(line: UsageLine): number {
  if (line.limit === unlimited || line.limit === 0) return line.count > 0 ? 100 : 0
  return Math.min(100, Math.round((line.count / line.limit) * 100))
}

function resLabel(r: string): string {
  return RES_LABEL[r] ?? r
}

function fmtMoney(n: number): string {
  return `¥${n.toFixed(2)}`
}

onMounted(load)
</script>

<template>
  <div class="bill-page">
    <div class="page-head">
      <div>
        <h2>配额与账单</h2>
        <p class="sub">租户级资源配额 + 用量统计 + 计费账单（多租户商业化根基）</p>
      </div>
      <el-button @click="startEditQuota" :disabled="!view">调整配额</el-button>
    </div>

    <!-- 配额用量 -->
    <section class="block" v-loading="loading">
      <div class="block-head">
        <span class="block-title">配额用量</span>
        <el-tag v-if="hasOver" type="danger" size="small">⚠️ 存在超额资源</el-tag>
      </div>
      <div class="quota-grid">
        <div v-for="line in view?.items ?? []" :key="line.resource" class="quota-card" :class="{ over: line.over }">
          <div class="qc-head">
            <span class="qc-label">{{ resLabel(line.resource) }}</span>
            <el-tag v-if="line.over" type="danger" size="small">超额</el-tag>
            <el-tag v-else-if="line.limit === unlimited" type="info" size="small">无限</el-tag>
          </div>
          <div class="qc-value">
            <span class="qc-count mono">{{ line.count.toLocaleString() }}</span>
            <span class="qc-limit" v-if="line.limit !== unlimited">/ {{ line.limit }}</span>
          </div>
          <el-progress
            :percentage="pct(line)"
            :color="line.over ? '#f43f5e' : '#6366f1'"
            :show-text="false"
            :stroke-width="6"
          />
        </div>
      </div>
    </section>

    <!-- 账单 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">计费账单</span>
        <div class="gen-row">
          <el-input v-model="periodInput" size="small" style="width: 120px" placeholder="YYYY-MM" />
          <el-button type="primary" size="small" :loading="generating" @click="generate">生成本期账单</el-button>
        </div>
      </div>
      <el-table :data="bills" size="small" empty-text="暂无账单，可生成本期账单" row-key="id">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="bill-items">
              <table class="items-table">
                <thead>
                  <tr><th>资源</th><th class="num">数量</th><th class="num">单价</th><th class="num">金额</th></tr>
                </thead>
                <tbody>
                  <tr v-for="it in row.items" :key="it.resource">
                    <td>{{ resLabel(it.resource) }}</td>
                    <td class="num mono">{{ it.quantity }}</td>
                    <td class="num mono">{{ fmtMoney(it.unitPrice) }}</td>
                    <td class="num mono">{{ fmtMoney(it.amount) }}</td>
                  </tr>
                  <tr v-if="!row.items?.length"><td colspan="4" class="empty">无用量明细</td></tr>
                </tbody>
              </table>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="period" label="周期" width="120" />
        <el-table-column label="金额" width="140">
          <template #default="{ row }"><span class="mono strong">{{ fmtMoney(row.total) }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.status === 'paid' ? 'success' : 'warning'" size="small">
              {{ row.status === 'paid' ? '已支付' : '待支付' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="生成时间" width="180">
          <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="支付时间" width="180">
          <template #default="{ row }">{{ row.paidAt ? new Date(row.paidAt).toLocaleString() : '—' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button v-if="row.status === 'unpaid'" text type="primary" size="small" @click="pay(row)">支付</el-button>
            <span v-else class="text-faint">—</span>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 调整配额弹窗 -->
    <el-dialog v-model="quotaEditable" title="调整资源配额" width="520px">
      <p class="dialog-tip">-1 表示无上限。调整配额会影响后续账单核算。</p>
      <el-form label-width="140px">
        <el-form-item v-for="r in Object.keys(RES_LABEL)" :key="r" :label="RES_LABEL[r]">
          <el-input-number v-model="quotaDraft[r]" :min="-1" :step="1" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="quotaEditable = false">取消</el-button>
        <el-button type="primary" @click="saveQuota">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.bill-page { max-width: 1100px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.block { margin-bottom: 24px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.block-title { font-size: 14px; font-weight: 600; }
.gen-row { display: flex; gap: 8px; }

.quota-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}
.quota-card {
  padding: 14px 16px;
  border-radius: var(--radius);
  background: var(--surface);
  border: 1px solid var(--border);
}
.quota-card.over {
  border-color: #f43f5e;
  background: rgba(244, 63, 94, 0.06);
}
.qc-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.qc-label { font-size: 12.5px; color: var(--text-dim); }
.qc-value { margin-bottom: 10px; }
.qc-count { font-size: 22px; font-weight: 700; }
.qc-limit { font-size: 13px; color: var(--text-faint); margin-left: 6px; }

.bill-items { padding: 8px 24px; }
.items-table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.items-table th { text-align: left; padding: 6px 10px; color: var(--text-faint); font-weight: 500; border-bottom: 1px solid var(--border); }
.items-table td { padding: 6px 10px; border-bottom: 1px solid var(--border); }
.items-table .num { text-align: right; }
.items-table .empty { text-align: center; color: var(--text-faint); padding: 14px; }

.strong { font-weight: 600; }
.text-faint { color: var(--text-faint); }
.dialog-tip { font-size: 12.5px; color: var(--text-dim); margin: 0 0 12px; }
</style>
