import { describe, it, expect, beforeAll, afterAll } from 'vitest'
// 验证菜单 mock 结构契约（ALL_MENUS 多级嵌套）经 MSW 真实请求获取，
// 锁三件事：扁平化收集完整 / 父子链正确 / 图标 PascalCase 且全局唯一。
// 注：旧版断言（systemAccess 三分组 + /api/system/menu）随菜单 IA 重构失效，
// 当前 handler = /api/system/menus，节点主键 = name 字段。
import { api, http } from '@/lib/http/client'
import httpAdapter from 'axios/unsafe/adapters/http.js'

// jsdom 下 axios 默认走 XHR adapter，MSW node 无法拦截；
// 强制使用 Node http adapter，使测试中的真实 HTTP 请求能被 MSW server 处理。
// handler 按 token 含 _admin_ 判定 admin 下发全量菜单，测试注入管理员 token。
beforeAll(() => {
  http.defaults.adapter = httpAdapter as never
  http.defaults.headers.common['Authorization'] = 'Bearer a_admin_test'
})
afterAll(() => {
  delete http.defaults.headers.common['Authorization']
})

interface MenuTreeNode {
  name: string
  path: string
  component?: string
  icon?: string
  children?: MenuTreeNode[]
}

// 与前端菜单扁平化同构：以 name 作为节点主键维护父子链
function flatten(list: MenuTreeNode[]): Array<MenuTreeNode & { parentId: string | null }> {
  const records: Array<MenuTreeNode & { parentId: string | null }> = []
  const walk = (nodes: MenuTreeNode[], parentId: string | null) => {
    nodes.forEach((node) => {
      records.push({ ...node, parentId })
      if (node.children?.length) walk(node.children, node.name)
    })
  }
  walk(list, null)
  return records
}

describe('菜单结构（多级嵌套）', () => {
  it('所有叶子节点（component 非空）均被扁平化收集', async () => {
    const tree = await api.get<MenuTreeNode[]>('/api/system/menus')
    const records = flatten(tree)
    const leaves = records.filter((r) => r.component)
    expect(leaves.length).toBeGreaterThanOrEqual(20)
  })

  it('关键页面挂在正确的父级（身份/CI-CD/Agent 运行）', async () => {
    const tree = await api.get<MenuTreeNode[]>('/api/system/menus')
    const records = flatten(tree)
    const byName = Object.fromEntries(records.map((r) => [r.name, r]))
    expect(byName['systemUser']?.parentId).toBe('system')
    expect(byName['resCicd']?.parentId).toBe('resources')
    expect(byName['resAgentRuns']?.parentId).toBe('resCicd')
    expect(byName['home']?.parentId).toBeNull()
  })

  it('图标全部 PascalCase（无小写不渲染问题）且全局唯一', async () => {
    const tree = await api.get<MenuTreeNode[]>('/api/system/menus')
    const icons: string[] = []
    const walk = (nodes: MenuTreeNode[]) => {
      for (const n of nodes) {
        if (n.icon) icons.push(n.icon)
        if (n.children) walk(n.children)
      }
    }
    walk(tree)
    for (const ic of icons) {
      expect(ic[0] === ic[0].toUpperCase()).toBe(true)
    }
    expect(new Set(icons).size).toBe(icons.length)
  })
})
