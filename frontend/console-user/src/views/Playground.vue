<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'

// Playground：聊天式交互推理，直连 Platform Core Gateway（OpenAI 兼容 SSE）。
// 本切片 API Key 为开发默认值；生产应从用户会话/Identity 获取（Plan 2）。
const API_KEY = 'sk-paas-dev-key'

interface Msg {
  role: 'user' | 'assistant'
  content: string
}

const model = ref('echo')
const input = ref('')
const loading = ref(false)
const lastTokens = ref(0)
const messages = ref<Msg[]>([
  { role: 'assistant', content: '在下方输入提示词，我会以流式返回结果。当前连接 echo 模型（回显，用于切片验证）。' },
])
const scrollRef = ref<HTMLElement | null>(null)

async function scrollToBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

async function send() {
  const text = input.value.trim()
  if (!text || loading.value) return
  loading.value = true
  messages.value.push({ role: 'user', content: text })
  input.value = ''
  const assistant: Msg = { role: 'assistant', content: '' }
  messages.value.push(assistant)
  await scrollToBottom()

  const resp = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${API_KEY}`,
    },
    body: JSON.stringify({
      model: model.value,
      messages: [{ role: 'user', content: text }],
      stream: true,
    }),
  })

  if (!resp.ok || !resp.body) {
    loading.value = false
    assistant.content = `请求失败：HTTP ${resp.status}`
    ElMessage.error(`请求失败：HTTP ${resp.status}`)
    return
  }

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let tokens = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split('\n\n')
    buffer = parts.pop() ?? ''
    for (const part of parts) {
      const line = part.trim()
      if (!line.startsWith('data:')) continue
      const data = line.slice(5).trim()
      if (data === '[DONE]') {
        loading.value = false
        lastTokens.value = tokens
        return
      }
      try {
        const delta = JSON.parse(data).choices?.[0]?.delta?.content
        if (delta) {
          assistant.content += delta
          tokens += [...delta].length
          await scrollToBottom()
        }
      } catch {
        /* 流式分片，忽略不完整 JSON */
      }
    }
  }
  loading.value = false
  lastTokens.value = tokens
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}
</script>

<template>
  <div class="chat">
    <div class="chat-bar">
      <div class="model-pill">
        <span class="pulse-dot" />
        <span class="mono">{{ model }}</span>
        <span class="muted">· 已就绪</span>
      </div>
      <div v-if="lastTokens > 0" class="tokens mono">{{ lastTokens }} tokens</div>
    </div>

    <div ref="scrollRef" class="messages">
      <div v-for="(m, i) in messages" :key="i" class="msg" :class="m.role">
        <div class="bubble">
          <span v-if="m.role === 'assistant' && m.content === '' && loading" class="typing">
            <i /><i /><i />
          </span>
          <template v-else>{{ m.content }}<span v-if="m.role === 'assistant' && loading && i === messages.length - 1" class="cursor">▋</span></template>
        </div>
      </div>
    </div>

    <div class="composer">
      <div class="input-wrap">
        <textarea
          v-model="input"
          rows="1"
          placeholder="发送消息…  (Enter 发送，Shift+Enter 换行)"
          @keydown="onKey"
        />
        <button class="send" :disabled="!input.trim() || loading" @click="send">
          <Icon name="playground" :size="18" />
        </button>
      </div>
      <div class="hint">输出由 echo 模型生成，仅用于端到端验证</div>
    </div>
  </div>
</template>

<style scoped>
.chat {
  height: calc(100vh - 56px - 64px);
  max-width: 820px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
}
.chat-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.model-pill {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 20px;
  font-size: 13px;
}
.muted {
  color: var(--text-faint);
}
.tokens {
  font-size: 12px;
  color: var(--text-faint);
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 8px 4px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.msg {
  display: flex;
}
.msg.user {
  justify-content: flex-end;
}
.bubble {
  max-width: 72%;
  padding: 11px 15px;
  border-radius: 14px;
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}
.msg.user .bubble {
  background: var(--brand);
  color: #fff;
  border-bottom-right-radius: 4px;
}
.msg.assistant .bubble {
  background: var(--surface);
  border: 1px solid var(--border);
  font-family: var(--font-mono);
  border-bottom-left-radius: 4px;
}
.cursor {
  display: inline-block;
  margin-left: 2px;
  color: var(--brand);
  animation: blink 1s step-start infinite;
}
@keyframes blink {
  50% {
    opacity: 0;
  }
}

.typing {
  display: inline-flex;
  gap: 4px;
}
.typing i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-faint);
  animation: bounce 1.2s infinite;
}
.typing i:nth-child(2) {
  animation-delay: 0.2s;
}
.typing i:nth-child(3) {
  animation-delay: 0.4s;
}
@keyframes bounce {
  0%,
  60%,
  100% {
    transform: translateY(0);
    opacity: 0.4;
  }
  30% {
    transform: translateY(-4px);
    opacity: 1;
  }
}
@media (prefers-reduced-motion: reduce) {
  .cursor,
  .typing i {
    animation: none;
  }
}

.composer {
  flex-shrink: 0;
  padding-top: 12px;
}
.input-wrap {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 8px 8px 8px 16px;
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s;
}
.input-wrap:focus-within {
  border-color: var(--brand);
  box-shadow: 0 0 0 3px var(--brand-soft);
}
textarea {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--text);
  font-family: inherit;
  font-size: 14px;
  line-height: 1.6;
  resize: none;
  outline: none;
  padding: 6px 0;
  max-height: 160px;
}
textarea::placeholder {
  color: var(--text-faint);
}
.send {
  flex-shrink: 0;
  width: 38px;
  height: 38px;
  border: none;
  border-radius: 10px;
  background: var(--brand);
  color: #fff;
  cursor: pointer;
  display: grid;
  place-items: center;
  transition: background 0.12s, opacity 0.12s;
}
.send:hover:not(:disabled) {
  background: var(--brand-hover);
}
.send:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.send :deep(svg) {
  transform: rotate(-90deg);
}
.hint {
  text-align: center;
  font-size: 11px;
  color: var(--text-faint);
  margin-top: 8px;
}
</style>
