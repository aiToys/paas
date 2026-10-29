import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  type AgentDesc, type AssistantSession, type AssistantMessage, type AssistantEvent,
  type AssistantContextInput, type ResolutionRequired,
  createSession, createRun, listMessages, listSessions, listAgents,
  listEvents, cancelRun, isTerminal, getAssistantSettings, resolveContext, asResolutionRequired,
} from '@/api/assistant'
import { listWorkloads, type Workload } from '@/api/workload'
import { useSessionStore } from '@/stores/session'

export interface PresetTarget {
  workloadId?: string; workloadName?: string; service?: string; envName?: string
  symptom?: string; question?: string; sessionId?: string
  podName?: string; podUID?: string; container?: string
  /** 结构化上下文深链（告警/Trace 详情页）：服务端解析成授权目标，多候选走候选卡。 */
  context?: AssistantContextInput
}

/** 待补充上下文：409 target_resolution_required 的本地状态（候选卡/最小追问渲染源）。 */
export interface PendingResolution {
  question: string
  mode: 'investigate' | 'explain'
  resolution: ResolutionRequired
}

// 只持久化当前用户最后一个 session ID；问答内容始终从服务端恢复。
export const useAssistantStore = defineStore('assistant', () => {
  const panelOpen = ref(false)
  const preset = ref<PresetTarget | null>(null)
  const agents = ref<AgentDesc[]>([])
  const workloads = ref<Workload[]>([])
  const sessions = ref<AssistantSession[]>([])
  const session = ref<AssistantSession | null>(null)
  const messages = ref<AssistantMessage[]>([])
  const events = ref<Record<string, AssistantEvent[]>>({})
  const selectedAgent = ref('builtin:runtime-diagnosis')
  const selectedWorkload = ref('')
  const draft = ref('')
  const pendingResolution = ref<PendingResolution | null>(null)
  const error = ref('')
  const loading = ref(false)
  const sending = ref(false)
  const hasMore = ref(false)
  const before = ref('')
  const resourceError = ref('')
 const settings = ref<{persistent:boolean;historyDays:number;evidenceDays:number}|null>(null)
  let draftActive = false
  let owner = ''
  let generation = 0
  let selection = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let initialized: Promise<void> | null = null
  let retryKey = ''
  let retryBody = ''

  const activeRun = computed(() => [...messages.value].reverse().find(m => m.role === 'assistant' && m.run && !isTerminal(m.run.status))?.run ?? null)
  const agent = computed(() => agents.value.find(a => a.key === (session.value?.agentKey || selectedAgent.value)))
  const storageKey = () => `paas:assistant:last-session:${owner}`
  function stopPolling() { clearTimeout(timer); timer = undefined }
  function reset() {
    generation++; selection++; stopPolling(); owner = ''; initialized = null
    draftActive = false; selectedAgent.value = 'builtin:runtime-diagnosis'
    session.value = null; messages.value = []; events.value = {}; sessions.value = []
    preset.value = null; panelOpen.value = false; selectedWorkload.value = ''; draft.value = ''
    settings.value = null; agents.value = []; workloads.value = []; error.value = ''; resourceError.value = ''
    loading.value = false; sending.value = false; retryKey = ''; retryBody = ''
    pendingResolution.value = null
  }
  async function refreshSessions() { const g = generation; const rows = await listSessions(); if (g === generation) sessions.value = rows }
  async function initialize() {
    const uid = useSessionStore().profile?.id
    if (!uid) return
    if (owner !== uid) { reset(); owner = uid }
    if (initialized) return initialized
    const g = generation
    initialized = (async () => {
      const [a, s, w, config] = await Promise.allSettled([listAgents(), listSessions(), listWorkloads({}), getAssistantSettings()])
      if (g !== generation) return
      if (config.status === 'fulfilled') settings.value = config.value
      if (a.status === 'fulfilled') agents.value = a.value
      else error.value = '助手目录加载失败，请重试。'
      if (s.status === 'fulfilled') sessions.value = s.value
      else error.value = '会话列表加载失败，已有记录不会被清空。'
      if (w.status === 'fulfilled') workloads.value = w.value.filter(v => v.type === 'service')
      else resourceError.value = '工作负载读取失败，请稍后重试。'
      const remembered = sessionStorage.getItem(storageKey())
      const id = remembered && sessions.value.some(v => v.id === remembered) ? remembered : sessions.value[0]?.id
      if (!session.value && !preset.value && !draftActive && id) await selectSession(id)
    })()
    try { await initialized } catch (e) { if (g === generation) error.value = (e as Error).message } finally { initialized = null }
  }
  function newConversation(p?: PresetTarget) {
    draftActive = true; selection++; stopPolling(); session.value = null; messages.value = []; events.value = {}
    preset.value = p || null; selectedWorkload.value = p?.workloadId || ''; draft.value = p?.question || ''
    error.value = ''; hasMore.value = false; before.value = ''; retryKey = ''; retryBody = ''
    pendingResolution.value = null
  }
  async function openPanel(p?: PresetTarget) {
    await initialize()
    if (p?.sessionId) await selectSession(p.sessionId)
    else if (p && (p.workloadId !== session.value?.target?.workloadId || p.podUID !== session.value?.focus?.podUID || (p.context && p.context.id !== session.value?.context?.anchors?.[0]?.id))) newConversation(p)
    if (p?.question) draft.value = p.question
    panelOpen.value = true
  }
  function closePanel() { panelOpen.value = false }
  async function selectSession(id: string) {
    const token = ++selection; const g = generation; stopPolling(); loading.value = true; error.value = ''
    try {
      const c = await listMessages(id)
      if (token !== selection || g !== generation) return
      draftActive = false; session.value = c.session; messages.value = c.messages; hasMore.value = c.hasMore; before.value = c.before || ''
      selectedWorkload.value = c.session.target?.workloadId || ''; selectedAgent.value = c.session.agentKey
      preset.value = null; draft.value = ''; events.value = {}; sessionStorage.setItem(storageKey(), id)
      pendingResolution.value = null
      schedule(token)
    } catch (e) { if (token === selection && g === generation) error.value = (e as Error).message }
    finally { if (token === selection) loading.value = false }
  }
  async function loadOlder() {
    if (!session.value || !hasMore.value) return
    const id = session.value.id; const token = selection
    try {
      const c = await listMessages(id, before.value)
      if (token !== selection) return
      const seen = new Set(messages.value.map(m => m.id))
      messages.value = [...c.messages.filter(m => !seen.has(m.id)), ...messages.value]
      hasMore.value = c.hasMore; before.value = c.before || ''
    } catch (e) { error.value = (e as Error).message }
  }
  async function refreshConversation() {
    if (!session.value) return
    const id = session.value.id; const token = selection
    const c = await listMessages(id)
    if (token !== selection) return
    const fresh = new Map(c.messages.map(m => [m.id, m]))
    messages.value = [...messages.value.map(m => fresh.get(m.id) || m), ...c.messages.filter(m => !messages.value.some(v => v.id === m.id))]
    session.value = c.session
  }
  function schedule(token = selection) {
    stopPolling()
    if (!activeRun.value || token !== selection) return
    timer = setTimeout(async () => {
      if (token !== selection) return
      const id = activeRun.value?.id
      try {
        await refreshConversation()
        if (id && token === selection) {
          const old = events.value[id] || []
          const next = await listEvents(id, old.at(-1)?.seq || 0)
          if (token === selection) events.value[id] = [...old, ...next.filter(v => !old.some(e => e.seq === v.seq))]
        }
        error.value = ''
      } catch (e) { if (token === selection) { error.value = (e as Error).message; if ([401, 403, 404].includes((e as Error & { status?: number }).status || 0)) return } }
      if (token === selection) schedule(token)
    }, panelOpen.value && document.visibilityState !== 'hidden' ? 2000 : 10000)
  }
  async function send(mode: 'investigate' | 'explain' = 'investigate') {
    const question = draft.value.trim()
    if (!question || sending.value || activeRun.value) return
    // crypto.randomUUID 仅 secure context（HTTPS/localhost）存在；http 域名下走本地拼接
    // （幂等键只需客户端会话内唯一，后端无格式要求）。
    const newKey = (): string => crypto.randomUUID?.() ?? `key-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
    const g = generation; const token = selection; sending.value = true; error.value = ''
    try {
      if (!session.value) {
        // 自然语言优先：preset 无结构化上下文时建 draft 会话，目标由服务端从问题解析。
        const p = preset.value
        const body: Parameters<typeof createSession>[0] = { agentKey: selectedAgent.value }
        if (p?.context) body.context = p.context
        else if (p?.workloadId) body.target = { type: 'workload', id: p.workloadId }
        if (p && (p.podName || p.podUID || p.container)) body.focus = { podName: p.podName, podUID: p.podUID, container: p.container }
        const s = await createSession(body)
        if (g !== generation || token !== selection) return
        draftActive = false; session.value = s; sessionStorage.setItem(storageKey(), s.id)
        // 结构化锚点解析出多候选：会话即待选状态（选择前不得发 run）。
        if (s.candidates?.length) {
          pendingResolution.value = { question, mode, resolution: { status: 'need_choice', question: '请选择诊断目标', candidates: s.candidates, anchor: s.context?.anchors?.[0] } }
          draft.value = ''; return
        }
      }
      const body = { question, mode, lookbackMinutes: 30 }
      const serialized = JSON.stringify(body)
      if (retryBody !== serialized) { retryKey = newKey(); retryBody = serialized }
      await createRun(session.value.id, body, retryKey)
      if (g !== generation || token !== selection) return
      retryBody = ''; retryKey = ''; draft.value = ''; pendingResolution.value = null
      await refreshConversation(); schedule(token)
      await refreshSessions()
    } catch (e) {
      if (g !== generation || token !== selection) return
      // 409 target_resolution_required：多候选/最小追问（服务端未建 run、未扣预算）。
      const resolution = asResolutionRequired(e)
      if (resolution) {
        pendingResolution.value = { question, mode, resolution }
        draft.value = ''
      } else {
        error.value = (e as Error).message
      }
    } finally { if (g === generation) sending.value = false }
  }

  // 候选确认：resolve → resolved 会话 → 用原问题自动重发（新幂等键）。
  async function selectCandidate(candidateId: string) {
    if (!session.value || !pendingResolution.value || sending.value) return
    const { question, mode } = pendingResolution.value
    const g = generation; const token = selection; error.value = ''
    try {
      const s = await resolveContext(session.value.id, candidateId)
      if (g !== generation || token !== selection) return
      session.value = s; pendingResolution.value = null
      draft.value = question
      // send 自管 sending（此处不再置位，防 send 入口自锁）。
      await send(mode)
      await refreshSessions()
    } catch (e) { if (g === generation && token === selection) error.value = (e as Error).message }
  }
  async function stop() { if (!activeRun.value) return; try { await cancelRun(activeRun.value.id); await refreshConversation(); schedule() } catch (e) { error.value = (e as Error).message } }
  return { panelOpen, preset, session, messages, events, sessions, agents, workloads, selectedAgent, selectedWorkload, draft, pendingResolution, error, resourceError, loading, sending, hasMore, agent, activeRun, settings,
    initialize, openPanel, closePanel, newConversation, selectSession, loadOlder, send, stop, selectCandidate, reset, stopPolling, refreshConversation, refreshSessions }
})
