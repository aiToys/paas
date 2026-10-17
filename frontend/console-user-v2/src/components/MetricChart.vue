<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { finitePoints, metricLabel, metricScale } from '@/domain/observability'
import type { MetricSeries } from '@/api/observability'
const props = defineProps<{
  series: MetricSeries[]
  names: Record<string, string>
  title?: string
}>()
const focus = ref<number | null>(null)
const chartElement = ref<SVGSVGElement>()
const chartWidth = ref(700)
const plotWidth = computed(() => Math.max(120, chartWidth.value - 70))
let observer: ResizeObserver | undefined
onMounted(() => {
  if (chartElement.value) {
    observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width
      if (width) chartWidth.value = width
    })
    observer.observe(chartElement.value)
  }
})
onUnmounted(() => observer?.disconnect())
const palette = [
  'var(--brand)',
  'var(--chart-teal)',
  'var(--chart-violet)',
  'var(--chart-amber)',
  'var(--chart-rose)',
]
const rows = computed(() =>
  props.series.map((s, i) => ({
    ...s,
    points: finitePoints(s.points || []),
    color: palette[i % palette.length],
  })),
)
const all = computed(() => rows.value.flatMap((s) => s.points))
const minTime = computed(() =>
  Math.min(...all.value.map((p) => Date.parse(p.ts))),
)
const maxTime = computed(() =>
  Math.max(...all.value.map((p) => Date.parse(p.ts))),
)
const maxValue = computed(
  () => Math.max(0.001, ...all.value.map((p) => p.value)) * 1.12,
)
const minValue = computed(() => Math.min(0, ...all.value.map((p) => p.value)))
const x = (ts: string) =>
  54 +
  ((Date.parse(ts) - minTime.value) / (maxTime.value - minTime.value || 1)) *
    plotWidth.value
const y = (v: number) =>
  170 - ((v - minValue.value) / (maxValue.value - minValue.value)) * 140
const path = (s: (typeof rows.value)[number]) =>
  s.points
    .map(
      (p, i) =>
        `${i ? 'L' : 'M'}${x(p.ts).toFixed(2)},${y(p.value).toFixed(2)}`,
    )
    .join(' ')
const time = (n: number) =>
  new Date(n).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
