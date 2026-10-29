<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getSession, getRun } from '@/api/assistant'
import AssistantConversation from '@/components/assistant/AssistantConversation.vue'
import StatePanel from '@/components/StatePanel.vue'
const route = useRoute(), router = useRouter()
const sessionId = ref(''), error = ref('')
let sequence = 0
watch(() => String(route.params.id), async id => {
  if (id === sessionId.value) return
  const token = ++sequence; sessionId.value = ''; error.value = ''
  try {
    let resolved = id
    try { await getSession(id) } catch (e) {
      if ((e as Error & {status?: number}).status !== 404) throw e
      resolved = (await getRun(id)).sessionId // 兼容旧版误用 run ID 的已保存链接。
    }
    if (token !== sequence) return
    sessionId.value = resolved
    if (resolved !== id) await router.replace(`/assistant/sessions/${encodeURIComponent(resolved)}`)
  } catch (e) { if (token === sequence) error.value = (e as Error).message }
}, { immediate: true })
</script>
<template><div class="assistant-full-page"><StatePanel v-if="error" error title="会话无法读取" :description="error" /><AssistantConversation v-else-if="sessionId" :session-id="sessionId" full /><p v-else class="loading">正在恢复对话…</p></div></template>
<style scoped>.assistant-full-page{height:calc(100dvh - 140px);min-height:500px;border:1px solid var(--border);border-radius:var(--radius-lg);overflow:hidden}.loading{padding:24px;color:var(--text-dim);font-size:13px}@media(max-width:600px){.assistant-full-page{height:calc(100dvh - 110px);min-height:400px}}</style>
