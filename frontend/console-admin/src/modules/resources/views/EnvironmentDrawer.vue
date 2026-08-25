<template>
  <el-drawer
    :model-value="modelValue"
    title="环境详情"
    size="45%"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="load"
    @close="detail = null"
  >
    <el-empty v-if="!detail && !loading" description="暂无数据" />
    <div v-else-if="detail" v-loading="loading">
      <!-- 基本信息 -->
      <el-descriptions :column="2" border size="small" class="block">
        <el-descriptions-item label="环境 ID">{{ detail.id }}</el-descriptions-item>
        <el-descriptions-item label="租户">
          <el-tag size="small" type="info">{{ detail.tenantId }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="名称">{{ detail.name }}</el-descriptions-item>
        <el-descriptions-item label="类型">
          <el-tag :type="detail.type === 'prod' ? 'danger' : 'success'" size="small">
            {{ detail.type === 'prod' ? '生产' : '测试' }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="集群">{{ detail.cluster || '-' }}</el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ detail.createdAt || '-' }}</el-descriptions-item>
        <el-descriptions-item label="描述" :span="2">{{ detail.desc || '-' }}</el-descriptions-item>
      </el-descriptions>

      <!-- 环境内工作负载（前端聚合跨租户列表按 envId 过滤） -->
      <div class="block">
        <div class="block-title">环境内工作负载（{{ workloads.length }}）</div>
        <el-table :data="workloads" size="small" empty-text="该环境暂无工作负载">
          <el-table-column prop="appId" label="应用" min-width="130" />
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="type" label="类型" width="90" />
          <el-table-column label="副本" width="90">
            <template #default="{ row }">{{ row.ready ?? 0 }} / {{ row.replicas ?? 0 }}</template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="110" />
        </el-table>
      </div>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref } from 'vue'
import {
  fetchEnvironmentDetail,
  fetchWorkloadList,
  type AdminEnvironment,
  type AdminWorkload
} from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const detail = ref<AdminEnvironment | null>(null)
const workloads = ref<AdminWorkload[]>([])
const loading = ref(false)

const load = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchEnvironmentDetail(props.id)
    // 环境内工作负载：跨租户列表按 envId 过滤（取大页全量再前端聚合，量级可控）。
    const res = await fetchWorkloadList({ keyword: '', tenantId: '', page: 1, size: 5000 })
    workloads.value = (res.records ?? []).filter((w) => w.envId === props.id)
  } finally {
    loading.value = false
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
