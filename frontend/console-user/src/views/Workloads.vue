<script setup lang="ts">
// 工作负载视图：跨应用列表，按类型分 Tab（服务/Job/CronJob）。
// 数据来自 /api/workloads?type=；扩缩容 PUT、删除 DELETE。换 Key（租户）自动重载。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

interface Workload {
  id: string
  appId: string
  envId: string
  laneId: string
  type: 'service' | 'job' | 'cronjob'
  name: string
  image: string
  replicas: number
  ready: number
  status: 'running' | 'deploying' | 'failed' | 'succeeded' | 'pending'
  schedule?: string
}

const props = defineProps<{ type?: string }>()

const route = useRoute()
const envStore = useEnvStore()

const tabs = [
  { key: 'service', label: '服务', icon: 'server', desc: '长驻工作负载（Deployment 语义）' },
  { key: 'job', label: '任务', icon: 'job', desc: '一次性批处理' },
  { key: 'cronjob', label: '定时', icon: 'clock', desc: 'Cron 调度' },
] as const

const activeType = ref<string>(props.type || 'service')
// 环境来自全局 store（顶栏环境选择器），页面不再各自管理
const activeEnv = computed(() => envStore.currentEnvId)
const envs = computed(() => envStore.envs)
const items = ref<Workload[]>([])
const loading = ref(true)
const scaling = ref<string>('') // 正在扩缩容的 id

const statusMeta: Record<Workload['status'], { label: string; cls: string }> = {
  running: { label: '运行中', cls: 'ok' },
  deploying: { label: '部署中', cls: 'warn' },
  failed: { label: '异常', cls: 'err' },
  succeeded: { label: '已完成', cls: 'done' },
  pending: { label: '等待', cls: 'idle' },
}

