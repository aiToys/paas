<script setup lang="ts">
import { laneFetch } from '../lane'
import { ref } from 'vue'

// 客服弹窗（自 App.vue 整体迁移：SSE 流式 + reasoning 折叠）
const chatOpen = ref(false)
const chatMessages = ref<{ role: 'user' | 'assistant'; content: string; reasoning?: string }[]>([])
const chatInput = ref('')
const chatLoading = ref(false)
const showReasoning = ref<Record<number, boolean>>({})

// 打开弹窗；preset 非空时预填输入（不自动发送，用户确认后自己按发送）
function openWith(preset?: string) {
  chatOpen.value = true
  if (preset) {
    chatInput.value = preset
  }
}

// 保留原 openChat 行为（含欢迎语初始化）
function open() {
  chatOpen.value = true
  if (chatMessages.value.length === 0) {
    chatMessages.value.push({ role: 'assistant', content: '你好！我是 PaasShop 智能客服，可以帮你查商品、了解售后政策。试试问「有什么键盘推荐」或「退货政策」' })
  }
}

async function sendChat() {
  const msg = chatInput.value.trim()
  if (!msg || chatLoading.value) return
  chatInput.value = ''
  chatMessages.value.push({ role: 'user', content: msg })
  const assistantIdx = chatMessages.value.length
  chatMessages.value.push({ role: 'assistant', content: '', reasoning: '' })
  chatLoading.value = true

  try {
    const resp = await laneFetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({ message: msg, userId: 'web-user' }),
    })
    if (!resp.body) throw new Error('no body')
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const line of lines) {
        if (!line.startsWith('data: ')) continue
        const payload = line.slice(6).trim()
        if (payload === '[DONE]') continue
        try {
          const chunk = JSON.parse(payload)
          const delta = chunk.choices?.[0]?.delta
          if (delta?.reasoning_content) {
            chatMessages.value[assistantIdx].reasoning += delta.reasoning_content
          }
          if (delta?.content) {
            chatMessages.value[assistantIdx].content += delta.content
          }
        } catch {}
      }
    }
  } catch (e) {
    chatMessages.value[assistantIdx].content = '客服服务暂不可用，请稍后重试。'
  } finally {
    chatLoading.value = false
  }
}

function toggleReasoning(idx: number) {
  showReasoning.value[idx] = !showReasoning.value[idx]
}

defineExpose({ openWith, open })
</script>

<template>
  <!-- 客服弹窗 -->
  <div v-if="chatOpen" class="chat-mask" @click.self="chatOpen = false">
    <div class="chat-win">
      <div class="chat-hdr">
        <span>💬 智能客服</span>
        <button class="chat-close" @click="chatOpen = false">×</button>
      </div>
      <div class="chat-body">
        <div v-for="(m, i) in chatMessages" :key="i" :class="['msg', m.role]">
          <div v-if="m.role === 'assistant' && m.reasoning" class="reasoning">
            <button class="rs-btn" @click="toggleReasoning(i)">
              {{ showReasoning[i] ? '▼' : '▶' }} 思考过程
            </button>
            <div v-if="showReasoning[i]" class="rs-content">{{ m.reasoning }}</div>
          </div>
          <div class="msg-content">{{ m.content || (chatLoading && i === chatMessages.length - 1 ? '思考中…' : '') }}</div>
        </div>
      </div>
      <div class="chat-input">
        <input v-model="chatInput" placeholder="试试：有什么键盘推荐 / 退货政策" @keyup.enter="sendChat" :disabled="chatLoading" />
        <button @click="sendChat" :disabled="chatLoading || !chatInput.trim()">发送</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-mask { position: fixed; inset: 0; background: rgba(0,0,0,.3); display: flex; align-items: flex-end; justify-content: flex-end; padding: 24px; z-index: 100; }
.chat-win { background: #fff; width: 400px; max-width: 100%; height: 560px; border-radius: 16px; display: flex; flex-direction: column; box-shadow: 0 8px 32px rgba(0,0,0,.2); }
.chat-hdr { display: flex; justify-content: space-between; align-items: center; padding: 14px 18px; border-bottom: 1px solid #eee; font-weight: 600; }
.chat-close { background: none; border: none; font-size: 24px; cursor: pointer; color: #95a5a6; }
.chat-body { flex: 1; overflow-y: auto; padding: 16px; display: flex; flex-direction: column; gap: 12px; }
.msg { max-width: 85%; }
.msg.user { align-self: flex-end; }
.msg.assistant { align-self: flex-start; }
.msg.user .msg-content { background: #667eea; color: #fff; }
.msg.assistant .msg-content { background: #f0f2f5; color: #2c3e50; }
.msg-content { padding: 10px 14px; border-radius: 12px; font-size: 14px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.reasoning { margin-bottom: 6px; }
.rs-btn { background: none; border: 1px dashed #bbb; color: #888; font-size: 12px; padding: 2px 8px; border-radius: 4px; cursor: pointer; }
.rs-content { margin-top: 4px; padding: 8px; background: #fffaeb; border-left: 3px solid #fbbf24; font-size: 12px; color: #92660a; border-radius: 4px; white-space: pre-wrap; }
.chat-input { display: flex; gap: 8px; padding: 12px; border-top: 1px solid #eee; }
.chat-input input { flex: 1; border: 1px solid #ddd; border-radius: 8px; padding: 8px 12px; font-size: 14px; }
.chat-input button { background: #667eea; color: #fff; border: none; border-radius: 8px; padding: 0 18px; cursor: pointer; font-size: 14px; }
.chat-input button:disabled { opacity: .5; cursor: not-allowed; }
</style>
