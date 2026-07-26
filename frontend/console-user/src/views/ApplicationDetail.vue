<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'

const API_KEY = 'sk-paas-dev-key'

const route = useRoute()
const router = useRouter()

type TypeKey = 'models' | 'mq' | 'dal' | 'gov'

interface Binding {
  type: string
  name: string
  note?: string
}

interface App {
  id: string
  name: string
  initial: string
  env: string
  status: 'healthy' | 'degraded' | 'idle'
  gradient: string
  desc: string
  resources: { models: number; mq: number; dal: number }
  bindings: Binding[]
  replicas: string
  rps: string
}

const app = ref<App | null>(null)
const loading = ref(true)

// 资源类型元信息：图标 / 标签 / 主色。gov 不计入列表计数但仍可在详情绑定展示。
const typeMeta: Record<string, { label: string; icon: string; color: string }> = {
  models: { label: '模型推理', icon: 'market', color: '#6366f1' },
  mq: { label: '消息队列', icon: 'message', color: '#10b981' },
  dal: { label: '数据访问层', icon: 'database', color: '#f59e0b' },
  gov: { label: '服务治理', icon: 'service', color: '#ec4899' },
}

const statusLabel: Record<string, string> = {
  healthy: '健康',
  degraded: '降级',
  idle: '空闲',
}

// 按 type 分组绑定项；仅展示有绑定项的分组。
const groups = computed(() => {
  if (!app.value) return []
  const byType = new Map<string, Binding[]>()
  for (const b of app.value.bindings) {
    const arr = byType.get(b.type) ?? []
    arr.push(b)
    byType.set(b.type, arr)
  }
  // 固定顺序：models / mq / dal / gov
  const order = ['models', 'mq', 'dal', 'gov']
  return order
    .filter((t) => byType.has(t))
    .map((t) => ({ key: t, meta: typeMeta[t], items: byType.get(t)! }))
})

const totalBindings = computed(() => app.value?.bindings.length ?? 0)

async function load() {
  loading.value = true
  const id = route.params.id as string
  try {
    const resp = await fetch(`/api/applications/${id}`, {
      headers: { Authorization: `Bearer ${API_KEY}` },
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    app.value = (await resp.json()) as App
  } catch (e) {
    ElMessage.error('加载应用失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(load)

// —— 绑定资源浮层 ——
const showAdd = ref(false)
const form = ref<{ type: TypeKey; name: string }>({ type: 'models', name: '' })
const submitting = ref(false)

const addOptions: { typeKey: TypeKey; label: string; icon: string; hint: string; color: string }[] = [
  { typeKey: 'models', label: '模型推理', icon: 'market', hint: '部署 LLM / Embedding 模型', color: '#6366f1' },
  { typeKey: 'mq', label: '消息队列', icon: 'message', hint: '创建 Topic / 申请 MQ 实例', color: '#10b981' },
  { typeKey: 'dal', label: '数据访问层', icon: 'database', hint: '接入数据源 / SQL 工作台', color: '#f59e0b' },
  { typeKey: 'gov', label: '服务治理', icon: 'service', hint: '注册发现 / 配置中心', color: '#ec4899' },
]

function openAdd() {
  form.value = { type: 'models', name: '' }
  showAdd.value = true
}

async function submitBind() {
  if (!app.value) return
  const name = form.value.name.trim()
  if (!name) {
    ElMessage.warning('请填写资源名称')
    return
  }
  submitting.value = true
  try {
    const resp = await fetch(`/api/applications/${app.value.id}/bindings`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${API_KEY}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ type: form.value.type, name }),
    })
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}))
      throw new Error(err.error || `HTTP ${resp.status}`)
    }
    app.value = (await resp.json()) as App
    showAdd.value = false
    ElMessage.success(`已绑定 ${typeMeta[form.value.type].label}：${name}`)
  } catch (e) {
    ElMessage.error('绑定失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

async function unbind(b: Binding) {
  if (!app.value) return
  try {
    await ElMessageBox.confirm(
      `确定解绑「${b.name}」吗？该资源将从应用移除。`,
      '解绑资源',
      { type: 'warning', confirmButtonText: '解绑', cancelButtonText: '取消' },
    )
  } catch {
    return // 用户取消
  }
  try {
    const resp = await fetch(
      `/api/applications/${app.value.id}/bindings/${b.type}/${encodeURIComponent(b.name)}`,
      { method: 'DELETE', headers: { Authorization: `Bearer ${API_KEY}` } },
    )
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}))
      throw new Error(err.error || `HTTP ${resp.status}`)
    }
    app.value = (await resp.json()) as App
    ElMessage.success(`已解绑：${b.name}`)
  } catch (e) {
    ElMessage.error('解绑失败：' + (e as Error).message)
  }
}

