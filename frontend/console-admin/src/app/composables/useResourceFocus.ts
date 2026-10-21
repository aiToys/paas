import { watch, type Ref } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { useScopeStore } from '@/app/stores/scope'

// 详情由URL驱动：刷新/返回/分享可恢复；手动关抽屉或切租户时清除旧定位。
export function useResourceFocus(options: {
  path: string
  tab?: string
  queryKey?: string
  visible: Ref<boolean>
  id: Ref<string>
}) {
  const route = useRoute()
  const router = useRouter()
  const scope = useScopeStore()
  const key = options.queryKey ?? 'id'
  const active = () =>
    route.path === options.path &&
    (!options.tab || !route.query.tab || route.query.tab === options.tab)

  watch(
    () => [route.path, route.query.tab, route.query[key], route.query.tenantId],
    () => {
      if (!active()) return
      const value = route.query[key]
      options.id.value = typeof value === 'string' ? value : ''
      options.visible.value = !!options.id.value
    },
    { immediate: true, flush: 'post' },
  )

  watch(
    options.visible,
    (visible) => {
      // 切范围 reset 会同时清 id；此时不能用旧路由发 replace 抢占新深链导航。
      if (!visible && options.id.value && active() && route.query[key]) {
        const query: LocationQueryRaw = { ...route.query, tenantId: scope.tenantId || undefined }
        delete query[key]
        void router.replace({ query, hash: route.hash })
      }
    },
    { flush: 'post' },
  )

  return (id: string, extra: LocationQueryRaw = {}) =>
    router.push({
      path: options.path,
      query: {
        ...route.query,
        ...(options.tab ? { tab: options.tab } : {}),
        tenantId: scope.tenantId || undefined,
        ...extra,
        [key]: id,
      },
      hash: route.hash,
    })
}
