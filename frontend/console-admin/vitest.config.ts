import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import path from 'node:path'

export default defineConfig({
  plugins: [
    vue(),
    // 与 vite.config.ts 同款编译期解析：SFC 模板里的 EP 组件在测试中也走按需 import，
    // 贴近生产语义（全局注册会掩盖「动态 is 字符串解析不到」这类只在生产暴露的缺陷）。
    // importStyle: false —— jsdom 不消费样式，且 resolver 注入的 css import 无法被 vitest 加载
    Components({ resolvers: [ElementPlusResolver({ importStyle: false })] })
  ],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./test/setup.ts'],
    // 覆盖 vitest 默认 exclude（默认还含 cypress/.{idea,git}/** 等）
    // 关键：显式排除 test/smoke/**，避免 pnpm test 误抓 smoke 用例
    // （smoke 用例依赖 dev server，应在 pnpm smoke 中通过 playwright 跑）
    exclude: ['**/node_modules/**', '**/dist/**', 'test/smoke/**'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov', 'html'],
      reportsDirectory: './coverage'
    }
  },
  resolve: {
    alias: [
      { find: '@', replacement: path.resolve(__dirname, './src') },
      // EP 样式 side-effect import（组件显式引入）在测试里替换为空模块：
      // jsdom 不消费样式，且 node ESM 加载不了 node_modules 内的 .css
      {
        find: /^element-plus\/es\/components\/.+\/style\/css$/,
        replacement: path.resolve(__dirname, './test/empty-style.ts')
      }
    ]
  }
})
