<script setup lang="ts">
// 环境详情页（管理面）：单环境的深度视图。
// 工作负载总览（按类型）+ 应用部署矩阵（工作负载反推 appID + 应用信息）。
// 「在此环境工作」= 切顶栏 scope（操作面）+ 跳工作负载，桥接管理面 -> 操作面。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { listLanes, createLane, closeLane, type Lane as LaneEntity } from '@/api/lane'
import { confirmDangerous } from '@/composables/useDangerConfirm'
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
  laneId?: string
}
interface App {
  id: string
  name: string
  gradient: string
  initial: string
}
interface DataService {
  id: string
  kind: string
  name: string
  status: string
  envId: string
}

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
const env = ref<Env | null>(null)
const workloads = ref<Workload[]>([])
const apps = ref<App[]>([])
const dataServices = ref<DataService[]>([])
const loading = ref(true)

const typeMeta = [
  { key: 'service', label: '服务', icon: 'server', path: 'services' },
  { key: 'job', label: '任务', icon: 'job', path: 'jobs' },
  { key: 'cronjob', label: '定时', icon: 'clock', path: 'cronjobs' },
]

// 数据服务 kind 中文标签（与 DataServices 模块对齐）。
const KIND_LABEL: Record<string, string> = {
  db: '数据库', cache: '缓存', mq: '消息队列', storage: '对象存储', vector: '向量数据库', search: '搜索引擎',
}

// —— 泳道实体管理（一等实体：显式创建/关闭，workload 反推的泳道仅是未实体化的裸分支）——
const lanes = ref<LaneEntity[]>([])
const laneDlg = ref(false)
const laneForm = ref<{ name: string; mode: 'standard' | 'permanent'; description: string }>({ name: '', mode: 'standard', description: '' })

async function loadLanes() {
  try {
    lanes.value = await listLanes(route.params.id as string)
  } catch { /* 后端不可用降级为空，矩阵仍按 workload 反推 */ }
}

function openLaneManager() {
  laneDlg.value = true
  loadLanes()
}

// 列头点入泳道详情：优先实体（name 精确匹配）；裸分支（无实体）懒建实体再进详情
// （与 deploy EnsureByName 同语义）。无写权限（viewer）时创建会 403，降级提示。
async function goLane(name: string) {
  await loadLanes()
  const hit = lanes.value.find((l) => l.name === name && l.status === 'active')
  if (hit) {
    router.push(`/lanes/${hit.id}`)
  } else {
    try {
      const created = await createLane({ envId: route.params.id as string, name, mode: 'standard' })
      router.push(`/lanes/${created.id}`)
    } catch (e) {
      ElMessage.error(`该泳道尚未实体化，实体化失败（可能无写权限）：${(e as Error).message}`)
    }
  }
}

