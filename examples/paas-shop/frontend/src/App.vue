<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import type { Product } from './types'
import { fmtPrice } from './format'
import { lane, setLane, laneFetch, detectEnv, currentLane } from './lane'
import SearchBar from './components/SearchBar.vue'
import ProductCard from './components/ProductCard.vue'
import DetailDrawer from './components/DetailDrawer.vue'
import ChatWindow from './components/ChatWindow.vue'
import NotificationCenter from './components/NotificationCenter.vue'
import AdminPanel from './components/AdminPanel.vue'

// 环境推断（spec §10 三套域名）：hostname 定环境，红生产/绿测试/蓝泳道（spec §11）
const env = detectEnv(window.location.hostname)
// 商店/运营视图切换（AdminPanel：订单管理 + 经营统计 + 商品上新）。
// 视图进 URL hash（#/admin）：刷新/分享链接不丢视图——此前纯内存 ref，运营视图刷新即回商店页。
const ADMIN_HASH = '#/admin'
const view = ref<'shop' | 'admin'>(location.hash === ADMIN_HASH ? 'admin' : 'shop')
function toggleView() {
  // 赋值 location.hash 天然入历史栈（前进/后退可用）并触发 hashchange → view 同步
  location.hash = view.value === 'shop' ? ADMIN_HASH : '#/'
}
function onHashChange() {
  view.value = location.hash === ADMIN_HASH ? 'admin' : 'shop'
}
addEventListener('hashchange', onHashChange)
onUnmounted(() => removeEventListener('hashchange', onHashChange))

const products = ref<Product[]>([])
const recommends = ref<Product[]>([])
const loading = ref(false)
const error = ref('')

// 搜索状态 + 全量分类（首次全量加载后缓存，搜索过滤后不重算）
const searchQ = ref('')
const searchCategory = ref('')
const allCategories = ref<string[]>([])

// 商品详情抽屉选中项 + 客服弹窗 ref
const selected = ref<Product | null>(null)
const chatRef = ref<InstanceType<typeof ChatWindow> | null>(null)

// 泳道演示：header 输入 lane 名（如 feature-x），所有请求自动染色 x-paas-lane；
// 留空 = 基线。切换后需重载列表才走新泳道（下次请求即生效）。
const laneInput = ref(lane.value)
// 徽标按「当前生效泳道」显示（currentLane：hostname 域名优先于手动输入，与出站染色一致）。
const laneActive = computed(() => currentLane() !== '')
// 最近一次请求的实例指纹（bff/product 响应头）：泳道切换后可见「实际命中实例/版本」变化
const servedBy = ref('')
const servedLane = ref('')
function applyLane() {
  setLane(laneInput.value)
  loadProducts(searchQ.value, searchCategory.value)
  loadRecommend()
}

async function loadProducts(q?: string, category?: string) {
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    if (q) params.set('q', q)
    if (category) params.set('category', category)
    params.set('limit', '50')
    const resp = await laneFetch('/api/products?' + params.toString())
    servedLane.value = resp.headers.get('x-paas-lane') || ''
    servedBy.value = resp.headers.get('x-paas-service') || resp.headers.get('x-paas-bff') || ''
    const data: Product[] = await resp.json()
    // 首次全量加载（无过滤）时派生分类缓存
    if (!q && !category && allCategories.value.length === 0) {
      allCategories.value = [...new Set(data.map(p => p.category))]
    }
    products.value = data
  } catch (e) {
    error.value = '商品加载失败'
  } finally {
    loading.value = false
  }
}

async function loadRecommend() {
  try {
    const resp = await laneFetch('/api/recommend?userId=user-1')
    const data = await resp.json()
    recommends.value = data.products || []
  } catch {}
}

function onSearch(q: string, category: string) {
  searchQ.value = q
  searchCategory.value = category
  loadProducts(q, category)
}

// 抽屉「问客服」：关抽屉 + 打开客服并预填问题（不自动发送）
function onAsk(p: Product) {
  selected.value = null
  chatRef.value?.openWith(`${p.name}怎么样`)
}

onMounted(() => {
  loadProducts()
  loadRecommend()
})
</script>

