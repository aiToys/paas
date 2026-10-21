<template>
  <div class="lanes">
    <!-- 回收结果语义：平台不持久化 CloseResult（reclaimed/failedIds 只在关闭请求返回时可见），
         此处只展示实体终态，绝不臆造「回收成功/失败」结论。 -->
    <el-alert type="info" :closable="false" show-icon class="semantic-note">
      <template #title>
        关闭（closed）= 资源已回收、记录保留供审计；standard 泳道闲置超 TTL 由 LaneGC 回收。
        回收任务结果（reclaimed / failedIds）只在关闭执行时返回、平台不持久化，本视图不判断回收成败。
        基线 default 受保护，不会实体化为泳道。
      </template>
    </el-alert>

    <SearchTable
      title="泳道总览（跨租户只读）"
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
          placeholder="搜索 名称 / 环境 / ID"
          clearable
          style="width: 200px"
          @keyup.enter="handleSearch"
        />
        <el-input
          v-model="searchForm.envId"
          placeholder="环境 ID（精确）"
          clearable
          style="width: 190px"
          @keyup.enter="handleSearch"
        />
        <el-select v-model="searchForm.status" placeholder="状态" clearable style="width: 120px" @change="handleSearch">
          <el-option label="活跃" value="active" />
          <el-option label="已关闭" value="closed" />
        </el-select>
        <el-select v-model="searchForm.mode" placeholder="模式" clearable style="width: 140px" @change="handleSearch">
          <el-option label="standard（可 TTL 回收）" value="standard" />
          <el-option label="permanent（常驻）" value="permanent" />
        </el-select>
      </template>

      <template #actions>
        <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
      </template>

      <template #col-tenantId="{ row }">
        <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
      </template>
      <template #col-status="{ row }">
        <el-tag size="small" :type="row.status === 'closed' ? 'info' : 'success'">
          {{ row.status === 'closed' ? '已关闭' : '活跃' }}
        </el-tag>
      </template>
      <template #col-mode="{ row }">
        <el-tag size="small" :type="row.mode === 'permanent' ? 'warning' : 'primary'">
          {{ row.mode === 'permanent' ? 'permanent' : 'standard' }}
        </el-tag>
      </template>
    </SearchTable>
  </div>
</template>

<script lang="ts" setup>
// 泳道跨租户只读总览（D4）。不提供后台关闭/回收：关闭泳道会级联回收租户资源，
// 属租户侧运维动作（生产泳道关闭还需 prod:write），后台不代执行。
import { computed, onMounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { tableTimeFormatter } from '@/lib/format'
import { fetchLaneList, type AdminLane, type LaneSearchRequest } from '../api'

const scopeStore = useScopeStore()

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminLane>({
    fetch: (params) => fetchLaneList({ ...params, tenantId: scopeStore.tenantId } as unknown as LaneSearchRequest),
    defaultSearchForm: { keyword: '', envId: '', status: '', mode: '', tenantId: '' },
    pageSize: 20
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 120, slot: 'tenantId' },
  { prop: 'id', label: '泳道 ID', minWidth: 180 },
  { prop: 'name', label: '名称', minWidth: 140 },
  { prop: 'envId', label: '环境', minWidth: 140 },
  { prop: 'mode', label: '模式', width: 150, slot: 'mode' },
  { prop: 'status', label: '状态', width: 100, slot: 'status' },
  { prop: 'createdAt', label: '创建时间', width: 170, formatter: tableTimeFormatter },
  { prop: 'updatedAt', label: '最后变更', width: 170, formatter: tableTimeFormatter }
])

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
useScopeChange(() => fetchList())

onMounted(() => fetchList())
</script>

<style scoped>
.semantic-note {
  margin-bottom: 12px;
}
</style>
