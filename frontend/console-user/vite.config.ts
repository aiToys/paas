import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 用户控制台 Vite 配置；@ 别名指向 src。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5174,
    proxy: {
      // 开发期代理到 Platform Core Gateway（Plan 4 落地后）
      '/v1': { target: 'http://localhost:8081', changeOrigin: true },
      '/api': { target: 'http://localhost:8081', changeOrigin: true },
    },
  },
})
