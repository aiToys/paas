<template>
  <div v-loading="loading">
    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      show-icon
      style="margin-bottom: 8px"
    >
      {{ error }}
    </el-alert>

    <h4 class="section-title">
      当前告警（{{ alerts.length }}，即时评估 firing）
    </h4>
    <el-table
      :data="alerts"
      size="small"
      border
    >
      <el-table-column
        prop="tenantId"
        label="租户"
        width="110"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            type="info"
          >
            {{ row.tenantId }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="规则"
        min-width="140"
      >
        <template #default="{ row }">
          <el-button
            link
            type="primary"
            @click="openAlert(row)"
          >
            {{ row.ruleName }}
          </el-button>
        </template>
      </el-table-column>
      <el-table-column
        label="对象"
        min-width="160"
      >
        <template #default="{ row }">
          {{ row.targetType }}/{{ row.targetId }}
        </template>
      </el-table-column>
      <el-table-column
        label="指标"
        min-width="150"
      >
        <template #default="{ row }">
          {{ row.metricName }} {{ row.value }} {{ row.operator }} {{ row.threshold }}
        </template>
      </el-table-column>
      <el-table-column
        label="级别"
        width="90"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            :type="row.severity === 'critical' ? 'danger' : 'warning'"
          >
            {{ row.severity }}
          </el-tag>
        </template>
      </el-table-column>
    </el-table>
    <el-empty
      v-if="!loading && !error && !alerts.length"
      description="当前无 firing 告警（有规则不等于发生告警）"
      :image-size="48"
    />

    <h4 class="section-title">
      告警历史事件（{{ events.length }}）
    </h4>
    <el-alert
      v-if="eventsNote"
      type="warning"
      :closable="false"
      show-icon
      style="margin-bottom: 8px"
    >
      {{ eventsNote }}
    </el-alert>
    <el-table
      :data="events"
      size="small"
      border
    >
      <el-table-column
        prop="tenantId"
        label="租户"
        width="110"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            type="info"
          >
            {{ row.tenantId }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        prop="ruleName"
        label="规则"
        min-width="140"
      />
      <el-table-column
        label="对象"
        min-width="160"
      >
        <template #default="{ row }">
          {{ row.targetType }}/{{ row.targetId }}
        </template>
      </el-table-column>
      <el-table-column
        label="状态"
        width="100"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            :type="row.status === 'firing' ? 'danger' : row.status === 'resolved' ? 'success' : 'warning'"
          >
            {{ row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        prop="occurredAt"
        label="发生时间"
        width="180"
      />
    </el-table>
    <el-drawer
      v-model="detailVisible"
      title="告警对象详情"
      size="50%"
    >
      <div v-loading="loading">
        <el-descriptions
          :column="1"
          border
        >
          <el-descriptions-item label="租户">
            {{ route.query.tenantId || '全部租户' }}
          </el-descriptions-item>
          <el-descriptions-item label="规则 ID">
            {{ detailId }}
          </el-descriptions-item>
          <el-descriptions-item label="对象">
            {{ route.query.targetType }}/{{ route.query.targetId }}
          </el-descriptions-item>
          <el-descriptions-item label="来源快照时间">
            {{ route.query.firedAt || '未提供' }}
          </el-descriptions-item>
        </el-descriptions>
        <template v-if="focusedAlert">
          <p>以下为该对象的当前评估，告警值与触发时间可能已变化。</p>
          <el-descriptions
            :column="1"
            border
          >
            <el-descriptions-item label="规则">
              {{ focusedAlert.ruleName }}
            </el-descriptions-item>
            <el-descriptions-item label="指标条件">
              {{ focusedAlert.metricName }} {{ alertCondition(focusedAlert) }}
            </el-descriptions-item>
            <el-descriptions-item label="状态">
              {{ focusedAlert.status }}
            </el-descriptions-item>
            <el-descriptions-item label="当前触发时间">
              {{ focusedAlert.firedAt }}
            </el-descriptions-item>
          </el-descriptions>
          <RouterLink
            v-if="focusedAlert.targetType === 'workload'"
            :to="workloadLocation({ id: focusedAlert.targetId, tenantId: focusedAlert.tenantId })"
          >
            查看工作负载
          </RouterLink>
        </template>
        <el-alert
          v-else-if="!loading"
          type="warning"
          :closable="false"
          :title="error || '该对象当前未触发告警，可能已恢复、删除或不在可见范围内；来源快照不代表当前状态。'"
        />
      </div>
    </el-drawer>
  </div>
</template>

<script lang="ts" setup>
// 跨租户当前告警 + 历史事件（D3 spec §8.6）。告警事件与规则配置分列：规则 tab 是配置，
// 这里是「发生了什么」。历史事件依赖 PG 持久化，内存模式后端 501 → 如实提示而非空表冒充无事件。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange, useResourceFocus } from '@/app/composables'
import { alertCondition, workloadLocation, findFocusedAlert } from '@/modules/dashboard/attention'
import { fetchAdminAlertListAll, fetchAdminAlertEventList, type AdminAlert, type AdminAlertEvent } from '../api'

const scopeStore = useScopeStore()
const route = useRoute()
const loading = ref(false)
const error = ref('')
const eventsNote = ref('')
const alerts = ref<AdminAlert[]>([])
const events = ref<AdminAlertEvent[]>([])
const detailVisible = ref(false)
const detailId = ref('')
const focus = useResourceFocus({ path: '/operations/observability', tab: 'events', queryKey: 'ruleId', visible: detailVisible, id: detailId })
const focusedAlert = computed(() => findFocusedAlert(alerts.value, route.query))
const openAlert = (alert: AdminAlert) => focus(alert.ruleId, {
  tenantId: alert.tenantId, targetType: alert.targetType, targetId: alert.targetId, firedAt: alert.firedAt
})
let request = 0
async function load() {
  const current = ++request
  loading.value = true
  error.value = eventsNote.value = ''
  alerts.value = []
  events.value = []
  const [currentAlerts, history] = await Promise.allSettled([
    fetchAdminAlertListAll(scopeStore.tenantId),
    fetchAdminAlertEventList({ page: 1, size: 1000, tenantId: scopeStore.tenantId })
  ])
  if (current !== request) return
  if (currentAlerts.status === 'fulfilled') alerts.value = currentAlerts.value
  else error.value = '当前告警加载失败：' + String(currentAlerts.reason?.message || '未知错误')
  if (history.status === 'fulfilled') events.value = history.value.records
  else eventsNote.value = '告警历史不可用：' + String(history.reason?.message || '需启用 PG 持久化')
  loading.value = false
}

useScopeChange(load, () => { detailVisible.value = false; detailId.value = '' })
onBeforeUnmount(() => { request++ })
onMounted(load)
</script>

<style scoped>
.section-title {
  margin: 12px 0 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
</style>
