<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import type { Product } from '../types'
import { fmtPrice } from '../format'

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
          <div class="card-cat">{{ props.product.category }}</div>
          <div class="drawer-name">{{ props.product.name }}</div>
          <div class="drawer-desc">{{ props.product.description }}</div>
          <div class="drawer-price">{{ fmtPrice(props.product.price) }}</div>
          <div class="drawer-meta">
            <span>库存 {{ props.product.stock }}</span>
            <span v-if="props.product.created_at">上架时间：{{ fmtDate(props.product.created_at) }}</span>
          </div>
        </div>
        <div class="drawer-foot">
          <button class="ask-btn" @click="emit('ask', props.product)">💬 问客服</button>
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
.card-cat { display: inline-block; background: #eef; color: #667eea; font-size: 11px; padding: 2px 8px; border-radius: 4px; margin-bottom: 10px; }
.drawer-name { font-size: 20px; font-weight: 700; margin-bottom: 10px; }
.drawer-desc { font-size: 14px; color: #8a9bae; line-height: 1.6; margin-bottom: 18px; }
.drawer-price { color: #e74c3c; font-weight: 700; font-size: 28px; margin-bottom: 14px; }
.drawer-meta { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: #5a6a7a; }
.drawer-foot { padding: 12px 18px; border-top: 1px solid #eee; }
.ask-btn { width: 100%; background: #667eea; color: #fff; border: none; border-radius: 10px; padding: 10px 0; font-size: 14px; cursor: pointer; }
.ask-btn:hover { background: #5a72d8; }

/* 右侧滑出过渡 */
.drawer-enter-active, .drawer-leave-active { transition: opacity .2s; }
.drawer-enter-active .drawer, .drawer-leave-active .drawer { transition: transform .25s ease; }
.drawer-enter-from, .drawer-leave-to { opacity: 0; }
.drawer-enter-from .drawer, .drawer-leave-to .drawer { transform: translateX(100%); }
</style>