<template>
  <div class="app">
    <header class="hdr">
      <div class="logo">🛍️ PaasShop</div>
      <span class="env-badge" :class="env.kind">{{ env.kind === 'lane' ? `泳道 ${env.lane}` : env.kind === 'prod' ? '生产' : '测试' }}</span>
      <div class="tag">智能商品服务 · 演示微服务 + AI 客服 + 全链路可观测</div>
      <div class="lane-box" title="泳道演示：输入 lane 名（如 feature-x）使请求带 x-paas-lane 染色，走对应泳道实例；留空 = 基线">
        <span class="lane-label">🚧 泳道</span>
        <input v-model="laneInput" class="lane-input" placeholder="基线（default）" @keyup.enter="applyLane" />
        <span v-if="laneActive" class="lane-badge">染色中：{{ currentLane() }}</span>
        <button class="lane-btn" @click="applyLane">切换</button>
      </div>
      <div v-if="servedBy" class="served" title="最近一次请求实际命中的实例（Pod 名），泳道切换后此值变化即验证流量走了不同实例">
        <span class="served-lane">lane={{ servedLane || 'default' }}</span>
        <span class="served-svc">{{ servedBy }}</span>
      </div>
      <NotificationCenter />
      <button class="chat-btn" @click="toggleView()">
        {{ view === 'shop' ? '运营视图' : '返回商店' }}
      </button>
      <button class="chat-btn" @click="chatRef?.open()">💬 智能客服</button>
    </header>

    <!-- 泳道横幅（仅 lane 环境）：hostname 染色生效提示 -->
    <div v-if="env.kind === 'lane'" class="lane-banner">泳道演示环境 {{ env.lane }} —— 流量已染色</div>

    <main v-if="view === 'admin'" class="main main-admin">
      <AdminPanel />
    </main>

    <main v-else class="main">
      <section class="products">
        <h2>全部商品（bff → product → postgres）</h2>
        <SearchBar :categories="allCategories" @search="onSearch" />
        <div v-if="loading" class="empty">加载中…</div>
        <div v-else-if="error" class="empty err">{{ error }}</div>
        <div v-else-if="products.length === 0" class="empty">未找到匹配商品</div>
        <div v-else class="grid">
          <ProductCard v-for="p in products" :key="p.id" :product="p" @click="selected = p" />
        </div>
      </section>

      <aside class="rec">
        <h2>为你推荐（bff → recommend → redis + product）</h2>
        <div v-if="recommends.length === 0" class="empty">加载中…</div>
        <div v-else class="rec-list">
          <div v-for="p in recommends" :key="p.id" class="rec-item">
            <div class="rec-name">{{ p.name }}</div>
            <div class="rec-price">{{ fmtPrice(p.price) }}</div>
          </div>
        </div>
      </aside>
    </main>

    <DetailDrawer :product="selected" @close="selected = null" @ask="onAsk" />
    <ChatWindow ref="chatRef" />
  </div>
</template>

<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif; background: #f5f7fa; color: #2c3e50; }
.app { min-height: 100vh; }
.hdr { display: flex; align-items: center; gap: 16px; padding: 14px 32px; background: linear-gradient(135deg, #667eea, #764ba2); color: #fff; box-shadow: 0 2px 8px rgba(0,0,0,.1); }
.logo { font-size: 22px; font-weight: 700; }
.tag { font-size: 13px; opacity: .9; flex: 1; }
.chat-btn { background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 8px 16px; border-radius: 20px; cursor: pointer; font-size: 14px; }
.chat-btn:hover { background: rgba(255,255,255,.3); }
.lane-box { display: flex; align-items: center; gap: 8px; }
.lane-label { font-size: 13px; opacity: .9; }
.lane-input { width: 110px; padding: 5px 10px; border-radius: 14px; border: 1px solid rgba(255,255,255,.4); background: rgba(255,255,255,.15); color: #fff; font-size: 13px; }
.lane-input::placeholder { color: rgba(255,255,255,.6); }
.lane-input:focus { outline: 1px solid rgba(255,255,255,.7); }
.lane-badge { font-size: 12px; background: #ff9800; color: #fff; padding: 3px 10px; border-radius: 12px; white-space: nowrap; }
.lane-btn { background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 5px 12px; border-radius: 14px; cursor: pointer; font-size: 13px; }
.lane-btn:hover { background: rgba(255,255,255,.3); }
.served { display: flex; align-items: center; gap: 8px; font-size: 12px; background: rgba(0,0,0,.25); padding: 4px 12px; border-radius: 14px; }
.served-lane { color: #ffd54f; font-family: ui-monospace, monospace; }
.served-svc { color: #b3e5fc; font-family: ui-monospace, monospace; max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.main { display: grid; grid-template-columns: 1fr 320px; gap: 24px; padding: 24px 32px; }
.products h2, .rec h2 { font-size: 16px; margin-bottom: 16px; color: #5a6a7a; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 16px; }
.rec { background: #fff; border-radius: 12px; padding: 20px; height: fit-content; box-shadow: 0 1px 3px rgba(0,0,0,.08); }
.rec-list { display: flex; flex-direction: column; gap: 10px; }
.rec-item { display: flex; justify-content: space-between; padding: 10px; background: #f8f9fb; border-radius: 8px; }
.rec-name { font-size: 14px; }
.rec-price { color: #e74c3c; font-weight: 600; }
.empty { color: #95a5a6; font-size: 14px; padding: 20px; }
.err { color: #e74c3c; }
/* 环境徽标（spec §11：红生产/绿测试/蓝泳道）+ 泳道横幅（仅 lane 环境） */
.env-badge { padding: 2px 10px; border-radius: 10px; font-size: 12px; color: #fff; white-space: nowrap; }
.env-badge.prod { background: #e5484d; }
.env-badge.test { background: #30a46c; }
.env-badge.lane { background: #3e63dd; }
.lane-banner { background: #eef4ff; color: #3e63dd; padding: 6px 16px; font-size: 13px; }
/* 运营视图单列（商店主体是 1fr+320px 两列，AdminPanel 占满） */
.main-admin { grid-template-columns: 1fr; }
</style>
