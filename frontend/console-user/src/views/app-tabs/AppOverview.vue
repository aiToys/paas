<script setup lang="ts">
// 应用详情 - 概览 tab（真实工作台）：副本就绪 + 指标 sparkline + 访问入口 + 依赖拓扑 + 最新发布/构建。
// 概览专属数据（指标/发布/构建）本组件自拉（挂载即拉 = 切到概览 tab 才加载，应用详情首屏不白拉）；
// workloads 由父组件传入（部署 tab 与概览副本统计共享）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import { fetchJSON } from '@/api'
import { formatDateTime } from '@/utils/format'
import { BUILD_STATUS, RELEASE_STATUS, statusOf } from '@/composables/useStatus'
import { typeMeta, TYPE_ORDER, type Binding } from './resource-meta'
import type { Workload } from '@/api/workload'

interface AppInfo {
  id: string
  name: string
  desc?: string
  gradient: string
  initial: string
  bindings?: Binding[]
}
const props = defineProps<{ app: AppInfo; workloads: Workload[] }>()

const router = useRouter()

// 概览聚合：副本就绪比（去 seed 假数据 rps/replicas，真实运行态）。
const replicaStat = computed(() => {
  const total = props.workloads.reduce((s, w) => s + w.replicas, 0)
  const ready = props.workloads.reduce((s, w) => s + w.ready, 0)
  return { ready, total }
})

// 访问入口：聚合对外暴露的工作负载（domain 非空 → reconciler 自动建 Ingress），
// 让用户在概览一眼看到「怎么访问这个应用」。
const accessEntries = computed(() =>
  props.workloads
    .filter((w) => w.type === 'service' && w.domain)
    .map((w) => ({ workload: w.name, domain: w.domain!, port: w.port }))
)

// 依赖拓扑：按 type 分组绑定项（仅展示有绑定项的分组）。
const groups = computed(() => {
  const byType = new Map<string, Binding[]>()
  for (const b of props.app.bindings ?? []) {
    const arr = byType.get(b.type) ?? []
    arr.push(b)
    byType.set(b.type, arr)
  }
  return TYPE_ORDER.filter((t) => byType.has(t)).map((t) => ({ key: t, meta: typeMeta[t], items: byType.get(t)! }))
})

// —— 概览专属数据（懒加载）：指标 + 最新发布/构建 ——
interface MetricPoint { ts: string; value: number }
interface MetricSeries { name: string; unit: string; current: number; points: MetricPoint[] }
interface Release { id: string; status: string; envId: string; createdAt: string }
interface Build { id: string; status: string; startedAt: string }
const metrics = ref<MetricSeries[]>([])
const metricsFailed = ref(false)
const latestRelease = ref<Release | null>(null)
const latestBuild = ref<Build | null>(null)

async function loadOverview() {
  const [mt, rels, blds] = await Promise.allSettled([
    fetchJSON<MetricSeries[]>(`/api/observability/metrics?targetType=app&targetId=${props.app.id}`),
    fetchJSON<Release[]>(`/api/applications/${props.app.id}/releases`),
    fetchJSON<Build[]>(`/api/applications/${props.app.id}/buildruns`),
  ])
  metrics.value = mt.status === 'fulfilled' ? mt.value : []
  metricsFailed.value = mt.status === 'rejected'
  latestRelease.value = rels.status === 'fulfilled' && rels.value.length ? rels.value[0] : null
  latestBuild.value = blds.status === 'fulfilled' && blds.value.length ? blds.value[0] : null
}

// 挂载即拉（v-else-if 切到概览 tab 才 mount = 懒加载）；allSettled 不 reject，失败态走 metricsFailed。
onMounted(() => { void loadOverview() })

const cpuSeries = computed(() => metrics.value.find((m) => m.name === 'cpu'))
const rpsSeries = computed(() => metrics.value.find((m) => m.name === 'rps'))

