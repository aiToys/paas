import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

// tab 容器页的 query 双向同步（spec 2026-10-19 D1）：
//   - ?tab= 定位初始 tab（深链分享 / 兼容 redirect 落点可用）
//   - 切 tab 时 replace 写回 query（不产生历史记录，返回键仍回上一页）
//   - 未知 tab 值回退默认（防手输错值渲染空白 tab）
export function useRouteTab(defaultTab: string, known: readonly string[]) {
  const route = useRoute()
  const router = useRouter()

  const pick = (v: unknown): string | null =>
    typeof v === 'string' && known.includes(v) ? v : null

  const tab = ref(pick(route.query.tab) ?? defaultTab)

  watch(
    () => route.query.tab,
    (v) => {
      const next = pick(v)
      if (next && next !== tab.value) tab.value = next
    }
  )

  watch(tab, (v) => {
    if (route.query.tab !== v) {
      router.replace({ query: { ...route.query, tab: v } })
    }
  })

  return tab
}
