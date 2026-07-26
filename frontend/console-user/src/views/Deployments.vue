<script setup lang="ts">
import { ref } from 'vue'
import { ElTable, ElTableColumn, ElTag, ElButton } from 'element-plus'

// 我的部署：管理已部署的模型实例。Plan 3 落地 InferenceDeployment CRD 后对接真实数据。
interface Deployment {
  id: string
  model: string
  replicas: string
  gpu: string
  status: 'running' | 'deploying' | 'failed'
  endpoint: string
}

const deployments = ref<Deployment[]>([
  { id: 'dep-1', model: 'Qwen2.5-7B-Instruct', replicas: '2/2', gpu: 'A100 ×1', status: 'running', endpoint: 'https://api.paas.dev/v1' },
])

function statusType(s: Deployment['status']) {
  return s === 'running' ? 'success' : s === 'failed' ? 'danger' : 'warning'
}
</script>

<template>
  <el-table :data="deployments" border>
    <el-table-column prop="model" label="模型" />
    <el-table-column prop="replicas" label="副本" width="100" />
    <el-table-column prop="gpu" label="GPU" width="120" />
    <el-table-column label="状态" width="120">
      <template #default="{ row }">
        <el-tag :type="statusType(row.status)">{{ row.status }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column prop="endpoint" label="推理端点" />
    <el-table-column label="操作" width="160">
      <template #default>
        <el-button size="small">扩缩容</el-button>
        <el-button size="small" type="danger">下线</el-button>
      </template>
    </el-table-column>
  </el-table>
</template>
