import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

// useUrlState 双向同步某字符串状态项 ↔ URL query key（深链接核心）。
//
// 读：route.query[key] ?? defaultValue。
// 写：value 变化 → router.replace({ query: { ...route.query, [key]: next } }），
//     用 replace 而非 push 避免历史栈膨胀（每输入一个字符不产生一条历史）。
// 回：route.query[key] 变化 → value（浏览器前进/后退、外部改 URL 时同步回状态）。
//
// 等于默认值时省略 query（保持 URL 干净）：searchQ='' 不写 ?q=。
// 适用于 tab/搜索词/筛选/环境 scope 等字符串型状态。数值/分页用字符串往返转换。
export function useUrlState<T extends string>(key: string, defaultValue: T) {
  const route = useRoute()
  const router = useRouter()

  const read = (): T => {
    const v = route.query[key]
    return (typeof v === 'string' ? (v as T) : defaultValue) ?? defaultValue
  }

  const value = ref<T>(read())

  // value → URL（用户改状态：写 query）
  watch(
    value,
    (v) => {
      const cur = route.query[key]
      const next: string | undefined = v === defaultValue ? undefined : v
      if ((cur ?? '') !== (next ?? '')) {
        router.replace({ query: { ...route.query, [key]: next } })
      }
    },
    { flush: 'post' },
  )

  // URL → value（前进/后退、外部跳转带 query：同步回状态）
  watch(
    () => route.query[key],
    (q) => {
      const v: T = (typeof q === 'string' ? (q as T) : defaultValue) ?? defaultValue
      if (v !== value.value) value.value = v
    },
  )

  return { value }
}
