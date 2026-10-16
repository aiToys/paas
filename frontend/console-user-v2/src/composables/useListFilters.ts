import { reactive, watch } from 'vue'
import type { LocationQueryRaw } from 'vue-router'
import { useRoute, useRouter } from 'vue-router'
// 一次写入整组筛选，避免多个 query watcher 同时 replace 丢失彼此的参数。
export function useListFilters(defaults: Record<string, string>) {
  const route = useRoute()
  const router = useRouter()
  const read = () => Object.fromEntries(Object.entries(defaults).map(([key, value]) => [key, typeof route.query[key] === 'string' ? route.query[key] : value])) as Record<string, string>
  const filters = reactive(read())
  watch(() => route.query, () => { Object.assign(filters, read()) })
  watch(filters, (values) => {
    const query: LocationQueryRaw = { ...route.query }
    let changed = false
    for (const [key, fallback] of Object.entries(defaults)) {
      const value = values[key] === fallback ? undefined : values[key]
      if (query[key] !== value && !(query[key] == null && value == null)) changed = true
      query[key] = value
    }
    if (changed) void router.replace({ query })
  }, { flush: 'post' })
  return { filters, reset: () => Object.assign(filters, defaults) }
}
