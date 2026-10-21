<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import type { Product } from './types'
import { fmtPrice } from './format'
import { lane, selectedLaneId, setLane, laneFetch, detectEnv, currentLane, domainPinned, laneError, laneNotice } from './lane'
import { platform, platformLoading, platformError, loadPlatform, consoleLink } from './platform'
import ExperiencePanel from './components/ExperiencePanel.vue'
import SearchBar from './components/SearchBar.vue'
import ProductCard from './components/ProductCard.vue'
import DetailDrawer from './components/DetailDrawer.vue'
import ChatWindow from './components/ChatWindow.vue'
import NotificationCenter from './components/NotificationCenter.vue'
import AdminPanel from './components/AdminPanel.vue'

// 环境推断（spec §10 三套域名）：hostname 定环境，红生产/绿测试/蓝泳道（spec §11）
const env = computed(() => platform.value ? platform.value.envType : detectEnv(window.location.hostname).kind === 'prod' ? 'prod' : 'unknown')
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

const showExperience = ref(false)
const recommendationError = ref('')
const recommendationLoading = ref(false)
const routes = ref<Record<string, { requested: string; resolved: string; reason: string; instance: string; traceId: string }>>({})
const reasonLabels: Record<string, string> = { matched: '命中目标泳道', 'baseline-fallback': '未覆盖，复用基线', static: '静态地址，泳道未验证', unknown: '实际泳道未知' }
const laneActive = computed(() => currentLane() !== '')
const selectionDisabled = computed(() => platformLoading.value || !platform.value || platform.value.envType === 'prod' || !platform.value.discovery || domainPinned.value)
let productsRequest = 0
let recommendationsRequest = 0
function recordRoute(service: string, response: Response) {
  routes.value[service] = {
    requested: response.headers.get('x-paas-requested-lane') || 'default',
    resolved: response.headers.get('x-paas-resolved-lane') || 'unknown',
    reason: response.headers.get('x-paas-route-reason') || 'unknown',
    instance: response.headers.get('x-paas-target-instance') || '未返回',
    traceId: response.headers.get('x-paas-trace-id') || '',
  }
}
function applyLane(event: Event) {
  laneNotice.value = ''
  setLane((event.target as HTMLSelectElement).value)
  selected.value = null
  routes.value = {}
  products.value = []
  recommends.value = []
  void loadProducts(searchQ.value, searchCategory.value)
  void loadRecommend()
}
async function refreshPlatform() {
  await loadPlatform()
  routes.value = {}
  await Promise.all([loadProducts(searchQ.value, searchCategory.value), loadRecommend()])
}

async function loadProducts(q?: string, category?: string) {
  const request = ++productsRequest
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    if (q) params.set('q', q)
    if (category) params.set('category', category)
    params.set('limit', '50')
    const resp = await laneFetch('/api/products?' + params.toString())
    if (request !== productsRequest) return
    if (!resp.ok) throw new Error((await resp.text()).trim() || `商品请求失败（${resp.status}）`)
    recordRoute('商品', resp)
    const data: Product[] = await resp.json()
    if (request !== productsRequest) return
    if (!Array.isArray(data)) throw new Error('商品响应格式错误')
    // 首次全量加载（无过滤）时派生分类缓存
    if (!q && !category && allCategories.value.length === 0) {
      allCategories.value = [...new Set(data.map(p => p.category))]
    }
    products.value = data
  } catch (e) {
    if (request === productsRequest) { delete routes.value['商品']; error.value = e instanceof Error ? e.message : '商品加载失败' }
  } finally {
    if (request === productsRequest) loading.value = false
  }
}

