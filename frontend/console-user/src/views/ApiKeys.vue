<script setup lang="ts">
import { ref } from 'vue'
import { ElTable, ElTableColumn, ElButton, ElTag } from 'element-plus'

// API Key 管理：租户级密钥。Plan 2 落地 Identity/RBAC 后对接。
interface Key {
  id: string
  name: string
  prefix: string
  status: 'active' | 'revoked'
  createdAt: string
}

const keys = ref<Key[]>([
  { id: 'k1', name: '默认密钥', prefix: 'sk-paas-a1b2', status: 'active', createdAt: '2026-07-20' },
])
</script>

<template>
  <div class="bar">
    <el-button type="primary">创建 API Key</el-button>
  </div>
  <el-table :data="keys" border>
    <el-table-column prop="name" label="名称" />
    <el-table-column prop="prefix" label="前缀" />
    <el-table-column label="状态" width="120">
      <template #default="{ row }">
        <el-tag :type="row.status === 'active' ? 'success' : 'info'">{{ row.status }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column prop="createdAt" label="创建时间" width="160" />
    <el-table-column label="操作" width="120">
      <template #default>
        <el-button size="small" type="danger">吊销</el-button>
      </template>
    </el-table-column>
  </el-table>
</template>

<style scoped>
.bar {
  margin-bottom: 16px;
}
</style>
