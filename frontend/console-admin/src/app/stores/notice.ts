import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

/** 公告信息（vue-admin 基座 notice 模块遗留类型；UI 壳保留，数据已短路为空）。 */
export interface NoticeInfo {
  id: string
  title: string
  content: string
  type: 'announcement' | 'notice' | 'todo'
  status: 'published' | 'draft' | 'expired'
  priority: 'high' | 'medium' | 'low'
  publishTime?: string
  expireTime?: string
  publisher: string
  createTime: string
  updateTime: string
}

export const useNoticeStore = defineStore('notice', () => {
  // 公告列表
  const notices = ref<NoticeInfo[]>([])
  const loading = ref(false)
  // 已读公告 ID
  const readIds = ref<Set<string>>(new Set())

  // 未读数量
  const unreadCount = computed(() => {
    return notices.value.filter(n => !readIds.value.has(n.id) && n.status === 'published').length
  })

  // 公告类型过滤
  const announcements = computed(() => notices.value.filter(n => n.type === 'announcement' && n.status === 'published'))
  const notifications = computed(() => notices.value.filter(n => n.type === 'notice' && n.status === 'published'))
  const todos = computed(() => notices.value.filter(n => n.type === 'todo' && n.status === 'published'))

  // 加载公告列表
  async function loadNotices() {
    // PaaS 控制面无公告端点（notice 是 vue-admin 基座自带模块，非 PaaS 业务）。
    // 短路避免每次布局挂载都打 /api/system/notice → 404 噪音（浏览器网络层错误无法 JS catch）。
    // UI 壳保留（Header 通知铃铛 + dashboard 横幅），公告接入后端时移除此短路即可。
    notices.value = []
  }

  // 标记已读
  function markAsRead(id: string) {
    readIds.value.add(id)
  }

  // 标记全部已读
  function markAllAsRead() {
    notices.value.forEach(n => readIds.value.add(n.id))
  }

  return {
    notices,
    loading,
    readIds,
    unreadCount,
    announcements,
    notifications,
    todos,
    loadNotices,
    markAsRead,
    markAllAsRead,
  }
})
