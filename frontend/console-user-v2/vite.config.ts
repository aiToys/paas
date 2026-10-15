import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'

// console-user-v2：新 IA 试验田（2026-09-07 终点 spec 第一期）。
// 与老 console-user（/console/）并行共存、独立 URL（/next/）——达不到目标可整目录删除，
// 不影响老版。设计真源：docs/superpowers/specs/2026-09-07-console-user-ia-destination.md
export default defineConfig({
  base: '/next/',
  plugins: [
    vue(),
    AutoImport({ resolvers: [ElementPlusResolver()] }),
    Components({ resolvers: [ElementPlusResolver()] }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5176,
    proxy: {
      // 开发期代理到 Platform Core Gateway（:8080）
      '/v1': { target: 'http://localhost:8080', changeOrigin: true },
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  build: {
    chunkSizeWarningLimit: 800,
    rollupOptions: {
      output: {
        manualChunks: {
          vue: ['vue', 'vue-router', 'pinia'],
        },
      },
    },
  },
  esbuild: {
    drop: ['console', 'debugger'],
  },
})