const tabs = ['概览', '资源绑定', '部署', '日志', '监控'] as const
const activeTab = ref<(typeof tabs)[number]>('资源绑定')
</script>

<template>
  <div class="detail">
    <button class="back" @click="router.push('/applications')">
      <Icon name="chevron" :size="16" style="transform: rotate(90deg)" /> 返回应用列表
    </button>

    <div v-if="loading" class="head skel-bar" />
    <template v-else-if="app">
      <header class="head">
        <div class="a-icon" :style="{ background: app.gradient }">{{ app.initial }}</div>
        <div class="head-info">
          <div class="name-row">
            <h2>{{ app.name }}</h2>
            <span class="env">{{ app.env }}</span>
            <span class="health"><span class="pulse-dot" /> {{ statusLabel[app.status] ?? app.status }}</span>
          </div>
          <p class="desc">{{ app.desc }}</p>
        </div>
        <div class="head-actions">
          <button class="ghost">设置</button>
          <button class="primary">部署</button>
        </div>
      </header>

      <div class="tabs">
        <button v-for="t in tabs" :key="t" class="tab" :class="{ on: activeTab === t }" @click="activeTab = t">
          {{ t }}
          <span v-if="t === '资源绑定'" class="tab-count mono">{{ totalBindings }}</span>
        </button>
      </div>

      <!-- 资源绑定（核心） -->
      <div v-if="activeTab === '资源绑定'" class="bind-view">
        <div class="bind-head">
          <div class="bind-title">绑定的资源</div>
          <button class="add-btn" @click="openAdd">+ 绑定资源</button>
        </div>

        <div v-if="!groups.length" class="empty">
          <Icon name="rocket" :size="28" />
          <p>该应用尚未绑定任何资源</p>
          <button class="add-btn" @click="openAdd">绑定第一个资源</button>
        </div>

        <section v-for="g in groups" :key="g.key" class="res-group">
          <div class="group-head">
            <Icon :name="g.meta.icon" :size="16" :style="{ color: g.meta.color }" />
            <span class="group-label">{{ g.meta.label }}</span>
            <span class="group-count mono">{{ g.items.length }}</span>
          </div>
          <div class="group-items">
            <div v-for="it in g.items" :key="it.name" class="res-card">
              <div class="res-card-head">
                <span class="res-name mono">{{ it.name }}</span>
                <span class="res-status">已绑定</span>
              </div>
              <div class="res-type">{{ g.meta.label }}</div>
              <div v-if="it.note" class="res-detail">{{ it.note }}</div>
              <button class="unbind" @click="unbind(it)">解绑</button>
            </div>
          </div>
        </section>
      </div>

      <!-- 概览 -->
      <div v-else-if="activeTab === '概览'" class="overview">
        <div class="metrics">
          <div class="metric"><div class="m-v mono">{{ app.rps }}</div><div class="m-k">请求/秒</div></div>
          <div class="metric"><div class="m-v mono">{{ app.replicas }}</div><div class="m-k">副本</div></div>
          <div class="metric"><div class="m-v mono">{{ app.resources.models + app.resources.mq + app.resources.dal }}</div><div class="m-k">绑定资源</div></div>
          <div class="metric"><div class="m-v mono">{{ app.env }}</div><div class="m-k">环境</div></div>
        </div>
        <div class="topo-card">
          <div class="chart-title">资源依赖拓扑</div>
          <div class="topo-graph">
            <div class="topo-app">
              <div class="a-icon small" :style="{ background: app.gradient }">{{ app.initial }}</div>
              <span>{{ app.name }}</span>
            </div>
            <div class="topo-links">
              <div v-for="g in groups" :key="g.key" class="topo-res">
                <Icon :name="g.meta.icon" :size="16" :style="{ color: g.meta.color }" />
                <span>{{ g.meta.label }}</span>
                <span class="topo-n mono">{{ g.items.length }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 占位 Tab -->
      <div v-else class="placeholder">
        <div class="ph-card">
          <Icon name="rocket" :size="32" />
          <p>{{ activeTab }} 视图开发中</p>
        </div>
      </div>
    </template>

    <!-- 绑定资源浮层 -->
    <Teleport to="body">
      <div v-if="showAdd" class="overlay" @click.self="showAdd = false">
        <div class="sheet">
          <div class="sheet-head">
            <h3>为「{{ app?.name }}」绑定资源</h3>
            <button class="close" @click="showAdd = false">×</button>
          </div>
          <p class="sheet-sub">资源将归属该应用，随应用生命周期管理。选择类型并命名：</p>

          <div class="field-label">资源类型</div>
          <div class="opt-grid">
            <button
              v-for="o in addOptions"
              :key="o.typeKey"
              class="opt"
              :class="{ on: form.type === o.typeKey }"
              @click="form.type = o.typeKey"
            >
              <div class="opt-icon" :style="{ background: o.color }"><Icon :name="o.icon" :size="18" /></div>
              <div class="opt-text">
                <div class="opt-label">{{ o.label }}</div>
                <div class="opt-hint">{{ o.hint }}</div>
              </div>
            </button>
          </div>

          <div class="field-label">资源名称</div>
          <input
            v-model="form.name"
            class="name-input"
            placeholder="如 qwen-cs-route、mq-order-events"
            @keyup.enter="submitBind"
          />

          <div class="sheet-foot">
            <button class="ghost" @click="showAdd = false">取消</button>
            <button class="primary" :disabled="submitting" @click="submitBind">
              {{ submitting ? '绑定中…' : '确认绑定' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.detail {
  max-width: 1100px;
  margin: 0 auto;
}
.back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 16px;
  border: none;
  background: transparent;
  color: var(--text-faint);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
  transition: color 0.12s;
}
.back:hover {
  color: var(--text);
}

.head {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 22px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  margin-bottom: 20px;
}
.skel-bar {
  height: 96px;
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .skel-bar {
    animation: none;
  }
}
.a-icon {
  width: 52px;
  height: 52px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 20px;
  color: #fff;
  flex-shrink: 0;
}
.a-icon.small {
  width: 32px;
  height: 32px;
  font-size: 14px;
  border-radius: 8px;
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
  letter-spacing: -0.01em;
}
.env {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--success-soft);
  color: var(--success);
}
.health {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--success);
}
.desc {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--text-dim);
}
.head-actions {
  display: flex;
  gap: 8px;
}
.ghost,
.primary {
  padding: 8px 16px;
  border-radius: var(--radius);
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.12s;
}
.ghost {
  border: 1px solid var(--border-strong);
  background: transparent;
  color: var(--text);
}
.ghost:hover {
  background: var(--surface-2);
}
.primary {
  border: none;
  background: var(--brand);
  color: #fff;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
  box-shadow: none;
}

.tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 20px;
}
.tab {
  position: relative;
  padding: 10px 16px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13.5px;
  font-weight: 500;
  cursor: pointer;
  transition: color 0.12s;
}
.tab:hover {
  color: var(--text);
}
.tab.on {
  color: var(--text);
}
.tab.on::after {
  content: '';
  position: absolute;
  left: 16px;
  right: 16px;
  bottom: -1px;
  height: 2px;
  background: var(--brand);
  border-radius: 2px;
}
.tab-count {
  margin-left: 4px;
  font-size: 11px;
  color: var(--text-faint);
}

/* 资源绑定 */
.bind-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.bind-title {
  font-size: 14px;
  font-weight: 600;
}
.add-btn {
  padding: 7px 14px;
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
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 0;
  color: var(--text-faint);
  text-align: center;
}
.empty :deep(svg) {
  color: var(--brand);
}
.empty p {
  margin: 0;
  font-size: 13px;
}
.res-group {
  margin-bottom: 20px;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.group-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-dim);
}
.group-count {
  font-size: 11px;
  color: var(--text-faint);
}
.group-items {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.res-card {
  position: relative;
  padding: 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  transition: border-color 0.12s;
}
.res-card:hover {
  border-color: var(--border-strong);
}
.res-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.res-name {
  font-size: 13px;
  font-weight: 600;
}
.res-status {
  font-size: 11px;
  color: var(--success);
}
.res-type {
  font-size: 12px;
  color: var(--text-dim);
  margin-top: 4px;
}
.res-detail {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}
.unbind {
  position: absolute;
  top: 12px;
  right: 12px;
  display: none;
  padding: 3px 8px;
  border: 1px solid var(--danger);
  border-radius: 5px;
  background: var(--danger-soft);
  color: var(--danger);
  font-family: inherit;
  font-size: 11px;
  cursor: pointer;
}
.res-card:hover .unbind {
  display: block;
}
.res-card:hover .res-status {
  display: none;
}

/* 概览 */
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
.topo-card {
  padding: 20px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.chart-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 18px;
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
}
.topo-n {
  margin-left: 2px;
  font-weight: 600;
  color: var(--text);
}

.placeholder {
  padding: 60px 0;
}
.ph-card {
  text-align: center;
  padding: 40px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  color: var(--text-faint);
}
.ph-card :deep(svg) {
  color: var(--brand);
  margin-bottom: 8px;
}

/* 绑定资源浮层 */
.overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  backdrop-filter: blur(4px);
  display: grid;
  place-items: center;
  z-index: 100;
}
.sheet {
  width: 520px;
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
  padding: 24px;
  box-shadow: var(--shadow);
}
.sheet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.sheet-head h3 {
  margin: 0;
  font-size: 16px;
}
.close {
  border: none;
  background: transparent;
  color: var(--text-faint);
  font-size: 22px;
  cursor: pointer;
  line-height: 1;
}
.sheet-sub {
  font-size: 13px;
  color: var(--text-dim);
  margin: 6px 0 18px;
}
.field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-dim);
  margin: 8px 0 8px;
}
.opt-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.opt {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  font-family: inherit;
  text-align: left;
  transition: all 0.12s;
}
.opt:hover {
  border-color: var(--brand);
}
.opt.on {
  border-color: var(--brand);
  background: var(--brand-soft);
  box-shadow: 0 0 0 1px var(--brand) inset;
}
.opt-icon {
  width: 38px;
  height: 38px;
  border-radius: 9px;
  display: grid;
  place-items: center;
  color: #fff;
  flex-shrink: 0;
}
.opt-label {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--text);
}
.opt-hint {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 2px;
}
.name-input {
  width: 100%;
  box-sizing: border-box;
  padding: 10px 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  font-family: 'JetBrains Mono', monospace;
  font-size: 13px;
  outline: none;
  transition: border-color 0.12s;
}
.name-input:focus {
  border-color: var(--brand);
}
.name-input::placeholder {
  color: var(--text-faint);
}
.sheet-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
</style>
