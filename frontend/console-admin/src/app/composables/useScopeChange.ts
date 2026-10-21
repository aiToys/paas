import { watch } from 'vue'
import { useScopeStore } from '@/app/stores/scope'

// 浏览范围切换联动（spec 2026-10-19 §7.1 / 复核 F01）：
// 切租户时先 reset（关抽屉/弹层、清详情 id 与勾选行——旧租户的详情在新范围下
// 是上下文误导），再 refresh 重拉列表。reset 先于 refresh 保证不出现
// 「新列表 + 旧详情」的中间态；旧请求丢弃由组件卸载自然覆盖（scope store 注释同款约定）。
export function useScopeChange(refresh: () => void, reset?: () => void) {
  const scopeStore = useScopeStore()
  watch(() => scopeStore.tenantId, () => {
    reset?.()
    refresh()
  })
}
