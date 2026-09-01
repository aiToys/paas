<template>
  <el-drawer
    :model-value="modelValue"
    title="服务治理详情"
    size="50%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <el-empty v-if="!detail && !loading" description="暂无数据" />
      <template v-else-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="服务 ID">{{ detail.service.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.service.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.service.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="协议">
            <el-tag :type="detail.service.protocol === 'grpc' ? 'success' : 'primary'" size="small">{{ detail.service.protocol }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="端口">{{ detail.service.port }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.service.envId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="应用">{{ detail.service.appId || '-' }}</el-descriptions-item>
          <el-descriptions-item label="描述">{{ detail.service.desc || '-' }}</el-descriptions-item>
        </el-descriptions>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作</div>
          <el-space wrap>
            <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除服务</el-button>
          </el-space>
        </div>

        <!-- 实例列表 -->
        <div class="block">
          <div class="block-title">实例列表</div>
          <el-table :data="detail.instances" size="small" empty-text="无运行实例">
            <el-table-column prop="id" label="实例 ID" minWidth="160" />
            <el-table-column prop="addr" label="地址" minWidth="140" />
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag :type="row.status === 'healthy' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="laneId" label="泳道" width="100" />
            <el-table-column label="操作" width="100">
              <template #default="{ row }">
                <el-button type="danger" link size="small" :loading="acting" @click="deregister(row.id)">注销</el-button>
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
  </el-drawer>
</template>

<script lang="ts" setup>
import { visibleTick } from '@/app/composables/useAdminPolling'
import { ref, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { tableTimeFormatter } from "@/lib/format"
import {
  fetchServiceDetail,
  deregisterServiceInstance,
  deleteService,
  fetchAuditLogList,
  type AdminServiceDetail,
  type AdminServiceInstance,
  type AdminAuditLog
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminServiceDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)
let timer: number | undefined

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchServiceDetail(props.id)
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
  if (timer) clearInterval(timer) // 防重复打开叠加（R3-9）
  loadDetail()
  loadAudits()
  timer = window.setInterval(visibleTick(loadDetail), 10000)
}
const onClose = () => {
  if (timer) clearInterval(timer)
  timer = undefined
  detail.value = null
}
onUnmounted(onClose)

const deregister = async (instID: string) => {
  try {
    await ElMessageBox.confirm('确认注销该实例？', '提示', { type: 'warning' })
  } catch {
    return
  }
  acting.value = true
  try {
    await deregisterServiceInstance(props.id, instID)
    ElMessage.success('已注销')
    await loadDetail()
    emit('refresh')
  } finally {
    acting.value = false
  }
}

const remove = async () => {
  try {
    await ElMessageBox.confirm('确认强制删除该服务？此操作将级联清除所有实例，不可恢复。', '危险操作', { type: 'error', confirmButtonText: '删除' })
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteService(props.id)
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
