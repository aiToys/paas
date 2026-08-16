<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

// 事件结构（与后端 bff /api/events 返回的 shopEvent JSON 一致）
interface Event {
  type: string
  productId: number
  name: string
  category: string
  at: string
  receivedAt: string
}

const events = ref<Event[]>([])
const unread = ref(0)
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
let timer: number | undefined

// 上次已读时间戳（localStorage 持久化，容错读取）
let lastSeen = 0
try {
  lastSeen = Number(localStorage.getItem('paas:events:lastSeen') || 0)
} catch {}

async function fetchEvents() {
  try {
    const resp = await fetch('/api/events?limit=20')
    const data: Event[] = await resp.json()
    events.value = data
    unread.value = data.filter(e => new Date(e.receivedAt).getTime() > lastSeen).length
  } catch (e) {
    console.debug('events fetch failed', e)
  }
}

// 类型中文映射
function typeLabel(type: string): string {
  const map: Record<string, string> = {
    'product.created': '商品上新',
    'product.updated': '商品变更',
    'product.bulk-seed': '批量导入',
  }
  return map[type] || type
}

// 相对时间：x 秒前 / x 分钟前（负数或非法时间显示空串）
function relTime(iso: string): string {
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (isNaN(s) || s < 0) return ''
  return s >= 60 ? `${Math.floor(s / 60)} 分钟前` : `${s} 秒前`
}

// 面板外点击关闭
function onDocClick(e: MouseEvent) {
  if (rootRef.value && !rootRef.value.contains(e.target as Node)) open.value = false
}

function toggle() {
  open.value = !open.value
  if (open.value) {
    // 打开即视为已读：lastSeen 取最大 receivedAt 与当前时间较大者
    const maxRecv = events.value.reduce((m, e) => Math.max(m, new Date(e.receivedAt).getTime() || 0), 0)
    lastSeen = Math.max(maxRecv, Date.now())
    try { localStorage.setItem('paas:events:lastSeen', String(lastSeen)) } catch {}
    unread.value = 0
    document.addEventListener('click', onDocClick)
  } else {
    document.removeEventListener('click', onDocClick)
  }
}

onMounted(() => {
  fetchEvents()
  timer = window.setInterval(fetchEvents, 10000)
  // 页面不可见时暂停轮询（后台 tab 持续打点会污染 bff trace/指标，回可见立即补拉）
  document.addEventListener('visibilitychange', onVisibility)
})

function onVisibility() {
  if (document.hidden) {
    if (timer) { clearInterval(timer); timer = undefined }
  } else {
    if (!timer) {
      fetchEvents()
      timer = window.setInterval(fetchEvents, 10000)
    }
  }
}

onUnmounted(() => {
  if (timer) clearInterval(timer)
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div ref="rootRef" class="notification">
    <button class="bell-btn" @click="toggle">🔔 消息
      <span v-if="unread > 0" class="bell-badge">{{ unread > 99 ? '99+' : unread }}</span>
    </button>
    <div v-if="open" class="notification-panel">
      <div v-if="events.length === 0" class="notification-empty">暂无消息</div>
      <div v-for="(e, i) in events" :key="i" class="notification-item">
        <span class="notification-badge">{{ typeLabel(e.type) }}</span>
        <span class="notification-name">{{ e.name }} / {{ e.category }}</span>
        <span class="notification-time">{{ relTime(e.receivedAt) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.notification { position: relative; }
.bell-btn { position: relative; background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 8px 16px; border-radius: 20px; cursor: pointer; font-size: 14px; }
.bell-btn:hover { background: rgba(255,255,255,.3); }
.bell-badge { position: absolute; top: -6px; right: -6px; background: #e74c3c; color: #fff; font-size: 11px; line-height: 1; padding: 3px 6px; border-radius: 10px; font-weight: 600; }
.notification-panel { position: absolute; right: 0; top: calc(100% + 8px); width: 300px; max-height: 360px; overflow-y: auto; background: #fff; border-radius: 10px; box-shadow: 0 4px 16px rgba(0,0,0,.15); padding: 8px 0; z-index: 100; }
.notification-item { display: flex; align-items: center; gap: 8px; padding: 8px 12px; }
.notification-item:hover { background: #f8f9fb; }
.notification-badge { flex-shrink: 0; font-size: 12px; color: #667eea; background: rgba(102,126,234,.12); border-radius: 4px; padding: 2px 6px; }
.notification-name { flex: 1; font-size: 13px; color: #2c3e50; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.notification-time { flex-shrink: 0; font-size: 12px; color: #95a5a6; }
.notification-empty { padding: 20px; text-align: center; color: #95a5a6; font-size: 13px; }
</style>
