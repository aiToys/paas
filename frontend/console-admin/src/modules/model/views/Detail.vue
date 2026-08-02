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
      <span class="hint">通道按优先级升序路由；健康通道参与请求级 failover。</span>
      <div>
        <el-button type="primary" :icon="Plus" @click="openChannel(null)">新建通道</el-button>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>
    </div>

    <el-table v-loading="loading" :data="channels" border row-key="id">
      <el-table-column prop="id" label="通道 ID" min-width="180" />
      <el-table-column prop="type" label="类型" width="140" />
      <el-table-column prop="priority" label="优先级" width="80" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
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

const statusType = (s: string) => {
  if (s === 'healthy') return 'success'
  if (s === 'degraded') return 'warning'
  return 'danger'
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
</style>
