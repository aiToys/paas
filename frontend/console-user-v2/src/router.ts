import { createRouter, createWebHistory } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const router = createRouter({
  history: createWebHistory('/next/'),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('./views/Login.vue'),
      meta: { title: '登录', public: true },
    },
    {
      path: '/',
      name: 'home',
      component: () => import('./views/Home.vue'),
      meta: { title: '总览' },
    },
    { path: '/governance/services/:id', name: 'service-detail', component: () => import('./views/ServiceDetail.vue'), meta: { title: '服务发现详情' } },
    { path: '/config-center/:nsId?', name: 'config-center', component: () => import('./views/ConfigCenter.vue'), meta: { title: '配置中心' } },
    { path: '/environments/:id', name: 'environment-detail', component: () => import('./views/EnvironmentDetail.vue'), meta: { title: '环境详情' } },
    { path: '/lanes/:id', name: 'lane-detail', component: () => import('./views/LaneDetail.vue'), meta: { title: '泳道详情' } },
    { path: '/ai/runs/:name', name: 'agent-run-detail', component: () => import('./views/AgentRunDetail.vue'), meta: { title: 'AI 执行详情' } },
    { path: '/products/:appId/repositories/:repoId/pulls', component: () => import('./views/Pulls.vue'), meta: { title: '代码评审' } },
    { path: '/pulls/:repoId/:number', component: () => import('./views/PullDetail.vue'), meta: { title: 'PR 详情' } },
    { path: '/releases/:id', component: () => import('./views/ReleaseDetail.vue'), meta: { title: '发布详情' } },
    { path: '/changes/:id', component: () => import('./views/ChangeDetail.vue'), meta: { title: '变更详情' } },
    { path: '/batches/:id', component: () => import('./views/BatchDetail.vue'), meta: { title: '批次详情' } },
    { path: '/products', name: 'products', component: () => import('./views/Products.vue'), meta: { title: '产品' } },
    { path: '/delivery', name: 'delivery', component: () => import('./views/Delivery.vue'), meta: { title: '交付中心' } },
    {
      path: '/products/:id',
      name: 'product',
      component: () => import('./views/Product.vue'),
      meta: { title: '产品' },
    },
    {
      path: '/builds/:id',
      name: 'build-detail',
      component: () => import('./views/BuildDetail.vue'),
      meta: { title: '构建详情' },
    },
    {
      path: '/runs/:runId',
      name: 'run-detail',
      component: () => import('./views/RunDetail.vue'),
      meta: { title: '运行详情' },
    },
    {
      path: '/models',
      name: 'models',
      component: () => import('./views/Models.vue'),
      meta: { title: '模型目录' },
    },
    {
      path: '/dataservices',
      name: 'dataservices',
      component: () => import('./views/DataServices.vue'),
      meta: { title: '数据服务' },
    },
    {
      path: '/dataservices/:id',
      name: 'dataservice-detail',
      component: () => import('./views/DataServiceDetail.vue'),
      meta: { title: '数据服务详情' },
    },
    {
      path: '/workloads',
      name: 'workloads',
      component: () => import('./views/Workloads.vue'),
      meta: { title: '工作负载' },
    },
    { path: '/workloads/:id', name: 'workload-runtime', component: () => import('./views/WorkloadRuntime.vue'), meta: { title: '服务运行' } },
    {
      path: '/environments',
      name: 'environments',
      component: () => import('./views/Environments.vue'),
      meta: { title: '环境' },
    },
    {
      path: '/api-keys',
      name: 'api-keys',
      component: () => import('./views/ApiKeys.vue'),
      meta: { title: 'API 密钥' },
    },
    {
      path: '/observability',
      name: 'observability',
      component: () => import('./views/Observability.vue'),
      meta: { title: '可观测' },
    },
    {
      path: '/ai/:section?',
      name: 'ai-hub',
      component: () => import('./views/AiWorkspace.vue'),
      meta: { title: 'AI 开发' },
    },
    {
      path: '/playground',
      name: 'playground',
      component: () => import('./views/Playground.vue'),
      meta: { title: 'Playground' },
    },
    {
      path: '/governance',
      name: 'governance',
      component: () => import('./views/Governance.vue'),
      meta: { title: '流量治理' },
    },
    {
      path: '/secrets',
      name: 'secrets',
      component: () => import('./views/Secrets.vue'),
      meta: { title: '密钥与凭证' },
    },
    {
      path: '/billing',
      name: 'billing',
      component: () => import('./views/Billing.vue'),
      meta: { title: '用量与配额' },
    },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFound.vue'), meta: { title: '页面不存在' } },
  ],
})

// 登录态缓存由 API 的 session-expired 事件失效；不再每次导航重复请求身份。
router.beforeEach(async (to) => {
  const session = useSessionStore()
  if (!session.loaded) await session.loadProfile()
  if (to.meta.public) return to.path === '/login' && session.profile ? '/' : true
  if (!session.profile) return { name: 'login', query: { redirect: to.fullPath } }
  return true
})
router.afterEach((to) => { document.title = String(to.meta.title || '控制台') + ' · PaaS' })
router.onError((error) => {
  if (!/Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(error.message)) return
  const key = 'paas-v2-chunk-reloaded'
  if (sessionStorage.getItem(key)) return
  sessionStorage.setItem(key, '1')
  window.location.reload()
})
router.afterEach(() => { sessionStorage.removeItem('paas-v2-chunk-reloaded') })
export default router
