<script setup lang="ts">
import { nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useAssistantStore } from '@/stores/assistant'
import AssistantConversation from './AssistantConversation.vue'
const store = useAssistantStore()
const router = useRouter()
function expand() { const id = store.session?.id; store.closePanel(); void router.push(id ? `/assistant/sessions/${encodeURIComponent(id)}` : '/assistant') }
function opened() { const log = document.querySelector(".assistant-drawer [aria-label=问答记录]"); if (log instanceof HTMLElement) log.scrollTop = log.scrollHeight }
async function closed() { await nextTick(); document.getElementById('assistant-trigger')?.focus() }
</script>
<template>
  <el-drawer v-model="store.panelOpen" title="AI 助手" direction="rtl" size="min(680px, 100vw)" :destroy-on-close="false" class="assistant-drawer" @closed="closed" @opened="opened">
    <template #header><span id="assistant-dialog">AI 助手</span><button class="text-button" @click="expand">展开完整页面 ↗</button></template>
    <AssistantConversation v-if="store.panelOpen" />
  </el-drawer>
</template>
<style>
.assistant-drawer .el-drawer__header{margin:0;padding:16px 20px;border-bottom:1px solid var(--border);color:var(--text);font-size:14px}.assistant-drawer .el-drawer__header .text-button{font-size:12px;margin-right:12px}.assistant-drawer .el-drawer__body{padding:0;overflow:hidden}.assistant-drawer{max-width:100vw}
</style>
