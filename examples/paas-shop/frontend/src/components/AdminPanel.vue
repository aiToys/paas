<!-- 运营视图（spec §11）：概览/订单/商品三 tab。无 router/pinia，跟随现有纯 ref 模式。 -->
<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { laneFetch } from '../lane'
import { fmtPrice } from '../format'
import type { Order, ShopStats, Product } from '../types'

const tab = ref<'overview' | 'orders' | 'products'>('overview')
const stats = ref<ShopStats>({ orders: 0, gmvCents: 0, byStatus: {} })
const orders = ref<Order[]>([])
const statusFilter = ref('')
const products = ref<Product[]>([])
const loadErr = ref('')
let timer: number | undefined

async function refresh() {
  try {
    const [s, o] = await Promise.all([
      laneFetch('/api/stats').then(r => r.json() as Promise<ShopStats>),
      laneFetch('/api/orders' + (statusFilter.value ? `?status=${statusFilter.value}` : '')).then(r => r.json() as Promise<Order[]>),
    ])
    stats.value = s
    orders.value = o
    loadErr.value = ''
  } catch {
    loadErr.value = '数据加载失败（bff/order 不可达）'
  }
}

const actErr = ref('')
async function act(id: string, action: 'ship' | 'close' | 'confirm') {
  try {
    const resp = await laneFetch(`/api/orders/${id}/${action}`, { method: 'POST' })
    actErr.value = resp.ok ? '' : `${action} 失败：${await resp.text()}`
  } catch {
    actErr.value = `${action} 失败：网络错误，请重试`
  }
  await refresh()
}

// 退款引导层：运营动作发生在 PaaS 控制台工作流（spec §6 叙事），这里只复制订单号。
const refundHint = ref('')
function copyRefund(id: string) {
  navigator.clipboard?.writeText(id)
  refundHint.value = `订单号 ${id} 已复制——请到平台控制台「AI 服务 → 工作流」运行 shop-refund-approval 完成退款审批`
}

async function loadProducts() {
  try {
    products.value = await laneFetch('/api/products?limit=100').then(r => r.json() as Promise<Product[]>)
  } catch {
    loadErr.value = '商品加载失败'
  }
}

// 上新表单（product 契约：name/price 必填，价格单位元）
const np = ref({ name: '', price: 0, stock: 0, category: '' })
const productMsg = ref('')
async function createProduct() {
  try {
    const resp = await laneFetch('/api/products', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(np.value),
    })
    if (resp.ok) {
      productMsg.value = `已上新：${np.value.name}`
      np.value = { name: '', price: 0, stock: 0, category: '' }
      await loadProducts()
    } else {
      productMsg.value = `上新失败：${await resp.text()}`
    }
  } catch {
    productMsg.value = '上新失败：网络错误，请重试'
  }
}

