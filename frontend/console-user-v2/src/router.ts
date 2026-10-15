// console-user-v2 路由（新 IA 第一期）：登录无壳 / 首页=产品列表 / 产品=三面。
// 真源设计：docs/superpowers/specs/2026-09-07-console-user-ia-destination.md §9
// 「更多」低频能力不在此重复实现——跳老版 /console/ 对应页（并行期互补，非迁移阻断）。
import { createRouter, createWebHistory } from 'vue-router'
import { fetchAuth } from '@/api'

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
      meta: { title: '首页' },
    },
    {
      path: '/products/:id',
      name: 'product',
      component: () => import('./views/Product.vue'),
      meta: { title: '产品' },
    },
    {
      path: '/runs/:runId',
      name: 'run-detail',
      component: () => import('./views/RunDetail.vue'),
      meta: { title: '运行详情' },
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

// 会话守卫：未登录（无 paas_access cookie 会话）跳登录。老版同款约定——
// 以 /api/auth/users/me 探测会话有效性（401 = 未登录；fetchAuth 自动 refresh 后仍 401 才到这里），
// 凭证 cookie 同域对 /next/ 同样生效。
router.beforeEach(async (to) => {
  if (to.meta.public) return true
  const resp = await fetchAuth('/api/auth/users/me')
  if (resp.status === 401) {
    return { name: 'login', query: to.fullPath !== '/' ? { redirect: to.fullPath } : {} }
  }
  return true
})

export default router
