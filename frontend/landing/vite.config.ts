import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 官网展示页 Vite 配置；静态产物，SEO 友好。
export default defineConfig({
  plugins: [vue()],
  server: { port: 5175 },
})
