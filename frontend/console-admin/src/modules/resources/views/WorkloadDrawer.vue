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
      <template v-if="detail">
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
            <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除</el-button>
          </el-space>
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
                <el-table-column prop="at" label="时间" width="170" />
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
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  fetchWorkloadDetail,
  scaleWorkload,
  deleteWorkload,
  fetchWorkloadLogs,
  fetchAuditLogList,
  type AdminWorkloadDetail,
  type AdminAuditLog
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminWorkloadDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)
const scale = ref<{ replicas?: number }>({})
let timer: number | undefined

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
  } finally {
    loading.value = false
  }
}
const loadAudits = async () => {
  if (!props.id) return
  const res = await fetchAuditLogList({ page: 1, size: 50 })
  audits.value = (res.records ?? []).filter((a) => a.resourceId === props.id)
}
const loadAll = () => {
  loadDetail()
  loadAudits()
  timer = window.setInterval(loadDetail, 10000)
}
const onClose = () => {
  if (timer) clearInterval(timer)
  timer = undefined
  detail.value = null
  scale.value = {}
}
onUnmounted(onClose)

const doScale = async () => {
  if (scale.value.replicas === undefined) return
  acting.value = true
  try {
    await scaleWorkload(props.id, { replicas: scale.value.replicas })
    ElMessage.success('已扩缩容')
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
