<template>
  <el-drawer
    :model-value="modelValue"
    title="数据服务详情"
    size="50%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <template v-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="实例 ID">{{ detail.resource.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.resource.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.resource.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="类型">{{ kindLabel(detail.resource.kind) }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.resource.envId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.resource.status)" size="small">{{ detail.resource.status || '-' }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="来源">{{ detail.resource.source || '-' }}</el-descriptions-item>
          <el-descriptions-item label="引擎">{{ detail.resource.engineId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="副本">{{ detail.resource.replicas ?? '-' }}</el-descriptions-item>
          <el-descriptions-item label="CPU/内存/存储">
            {{ detail.resource.cpu || '-' }} / {{ detail.resource.memory || '-' }} / {{ detail.resource.storageGb ?? '-' }}GB
          </el-descriptions-item>
        </el-descriptions>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作</div>
          <el-space wrap>
            <el-button
              v-if="detail.resource.status !== 'running'"
              type="success"
              size="small"
              :loading="acting"
              @click="run('start')"
            >启动</el-button>
            <el-button
              v-if="detail.resource.status === 'running'"
              type="warning"
              size="small"
              :loading="acting"
              @click="run('stop')"
            >停止</el-button>
            <el-button size="small" :loading="acting" @click="run('restart')">重启</el-button>
            <el-popover trigger="click" width="280" placement="bottom">
              <template #reference>
                <el-button size="small">扩缩容</el-button>
              </template>
              <el-form label-width="64px" size="small">
                <el-form-item label="副本">
                  <el-input-number v-model="scale.replicas" :min="0" :max="10" />
                </el-form-item>
                <el-form-item label="CPU">
                  <el-input v-model="scale.cpu" placeholder="如 1" />
                </el-form-item>
                <el-form-item label="内存">
                  <el-input v-model="scale.memory" placeholder="如 1Gi" />
                </el-form-item>
                <el-button type="primary" size="small" :loading="acting" @click="doScale">应用</el-button>
              </el-form>
            </el-popover>
            <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除</el-button>
          </el-space>
        </div>

        <!-- 运行实例 -->
        <div class="block">
          <div class="block-title">运行实例</div>
          <el-table :data="detail.instances" size="small" empty-text="未接入集群数据面或无运行实例">
            <el-table-column prop="name" label="Pod 名" />
            <el-table-column prop="ip" label="IP" />
            <el-table-column prop="port" label="端口" width="100" />
          </el-table>
        </div>

        <!-- 连接信息（掩码） -->
        <div v-if="detail.resource.connection && Object.keys(detail.resource.connection).length" class="block">
          <div class="block-title">连接信息（敏感字段已掩码）</div>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item v-for="(v, k) in detail.resource.connection" :key="k" :label="String(k)">
              {{ displayValue(k, v) }}
            </el-descriptions-item>
          </el-descriptions>
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
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, watch, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  fetchDataserviceDetail,
  dataserviceAction,
  scaleDataservice,
  deleteDataservice,
  fetchAuditLogList,
  type AdminDataserviceDetail,
  type AdminAuditLog
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminDataserviceDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)
const scale = ref<{ replicas?: number; cpu?: string; memory?: string }>({})
let timer: number | undefined

// SENSITIVE_KEYS 客户端兜底掩码（防御性深度：后端 maskDS 已掩码 password/secretKey/token/uri，
// 前端兜底防后端未来新增敏感字段忘记加入 MaskConnection 白名单时泄漏到 DOM）。
const SENSITIVE_KEYS = new Set([
  'password', 'secret', 'secretkey', 'token', 'apikey', 'api_key',
  'master_key', 'masterkey', 'accesskey', 'access_key', 'uri', 'connectionstring'
])
const displayValue = (k: string, v: unknown) =>
  SENSITIVE_KEYS.has(String(k).toLowerCase()) ? '••••••' : (v ?? '-')

const kindLabel = (k: string) =>
  (({ db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量库', search: '搜索引擎' }) as Record<string, string>)[k] ?? k
const statusType = (s: string) =>
  (({ running: 'success', creating: 'warning', stopped: 'info', failed: 'danger' }) as Record<string, string>)[s] ?? 'info'

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchDataserviceDetail(props.id)
  } finally {
    loading.value = false
  }
}
const loadAudits = async () => {
  if (!props.id) return
  const res = await fetchAuditLogList({ page: 1, size: 1000 })
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
}
onUnmounted(onClose)

const run = async (action: 'start' | 'stop' | 'restart') => {
  try {
    await ElMessageBox.confirm(`确认${action === 'stop' ? '停止' : action === 'start' ? '启动' : '重启'}该数据服务？`, '提示', { type: 'warning' })
  } catch {
    return
  }
  acting.value = true
  try {
    await dataserviceAction(props.id, action)
    ElMessage.success('已执行')
    await loadDetail()
    emit('refresh')
  } finally {
    acting.value = false
  }
}

const doScale = async () => {
  acting.value = true
  try {
    await scaleDataservice(props.id, scale.value)
    ElMessage.success('已扩缩容')
    await loadDetail()
    emit('refresh')
  } finally {
    acting.value = false
  }
}

const remove = async () => {
  try {
    await ElMessageBox.confirm('确认强制删除该数据服务？此操作不可恢复，将回收目标租户配额。', '危险操作', { type: 'error', confirmButtonText: '删除' })
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteDataservice(props.id)
    ElMessage.success('已删除')
    emit('update:modelValue', false)
    emit('refresh')
  } finally {
    acting.value = false
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
</style>
