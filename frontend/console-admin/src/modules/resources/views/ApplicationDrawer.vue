<template>
  <el-drawer
    :model-value="modelValue"
    title="应用详情"
    size="50%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <template v-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="应用 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.env || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.status)" size="small">{{ detail.status || '-' }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="副本">{{ detail.replicas || '-' }}</el-descriptions-item>
          <el-descriptions-item label="描述" :span="2">{{ detail.desc || '-' }}</el-descriptions-item>
        </el-descriptions>

        <!-- 资源绑定 -->
        <div v-if="detail.bindings && detail.bindings.length" class="block">
          <div class="block-title">资源绑定（{{ detail.bindings.length }}）</div>
          <el-table :data="detail.bindings" size="small">
            <el-table-column prop="type" label="类型" width="120" />
            <el-table-column prop="name" label="名称" />
            <el-table-column prop="note" label="备注" />
          </el-table>
        </div>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作（绕过 prod:write，全记审计）</div>
          <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除（级联清理 + 回收配额）</el-button>
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
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { tableTimeFormatter } from '@/lib/format'
import {
  fetchApplicationDetail,
  deleteApplication,
  fetchAuditLogList,
  type AdminApplicationDetail,
  type AdminAuditLog
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminApplicationDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)

const statusType = (s: string) =>
  (({ healthy: 'success', degraded: 'warning', idle: 'info' }) as Record<string, string>)[s] ?? 'info'

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchApplicationDetail(props.id)
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
}
const onClose = () => {
  detail.value = null
  audits.value = []
}
onUnmounted(onClose)

const remove = async () => {
  try {
    await ElMessageBox.confirm(
      '确认强制删除该应用？此操作不可恢复，将级联清理该应用下的工作负载与应用配置，并回收目标租户应用配额。',
      '危险操作',
      { type: 'error', confirmButtonText: '删除' }
    )
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteApplication(props.id)
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
