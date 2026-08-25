<template>
  <el-drawer
    :model-value="modelValue"
    title="账单详情"
    size="45%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="load"
    @close="detail = null"
  >
    <el-empty v-if="!detail && !loading" description="暂无数据" />
    <div v-else-if="detail" v-loading="loading">
      <!-- 基本信息 -->
      <el-descriptions :column="2" border size="small" class="block">
        <el-descriptions-item label="账单 ID">{{ detail.id }}</el-descriptions-item>
        <el-descriptions-item label="租户">
          <el-tag size="small" type="info">{{ detail.tenantId }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="周期">{{ detail.period }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag :type="detail.status === 'paid' ? 'success' : 'danger'" size="small">
            {{ detail.status === 'paid' ? '已支付' : '未支付' }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="总额">¥{{ Number(detail.total).toFixed(2) }}</el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ detail.createdAt || '-' }}</el-descriptions-item>
        <el-descriptions-item v-if="detail.paidAt" label="支付时间">{{ detail.paidAt }}</el-descriptions-item>
      </el-descriptions>

      <!-- 账单明细 -->
      <div class="block">
        <div class="block-title">账单明细（{{ detail.items?.length ?? 0 }} 项）</div>
        <el-table :data="detail.items" size="small" empty-text="无明细">
          <el-table-column prop="resource" label="资源" min-width="140" />
          <el-table-column prop="quantity" label="数量" width="110" />
          <el-table-column label="单价" width="110">
            <template #default="{ row }">¥{{ Number(row.unitPrice).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column label="金额" width="110">
            <template #default="{ row }">¥{{ Number(row.amount).toFixed(2) }}</template>
          </el-table-column>
        </el-table>
      </div>

      <!-- 支付（未支付时显示，明细决策后操作） -->
      <div v-if="detail.status === 'unpaid'" class="block">
        <el-button type="success" :loading="paying" @click="doPay">标记已付</el-button>
      </div>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchBillDetail, payBill, type AdminBillDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminBillDetail | null>(null)
const loading = ref(false)
const paying = ref(false)

const load = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchBillDetail(props.id)
  } finally {
    loading.value = false
  }
}

const doPay = async () => {
  if (!detail.value) return
  try {
    await ElMessageBox.confirm(
      `确认将租户 ${detail.value.tenantId} 周期 ${detail.value.period} 的账单（¥${Number(detail.value.total).toFixed(2)}）标记为已支付？`,
      '支付确认',
      { type: 'warning', confirmButtonText: '确认支付' }
    )
  } catch {
    return
  }
  paying.value = true
  try {
    await payBill(props.id)
    ElMessage.success('已标记为已支付')
    emit('update:modelValue', false)
    emit('refresh')
  } finally {
    paying.value = false
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
