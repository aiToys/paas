<script setup lang="ts">
// 应用详情 - 用量 tab：精确归因（token/gpu 走 billing.byApp）+ 资源占用 + 预估月成本。
// GET /api/billing/usage 返 {usage:{counts, byApp:{[appId]:{tokens,gpu,...}}}}。
// byApp 由 gateway 经应用级 API Key 归因落库（模型推理 token 计费真源）。
// PriceTable 是 mock 单价，成本为「预估」（真实计费引擎留后续）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string }>()
const router = useRouter()

// PriceTable 与 internal/billing/model.go:31-38 对齐（元/单位）。
const PRICE: Record<string, number> = {
  applications: 10, workloads: 5, models: 20, gpu: 100, tokens: 0.001, storage_gb: 0.5,
}
const RES_LABEL: Record<string, string> = {
  applications: '应用', workloads: '工作负载', models: '模型部署', gpu: 'GPU（卡·时）', tokens: 'Token（千次）', storage_gb: '存储（GB）',
}

interface Workload { id: string; envId: string; type: string; replicas: number; ready: number }

const appUsage = ref<Record<string, number>>({})   // 该应用 byApp 归因（token/gpu 精确）
const workloads = ref<Workload[]>([])
const bindingCounts = ref<Record<string, number>>({}) // 按 type 计数的绑定资源
const loading = ref(false)

// 该应用精确归因用量行（token/gpu 等真有 byApp 数据的维度）。
const usageLines = computed(() => {
  return Object.entries(appUsage.value)
    .filter(([, n]) => n > 0)
    .map(([res, n]) => ({
      res, label: RES_LABEL[res] || res, count: n,
      unitPrice: PRICE[res] ?? 0, amount: (PRICE[res] ?? 0) * n,
    }))
})

// 资源占用（应用自身计数）。
const totalReplicas = computed(() => workloads.value.reduce((s, w) => s + w.replicas, 0))
const totalReady = computed(() => workloads.value.reduce((s, w) => s + w.ready, 0))
const resourceStats = computed(() => [
  { label: '工作负载', count: workloads.value.length },
  { label: '副本', count: totalReplicas.value },
  { label: '就绪', count: totalReady.value },
  ...Object.entries(bindingCounts.value)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => ({ label: RES_LABEL[k] || k, count: n })),
])

// 预估月成本（归因用量 × 单价求和）。标注预估（PriceTable 是 mock 单价）。
const estCost = computed(() => usageLines.value.reduce((s, l) => s + l.amount, 0))

async function load() {
  loading.value = true
  try {
    const [usageResp, wlResp] = await Promise.all([
      fetchAuth('/api/billing/usage'),
      fetchAuth(`/api/applications/${props.appId}/workloads`),
    ])
    if (usageResp.ok) {
      const json = await usageResp.json()
      const usage = json?.data?.usage ?? json?.usage ?? {}
      appUsage.value = usage.byApp?.[props.appId] ?? {}
    } else {
      ElMessage.error(`加载用量失败：HTTP ${usageResp.status}`)
    }
    if (wlResp.ok) workloads.value = (await wlResp.json()).data ?? []
    else ElMessage.error(`加载工作负载失败：HTTP ${wlResp.status}`)
    // 绑定资源计数：从 application detail 的 bindings（独立拉一次应用详情）。
    const appResp = await fetchAuth(`/api/applications/${props.appId}`)
    if (appResp.ok) {
      const app = (await appResp.json()).data
      const counts: Record<string, number> = {}
      // 绑定 type 映射到计费维度：models→models，其余按 type 计数（db/cache 等非计费维度忽略）。
      for (const b of app?.bindings ?? []) {
        if (b.type === 'models') counts.models = (counts.models ?? 0) + 1
      }
      bindingCounts.value = counts
    }
  } finally {
    loading.value = false
  }
}

function goBilling() { router.push('/settings/billing') }

onMounted(load)
watch(() => props.appId, load)
</script>

<template>
  <div class="devops-tab" v-loading="loading">
    <div class="tab-head">
      <span class="tab-title">用量与成本</span>
      <span class="tab-hint">精确归因（token/GPU）+ 资源占用 + 预估月成本</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goBilling">
        查看租户账单 →
      </el-button>
    </div>

    <!-- 资源占用 -->
    <section class="sub-block">
      <div class="sub-title">资源占用</div>
      <div class="stat-grid">
        <div v-for="s in resourceStats" :key="s.label" class="stat-card">
          <div class="stat-v mono">{{ s.count }}</div>
          <div class="stat-k">{{ s.label }}</div>
        </div>
      </div>
    </section>

    <!-- 精确归因用量 -->
    <section class="sub-block">
      <div class="sub-title">归因用量（应用级 API Key 计费维度）</div>
      <el-table :data="usageLines" size="small" empty-text="暂无归因用量（应用级 Key 调用 /v1 推理后产生）">
        <el-table-column label="资源" min-width="160">
          <template #default="{ row }">{{ row.label }}</template>
        </el-table-column>
        <el-table-column label="用量" width="140">
          <template #default="{ row }"><span class="mono">{{ row.count }}</span></template>
        </el-table-column>
        <el-table-column label="单价" width="120">
          <template #default="{ row }"><span class="mono">¥{{ row.unitPrice }}</span></template>
        </el-table-column>
        <el-table-column label="金额" width="120">
          <template #default="{ row }"><span class="mono">¥{{ row.amount.toFixed(2) }}</span></template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 预估成本 -->
    <section class="sub-block cost-card">
      <div class="cost-label">预估月成本</div>
      <div class="cost-val mono">¥{{ estCost.toFixed(2) }}</div>
      <div class="cost-note">基于平台单价表预估，非精确计费（计费引擎留后续）</div>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.sub-block { margin-top: 18px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.stat-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; }
.stat-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.stat-v { font-size: 22px; font-weight: 700; }
.stat-k { font-size: 12px; color: var(--text-faint); margin-top: 2px; }
.cost-card { padding: 18px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.cost-label { font-size: 13px; color: var(--text-dim); }
.cost-val { font-size: 30px; font-weight: 700; letter-spacing: -0.02em; margin: 4px 0; color: var(--brand); }
.cost-note { font-size: 11.5px; color: var(--text-faint); }
.mono { font-family: var(--font-mono); }
</style>
