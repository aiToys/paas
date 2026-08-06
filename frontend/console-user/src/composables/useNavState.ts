// 侧栏资源组折叠态记忆：localStorage 持久化，跨组件共享（模块级单例）。
// 默认全部折叠（key 不存在视为 false）；用户展开过的组下次保持。
import { ref } from 'vue'

export type NavGroup = 'resources' | 'workloads' | 'platform'

const STORAGE_PREFIX = 'paas:nav-open:'

// 模块级单例状态：首次 import 时从 localStorage 读一次，之后跨组件共享。
const openSet = ref<Set<NavGroup>>(new Set(readAll()))

function readAll(): NavGroup[] {
  // localStorage 在 SSR/异常环境可能不可用，容错。
  try {
    const groups: NavGroup[] = ['resources', 'workloads', 'platform']
    return groups.filter((g) => localStorage.getItem(STORAGE_PREFIX + g) === '1')
  } catch {
    return []
  }
}

function persist(g: NavGroup) {
  try {
    localStorage.setItem(STORAGE_PREFIX + g, openSet.value.has(g) ? '1' : '0')
  } catch {
    /* 忽略写入失败（隐私模式等） */
  }
}

export function useNavState() {
  function isOpen(g: NavGroup): boolean {
    return openSet.value.has(g)
  }
  function toggle(g: NavGroup) {
    const next = new Set(openSet.value)
    if (next.has(g)) next.delete(g)
    else next.add(g)
    openSet.value = next
    persist(g)
  }
  return { isOpen, toggle }
}
