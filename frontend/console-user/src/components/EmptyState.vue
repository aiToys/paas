<script setup lang="ts">
// 公共空状态组件：消除 6+ 视图重复的 empty 结构（DRY）。
// icon → 图标名；text → 主文案；副内容/CTA 走默认 slot。
import Icon from './Icon.vue'
defineProps<{ icon?: string; text?: string }>()
</script>

<template>
  <div class="empty-state">
    <Icon v-if="icon" :name="icon" :size="32" />
    <p v-if="text">{{ text }}</p>
    <slot />
  </div>
</template>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 60px 20px;
  color: var(--text-faint);
  text-align: center;
}
.empty-state :deep(svg) {
  color: var(--brand);
}
.empty-state p {
  margin: 0;
  font-size: 13px;
}
</style>
