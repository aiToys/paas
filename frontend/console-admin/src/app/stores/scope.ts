import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/lib/http/client'

// 浏览范围（spec 2026-10-19 §7.1）：顶栏「全部租户 / 指定租户」的单一真源。
//
// 语义约束：
// - 纯过滤器：只影响跨租户列表请求的 tenantId query，不改变登录身份、
//   资源归属或写入目标（写操作仍走各自动端点）。
// - 平台级供给页（模型/供应商/引擎/平台凭证/广场）不消费本状态，
//   页面以「平台级，不按租户过滤」声明。
// - sessionStorage 持久化：仅标签页会话内有效（刷新/页内跳转保留），
//   不落 localStorage——跨会话记住范围会伪装成权限。
// - 页面职责：watch tenantId 变化重新拉列表、关闭打开的抽屉/清勾选；
//   旧请求丢弃由各页 fetch 前置序号或组件卸载自然覆盖（数据量小，KISS）。
const SCOPE_KEY = 'paas.admin.scope.tenantId'

const readStored = (): string => {
  try {
    return sessionStorage.getItem(SCOPE_KEY) ?? ''
  } catch {
    return ''
  }
}

export const useScopeStore = defineStore('scope', () => {
  // '' = 全部租户
  const tenantId = ref(readStored())
  // 选项缓存（顶栏选择器复用；加载失败由选择器呈现独立错误态）。
  // 直调 GET /api/admin/tenants（与 modules/system/tenant/api.ts 同端点）：
  // app 层 store 不得反向依赖 modules（四层目录边界）。
  const tenants = ref<{ id: string; name: string }[]>([])
  const tenantsError = ref('')
  const tenantsLoaded = ref(false)

  let inFlight: Promise<void> | null = null

  const loadTenants = async (): Promise<void> => {
    if (tenantsLoaded.value) return
    if (inFlight) return inFlight
    inFlight = (async () => {
      try {
        const list = await api.get<{ id: string; name: string }[]>('/api/admin/tenants')
        tenants.value = list ?? []
        tenantsError.value = ''
        tenantsLoaded.value = true
      } catch (e) {
        tenantsError.value = (e as Error).message || '租户列表加载失败'
      } finally {
        inFlight = null
      }
    })()
    return inFlight
  }

  const setTenant = (tid: string) => {
    tenantId.value = tid ?? ''
    try {
      if (tenantId.value) sessionStorage.setItem(SCOPE_KEY, tenantId.value)
      else sessionStorage.removeItem(SCOPE_KEY)
    } catch {
      // 隐私模式等存储不可用：仅内存态，刷新后回全量视图（可接受降级）
    }
  }

  // 当前范围显示名（顶栏/页头用）
  const tenantName = (tid: string): string =>
    tenants.value.find((t) => t.id === tid)?.name ?? tid

  return { tenantId, tenants, tenantsError, tenantsLoaded, loadTenants, setTenant, tenantName }
})

// withScope 把浏览范围合入请求 params（tenantId 为空时不加键，等价全部租户）。
// 供各列表页 fetch 时统一调用，避免每页手写条件。
export function withScope(scopeTenantId: string): Record<string, string> {
  return scopeTenantId ? { tenantId: scopeTenantId } : {}
}
