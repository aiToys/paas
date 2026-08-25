<template>
  <el-drawer
    :model-value="modelValue"
    title="构建详情"
    size="60%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadDetail"
    @close="onClose"
  >
    <div v-loading="loading">
      <el-empty v-if="!detail && !loading" description="暂无数据" />
      <template v-else-if="detail">
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="构建 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="应用">{{ detail.appId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="仓库">{{ detail.repoId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusType(detail.status)" size="small">{{ detail.status }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="触发">{{ detail.trigger || '-' }}</el-descriptions-item>
          <el-descriptions-item label="Commit">{{ detail.commit || '-' }}</el-descriptions-item>
          <el-descriptions-item label="分支">{{ detail.branch || '-' }}</el-descriptions-item>
          <el-descriptions-item label="镜像 ID" :span="2">{{ detail.imageId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="提交信息" :span="2">{{ detail.message || '-' }}</el-descriptions-item>
          <el-descriptions-item label="开始时间">{{ detail.startedAt ? formatDate(detail.startedAt) : '-' }}</el-descriptions-item>
          <el-descriptions-item label="结束时间">{{ detail.finishedAt ? formatDate(detail.finishedAt) : '-' }}</el-descriptions-item>
        </el-descriptions>

        <div class="block">
          <div class="block-title">
            构建日志
            <el-button link type="primary" size="small" @click="logExpanded = !logExpanded">
              {{ logExpanded ? '收起' : '展开' }}
            </el-button>
          </div>
          <pre v-if="logExpanded" class="log-view">{{ detail.log || '（无日志）' }}</pre>
        </div>
      </template>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, onUnmounted } from 'vue'
import { formatDate } from '@/lib/format'
import { fetchBuildRunDetail, type AdminBuildRunDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const detail = ref<AdminBuildRunDetail | null>(null)
const loading = ref(false)
const logExpanded = ref(false)

const statusType = (s: string) =>
  (({ success: 'success', running: 'warning', failed: 'danger', pending: 'info' }) as Record<
    string,
    string
  >)[s] ?? 'info'

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchBuildRunDetail(props.id)
  } finally {
    loading.value = false
  }
}
const onClose = () => {
  detail.value = null
  logExpanded.value = false
}
onUnmounted(onClose)
</script>

<style scoped>
.block {
  margin-bottom: 20px;
}
.block-title {
  font-weight: 600;
  margin-bottom: 8px;
  color: var(--el-text-color-primary);
  display: flex;
  align-items: center;
  gap: 8px;
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
