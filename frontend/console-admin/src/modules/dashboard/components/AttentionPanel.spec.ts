import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { useScopeStore } from '@/app/stores/scope'
import AttentionPanel from './AttentionPanel.vue'
import { fetchAttention } from '../attention'

vi.mock('../attention', async (original) => ({
  ...(await original<typeof import('../attention')>()),
  fetchAttention: vi.fn(),
}))
type Result = Awaited<ReturnType<typeof fetchAttention>>
const result = (name: string): Result => ({
  runs: { records: [], error: '' },
  alerts: { records: [], error: '' },
  workloads: {
    records: [
      {
        id: name,
        name,
        tenantId: 't-globex',
        envId: 'prod',
        appId: 'app-g',
        type: 'service',
        status: 'failed',
      },
    ],
    error: '',
  },
})
let wrapper: VueWrapper | undefined
beforeEach(() => {
  sessionStorage.clear()
  vi.resetAllMocks()
})
afterEach(() => wrapper?.unmount())
async function setup() {
  const pinia = createPinia()
  const scope = useScopeStore(pinia)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/resources/workloads', component: { template: '<div />' } },
    ],
  })
  await router.push('/')
  wrapper = mount(AttentionPanel, { global: { plugins: [pinia, router] } })
  return { scope }
}

describe('关注事项组件', () => {
  it('错误不会被展示成无异常；成功分区提供携带实体和租户的可访问链接', async () => {
    const data = result('wl-51')
    data.runs.error = '权限不足'
    vi.mocked(fetchAttention).mockResolvedValue(data)
    await setup()
    await flushPromises()
    expect(wrapper!.text()).toContain('失败运行加载失败：权限不足')
    expect(wrapper!.text()).not.toContain('暂无需要关注的事项')
    const href = wrapper!.get('a.issue-row').attributes('href')
    expect(href).toContain('id=wl-51')
    expect(href).toContain('tenantId=t-globex')
    expect(href).toContain('envId=prod')
  })

  it('切范围后即使旧请求后返回，也不能覆盖新范围的异常', async () => {
    let finishOld!: (value: Result) => void
    vi.mocked(fetchAttention).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishOld = resolve
        }),
    )
    vi.mocked(fetchAttention).mockResolvedValue(result('new-scope'))
    const { scope } = await setup()
    scope.setTenant('t-globex')
    await flushPromises()
    expect(wrapper!.text()).toContain('new-scope')
    finishOld(result('old-scope'))
    await flushPromises()
    expect(wrapper!.text()).not.toContain('old-scope')
    expect(wrapper!.text()).toContain('new-scope')
    expect(fetchAttention).toHaveBeenLastCalledWith('t-globex')
  })
})
