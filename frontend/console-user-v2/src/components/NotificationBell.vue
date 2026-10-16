<template>
  <el-popover placement="bottom-end" :width="380" trigger="click" popper-class="notif-popper">
    <template #reference>
      <button type="button" :aria-label="'通知中心，' + unread + ' 条未读'" class="bell" :class="{ has: unread > 0 }">
        <Icon name="message" :size="16" />
        <span v-if="unread" class="badge">{{ unread > 99 ? '99+' : unread }}</span>
      </button>
    </template>
    <div class="notif-head">
      <b>通知</b>
      <el-button v-if="items.length" size="small" text @click="markAllRead">全部已读</el-button>
    </div>
    <div class="notif-list">
      <div v-if="failed" class="empty" role="alert">通知暂时无法加载 <button class="text-button" @click="load">重试</button></div>
      <div v-else-if="!items.length" class="empty">没有需要关注的事件</div>
      <div
        v-for="n in items" :key="n.id" class="notif-item" role="link" tabindex="0" @keydown.enter="go(n)" :class="[n.severity, { read: isRead(n) }]"
        @click="go(n)"
      >
        <span class="dot"></span>
        <div class="body">
          <div class="title">{{ n.title }}</div>
          <div class="meta mono">{{ n.appId }}</div>
        </div>
      </div>
    </div>
  </el-popover>
</template>

<script setup lang="ts">
// 通知中心铃铛（L1 站内通知）：30s 轮询 /api/notifications 聚合事件，
// 未读状态存 localStorage（通知 ID 稳定：targetType:targetID:status）。
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { listNotifications, type Notification } from '@/api/change'
import { usePolling } from '@/composables/usePolling'
import { useSessionStore } from '@/stores/session'
import Icon from '@/components/Icon.vue'
import { notificationLink } from '@/navigation'

const READ_KEY = 'paas:notif-read'
const READ_MAX = 500 // 已读集合上限（防 localStorage 无界增长，超出裁剪最旧）
const router = useRouter()
const items = ref<Notification[]>([])
const failed = ref(false)

// 响应式已读集合（一次性读入，不再每渲染解析 localStorage）
const readIds = ref<Set<string>>(new Set())
try { readIds.value = new Set(JSON.parse(localStorage.getItem(READ_KEY) ?? '[]')) } catch { /* 损坏重置 */ }

function persistRead() {
  // 保留最近 READ_MAX 条（数组序即插入序，超出截尾）
  const arr = [...readIds.value]
  localStorage.setItem(READ_KEY, JSON.stringify(arr.slice(-READ_MAX)))
}

const isRead = (n: Notification) => readIds.value.has(n.id)
const unread = computed(() => items.value.filter((n) => !isRead(n)).length)

function markAllRead() {
  for (const n of items.value) readIds.value.add(n.id)
  persistRead()
}

function go(n: Notification) {
  markOneRead(n)
  void router.push(notificationLink(n))
}
function markOneRead(n: Notification) {
  readIds.value.add(n.id)
  persistRead()
}

const session = useSessionStore()

async function load() {
  // 未登录跳过（防每 30s 一次注定 401 的无效请求）
  if (!session.profile) return
  try { items.value = await listNotifications(); failed.value = false } catch { failed.value = true }
}

// 30s 轮询（页面不可见自动暂停）+ 登录成功立即拉一次（usePolling 启动时可能未登录）
usePolling(load, 30000)
watch(() => session.profile, (p) => { items.value = []; failed.value = false; if (p) void load() })
</script>

<style scoped>
.bell { border: 0; background: transparent; position: relative; display: flex; align-items: center; justify-content: center; width: 32px; height: 32px; border-radius: 6px; cursor: pointer; color: var(--el-text-color-regular); }
.bell:hover { background: var(--el-fill-color); }
.badge { position: absolute; top: 2px; right: 2px; min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px; background: var(--danger-solid); color: #fff; font-size: 10px; line-height: 16px; text-align: center; }
.notif-head { display: flex; justify-content: space-between; align-items: center; padding: 4px 4px 8px; border-bottom: 1px solid var(--el-border-color-lighter); }
.notif-list { max-height: 360px; overflow: auto; }
.empty { padding: 24px 0; text-align: center; color: var(--el-text-color-placeholder); font-size: 13px; }
.notif-item { display: flex; gap: 10px; align-items: flex-start; padding: 10px 8px; border-radius: 6px; cursor: pointer; }
.notif-item:hover { background: var(--el-fill-color-light); }
.notif-item.read .title { color: var(--text-faint); }
.dot { width: 8px; height: 8px; border-radius: 50%; margin-top: 6px; flex-shrink: 0; }
.notif-item.error .dot { background: var(--el-color-danger); }
.notif-item.warning .dot { background: var(--el-color-warning); }
.notif-item.info .dot { background: var(--el-color-primary); }
.title { font-size: 13px; line-height: 1.4; }
.meta { font-size: 12px; color: var(--el-text-color-placeholder); margin-top: 2px; }
</style>