async function loadRecommend() {
  const request = ++recommendationsRequest
  recommendationLoading.value = true
  recommendationError.value = ''
  try {
    const resp = await laneFetch('/api/recommend?userId=user-1')
    if (request !== recommendationsRequest) return
    if (!resp.ok) throw new Error((await resp.text()).trim() || `推荐请求失败（${resp.status}）`)
    const data = await resp.json()
    if (request !== recommendationsRequest) return
    if (!Array.isArray(data.products)) throw new Error('推荐响应格式错误')
    recordRoute('推荐', resp)
    recommends.value = data.products
  } catch (e) {
    if (request === recommendationsRequest) { recommends.value = []; delete routes.value['推荐']; recommendationError.value = e instanceof Error ? e.message : '推荐加载失败' }
  } finally { if (request === recommendationsRequest) recommendationLoading.value = false }
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

onMounted(refreshPlatform)
</script>

<template>
  <div class="app">
    <header class="hdr">
      <div class="logo">🛍️ PaasShop</div>
      <span class="env-badge" :class="env">{{ env === 'prod' ? '生产' : env === 'test' ? '测试' : '环境待核实' }}</span>
      <div class="tag">智能商品服务 · 演示微服务 + AI 客服 + 全链路可观测</div>
      <div class="lane-box">
        <label for="lane-select" class="lane-label">联调泳道</label>
        <select id="lane-select" :value="selectedLaneId" class="lane-input" :disabled="selectionDisabled" @change="applyLane">
          <option value="">基线（default）</option>
          <option v-for="item in platform?.lanes || []" :key="item.id" :value="item.id">{{ item.name }}{{ item.description ? ` · ${item.description}` : '' }}</option>
        </select>
        <button class="lane-btn" :disabled="platformLoading" @click="refreshPlatform">{{ platformLoading ? '读取中…' : '刷新' }}</button>
      </div>
      <button class="chat-btn" :aria-expanded="showExperience" @click="showExperience = !showExperience">平台能力体验</button>
      <NotificationCenter :key="lane" />
      <button class="chat-btn" @click="toggleView()">
        {{ view === 'shop' ? '运营视图' : '返回商店' }}
      </button>
      <button class="chat-btn" @click="chatRef?.open()">💬 智能客服</button>
    </header>

    <div class="scope-bar" aria-live="polite">
      <span v-if="platform">{{ platform.envName }} · {{ laneActive ? `请求泳道：${currentLane()}` : '基线流量' }}</span>
      <span v-if="platform?.envType === 'prod'">生产环境不开放联调泳道</span>
      <span v-else-if="platform && !platform.discovery" class="err">未接入数据面发现，泳道选择暂不可用</span>
      <span v-else-if="platform && !platform.lanes.length">当前环境暂无活跃泳道</span>
      <span v-if="domainPinned">当前泳道由访问域名固定</span>
      <span v-if="laneNotice">{{ laneNotice }}</span>
      <span v-if="platformError || laneError" class="err">{{ platformError || laneError }}</span>
    </div>
    <ExperiencePanel v-if="showExperience" />
    <div v-if="Object.keys(routes).length" class="route-summary" aria-label="最近请求的选路结果">
      <div v-for="(result, service) in routes" :key="service">
        <strong>{{ service }}</strong> 请求 {{ result.requested }} → 选路 {{ result.resolved }} · {{ reasonLabels[result.reason] || result.reason }}
        <span class="route-instance">实例：{{ result.instance }}</span>
        <a v-if="result.traceId && consoleLink('observability')" :href="consoleLink(`observability?tab=traces&traceId=${result.traceId}`)" target="_blank" rel="noopener noreferrer">查看本次链路</a>
      </div>
      <p>以上为商品/推荐请求的选路结果；完整调用链及下游版本请查看 Trace。</p>
    </div>

    <main v-if="view === 'admin'" class="main main-admin">
      <AdminPanel :key="lane" />
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
        <div v-if="recommendationLoading" class="empty">加载中…</div>
        <div v-else-if="recommendationError" class="empty err">{{ recommendationError }}</div>
        <div v-else-if="recommends.length === 0" class="empty">暂无推荐</div>
        <div v-else class="rec-list">
          <div v-for="p in recommends" :key="p.id" class="rec-item">
            <div class="rec-name">{{ p.name }}</div>
            <div class="rec-price">{{ fmtPrice(p.price) }}</div>
          </div>
        </div>
      </aside>
    </main>

    <DetailDrawer :product="selected" @close="selected = null" @ask="onAsk" />
    <ChatWindow :key="lane" ref="chatRef" />
  </div>
</template>

<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif; background: #f5f7fa; color: #2c3e50; }
.app { min-height: 100vh; }
.hdr { display: flex; flex-wrap: wrap; align-items: center; gap: 16px; padding: 14px 32px; background: linear-gradient(135deg, #667eea, #764ba2); color: #fff; box-shadow: 0 2px 8px rgba(0,0,0,.1); }
.logo { font-size: 22px; font-weight: 700; }
.tag { font-size: 13px; opacity: .9; flex: 1; }
.chat-btn { background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 8px 16px; border-radius: 20px; cursor: pointer; font-size: 14px; }
.chat-btn:hover { background: rgba(255,255,255,.3); }
.lane-box { display: flex; align-items: center; gap: 8px; }
.lane-label { font-size: 13px; opacity: .9; }
.lane-input { max-width: 260px; min-width: 160px; padding: 5px 10px; border-radius: 14px; border: 1px solid rgba(255,255,255,.4); background: rgba(255,255,255,.15); color: #fff; font-size: 13px; }
.lane-input option { color: #24304b; background: #fff; }
.lane-input:disabled { opacity: .75; }
.lane-input:focus { outline: 1px solid rgba(255,255,255,.7); }
.lane-btn { background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 5px 12px; border-radius: 14px; cursor: pointer; font-size: 13px; }
.lane-btn:hover { background: rgba(255,255,255,.3); }
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
.env-badge.unknown { background: #526174; }
/* 运营视图单列（商店主体是 1fr+320px 两列，AdminPanel 占满） */
.main-admin { grid-template-columns: 1fr; }
.scope-bar, .route-summary { padding: 12px 32px; font-size: 13px; background: #fff; border-bottom: 1px solid #dce1ea; }
.scope-bar { display: flex; flex-wrap: wrap; gap: 12px; }
.route-summary > div { margin: 6px 0; display: flex; flex-wrap: wrap; gap: 8px; }
.route-summary p { color: #526174; margin-top: 8px; }
.route-instance { font-family: ui-monospace, monospace; overflow-wrap: anywhere; }
a { color: #304ca9; }
button:focus-visible, select:focus-visible, a:focus-visible { outline: 3px solid #d79600; outline-offset: 3px; }
button:disabled { cursor: default; opacity: .65; }
.err { color: #ad2525; }
@media (max-width: 760px) { .hdr { padding: 16px; gap: 12px; } .tag { flex-basis: 100%; } .main { grid-template-columns: 1fr; padding: 16px; } .scope-bar, .route-summary { padding: 12px 16px; } .lane-input { max-width: 210px; } }
</style>
