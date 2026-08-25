<template>
  <el-drawer
    :model-value="modelValue"
    title="镜像详情"
    size="50%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadDetail"
    @close="onClose"
  >
    <div v-loading="loading">
      <el-empty v-if="!detail && !loading" description="暂无数据" />
      <template v-else-if="detail">
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="镜像 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="应用">{{ detail.appId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="Registry">{{ detail.registry || '-' }}</el-descriptions-item>
          <el-descriptions-item label="Tag">{{ detail.tag || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="detail.status === 'ready' ? 'success' : 'info'" size="small">
              {{ detail.status }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="Branch">{{ detail.branch || '-' }}</el-descriptions-item>
          <el-descriptions-item label="构建 ID">{{ detail.buildRunId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="构建时间" :span="2">{{ detail.builtAt ? formatDate(detail.builtAt) : '-' }}</el-descriptions-item>
          <el-descriptions-item label="Digest" :span="2">
            <code style="word-break: break-all">{{ detail.digest || '-' }}</code>
          </el-descriptions-item>
          <el-descriptions-item label="来源 Commit" :span="2">
            <code style="word-break: break-all">{{ detail.source || '-' }}</code>
          </el-descriptions-item>
        </el-descriptions>

        <div class="block">
          <el-alert
            title="镜像是不可变构建产物，无运维操作。"
            type="info"
            :closable="false"
            show-icon
          />
        </div>
      </template>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, onUnmounted } from 'vue'
import { formatDate } from '@/lib/format'
import { fetchImageDetail, type AdminImageDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const detail = ref<AdminImageDetail | null>(null)
const loading = ref(false)

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchImageDetail(props.id)
  } finally {
    loading.value = false
  }
}
const onClose = () => {
  detail.value = null
}
onUnmounted(onClose)
</script>

<style scoped>
.block {
  margin-bottom: 20px;
}
</style>
