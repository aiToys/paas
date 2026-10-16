<script setup lang="ts">
import { computed, ref } from 'vue'
import { fetchList, apiError } from '@/api'
import { listNotifications, type Notification } from '@/api/change'
import { usePolling } from '@/composables/usePolling'
import { notificationLink } from '@/navigation'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import Icon from '@/components/Icon.vue'
const products = ref<{ id: string; name: string; desc?: string }[]>([])
const notifications = ref<Notification[]>([])
const productsLoading = ref(true)
const notificationsLoading = ref(true)
const productError = ref('')
const notificationError = ref('')
const filter = ref('action')
const attention = computed(() => notifications.value.filter((n) => filter.value === 'all' || n.severity !== 'info'))
async function loadProducts() {
  try { products.value = await fetchList<(typeof products.value)[number]>('/api/applications'); productError.value = '' }
  catch (e) { productError.value = apiError(e) }
  finally { productsLoading.value = false }
}
async function loadNotifications() {
  try { notifications.value = await listNotifications(); notificationError.value = '' }
  catch (e) { notificationError.value = apiError(e) }
  finally { notificationsLoading.value = false }
}
async function load() { await Promise.all([loadProducts(), loadNotifications()]) }

usePolling(load, 30000)
const entries = [
  { name: '部署与交付', description: '查看跨产品运行与待审批事项', icon: 'pipeline', to: '/delivery' },
  { name: '使用模型', description: '比较能力与价格，直接进入试玩', icon: 'market', to: '/models' },
  { name: '构建 AI 应用', description: '智能体、工作流与知识库', icon: 'bot', to: '/ai' },
  { name: '排查问题', description: '从告警进入日志与调用链', icon: 'activity', to: '/observability' },
]
</script>
<template>
  <div class="overview">
    <PageHeader title="工作空间总览" description="关注需要处理的事项，快速进入你的产品与常用能力。" eyebrow="WORKSPACE"><RouterLink to="/products?new=1" class="act primary">新建产品</RouterLink></PageHeader>
    <div class="overview-grid">
      <section class="card attention-card" aria-labelledby="attention-title">
        <div class="card-head"><h2 id="attention-title" class="card-title">需要关注</h2><span class="card-sub">组织内跨产品事项</span><RouterLink to="/delivery" class="text-button all-link">交付中心 →</RouterLink></div>
        <div class="attention-filters" aria-label="事项范围"><button :class="{ selected: filter === 'action' }" @click="filter = 'action'">待处理</button><button :class="{ selected: filter === 'all' }" @click="filter = 'all'">全部动态</button></div>
        <div v-if="notificationsLoading" v-loading="true" class="loading-block"></div>
        <StatePanel v-else-if="notificationError" title="待办暂时无法加载" :description="notificationError" error compact><button class="act" @click="load">重试</button></StatePanel>
        <StatePanel v-else-if="!attention.length" title="当前没有待处理事项" description="失败、等待审批和需要决策的交付会集中出现在这里。" compact />
        <div v-else class="attention-list">
          <RouterLink v-for="n in attention.slice(0, 8)" :key="n.id" :to="notificationLink(n)" class="attention-item">
            <span class="event-mark" :class="n.severity"><Icon :name="n.severity === 'error' ? 'activity' : 'clock'" :size="17" /></span>
            <div><strong>{{ n.title }}</strong><small>{{ products.find((p) => p.id === n.appId)?.name ?? n.appId }} · {{ new Date(n.at).toLocaleString('zh-CN') }}</small></div>
            <span class="attention-arrow" aria-hidden="true">→</span>
          </RouterLink>
          <RouterLink v-if="attention.length > 8" to="/delivery" class="text-button">查看全部 {{ attention.length }} 项 →</RouterLink>
        </div>
      </section>
      <section class="card quick-card" aria-labelledby="quick-title">
<div class="card-head"><h2 id="quick-title" class="card-title">快速开始</h2></div>
        <RouterLink v-for="e in entries" :key="e.to" :to="e.to" class="quick-entry"><Icon :name="e.icon" :size="20" /><div><strong>{{ e.name }}</strong><span>{{ e.description }}</span></div><span aria-hidden="true">↗</span></RouterLink>
      </section>
    </div>
    <section class="card" aria-labelledby="products-title">
