<script setup lang="ts">
import { ref, onMounted } from 'vue'
import type { Product } from './types'
import { fmtPrice } from './format'
import SearchBar from './components/SearchBar.vue'
import ProductCard from './components/ProductCard.vue'
import DetailDrawer from './components/DetailDrawer.vue'
import ChatWindow from './components/ChatWindow.vue'
import NotificationCenter from './components/NotificationCenter.vue'

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

async function loadProducts(q?: string, category?: string) {
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    if (q) params.set('q', q)
    if (category) params.set('category', category)
    params.set('limit', '50')
    const resp = await fetch('/api/products?' + params.toString())
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
    const resp = await fetch('/api/recommend?userId=user-1')
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
      <div class="tag">智能商品服务 · 演示微服务 + AI 客服 + 全链路可观测</div>
      <NotificationCenter />
      <button class="chat-btn" @click="chatRef?.open()">💬 智能客服</button>
    </header>

    <main class="main">
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
</style>
