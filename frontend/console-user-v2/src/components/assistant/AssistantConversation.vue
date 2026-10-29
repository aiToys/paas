<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useAssistantStore } from '@/stores/assistant'
import { isTerminal, statusLabel } from '@/api/assistant'
import Icon from '@/components/Icon.vue'
import DiagnosisReport from './DiagnosisReport.vue'
import RunTimeline from './RunTimeline.vue'

const props = defineProps<{ sessionId?: string; full?: boolean }>()
const store = useAssistantStore()
const scroll = ref<HTMLElement>()
const historyOpen = ref(false)
const mode = ref<'investigate' | 'explain'>('investigate')
const target = computed(() => store.session?.target)
const hasReport = computed(() => store.messages.some(m => m.resultKind === 'diagnosis_report'))
// 自然语言优先（spec 2026-10-29 §7）：首屏快捷提示引导带线索提问；有报告后转为追问建议。
const suggestions = computed(() => hasReport.value
  ? ['这条结论有哪些证据？', '我应该先验证什么？', '如果要修复，有哪些风险？']
  : ['分析这条告警', '分析这次 Trace', '检查这个服务', '查看最近发布影响'])
// 上下文来源徽标（resolved 后固定显示；draft 显示自然语言模式）。
const sourceLabel = computed(() => {
  const src = store.session?.context?.source
  if (src === 'alert') return '来自告警'
  if (src === 'trace') return '来自 Trace'
  if (src === 'text') return '自然语言识别'
  if (src === 'workload') return '工作负载'
  return ''
})
watch(hasReport, value => { mode.value = value ? 'explain' : 'investigate' }, { immediate: true })
watch(() => props.sessionId, async id => { await store.initialize(); if (id) await store.selectSession(id) }, { immediate: true })
watch(() => store.messages.map(m => `${m.id}:${m.status}:${m.text.length}`).join('|'), async () => {
  await nextTick(); if (scroll.value) scroll.value.scrollTop = scroll.value.scrollHeight
})
function useSuggestion(q: string) { store.draft = q; void store.send(hasReport.value ? 'explain' : 'investigate') }
async function openHistory() { historyOpen.value = !historyOpen.value; if (historyOpen.value) { try { await store.refreshSessions() } catch { store.error = '会话列表读取失败，请重试。' } } }
function newChat() { store.newConversation(); historyOpen.value = false; mode.value = 'investigate' }
async function choose(id: string) { await store.selectSession(id); historyOpen.value = false }
function sendKey(e: KeyboardEvent) { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); void store.send(mode.value) } }
</script>

