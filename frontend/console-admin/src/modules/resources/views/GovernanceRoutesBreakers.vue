<template>
  <div v-loading="loading">
    <el-alert v-if="error" type="error" :closable="false" show-icon style="margin-bottom: 8px">
      {{ error }}
    </el-alert>

    <h4 class="section-title">网关路由（{{ routes.length }}）</h4>
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 8px">
      「已下发」仅表示配置已聚合为 Ingress，不代表公网可达（DNS/TLS/HTTP 探测平台未做）。
    </el-alert>
    <el-table :data="routes" size="small" border>
      <el-table-column prop="tenantId" label="租户" width="110">
        <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="name" label="名称" min-width="120" />
      <el-table-column label="域名 / 路径" min-width="200">
        <template #default="{ row }">{{ (row.host || '*') + row.path }}</template>
      </el-table-column>
      <el-table-column prop="serviceId" label="目标服务" min-width="130" />
      <el-table-column label="期望" width="70">
        <template #default="{ row }">
          <el-tag size="small" :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="下发状态" min-width="150">
        <template #default="{ row }">
          <el-tooltip :disabled="!row.applyError" :content="row.applyError">
            <el-tag size="small" :type="applyType(row.applyStatus)">{{ applyText(row.applyStatus) }}</el-tag>
          </el-tooltip>
          <div v-if="row.appliedAt" class="dim">{{ row.appliedAt }}</div>
        </template>
      </el-table-column>
    </el-table>

    <h4 class="section-title">熔断器（{{ breakers.length }}）</h4>
    <el-table :data="breakers" size="small" border>
      <el-table-column prop="tenantId" label="租户" width="110">
        <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="name" label="名称" min-width="120" />
      <el-table-column prop="serviceId" label="服务" min-width="130" />
      <el-table-column prop="strategy" label="策略" width="110" />
      <el-table-column label="阈值 / 窗口" width="150">
        <template #default="{ row }">{{ row.threshold }} / {{ row.windowSecs }}s（≥{{ row.minRequests }} 请求）</template>
      </el-table-column>
      <el-table-column label="即时状态" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="row.state === 'open' ? 'danger' : row.state === 'half-open' ? 'warning' : 'success'">
            {{ row.enabled ? row.state || 'closed' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script lang="ts" setup>
// 治理路由 / 熔断器跨租户只读视图（D3 spec §8.6）：路由并列「期望启用」与「下发状态+最后错误」，
// 不用单个 enabled 表示已下发；熔断状态为后端即时评估（真实流量采集留后续，不假绿）。
import { onMounted, ref } from 'vue'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import { fetchAdminRouteList, fetchAdminBreakerList, type AdminRoute, type AdminBreaker } from '../api'

const scopeStore = useScopeStore()
const loading = ref(false)
const error = ref('')
const routes = ref<AdminRoute[]>([])
const breakers = ref<AdminBreaker[]>([])

const applyType = (s?: string) => (s === 'applied' ? 'success' : s === 'failed' ? 'danger' : 'info')
const applyText = (s?: string) => (s === 'applied' ? '已下发' : s === 'failed' ? '下发失败' : '未下发')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const q = { page: 1, size: 1000, keyword: '', tenantId: scopeStore.tenantId }
    const [r, b] = await Promise.all([fetchAdminRouteList(q), fetchAdminBreakerList(q)])
    routes.value = r.records
    breakers.value = b.records
  } catch (e) {
    error.value = (e as Error).message || '加载失败'
  } finally {
    loading.value = false
  }
}

useScopeChange(load)
onMounted(load)
</script>

<style scoped>
.section-title {
  margin: 12px 0 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.dim {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}
</style>
