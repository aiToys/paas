import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 用户控制台 Vite 配置；@ 别名指向 src。
// base='/console/'：生产部署到 paas.k8s.dd/console/ 子路径（core 单镜像同域反代，无 CORS）。
export default defineConfig({
  base: '/console/',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5174,
    proxy: {
      // 开发期代理到 Platform Core Gateway（:8080）
      '/v1': { target: 'http://localhost:8080', changeOrigin: true },
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
