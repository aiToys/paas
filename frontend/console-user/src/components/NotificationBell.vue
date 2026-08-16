<template>
  <el-popover placement="bottom-end" :width="380" trigger="click" popper-class="notif-popper">
    <template #reference>
      <div class="bell" :class="{ has: unread > 0 }">
        <Icon name="message" :size="16" />
        <span v-if="unread" class="badge">{{ unread > 99 ? '99+' : unread }}</span>
      </div>
    </template>
    <div class="notif-head">
      <b>通知</b>
      <el-button v-if="items.length" size="small" text @click="markAllRead">全部已读</el-button>
    </div>
    <div class="notif-list">
      <div v-if="!items.length" class="empty">🎉 一切正常，没有需要关注的事件</div>
      <div
        v-for="n in items" :key="n.id" class="notif-item" :class="[n.severity, { read: isRead(n) }]"
        @click="go(n)"
      >
        <span class="dot"></span>
        <div class="body">
          <div class="title">{{ n.title }}</div>
          <div class="meta">{{ appName(n.appId) }}</div>
        </div>
      </div>
    </div>
  </el-popover>
</template>

<script setup lang="ts">
// 通知中心铃铛（L1 站内通知）：30s 轮询 /api/notifications 聚合事件，
// 未读状态存 localStorage（通知 ID 稳定：targetType:targetID:status）。
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { listNotifications, type Notification } from '@/api/change'
import Icon from '@/components/Icon.vue'

const READ_KEY = 'paas:notif-read'
const router = useRouter()
const items = ref<Notification[]>([])
const open = ref(false)
let timer: number | undefined

function readSet(): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem(READ_KEY) ?? '[]')) } catch { return new Set() }
}
const isRead = (n: Notification) => readSet().has(n.id)
const unread = computed(() => items.value.filter((n) => !isRead(n)).length)

function appName(id: string) { return id }

function markAllRead() {
  localStorage.setItem(READ_KEY, JSON.stringify([...readSet(), ...items.value.map((n) => n.id)]))
  // 触发响应式刷新（readSet 非响应式，借用 items 引用更新）
  items.value = [...items.value]
}

function go(n: Notification) {
  markOneRead(n)
  if (n.targetType === 'run') router.push(`/devops/runs/${n.targetId}`)
  else if (n.targetType === 'batch') router.push(`/devops/batches/${n.targetId}`)
  else router.push(`/devops/changes/${n.targetId}`)
}
function markOneRead(n: Notification) {
  const s = readSet()
  s.add(n.id)
  localStorage.setItem(READ_KEY, JSON.stringify([...s]))
  items.value = [...items.value]
}

async function load() {
  try { items.value = await listNotifications() } catch { /* 非关键（未登录/网络失败静默） */ }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 30000)
})
onUnmounted(() => clearInterval(timer))
</script>

<style scoped>
.bell { position: relative; display: flex; align-items: center; justify-content: center; width: 32px; height: 32px; border-radius: 6px; cursor: pointer; color: var(--el-text-color-regular); }
.bell:hover { background: var(--el-fill-color); }
.badge { position: absolute; top: 2px; right: 2px; min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px; background: var(--el-color-danger); color: #fff; font-size: 10px; line-height: 16px; text-align: center; }
.notif-head { display: flex; justify-content: space-between; align-items: center; padding: 4px 4px 8px; border-bottom: 1px solid var(--el-border-color-lighter); }
.notif-list { max-height: 360px; overflow: auto; }
.empty { padding: 24px 0; text-align: center; color: var(--el-text-color-placeholder); font-size: 13px; }
.notif-item { display: flex; gap: 10px; align-items: flex-start; padding: 10px 8px; border-radius: 6px; cursor: pointer; }
.notif-item:hover { background: var(--el-fill-color-light); }
.notif-item.read { opacity: 0.55; }
.dot { width: 8px; height: 8px; border-radius: 50%; margin-top: 6px; flex-shrink: 0; }
.notif-item.error .dot { background: var(--el-color-danger); }
.notif-item.warning .dot { background: var(--el-color-warning); }
.notif-item.info .dot { background: var(--el-color-primary); }
.title { font-size: 13px; line-height: 1.4; }
.meta { font-size: 12px; color: var(--el-text-color-placeholder); margin-top: 2px; }
</style>
