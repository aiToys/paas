import { afterEach, describe, expect, it } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import TagsView from '@/layout/components/TagsView/Index.vue'
import { useTagsViewStore } from '@/app/stores/tagsView'

let wrapper: VueWrapper | undefined
afterEach(() => wrapper?.unmount())

async function setup() {
  const pinia = createPinia()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/model', '/provider'].map((path) => ({
      path,
      component: { template: '<div />' },
      meta: { title: path === '/' ? '首页' : path === '/model' ? '模型管理' : '供应商管理' }
    }))
  })
  await router.push('/')
  wrapper = mount(TagsView, { global: { plugins: [pinia, router] } })
  await router.push('/model')
  await router.push('/provider')
  await flushPromises()
  return { router, tags: useTagsViewStore(pinia) }
}

describe('TagsView关闭入口', () => {
  it('点击标签选中后仍渲染真实关闭图标，关闭当前标签后返回相邻页', async () => {
    const { router, tags } = await setup()
    const model = wrapper!.get('a[href="/model"]')
    await model.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/model')
    const close = model.get('.el-icon-close')
    // 不注册全局图标，确保按需加载环境也能渲染SVG，避免未解析的<close>空元素。
    expect(close.element.tagName.toLowerCase()).toBe('svg')
    expect(close.find('path').exists()).toBe(true)
    await close.trigger('click')
    await flushPromises()
    expect(tags.tagsViewList.some((tag) => tag.path === '/model')).toBe(false)
    expect(router.currentRoute.value.path).toBe('/')
    expect(wrapper!.get('a[href="/provider"] .el-icon-close').element.tagName.toLowerCase()).toBe('svg')
  })

  it('关闭未选中标签不切换页面，固定首页不提供关闭入口', async () => {
    const { router, tags } = await setup()
    expect(wrapper!.get('a[href="/"]').find('.el-icon-close').exists()).toBe(false)
    await wrapper!.get('a[href="/model"] .el-icon-close').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/provider')
    expect(tags.tagsViewList.some((tag) => tag.path === '/model')).toBe(false)
    expect(wrapper!.get('a[href="/provider"] .el-icon-close').element.tagName.toLowerCase()).toBe('svg')
  })
})
