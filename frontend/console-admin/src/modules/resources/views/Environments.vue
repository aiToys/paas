<template>
  <SearchTable
    title="环境总览（跨租户）"
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
        placeholder="搜索 ID / 名称 / 类型"
        clearable
        style="width: 240px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button type="primary" :icon="Plus" @click="createVisible = true">新建环境</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-tenant="{ row }">
      <el-tag size="small" type="info">{{ row.tenantId }}</el-tag>
    </template>

    <template #col-name="{ row }">
      <el-button link type="primary" @click="openDetail(row.id)">{{ row.name }}</el-button>
    </template>

        <template #col-type="{ row }">
      <el-tag :type="row.type === 'prod' ? 'danger' : 'success'" size="small">
        {{ row.type === 'prod' ? '生产' : '测试' }}
      </el-tag>
    </template>
    <template #col-usage="{ row }">
      <el-button link type="primary" size="small" @click="openUsage(row)">依赖预检</el-button>
    </template>
  </SearchTable>

  <EnvironmentCreateDrawer v-model="createVisible" @created="fetchList" />
  <EnvironmentDrawer v-model="drawerVisible" :id="detailId" />

  <!-- 依赖预检（只读）：与租户侧删除前置检查同源；有依赖时删除会被 409 拦截，后台不代删。 -->
  <el-dialog v-model="usageVisible" title="环境依赖预检" width="460px">
    <div v-if="usageLoading" v-loading="true" class="usage-loading" />
    <el-alert v-else-if="usageError" type="error" :closable="false" show-icon :title="usageError" />
    <template v-else-if="usage">
      <el-descriptions :column="1" border size="small">
        <el-descriptions-item label="工作负载">{{ usage.workloads }}</el-descriptions-item>
        <el-descriptions-item label="数据服务">{{ usage.dataservices }}</el-descriptions-item>
        <el-descriptions-item label="泳道">{{ usage.lanes }}</el-descriptions-item>
      </el-descriptions>
      <el-alert
        class="usage-note"
        :type="usageTotal > 0 ? 'warning' : 'info'"
        :closable="false"
        show-icon
        :title="usageTotal > 0
          ? `存在 ${usageTotal} 项依赖：租户侧删除该环境会被 409 拦截（需先迁移或删除依赖）。`
          : '未发现依赖，租户侧可删除该环境。'"
      />
    </template>
    <template #footer>
      <el-button @click="usageVisible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import { fetchEnvironmentList, fetchEnvUsage, type AdminEnvironment, type AdminEnvUsage, type ResSearchRequest } from '../api'
import EnvironmentCreateDrawer from './EnvironmentCreateDrawer.vue'
import EnvironmentDrawer from './EnvironmentDrawer.vue'

const { listData, loading, pagination, searchForm, fetchList, handleSearch, handleReset, handlePageChange } =
  useCrud<AdminEnvironment>({
    fetch: (params) => fetchEnvironmentList({ ...params, tenantId: scopeStore.tenantId } as unknown as ResSearchRequest),
    defaultSearchForm: { keyword: '', tenantId: '' },
    pageSize: 10
  })

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])
const createVisible = ref(false)
const drawerVisible = ref(false)
const detailId = ref('')
const openDetail = (id: string) => {
  detailId.value = id
  drawerVisible.value = true
}

const columns = computed<ColumnDef[]>(() => [
  { prop: 'tenantId', label: '租户', width: 130, slot: 'tenant' },
  { prop: 'id', label: '环境 ID', minWidth: 150 },
  { prop: 'name', label: '名称', minWidth: 140, slot: 'name' },
  { prop: 'type', label: '类型', width: 100, slot: 'type' },
  { prop: 'cluster', label: '集群', width: 140 },
  { prop: 'usage', label: '依赖', width: 110, slot: 'usage', hideable: false }
])

// 依赖预检：失败如实显示错误，不用 0 冒充「无依赖」。
const usageVisible = ref(false)
const usageLoading = ref(false)
const usageError = ref('')
const usage = ref<AdminEnvUsage>()
const usageTotal = computed(() => (usage.value ? usage.value.workloads + usage.value.dataservices + usage.value.lanes : 0))

async function openUsage(row: Record<string, unknown>) {
  usage.value = undefined
  usageError.value = ''
  usageLoading.value = true
  usageVisible.value = true
  try {
    usage.value = await fetchEnvUsage(String(row.id))
  } catch (e) {
    usageError.value = '依赖预检失败：' + ((e as Error).message || '未知错误')
  } finally {
    usageLoading.value = false
  }
}

// 浏览范围（顶栏）联动：切租户重拉列表；范围是纯过滤器不改写目标
const scopeStore = useScopeStore()
useScopeChange(() => fetchList(), () => {
  drawerVisible.value = false
  detailId.value = ''
})

fetchList()
</script>

<style scoped>
.usage-loading {
  height: 120px;
}
.usage-note {
  margin-top: 12px;
}
</style>
