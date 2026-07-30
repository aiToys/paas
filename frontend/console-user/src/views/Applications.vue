<script setup lang="ts">
// 应用列表（主线）。应用是逻辑跨环境实体，列表不按 scope 过滤；
// 每个应用卡片显示「在当前 scope 环境的部署徽标」（前端聚合应用 + 工作负载）。
// scope 全部时显示「部署在 N 个环境」。环境切换统一走顶栏，本页无环境控件。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

interface App {
  id: string
  name: string
  initial: string
  status: 'healthy' | 'degraded' | 'idle'
  gradient: string
  resources: { models: number; mq: number; dal: number }
  replicas: string
  rps: string
  desc: string
}
interface Workload {
  id: string
  appId: string
  envId: string
  type: string
  replicas: number
  ready: number
  status: string
}

const apps = ref<App[]>([])
const workloads = ref<Workload[]>([])
const loading = ref(true)
const envStore = useEnvStore()
const router = useRouter()
const route = useRoute()

// 应用名/ID 过滤（顶栏搜索框 ?q=）
const filteredApps = computed(() => {
  const q = (route.query.q ?? '').toString().toLowerCase().trim()
  if (!q) return apps.value
  return apps.value.filter((a) => a.name.toLowerCase().includes(q) || a.id.toLowerCase().includes(q))
})

