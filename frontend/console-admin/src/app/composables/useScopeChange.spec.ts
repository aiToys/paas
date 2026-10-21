import { describe, it, expect, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useScopeChange } from './useScopeChange'
import { useScopeStore } from '@/app/stores/scope'

// 浏览范围切换联动回归（复核 F01）：切租户时 reset（关抽屉/清详情）先于 refresh
// （重拉列表）执行——保证不出现「新列表 + 旧详情」中间态。
describe('useScopeChange', () => {
  it('切租户触发 reset→refresh 且顺序正确；初始不触发', async () => {
    setActivePinia(createPinia())
    const scope = useScopeStore()
    const reset = vi.fn()
    const refresh = vi.fn()
    const order: string[] = []
    reset.mockImplementation(() => order.push('reset'))
    refresh.mockImplementation(() => order.push('refresh'))

    useScopeChange(refresh, reset)
    await Promise.resolve()
    expect(reset).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()

    scope.setTenant('t-acme')
    await Promise.resolve()
    expect(order).toEqual(['reset', 'refresh'])
  })

  it('无 reset 回调时仅刷新（纯列表页形态）', async () => {
    setActivePinia(createPinia())
    const scope = useScopeStore()
    const refresh = vi.fn()
    useScopeChange(refresh)
    scope.setTenant('t-globex')
    await Promise.resolve()
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('切回全部租户（空串）同样触发', async () => {
    setActivePinia(createPinia())
    const scope = useScopeStore()
    scope.setTenant('t-acme')
    const refresh = vi.fn()
    useScopeChange(refresh)
    scope.setTenant('')
    await Promise.resolve()
    expect(refresh).toHaveBeenCalledTimes(1)
  })
})
