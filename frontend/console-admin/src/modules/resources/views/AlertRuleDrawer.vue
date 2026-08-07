<template>
  <el-drawer
    :model-value="modelValue"
    title="告警规则详情"
    size="45%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <template v-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="规则 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="指标">{{ detail.metricName }}</el-descriptions-item>
          <el-descriptions-item label="目标类型">{{ detail.targetType }}</el-descriptions-item>
          <el-descriptions-item label="目标 ID">{{ detail.targetId || '全部' }}</el-descriptions-item>
          <el-descriptions-item label="比较运算">{{ detail.operator }}</el-descriptions-item>
          <el-descriptions-item label="阈值">{{ detail.threshold }}</el-descriptions-item>
          <el-descriptions-item label="严重级别">
            <el-tag :type="severityType(detail.severity)" size="small">{{ detail.severity }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="启用">
            <el-tag :type="detail.enabled ? 'success' : 'info'" size="small">{{ detail.enabled ? '已启用' : '已禁用' }}</el-tag>
          </el-descriptions-item>
        </el-descriptions>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作</div>
          <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除规则</el-button>
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
import { ref, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  fetchAlertRuleDetail,
  deleteAlertRule,
  fetchAuditLogList,
  type AdminAlertRuleDetail,
  type AdminAuditLog
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminAlertRuleDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)
let timer: number | undefined

const severityType = (s: string) =>
  (({ critical: 'danger', warning: 'warning', info: 'info' }) as Record<string, string>)[s] ?? 'info'

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchAlertRuleDetail(props.id)
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
}
onUnmounted(onClose)

const remove = async () => {
  try {
    await ElMessageBox.confirm('确认强制删除该告警规则？此操作不可恢复。', '危险操作', { type: 'error', confirmButtonText: '删除' })
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteAlertRule(props.id)
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
