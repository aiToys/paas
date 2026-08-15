<script setup lang="ts">
import type { Product } from '../types'
import { fmtPrice } from '../format'

defineProps<{ product: Product }>()
defineEmits<{ click: [] }>()
</script>

<template>
  <div class="card" @click="$emit('click')">
    <div class="card-cat">{{ product.category }}</div>
    <div class="card-name">{{ product.name }}</div>
    <div class="card-desc">{{ product.description }}</div>
    <div class="card-foot">
      <span class="price">{{ fmtPrice(product.price) }}</span>
      <span class="stock">库存 {{ product.stock }}</span>
    </div>
  </div>
</template>

<style scoped>
.card { background: #fff; border-radius: 12px; padding: 16px; box-shadow: 0 1px 3px rgba(0,0,0,.08); transition: transform .2s; cursor: pointer; }
.card:hover { transform: translateY(-2px); box-shadow: 0 4px 12px rgba(0,0,0,.12); }
.card-cat { display: inline-block; background: #eef; color: #667eea; font-size: 11px; padding: 2px 8px; border-radius: 4px; margin-bottom: 8px; }
.card-name { font-size: 16px; font-weight: 600; margin-bottom: 6px; }
.card-desc { font-size: 13px; color: #8a9bae; margin-bottom: 12px; min-height: 36px; }
.card-foot { display: flex; justify-content: space-between; align-items: center; }
.price { color: #e74c3c; font-weight: 700; font-size: 18px; }
.stock { font-size: 12px; color: #95a5a6; }
</style>
