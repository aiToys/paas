import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 前端构建为静态文件，nginx serve。API 走相对路径 /api/*，nginx 反代到 bff。
// 同源（前端 + bff 经 nginx），无 CORS。
export default defineConfig({
  plugins: [vue()],
  server: { port: 5180 },
  build: { outDir: 'dist', target: 'es2020' },
})
