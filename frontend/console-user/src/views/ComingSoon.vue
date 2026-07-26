<script setup lang="ts">
import Icon from '@/components/Icon.vue'

defineProps<{ product: string; features: string[] }>()
</script>

<template>
  <div class="soon">
    <div class="badge"><span class="pulse-dot" /> 即将上线</div>
    <div class="rocket"><Icon name="rocket" :size="40" /></div>
    <h2>{{ product }}</h2>
    <p class="desc">该产品域正在规划中，将以插件形式接入统一控制台，与本平台共享鉴权、计量与可观测能力。</p>
    <div class="features">
      <div v-for="f in features" :key="f" class="feature">
        <Icon name="check" :size="14" />
        <span>{{ f }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.soon {
  max-width: 480px;
  margin: 60px auto;
  text-align: center;
  padding: 40px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border-radius: 20px;
  background: var(--brand-soft);
  color: var(--brand);
  font-size: 12px;
  font-weight: 500;
}
.rocket {
  margin: 24px 0 16px;
  color: var(--brand);
}
h2 {
  margin: 0 0 8px;
  font-size: 22px;
  letter-spacing: -0.02em;
}
.desc {
  margin: 0 0 24px;
  color: var(--text-dim);
  font-size: 14px;
  line-height: 1.6;
}
.features {
  display: flex;
  flex-direction: column;
  gap: 10px;
  text-align: left;
  max-width: 320px;
  margin: 0 auto;
}
.feature {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-dim);
}
.feature :deep(svg) {
  color: var(--success);
  flex-shrink: 0;
}
</style>
