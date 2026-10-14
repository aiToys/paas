// FormDrawer 动态组件解析回归锁（2026-10-13 实测创建用户页面崩）：
// componentMap 曾用字符串 'el-select' 等，Element Plus 按需引入（unplugin 编译期
// 解析）下运行时字符串解析不到真实组件——el-option 在无 el-select 祖先时
// throwError("usage: <el-select><el-option /></el-select/>")，页面整个崩进 ErrorBoundary。
// 修复后 componentMap 是显式 import 的组件对象，select 渲染出真实 el-select + el-option。
import { describe, expect, it, beforeAll, afterAll } from 'vitest'
import { mount, config } from '@vue/test-utils'
import { nextTick } from 'vue'
import { i18n } from '@/lib/i18n'
import FormDrawer from './index.vue'
import type { FormField } from './types'

// 摘除全局 ElementPlus 注册（test/setup.ts 为存量测试注入）：本用例专验「无全局注册时
// 动态组件仍可解析」的生产语义，模板静态组件由 vitest.config 的编译期 resolver 接管
const originalPlugins = config.global.plugins
beforeAll(() => {
  config.global.plugins = [i18n]
})
afterAll(() => {
  config.global.plugins = originalPlugins
})

const fields: FormField[] = [
  { prop: 'status', label: '状态', type: 'select', options: [{ label: '启用', value: 'active' }] },
  { prop: 'name', label: '名称', type: 'input' },
]

describe('FormDrawer 动态组件解析', () => {
  it('select 字段渲染真实 el-select 且 el-option 不脱离上下文', async () => {
    const wrapper = mount(FormDrawer, {
      props: {
        modelValue: true,
        title: '回归',
        formData: { status: 'active', name: '' },
        fields,
      },
    })
    await nextTick()
    expect(wrapper.findComponent({ name: 'ElSelect' }).exists()).toBe(true)
    // el-option 挂载即校验 select 注入（EP2.11 缺祖先会 throwError，mount 直接抛错）
    expect(wrapper.findAllComponents({ name: 'ElOption' }).length).toBe(1)
    // input 字段同样解析为真实组件而非原生未知元素
    expect(wrapper.findComponent({ name: 'ElInput' }).exists()).toBe(true)
  })
})
