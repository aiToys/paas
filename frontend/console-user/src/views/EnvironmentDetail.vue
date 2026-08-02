<script setup lang="ts">
// 环境详情页（管理面）：单环境的深度视图。
// 工作负载总览（按类型）+ 应用部署矩阵（工作负载反推 appID + 应用信息）。
// 「在此环境工作」= 切顶栏 scope（操作面）+ 跳工作负载，桥接管理面 -> 操作面。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

interface Env {
  id: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
}
interface Workload {
  id: string
  appId: string
  envId: string
  type: string
  name: string
  replicas: number
  ready: number
  status: string
}
interface App {
  id: string
  name: string
  gradient: string
  initial: string
}

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
const env = ref<Env | null>(null)
const workloads = ref<Workload[]>([])
const apps = ref<App[]>([])
const loading = ref(true)

const typeMeta = [
  { key: 'service', label: '服务', icon: 'server' },
  { key: 'job', label: '任务', icon: 'job' },
  { key: 'cronjob', label: '定时', icon: 'clock' },
]

async function load() {
  loading.value = true
  try {
    const id = route.params.id as string
    const [eResp, wResp, aResp] = await Promise.all([
      fetchAuth('/api/environments'),
      fetchAuth(`/api/workloads?envId=${id}`),
      fetchAuth('/api/applications'),
    ])
    if (eResp.ok) {
      const all = ((await eResp.json()).data ?? []) as Env[]
      env.value = all.find((e) => e.id === id) ?? null
    }
    if (wResp.ok) workloads.value = (await wResp.json()).data ?? []
    if (aResp.ok) apps.value = (await aResp.json()).data ?? []
  } catch (e) {
    ElMessage.error('加载环境详情失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

const byType = computed(() => {
  const m = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    const arr = m.get(w.type) ?? []
    arr.push(w)
    m.set(w.type, arr)
  }
  return m
})

// 应用部署矩阵：工作负载反推 appID + 应用信息
const appMatrix = computed(() => {
  const byApp = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    const arr = byApp.get(w.appId) ?? []
    arr.push(w)
    byApp.set(w.appId, arr)
  }
  return [...byApp.entries()].map(([appId, wls]) => {
    const app = apps.value.find((a) => a.id === appId)
    const reps = wls.reduce((s, w) => s + w.replicas, 0)
    const ready = wls.reduce((s, w) => s + w.ready, 0)
    const degraded = wls.some((w) => w.ready < w.replicas || w.status === 'failed')
    return { app, appId, count: wls.length, reps, ready, degraded }
  })
})

async function workHere() {
  if (!env.value) return
  // 切顶栏 scope（生产走 gated 确认）+ 跳工作负载操作面
  if (await envStore.switchEnv(env.value)) {
    router.push('/workloads/services')
  }
}

function openApp(appId: string) {
  router.push(`/applications/${appId}`)
}

onMounted(load)
// 同组件复用（URL 改 envId 或列表切不同环境）时 watch 路由参数刷新。
watch(() => route.params.id, load)
</script>

<template>
  <div class="page">
    <button class="back" @click="router.push('/environments')">
      <Icon name="chevron" :size="16" style="transform: rotate(90deg)" /> 返回环境列表
    </button>

    <div v-if="loading" class="skel-bar" />
    <template v-else-if="env">
      <header class="head" :class="{ prod: env.type === 'prod' }">
        <div class="e-icon" :class="env.type">
          <Icon :name="env.type === 'prod' ? 'shield' : 'server'" :size="22" />
        </div>
        <div class="head-info">
          <div class="name-row">
            <h2>{{ env.name }}</h2>
            <span class="type-badge" :class="env.type">{{ env.type === 'prod' ? '生产' : '测试' }}</span>
          </div>
          <div class="e-id mono">{{ env.id }} · 物理落点 {{ env.cluster || '默认' }}</div>
        </div>
        <button class="work-btn" @click="workHere">在此环境工作</button>
      </header>

      <section class="card">
        <h3 class="card-title">工作负载总览</h3>
        <div class="stat-row">
          <div v-for="t in typeMeta" :key="t.key" class="stat">
            <div class="stat-icon"><Icon :name="t.icon" :size="18" /></div>
            <div>
              <div class="stat-v mono">{{ (byType.get(t.key) ?? []).length }}</div>
              <div class="stat-k">{{ t.label }}</div>
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <h3 class="card-title">应用部署矩阵</h3>
        <el-table :data="appMatrix" size="small" empty-text="该环境尚无部署">
          <el-table-column label="应用" min-width="200">
            <template #default="{ row }">
              <div class="app-cell" @click="openApp(row.appId)">
                <div v-if="row.app" class="a-icon small" :style="{ background: row.app.gradient }">
                  {{ row.app.initial }}
                </div>
                <span>{{ row.app?.name ?? row.appId }}</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="工作负载" width="100">
            <template #default="{ row }"><span class="mono">{{ row.count }}</span></template>
          </el-table-column>
          <el-table-column label="副本就绪" width="120">
            <template #default="{ row }">
              <span class="mono" :class="{ warn: row.degraded }">{{ row.ready }}/{{ row.reps }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="row.degraded ? 'warning' : 'success'" size="small">
                {{ row.degraded ? '有异常' : '正常' }}
              </el-tag>
            </template>
          </el-table-column>
        </el-table>
      </section>
    </template>
    <div v-else class="empty">环境不存在或无权访问</div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 14px;
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
}
.back:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.skel-bar {
  height: 80px;
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2, #1a1f2e) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
.head {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 18px 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  margin-bottom: 16px;
}
.head.prod {
  border-color: var(--warning-soft, rgba(245, 158, 11, 0.3));
}
.e-icon {
  width: 48px;
  height: 48px;
  flex-shrink: 0;
  border-radius: 12px;
  display: grid;
  place-items: center;
  background: var(--brand-soft);
  color: var(--brand);
}
.e-icon.prod {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  color: var(--warning, #f59e0b);
}
.head-info {
  flex: 1;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.name-row h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}
.type-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.type-badge.prod {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  color: var(--warning, #f59e0b);
}
.type-badge.test {
  background: var(--surface-2, #1e2433);
  color: var(--text-dim);
}
.e-id {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 4px;
}
.work-btn {
  padding: 9px 16px;
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
.work-btn:hover {
  opacity: 0.9;
}
.card {
  padding: 18px 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  margin-bottom: 16px;
}
.card-title {
  margin: 0 0 14px;
  font-size: 14px;
  font-weight: 600;
}
.stat-row {
  display: flex;
  gap: 32px;
}
.stat {
  display: flex;
  align-items: center;
  gap: 10px;
}
.stat-icon {
  width: 38px;
  height: 38px;
  border-radius: 9px;
  display: grid;
  place-items: center;
  background: var(--brand-soft);
  color: var(--brand);
}
.stat-v {
  font-size: 18px;
  font-weight: 700;
}
.stat-k {
  font-size: 12px;
  color: var(--text-faint);
}
.app-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.app-cell:hover span {
  color: var(--brand);
}
.a-icon.small {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  display: grid;
  place-items: center;
  font-size: 12px;
  font-weight: 700;
  color: #fff;
}
.mono.warn {
  color: var(--warning);
}
.empty {
  padding: 60px 20px;
  text-align: center;
  color: var(--text-faint);
  font-size: 14px;
}
</style>
