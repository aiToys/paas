<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listSessions, type AssistantSession } from '@/api/assistant'
import { useAssistantStore } from '@/stores/assistant'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import Icon from '@/components/Icon.vue'
const router = useRouter()
const store = useAssistantStore()
const sessions = ref<AssistantSession[]>([])
const loading = ref(false)
const error = ref('')
const more = ref(true)
async function load(older = false) {
  loading.value = true; error.value = ''
  try {
    const rows = await listSessions(older ? sessions.value.at(-1)?.updatedAt : '')
    sessions.value = older ? [...sessions.value, ...rows.filter(r => !sessions.value.some(s => s.id === r.id))] : rows
    more.value = rows.length === 20
  } catch (e) { error.value = (e as Error).message } finally { loading.value = false }
}
function open(id: string) { store.closePanel(); void router.push(`/assistant/sessions/${encodeURIComponent(id)}`) }
async function fresh() { await store.initialize(); store.newConversation(); store.panelOpen = true }
onMounted(() => load())

// 历史列表来源类型（spec 2026-10-29 §7：显示来源与目标摘要；旧 workload 会话照常恢复）。
const sourceTag = (s: (typeof sessions.value)[number]) => {
  const src = s.context?.source
  if (src === 'alert') return '告警诊断'
  if (src === 'trace') return 'Trace 诊断'
  if (src === 'text') return '自然语言识别'
  return '工作负载'
}
const sessionSourceLabel = (s: (typeof sessions.value)[number]) =>
  s.context?.source === 'alert' ? '告警诊断（未选定目标）' : s.context?.source === 'trace' ? 'Trace 诊断（未选定目标）' : '对话（未选定目标）'
</script>
<template>
  <div class="page">
    <PageHeader title="AI 助手对话" description="恢复此前的完整问答、检查过程和证据，或开启新的讨论。"><button class="act" :disabled="loading" @click="load()">刷新</button><button class="act primary" @click="fresh"><Icon name="plus" :size="15" />新对话</button></PageHeader>
    <StatePanel v-if="error" error title="会话读取失败" :description="error"><button class="act" @click="load()">重试</button></StatePanel>
    <StatePanel v-else-if="!loading && !sessions.length" title="还没有对话" description="打开顶栏 AI 助手，描述问题并选择工作负载开始讨论。"><button class="act" @click="fresh">打开助手</button></StatePanel>
    <div class="session-list"><button v-for="s in sessions" :key="s.id" class="session-row" @click="open(s.id)"><Icon :name="s.context?.source === 'alert' || s.context?.source === 'trace' ? 'activity' : 'bot'" :size="20" /><span><strong>{{ s.target ? (s.target.workloadName || s.target.workloadId) : sessionSourceLabel(s) }}</strong><small>{{ (s.target ? (s.target.envName || s.target.envId) + ' / ' + (s.target.laneId || '默认泳道') + ' · ' : '') + (s.target ? sourceTag(s) + ' · ' : '') }}点击恢复完整对话</small></span><time>{{ new Date(s.updatedAt).toLocaleString() }}</time><span aria-hidden="true">→</span></button></div>
    <button v-if="more && sessions.length" class="act load-more" :disabled="loading" @click="load(true)">{{ loading ? '读取中…' : '加载更多对话' }}</button>
  </div>
</template>
<style scoped>.session-list{border:1px solid var(--border);border-radius:var(--radius-lg);overflow:hidden;background:var(--surface)}.session-list:empty{display:none}.session-row{display:flex;align-items:center;gap:14px;width:100%;background:transparent;border:0;border-bottom:1px solid var(--border);color:var(--text);font:inherit;text-align:left;cursor:pointer;padding:20px}.session-row:last-child{border-bottom:0}.session-row:hover{background:var(--surface-hover)}.session-row>span:first-of-type{display:flex;flex-direction:column;gap:8px;flex:1;min-width:0}.session-row strong{font-size:14px;overflow-wrap:anywhere}.session-row small,.session-row time{font-size:12px;color:var(--text-dim)}.session-row svg{color:var(--brand)}.load-more{margin:20px auto}@media(max-width:600px){.session-row{flex-wrap:wrap}.session-row time{width:100%;padding-left:34px}}</style>
