<script setup lang="ts">
import { computed } from 'vue'

// 自制 SVG sparkline / 折线，保持轻量、不引图表库。
function points(data: number[], w: number, h: number) {
  const max = Math.max(...data, 1)
  const min = Math.min(...data, 0)
  const span = max - min || 1
  const step = w / (data.length - 1)
  return data
    .map((v, i) => `${(i * step).toFixed(1)},${(h - ((v - min) / span) * h).toFixed(1)}`)
    .join(' ')
}

const stats = [
  { title: '本月 Token', value: '12.8M', delta: '+18%', up: true, data: [8, 9, 11, 10, 13, 12, 14, 15, 13, 16, 18, 17, 20, 22], color: '#6366f1' },
  { title: '推理请求', value: '86.4K', delta: '+7%', up: true, data: [40, 45, 42, 50, 48, 55, 60, 58, 62, 65, 70, 68, 72, 75], color: '#10b981' },
  { title: 'GPU 时长', value: '320', unit: '卡·时', delta: '-3%', up: false, data: [30, 28, 32, 29, 27, 30, 28, 26, 29, 27, 25, 28, 26, 24], color: '#f59e0b' },
  { title: '本月费用', value: '¥2,560', delta: '+12%', up: true, data: [10, 12, 14, 13, 16, 18, 17, 20, 22, 21, 24, 26, 28, 30], color: '#ec4899' },
]

const trend = [120, 180, 150, 220, 280, 240, 320, 380, 340, 420, 480, 440, 520, 580]
const trendW = 800
const trendH = 180

const dist = [
  { name: 'Qwen2.5-7B', pct: 42, color: '#6366f1' },
  { name: 'DeepSeek-V3', pct: 28, color: '#10b981' },
  { name: 'bge-m3', pct: 18, color: '#f59e0b' },
  { name: 'GLM-4-9B', pct: 12, color: '#ec4899' },
]
const days = computed(() => trend.map((_, i) => `${i + 12}日`))
</script>

<template>
  <div class="page">
    <div class="stats-grid">
      <div v-for="s in stats" :key="s.title" class="stat-card">
        <div class="stat-head">
          <span class="stat-title">{{ s.title }}</span>
          <span class="delta" :class="{ up: s.up, down: !s.up }">{{ s.delta }}</span>
        </div>
        <div class="stat-value mono">
          {{ s.value }}<span v-if="s.unit" class="unit">{{ s.unit }}</span>
        </div>
        <svg class="spark" :viewBox="`0 0 100 32`" preserveAspectRatio="none">
          <polyline :points="points(s.data, 100, 32)" fill="none" :stroke="s.color" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </div>
    </div>

    <div class="chart-card">
      <div class="chart-head">
        <div>
          <div class="chart-title">Token 消耗趋势</div>
          <div class="chart-sub">最近 14 天 · 单位 K</div>
        </div>
        <div class="legend"><span class="pulse-dot" /> 实时</div>
      </div>
      <svg class="trend" :viewBox="`0 0 ${trendW} ${trendH + 24}`" preserveAspectRatio="none">
        <defs>
          <linearGradient id="fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#6366f1" stop-opacity="0.35" />
            <stop offset="100%" stop-color="#6366f1" stop-opacity="0" />
          </linearGradient>
        </defs>
        <polygon :points="`0,${trendH} ${points(trend, trendW, trendH)} ${trendW},${trendH}`" fill="url(#fill)" />
        <polyline :points="points(trend, trendW, trendH)" fill="none" stroke="#6366f1" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <div class="x-axis">
        <span v-for="(d, i) in days" :key="i">{{ i % 2 === 0 ? d : '' }}</span>
      </div>
    </div>

    <div class="dist-card">
      <div class="chart-title">按模型分布</div>
      <div class="dist-list">
        <div v-for="d in dist" :key="d.name" class="dist-row">
          <span class="dist-name">{{ d.name }}</span>
          <div class="bar-track">
            <div class="bar-fill" :style="{ width: d.pct + '%', background: d.color }" />
          </div>
          <span class="dist-pct mono">{{ d.pct }}%</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1100px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
.stat-card {
  padding: 18px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.stat-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.stat-title {
  font-size: 12px;
  color: var(--text-faint);
}
.delta {
  font-size: 11.5px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 5px;
}
.delta.up {
  color: var(--success);
  background: var(--success-soft);
}
.delta.down {
  color: var(--danger);
  background: var(--danger-soft);
}
.stat-value {
  font-size: 24px;
  font-weight: 700;
  letter-spacing: -0.02em;
  margin: 8px 0 12px;
}
.unit {
  font-size: 13px;
  color: var(--text-faint);
  font-weight: 500;
  margin-left: 4px;
}
.spark {
  width: 100%;
  height: 32px;
  display: block;
}

.chart-card,
.dist-card {
  padding: 20px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.chart-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.chart-title {
  font-size: 14px;
  font-weight: 600;
}
.chart-sub {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 2px;
}
.legend {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--success);
}
.trend {
  width: 100%;
  height: 180px;
  display: block;
}
.x-axis {
  display: flex;
  justify-content: space-between;
  font-size: 10.5px;
  color: var(--text-faint);
  margin-top: 4px;
}

.dist-list {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 16px;
}
.dist-row {
  display: grid;
  grid-template-columns: 140px 1fr 48px;
  align-items: center;
  gap: 14px;
}
.dist-name {
  font-size: 13px;
  color: var(--text-dim);
}
.bar-track {
  height: 8px;
  background: var(--surface-2);
  border-radius: 4px;
  overflow: hidden;
}
.bar-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.4s ease;
}
.dist-pct {
  font-size: 12.5px;
  color: var(--text);
  text-align: right;
}
</style>