async function load() {
  loading.value = true
  try {
    // 并行加载应用（逻辑态全量）+ 工作负载（运行态，部署徽标用）
    const [aResp, wResp] = await Promise.all([
      fetchAuth('/api/applications'),
      fetchAuth('/api/workloads?type=service'),
    ])
    if (aResp.ok) apps.value = (await aResp.json()).data ?? []
    if (wResp.ok) workloads.value = (await wResp.json()).data ?? []
  } catch (e) {
    ElMessage.error('加载应用失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

// 按 appID 聚合工作负载（部署徽标用）
const wlByApp = computed(() => {
  const m = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    const arr = m.get(w.appId) ?? []
    arr.push(w)
    m.set(w.appId, arr)
  }
  return m
})

// 部署徽标：scope 具体环境 -> 该环境部署状态；scope 全部 -> 部署环境数。
// 读 envStore.currentEnv（响应式），切换 scope 自动重算，无需重载列表。
function deployBadge(appId: string): { text: string; cls: string } {
  const wls = wlByApp.value.get(appId) ?? []
  if (envStore.currentEnv) {
    const inEnv = wls.filter((w) => w.envId === envStore.currentEnv!.id)
    if (!inEnv.length) return { text: '未部署', cls: 'none' }
    const reps = inEnv.reduce((s, w) => s + w.replicas, 0)
    const ready = inEnv.reduce((s, w) => s + w.ready, 0)
    return { text: `${envStore.currentEnv.name} ${ready}/${reps}`, cls: envStore.isProd ? 'prod' : 'test' }
  }
  const envSet = new Set(wls.map((w) => w.envId))
  if (!envSet.size) return { text: '未部署', cls: 'none' }
  return { text: `${envSet.size} 个环境`, cls: 'multi' }
}

const statusMeta: Record<App['status'], { label: string; cls: string }> = {
  healthy: { label: '健康', cls: 'ok' },
  degraded: { label: '降级', cls: 'warn' },
  idle: { label: '空闲', cls: 'idle' },
}

function open(a: App) {
  router.push(`/applications/${a.id}`)
}

function onKeyChanged() {
  load()
}
onMounted(() => {
  load()
  envStore.loadEnvs()
  window.addEventListener('paas:key-changed', onKeyChanged)
})
onUnmounted(() => window.removeEventListener('paas:key-changed', onKeyChanged))
</script>

<template>
  <div class="page">
    <div class="toolbar">
      <div class="right">
        <span class="count mono">{{ apps.length }} 个应用</span>
        <button class="new-btn">+ 新建应用</button>
      </div>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 6" :key="i" class="skel" />
    </div>
    <div v-else class="grid">
      <article v-for="a in filteredApps" :key="a.id" class="app-card" @click="open(a)">
        <div class="card-top">
          <div class="a-icon" :style="{ background: a.gradient }">{{ a.initial }}</div>
          <div class="a-titles">
            <div class="a-name-row">
              <h3 class="a-name">{{ a.name }}</h3>
              <span class="env-badge" :class="deployBadge(a.id).cls">{{ deployBadge(a.id).text }}</span>
            </div>
            <div class="a-id mono">{{ a.id }}</div>
          </div>
          <span class="status" :class="statusMeta[a.status].cls">
            <span v-if="a.status === 'healthy'" class="pulse-dot" />
            {{ statusMeta[a.status].label }}
          </span>
        </div>

        <p class="a-desc">{{ a.desc }}</p>

        <div class="a-resources">
          <div class="res" :class="{ off: !a.resources.models }">
            <Icon name="market" :size="13" /><span class="mono">{{ a.resources.models }}</span>
          </div>
          <div class="res" :class="{ off: !a.resources.mq }">
            <Icon name="message" :size="13" /><span class="mono">{{ a.resources.mq }}</span>
          </div>
          <div class="res" :class="{ off: !a.resources.dal }">
            <Icon name="database" :size="13" /><span class="mono">{{ a.resources.dal }}</span>
          </div>
        </div>

        <div class="card-foot">
          <div class="foot-stat"><span class="k">副本</span><span class="v mono">{{ a.replicas }}</span></div>
          <div class="foot-stat"><span class="k">请求/秒</span><span class="v mono">{{ a.rps }}</span></div>
        </div>
      </article>

      <button class="add-card">
        <div class="add-icon">+</div>
        <div class="add-text">新建应用</div>
        <div class="add-hint">申请资源、部署服务</div>
      </button>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  margin-bottom: 20px;
}
.right {
  display: flex;
  align-items: center;
  gap: 14px;
}
.count {
  font-size: 12px;
  color: var(--text-faint);
}
.new-btn {
  padding: 8px 16px;
  border: none;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px var(--brand-glow);
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
}
.skel {
  height: 220px;
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
  border: 1px solid var(--border);
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .skel {
    animation: none;
  }
}
.app-card {
  padding: 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  cursor: pointer;
  transition: border-color 0.15s, transform 0.15s, box-shadow 0.15s;
}
.app-card:hover {
  border-color: var(--border-strong);
  transform: translateY(-2px);
  box-shadow: var(--shadow);
}
.card-top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.a-icon {
  width: 42px;
  height: 42px;
  flex-shrink: 0;
  border-radius: 10px;
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 16px;
  color: #fff;
}
.a-name-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.a-name {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.env-badge {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 10.5px;
  font-weight: 500;
  white-space: nowrap;
}
.env-badge.test {
  background: var(--brand-soft);
  color: var(--brand);
}
.env-badge.prod {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  color: var(--warning, #f59e0b);
}
.env-badge.multi {
  background: var(--success-soft);
  color: var(--success);
}
.env-badge.none {
  background: var(--surface-2);
  color: var(--text-faint);
}
.a-id {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 2px;
}
.status {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  flex-shrink: 0;
}
.status.ok {
  color: var(--success);
}
.status.warn {
  color: var(--warning);
}
.status.idle {
  color: var(--text-faint);
}
.status.idle .pulse-dot {
  display: none;
}
.a-desc {
  margin: 14px 0;
  font-size: 12.5px;
  color: var(--text-dim);
  line-height: 1.5;
  min-height: 38px;
}
.a-resources {
  display: flex;
  gap: 14px;
  padding: 10px 0;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.res {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--text-dim);
}
.res :deep(svg) {
  color: var(--brand);
}
.res.off {
  opacity: 0.3;
}
.res.off :deep(svg) {
  color: var(--text-faint);
}
.card-foot {
  display: flex;
  gap: 24px;
  margin-top: 12px;
}
.foot-stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.foot-stat .k {
  font-size: 11px;
  color: var(--text-faint);
}
.foot-stat .v {
  font-size: 13px;
  font-weight: 600;
}

.add-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 200px;
  background: transparent;
  border: 1.5px dashed var(--border-strong);
  border-radius: var(--radius-lg);
  color: var(--text-faint);
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
}
.add-card:hover {
  border-color: var(--brand);
  color: var(--brand);
  background: var(--brand-soft);
}
.add-icon {
  font-size: 28px;
  font-weight: 300;
}
.add-text {
  font-size: 14px;
  font-weight: 600;
}
.add-hint {
  font-size: 12px;
}
</style>