function sparkHeights(points?: MetricPoint[]): number[] {
  if (!points || points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}

function onBindingClick() {
  router.push('/applications/' + props.app.id + '?tab=bindings')
}
</script>

<template>
  <div class="overview">
    <div v-if="metricsFailed" class="load-fail">
      <span>指标加载失败（可观测服务异常），下述指标暂不可用</span>
      <button class="retry-btn" @click="loadOverview">重试</button>
    </div>
    <p v-if="app.desc" class="overview-desc">{{ app.desc }}</p>
    <div class="metrics">
      <div class="metric">
        <div class="m-v mono">{{ replicaStat.ready }}/{{ replicaStat.total }}</div>
        <div class="m-k">副本就绪</div>
      </div>
      <div class="metric">
        <div class="m-v mono">{{ app.bindings?.length ?? 0 }}</div>
        <div class="m-k">绑定资源</div>
      </div>
      <div class="metric">
        <div class="m-v mono">{{ rpsSeries ? rpsSeries.current.toFixed(1) : '—' }}</div>
        <div class="m-k">请求/秒</div>
        <div v-if="rpsSeries" class="mini-spark">
          <span v-for="(h, i) in sparkHeights(rpsSeries.points)" :key="i" :style="{ height: h + '%' }" />
        </div>
      </div>
      <div class="metric">
        <div class="m-v mono">{{ cpuSeries ? cpuSeries.current.toFixed(1) + cpuSeries.unit : '—' }}</div>
        <div class="m-k">CPU</div>
        <div v-if="cpuSeries" class="mini-spark">
          <span v-for="(h, i) in sparkHeights(cpuSeries.points)" :key="i" :style="{ height: h + '%' }" />
        </div>
      </div>
    </div>

    <!-- 访问入口：该应用对外暴露的域名（工作负载 domain → 自动建 Ingress） -->
    <section v-if="accessEntries.length" class="access-card">
      <div class="chart-title">访问入口</div>
      <div class="access-list">
        <a
          v-for="e in accessEntries" :key="e.workload"
          :href="'http://' + e.domain" target="_blank" rel="noopener" class="access-item"
        >
          <Icon name="link" :size="14" />
          <span class="access-domain mono">{{ e.domain }}</span>
          <span class="access-wl faint">{{ e.workload }}</span>
        </a>
      </div>
    </section>

    <div class="overview-row">
      <div class="topo-card">
        <div class="chart-title">资源依赖拓扑</div>
        <div class="topo-graph">
          <div class="topo-app">
            <div class="a-icon small" :style="{ background: app.gradient }">{{ app.initial }}</div>
            <span>{{ app.name }}</span>
          </div>
          <div class="topo-links">
            <div v-for="g in groups" :key="g.key" class="topo-res" @click="onBindingClick">
              <Icon :name="g.meta.icon" :size="16" :style="{ color: g.meta.color }" />
              <span>{{ g.meta.label }}</span>
              <span class="topo-n mono">{{ g.items.length }}</span>
            </div>
            <div v-if="!groups.length" class="topo-empty" @click="onBindingClick">尚未绑定资源</div>
          </div>
        </div>
      </div>

      <div class="side-cards">
        <div class="side-card">
          <div class="side-label">最新发布</div>
          <div v-if="latestRelease" class="side-body">
            <el-tag size="small" :type="statusOf(RELEASE_STATUS, latestRelease.status).type">{{ statusOf(RELEASE_STATUS, latestRelease.status).label }}</el-tag>
            <span class="side-time">{{ formatDateTime(latestRelease.createdAt) }}</span>
          </div>
          <div v-else class="side-empty">暂无发布</div>
        </div>
        <div class="side-card">
          <div class="side-label">最新构建</div>
          <div v-if="latestBuild" class="side-body">
            <el-tag size="small" :type="statusOf(BUILD_STATUS, latestBuild.status).type">{{ statusOf(BUILD_STATUS, latestBuild.status).label }}</el-tag>
            <span class="side-time">{{ formatDateTime(latestBuild.startedAt) }}</span>
          </div>
          <div v-else class="side-empty">暂无构建</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 副数据加载失败提示条：区别于空态（指标显示 '—'），带重试出口 */
.load-fail {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  margin-bottom: 16px;
  border: 1px dashed var(--warning, #f59e0b);
  border-radius: var(--radius);
  background: rgba(245, 158, 11, 0.08);
  font-size: 12.5px;
  color: var(--warning, #f59e0b);
}
.retry-btn {
  flex-shrink: 0;
  padding: 4px 12px;
  border: 1px solid var(--warning, #f59e0b);
  border-radius: 5px;
  background: transparent;
  color: var(--warning, #f59e0b);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
}
.retry-btn:hover {
  background: rgba(245, 158, 11, 0.12);
}
.overview-desc {
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.6;
}
.metrics {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 20px;
}
.metric {
  padding: 18px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.m-v {
  font-size: 24px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.m-k {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 2px;
}
.chart-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 18px;
}
.access-card {
  margin-top: 16px;
  padding: 16px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.access-list { display: flex; flex-wrap: wrap; gap: 10px; }
.access-item {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px; border-radius: var(--radius);
  background: var(--surface-alt, rgba(99, 102, 241, 0.08));
  color: var(--primary, #6366f1); font-size: 13px;
  text-decoration: none; transition: opacity 0.15s;
}
.access-item:hover { opacity: 0.8; }
.access-domain { font-weight: 600; }
.access-wl { font-size: 11px; }
.a-icon.small {
  width: 32px;
  height: 32px;
  font-size: 14px;
  border-radius: 8px;
  display: grid;
  place-items: center;
  font-weight: 700;
  color: #fff;
  flex-shrink: 0;
}
.overview-row { display: flex; gap: 16px; flex-wrap: wrap; }
.topo-card {
  padding: 20px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  flex: 1;
  min-width: 320px;
}
.topo-graph {
  display: flex;
  align-items: center;
  gap: 48px;
  flex-wrap: wrap;
}
.topo-app {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-dim);
}
.topo-links {
  display: flex;
  gap: 20px;
  flex-wrap: wrap;
}
.topo-res {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 12.5px;
  cursor: pointer;
  transition: border-color 0.12s;
}
.topo-res:hover { border-color: var(--border-strong); }
.topo-n {
  margin-left: 2px;
  font-weight: 600;
  color: var(--text);
}
.topo-empty { font-size: 12px; color: var(--text-faint); cursor: pointer; }
.side-cards { display: flex; flex-direction: column; gap: 12px; min-width: 220px; }
.side-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.side-label { font-size: 12px; color: var(--text-faint); margin-bottom: 8px; }
.side-body { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.side-time { font-size: 12px; color: var(--text-dim); }
.side-empty { font-size: 13px; color: var(--text-faint); }
.mini-spark { display: flex; align-items: flex-end; gap: 2px; height: 24px; margin-top: 6px; }
.mini-spark span { flex: 1; background: var(--brand); opacity: 0.6; border-radius: 2px 2px 0 0; min-width: 2px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.faint { color: var(--text-faint); font-size: 11px; }
</style>
