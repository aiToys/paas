<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'

// Playground：聊天式交互推理，直连 Platform Core Gateway（OpenAI 兼容 SSE）。
// 模型列表来自 /api/models（富信息：id + 供应商 + 描述）；支持 ?model=<id> 预选。
// API Key 来自顶栏会话（@/api），换 Key 即换租户/权限视角。

interface Msg {
  role: 'user' | 'assistant'
  content: string
}
interface ModelOpt {
  id: string
  name: string
  vendor: string
}

const route = useRoute()
const model = ref('')
const modelOptions = ref<ModelOpt[]>([])
const input = ref('')
const loading = ref(false)
const lastTokens = ref(0)
const messages = ref<Msg[]>([])
// 推理参数（null = 不传，用上游默认）；多轮对话累积完整历史下发。
const temperature = ref<number | null>(null)
const maxTokens = ref<number | null>(null)
const scrollRef = ref<HTMLElement | null>(null)
// 跟踪当前 SSE 请求，切模型/重发/卸载时中断旧流，避免并发写入与泄漏。
let activeCtrl: AbortController | null = null

async function scrollToBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

onMounted(async () => {
  try {
    const resp = await fetchAuth('/api/models')
    const json = await resp.json()
    modelOptions.value = (json.data ?? []).map((m: any) => ({ id: m.id, name: m.name, vendor: m.vendor }))
  } catch {
    ElMessage.error('加载模型列表失败')
  }
  const q = route.query.model as string
  const ids = modelOptions.value.map((m) => m.id)
  model.value = q && ids.includes(q) ? q : modelOptions.value[0]?.id ?? ''
})

async function send() {
  const text = input.value.trim()
  if (!text || loading.value || !model.value) return
  // 中断上一条未完成的流（切模型/重发场景）
  if (activeCtrl) activeCtrl.abort()
  activeCtrl = new AbortController()
  const ctrl = activeCtrl
  loading.value = true
  // 多轮：下发完整对话历史（过滤空占位），上游据此维持上下文。
  const hist = messages.value
    .filter((m) => m.content.trim() !== '')
    .map((m) => ({ role: m.role, content: m.content }))
  hist.push({ role: 'user', content: text })
  messages.value.push({ role: 'user', content: text })
  input.value = ''
  const assistant: Msg = { role: 'assistant', content: '' }
  messages.value.push(assistant)
  await scrollToBottom()

  try {
    const body: Record<string, unknown> = {
      model: model.value,
      messages: hist,
      stream: true,
    }
    if (temperature.value !== null) body.temperature = temperature.value
    if (maxTokens.value !== null) body.max_tokens = maxTokens.value
    const resp = await fetchAuth('/v1/chat/completions', {
      method: 'POST',
      signal: ctrl.signal,
      body: JSON.stringify(body),
    })

    if (!resp.ok || !resp.body) {
      // 503 = 全部通道不可用（常见：第三方供应商凭证未配置）→ 友好引导。
      if (resp.status === 503) {
        assistant.content = '模型无可用通道：可能供应商 API Key 未配置。请联系管理员在「平台能力 → 安全」填写平台级供应商凭证。'
        ElMessage.warning('供应商凭证可能未配置，详见对话区提示')
      } else {
        assistant.content = `请求失败：HTTP ${resp.status}`
        ElMessage.error(`请求失败：HTTP ${resp.status}`)
      }
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
    lastTokens.value = tokens
  } catch (e) {
    // abort（被新请求接管或组件卸载）静默；其它错误提示。
    if (!ctrl.signal.aborted) {
      assistant.content = '请求失败：' + (e as Error).message
      ElMessage.error('请求失败：' + (e as Error).message)
    }
  } finally {
    if (activeCtrl === ctrl) {
      loading.value = false
      activeCtrl = null
    }
  }
}

onUnmounted(() => {
  // 离开页面中断未完成流，避免往已卸载组件写入。
  if (activeCtrl) activeCtrl.abort()
})

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

function clearChat() {
  if (activeCtrl) activeCtrl.abort()
  messages.value = []
  lastTokens.value = 0
}
</script>

<template>
  <div class="chat">
    <div class="chat-bar">
      <div class="model-pill">
        <span class="pulse-dot" />
        <el-select v-model="model" size="small" class="model-select" placeholder="选择模型">
          <el-option v-for="m in modelOptions" :key="m.id" :label="`${m.name}（${m.vendor}）`" :value="m.id" />
        </el-select>
        <span class="muted">· 已就绪</span>
      </div>
      <div class="bar-actions">
        <el-popover trigger="click" placement="bottom" :width="280">
          <template #reference>
            <el-button text size="small" class="param-btn">参数</el-button>
          </template>
          <div class="param-panel">
            <div class="param-row">
              <span class="param-label">温度 {{ temperature === null ? '（默认）' : temperature.toFixed(2) }}</span>
              <el-slider v-model="temperature" :min="0" :max="2" :step="0.05" />
              <el-button text size="small" @click="temperature = null">重置</el-button>
            </div>
            <div class="param-row">
              <span class="param-label">最大 token</span>
              <el-input-number
                v-model="maxTokens"
                :min="1"
                :max="32768"
                :step="128"
                placeholder="不限"
                controls-position="right"
                size="small"
              />
              <el-button text size="small" @click="maxTokens = null">重置</el-button>
            </div>
            <p class="param-tip">留空则用供应商默认值。多轮对话自动携带完整历史。</p>
          </div>
        </el-popover>
        <el-button text size="small" :disabled="!messages.length" @click="clearChat">清空</el-button>
        <span v-if="lastTokens > 0" class="tokens mono">{{ lastTokens }} tokens</span>
      </div>
    </div>

    <div ref="scrollRef" class="messages">
      <div v-if="!messages.length" class="empty-hint">
        选择模型后发送消息即可开始对话。支持多轮上下文与参数调节。
      </div>
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
        <button class="send" :disabled="!input.trim() || loading || !model" @click="send">
          <Icon name="playground" :size="18" />
        </button>
      </div>
      <div class="hint">流式推理 · 多轮上下文 · Enter 发送 / Shift+Enter 换行</div>
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
  padding: 5px 12px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 20px;
  font-size: 13px;
}
.model-select {
  width: 200px;
}
.model-select :deep(.el-select__wrapper) {
  background: transparent;
  box-shadow: none !important;
  min-height: 24px;
  padding: 0;
}
.model-select :deep(.el-select__placeholder) {
  font-family: var(--font-mono);
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
.empty-hint {
  margin: auto;
  color: var(--text-dim);
  font-size: 13.5px;
  text-align: center;
  line-height: 1.8;
}
.bar-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}
.param-btn { color: var(--text-dim); }
.param-panel { display: flex; flex-direction: column; gap: 14px; }
.param-row { display: flex; flex-direction: column; gap: 6px; }
.param-label { font-size: 12.5px; color: var(--text-dim); }
.param-tip { margin: 0; font-size: 12px; color: var(--text-faint); line-height: 1.5; }
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
