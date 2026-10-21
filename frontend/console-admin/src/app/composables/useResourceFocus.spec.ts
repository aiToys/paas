import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { defineComponent, ref } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory, RouterView } from 'vue-router'
import { useScopeStore } from '@/app/stores/scope'
import { useResourceFocus } from './useResourceFocus'
import { useScopeChange } from './useScopeChange'

let wrapper: VueWrapper | undefined
beforeEach(() => sessionStorage.clear())
afterEach(() => wrapper?.unmount())
async function setup(initial = '/resources/workloads?tenantId=t-globex&id=wl-1&envId=prod') {
  const pinia = createPinia()
  const scope = useScopeStore(pinia)
  scope.setTenant('t-acme')
  const visible = ref(false)
  const id = ref('')
  const page = defineComponent({
    setup() {
      useResourceFocus({ path: '/resources/workloads', visible, id })
      useScopeChange(
        () => {},
        () => {
          visible.value = false
          id.value = ''
        },
      )
      return () => null
    },
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/resources/workloads', component: page }],
  })
  // 与真实守卫一样，在允许导航后同步浏览租户。
  router.beforeEach((to) => {
    if (typeof to.query.tenantId === 'string') scope.setTenant(to.query.tenantId)
  })
  await router.push(initial)
  wrapper = mount(RouterView, { global: { plugins: [pinia, router] } })
  await flushPromises()
  return { router, scope, visible, id }
}

describe('实体URL定位', () => {
  it('冷启动深链自动打开详情，手动关闭清URL，保留浏览租户', async () => {
    const { router, scope, visible, id } = await setup()
    expect(scope.tenantId).toBe('t-globex')
    expect(visible.value).toBe(true)
    expect(id.value).toBe('wl-1')
    visible.value = false
    await flushPromises()
    expect(router.currentRoute.value.query.id).toBeUndefined()
    expect(router.currentRoute.value.query.tenantId).toBe('t-globex')
  })
  it('切到另一租户的深链后仍打开新实体，返回恢复原实体', async () => {
    const { router, scope, visible, id } = await setup()
    await router.push('/resources/workloads?tenantId=t-acme&id=wl-2')
    await flushPromises()
    expect(scope.tenantId).toBe('t-acme')
    expect(visible.value).toBe(true)
    expect(id.value).toBe('wl-2')
    router.back()
    await flushPromises()
    expect(id.value).toBe('wl-1')
    expect(visible.value).toBe(true)
  })
  it('手工切租户清详情，不能把原实体带到新范围', async () => {
    const { router, scope, visible, id } = await setup()
    scope.setTenant('t-acme')
    await router.replace({ query: { tenantId: 't-acme' } })
    await flushPromises()
    expect(visible.value).toBe(false)
    expect(id.value).toBe('')
    expect(router.currentRoute.value.query.id).toBeUndefined()
  })
})
