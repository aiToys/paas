<template>
  <div>
    <el-alert type="info" :closable="false" show-icon class="usage-alert">
      <template #title>口径说明（先看这里）</template>
      <div class="usage-lines">
        <div>· 用量是<strong>当前快照</strong>，不是周期增量；周期口径只存在于「账单」。</div>
        <div>· 估算金额按平台 mock 单价计算（与生成账单同一公式），<strong>不是结算金额</strong>，真实结算以账单记录为准。</div>
        <div>· Token 为网关实测回写（租户 + 应用两级）；<strong>模型 / API 密钥维度平台未采集</strong>，下表不展示假数据。</div>
      </div>
    </el-alert>

    <div class="dim-row">
      <span class="dim-label">维度：</span>
      <el-tooltip v-for="d in dimensions" :key="d.key" :content="d.note" placement="top">
        <el-tag :type="d.available ? 'success' : 'info'" size="small" class="dim-tag">
          {{ d.label }}{{ d.available ? '' : '（未采集）' }}
        </el-tag>
      </el-tooltip>
    </div>

    <SearchTable
      title="用量总览（跨租户当前快照）"
      :loading="loading"
      :data="tableData"
      :columns="columns"
      :pagination="pagination"
      row-key="tenantId"
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
        <span class="est-total">全平台估算合计：¥ {{ overview?.estimatedTotal?.toFixed(2) ?? '-' }}</span>
      </template>

      <template #actions>
        <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      </template>

      <template #col-tenant="{ row }">
        <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
      </template>
      <template #col-counts="{ row }">
        <span class="count-cell">
          <span>应用 {{ row.counts?.applications ?? 0 }}</span>
          <span>负载 {{ row.counts?.workloads ?? 0 }}</span>
          <span>模型 {{ row.counts?.models ?? 0 }}</span>
          <span>数据服务 {{ row.counts?.dataservices ?? 0 }}</span>
        </span>
      </template>
      <template #col-tokens="{ row }">
        {{ row.counts?.tokens ?? 0 }}
        <el-tooltip v-if="!row.counts?.tokens" content="网关尚未回写该租户 token 用量" placement="top">
          <el-tag size="small" type="info" class="tag-gap">未采集</el-tag>
        </el-tooltip>
      </template>
      <template #col-over="{ row }">
        <el-tag v-for="k in row.over" :key="k" type="danger" size="small" class="tag-gap">{{ k }} 超限</el-tag>
        <span v-if="!row.over?.length" class="muted">未超限</span>
      </template>
      <template #col-estimatedAmount="{ row }">¥ {{ Number(row.estimatedAmount ?? 0).toFixed(2) }}</template>
    </SearchTable>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import { filterPage } from '../api'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import {
  fetchUsageOverview,
  type AdminUsageOverview,
  type AdminUsageRow,
  type ResSearchRequest
} from '../api'

const scopeStore = useScopeStore()
const overview = ref<AdminUsageOverview | null>(null)
const dimensions = computed(() => overview.value?.dimensions ?? [])

// useCrud 要求行带 id：租户即聚合行主键（快照行 = 每租户一行）。
type UsageRow = AdminUsageRow & { id: string }

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<UsageRow>({
    fetch: async (raw) => {
      const params = raw as unknown as ResSearchRequest
      const data = await fetchUsageOverview(scopeStore.tenantId)
      overview.value = data
      const keyword = params.keyword ?? ''
      // 服务端已按浏览范围过滤；关键字在返回集内匹配租户 ID（行数 = 租户数，量级可控）。
      return filterPage(
        data.rows.map((r) => ({ ...r, id: r.tenantId })),
        params,
        (r) => !keyword || r.tenantId.includes(keyword)
      )
    },
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 160, slot: 'tenant' },
  { prop: 'counts', label: '资源计数（实测）', minWidth: 320, slot: 'counts' },
  { prop: 'tokens', label: 'Token（实测）', width: 140, slot: 'tokens' },
  { prop: 'over', label: '配额', width: 180, slot: 'over' },
  { prop: 'estimatedAmount', label: '估算金额', width: 130, slot: 'estimatedAmount' },
  { prop: 'updatedAt', label: '最近更新', width: 180, formatter: tableTimeFormatter }
])

// 浏览范围（顶栏）联动：纯过滤器，不改变任何租户数据。
useScopeChange(() => fetchList())

fetchList()
</script>

<style scoped>
.usage-alert {
  margin-bottom: 12px;
}
.usage-lines {
  line-height: 1.7;
  font-size: 13px;
}
.dim-row {
  margin-bottom: 12px;
}
.dim-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  margin-right: 4px;
}
.dim-tag {
  margin-right: 6px;
}
.count-cell span {
  margin-right: 12px;
  white-space: nowrap;
}
.tag-gap {
  margin-left: 4px;
}
.muted {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.est-total {
  margin-left: 12px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
</style>
