import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface TagsViewItem {
  title: string
  name?: string
  fullPath: string
  path?: string
  meta?: TagsViewItemMeta
  params?: object
  query?: object
}

export interface TagsViewItemMeta {
  title: string
  affix: boolean
}

export interface TagsViewPayload {
  index: number
  type: 'index' | 'other' | 'right' | 'all'
}

const defaultTagsViewItem: TagsViewItem = {
  fullPath: '/',
  meta: { title: '首页', affix: true },
  name: 'home',
  params: {},
  path: '/',
  query: {},
  title: '首页'
}

/**
 * TagsView（多标签页）状态。
 *
 * 迁移自 `src/stores/tagsView.ts`，逻辑保持一致。
 * `removeTagsView({ type: 'index', index })` 即「关闭当前」语义，
 * 由 `Index.vue` 的关闭按钮和 `ContextMenu.vue` 的「关闭当前」菜单项共享调用。
 */
export const useTagsViewStore = defineStore('tagsView', () => {
  const tagsViewList = ref<TagsViewItem[]>([defaultTagsViewItem])

  const addTagsViewList = (tag: TagsViewItem) => {
    // 去重键用 fullPath（含 query）：同路由不同 query 是不同标签页（如详情页按 id 区分），
    // 按 path 去重会合并丢页（R4 审计 F2）。
    const exists = tagsViewList.value.some((item) => item.fullPath === tag.fullPath)
    if (!exists) {
      tagsViewList.value.push(tag)
    }
  }

  const removeTagsView = (payload: TagsViewPayload) => {
    if (payload.type === 'index') {
      tagsViewList.value.splice(payload.index, 1)
    } else if (payload.type === 'other') {
      tagsViewList.value.splice(
        payload.index + 1,
        tagsViewList.value.length - payload.index + 1
      )
      tagsViewList.value.splice(0, payload.index)
      if (payload.index !== 0) {
        tagsViewList.value.unshift(defaultTagsViewItem)
      }
    } else if (payload.type === 'right') {
      tagsViewList.value.splice(
        payload.index + 1,
        tagsViewList.value.length - payload.index + 1
      )
    } else if (payload.type === 'all') {
      tagsViewList.value = []
    }
  }

  return {
    tagsViewList,
    addTagsViewList,
    removeTagsView
  }
})