<div class="card-head"><h2 id="products-title" class="card-title">我的产品</h2><span class="card-sub">{{ products.length }} 个</span><RouterLink to="/products" class="text-button all-link">全部产品 →</RouterLink></div>
      <StatePanel v-if="productError" title="产品加载失败" :description="productError" error compact><button class="act" @click="load">重试</button></StatePanel>
      <div v-else-if="productsLoading" class="loading-block" v-loading="true"></div>
      <StatePanel v-else-if="!products.length" title="还没有产品" description="先创建产品，再添加服务；如果只需调用模型，可直接从模型目录开始。" compact><RouterLink to="/products?new=1" class="act">新建产品</RouterLink><RouterLink to="/models" class="act">浏览模型</RouterLink></StatePanel>
      <div v-else class="overview-products"><RouterLink v-for="p in products.slice(0, 6)" :key="p.id" :to="'/products/' + p.id" class="overview-product"><span class="product-initial">{{ p.name.slice(0, 1).toUpperCase() }}</span><div><strong>{{ p.name }}</strong><small>{{ p.desc || '查看服务、交付与资源' }}</small></div><span aria-hidden="true">→</span></RouterLink></div>
    </section>
  </div>
</template>
<style scoped>
.overview { display: flex; flex-direction: column; gap: 24px; }
.overview > .page-heading { margin-bottom: 0; }
.overview-grid { display: grid; grid-template-columns: minmax(0, 1.8fr) minmax(280px, 1fr); gap: 24px; }
.card-title { margin: 0; }
.all-link { margin-left: auto; }
.attention-filters { display: flex; gap: 6px; margin-bottom: 12px; }
.attention-filters button { background: transparent; color: var(--text-faint); border: 0; padding: 5px 10px; border-radius: 4px; font-size: 12px; }
.attention-filters .selected { background: var(--surface-2); color: var(--text); font-weight: 600; }
.attention-item { display: flex; align-items: center; gap: 12px; padding: 13px 0; border-bottom: 1px solid var(--border); text-decoration: none; }
.attention-item:last-child { border-bottom: 0; }
.attention-item strong { display: block; font-size: 13px; font-weight: 500; }
.attention-item small { display: block; color: var(--text-faint); font-size: 12px; margin-top: 4px; }
.event-mark { display: grid; place-items: center; width: 32px; height: 32px; flex-shrink: 0; border-radius: 6px; color: var(--brand); background: var(--brand-soft); }
.event-mark.error { background: var(--danger-soft); color: var(--danger); }
.event-mark.warning { background: var(--warning-soft); color: var(--warning); }
.attention-arrow { margin-left: auto; color: var(--text-faint); }
.quick-entry { display: flex; align-items: center; gap: 13px; padding: 16px 0; text-decoration: none; border-bottom: 1px solid var(--border); }
.quick-entry:last-child { border-bottom: 0; }
.quick-entry > svg { color: var(--brand); flex-shrink: 0; }
.quick-entry > span { margin-left: auto; color: var(--text-faint); }
.quick-entry strong { display: block; font-size: 13px; font-weight: 600; }
.quick-entry div span { display: block; font-size: 12px; color: var(--text-faint); margin-top: 5px; }
.overview-products { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0 24px; }
.overview-product { display: flex; align-items: center; gap: 12px; padding: 18px 0; text-decoration: none; min-width: 0; }
.product-initial { width: 34px; height: 34px; display: grid; place-items: center; background: var(--surface-2); color: var(--text-dim); border: 1px solid var(--border); border-radius: 7px; flex-shrink: 0; font-weight: 600; }
.overview-product div { min-width: 0; }
.overview-product strong { display: block; font-size: 13px; }
.overview-product small { display: block; color: var(--text-faint); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 3px; }
.overview-product > span:last-child { margin-left: auto; color: var(--text-faint); }
.quick-entry:hover strong, .overview-product:hover strong, .attention-item:hover strong { color: var(--brand); }
.loading-block { height: 180px; }
@media (max-width: 1200px) { .overview-grid { grid-template-columns: minmax(0, 1.5fr) minmax(240px, 1fr); gap: 16px; } .overview-products { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 767px) { .overview-grid, .overview-products { grid-template-columns: 1fr; } .overview { gap: 16px; } }
</style>