async function onCreateLane() {
  if (!laneForm.value.name) { ElMessage.warning('泳道名必填（小写字母数字与 -）'); return }
  try {
    await createLane({ envId: route.params.id as string, ...laneForm.value })
    ElMessage.success('泳道已创建')
    laneForm.value = { name: '', mode: 'standard', description: '' }
    await loadLanes()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

async function onCloseLane(l: LaneEntity) {
  const ok = await confirmDangerous({ action: '关闭泳道', target: l.name, requireNameConfirm: true, isProd: env.value?.type === 'prod' })
  if (!ok) return
  try {
    await closeLane(l.id)
    ElMessage.success('泳道已关闭')
    await loadLanes()
  } catch (e) {
    ElMessage.error((e as Error).message)
  }
}

async function load() {
  loading.value = true
  try {
    const id = route.params.id as string
    const [eResp, wResp, aResp, dResp] = await Promise.all([
      fetchAuth('/api/environments'),
      fetchAuth(`/api/workloads?envId=${id}`),
      fetchAuth('/api/applications'),
      fetchAuth('/api/dataservices'),
    ])
    loadLanes() // 非阻塞：泳道实体列表独立加载（失败降级空）
    if (eResp.ok) {
      const all = ((await eResp.json()).data ?? []) as Env[]
      env.value = all.find((e) => e.id === id) ?? null
    }
    if (wResp.ok) workloads.value = (await wResp.json()).data ?? []
    if (aResp.ok) apps.value = (await aResp.json()).data ?? []
    if (dResp.ok) {
      const all = ((await dResp.json()).data ?? []) as DataService[]
      dataServices.value = all.filter((d) => d.envId === id)
    }
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

// 应用部署矩阵（应用 × 泳道）：工作负载反推 appID + 应用信息。
// 列 = 本环境全部泳道（default 固定第一列 + feature 按名排序）；格 = 就绪比 + 服务数。
const appMatrix = computed(() => {
  const byApp = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    const arr = byApp.get(w.appId) ?? []
    arr.push(w)
    byApp.set(w.appId, arr)
  }
  return [...byApp.entries()].map(([appId, wls]) => {
    const app = apps.value.find((a) => a.id === appId)
    return { app, appId, count: wls.length }
  })
})

// 泳道列集合：default 固定第一列，feature 按名排序。
const laneCols = computed(() => {
  const lanes = new Set(workloads.value.map((w) => w.laneId || 'default'))
  lanes.add('default')
  return [...lanes].sort((a, b) => (a === 'default' ? -1 : b === 'default' ? 1 : a.localeCompare(b)))
})

// 泳道矩阵：map[appId][lane] -> { ready, replicas, count }
const laneMatrix = computed(() => {
  const m: Record<string, Record<string, { ready: number; replicas: number; count: number }>> = {}
  for (const w of workloads.value) {
    const lane = w.laneId || 'default'
    m[w.appId] ??= {}
    const cell = m[w.appId][lane] ??= { ready: 0, replicas: 0, count: 0 }
    cell.ready += w.ready || 0
    cell.replicas += w.replicas || 0
    cell.count++
  }
  return m
})

async function workHere() {
  if (!env.value) return
  // 切顶栏 scope（生产走 gated 确认）+ 跳工作负载操作面
  if (await envStore.switchEnv(env.value)) {
    router.push('/workloads/services')
  }
}

// 工作负载总览 stat 卡 → 切 scope + 跳对应类型工作负载列表（覆盖 jobs/cronjobs 孤岛）。
async function goWorkloads(path: string) {
  if (!env.value) return
  if (await envStore.switchEnv(env.value)) {
    router.push(`/workloads/${path}`)
  }
}

function openDS(kind: string, id: string) {
  router.push(`/resources/${kind}/${id}`)
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
          <div v-for="t in typeMeta" :key="t.key" class="stat clickable" @click="goWorkloads(t.path)">
            <div class="stat-icon"><Icon :name="t.icon" :size="18" /></div>
            <div>
              <div class="stat-v mono">{{ (byType.get(t.key) ?? []).length }}</div>
              <div class="stat-k">{{ t.label }}</div>
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <h3 class="card-title">环境内数据服务</h3>
        <el-table :data="dataServices" size="small" empty-text="该环境尚无数据服务">
          <el-table-column label="名称" min-width="180">
            <template #default="{ row }">
              <a class="link" @click="openDS(row.kind, row.id)">{{ row.name }}</a>
            </template>
          </el-table-column>
          <el-table-column label="类型" width="120">
            <template #default="{ row }">{{ KIND_LABEL[row.kind] ?? row.kind }}</template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="row.status === 'running' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </section>

      <section class="card">
        <h3 class="card-title">
          应用部署矩阵（应用 × 泳道）
          <el-button size="small" style="float: right" @click="openLaneManager">管理泳道</el-button>
        </h3>
        <el-table :data="appMatrix" size="small" empty-text="该环境尚无部署">
          <el-table-column label="应用" min-width="200" fixed="left">
            <template #default="{ row }">
              <div class="app-cell" @click="openApp(row.appId)">
                <div v-if="row.app" class="a-icon small" :style="{ background: row.app.gradient }">
                  {{ row.app.initial }}
                </div>
                <span>{{ row.app?.name ?? row.appId }}</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column
            v-for="lane in laneCols"
            :key="lane"
            :label="lane === 'default' ? '基线' : `泳道 ${lane}`"
            min-width="150"
          >
            <template #header>
              <span
                :class="{ 'lane-header-feature': lane !== 'default' }"
                :style="lane !== 'default' ? 'cursor: pointer' : ''"
                @click="lane !== 'default' && goLane(lane)"
              >
                {{ lane === 'default' ? '基线' : `泳道 ${lane}` }}
              </span>
            </template>
            <template #default="{ row }">
              <template v-if="laneMatrix[row.appId]?.[lane]">
                <div class="lane-cell">
                  <span
                    class="mono"
                    :class="{ warn: laneMatrix[row.appId][lane].ready < laneMatrix[row.appId][lane].replicas }"
                  >{{ laneMatrix[row.appId][lane].ready }}/{{ laneMatrix[row.appId][lane].replicas }}</span>
                  <span class="lane-svc-count">{{ laneMatrix[row.appId][lane].count }} 服务</span>
                </div>
              </template>
              <span v-else-if="lane !== 'default'" class="lane-fallback">↩ 基线</span>
            </template>
          </el-table-column>
        </el-table>
      </section>
    </template>
    <div v-else class="empty">环境不存在或无权访问</div>

    <!-- 泳道实体管理：显式创建（standard/permanent）+ 关闭（同步回收工作负载） -->
    <el-dialog v-model="laneDlg" title="管理泳道" width="640px">
      <el-form inline @submit.prevent="onCreateLane">
        <el-form-item label="泳道名">
          <el-input v-model="laneForm.name" placeholder="如 feature-pay（小写字母数字-）" style="width: 200px" />
        </el-form-item>
        <el-form-item label="模式">
          <el-select v-model="laneForm.mode" style="width: 140px">
            <el-option label="常规（闲置可回收）" value="standard" />
            <el-option label="常驻（GC 不回收）" value="permanent" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="onCreateLane">创建</el-button>
        </el-form-item>
      </el-form>
      <el-table :data="lanes" size="small" empty-text="暂无实体化泳道（矩阵中的泳道为裸分支，点列头可实体化）">
        <el-table-column prop="name" label="泳道" min-width="140">
          <template #default="{ row }">
            <router-link :to="`/lanes/${row.id}`" style="color: var(--el-color-primary); text-decoration: none">
              {{ row.name }}
            </router-link>
          </template>
        </el-table-column>
        <el-table-column prop="mode" label="模式" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="row.mode === 'permanent' ? 'warning' : 'info'">
              {{ row.mode === 'permanent' ? '常驻' : '常规' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="status" label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="说明" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button v-if="row.status === 'active'" size="small" type="danger" link @click="onCloseLane(row)">
              关闭
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
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
.stat.clickable {
  cursor: pointer;
  transition: background 0.12s;
}
.stat.clickable:hover {
  background: var(--surface-2);
}
.link {
  color: var(--brand);
  cursor: pointer;
}
.link:hover {
  text-decoration: underline;
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
.lane-header-feature {
  color: var(--el-color-warning);
}
.lane-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.lane-svc-count {
  font-size: 12px;
  color: var(--text-faint);
}
.lane-fallback {
  color: var(--text-faint);
  font-size: 12px;
}
</style>
