import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

// 统一控制台路由 —— 三层信息架构：
//   资源中心 = 数据服务（可绑定 Add-on）
//   工作负载 = 应用运行形态（Service/Job/CronJob）
//   平台能力 = 横切基础设施（治理/可观测/安全）
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

  // -- 环境：物理隔离单元（生产/测试） --
  // 管理面：列表（CRUD+总览）+ 详情（单环境深度视图）。切换工作环境走顶栏 scope。
  {
    path: '/environments',
    name: 'environments',
    component: () => import('@/views/Environments.vue'),
    meta: { title: '环境' },
  },
  {
    path: '/environments/:id',
    name: 'environment-detail',
    component: () => import('@/views/EnvironmentDetail.vue'),
    meta: { title: '环境详情' },
  },

  // —— 资源中心：数据服务（可绑定 Add-on） ——
  {
    path: '/resources/models',
    name: 'res-models',
    component: () => import('@/views/Marketplace.vue'),
    meta: { title: '模型推理' },
  },
  {
    path: '/resources/db',
    name: 'res-db',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'db' },
    meta: { title: '数据库' },
  },
  {
    path: '/resources/cache',
    name: 'res-cache',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'cache' },
    meta: { title: '缓存' },
  },
  {
    path: '/resources/mq',
    name: 'res-mq',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'mq' },
    meta: { title: '消息队列' },
  },
  {
    path: '/resources/storage',
    name: 'res-storage',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'storage' },
    meta: { title: '对象存储' },
  },
  {
    path: '/resources/vector',
    name: 'res-vector',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'vector' },
    meta: { title: '向量数据库' },
  },
  {
    path: '/resources/search',
    name: 'res-search',
    component: () => import('@/views/DataServices.vue'),
    props: { kind: 'search' },
    meta: { title: '搜索引擎' },
  },

  // —— 工作负载：应用运行形态 ——
  {
    path: '/workloads/services',
    name: 'wl-services',
    component: () => import('@/views/Workloads.vue'),
    props: { type: 'service' },
    meta: { title: '服务' },
  },
  {
    path: '/workloads/jobs',
    name: 'wl-jobs',
    component: () => import('@/views/Workloads.vue'),
    props: { type: 'job' },
    meta: { title: '任务' },
  },
  {
    path: '/workloads/cronjobs',
    name: 'wl-cronjobs',
    component: () => import('@/views/Workloads.vue'),
    props: { type: 'cronjob' },
    meta: { title: '定时任务' },
  },

  // —— 平台能力：横切基础设施 ——
  {
    path: '/platform/governance',
    name: 'plat-governance',
    component: () => import('@/views/ServiceRegistry.vue'),
    meta: { title: '服务治理' },
  },
  {
    path: '/platform/governance/services/:id',
    name: 'service-detail',
    component: () => import('@/views/ServiceDetail.vue'),
    meta: { title: '服务详情' },
  },
  {
    path: '/platform/config-center',
    name: 'plat-config-center',
    component: () => import('@/views/ConfigCenter.vue'),
    meta: { title: '配置中心' },
  },
  {
    path: '/platform/config-center/:nsId',
    name: 'config-center-detail',
    component: () => import('@/views/ConfigCenter.vue'),
    meta: { title: '命名空间详情' },
  },
  {
    path: '/platform/observability',
    name: 'plat-observability',
    component: () => import('@/views/Observability.vue'),
    meta: { title: '可观测' },
  },
  {
    path: '/platform/security',
    name: 'plat-security',
    component: () => import('@/views/Security.vue'),
    meta: { title: '安全' },
  },

  // —— DevOps ——
  {
    path: '/devops',
    name: 'devops',
    component: () => import('@/views/DevOps.vue'),
    meta: { title: 'DevOps 中心' },
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
    path: '/settings/billing',
    name: 'billing',
    component: () => import('@/views/Billing.vue'),
    meta: { title: '配额与账单' },
  },
]

const router = createRouter({
  // base 跟随 vite base（'/console/'），SPA 前端路由正确解析子路径。
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

export default router
