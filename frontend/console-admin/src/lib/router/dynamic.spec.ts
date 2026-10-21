import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import { registerDynamicRoutes } from './dynamic'
import type { MenuDTO } from './types-menu'

// 兼容跳转合并规则回归（spec 2026-10-19 §5.1 / 复核 F02）：
// 迁移目标 tab 优先；原 query 其余键与 hash 保留。
const menu = (path: string, redirect: string): MenuDTO =>
  ({ path, name: path, redirect, meta: { title: path } }) as MenuDTO

const buildRouter = (menus: MenuDTO[]): Router => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div/>' } }]
  })
  registerDynamicRoutes(
    router,
    menus,
    { captureMessage: vi.fn(), captureException: vi.fn() } as never,
    {} as never
  )
  return router
}

describe('registerDynamicRoutes 兼容跳转', () => {
  beforeEach(() => {
    vi.resetModules()
  })

  it('旧链接的 tab 被迁移目标覆盖（buildruns?tab=releases 落构建 tab，不落发布 tab）', async () => {
    const router = buildRouter([menu('/resources/buildruns', '/resources/pipelineruns?tab=builds')])
    await router.push('/resources/buildruns?tab=releases')
    expect(router.currentRoute.value.path).toBe('/resources/pipelineruns')
    expect(router.currentRoute.value.query.tab).toBe('builds')
  })

  it('原 query 的非 tab 键保留（tenantId 过滤不丢）', async () => {
    const router = buildRouter([menu('/system/role', '/system/user?tab=roles')])
    await router.push('/system/role?tenantId=t-acme&keyword=x')
    expect(router.currentRoute.value.query.tab).toBe('roles')
    expect(router.currentRoute.value.query.tenantId).toBe('t-acme')
    expect(router.currentRoute.value.query.keyword).toBe('x')
  })

  it('hash 保留（分享链接的锚点不失效）', async () => {
    const router = buildRouter([menu('/resources/buildruns', '/resources/pipelineruns?tab=builds')])
    await router.push('/resources/buildruns?tenantId=t-acme#row-3')
    expect(router.currentRoute.value.hash).toBe('#row-3')
  })

  it('无 query 的裸旧链接落到目标默认 tab', async () => {
    const router = buildRouter([menu('/resources/engines', '/resources/dataservices?tab=engines')])
    await router.push('/resources/engines')
    expect(router.currentRoute.value.query.tab).toBe('engines')
  })
})