<template>
  <section class="conversation" :class="{ full }" aria-label="Agent 对话">
    <div class="conversation-toolbar">
      <div class="agent-select">
        <Icon name="bot" :size="18" />
        <select v-model="store.selectedAgent" aria-label="选择 Agent" :disabled="!!store.session || store.sending">
          <option v-for="a in store.agents" :key="a.key" :value="a.key" :disabled="!!a.disabledReason">{{ a.name }}</option>
        </select>
        <span v-if="store.agent?.readOnly" class="badge">只读</span>
      </div>
      <button class="act small" :aria-expanded="historyOpen" @click="openHistory"><Icon name="clock" :size="14" />历史对话</button>
      <button class="act small" @click="newChat"><Icon name="plus" :size="14" />新对话</button>
    </div>
    <div v-if="historyOpen" class="history-list" aria-label="历史会话">
      <p v-if="!store.sessions.length">还没有历史对话。</p>
      <button v-for="s in store.sessions" :key="s.id" :class="{ current: s.id === store.session?.id }" @click="choose(s.id)">
        <span>{{ s.target ? (s.target.workloadName || s.target.workloadId) : (s.context?.source === 'alert' ? '告警诊断' : s.context?.source === 'trace' ? 'Trace 诊断' : '待定目标') }}</span><small>{{ s.target ? (s.target.envName || s.target.envId) + ' · ' : '' }}{{ new Date(s.updatedAt).toLocaleString() }}</small>
      </button>
      <RouterLink to="/assistant" @click="store.closePanel()">查看全部会话 →</RouterLink>
    </div>
    <div v-if="target" class="scope-bar">
      <Icon name="server" :size="14" /><strong>{{ target.workloadName || target.workloadId }}</strong>
      <span>{{ target.envName || target.envId }} / {{ target.laneId || '默认泳道' }}</span>
      <small v-if="store.session?.focus?.podName">实例 {{ store.session.focus.podName }}</small>
      <span v-if="sourceLabel" class="badge">{{ sourceLabel }}</span>
      <button class="text-button" title="为另一个目标新建会话" @click="newChat">更换目标</button>
    </div>
    <div v-else class="scope-bar scope-draft">
      <Icon name="bot" :size="14" /><strong>自然语言模式</strong>
      <span>直接描述问题，我会从内容里识别服务、告警或 Trace ID</span>
    </div>
    <div ref="scroll" class="message-list" role="log" aria-label="问答记录" aria-live="polite" :aria-busy="store.loading">
      <button v-if="store.hasMore" class="older" @click="store.loadOlder()">加载更早的对话</button>
      <div v-if="store.loading" class="empty-chat">正在恢复对话…</div>
      <div v-else-if="!store.messages.length" class="welcome">
        <span class="assistant-mark"><Icon name="bot" :size="25" /></span>
        <h2>一起定位问题</h2>
        <p>{{ store.agent?.description || '告诉我你遇到的现象。我可以检查运行状态、事件、日志和指标，结合证据解释原因，并帮你判断下一步。' }}</p>
        <p class="welcome-note">当前会话只读。历史问答会保留在当前账号下，操作建议由你决定。</p>
      </div>
      <article v-for="m in store.messages" :key="m.id" class="message" :class="m.role">
        <div class="message-author"><Icon v-if="m.role === 'assistant'" name="bot" :size="15" /><strong>{{ m.role === 'user' ? '你' : store.agent?.name || '助手' }}</strong><time>{{ new Date(m.createdAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) }}</time></div>
        <div v-if="m.text" class="message-text">{{ m.text }}</div>
        <template v-if="m.role === 'assistant' && m.run">
          <details v-if="!isTerminal(m.run.status) || store.events[m.runId]?.length" class="progress" :open="!isTerminal(m.run.status)">
            <summary>{{ isTerminal(m.run.status) ? '查看本轮检查过程' : statusLabel[m.run.status] }}<span v-if="m.run.mode === 'explain'"> · 正在解释历史证据</span></summary>
            <RunTimeline :run="m.run" :events="store.events[m.runId] || []" :polling="!!store.activeRun" />
          </details>
          <details v-if="m.resultKind === 'diagnosis_report' && m.run.report" class="artifact">
            <summary><Icon name="activity" :size="15" />诊断报告与证据 <span>{{ m.run.evidence?.length || 0 }} 条记录</span></summary>
            <DiagnosisReport :run="m.run" :workload-id="target?.workloadId" />
          </details>
        </template>
      </article>
      <div v-if="store.pendingResolution" class="resolution-card" role="dialog" aria-label="补充诊断目标">
        <div class="resolution-head"><Icon name="bot" :size="15" /><strong>{{ store.pendingResolution.resolution.status === 'need_choice' ? '请选择诊断目标' : '需要补充信息' }}</strong><span v-if="store.pendingResolution.resolution.anchor?.kind" class="badge">{{ store.pendingResolution.resolution.anchor.kind === 'alert' ? '来自告警' : '来自 Trace' }}</span></div>
        <p class="resolution-question">{{ store.pendingResolution.resolution.question }}</p>
        <div v-if="store.pendingResolution.resolution.status === 'need_choice'" class="candidate-list">
          <button v-for="c in store.pendingResolution.resolution.candidates || []" :key="c.id" :disabled="store.sending" @click="store.selectCandidate(c.id)">
            <strong>{{ c.workloadName || c.workloadId }}</strong>
            <small>{{ c.envName || c.envId }}{{ c.service ? ' · ' + c.service : '' }}</small>
          </button>
        </div>
        <p class="resolution-note">选择后开始只读诊断；在此之前不会调用工具、不会消耗模型预算。</p>
      </div>
    </div>
    <div class="composer-area">
      <p v-if="store.error || store.resourceError || store.agent?.disabledReason" class="error" role="alert">{{ store.error || store.resourceError || store.agent?.disabledReason }}<button v-if="store.resourceError" class="text-button" @click="store.initialize()">重新加载</button></p>
      <div v-if="!store.activeRun" class="suggestions"><button v-for="q in suggestions" :key="q" @click="useSuggestion(q)">{{ q }}</button></div>
      <div class="composer">
        <textarea v-model="store.draft" aria-label="发送给 Agent 的消息" rows="3" maxlength="2000" :placeholder="store.messages.length ? '继续问我如何判断、验证或处理…' : '描述问题并带上线索，例如：shop-web 发布后一直重启 / 告警 rule-xx / Trace 4bf9…'" @keydown="sendKey" />
        <div class="composer-actions">
          <select v-model="mode" aria-label="本轮工作方式" :disabled="!!store.activeRun"><option value="investigate">检查最新状态</option><option value="explain" :disabled="!hasReport">讨论已有证据</option></select>
          <button v-if="store.activeRun" class="act" @click="store.stop()">停止本轮</button>
          <button v-else class="act primary" :disabled="!store.draft.trim() || store.sending || !!store.agent?.disabledReason || !store.agent" @click="store.send(mode)">{{ store.sending ? '发送中…' : '发送' }}<Icon name="chevron" :size="14" /></button>
        </div>
      </div>
      <p v-if="store.settings" class="composer-note">{{ store.settings.persistent ? `对话保留 ${store.settings.historyDays} 天，证据片段保留 ${store.settings.evidenceDays} 天` : '当前为临时会话，服务重启后不会保留；请勿将此处作为长期记录' }}</p>
      <p class="composer-note">Enter 发送 · Shift+Enter 换行 · {{ mode === 'explain' ? '使用历史证据，不代表当前状态' : '检查限定在当前目标，进度与结果记录在本轮回答中' }}</p>
    </div>
  </section>
