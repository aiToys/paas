<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'

const props = defineProps<{ categories: string[] }>()
const emit = defineEmits<{ search: [q: string, category: string] }>()

const q = ref('')
const category = ref('')

// 输入防抖 300ms；新输入清旧 timer，回车立即触发
let timer: ReturnType<typeof setTimeout> | null = null

watch(q, () => {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => {
    timer = null
    emit('search', q.value, category.value)
  }, 300)
})

function onEnter() {
  if (timer) clearTimeout(timer)
  timer = null
  emit('search', q.value, category.value)
}

function pickCategory(c: string) {
  category.value = c
  emit('search', q.value, category.value)
}

onUnmounted(() => {
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <div class="searchbar">
    <input v-model="q" class="search-input" placeholder="搜索商品名称…" @keyup.enter="onEnter" />
    <div class="chips">
      <button :class="['chip', category === '' ? 'chip-on' : '']" @click="pickCategory('')">全部</button>
      <button
        v-for="c in props.categories"
        :key="c"
        :class="['chip', category === c ? 'chip-on' : '']"
        @click="pickCategory(c)"
      >
        {{ c }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.searchbar { display: flex; flex-direction: column; gap: 10px; margin-bottom: 16px; }
.search-input { width: 100%; border: 1px solid #ddd; border-radius: 10px; padding: 10px 14px; font-size: 14px; background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,.05); }
.search-input:focus { outline: none; border-color: #667eea; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chip { background: #fff; border: 1px solid #e3e8ef; color: #5a6a7a; font-size: 12px; padding: 4px 12px; border-radius: 14px; cursor: pointer; }
.chip:hover { border-color: #667eea; color: #667eea; }
.chip-on { background: #667eea; border-color: #667eea; color: #fff; }
</style>
