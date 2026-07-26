import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

// 统一控制台路由 —— 应用为主线，资源中心为横向视图。
const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/applications' },

  // —— 主线：应用 ——
  {
    path: '/applications',
    name: 'applications',
    component: () => import('@/views/Applications.vue'),
    meta: { title: '应用' },
  },
  {
    path: '/applications/:id',
    name: 'application-detail',
    component: () => import('@/views/ApplicationDetail.vue'),
    meta: { title: '应用详情' },
  },

  // —— 资源中心 ——
  {
    path: '/resources/models',
    name: 'res-models',
    component: () => import('@/views/Marketplace.vue'),
    meta: { title: '模型推理' },
  },
  {
    path: '/resources/deployments',
    name: 'res-deployments',
    component: () => import('@/views/Deployments.vue'),
    meta: { title: '模型部署' },
  },
  {
    path: '/resources/mq',
    name: 'res-mq',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '消息队列', features: ['Topic 与分区管理', '消费组与重平衡', '消息回溯与追踪', '配额与限流'] },
    meta: { title: '消息队列' },
  },
  {
    path: '/resources/dal',
    name: 'res-dal',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '数据访问层', features: ['多数据源接入', 'SQL 工作台', '慢查询分析', '读写分离'] },
    meta: { title: '数据访问层' },
  },
  {
    path: '/resources/gov',
    name: 'res-gov',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '服务治理', features: ['注册发现', '配置中心', '流量治理', '链路追踪'] },
    meta: { title: '服务治理' },
  },

  // —— 工具与设置 ——
  {
    path: '/coming-soon',
    name: 'devops',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: 'DevOps 应用中心', features: ['CI/CD 流水线', '发布编排', '灰度与回滚', '制品与镜像管理'] },
    meta: { title: 'DevOps' },
  },
  {
    path: '/playground',
    name: 'playground',
    component: () => import('@/views/Playground.vue'),
    meta: { title: 'Playground' },
  },
  {
    path: '/settings/api-keys',
    name: 'api-keys',
    component: () => import('@/views/ApiKeys.vue'),
    meta: { title: 'API 密钥' },
  },
  {
    path: '/settings/usage',
    name: 'usage',
    component: () => import('@/views/Usage.vue'),
    meta: { title: '用量' },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
