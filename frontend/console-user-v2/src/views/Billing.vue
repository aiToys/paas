<script setup lang="ts">
import PageHeader from '@/components/PageHeader.vue'

import { formatDateTime } from '@/utils/format'
import { useRouter } from 'vue-router'
const router = useRouter()
// 设置 → 配额与账单（租户级资源配额 + 用量 + 账单，多租户商业化根基）。
// 配额用量卡（超限红色告警）+ 生成本期账单 + 账单列表（展开明细 + 支付）。
// 独立于物理环境，不接 prod:write。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { useSessionStore } from '@/stores/session'

interface UsageLine {
  resource: string; count: number; limit: number; over: boolean
}
interface UsageView {
  quota: { limits: Record<string, number> }
  usage: { counts: Record<string, number>; byApp?: Record<string, Record<string, number>> }
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
  dataservices: '数据服务',
  models: '模型部署',
  gpu: 'GPU（卡·时）',
  tokens: 'Token（千）',
  storage_gb: '存储（GB）',
}
// 参与强制拦截的资源（后端 billing.CheckAndInc 在 Create 前拦截 429：application/workload/dataservice）。
// 其余资源只进估算，不拦截——页面如实区分（专项06 §5）。
const ENFORCED_RESOURCES = new Set(['applications', 'workloads', 'dataservices'])
// 无自动采集源的估算资源：counts 为 0 时是「未接入采集」不是「确无用量」。
const UNCOLLECTED_RESOURCES = new Set(['models', 'gpu', 'storage_gb'])

const view = ref<UsageView | null>(null)
const bills = ref<Bill[]>([])
// 独立错误态（专项06 §5：用量失败不显示 0、账单失败不显示空）
const usageError = ref('')
const billsError = ref('')
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

// 「调整配额」需后端专用权限 billing:quota-manage（R8-L1 分离 billing:write）；
// developer 只读配额，按钮不显示（后端仍 403 兜底，前端仅 UX 对齐）。
// tenant:admin 与 *（super_admin）通行一切权限，与后端 Role.Grants 同语义。
const session = useSessionStore()
const canManageQuota = computed(() => {
  const perms = session.profile?.permissions ?? []
  return perms.includes('*') || perms.includes('tenant:admin') || perms.includes('billing:quota-manage')
})

const hasOver = computed(() => view.value?.items.some((i) => i.over) ?? false)

// 按应用拆 token 用量（模型推理归因）：byApp[appID].tokens → 降序展示。
// 让租户看清"哪个应用消耗了多少 token"，与工作负载/数据服务的应用维度计费对齐。
const tokenByApp = computed(() => {
  const byApp = view.value?.usage.byApp
  if (!byApp) return []
  return Object.entries(byApp)
    .map(([appId, res]) => ({ appId, tokens: res.tokens ?? 0 }))
    .filter((x) => x.tokens > 0)
    .sort((a, b) => b.tokens - a.tokens)
})
const totalTokens = computed(() => tokenByApp.value.reduce((s, x) => s + x.tokens, 0))
// 未归属差额：全租户 token 计量 - 已按应用归因部分。>0 时单列展示——
// 「按应用」是计量子集视图，子集之和≠全组织总量（专项06 §5 不把子集当全量）。
const unattributedTokens = computed(() => {
  const total = view.value?.usage.counts?.tokens ?? 0
  return Math.max(0, total - totalTokens.value)
})

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
    ElMessage.error(apiError(e, '更新失败'))
  }
}

async function loadUsage() {
  usageError.value = ''
  const resp = await fetchAuth('/api/billing/usage')
  if (resp.ok) view.value = (await resp.json()).data
  else usageError.value = (await resp.json().catch(() => ({}))).error || `用量加载失败（${resp.status}）`
}

