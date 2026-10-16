<script setup lang="ts">
import { computed, watch } from 'vue'
const props = withDefaults(defineProps<{ modelValue: number; total: number; size?: number }>(), { size: 20 })
const emit = defineEmits<{ (e: 'update:modelValue', value: number): void }>()
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.size)))
watch(pages, (n) => { if (props.modelValue > n) emit('update:modelValue', n) })
</script>
<template>
  <div class="table-footer">
    <span role="status">共 {{ total }} 项<span v-if="total"> · 显示 {{ (modelValue - 1) * size + 1 }}–{{ Math.min(modelValue * size, total) }}</span></span>
    <nav v-if="pages > 1" class="pagination" aria-label="列表分页">
      <button class="act small" :disabled="modelValue <= 1" @click="emit('update:modelValue', modelValue - 1)">上一页</button>
      <span>{{ modelValue }} / {{ pages }}</span>
      <button class="act small" :disabled="modelValue >= pages" @click="emit('update:modelValue', modelValue + 1)">下一页</button>
    </nav>
  </div>
</template>
