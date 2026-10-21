<template>
  <el-drawer
    :model-value="modelValue"
    title="密钥详情"
    size="45%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadAll"
    @close="onClose"
  >
    <div v-loading="loading">
      <el-empty v-if="!detail && !loading" description="暂无数据" />
      <template v-else-if="detail">
        <!-- 基本信息 -->
        <el-descriptions :column="2" border size="small" class="block">
          <el-descriptions-item label="密钥 ID">{{ detail.id }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detail.name }}</el-descriptions-item>
          <el-descriptions-item label="租户">{{ detail.tenantId || '平台级' }}</el-descriptions-item>
          <el-descriptions-item label="作用域">
            <el-tag :type="detail.scope === 'platform' ? 'warning' : 'primary'" size="small">{{ detail.scope }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="类型">
            <el-tag size="small">{{ detail.type }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="值（掩码）">
            <code class="masked-value">{{ detail.value }}</code>
          </el-descriptions-item>
          <el-descriptions-item label="描述">{{ detail.desc || '-' }}</el-descriptions-item>
        </el-descriptions>

        <!-- 运维操作 -->
        <div class="block">
          <div class="block-title">运维操作</div>
          <el-button type="danger" size="small" :loading="acting" @click="remove">强制删除密钥</el-button>
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
  fetchSecretDetail,
  deleteSecret,
  fetchAuditLogList,
  type AdminSecretDetail,
  type AdminAuditLog
} from '../api'
import { fetchVendorList, fetchModelList } from '@/modules/model/api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminSecretDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
const acting = ref(false)
let timer: number | undefined

const loadDetail = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchSecretDetail(props.id)
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

const remove = async () => {
  // 引用预检（spec 2026-10-19 D2b）：供应商（credentialRef）+ 通道（credentialRef）双路；
  // 无安全同步能力——只列引用，不提供一键替换/传播。
  let refLines = ''
  try {
    const [vendors, models] = await Promise.all([fetchVendorList(), fetchModelList()])
    const refs: string[] = []
    for (const v of vendors ?? []) {
      if (v.credentialRef === props.id) refs.push(`  · 供应商 ${v.name} (${v.id})`)
    }
    for (const m of models ?? []) {
      for (const ch of m.channels ?? []) {
        if (ch.credentialRef === props.id) refs.push(`  · 模型 ${m.id} / 通道 ${ch.id}`)
      }
    }
    if (refs.length > 0) {
      const more = refs.length > 8 ? `\n  …共 ${refs.length} 条` : ''
      refLines = `\n\n该密钥被 ${refs.length} 处引用：\n${refs.slice(0, 8).join('\n')}${more}\n（删除后引用方凭证解析将失败，需逐个更新；平台无凭证同步能力）`
    }
  } catch {
    refLines = '\n\n（引用预检加载失败，无法确认引用数量）'
  }
  try {
    await ElMessageBox.confirm(
      `确认强制删除该密钥？此操作不可恢复。${refLines}`,
      '危险操作',
      { type: 'error', confirmButtonText: '删除' }
    )
  } catch {
    return
  }
  acting.value = true
  try {
    await deleteSecret(props.id)
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
.masked-value {
  font-family: monospace;
  color: var(--el-text-color-secondary);
}
</style>
