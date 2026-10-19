<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { playgroundMode, conversationHistory, chatEvent } from '@/domain/playground'
import PageHeader from '@/components/PageHeader.vue'
import { fetchAuth, apiError, respError } from '@/api'

// Playground：聊天式交互推理，直连 Platform Core Gateway（OpenAI 兼容 SSE）。
// 模型列表来自 /api/models（富信息：id + 供应商 + 描述）；支持 ?model=<id> 预选。
// API Key 来自顶栏会话（@/api），换 Key 即换租户/权限视角。

interface Msg {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string // 推理模型思考过程（reasoning_content），无则空
  reasoningCollapsed?: boolean // 思考区块折叠态（per-msg）
  error?: string // 失败原因（MD-02：与正文分离，不入后续对话上下文）
  stopped?: boolean // 用户主动停止（切模型/重发/清空前中断）
  interrupted?: boolean // 流中断：连接断在 [DONE] 之前，正文可能不完整
}
interface ModelOpt {
  id: string
  name: string
  vendor: string
  capabilities?: string[]
}
// /api/models 富信息响应（仅取前端需要的字段，避免维护完整 schema）。
interface ModelListItem {
  id: string
  name: string
  vendor: string
  capabilities?: string[]
}

const route = useRoute()
const router = useRouter()
const model = ref('')
const modelOptions = ref<ModelOpt[]>([])
const input = ref('')
const loading = ref(false)
// MD-03：网关不回传上游 usage，此计数是 Unicode 字符数（近似），不冒充 token 计量
const lastChars = ref(0)
const messages = ref<Msg[]>([])
// 推理参数（null = 不传，用上游默认）；多轮对话累积完整历史下发。
const temperature = ref<number | null>(null)
const maxTokens = ref<number | null>(null)
const scrollRef = ref<HTMLElement | null>(null)
// 请求证据（D3）：前端可测的时延指标；usage 网关不回传（服务端计量），如实标注不填 0。
const lastTiming = ref<{ firstMs: number; totalMs: number } | null>(null)
// embedding 向量测试（D5）：非 chat 模型不盲发 chat 请求——检测到 embedding 模型切换表单。
const embInput = ref('')
const embLoading = ref(false)
const embResult = ref<{ count: number; dims: number; ms: number; preview: number[][] } | null>(null)
const embError = ref('')
// 跟踪当前 SSE 请求，切模型/重发/卸载时中断旧流，避免并发写入与泄漏。
let activeCtrl: AbortController | null = null

// 当前模型条目（agent: 前缀是虚拟模型，恒走 chat 端点）
const currentModel = computed(() => modelOptions.value.find((m) => m.id === model.value))
const mode = computed(() => playgroundMode(currentModel.value))
const isEmbeddingMode = computed(() => mode.value === 'embedding')
// embedding 输入按行拆条（去空行）
const embLines = computed(() => embInput.value.split('\n').map((l) => l.trim()).filter(Boolean))

async function scrollToBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