async function loadBills() {
  billsError.value = ''
  const resp = await fetchAuth('/api/billing/records')
  if (resp.ok) bills.value = (await resp.json()).data ?? []
  else billsError.value = (await resp.json().catch(() => ({}))).error || `账单加载失败（${resp.status}）`
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
      // 历史周期也按当前计数快照生成（无期间计量事件），不伪装该期实际用量
      ElMessage.success(`已生成 ${periodInput.value} 估算记录（按当前用量快照）`)
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
    await ElMessageBox.confirm(
      `确认将估算记录 ${bill.period}（¥${bill.total.toFixed(2)}）标记为已结算？`
      + '未接入支付渠道与资金凭证，此操作仅更新记录状态，不会发生真实扣款。',
      '标记结算（模拟）',
      { confirmButtonText: '标记为已结算', cancelButtonText: '取消', type: 'warning' }
    )
  } catch {
    return
  }
  try {
    const resp = await fetchAuth(`/api/billing/records/${bill.id}/pay`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success('已标记结算（模拟，无真实扣款）')
      loadBills()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '标记结算失败')
    }
  } catch (e) {
    ElMessage.error(apiError(e, '标记结算失败'))
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
    <PageHeader title="用量与配额" description="查看组织资源配额、产品用量与账单。" eyebrow="组织与访问"><el-button v-if="canManageQuota" @click="startEditQuota" :disabled="!view">调整配额</el-button></PageHeader>

    <!-- 配额用量 -->
    <section class="block" v-loading="loading">
      <div class="block-head">
        <span class="block-title">配额用量</span>
        <el-tag v-if="hasOver" type="danger" size="small">⚠️ 存在超额资源</el-tag>
      </div>
      <p class="bill-note">计数口径：当前实时计数（非期间累计）；「拦截」= 超限时创建请求被拒（429），「估算」= 仅参与账单估算、不拦截。</p>
      <div v-if="usageError" class="usage-error">
        ⚠ {{ usageError }}
        <button class="act" @click="loadUsage">重新加载</button>
      </div>
      <div v-else class="quota-grid">
        <div v-for="line in view?.items ?? []" :key="line.resource" class="quota-card" :class="{ over: line.over }">
          <div class="qc-head">
            <span class="qc-label">{{ resLabel(line.resource) }}</span>
            <el-tag v-if="line.over" type="danger" size="small">超额</el-tag>
            <el-tag v-else-if="ENFORCED_RESOURCES.has(line.resource)" type="success" size="small" effect="plain">超限拦截</el-tag>
            <el-tag v-else type="info" size="small" effect="plain">仅估算</el-tag>
          </div>
          <div class="qc-value">
            <span class="qc-count mono">{{ line.count.toLocaleString('zh-CN') }}</span>
            <span class="qc-limit" v-if="line.limit !== unlimited">/ {{ line.limit }}</span>
            <el-tag v-else type="info" size="small">无限</el-tag>
          </div>
          <el-progress
            :percentage="pct(line)"
            :color="line.over ? '#f43f5e' : '#6366f1'"
            :show-text="false"
            :stroke-width="6"
          />
          <div v-if="UNCOLLECTED_RESOURCES.has(line.resource) && !line.count" class="qc-uncollected">
            未接入自动采集，按 0 估算
          </div>
        </div>
      </div>
    </section>

    <!-- 模型推理 token 按应用拆分（应用级 Key 归因） -->
    <section v-if="tokenByApp.length || unattributedTokens" class="block">
      <div class="block-head">
        <span class="block-title">模型 Token 用量 · 按应用</span>
        <el-tag type="info" size="small">应用级 Key 归因</el-tag>
      </div>
      <p class="bill-note">按应用拆分只覆盖绑定应用的 Key 调用；下方各行之和 ≠ 全组织 Token 总量（未归属部分单列）。</p>
      <el-table :data="[...tokenByApp, ...(unattributedTokens ? [{ appId: '__unattributed__', tokens: unattributedTokens }] : [])]" size="small">
        <el-table-column label="应用" min-width="200">
          <template #default="{ row }">
            <span v-if="row.appId === '__unattributed__'" class="dim">未归属（组织级 Key / 无应用绑定）</span>
            <a v-else class="mono app-link" @click="router.push('/products/' + row.appId)">{{ row.appId }}</a>
          </template>
        </el-table-column>
        <el-table-column label="Token 用量（千）" width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.tokens.toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="占比（已归属内）" width="160">
          <template #default="{ row }">
            <el-progress
              :percentage="Math.round((row.tokens / totalTokens) * 100) || 0"
              :color="'#6366f1'"
              :show-text="true"
              :stroke-width="6"
            />
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 账单 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">估算记录</span>
        <div class="gen-row">
          <el-input v-model="periodInput" size="small" style="width: 120px" placeholder="YYYY-MM" />
          <el-button type="primary" size="small" :loading="generating" @click="generate">生成估算记录</el-button>
        </div>
      </div>
      <p class="bill-note">
        记录按<b>生成时刻</b>的用量快照 × 示例单价估算（历史周期亦如此，非该期实际用量）；
        未接入真实计费、支付渠道与资金凭证，「结算」为人工标记（模拟），不发生真实扣款。
      </p>
      <div v-if="billsError" class="usage-error">
        ⚠ {{ billsError }}
        <button class="act" @click="loadBills">重新加载</button>
      </div>
      <el-table v-else :data="bills" size="small" empty-text="暂无估算记录，可生成" row-key="id">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="bill-items">
              <div class="table-scroll" data-table-scroll tabindex="0">
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
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="period" label="周期" width="120" />
        <el-table-column label="金额" width="140">
          <template #default="{ row }"><span class="mono strong">{{ fmtMoney(row.total) }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="row.status === 'paid' ? 'success' : 'warning'" size="small">
              {{ row.status === 'paid' ? '已结算（人工标记）' : '待结算' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="生成时间" width="180">
          <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="结算时间" width="180">
          <template #default="{ row }">{{ row.paidAt ? formatDateTime(row.paidAt) : '—' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button v-if="row.status === 'unpaid'" text type="primary" size="small" @click="pay(row)">标记已结算（模拟）</el-button>
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
.dim { color: var(--text-dim); }
.dialog-tip { font-size: 12.5px; color: var(--text-dim); margin: 0 0 12px; }
.app-link { color: var(--brand); cursor: pointer; }
.app-link:hover { text-decoration: underline; }
/* BL-01/专项06 §5：估算口径说明（弱化，不与真实结算混淆） */
.bill-note { margin: 0 0 10px; font-size: 12px; color: var(--text-faint); line-height: 1.6; }
.usage-error {
  padding: 10px 12px;
  border: 1px solid rgba(244, 63, 94, 0.4);
  border-radius: var(--radius);
  color: var(--danger);
  font-size: 12.5px;
  display: flex;
  gap: 10px;
  align-items: center;
}
.qc-uncollected { margin-top: 6px; font-size: 11px; color: var(--text-faint); }
</style>
