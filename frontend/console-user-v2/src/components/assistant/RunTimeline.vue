<script setup lang="ts">
// 任务进度时间线（spec §9）：事件流 + 工具名中文化；轮询中的呼吸点提示仍在进行。
import { computed } from 'vue'
import { AssistantEvent, AssistantRun, eventLabel, isTerminal, toolLabel } from '@/api/assistant'

const props = defineProps<{ run: AssistantRun; events: AssistantEvent[]; polling: boolean }>()

interface Row { key: string; label: string; detail?: string; time: string; kind: 'info' | 'tool' | 'error' | 'done' }

const rows = computed<Row[]>(() => {
  const out: Row[] = props.events.map(e => {
    const tool = typeof e.payload?.tool === 'string' ? e.payload.tool : ''
    const detail = tool ? toolLabel[tool] || tool : ''
    const kind: Row['kind'] =
      e.type === 'run.failed' || e.type === 'run.timed_out' ? 'error'
        : e.type === 'report.ready' ? 'done'
          : e.type.startsWith('tool.') ? 'tool' : 'info'
    return { key: `${e.seq}-${e.type}`, label: eventLabel[e.type] || e.type, detail, time: new Date(e.createdAt).toLocaleTimeString('zh-CN'), kind }
  })
  return out
})

const active = computed(() => !isTerminal(props.run.status))
</script>

<template>
  <div class="timeline" role="log" :aria-label="'诊断进度（' + rows.length + ' 条）'">
    <div v-for="r in rows" :key="r.key" class="row" :class="r.kind">
      <span class="dot" />
      <div class="body">
        <b>{{ r.label }}</b><small v-if="r.detail">{{ r.detail }}</small>
      </div>
      <time>{{ r.time }}</time>
    </div>
    <div v-if="active" class="row pending">
      <span class="dot pulse" />
      <div class="body"><b>{{ polling ? (run.mode === 'explain' ? '正在整理历史证据并回答…' : '正在采集证据与分析…') : '等待执行…' }}</b><small>通常 30-90 秒；可随时停止</small></div>
    </div>
    <p v-if="!rows.length && !active" class="quiet">暂无事件记录。</p>
  </div>
</template>

<style scoped>
.timeline { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; display: flex; flex-direction: column; gap: 6px; max-height: 260px; overflow-y: auto; background: var(--surface); }
.row { display: flex; align-items: center; gap: 10px; font-size: 12px; }
.row time { margin-left: auto; color: var(--text-faint); font-size: 11px; white-space: nowrap; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--text-faint); flex: none; }
.row.tool .dot { background: var(--brand); }
.row.done .dot { background: var(--ok, #22a06b); }
.row.error .dot { background: var(--danger); }
.dot.pulse { background: var(--brand); animation: pulse 1.4s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: 0.35; } }
.body { display: flex; flex-direction: column; }
.body small { color: var(--text-faint); }
.quiet { color: var(--text-faint); font-size: 12px; margin: 4px 0; }
</style>
