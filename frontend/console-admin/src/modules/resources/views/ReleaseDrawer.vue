<template>
  <el-drawer
    :model-value="modelValue"
    title="发布详情"
    size="55%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadDetail"
    @close="onClose"
  >
    <div v-loading="loading">
      <template v-if="detail">
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="发布 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="应用">{{ detail.appId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.envId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.status)" size="small">{{ detail.status }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="策略">{{ detail.strategy || '-' }}</el-descriptions-item>
          <el-descriptions-item label="镜像 ID">{{ detail.imageId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="工作负载">{{ detail.workloadId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="回滚指针">{{ detail.previousImageId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="创建者">{{ detail.createdBy || '-' }}</el-descriptions-item>
          <el-descriptions-item label="回滚标记">
            <el-tag v-if="detail.isRollback" type="warning" size="small">回滚</el-tag>
            <span v-else>-</span>
          </el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ detail.createdAt ? formatDate(detail.createdAt) : '-' }}</el-descriptions-item>
          <el-descriptions-item label="镜像 Digest" :span="2">
            <code style="word-break: break-all">{{ detail.imageDigest || '-' }}</code>
          </el-descriptions-item>
        </el-descriptions>

        <div class="block">
          <div class="block-title">运维操作（绕过 prod:write，全记审计）</div>
          <el-space wrap>
            <el-button
              type="warning"
              size="small"
              :disabled="!canRollback"
              :loading="acting"
              @click="rollback"
            >
              回滚到上一镜像
            </el-button>
          </el-space>
          <div v-if="!canRollback" class="hint">该发布无可回滚的上一镜像（首次发布或已回滚过）。</div>
        </div>
      </template>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, computed, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { formatDate } from '@/lib/format'
import { fetchReleaseDetail, rollbackRelease, type AdminReleaseDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminReleaseDetail | null>(null)
const loading = ref(false)
const acting = ref(false)

const statusType = (s: string) =>
  (
    { succeeded: 'success', failed: 'danger', 'rolled-back': 'info', deploying: 'warning', pending: 'warning' } as Record<string, string>
  )[s] ?? 'info'

const canRollback = computed(
  () => !!detail.value && !!detail.value.previousImageId && detail.value.status === 'succeeded'
)

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchReleaseDetail(props.id)
  } finally {
    loading.value = false
  }
}
const onClose = () => {
  detail.value = null
}
onUnmounted(onClose)

const rollback = async () => {
  try {
    await ElMessageBox.confirm(
      '确认回滚该发布到上一镜像？此操作将影响目标环境的工作负载，绕过 prod:write 校验（super_admin 权限）。',
      '危险操作',
      { type: 'warning', confirmButtonText: '回滚' }
    )
  } catch {
    return
  }
  acting.value = true
  try {
    await rollbackRelease(props.id)
    ElMessage.success('已回滚')
    await loadDetail()
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
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-top: 6px;
}
</style>
