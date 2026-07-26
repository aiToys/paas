<script setup lang="ts">
import { ref } from 'vue'
import { ElCard, ElTag, ElButton, ElRow, ElCol } from 'element-plus'

// 模型市场：浏览可部署的模型。数据待 Plan 4 接入 Gateway 后从 /v1/models 拉取。
interface Model {
  id: string
  name: string
  provider: string
  context: string
  tags: string[]
}

const models = ref<Model[]>([
  { id: 'qwen2.5-7b', name: 'Qwen2.5-7B-Instruct', provider: 'Qwen', context: '32K', tags: ['对话', '中文'] },
  { id: 'qwen2.5-72b', name: 'Qwen2.5-72B-Instruct', provider: 'Qwen', context: '128K', tags: ['对话', '旗舰'] },
  { id: 'deepseek-v3', name: 'DeepSeek-V3', provider: 'DeepSeek', context: '64K', tags: ['推理', 'MoE'] },
  { id: 'bge-m3', name: 'BGE-M3', provider: 'BAAI', context: '8K', tags: ['Embedding'] },
])

function onDeploy(m: Model) {
  // 跳转到部署页并预填模型；Plan 3 落地部署流程后补全。
  console.log('deploy', m.id)
}
</script>

<template>
  <el-row :gutter="16">
    <el-col v-for="m in models" :key="m.id" :span="8">
      <el-card class="model-card">
        <div class="title">{{ m.name }}</div>
        <div class="meta">{{ m.provider }} · 上下文 {{ m.context }}</div>
        <div class="tags">
          <el-tag v-for="t in m.tags" :key="t" size="small" class="tag">{{ t }}</el-tag>
        </div>
        <el-button type="primary" size="small" @click="onDeploy(m)">部署</el-button>
      </el-card>
    </el-col>
  </el-row>
</template>

<style scoped>
.model-card {
  margin-bottom: 16px;
}
.title {
  font-weight: 600;
  font-size: 16px;
}
.meta {
  color: #888;
  margin: 8px 0;
  font-size: 13px;
}
.tags {
  margin-bottom: 12px;
}
.tag {
  margin-right: 6px;
}
</style>