const envName = computed(() => (id: string) => envs.value.find((e) => e.id === id)?.name ?? id)

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ type: activeType.value })
    if (activeEnv.value) params.set('envId', activeEnv.value)
    const resp = await fetchAuth(`/api/workloads?${params}`)
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const json = await resp.json()
    items.value = (json.data ?? []) as Workload[]
  } catch (e) {
    ElMessage.error('加载工作负载失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

async function scale(w: Workload) {
  try {
    const { value } = await ElMessageBox.prompt(`调整「${w.name}」的副本数`, '扩缩容', {
      confirmButtonText: '应用',
      cancelButtonText: '取消',
      inputValue: String(w.replicas),
      inputType: 'number',
    })
    const replicas = parseInt(value, 10)
    if (Number.isNaN(replicas) || replicas < 0) {
      ElMessage.warning('请输入有效副本数')
      return
    }
    scaling.value = w.id
    const resp = await fetchAuth(`/api/workloads/${w.id}`, {
      method: 'PUT',
      body: JSON.stringify({ replicas, status: 'running' }),
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    ElMessage.success(`已调整为 ${replicas} 副本`)
    await load()
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      ElMessage.error('扩缩容失败：' + (e as Error).message)
    }
  } finally {
    scaling.value = ''
  }
}

async function remove(w: Workload) {
  try {
    await ElMessageBox.confirm(`确定删除工作负载「${w.name}」吗？`, '删除', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
    const resp = await fetchAuth(`/api/workloads/${w.id}`, { method: 'DELETE' })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      ElMessage.error('删除失败：' + (e as Error).message)
    }
  }
}

function switchType(key: string) {
  activeType.value = key
  load()
}

async function switchEnv(id: string) {
  const env = id ? envStore.envs.find((e) => e.id === id) ?? null : null
  if (await envStore.switchEnv(env)) load()
}

function onKeyChanged() {
  envStore.loadEnvs()
  load()
}
function onEnvChanged() {
  load()
}
onMounted(() => {
  // 环境视图跳转携带 ?env= 预选环境
  const q = route.query.env as string
  if (q) {
    envStore.switchEnv(envStore.envs.find((e) => e.id === q) ?? null).then(() => load())
  } else {
    load()
  }
  window.addEventListener('paas:key-changed', onKeyChanged)
  window.addEventListener('paas:env-changed', onEnvChanged)
})
onUnmounted(() => {
  window.removeEventListener('paas:key-changed', onKeyChanged)
  window.removeEventListener('paas:env-changed', onEnvChanged)
})
</script>

<template>
  <div class="page">
    <div class="env-bar">
      <span class="env-label">环境</span>
      <button class="env-pill" :class="{ on: !activeEnv }" @click="switchEnv('')">全部</button>
      <button
        v-for="e in envs"
        :key="e.id"
        class="env-pill"
        :class="{ on: activeEnv === e.id, prod: e.type === 'prod' }"
        @click="switchEnv(e.id)"
      >
        {{ e.name }}
        <span v-if="e.cluster" class="env-cluster">{{ e.cluster }}</span>
      </button>
    </div>

    <div class="tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        class="tab"
        :class="{ on: activeType === t.key }"
        @click="switchType(t.key)"
      >
        <Icon :name="t.icon" :size="16" />
        <span>{{ t.label }}</span>
        <span class="tab-desc">{{ t.desc }}</span>
      </button>
    </div>

    <div v-if="loading" class="table-wrap">
      <div v-for="i in 4" :key="i" class="skel-row" />
    </div>

    <div v-else-if="items.length === 0" class="empty">
      <Icon name="server" :size="32" />
      <p>当前租户下暂无{{ tabs.find((t) => t.key === activeType)?.label }}工作负载</p>
    </div>

    <div v-else class="table-wrap">
      <table class="tbl">
        <thead>
          <tr>
            <th>名称</th>
            <th>归属应用</th>
            <th>环境</th>
            <th>镜像</th>
            <th v-if="activeType === 'cronjob'">调度</th>
            <th>副本</th>
            <th>状态</th>
            <th class="col-act"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in items" :key="w.id">
            <td>
              <div class="name-cell">
                <span class="name">{{ w.name }}</span>
                <span class="id mono">{{ w.id }}</span>
              </div>
            </td>
            <td class="mono app-id">{{ w.appId }}</td>
            <td class="env-cell">{{ envName(w.envId) }}</td>
            <td class="mono img">{{ w.image }}</td>
            <td v-if="activeType === 'cronjob'" class="mono sched">{{ w.schedule }}</td>
            <td>
              <span class="reps mono" :class="{ notready: w.ready < w.replicas }">
                {{ w.ready }}/{{ w.replicas }}
              </span>
            </td>
            <td>
              <span class="status" :class="statusMeta[w.status].cls">
                <span v-if="w.status === 'running'" class="pulse-dot" />
                {{ statusMeta[w.status].label }}
              </span>
            </td>
            <td class="col-act">
              <button class="act" :disabled="scaling === w.id || activeType === 'cronjob'" @click="scale(w)">
                扩缩容
              </button>
              <button class="act danger" @click="remove(w)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.env-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 14px;
  flex-wrap: wrap;
}
.env-label {
  font-size: 12px;
  color: var(--text-faint);
  margin-right: 4px;
}
.env-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--surface);
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.env-pill:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.env-pill.on {
  background: var(--brand-soft);
  border-color: var(--brand);
  color: var(--brand);
}
.env-pill.prod.on {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  border-color: var(--warning, #f59e0b);
  color: var(--warning, #f59e0b);
}
.env-cluster {
  font-size: 10px;
  opacity: 0.7;
  font-family: var(--mono, monospace);
}
.env-cell {
  font-size: 12px;
  color: var(--text-dim);
  white-space: nowrap;
}
.tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 18px;
}
.tab {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.12s;
}
.tab:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.tab.on {
  background: var(--brand-soft);
  border-color: var(--brand);
  color: var(--brand);
}
.tab-desc {
  font-size: 11px;
  color: var(--text-faint);
  font-weight: 400;
}
.table-wrap {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.tbl {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.tbl th {
  text-align: left;
  padding: 12px 16px;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-faint);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  border-bottom: 1px solid var(--border);
  background: var(--surface-2, transparent);
}
.tbl td {
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
  color: var(--text-dim);
}
.tbl tbody tr:last-child td {
  border-bottom: none;
}
.tbl tbody tr:hover td {
  background: var(--surface-2, rgba(255, 255, 255, 0.02));
}
.name-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.name {
  color: var(--text);
  font-weight: 600;
}
.id {
  font-size: 11px;
  color: var(--text-faint);
}
.app-id,
.img {
  color: var(--text-dim);
}
.sched {
  color: var(--brand);
}
.reps {
  font-weight: 600;
  color: var(--success);
}
.reps.notready {
  color: var(--warning);
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}
.status.ok {
  color: var(--success);
}
.status.warn {
  color: var(--warning);
}
.status.err {
  color: var(--danger, #f43f5e);
}
.status.done {
  color: var(--text-faint);
}
.status.idle {
  color: var(--text-faint);
}
.pulse-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 0 0 currentColor;
  animation: pulse 1.6s infinite;
}
@keyframes pulse {
  0% {
    box-shadow: 0 0 0 0 currentColor;
  }
  70% {
    box-shadow: 0 0 0 5px transparent;
  }
  100% {
    box-shadow: 0 0 0 0 transparent;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pulse-dot {
    animation: none;
  }
}
.col-act {
  text-align: right;
  white-space: nowrap;
}
.act {
  padding: 5px 12px;
  margin-left: 8px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.act:hover:not(:disabled) {
  border-color: var(--brand);
  color: var(--brand);
}
.act.danger:hover:not(:disabled) {
  border-color: var(--danger, #f43f5e);
  color: var(--danger, #f43f5e);
}
.act:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.skel-row {
  height: 56px;
  border-bottom: 1px solid var(--border);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2, #1a1f2e) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 60px 20px;
  color: var(--text-faint);
}
.empty p {
  margin: 0;
  font-size: 13px;
}
</style>
