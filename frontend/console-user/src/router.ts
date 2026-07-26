import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

// 用户控制台路由：MaaS 相关核心页面。
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/marketplace' },
  {
    path: '/marketplace',
    name: 'marketplace',
    component: () => import('@/views/Marketplace.vue'),
    meta: { title: '模型市场' },
  },
  {
    path: '/deployments',
    name: 'deployments',
    component: () => import('@/views/Deployments.vue'),
    meta: { title: '我的部署' },
  },
  {
    path: '/playground',
    name: 'playground',
    component: () => import('@/views/Playground.vue'),
    meta: { title: 'Playground' },
  },
  {
    path: '/api-keys',
    name: 'api-keys',
    component: () => import('@/views/ApiKeys.vue'),
    meta: { title: 'API Key' },
  },
  {
    path: '/usage',
    name: 'usage',
    component: () => import('@/views/Usage.vue'),
    meta: { title: '用量与计费' },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
