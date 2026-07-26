<script setup lang="ts">
import { ref } from 'vue'
import { ElInput, ElButton, ElSelect, ElOption, ElCard, ElMessage } from 'element-plus'

// Playground：交互式推理测试，直连 Platform Core Gateway（OpenAI 兼容 SSE）。
// 本切片 API Key 为开发默认值；生产应从用户会话/Identity 获取（Plan 2）。
const API_KEY = 'sk-paas-dev-key'

const model = ref('echo')
const input = ref('')
const output = ref('')
const loading = ref(false)

// SSE 行解析：把 Reader 读到的字节按 \n\n 分块，提取 data: {...} 的 delta.content。
async function streamChat() {
  if (!input.value.trim() || loading.value) return
  loading.value = true
  output.value = ''

  const resp = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${API_KEY}`,
    },
    body: JSON.stringify({
      model: model.value,
      messages: [{ role: 'user', content: input.value }],
      stream: true,
    }),
  })

  if (!resp.ok || !resp.body) {
    loading.value = false
    ElMessage.error(`请求失败：HTTP ${resp.status}`)
    return
  }

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    // 按 SSE 事件边界（空行）切分
    const parts = buffer.split('\n\n')
    buffer = parts.pop() ?? ''
    for (const part of parts) {
      const line = part.trim()
      if (!line.startsWith('data:')) continue
      const data = line.slice(5).trim()
      if (data === '[DONE]') {
        loading.value = false
        return
      }
      try {
        const json = JSON.parse(data)
        const delta = json.choices?.[0]?.delta?.content
        if (delta) output.value += delta
      } catch {
        // 忽略不完整 JSON（流式分片）
      }
    }
  }
  loading.value = false
}
</script>

<template>
  <el-card>
    <div class="bar">
      <el-select v-model="model" placeholder="选择模型" style="width: 240px">
        <el-option label="echo（回显，切片验证用）" value="echo" />
      </el-select>
      <el-button type="primary" :loading="loading" @click="streamChat">发送</el-button>
    </div>
    <el-input
      v-model="input"
      type="textarea"
      :rows="6"
      placeholder="输入提示词，例如：你好PaaS"
    />
    <div class="label">输出（流式）</div>
    <div class="output">{{ output }}<span v-if="loading" class="cursor">▋</span></div>
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
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.cursor {
  animation: blink 1s step-start infinite;
  color: var(--el-color-primary);
}
@keyframes blink {
  50% {
    opacity: 0;
  }
}
</style>
