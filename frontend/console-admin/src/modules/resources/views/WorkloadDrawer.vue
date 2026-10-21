<template>
  <el-drawer
    :model-value="modelValue"
    title="工作负载详情"
    size="60%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <el-empty v-if="!detail && !loading" description="暂无数据" />
      <template v-else-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="工作负载 ID">{{ detail.workload.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.workload.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.workload.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="所属应用">{{ detail.workload.appId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.workload.envId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="类型">
            <el-tag size="small" type="info">{{ typeLabel(detail.workload.type) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.workload.status)" size="small">{{ detail.workload.status || '-' }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="副本">{{ detail.workload.ready ?? 0 }} / {{ detail.workload.replicas ?? 0 }}</el-descriptions-item>
          <el-descriptions-item label="镜像" :span="2">{{ detail.workload.image || '-' }}</el-descriptions-item>
          <el-descriptions-item v-if="detail.workload.schedule" label="调度">
            {{ detail.workload.schedule }}
          </el-descriptions-item>
          <el-descriptions-item v-if="detail.workload.command" label="启动命令">
            {{ detail.workload.command }}
          </el-descriptions-item>
        </el-descriptions>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作（绕过 prod:write，全记审计）</div>
          <el-space wrap>
            <el-popover trigger="click" width="240" placement="bottom">
              <template #reference>
                <el-button size="small">扩缩容</el-button>
              </template>
              <el-form label-width="64px" size="small">
                <el-form-item label="副本">
                  <el-input-number v-model="scale.replicas" :min="0" :max="100" />
                </el-form-item>
                <el-button type="primary" size="small" :loading="acting" @click="doScale">应用</el-button>
              </el-form>
            </el-popover>
            <el-button
              size="small"
              :disabled="detail.workload.type !== 'service' || !detail.workload.replicas"
              @click="openRestart"
            >
              滚动重启
            </el-button>
            <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除</el-button>
          </el-space>
          <div class="ops-note">
            滚动重启需填写原因；平台返回「已受理」不等于已生效——以下方操作记录 status 为准。{{
              envHint
            }}
          </div>
        </div>

        <!-- 运维操作记录（受理/执行/生效证据；没有终态源就保持未知） -->
        <div class="block">
          <div class="block-title">
            运维操作记录
            <el-button link type="primary" size="small" class="title-action" @click="loadOperations">刷新</el-button>
          </div>
          <el-table :data="operations" size="small" empty-text="无运维操作记录">
            <el-table-column label="类型" width="90">
              <template #default="{ row }">{{ opKindLabel(row.kind) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="110">
              <template #default="{ row }">
                <el-tag :type="opStatusType(row.status)" size="small">{{ opStatusLabel(row.status) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
            <el-table-column prop="actor" label="操作者" width="120" show-overflow-tooltip />
            <el-table-column prop="acceptedAt" label="受理时间" width="170" :formatter="tableTimeFormatter" />
            <el-table-column prop="updatedAt" label="最近观察" width="170" :formatter="tableTimeFormatter" />
            <el-table-column label="证据" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">{{ row.evidence || '-' }}</template>
            </el-table-column>
          </el-table>
        </div>

        <!-- 运行实例 -->
        <div class="block">
          <div class="block-title">运行实例（Pod 级）</div>
          <el-table :data="detail.instances" size="small" empty-text="未接入集群数据面或无运行实例">
            <el-table-column prop="name" label="Pod 名" min-width="200" />
            <el-table-column prop="status" label="状态" width="100" />
            <el-table-column prop="ready" label="就绪" width="80" />
            <el-table-column prop="restarts" label="重启" width="70" />
            <el-table-column prop="node" label="节点" width="130" />
            <el-table-column prop="ip" label="IP" width="130" />
            <el-table-column label="日志" width="80" fixed="right">
              <template #default="{ row }">
                <el-button type="primary" link size="small" @click="openLogs(row.name)">查看</el-button>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <!-- 操作历史（审计） -->
        <div class="block">
          <el-collapse>
            <el-collapse-item title="操作历史" name="audit">
              <el-table :data="audits" size="small" empty-text="无操作记录">
                <el-table-column prop="at" label="时间" width="170" :formatter="tableTimeFormatter" />
                <el-table-column prop="actor" label="操作者" width="130" />
                <el-table-column prop="action" label="动作" width="130" />
                <el-table-column prop="detail" label="详情" />
              </el-table>
            </el-collapse-item>
          </el-collapse>
        </div>
      </template>
    </div>

    <!-- 日志对话框 -->
    <el-dialog v-model="logs.visible" title="实例日志" width="70%" append-to-body>
      <el-form :inline="true" size="small" class="log-form">
        <el-form-item label="Pod">
          <el-input v-model="logs.pod" readonly style="width: 280px" />
        </el-form-item>
        <el-form-item label="行数">
          <el-input-number v-model="logs.tail" :min="100" :max="10000" :step="500" />
        </el-form-item>
        <el-form-item label="上次终止">
          <el-switch v-model="logs.previous" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="loadLogs">刷新</el-button>
        </el-form-item>
      </el-form>
      <pre class="log-view">{{ logs.text || '（加载中...）' }}</pre>
    </el-dialog>

    <!-- 滚动重启（原因必填；受理 ≠ 生效） -->
    <el-dialog v-model="restart.visible" title="滚动重启工作负载" width="480px" append-to-body>
      <el-alert
        v-if="envType === 'prod'"
        type="warning"
        :closable="false"
        show-icon
        title="目标是生产环境"
        description="后台代操作绕过 prod:write，将以你的账号记审计。请确认已完成变更评审。"
        class="restart-alert"
      />
      <el-alert
        v-else-if="!envType"
        type="info"
        :closable="false"
        show-icon
        title="环境类型未知"
        description="未能读取目标环境类型，可能包含生产环境；操作同样记审计。"
        class="restart-alert"
      />
      <el-form label-width="64px">
        <el-form-item label="原因" required>
          <el-input
            v-model="restart.reason"
            type="textarea"
            :rows="3"
            maxlength="200"
            show-word-limit
            placeholder="例如：实例内存异常增长，按变更单 CHG-123 重启"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="restart.visible = false">取消</el-button>
        <el-button type="primary" :loading="restart.submitting" :disabled="!restart.reason.trim()" @click="doRestart">
          提交重启
        </el-button>
      </template>
    </el-dialog>
  </el-drawer>
</template>

<script lang="ts" setup>
import { visibleTick } from '@/app/composables/useAdminPolling'
import { computed, ref, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { tableTimeFormatter } from "@/lib/format"
import {
  fetchWorkloadDetail,
  scaleWorkload,
  deleteWorkload,
  fetchWorkloadLogs,
  fetchAuditLogList,
  restartWorkload,
  fetchWorkloadOperations,
  fetchEnvironmentDetail,
  type AdminWorkloadDetail,
  type AdminAuditLog,
  type WorkloadOperation
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminWorkloadDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const operations = ref<WorkloadOperation[]>([])
const loading = ref(false)
const acting = ref(false)
const scale = ref<{ replicas?: number }>({})
// 环境类型（prod 提示用）：'' = 未读取/读取失败——不猜测为非生产。
const envType = ref('')
const restart = ref<{ visible: boolean; reason: string; submitting: boolean }>({
  visible: false,
  reason: '',
  submitting: false
})
let timer: number | undefined

const envHint = computed(() => {
  if (!envType.value) return '目标环境类型未知。'
  return envType.value === 'prod' ? '目标是生产环境。' : '目标是测试环境。'
})

const opKindLabel = (k: string) => (({ restart: '滚动重启', recreate: '重建实例' }) as Record<string, string>)[k] ?? k
// 语义：accepted/reconciling/running 都是「进行中」，succeeded 才算确认完成，
// unknown/superseded 如实显示，不折叠成成功或失败。
const opStatusLabel = (s: string) =>
  (
    {
      accepted: '已受理',
      reconciling: '下发中',
      running: '执行中',
      succeeded: '已完成',
      failed: '失败',
      superseded: '已被覆盖',
      unknown: '无法确认'
    } as Record<string, string>
  )[s] ?? s
const opStatusType = (s: string) =>
  (
    {
      succeeded: 'success',
      failed: 'danger',
      accepted: 'info',
      reconciling: 'warning',
      running: 'warning',
      superseded: 'info',
      unknown: 'info'
    } as Record<string, string>
  )[s] ?? 'info'

const logs = ref<{ visible: boolean; pod: string; tail: number; previous: boolean; text: string }>({
  visible: false,
  pod: '',
  tail: 1000,
  previous: false,
  text: ''
})

const typeLabel = (t: string) =>
  (({ service: '服务', job: '任务', cronjob: '定时' }) as Record<string, string>)[t] ?? t
const statusType = (s: string) =>
  (
    { running: 'success', deploying: 'warning', stopped: 'info', failed: 'danger' } as Record<string, string>
  )[s] ?? 'info'

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchWorkloadDetail(props.id)
    // 初始化扩缩容输入为当前副本
    if (scale.value.replicas === undefined && detail.value?.workload.replicas !== undefined) {
      scale.value.replicas = detail.value.workload.replicas
    }
    await loadEnvType(detail.value?.workload.envId)
    await loadOperations()
  } finally {
    loading.value = false
  }
}

// 环境类型只用于提示（生产/测试/未知）；读取失败保持空串，不默认按测试环境处理。
const loadEnvType = async (envId?: string) => {
  if (!envId) {
    envType.value = ''
    return
  }
  try {
    const env = await fetchEnvironmentDetail(envId)
    envType.value = env?.type ?? ''
  } catch {
    envType.value = ''
  }
}
const loadAudits = async () => {
  if (!props.id) return
  const res = await fetchAuditLogList({ page: 1, size: 1000 })
  audits.value = (res.records ?? []).filter((a) => a.resourceId === props.id)
}
// 操作记录是「是否生效」的唯一证据源；失败保持上一次结果（不打成空表冒充无记录）。
const loadOperations = async () => {
  if (!props.id) return
  try {
    operations.value = await fetchWorkloadOperations(props.id, { limit: 50 })
  } catch {
    /* 保留原记录；下次轮询重试 */
  }
}
const loadAll = () => {
  if (timer) clearInterval(timer) // 防重复打开叠加（R3-9）
  loadDetail()
  loadAudits()
  loadOperations()
  // 轮询并列刷操作记录（R4 审计 F5）：它是「是否生效」的唯一证据源，
  // 只刷 detail 不刷 operations，用户提交重启/伸缩后状态永远停在受理态。
  timer = window.setInterval(visibleTick(() => { loadDetail(); loadOperations() }), 10000)
}
const onClose = () => {
  if (timer) clearInterval(timer)
  timer = undefined
  detail.value = null
  scale.value = {}
  operations.value = []
  envType.value = ''
  restart.value = { visible: false, reason: '', submitting: false }
}
onUnmounted(onClose)

const openRestart = () => {
  restart.value = { visible: true, reason: '', submitting: false }
}
const doRestart = async () => {
  const reason = restart.value.reason.trim()
  if (!reason) return
  restart.value.submitting = true
  try {
    const op = await restartWorkload(props.id, reason)
    // 202 只表示受理：文案明确指向操作记录，不宣称已生效。
    ElMessage.success(`已受理（${op.id}），生效与否请看操作记录状态`)
    restart.value.visible = false
    await loadOperations()
    emit('refresh')
  } finally {
    restart.value.submitting = false
  }
}

const doScale = async () => {
  if (scale.value.replicas === undefined) return
  acting.value = true
  try {
    await scaleWorkload(props.id, { replicas: scale.value.replicas })
    ElMessage.success('伸缩指令已下发，副本数由控制器异步推进')
    await loadDetail()
    emit('refresh')
  } finally {
    acting.value = false
  }
}

const remove = async () => {
  try {
    await ElMessageBox.confirm(
      '确认强制删除该工作负载？此操作不可恢复，将回收目标租户配额。',
      '危险操作',
      { type: 'error', confirmButtonText: '删除' }
    )
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteWorkload(props.id)
    ElMessage.success('已删除')
    emit('update:modelValue', false)
    emit('refresh')
  } finally {
    acting.value = false
  }
}

const openLogs = (pod: string) => {
  logs.value.visible = true
  logs.value.pod = pod
  logs.value.previous = false
  logs.value.text = ''
  loadLogs()
}
const loadLogs = async () => {
  if (!props.id || !logs.value.pod) return
  try {
    logs.value.text = await fetchWorkloadLogs(props.id, {
      pod: logs.value.pod,
      tail: logs.value.tail,
      previous: logs.value.previous
    })
  } catch (e) {
    logs.value.text = `加载失败: ${e instanceof Error ? e.message : String(e)}`
  }
}
</script>

<style scoped>
.block {
  margin-bottom: 20px;
}
.block-title {
  font-weight: 600;
  margin-bottom: 8px;
  color: var(--el-text-color-primary);
}
.log-form {
  margin-bottom: 12px;
}
.ops-note {
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}
.title-action {
  margin-left: 8px;
}
.restart-alert {
  margin-bottom: 12px;
}
.log-view {
  background: var(--el-fill-color-darker, #1e1e1e);
  color: #e0e0e0;
  padding: 12px;
  border-radius: 4px;
  max-height: 480px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
