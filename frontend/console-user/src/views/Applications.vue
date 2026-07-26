<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'

const API_KEY = 'sk-paas-dev-key'

interface App {
  id: string
  name: string
  initial: string
  env: '生产' | '预发' | '开发'
  status: 'healthy' | 'degraded' | 'idle'
  gradient: string
  resources: { models: number; mq: number; dal: number }
  replicas: string
  rps: string
  desc: string
}

const apps = ref<App[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const resp = await fetch('/api/applications', {
      headers: { Authorization: `Bearer ${API_KEY}` },
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const json = await resp.json()
    apps.value = json.data ?? []
  } catch (e) {
    ElMessage.error('加载应用失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const envs = ['全部', '生产', '预发', '开发'] as const
const activeEnv = ref<string>('全部')
const filtered = computed(() => (activeEnv.value === '全部' ? apps.value : apps.value.filter((a) => a.env === activeEnv.value)))

const statusMeta: Record<App['status'], { label: string; cls: string }> = {
  healthy: { label: '健康', cls: 'ok' },
  degraded: { label: '降级', cls: 'warn' },
  idle: { label: '空闲', cls: 'idle' },
}

const router = useRouter()
function open(a: App) {
  router.push(`/applications/${a.id}`)
}
</script>

<template>
  <div class="page">
    <div class="toolbar">
      <div class="envs">
        <button v-for="e in envs" :key="e" class="env" :class="{ on: activeEnv === e }" @click="activeEnv = e">
          {{ e }}
        </button>
      </div>
      <div class="right">
        <span class="count mono">{{ filtered.length }} 个应用</span>
        <button class="new-btn">+ 新建应用</button>
      </div>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 6" :key="i" class="skel" />
    </div>
    <div v-else class="grid">
      <article v-for="a in filtered" :key="a.id" class="app-card" @click="open(a)">
        <div class="card-top">
          <div class="a-icon" :style="{ background: a.gradient }">{{ a.initial }}</div>
          <div class="a-titles">
            <div class="a-name-row">
              <h3 class="a-name">{{ a.name }}</h3>
              <span class="env-badge" :class="a.env">{{ a.env }}</span>
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
  justify-content: space-between;
  margin-bottom: 20px;
}
.envs {
  display: flex;
  gap: 4px;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.env {
  padding: 6px 14px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  border-radius: 7px;
  cursor: pointer;
  transition: all 0.12s;
}
.env.on {
  background: var(--brand);
  color: #fff;
  box-shadow: 0 2px 8px var(--brand-glow);
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
}
.env-badge.生产 {
  background: var(--success-soft);
  color: var(--success);
}
.env-badge.预发 {
  background: var(--warning-soft);
  color: var(--warning);
}
.env-badge.开发 {
  background: var(--surface-2);
  color: var(--text-dim);
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