</template>

<style scoped>
.conversation{display:flex;flex-direction:column;min-height:0;height:100%;background:var(--surface);color:var(--text)}
.conversation-toolbar{display:flex;align-items:center;gap:8px;padding:12px 18px;border-bottom:1px solid var(--border);flex-wrap:wrap}.agent-select{display:flex;align-items:center;gap:8px;margin-right:auto}.agent-select select{max-width:180px;font-weight:600}.badge{font-size:11px;color:var(--text-dim);border:1px solid var(--border);border-radius:var(--radius-sm);padding:2px 6px}
select{background:var(--surface);color:var(--text);border:1px solid var(--border-strong);border-radius:var(--radius-sm);padding:5px;font:inherit;font-size:12px;max-width:100%}.scope-bar{display:flex;align-items:center;gap:8px;padding:10px 18px;background:var(--surface-2);font-size:12px;flex-wrap:wrap}.scope-bar span,.scope-bar small{color:var(--text-dim)}.scope-bar .text-button{margin-left:auto;font-size:12px}
.message-list{flex:1;overflow-y:auto;padding:24px 20px;min-height:160px;scroll-behavior:smooth}.welcome{padding:32px 12px 20px;max-width:520px;margin:0 auto}.welcome h2{font-size:23px;margin:18px 0 10px}.welcome p{font-size:13px;line-height:1.8;color:var(--text-dim)}.welcome .welcome-note{font-size:12px;color:var(--text-faint)}.assistant-mark{display:inline-flex;padding:10px;border:1px solid var(--border);border-radius:var(--radius-lg);color:var(--brand)}
.message{margin:0 0 26px;max-width:100%;min-width:0}.message.user{margin-left:36px;background:var(--surface-2);border-radius:var(--radius-lg);padding:12px 14px}.message-author{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--text-dim);margin-bottom:8px}.message-author time{margin-left:auto;color:var(--text-faint);font-size:11px}.message-text{white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px;line-height:1.85}.message.assistant .message-author svg{color:var(--brand)}
.artifact,.progress{margin-top:12px;border:1px solid var(--border);border-radius:var(--radius);padding:10px;min-width:0}.artifact summary,.progress summary{display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer;color:var(--text-dim)}.artifact summary span{margin-left:auto;color:var(--text-faint)}.artifact[open] summary,.progress[open] summary{margin-bottom:12px}.older{display:block;margin:0 auto 20px;border:0;background:none;color:var(--brand);font:inherit;font-size:12px;cursor:pointer}
.composer-area{padding:12px 18px 14px;border-top:1px solid var(--border);background:var(--surface)}.suggestions{display:flex;gap:6px;flex-wrap:wrap;margin-bottom:10px}.suggestions button{border:1px solid var(--border);background:var(--surface);color:var(--text-dim);padding:5px 8px;border-radius:var(--radius-sm);font:inherit;font-size:11px;cursor:pointer}.suggestions button:hover{color:var(--brand);border-color:var(--brand)}.target-picker{display:flex;align-items:center;gap:10px;font-size:12px;color:var(--text-dim);margin-bottom:10px}.target-picker select{flex:1;min-width:0}.composer{border:1px solid var(--border-strong);border-radius:var(--radius-lg);background:var(--bg);padding:10px}.composer:focus-within{border-color:var(--brand)}textarea{width:100%;display:block;resize:vertical;max-height:180px;border:0;outline:none;background:transparent;color:var(--text);font:inherit;font-size:13px;line-height:1.7;min-height:56px}.composer-actions{display:flex;justify-content:space-between;align-items:center;gap:10px}.composer-actions select{border:0;background:transparent;color:var(--text-dim)}.composer-note{margin:7px 0 0;font-size:10px;color:var(--text-faint)}.error{font-size:12px;color:var(--danger);line-height:1.6;margin:0 0 10px}.history-list{max-height:250px;overflow:auto;padding:8px 18px;border-bottom:1px solid var(--border);background:var(--surface-2)}.history-list button{display:flex;flex-direction:column;gap:4px;width:100%;border:0;border-bottom:1px solid var(--border);background:transparent;color:var(--text);padding:10px;text-align:left;cursor:pointer}.history-list button.current{background:var(--brand-soft)}.history-list small,.history-list a{font-size:11px;color:var(--text-dim)}.history-list a{display:block;margin:10px}.full .message-list{padding:28px max(20px,calc((100% - 840px)/2))}.full .composer-area{padding-left:max(18px,calc((100% - 840px)/2));padding-right:max(18px,calc((100% - 840px)/2))}
.scope-draft{color:var(--text-dim)}.scope-draft span{font-size:12px}
.resolution-card{margin:0 0 24px;border:1px solid var(--brand);border-radius:var(--radius);padding:14px;background:var(--surface-2)}.resolution-head{display:flex;align-items:center;gap:8px;font-size:13px;margin-bottom:8px}.resolution-head svg{color:var(--brand)}.resolution-question{font-size:13px;line-height:1.7;margin:0 0 12px;color:var(--text)}.candidate-list{display:flex;flex-direction:column;gap:8px}.candidate-list button{display:flex;flex-direction:column;gap:4px;text-align:left;border:1px solid var(--border-strong);background:var(--surface);color:var(--text);border-radius:var(--radius-sm);padding:10px 12px;font:inherit;cursor:pointer}.candidate-list button:hover:not(:disabled){border-color:var(--brand)}.candidate-list button small{color:var(--text-dim);font-size:12px}.resolution-note{margin:10px 0 0;font-size:11px;color:var(--text-faint)}
@media(max-width:600px){.message-list{padding:18px 14px}.composer-area{padding:10px 12px}.conversation-toolbar{padding:10px 12px}.suggestions button{font-size:11px}.composer-note{font-size:10px}.message.user{margin-left:20px}}
@media(prefers-reduced-motion:reduce){.message-list{scroll-behavior:auto}}
</style>
