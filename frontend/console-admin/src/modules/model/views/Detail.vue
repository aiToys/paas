<template>
  <div class="page">
    <div class="header">
      <el-button :icon="ArrowLeft" @click="router.back()">返回</el-button>
      <div v-if="model" class="title">
        <h2>{{ model.name }}</h2>
        <el-tag size="small">{{ model.id }}</el-tag>
        <el-tag size="small" type="info">{{ model.vendor }}</el-tag>
        <span class="ctx">上下文 {{ model.contextWindow }}</span>
      </div>
    </div>

    <div class="toolbar">
      <span class="hint">
        通道按优先级升序路由；「最近诊断」是网关运行态投影（请求级 failover 打标实时回显，无主动探活，重启后重置），
        配置成功不等于上游可达。
      </span>
      <div>
        <el-button type="primary" :icon="Plus" @click="openChannel(null)">新建通道</el-button>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>
    </div>

    <el-table v-loading="loading" :data="channels" border row-key="id">
      <el-table-column prop="id" label="通道 ID" min-width="180" />
      <el-table-column label="供应商" width="120">
        <template #default="{ row }">
          <span v-if="row.vendorId">{{ row.vendorId }}</span>
          <span v-else class="dim">直填</span>
        </template>
      </el-table-column>
      <el-table-column prop="type" label="类型" width="140" />
      <el-table-column prop="priority" label="优先级" width="80" />
      <el-table-column label="配置" width="110">
        <template #default="{ row }">
          <el-tag :type="configType(row)" size="small">{{ configText(row) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近诊断" width="200">
        <template #default="{ row }">
          <el-tag v-if="!row.status || row.status === 'healthy'" type="info" size="small">
            未诊断（无失败记录）
          </el-tag>
          <el-tag v-else-if="row.status === 'degraded'" type="warning" size="small">
            降级（请求失败被切走）
          </el-tag>
          <el-tag v-else type="danger" size="small">离线（摘出路由）</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="endpoint" label="BaseURL" min-width="200" show-overflow-tooltip />
      <el-table-column prop="upstreamModel" label="上游模型" min-width="140" />
      <el-table-column prop="credentialRef" label="凭证" min-width="170" show-overflow-tooltip />
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="openChannel(row)">编辑</el-button>
          <el-button link type="danger" size="small" @click="handleDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <ChannelFormDrawer
      v-model="channelDrawer"
      :mode="channelMode"
      :data="channelData"
      :model-id="modelId"
      @success="onChannelSuccess"
    />
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import {
  fetchModel,
  fetchChannels,
  deleteChannel,
  type ModelInfo,
  type ModelChannel
} from '../api'
import ChannelFormDrawer from './ChannelFormDrawer.vue'

const route = useRoute()
const router = useRouter()
const modelId = computed(() => route.params.id as string)

const model = ref<ModelInfo | null>(null)
const channels = ref<ModelChannel[]>([])
const loading = ref(false)

const channelDrawer = ref(false)
const channelMode = ref<'add' | 'edit'>('add')
const channelData = ref<ModelChannel | null>(null)

// 配置状态（spec M07 与观测分离）：echo/mock 进程内无需凭证；
// openai-compatible 看 credentialRef（vendorId 选定即快照带入，此处即最终真值）。
const needsCredential = (row: ModelChannel) => row.type === 'openai-compatible'
const configText = (row: ModelChannel) => {
  if (!needsCredential(row)) return '无需凭证'
  return row.credentialRef ? '已配置' : '缺少凭证'
}
const configType = (row: ModelChannel) => {
  if (!needsCredential(row)) return 'info'
  return row.credentialRef ? 'success' : 'danger'
}

const load = async () => {
  loading.value = true
  try {
    const [m, chs] = await Promise.all([fetchModel(modelId.value), fetchChannels(modelId.value)])
    model.value = m
    channels.value = chs ?? []
  } catch {
    // 拦截器提示
  } finally {
    loading.value = false
  }
}

const openChannel = (row: ModelChannel | null) => {
  channelData.value = row
  channelMode.value = row ? 'edit' : 'add'
  channelDrawer.value = true
}

const onChannelSuccess = () => {
  channelDrawer.value = false
  load()
}

const handleDelete = async (row: ModelChannel) => {
  try {
    await ElMessageBox.confirm(`确定删除通道 "${row.id}"？`, '删除通道', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning'
    })
  } catch {
    return
  }
  try {
    await deleteChannel(modelId.value, row.id)
    ElMessage.success('删除成功')
    load()
  } catch {
    // 拦截器提示
  }
}

onMounted(() => {
  load()
})
</script>

<style scoped>
.page {
  padding: 16px;
}
.header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}
.title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.title h2 {
  margin: 0;
  font-size: 18px;
}
.title .ctx {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.dim {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