onMounted(async () => {
  try {
    const resp = await fetchAuth('/api/models')
    const json = await resp.json()
    modelOptions.value = ((json.data ?? []) as ModelListItem[]).map((m) => ({ id: m.id, name: m.name, vendor: m.vendor, capabilities: m.capabilities }))
    // 并入 Agent 虚拟模型（agent:{id} 走同款 /v1/chat/completions；失败静默——无 Agent 时列表不变）。
    // 停用的 Agent 不进下拉（D6：能选就会真发请求，停用项选了必失败）。
    try {
      const ra = await fetchAuth('/api/agents')
      if (ra.ok) {
        const agents = ((await ra.json()).data ?? []) as Array<{ id: string; name: string; enabled?: boolean }>
        modelOptions.value.push(
          ...agents.filter((a) => a.enabled !== false).map((a) => ({ id: `agent:${a.id}`, name: a.name, vendor: 'Agent' }))
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

// 已停留本页时从模型市场带 ?model= 跳入（组件复用不重挂载）也要重选；
// 带入不弹切换确认（是导航语义不是用户改主意），也不清对话——与旧版行为一致。
let skipSwitchConfirm = false
watch(() => route.query.model, (q) => {
  const id = (q as string) || ''
  if (id && modelOptions.value.some((m) => m.id === id)) {
    skipSwitchConfirm = true
    model.value = id
  }
})

// D4：对话模式下手动切换模型默认开始新会话（多轮上下文不跨模型）；取消则回退。
// embedding 模式无多轮上下文（messages 恒空），自然不触发。
watch(model, (nv, ov) => {
  if (!ov || nv === ov) return
  if (skipSwitchConfirm) { skipSwitchConfirm = false; return }
  // 模式切换（chat ↔ embedding）无需确认——两边状态互不污染（对话区与向量结果各自独立）
  if (messages.value.length) confirmModelSwitch(ov)
})

async function confirmModelSwitch(prev: string) {
  try {
    await ElMessageBox.confirm(
      '切换模型后将开始新会话（多轮上下文不跨模型保留）。',
      '切换模型',
      { type: 'warning', confirmButtonText: '清空并切换', cancelButtonText: '留在当前模型' }
    )
    clearChat()
  } catch {
    skipSwitchConfirm = true
    model.value = prev
  }
}

async function send() {
  const text = input.value.trim()
  if (!text || loading.value || !model.value) return
  if (mode.value !== 'chat') { ElMessage.warning('该模型不支持对话试玩，请选择受支持的模型'); return }
  const requestModel = model.value
  // 中断上一条未完成的流（切模型/重发场景）
  if (activeCtrl) activeCtrl.abort()
  activeCtrl = new AbortController()
  const ctrl = activeCtrl
  loading.value = true
  // 请求证据计时（D3）：首字延迟 = 发出 → 第一个 delta；总耗时在 finally 统一结算
  const t0 = performance.now()
  let firstDeltaAt = 0
  lastTiming.value = null
  // 多轮：下发完整对话历史（过滤空占位与失败消息），上游据此维持上下文。
  // MD-02：失败文案/错误占位不入上下文——否则把「请求失败：HTTP 500」当模型回答发给上游。
  const hist = conversationHistory(messages.value)
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
      model: requestModel,
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
      // MD-02：错误写入独立的 error 字段，不进 content（不入后续上下文）。
      if (resp.status === 503) {
        assistant.error = '模型无可用通道：可能供应商 API Key 未配置。请联系管理员在后台管理「推理」页配置供应商通道凭证。'
        ElMessage.warning('供应商凭证可能未配置，详见对话区提示')
      } else {
        assistant.error = await respError(resp, '请求失败：')
        ElMessage.error(`请求失败：HTTP ${resp.status}`)
      }
      return
    }

    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let chars = 0
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const parts = buffer.split(/\r?\n\r?\n/)
      buffer = parts.pop() ?? ''
      for (const part of parts) {
        const event = chatEvent(part.replace(/\r/g, ''))
        if (event.done) { lastChars.value = chars; return }
        const delta = event.delta
          if (delta) {
            if (!firstDeltaAt) firstDeltaAt = performance.now()
            // 推理模型：思考过程（reasoning_content）先于答案到达，实时拼接渲染。
            if (delta.reasoning_content) {
              assistant.reasoning = (assistant.reasoning ?? '') + delta.reasoning_content
              chars += [...delta.reasoning_content].length
              await scrollToBottom()
            }
            if (delta.content) {
              assistant.content += delta.content
              chars += [...delta.content].length
              await scrollToBottom()
            }
          }
      }
    }
    // 连接断在 [DONE] 之前：正文可能不完整，如实标注（与完成/失败分开）
    lastChars.value = chars
    assistant.interrupted = true
  } catch (e) {
    if (ctrl.signal.aborted) {
      // 用户主动停止 / 被新请求接管：保留已生成部分并标记
      assistant.stopped = true
    } else {
      assistant.error = apiError(e, '请求失败')
      ElMessage.error(apiError(e, '请求失败'))
    }
  } finally {
    // 时延证据覆盖全部终态（完成/中断/停止/失败），被新请求接管也记录本请求自己的数据
    ctrl.abort() // 结束/错误后关闭剩余响应，避免后台继续消费流
    if (activeCtrl === ctrl) lastTiming.value = { firstMs: firstDeltaAt ? Math.round(firstDeltaAt - t0) : 0, totalMs: Math.round(performance.now() - t0) }
    if (activeCtrl === ctrl) {
      loading.value = false
      activeCtrl = null
    }
  }
}

// D5：embedding 向量测试——非 chat 模型走 /v1/embeddings（平台已支持该端点，是真玩法），
// 不拿 chat 请求代替。/v1/* 裸 JSON 契约：成功 {data:[{embedding:number[]}]}}，错误 {error:...}。
async function runEmbedding() {
  const lines = embLines.value
  if (!lines.length || embLoading.value || !model.value) return
  if (mode.value !== 'embedding') { embError.value = '该模型不支持向量测试'; return }
  const requestModel = model.value
  embLoading.value = true
  embError.value = ''
  embResult.value = null
  const t0 = performance.now()
  try {
    const resp = await fetchAuth('/v1/embeddings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: requestModel, input: lines }),
    })
    const j = await resp.json().catch(() => ({}))
    if (!resp.ok) {
      const e = j as { error?: { message?: string } | string }
      embError.value = typeof e.error === 'string' ? e.error : (e.error?.message || `HTTP ${resp.status}`)
      return
    }
    if (model.value !== requestModel) return
    const data = ((j as { data?: Array<{ embedding?: number[] }> }).data ?? []).filter((d) => Array.isArray(d.embedding))
    if (!data.length) {
      embError.value = '响应中没有向量数据'
      return
    }
    embResult.value = {
      count: data.length,
      dims: data[0].embedding!.length,
      ms: Math.round(performance.now() - t0),
      preview: data.map((d) => d.embedding!.slice(0, 8)),
    }
  } catch (e) {
    embError.value = apiError(e, '请求失败')
  } finally {
    embLoading.value = false
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
  lastChars.value = 0
  embResult.value = null
  embError.value = ''
}

// 主动停止当前生成（保留已生成部分并标记 stopped）
function stop() {
  if (activeCtrl) activeCtrl.abort()
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
    <PageHeader title="Playground" description="选择模型或智能体，验证多轮对话与推理参数。" eyebrow="AI 平台" />
    <div class="chat-bar">
      <div class="model-pill">
        <span class="pulse-dot" />
        <el-select aria-label="选择对话模型" v-model="model" size="small" class="model-select" placeholder="选择模型">
          <el-option v-for="m in modelOptions" :disabled="playgroundMode(m) === 'unsupported'" :key="m.id" :label="`${m.name}（${m.vendor}）`" :value="m.id" />
        </el-select>
        <span v-if="isEmbeddingMode" class="mode-badge">向量测试模式</span>
        <el-button text size="small" @click="router.push('/models')">模型市场</el-button>
        <span v-if="model && !isEmbeddingMode" class="muted">· 已选择模型</span>
      </div>
      <div class="bar-actions">
        <el-popover trigger="click" placement="bottom" :width="300">
          <template #reference>
            <el-button text size="small" class="param-btn">参数与请求</el-button>
          </template>
          <div class="param-panel">
            <template v-if="!isEmbeddingMode">
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
            </template>
            <!-- 请求证据（D3）：前端可测时延；token 用量网关不回传，如实标注不填 0 -->
            <div class="evidence-block">
              <p class="evidence-title">最近一次请求</p>
              <dl v-if="lastTiming" class="evidence-list">
                <div><dt>首字延迟</dt><dd>{{ lastTiming.firstMs ? lastTiming.firstMs + ' ms' : '未收到' }}</dd></div>
                <div><dt>总耗时</dt><dd>{{ lastTiming.totalMs }} ms</dd></div>
                <div v-if="!isEmbeddingMode && lastChars > 0 && lastTiming.totalMs > 0">
                  <dt>字符吞吐</dt><dd>{{ Math.round(lastChars / (lastTiming.totalMs / 1000)) }} 字符/秒</dd>
</div>
              </dl>
              <p v-else class="evidence-empty">暂无请求</p>
              <p class="param-tip">Token 用量：未返回（网关在服务端计量，响应不含 usage 帧）。</p>
            </div>
          </div>
        </el-popover>
        <el-button text size="small" :disabled="!messages.length && !embResult" @click="clearChat">清空</el-button>
        <el-button text size="small" @click="router.push('/api-keys')">API 密钥</el-button>
        <!-- MD-03：网关不回传上游 usage，这里是字符数（近似），不冒充 token 计量 -->
        <span v-if="lastChars > 0 && !isEmbeddingMode" class="tokens mono">{{ lastChars }} 字符</span>
      </div>
    </div>

    <!-- D5：embedding 模型走向量测试面板，不展示对话输入框（非 chat 模型不盲发 chat 请求） -->
    <div v-if="isEmbeddingMode" ref="scrollRef" class="messages">
      <div class="emb-panel">
        <p class="emb-hint">
          当前模型是 <strong>embedding 模型</strong>——它不做对话，只把文本转成向量。
          输入文本生成向量，验证模型连通性与输出维度；每行一条，支持批量。
        </p>
        <textarea
          v-model="embInput"
          rows="6"
          class="emb-input"
          placeholder="每行一条待向量化文本，例如：&#10;你好，世界&#10;向量检索测试"
        />
        <div class="emb-actions">
          <button class="send" :disabled="embLoading || !embLines.length" @click="runEmbedding">
            <Icon v-if="!embLoading" name="playground" :size="18" />
            <span v-else class="emb-loading-text">生成中…</span>
          </button>
          <span v-if="embLines.length" class="muted">{{ embLines.length }} 条待测</span>
        </div>
        <div v-if="embError" class="msg-error emb-error">⚠ {{ embError }}</div>
        <div v-if="embResult" class="emb-result">
          <p class="emb-summary">
            {{ embResult.count }} 条文本 · 每条 <strong>{{ embResult.dims }}</strong> 维 · 耗时 {{ embResult.ms }} ms
          </p>
          <div class="emb-vec-list">
            <div v-for="(p, i) in embResult.preview" :key="i" class="emb-vec">
              <span class="emb-vec-idx">#{{ i }}</span>
              <code class="emb-vec-val">[{{ p.map((v) => (typeof v === 'number' ? v.toFixed(4) : v)).join(', ') }}, …]</code>
            </div>
          </div>
          <p class="emb-note">仅展示每条前 8 维（维度实测，可用作知识库建索引前的维度核对）。</p>
        </div>
      </div>
    </div>

    <div v-else ref="scrollRef" class="messages">
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
            <!-- MD-02：失败态独立于正文渲染，不伪装成模型回答 -->
            <div v-if="m.error" class="msg-error">
              ⚠ {{ m.error }}
              <div class="msg-note">该消息不会进入后续对话上下文，可直接重试。</div>
            </div>
            <span v-else-if="m.content === '' && !m.reasoning && loading" class="typing">
              <i /><i /><i />
            </span>
            <template v-else>
              {{ m.content
              }}<span v-if="streamingContent(m, i)" class="cursor">▋</span>
              <span v-else-if="streamingReasoning(m, i)" class="phase-hint">（思考完成后再生成回答）</span>
            </template>
            <!-- 停止/中断与完成分开标注（MD-02）：正文可能不完整时如实提示 -->
            <div v-if="m.stopped" class="msg-flag">⏹ 已停止生成（保留已生成部分）</div>
            <div v-else-if="m.interrupted && !m.error" class="msg-flag warn">⚠ 连接中断，回答可能不完整</div>
          </template>
          <template v-else>{{ m.content }}</template>
        </div>
      </div>
    </div>

    <div v-if="mode === 'unsupported'" class="composer">该模型暂不支持控制台试玩，请选择对话或向量模型。</div>
    <div v-else-if="!isEmbeddingMode" class="composer">
      <div class="input-wrap">
        <textarea
          v-model="input"
          rows="1"
          placeholder="发送消息…  (Enter 发送，Shift+Enter 换行)"
          @keydown="onKey"
        />
        <button v-if="loading" class="send" aria-label="停止生成" @click="stop">
          <Icon name="stop" :size="14" />
        </button>
        <button v-else class="send" :disabled="!input.trim() || !model" @click="send">
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
  background: var(--brand-solid);
  color: #fff;
  border-bottom-right-radius: 4px;
}
.msg.assistant .bubble {
  background: var(--surface);
  border: 1px solid var(--border);
  font-family: var(--font-mono);
  border-bottom-left-radius: 4px;
}
/* 失败态（MD-02）：与正文分离的错误块，红色弱化区分 */
.msg-error {
  color: var(--danger);
  font-size: 13px;
  line-height: 1.6;
}
.msg-note {
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--text-faint);
}
/* 停止/中断标记：小字弱化，不影响正文阅读 */
.msg-flag {
  margin-top: 6px;
  padding-top: 6px;
  border-top: 1px dashed var(--border);
  font-size: 11.5px;
  color: var(--text-faint);
}
.msg-flag.warn {
  color: var(--warning);
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
  background: var(--brand-solid);
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

/* 模式标识（D5）：embedding 模型不进对话 */
.mode-badge {
  padding: 2px 10px;
  border-radius: 10px;
  font-size: 11.5px;
  color: var(--brand);
  background: var(--brand-soft, rgba(64, 158, 255, 0.12));
  white-space: nowrap;
}

/* 请求证据（D3） */
.evidence-block {
  padding-top: 10px;
  border-top: 1px dashed var(--border);
}
.evidence-title {
  margin: 0 0 8px;
  font-size: 12.5px;
  color: var(--text-dim);
  font-weight: 500;
}
.evidence-list {
  margin: 0 0 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.evidence-list > div {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
}
.evidence-list dt {
  color: var(--text-faint);
}
.evidence-list dd {
  margin: 0;
  font-family: var(--font-mono);
  color: var(--text-dim);
}
.evidence-empty {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--text-faint);
}

/* embedding 向量测试面板（D5） */
.emb-panel {
  margin: auto;
  width: 100%;
  max-width: 620px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.emb-hint {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-dim);
  background: var(--bg-soft, rgba(128, 128, 128, 0.08));
  border-left: 2px solid var(--brand);
  border-radius: 0 8px 8px 0;
  padding: 10px 14px;
}
.emb-input {
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
  background: var(--surface);
  padding: 10px 14px;
}
.emb-input:focus {
  outline: none;
  border-color: var(--brand);
  box-shadow: 0 0 0 3px var(--brand-soft);
}
.emb-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.emb-actions .send {
  width: auto;
  min-width: 96px;
  padding: 0 16px;
  gap: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.emb-loading-text {
  font-size: 13px;
}
.emb-error {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 14px;
}
.emb-result {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.emb-summary {
  margin: 0;
  font-size: 13px;
  color: var(--text-dim);
}
.emb-vec-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.emb-vec {
  display: flex;
  align-items: baseline;
  gap: 10px;
  font-size: 12px;
}
.emb-vec-idx {
  color: var(--text-faint);
  flex-shrink: 0;
}
.emb-vec-val {
  font-family: var(--font-mono);
  color: var(--text-dim);
  word-break: break-all;
}
.emb-note {
  margin: 0;
  font-size: 11.5px;
  color: var(--text-faint);
}
</style>
