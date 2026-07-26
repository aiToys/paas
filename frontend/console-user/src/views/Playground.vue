<script setup lang="ts">
import { ref } from 'vue'
import { ElInput, ElButton, ElSelect, ElOption, ElCard } from 'element-plus'

// Playground：交互式推理测试。Plan 4 接入 Gateway 的 /v1/chat/completions。
const model = ref('qwen2.5-7b')
const input = ref('')
const output = ref('')

async function onInfer() {
  if (!input.value) return
  // TODO(Plan 4): POST /v1/chat/completions 流式回填 output
  output.value = `[${model.value}] 暂未接入推理后端，Plan 4 落地 Inference Gateway 后启用流式输出。`
}
</script>

<template>
  <el-card>
    <div class="bar">
      <el-select v-model="model" placeholder="选择模型" style="width: 240px">
        <el-option label="Qwen2.5-7B" value="qwen2.5-7b" />
        <el-option label="Qwen2.5-72B" value="qwen2.5-72b" />
        <el-option label="DeepSeek-V3" value="deepseek-v3" />
      </el-select>
      <el-button type="primary" @click="onInfer">发送</el-button>
    </div>
    <el-input v-model="input" type="textarea" :rows="6" placeholder="输入提示词…" />
    <div class="label">输出</div>
    <div class="output">{{ output }}</div>
  </el-card>
</template>

<style scoped>
.bar {
  display: flex;
  gap: 12px;
  margin-bottom: 12px;
}
.label {
  margin: 16px 0 8px;
  color: #888;
  font-size: 13px;
}
.output {
  min-height: 120px;
  padding: 12px;
  background: #fafafa;
  border-radius: 4px;
  white-space: pre-wrap;
}
</style>
