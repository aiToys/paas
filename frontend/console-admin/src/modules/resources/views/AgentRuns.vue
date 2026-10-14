<template>
  <div class="agent-runs">
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
  </div>
</template>

<script lang="ts" setup>
import { visibleTick } from '@/app/composables/useAdminPolling'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchAgentRunList, type AdminAgentRun, type AdminAgentRunStats, type ResSearchRequest } from '../api'

const stats = ref<AdminAgentRunStats>()
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
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  fetchList()
  timer = setInterval(visibleTick(fetchList), 10000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
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
</style>
