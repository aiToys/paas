<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import type { Product, Order } from '../types'
import { fmtPrice } from '../format'
import { laneFetch } from '../lane'

const props = defineProps<{ product: Product | null }>()
const emit = defineEmits<{ close: []; ask: [product: Product] }>()

// ESC 关闭抽屉
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))

function fmtDate(s: string) {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN')
}

// 下单/支付（经 laneFetch 自动带泳道；bff 注入 X-Action-Token，前端不感知）
const orderMsg = ref('')
async function buy() {
  if (!props.product) return
  try {
    const resp = await laneFetch('/api/orders', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ userId: 'guest', items: [{ productId: props.product.id, qty: 1 }] }),
    })
    orderMsg.value = resp.ok ? '下单成功，请在运营视图或订单列表支付' : `下单失败：${await resp.text()}`
  } catch {
    orderMsg.value = '下单失败：网络错误，请重试'
  }
}
async function payLatest() {
  try {
    const list: Order[] = await laneFetch('/api/orders?status=pending').then(r => r.json() as Promise<Order[]>)
    if (!list.length) { orderMsg.value = '无待支付订单'; return }
    const resp = await laneFetch(`/api/orders/${list[0].id}/pay`, { method: 'POST' })
    orderMsg.value = resp.ok ? `订单 ${list[0].id} 已支付` : `支付失败：${await resp.text()}`
  } catch {
    orderMsg.value = '支付失败：网络错误，请重试'
  }
}
</script>

<template>
  <transition name="drawer">
    <div v-if="props.product" class="drawer-mask" @click.self="emit('close')">
      <div class="drawer">
        <div class="drawer-hdr">
          <span>商品详情</span>
          <button class="drawer-close" @click="emit('close')">×</button>
        </div>
        <div class="drawer-body">
          <img v-if="props.product.imageUrl" class="drawer-img" :src="props.product.imageUrl" :alt="props.product.name" />
          <div class="card-cat">{{ props.product.category }}</div>
          <div class="drawer-name">{{ props.product.name }}</div>
          <div class="drawer-desc">{{ props.product.description }}</div>
          <div class="drawer-price">{{ fmtPrice(props.product.price) }}</div>
          <div class="drawer-meta">
            <span>库存 {{ props.product.stock }}</span>
            <span v-if="props.product.createdAt">上架时间：{{ fmtDate(props.product.createdAt) }}</span>
          </div>
        </div>
        <div class="drawer-foot">
          <div class="actions">
            <button class="ask-btn" @click="emit('ask', props.product)">💬 问客服</button>
            <button class="buy-btn" @click="buy">立即下单</button>
            <button class="pay-btn" @click="payLatest">支付最新订单</button>
          </div>
          <p v-if="orderMsg" class="order-msg">{{ orderMsg }}</p>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.drawer-mask { position: fixed; inset: 0; background: rgba(0,0,0,.3); z-index: 90; display: flex; justify-content: flex-end; }
.drawer { background: #fff; width: 380px; max-width: 90vw; height: 100%; display: flex; flex-direction: column; box-shadow: -4px 0 24px rgba(0,0,0,.15); }
.drawer-hdr { display: flex; justify-content: space-between; align-items: center; padding: 14px 18px; border-bottom: 1px solid #eee; font-weight: 600; }
.drawer-close { background: none; border: none; font-size: 24px; cursor: pointer; color: #95a5a6; }
.drawer-body { flex: 1; overflow-y: auto; padding: 20px 18px; }
.drawer-img { width: 100%; height: 160px; object-fit: cover; border-radius: 10px; margin-bottom: 12px; display: block; }
.card-cat { display: inline-block; background: #eef; color: #667eea; font-size: 11px; padding: 2px 8px; border-radius: 4px; margin-bottom: 10px; }
.drawer-name { font-size: 20px; font-weight: 700; margin-bottom: 10px; }
.drawer-desc { font-size: 14px; color: #8a9bae; line-height: 1.6; margin-bottom: 18px; }
.drawer-price { color: #e74c3c; font-weight: 700; font-size: 28px; margin-bottom: 14px; }
.drawer-meta { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: #5a6a7a; }
.drawer-foot { padding: 12px 18px; border-top: 1px solid #eee; }
.actions { display: flex; gap: 8px; }
.ask-btn { flex: 1; background: #667eea; color: #fff; border: none; border-radius: 10px; padding: 10px 0; font-size: 14px; cursor: pointer; }
.ask-btn:hover { background: #5a72d8; }
.buy-btn { flex: 1; background: #e74c3c; color: #fff; border: none; border-radius: 10px; padding: 10px 0; font-size: 14px; cursor: pointer; }
.buy-btn:hover { background: #d34532; }
.pay-btn { flex: 1; background: #30a46c; color: #fff; border: none; border-radius: 10px; padding: 10px 0; font-size: 14px; cursor: pointer; }
.pay-btn:hover { background: #2a9162; }
.order-msg { margin-top: 10px; font-size: 13px; color: #5a6a7a; line-height: 1.5; }

/* 右侧滑出过渡 */
.drawer-enter-active, .drawer-leave-active { transition: opacity .2s; }
.drawer-enter-active .drawer, .drawer-leave-active .drawer { transition: transform .25s ease; }
.drawer-enter-from, .drawer-leave-to { opacity: 0; }
.drawer-enter-from .drawer, .drawer-leave-to .drawer { transform: translateX(100%); }
</style>
