<script setup lang="ts">
// 品牌 mark：三层堆叠方块隐喻"控制面 / 插件 / 数据面"三层架构。
// 全站复用（导航栏 / footer）。size 控制尺寸，withWord 是否带字标。
withDefaults(defineProps<{ size?: number; withWord?: boolean }>(), {
  size: 32,
  withWord: true,
})
</script>

<template>
  <span class="logo">
    <span class="mark" :style="{ width: size + 'px', height: size + 'px' }">
      <svg :width="size" :height="size" viewBox="0 0 32 32" fill="none" aria-hidden="true">
        <defs>
          <linearGradient id="lg-mark" x1="0" y1="0" x2="32" y2="32">
            <stop offset="0" stop-color="#6366f1" />
            <stop offset="1" stop-color="#8b5cf6" />
          </linearGradient>
        </defs>
        <!-- 三层堆叠：控制面（顶）/ 插件（中）/ 数据面（底） -->
        <rect x="4" y="4" width="24" height="6" rx="2" fill="url(#lg-mark)" />
        <rect x="8" y="13" width="16" height="6" rx="2" fill="#22d3ee" opacity="0.85" />
        <rect x="4" y="22" width="24" height="6" rx="2" fill="url(#lg-mark)" opacity="0.7" />
      </svg>
    </span>
    <span v-if="withWord" class="word">paas</span>
  </span>
</template>

<style scoped>
.logo {
  display: inline-flex;
  align-items: center;
  gap: 10px;
}
.mark {
  display: inline-grid;
  place-items: center;
  filter: drop-shadow(0 4px 10px rgba(99, 102, 241, 0.35));
}
.word {
  font-family: var(--mono);
  font-size: 19px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--text);
}
</style>