onMounted(() => {
  refresh(); loadProducts()
  timer = window.setInterval(refresh, 10000) // 10s 轮询，onUnmounted 清理
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="admin">
    <nav>
      <button :class="{ active: tab === 'overview' }" @click="tab = 'overview'">概览</button>
      <button :class="{ active: tab === 'orders' }" @click="tab = 'orders'">订单</button>
      <button :class="{ active: tab === 'products' }" @click="tab = 'products'">商品</button>
    </nav>
    <p v-if="loadErr" class="hint err">{{ loadErr }}</p>

    <section v-if="tab === 'overview'">
      <div class="cards">
        <div class="card"><b>{{ stats.orders }}</b><span>订单总数</span></div>
        <div class="card"><b>¥{{ (stats.gmvCents / 100).toFixed(2) }}</b><span>GMV</span></div>
        <div class="card" v-for="(n, s) in stats.byStatus" :key="s"><b>{{ n }}</b><span>{{ s }}</span></div>
      </div>
    </section>

    <section v-if="tab === 'orders'">
      <select v-model="statusFilter" @change="refresh">
        <option value="">全部状态</option>
        <option v-for="s in ['pending','paid','shipped','completed','refunded','closed']" :key="s" :value="s">{{ s }}</option>
      </select>
      <p v-if="actErr" class="hint err">{{ actErr }}</p>
      <p v-if="refundHint" class="hint">{{ refundHint }}</p>
      <table>
        <tr><th>订单号</th><th>状态</th><th>金额</th><th>商品</th><th>操作</th></tr>
        <tr v-for="o in orders" :key="o.id">
          <td>{{ o.id }}</td>
          <td><span class="st" :class="o.status">{{ o.status }}</span></td>
          <td>¥{{ (o.totalCents / 100).toFixed(2) }}</td>
          <td>{{ o.items.map(i => `${i.name}×${i.qty}`).join('、') }}</td>
          <td>
            <button v-if="o.status === 'paid'" @click="act(o.id, 'ship')">发货</button>
            <button v-if="o.status === 'shipped'" @click="act(o.id, 'confirm')">确认收货</button>
            <button v-if="o.status === 'pending'" @click="act(o.id, 'close')">关单</button>
            <button v-if="o.status === 'paid' || o.status === 'shipped'" @click="copyRefund(o.id)">退款</button>
          </td>
        </tr>
        <tr v-if="orders.length === 0"><td colspan="5" class="empty">暂无订单</td></tr>
      </table>
    </section>

    <section v-if="tab === 'products'">
      <table>
        <tr><th>商品</th><th>价格</th><th>库存</th><th>分类</th></tr>
        <tr v-for="p in products" :key="p.id">
          <td>{{ p.name }}</td><td>{{ fmtPrice(p.price) }}</td><td>{{ p.stock }}</td><td>{{ p.category }}</td>
        </tr>
      </table>
      <div class="new-product">
        <input v-model="np.name" placeholder="名称（必填）" />
        <input v-model.number="np.price" type="number" min="0" step="0.01" placeholder="价格¥（必填）" />
        <input v-model.number="np.stock" type="number" min="0" placeholder="库存" />
        <input v-model="np.category" placeholder="分类" />
        <button :disabled="!np.name || np.price <= 0" @click="createProduct">上新</button>
      </div>
      <p v-if="productMsg" class="hint">{{ productMsg }}</p>
    </section>
  </div>
</template>

<style scoped>
.admin { background: #fff; border-radius: 12px; padding: 20px; box-shadow: 0 1px 3px rgba(0,0,0,.08); }
nav { display: flex; gap: 8px; margin-bottom: 16px; }
nav button { background: #f8f9fb; border: 1px solid #e3e8ef; color: #5a6a7a; padding: 6px 18px; border-radius: 16px; cursor: pointer; font-size: 14px; }
nav button.active { background: #667eea; border-color: #667eea; color: #fff; }
.hint { font-size: 13px; color: #3e63dd; margin: 8px 0; }
.hint.err { color: #e74c3c; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; }
.card { background: #f8f9fb; border-radius: 10px; padding: 16px; display: flex; flex-direction: column; gap: 4px; }
.card b { font-size: 24px; color: #2c3e50; }
.card span { font-size: 12px; color: #95a5a6; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { text-align: left; color: #95a5a6; font-weight: 500; padding: 8px 10px; border-bottom: 1px solid #eee; }
td { padding: 8px 10px; border-bottom: 1px solid #f5f7fa; }
td button { background: #667eea; color: #fff; border: none; border-radius: 6px; padding: 4px 12px; cursor: pointer; font-size: 12px; margin-right: 6px; }
td button:hover { background: #5a72d8; }
.st { display: inline-block; padding: 2px 10px; border-radius: 10px; font-size: 12px; color: #fff; background: #95a5a6; }
.st.pending { background: #f59f00; }
.st.paid { background: #30a46c; }
.st.shipped { background: #3e63dd; }
.st.completed { background: #7c8698; }
.st.refunded { background: #e5484d; }
.empty { color: #95a5a6; font-size: 14px; text-align: center; padding: 20px; }
.new-product { display: flex; gap: 8px; margin-top: 16px; flex-wrap: wrap; }
.new-product input { flex: 1; min-width: 110px; padding: 7px 10px; border: 1px solid #e3e8ef; border-radius: 8px; font-size: 13px; }
.new-product input:focus { outline: 1px solid #667eea; }
.new-product button { background: #667eea; color: #fff; border: none; border-radius: 8px; padding: 7px 18px; cursor: pointer; font-size: 13px; }
.new-product button:disabled { background: #c6cfe0; cursor: not-allowed; }
</style>
