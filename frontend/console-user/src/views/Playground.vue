<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth, apiError } from '@/api'

// Playground：聊天式交互推理，直连 Platform Core Gateway（OpenAI 兼容 SSE）。
// 模型列表来自 /api/models（富信息：id + 供应商 + 描述）；支持 ?model=<id> 预选。
// API Key 来自顶栏会话（@/api），换 Key 即换租户/权限视角。

interface Msg {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string // 推理模型思考过程（reasoning_content），无则空
  reasoningCollapsed?: boolean // 思考区块折叠态（per-msg）
}
interface ModelOpt {
  id: string
  name: string
  vendor: string
}
// /api/models 富信息响应（仅取前端需要的字段，避免维护完整 schema）。
interface ModelListItem {
  id: string
  name: string
  vendor: string
}

const route = useRoute()
const router = useRouter()
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
    modelOptions.value = ((json.data ?? []) as ModelListItem[]).map((m) => ({ id: m.id, name: m.name, vendor: m.vendor }))
    // 并入 Agent 虚拟模型（agent:{id} 走同款 /v1/chat/completions；失败静默——无 Agent 时列表不变）。
    try {
      const ra = await fetchAuth('/api/agents')
      if (ra.ok) {
        const agents = ((await ra.json()).data ?? []) as Array<{ id: string; name: string }>
        modelOptions.value.push(
          ...agents.map((a) => ({ id: `agent:${a.id}`, name: a.name, vendor: 'Agent' }))
        )
      }
    } catch { /* 静默降级 */ }
  } catch {
    ElMessage.error('加载模型列表失败')
  }
  const q = route.query.model as string
  const ids = modelOptions.value.map((m) => m.id)
  model.value = q && ids.includes(q) ? q : modelOptions.value[0]?.id ?? ''
})

// 已停留本页时从模型市场带 ?model= 跳入（组件复用不重挂载）也要重选
watch(() => route.query.model, (q) => {
  const id = (q as string) || ''
  if (id && modelOptions.value.some((m) => m.id === id)) model.value = id
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
  messages.value.push({ role: 'assistant', content: '', reasoning: '' })
  // 必须取 messages.value 的元素（reactive proxy）而非 push 前的原始对象引用：
  // 直接改原始对象不触发 Vue trigger，导致流式 token 累积但视图不刷新（一次性出现，失去打字机效果）。
  const assistant = messages.value[messages.value.length - 1]
  await scrollToBottom()

  try {
    const body: Record<string, unknown> = {
      model: model.value,
      messages: hist,
      stream: true,
    }
    if (temperature.value !== null) body.temperature = temperature.value
    if (maxTokens.value !== null) body.max_tokens = maxTokens.value
    // Accept: text/event-stream 声明 SSE——hermes ingress 据此走流式转发（handleSSE），
    // 缺失则落普通 HTTP 代理被全量缓冲（客户端收到一次性大块，失去打字机效果）。
    const resp = await fetchAuth('/v1/chat/completions', {
      method: 'POST',
      signal: ctrl.signal,
      headers: { Accept: 'text/event-stream' },
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
          const delta = JSON.parse(data).choices?.[0]?.delta
          if (delta) {
            // 推理模型：思考过程（reasoning_content）先于答案到达，实时拼接渲染。
            if (delta.reasoning_content) {
              assistant.reasoning = (assistant.reasoning ?? '') + delta.reasoning_content
              tokens += [...delta.reasoning_content].length
              await scrollToBottom()
            }
            if (delta.content) {
              assistant.content += delta.content
              tokens += [...delta.content].length
              await scrollToBottom()
            }
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
      assistant.content = apiError(e, '请求失败')
      ElMessage.error(apiError(e, '请求失败'))
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

// 当前消息是否正在流式接收思考过程（推理阶段：reasoning 还在增长、content 尚未开始）。
// 用于在思考区块头部显示「思考中…」实时状态，强化 SSE 流式观感。
function streamingReasoning(m: Msg, i: number): boolean {
  return loading.value && i === messages.value.length - 1 && !!m.reasoning && !m.content
}
// 当前消息是否正在流式接收正文（回答阶段：content 还在增长）。
function streamingContent(m: Msg, i: number): boolean {
  return loading.value && i === messages.value.length - 1 && !!m.content
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
        <el-button text size="small" @click="router.push('/resources/models')">模型市场</el-button>
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
        <el-button text size="small" @click="router.push('/settings/api-keys')">API 密钥</el-button>
        <span v-if="lastTokens > 0" class="tokens mono">{{ lastTokens }} tokens</span>
      </div>
    </div>

    <div ref="scrollRef" class="messages">
      <div v-if="!messages.length" class="empty-hint">
        选择模型后发送消息即可开始对话。支持多轮上下文与参数调节。
      </div>
      <div v-for="(m, i) in messages" :key="i" class="msg" :class="m.role">
        <div class="bubble">
          <template v-if="m.role === 'assistant'">
            <!-- 推理模型思考过程（先于答案到达，可折叠） -->
            <div v-if="m.reasoning" class="reasoning">
              <div class="reasoning-head" @click="m.reasoningCollapsed = !m.reasoningCollapsed">
                <span class="reasoning-label">
                  💭 思考过程
                  <span v-if="streamingReasoning(m, i)" class="reasoning-status">思考中…</span>
                </span>
                <span class="reasoning-toggle">{{ m.reasoningCollapsed ? '展开' : '收起' }}</span>
              </div>
              <div v-show="!m.reasoningCollapsed" class="reasoning-body">
                {{ m.reasoning }}<span v-if="streamingReasoning(m, i)" class="cursor">▋</span>
              </div>
            </div>
            <span v-if="m.content === '' && !m.reasoning && loading" class="typing">
              <i /><i /><i />
            </span>
            <template v-else>
              {{ m.content
              }}<span v-if="streamingContent(m, i)" class="cursor">▋</span>
              <span v-else-if="streamingReasoning(m, i)" class="phase-hint">（思考完成后再生成回答）</span>
            </template>
          </template>
          <template v-else>{{ m.content }}</template>
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
/* 推理模型思考过程（可折叠区块，浅色弱化区别于正式回复） */
.reasoning {
  margin: -2px -4px 10px;
  border-left: 2px solid var(--brand);
  border-radius: 0 8px 8px 0;
  background: var(--bg-soft, rgba(128, 128, 128, 0.08));
  overflow: hidden;
}
.reasoning-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 5px 12px;
  font-size: 12px;
  color: var(--text-dim);
  cursor: pointer;
  user-select: none;
}
.reasoning-head:hover {
  color: var(--text);
}
.reasoning-label {
  font-weight: 500;
}
.reasoning-toggle {
  font-size: 11px;
  opacity: 0.7;
}
.reasoning-body {
  padding: 8px 12px;
  font-size: 12.5px;
  line-height: 1.65;
  color: var(--text-dim);
  white-space: pre-wrap;
  word-break: break-word;
}
.reasoning-status {
  margin-left: 6px;
  padding: 1px 7px;
  font-size: 11px;
  color: var(--brand);
  background: var(--brand-soft, rgba(64, 158, 255, 0.12));
  border-radius: 8px;
  animation: pulse 1.4s ease-in-out infinite;
}
.phase-hint {
  margin-left: 6px;
  font-size: 12px;
  color: var(--text-faint);
  font-style: italic;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 0.55;
  }
  50% {
    opacity: 1;
  }
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
  .typing i,
  .reasoning-status {
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