const cursorTime = computed(
  () =>
    minTime.value +
    ((focus.value ?? 0) / 100) * (maxTime.value - minTime.value),
)
const readings = computed(() =>
  rows.value.map((s) => ({
    ...s,
    point: s.points.reduce<(typeof s.points)[number] | undefined>(
      (best, p) =>
        !best ||
        Math.abs(Date.parse(p.ts) - cursorTime.value) <
          Math.abs(Date.parse(best.ts) - cursorTime.value)
          ? p
          : best,
      undefined,
    ),
  })),
)
function move(e: MouseEvent) {
  const r = e.currentTarget as SVGElement
  const rect = r.getBoundingClientRect()
  focus.value = Math.max(
    0,
    Math.min(
      100,
      ((((e.clientX - rect.left) / rect.width) * chartWidth.value - 54) /
        plotWidth.value) *
        100,
    ),
  )
}
const legendRows = computed(() =>
  readings.value.map((s) => ({
    ...s,
    displayValue: focus.value === null ? s.current : (s.point?.value ?? NaN),
  })),
)
const unit = computed(() => props.series[0]?.unit || '')
const scale = computed(() =>
  metricScale(
    unit.value,
    Math.max(
      ...all.value.map((p) => Math.abs(p.value)),
      ...rows.value.map((s) => Math.abs(s.current)),
    ),
  ),
)
const formatted = (value: number) => {
  if (!Number.isFinite(value)) return '—'
  const rounded = Number((value * scale.value.factor).toFixed(3))
  return (rounded === 0 ? 0 : rounded).toLocaleString('zh-CN', {
    maximumFractionDigits: 3,
  })
}
</script>
<template>
  <article class="metric-chart">
    <header>
      <div>
        <h3>{{ title || metricLabel(series[0]?.name || '') }}</h3>
        <span class="unit">{{ scale.unit }} · {{ series.length }} 个实体</span>
      </div>
      <strong v-if="series.length === 1" class="reading">{{ formatted(series[0]!.current) }}
        <small>{{ scale.unit }}</small></strong>
    </header>
    <template v-if="all.length">
      <svg
        ref="chartElement"
        :viewBox="`0 0 ${chartWidth} 210`"
        role="img"
        :aria-label="`${title || metricLabel(series[0]?.name || '')}时间趋势，单位${scale.unit}`"
        @mousemove="move"
        @mouseleave="focus = null"
      >
        <g v-for="i in [0, 1, 2, 3]" :key="i">
          <line
            x1="54"
            :x2="chartWidth - 16"
            :y1="30 + (i * 140) / 3"
            :y2="30 + (i * 140) / 3"
            class="grid-line"
          />
          <text x="44" :y="34 + (i * 140) / 3" text-anchor="end">
            {{ formatted(maxValue - (i * (maxValue - minValue)) / 3) }}
          </text>
        </g>
        <g v-for="(s, i) in rows" :key="s.id || i">
          <path
            :d="path(s)"
            fill="none"
            :stroke="s.color"
            stroke-width="2.2"
            vector-effect="non-scaling-stroke"
          />
          <circle
            v-if="s.points.length === 1"
            :cx="x(s.points[0]!.ts)"
            :cy="y(s.points[0]!.value)"
            r="3"
            :fill="s.color"
          />
        </g>
        <line
          v-if="focus !== null"
          :x1="54 + (focus / 100) * plotWidth"
          :x2="54 + (focus / 100) * plotWidth"
          y1="25"
          y2="175"
          class="cursor-line"
        />
        <text x="54" y="198">{{ time(minTime) }}</text>
        <text :x="54 + plotWidth / 2" y="198" text-anchor="middle">
          {{ time((minTime + maxTime) / 2) }}
        </text>
        <text :x="chartWidth - 16" y="198" text-anchor="end">
          {{ time(maxTime) }}
        </text>
      </svg>
      <label class="chart-scrubber">时间读数<input
          type="range"
          min="0"
          max="100"
          :value="focus ?? 100"
          aria-label="选择图表时间点"
          @input="focus = Number(($event.target as HTMLInputElement).value)"
      /></label>
      <div class="chart-legend" aria-live="polite">
        <time v-if="focus !== null">{{ time(cursorTime) }}</time><span
          v-for="(s, i) in legendRows"
          :key="s.id || i"
          :title="names[s.targetType + ':' + s.targetId] || s.targetId"
          ><i :style="{ background: s.color }" />{{
            names[s.targetType + ':' + s.targetId] || s.targetId
          }}
          <b>{{ formatted(s.displayValue) }}</b></span>
      </div>
    </template>
    <div v-else class="chart-empty">
      尚无有效时序样本<span>未采集不代表数值为 0</span>
    </div>
  </article>
</template>
<style scoped>
.metric-chart {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  padding: 20px;
  min-width: 0;
}
header {
  display: flex;
  justify-content: space-between;
  align-items: start;
  gap: 12px;
}
h3 {
  font-size: 14px;
  margin: 0 0 5px;
}
.unit,
small {
  color: var(--text-faint);
  font-size: 11px;
  font-weight: 400;
}
.reading {
  font-size: 23px;
  font-variant-numeric: tabular-nums;
}
svg {
  width: 100%;
  display: block;
  margin-top: 18px;
  overflow: visible;
}
svg text {
  fill: var(--text-faint);
  font-size: 11px;
}
.grid-line {
  stroke: var(--border);
  stroke-dasharray: 3 4;
}
.cursor-line {
  stroke: var(--text-dim);
  stroke-dasharray: 3;
}
.chart-legend {
  display: flex;
  gap: 8px 18px;
  flex-wrap: wrap;
  font-size: 11px;
  min-height: 22px;
}
.chart-legend span {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  overflow-wrap: anywhere;
}
.chart-legend b {
  font-variant-numeric: tabular-nums;
}
.chart-legend i {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
time {
  color: var(--text-faint);
}
.chart-scrubber {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--text-faint);
  font-size: 11px;
  margin-bottom: 12px;
}
.chart-scrubber input {
  flex: 1;
  min-width: 0;
  accent-color: var(--brand-solid);
}
.chart-empty {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: center;
  justify-content: center;
  height: 230px;
  color: var(--text-dim);
  font-size: 13px;
}
.chart-empty span {
  font-size: 12px;
  color: var(--text-faint);
}
</style>
