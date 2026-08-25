<template>
  <el-drawer
    :model-value="modelValue"
    title="流水线运行详情"
    size="60%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="start"
    @close="stop"
  >
    <el-empty v-if="!detail && !loading" description="暂无数据" />
    <div v-else-if="detail" v-loading="loading">
      <!-- 基本信息 -->
      <el-descriptions :column="2" border size="small" class="block">
        <el-descriptions-item label="运行 ID">{{ detail.id }}</el-descriptions-item>
        <el-descriptions-item label="租户">
          <el-tag v-if="detail.tenantId" size="small" type="info">{{ detail.tenantId }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="应用">{{ detail.appId }}</el-descriptions-item>
        <el-descriptions-item label="流水线">{{ detail.pipelineId }}</el-descriptions-item>
        <el-descriptions-item label="分支">{{ detail.branch || '-' }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag :type="statusType(detail.status)" size="small">{{ detail.status }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="当前阶段">{{ detail.currentStage || '-' }}</el-descriptions-item>
        <el-descriptions-item label="版本">
          <span v-if="detail.version" style="font-family: monospace">{{ detail.version }}</span>
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ detail.createdAt || '-' }}</el-descriptions-item>
        <el-descriptions-item label="结束时间">{{ detail.finishedAt || '-' }}</el-descriptions-item>
        <el-descriptions-item v-if="detail.commit" label="Commit" :span="2">
          <span style="font-family: monospace">{{ detail.commit }}</span>
        </el-descriptions-item>
      </el-descriptions>

      <!-- stage 时间线 -->
      <div class="block">
        <div class="block-title">阶段执行（{{ detail.stageRuns?.length ?? 0 }}）</div>
        <el-table :data="detail.stageRuns" size="small" empty-text="无阶段记录">
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="stage-expand">
                <div v-if="row.log"><strong>日志</strong><pre class="log-view">{{ row.log }}</pre></div>
                <div v-if="row.error" class="stage-error"><strong>错误</strong><pre class="log-view">{{ row.error }}</pre></div>
                <div v-if="row.output && Object.keys(row.output).length">
                  <strong>输出</strong>
                  <pre class="log-view">{{ JSON.stringify(row.output, null, 2) }}</pre>
                </div>
                <div v-if="!row.log && !row.error && !(row.output && Object.keys(row.output).length)" style="color: var(--el-text-color-secondary)">无详情</div>
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="stage" label="阶段类型" width="110" />
          <el-table-column prop="name" label="名称" min-width="130">
            <template #default="{ row }">{{ row.name || row.stage }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="startedAt" label="开始" width="170" />
          <el-table-column prop="finishedAt" label="结束" width="170" />
        </el-table>
      </div>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, onUnmounted } from 'vue'
import { fetchPipelineRunDetail, type AdminPipelineRunDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminPipelineRunDetail | null>(null)
const loading = ref(false)
let timer: number | undefined

const statusType = (s: string) =>
  (
    { running: 'primary', paused: 'warning', succeeded: 'success', failed: 'danger', aborted: 'info', pending: 'info', success: 'success', waiting: 'warning', skipped: 'info' } as Record<string, string>
  )[s] ?? 'info'

const load = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchPipelineRunDetail(props.id)
    emit('refresh')
  } finally {
    loading.value = false
  }
}
// 打开即加载 + 10s 轮询（与列表页同款；run 终态后停轮询）。
const start = () => {
  load()
  timer = window.setInterval(() => {
    if (detail.value && ['succeeded', 'failed', 'aborted'].includes(detail.value.status)) {
      if (timer) clearInterval(timer)
      return
    }
    load()
  }, 10000)
}
const stop = () => {
  if (timer) clearInterval(timer)
  timer = undefined
  detail.value = null
}
onUnmounted(stop)
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
.stage-expand {
  padding: 8px 12px;
}
.stage-error {
  color: var(--el-color-danger);
}
.log-view {
  background: var(--el-fill-color-darker, #1e1e1e);
  color: #e0e0e0;
  padding: 12px;
  border-radius: 4px;
  max-height: 360px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
