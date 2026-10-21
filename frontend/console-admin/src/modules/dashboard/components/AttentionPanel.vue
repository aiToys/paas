<template>
  <el-card
    shadow="never"
    class="attention-panel"
  >
    <template #header>
      <div class="panel-header">
        <span>需要关注</span>
        <el-button
          link
          type="primary"
          size="small"
          @click="load"
        >
          刷新
        </el-button>
      </div>
    </template>

    <div
      v-if="loading"
      v-loading="true"
      class="panel-body"
    />

    <template v-else>
      <!-- 三源独立错误条（失败≠无异常，语义诚实） -->
      <el-alert
        v-if="runError"
        type="error"
        :closable="false"
        show-icon
        :title="`失败运行加载失败：${runError}`"
        class="src-error"
      />
      <el-alert
        v-if="alertError"
        type="error"
        :closable="false"
        show-icon
        :title="`当前告警加载失败：${alertError}`"
        class="src-error"
      />
      <el-alert
        v-if="workloadError"
        type="error"
        :closable="false"
        show-icon
        :title="`异常负载加载失败：${workloadError}`"
        class="src-error"
      />

      <div
        v-if="!hasIssues && !anyError"
        class="all-clear"
      >
        <el-icon color="var(--el-color-success)">
          <CircleCheckFilled />
        </el-icon>
        暂无需要关注的事项（快照：{{ loadedAt }}）
      </div>

      <template v-else>
        <div
          v-if="failedRuns.length"
          class="issue-group"
        >
          <div class="group-title">
            失败运行（最近）
          </div>
          <RouterLink
            v-for="r in failedRuns"
            :key="r.id"
            class="issue-row"
            :to="runLocation(r)"
          >
            <el-tag
              type="danger"
              size="small"
            >
              失败
            </el-tag>
            <span class="issue-main">{{ r.pipelineId }}</span>
            <span class="issue-sub">{{ r.tenantId }} · {{ r.branch }} · {{ r.createdAt }}</span>
          </RouterLink>
        </div>

        <div
          v-if="firingAlerts.length"
          class="issue-group"
        >
          <div class="group-title">
            触发中的告警
          </div>
          <RouterLink
            v-for="a in firingAlerts"
            :key="`${a.tenantId}-${a.ruleId}-${a.targetType}-${a.targetId}-${a.firedAt}`"
            class="issue-row"
            :to="alertLocation(a)"
          >
            <el-tag
              :type="a.severity === 'critical' ? 'danger' : 'warning'"
              size="small"
            >
              {{ a.severity }}
            </el-tag>
            <span class="issue-main">{{ a.ruleName }}</span>
            <span class="issue-sub">{{ a.tenantId }} · {{ a.targetType }}/{{ a.targetId }} · {{ alertCondition(a) }}</span>
          </RouterLink>
        </div>

        <div
          v-if="abnormalWorkloads.length"
          class="issue-group"
        >
          <div class="group-title">
            异常负载（就绪副本不足或失败）
          </div>
          <RouterLink
            v-for="w in abnormalWorkloads"
            :key="w.id"
            class="issue-row"
            :to="workloadLocation(w)"
          >
            <el-tag
              :type="w.status === 'failed' ? 'danger' : 'warning'"
              size="small"
            >
              {{ (w.ready ?? 0) }}/{{ w.replicas ?? 0 }}
            </el-tag>
            <span class="issue-main">{{ w.name }}</span>
            <span class="issue-sub">{{ w.tenantId }} · {{ w.status || '未知状态' }}</span>
          </RouterLink>
        </div>

        <div class="snapshot-note">
          快照：{{ loadedAt }}（打开页面时聚合，不自动刷新）
        </div>
      </template>
    </template>
  </el-card>
</template>

<script lang="ts" setup>
// 首页「需要关注」（spec §6 / 复核 F06）：从真实数据源聚合三类异常——失败运行 /
// 触发中告警 / 异常负载，每条可点击直达对应页面（≤2 次点击到现场的验收要求）。
// 三源 allSettled 独立结算：单源失败显示错误条，不抹掉其他源的成功分区（H-3 同款）。
// 快照语义：打开页面时聚合一次，不做轮询（与统计卡一致的新鲜度口径）。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { CircleCheckFilled } from '@element-plus/icons-vue'
import { useScopeStore } from '@/app/stores/scope'
import { useScopeChange } from '@/app/composables'
import type { AdminPipelineRun, AdminAlert, AdminWorkload } from '@/modules/resources/api'
import { fetchAttention, runLocation, workloadLocation, alertLocation, alertCondition } from '../attention'

const scope = useScopeStore()
let request = 0
const loading = ref(false)
const loadedAt = ref('')
const failedRuns = ref<AdminPipelineRun[]>([])
const firingAlerts = ref<AdminAlert[]>([])
const abnormalWorkloads = ref<AdminWorkload[]>([])
const runError = ref('')
const alertError = ref('')
const workloadError = ref('')

const hasIssues = computed(
  () => failedRuns.value.length + firingAlerts.value.length + abnormalWorkloads.value.length > 0
)
const anyError = computed(() => !!(runError.value || alertError.value || workloadError.value))

async function load() {
  const current = ++request
  loading.value = true
  const result = await fetchAttention(scope.tenantId)
  if (current !== request) return
  failedRuns.value = result.runs.records
  runError.value = result.runs.error
  firingAlerts.value = result.alerts.records
  alertError.value = result.alerts.error
  abnormalWorkloads.value = result.workloads.records
  workloadError.value = result.workloads.error
  loadedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  loading.value = false
}

useScopeChange(load)
onBeforeUnmount(() => { request++ })

onMounted(load)
</script>

<style scoped>
.attention-panel {
  margin-bottom: 12px;
}
.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.panel-body {
  min-height: 120px;
}
.src-error {
  margin-bottom: 8px;
}
.all-clear {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--el-text-color-secondary);
  padding: 16px 0;
}
.issue-group {
  margin-bottom: 12px;
}
.group-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin-bottom: 4px;
}
.issue-row {
  color: inherit;
  text-decoration: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 4px;
  cursor: pointer;
}
.issue-row:hover {
  background: var(--el-fill-color-light);
}
.issue-main {
  font-size: 13px;
  min-width: 160px;
}
.issue-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex: 1;
  text-align: right;
}
.snapshot-note {
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}
</style>
