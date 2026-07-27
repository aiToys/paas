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
  {
    path: '/environments',
    name: 'environments',
    component: () => import('@/views/Environments.vue'),
    meta: { title: '环境' },
  },

  // —— 资源中心：数据服务（可绑定 Add-on） ——
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
    path: '/resources/db',
    name: 'res-db',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '数据库 RDS', features: ['PostgreSQL / MySQL 实例', '自动备份与回溯', '读写分离', '慢查询分析'] },
    meta: { title: '数据库' },
  },
  {
    path: '/resources/cache',
    name: 'res-cache',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '缓存 Redis', features: ['实例与集群', '持久化与容灾', '大 Key 诊断', '配额与限流'] },
    meta: { title: '缓存' },
  },
  {
    path: '/resources/mq',
    name: 'res-mq',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '消息队列', features: ['Topic 与分区管理', '消费组与重平衡', '消息回溯与追踪', '配额与限流'] },
    meta: { title: '消息队列' },
  },
  {
    path: '/resources/storage',
    name: 'res-storage',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '对象存储', features: ['Bucket 管理', 'CDN 加速', '生命周期策略', '访问授权'] },
    meta: { title: '对象存储' },
  },
  {
    path: '/resources/vector',
    name: 'res-vector',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '向量数据库', features: ['索引与检索', 'Embedding 入库', '相似度召回', '分区与副本'] },
    meta: { title: '向量数据库' },
  },
  {
    path: '/resources/search',
    name: 'res-search',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '搜索引擎', features: ['索引与分词', '聚合分析', '全文检索', '数据同步'] },
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
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '服务治理', features: ['注册发现', '配置中心', 'API 网关', '熔断降级'] },
    meta: { title: '服务治理' },
  },
  {
    path: '/platform/observability',
    name: 'plat-observability',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '可观测', features: ['指标监控', '日志聚合', '链路追踪', '告警通知'] },
    meta: { title: '可观测' },
  },
  {
    path: '/platform/security',
    name: 'plat-security',
    component: () => import('@/views/ComingSoon.vue'),
    props: { product: '安全', features: ['密钥与证书', 'IAM 访问控制', '网络与防火墙', '审计日志'] },
    meta: { title: '安全' },
  },

  // —— DevOps ——
  {
    path: '/devops',
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
