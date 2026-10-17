<script setup lang="ts">
// 容器终端面板（spec 2026-10-17 §6）：受理（reason 必填）→ WS 桥 → xterm 渲染。
// URL 不携带 token（cookie 鉴权 + 同源 Origin）；销毁即关闭会话（DELETE）。
import { computed, onUnmounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { apiError, fetchAuth } from '@/api'
import { createTerminalSession, type RuntimeInstance } from '@/api/workload'

const props = defineProps<{ workloadId: string; instance: RuntimeInstance; enabled: boolean; disabledReason?: string }>()
const emit = defineEmits<{ operated: [detail: string] }>()

const containers = computed(() => props.instance.containers || [])
const container = ref('')
const shell = ref('/bin/sh')
const reason = ref('')
const phase = ref<'idle' | 'connecting' | 'connected' | 'closed' | 'error'>('idle')
const message = ref('')
let session: { id: string; path: string } | null = null
let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
const stage = ref<HTMLElement>()

function teardown(code: number) {
  if (ws) { const s = ws; ws = null; try { s.close(code, 'client') } catch { /* 已关闭 */ } }
  if (session) { const s = session; session = null; void fetchAuth(s.path, { method: 'DELETE' }).catch(() => { /* 服务端已回收 */ }) }
  if (term) { term.dispose(); term = null; fit = null }
}

async function connect() {
  if (!reason.value.trim() || phase.value === 'connecting' || phase.value === 'connected') return
  phase.value = 'connecting'; message.value = ''
  try {
    const created = await createTerminalSession(props.workloadId, props.instance.name, {
      podUid: props.instance.uid || '', container: container.value || undefined,
      shell: shell.value, reason: reason.value.trim(),
    })
    if (!created?.sessionId || !created.streamPath) throw new Error('受理响应缺少会话信息')
    session = { id: created.sessionId, path: created.streamPath }
    const el = stage.value
    if (!el) throw new Error('终端容器未就绪')
    term = new Terminal({ cursorBlink: true, fontSize: 12, fontFamily: 'var(--font-mono)', theme: { background: '#0b1220' } })
    fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    fit.fit()
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const sock = new WebSocket(`${proto}://${location.host}${created.streamPath}`)
    ws = sock
    sock.onopen = () => {
      phase.value = 'connected'
      term?.write(`\x1b[1m已连接 ${props.instance.name}${container.value ? ' / ' + container.value : ''}（${shell.value}）\x1b[0m\r\n会话最长 30 分钟，空闲 5 分钟自动关闭。\r\n\r\n`)
      sock.send(JSON.stringify({ type: 'resize', cols: term?.cols || 80, rows: term?.rows || 24 }))
    }
    sock.onmessage = (ev) => {
      try {
        const msg = JSON.parse(String(ev.data))
        if (msg.type === 'output' && typeof msg.data === 'string') term?.write(msg.data)
        else if (msg.type === 'exit') { message.value = '容器内会话已退出'; phase.value = 'closed'; teardown(1000) }
        else if (msg.type === 'error') { message.value = String(msg.message || '会话错误'); phase.value = 'error'; teardown(1000) }
      } catch { /* 非 JSON 帧忽略 */ }
    }
    sock.onclose = () => { if (phase.value === 'connected') { phase.value = 'closed'; message.value = '连接已关闭' } teardown(1000) }
    sock.onerror = () => { if (phase.value !== 'error') { phase.value = 'error'; message.value = 'WebSocket 连接失败' } }
    term.onData((data) => { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'input', data })) })
    term.onResize(({ cols, rows }) => { if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows })) })
    term.focus()
    emit('operated', `终端连接 ${props.instance.name}`)
  } catch (e) {
    message.value = apiError(e, '终端会话建立失败')
    phase.value = 'error'
    teardown(1000)
  }
}

function disconnect() { teardown(1000); phase.value = 'closed'; message.value = '已主动断开' }
function retry() { teardown(1000); phase.value = 'idle'; message.value = '' }
onUnmounted(teardown.bind(null, 1000))
</script>

<template>
  <section class="terminal-panel" aria-label="容器终端">
    <div class="terminal-heading"><strong>容器终端</strong><span class="status" :class="phase === 'connected' ? 'ok' : phase === 'error' ? 'err' : 'idle'">{{ phase === 'connected' ? '已连接' : phase === 'connecting' ? '连接中' : phase === 'error' ? '连接失败' : phase === 'closed' ? '已断开' : '未连接' }}</span></div>
    <template v-if="enabled">
      <p class="terminal-desc">在 Pod <span class="mono">{{ instance.name }}</span> 的容器内启动交互 shell。会话绑定当前实例 UID，连接后容器内命令在本面板执行。</p>
      <div v-if="phase === 'idle' || phase === 'error' || phase === 'closed'" class="terminal-form">
        <label v-if="containers.length > 1">容器<select v-model="container" class="filter-select"><option value="">默认容器</option><option v-for="c in containers" :key="c.name" :value="c.name" :disabled="c.state !== 'running'">{{ c.name }}（{{ c.state }}）</option></select></label>
        <label>Shell<select v-model="shell" class="filter-select"><option value="/bin/sh">/bin/sh</option><option value="/bin/bash">/bin/bash</option></select></label>
        <label class="reason-field">操作原因<input v-model="reason" class="search-field" placeholder="必填：为何进入该容器（记入审计）" /></label>
        <button class="act primary" :disabled="!reason.trim() || !instance.uid" @click="connect">连接容器</button>
      </div>
      <p v-if="message" class="terminal-message" role="status">{{ message }}<button v-if="phase !== 'connected'" class="text-button" @click="retry">重新发起</button></p>
      <div v-show="phase === 'connected' || phase === 'connecting'" ref="stage" class="terminal-stage live" tabindex="0" aria-label="终端输出"></div>
      <div v-if="phase === 'connected'" class="terminal-footer"><span>生产环境操作需谨慎；所有连接会记录审计。</span><button class="act" @click="disconnect">断开</button></div>
    </template>
    <template v-else>
      <p class="terminal-desc">{{ disabledReason || '容器终端未启用（需平台开启 PAAS_TERMINAL_ENABLED 并授予 workload:exec 权限）。' }}</p>
      <div class="terminal-stage"><span class="terminal-prompt mono">&gt;_</span><strong>容器终端未开放</strong><span>当前不会建立会话或执行命令</span></div>
    </template>
  </section>
</template>

<style scoped>
@import './runtime-tabs.css';
.terminal-heading { display: flex; justify-content: space-between; align-items: center; gap: 16px; }
.terminal-desc { font-size: 12px; color: var(--text-faint); margin: 8px 0 16px; }
.terminal-form { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 12px; margin-bottom: 12px; }
.terminal-form label { display: flex; flex-direction: column; gap: 5px; font-size: 12px; color: var(--text-faint); }
.reason-field { flex: 1 1 240px; }
.terminal-message { display: flex; align-items: center; gap: 12px; font-size: 12px; color: var(--warning); }
.terminal-stage { min-height: 280px; border: 1px solid var(--border); border-radius: var(--radius); background: var(--inset-bg); display: flex; flex-direction: column; justify-content: center; align-items: center; gap: 12px; padding: 24px; text-align: center; }
.terminal-stage span { color: var(--text-faint); font-size: 12px; } .terminal-stage .terminal-prompt { font-size: 30px; }
.terminal-stage.live { display: block; padding: 8px; text-align: left; }
.terminal-footer { display: flex; justify-content: space-between; align-items: center; gap: 16px; margin-top: 16px; flex-wrap: wrap; font-size: 12px; color: var(--text-faint); }
</style>
