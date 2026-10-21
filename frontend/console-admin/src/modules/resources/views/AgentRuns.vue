<template>
  <div class="agent-runs">
    <!-- 执行记录双域（复核 F04 收敛）：外部 AgentRun + 平台工作流运行同页 tab，
         统一从「执行记录」入口进（spec §8.4），AI 资产页不再承载运行视图。 -->
    <el-tabs v-model="tab">
      <el-tab-pane label="Agent 运行" name="agent" lazy>
    <!-- 汇总卡片：全平台 agent run 健康度 + token 成本（auto-* 是 webhook 自动触发，无人为控制，成本大头需单列） -->
    <el-row :gutter="12" class="stats-row">
      <el-col :span="6">
        <el-card shadow="never">
          <div class="stat-label">总运行</div>
          <div class="stat-value">{{ stats?.total ?? '—' }}</div>
          <div class="stat-sub">成功 {{ stats?.succeeded ?? 0 }} · 失败 {{ stats?.failed ?? 0 }} · 进行中 {{ (stats?.running ?? 0) + (stats?.pending ?? 0) }}</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never">
          <div class="stat-label">成功率</div>
          <div class="stat-value">{{ successRate }}</div>
          <div class="stat-sub">已终态 run 中 succeeded 占比</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never">
          <div class="stat-label">tokens 合计</div>
          <div class="stat-value">{{ fmtTokens(stats?.tokensTotal) }}</div>
          <div class="stat-sub">全部 run 消耗（对方平台计）</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never">
          <div class="stat-label">自动触发占比</div>
          <div class="stat-value">{{ autoRatio }}</div>
          <div class="stat-sub">auto-review/test {{ fmtTokens(stats?.tokensAuto) }} · paas 侧 {{ fmtTokens(stats?.tokensPaas) }}</div>
        </el-card>
      </el-col>
    </el-row>

    <SearchTable
      title="Agent 运行总览（agentrun 执行域，上游无租户维度按来源归类）"
      :loading="loading"
      :data="tableData"
      :columns="columns"
      :pagination="pagination"
      row-key="name"
      @search="handleSearch"
      @reset="handleReset"
      @page-change="handlePageChange"
    >
      <template #search>
        <el-input
          v-model="searchForm.keyword"
          placeholder="搜索 名称 / 阶段 / 状态 / 来源"
          clearable
          style="width: 240px"
          @keyup.enter="handleSearch"
        />
      </template>

      <template #actions>
        <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      </template>

      <template #col-name="{ row }">
        <el-tooltip v-if="row.request" :content="row.request" placement="top" :show-after="300">
          <span class="run-name">{{ row.name }}</span>
        </el-tooltip>
        <span v-else>{{ row.name }}</span>
      </template>
      <template #col-source="{ row }">
        <el-tag :type="sourceType(row.source)" size="small">{{ row.source }}</el-tag>
      </template>
      <template #col-phase="{ row }">
        <el-tag :type="phaseType(row.phase)" size="small">{{ row.phase }}</el-tag>
      </template>
      <template #col-tokens="{ row }">
        <span class="tokens">{{ fmtTokens(row.tokens) }}</span>
      </template>
      <template #col-summary="{ row }">
        <el-tooltip v-if="row.summary" :content="row.summary" placement="top" :show-after="300">
          <span class="summary-text">{{ row.summary }}</span>
        </el-tooltip>
        <el-tooltip v-else-if="row.error" :content="row.error" placement="top" :show-after="300">
          <span class="err-text">{{ shortText(row.error) }}</span>
        </el-tooltip>
        <span v-else>—</span>
      </template>
    </SearchTable>
      </el-tab-pane>

      <el-tab-pane label="工作流运行" name="workflows" lazy>
        <div class="wf-toolbar">
          <el-input
            v-model="wfKeyword"
            placeholder="搜索工作流 ID / run ID"
            clearable
            style="width: 260px"
          />
          <el-button :icon="Refresh" @click="loadWorkflows">刷新</el-button>
          <span v-if="wfError" class="wf-error">{{ wfError }}</span>
        </div>
        <el-table v-loading="wfLoading" :data="filteredWfRuns" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="workflowId" label="工作流" min-width="150" />
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="wfRunStatusType(row.status)">{{ wfRunStatusText(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="节点异常（失败/等待确认）" min-width="220">
            <template #default="{ row }">
              <template v-if="wfNodeIssues(row).length">
                <el-tooltip v-for="issue in wfNodeIssues(row)" :key="issue.nodeId" :content="issue.error || '等待人工确认'">
                  <el-tag size="small" :type="issue.status === 'failed' ? 'danger' : 'warning'" class="wf-tag">
                    {{ issue.nodeId }}·{{ issue.status === 'failed' ? '失败' : '等待' }}
                  </el-tag>
                </el-tooltip>
              </template>
              <span v-else class="dim">无</span>
            </template>
          </el-table-column>
          <el-table-column prop="createdAt" label="开始时间" width="170" />
        </el-table>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
import { visibleTick } from '@/app/composables/useAdminPolling'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useRouteTab, useScopeChange } from '@/app/composables'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchAgentRunList,
  fetchAdminWorkflowRunList,
  type AdminAgentRun,
  type AdminAgentRunStats,
  type AdminWorkflowRun,
  type ResSearchRequest
} from '../api'

const stats = ref<AdminAgentRunStats>()
const tab = useRouteTab('agent', ['agent', 'workflows'])

// —— 工作流运行（平台编排域，从 AI 资产页迁入，复核 F04）——
const scopeStore = useScopeStore()
const wfRuns = ref<AdminWorkflowRun[]>([])
const wfLoading = ref(false)
const wfError = ref('')
const wfKeyword = ref('')

async function loadWorkflows() {
  wfLoading.value = true
  wfError.value = ''
  try {
    const runs = await fetchAdminWorkflowRunList({ page: 1, size: 1000, keyword: '' })
    wfRuns.value = runs.records
  } catch (e) {
    // 失败独立呈现，不抹成空表冒充无运行（语义诚实原则）。
    wfError.value = '加载失败：' + (e as Error).message
  } finally {
    wfLoading.value = false
  }
}

const filteredWfRuns = computed(() => {
  const kw = wfKeyword.value.trim().toLowerCase()
  return wfRuns.value.filter(
    (r) =>
      (!scopeStore.tenantId || r.tenantId === scopeStore.tenantId) &&
      (!kw || r.id.toLowerCase().includes(kw) || r.workflowId.toLowerCase().includes(kw))
  )
})

const wfRunStatusType = (s: string) => {
  if (s === 'succeeded') return 'success'
  if (s === 'failed') return 'danger'
  if (s === 'paused') return 'warning'
  return 'info'
}
const wfRunStatusText = (s: string) => {
  const map: Record<string, string> = {
    succeeded: '成功',
    failed: '失败',
    running: '运行中',
    paused: '等待确认'
  }
  return map[s] ?? s
}
// 节点异常：failed（失败）与 paused（approve 等待人工确认）。
const wfNodeIssues = (row: AdminWorkflowRun) =>
  (row.nodeRuns || []).filter((n) => n.status === 'failed' || n.status === 'paused')

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminAgentRun>({
    fetch: (params) =>
      fetchAgentRunList(params as unknown as ResSearchRequest, (ov) => {
        stats.value = ov.stats
      }),
    defaultSearchForm: { keyword: '' },
    pageSize: 15
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const fmtTokens = (n?: number) => (n == null ? '—' : n >= 10000 ? `${(n / 10000).toFixed(1)} 万` : String(n))
const shortText = (s: string) => (s.length > 40 ? `${s.slice(0, 40)}…` : s)
const successRate = computed(() => {
  const s = stats.value
  if (!s || s.succeeded + s.failed === 0) return '—'
  return `${Math.round((s.succeeded / (s.succeeded + s.failed)) * 100)}%`
})
const autoRatio = computed(() => {
  const s = stats.value
  if (!s || s.tokensTotal === 0) return '—'
  return `${Math.round((s.tokensAuto / s.tokensTotal) * 100)}%`
})

const phaseType = (p: string) =>
  ({ Succeeded: 'success', Failed: 'danger', Running: 'primary', Pending: 'info' } as Record<string, string>)[p] ?? 'info'
const sourceType = (s: string) =>
  ({ 'auto-review': 'warning', 'auto-test': 'warning', paas: 'success' } as Record<string, string>)[s] ?? 'info'

const columns = computed<ColumnDef[]>(() => [
  { prop: 'name', label: 'Run', minWidth: 220, slot: 'name' },
  { prop: 'source', label: '来源', width: 110, slot: 'source' },
  { prop: 'stage', label: '阶段', width: 100 },
  { prop: 'phase', label: '状态', width: 100, slot: 'phase' },
  { prop: 'verdict', label: '结论', width: 110 },
  { prop: 'tokens', label: 'tokens', width: 90, slot: 'tokens' },
  { prop: 'podName', label: 'Pod', minWidth: 140 },
  { prop: 'createdAt', label: '创建时间', width: 160 },
  { prop: 'age', label: '耗时', width: 100 },
  { prop: 'summary', label: '结果摘要', minWidth: 200, slot: 'summary' }
])

// 10s 轮询（运行态 run 实时刷新，与流水线运行总览同款）。
// 切租户重拉当前 tab 数据并清工作流错误态（F01 同款语义：纯过滤器）。
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  fetchList()
  timer = setInterval(
    visibleTick(() => {
      if (tab.value === 'workflows') loadWorkflows()
      else fetchList()
    }),
    10000
  )
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

// 工作流 tab 首次切入加载（lazy pane 只挂载不拉数）；切租户时工作流运行按租户重过滤。
watch(tab, (t) => {
  if (t === 'workflows' && wfRuns.value.length === 0 && !wfError.value) loadWorkflows()
})
useScopeChange(() => {
  wfError.value = ''
  if (tab.value === 'workflows') loadWorkflows()
})
</script>

<style scoped>
.stats-row {
  margin-bottom: 12px;
}
.stat-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.stat-value {
  font-size: 22px;
  font-weight: 600;
  margin: 4px 0;
}
.stat-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.run-name {
  font-family: monospace;
}
.tokens {
  font-family: monospace;
}
.summary-text {
  color: var(--el-text-color-regular);
}
.err-text {
  color: var(--el-color-danger);
}

.wf-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
}
.wf-error {
  color: var(--el-color-danger);
  font-size: 12px;
}
.wf-tag {
  margin: 2px 4px 2px 0;
}
.dim {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}
</style>
