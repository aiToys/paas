<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'

const route = useRoute()
const router = useRouter()

interface BoundResource {
  name: string
  type: string
  typeKey: 'models' | 'mq' | 'dal'
  icon: string
  status: string
  detail: string
}

const app = computed(() => {
  const map: Record<string, any> = {
    'app-cs': { name: '智能客服', initial: '客', env: '生产', status: '健康', gradient: 'linear-gradient(135deg,#6366f1,#8b5cf6)', desc: '对话式客服，多模型路由 + 消息异步落库' },
  }
  return (
    map[route.params.id as string] ?? {
      name: '智能客服',
      initial: '客',
      env: '生产',
      status: '健康',
      gradient: 'linear-gradient(135deg,#6366f1,#8b5cf6)',
      desc: '对话式客服，多模型路由 + 消息异步落库',
    }
  )
})

const tabs = ['概览', '资源绑定', '部署', '日志', '监控'] as const
const activeTab = ref<(typeof tabs)[number]>('资源绑定')

const groups = computed(() => [
  {
    key: 'models' as const,
    label: '模型推理',
    icon: 'market',
    color: '#6366f1',
    items: [
      { name: 'qwen-cs-route', type: 'Qwen2.5-7B-Instruct', typeKey: 'models' as const, icon: 'market', status: '运行中', detail: '2 副本 · A100 ×2 · 1.2k tok/s' },
      { name: 'bge-recall', type: 'BGE-M3', typeKey: 'models' as const, icon: 'market', status: '运行中', detail: '4 副本 · L4 ×4 · 8.6k tok/s' },
    ],
  },
  {
    key: 'mq' as const,
    label: '消息队列',
    icon: 'message',
    color: '#10b981',
    items: [{ name: 'mq-order-events', type: 'Topic', typeKey: 'mq' as const, icon: 'message', status: '运行中', detail: '6 分区 · 320 msg/s' }],
  },
  {
    key: 'dal' as const,
    label: '数据访问层',
    icon: 'database',
    color: '#f59e0b',
    items: [{ name: 'cust-db', type: 'PostgreSQL 15', typeKey: 'dal' as const, icon: 'database', status: '运行中', detail: '主从 · 4C16G · 18% 连接' }],
  },
])

const showAdd = ref(false)
const addOptions = [
  { typeKey: 'models', label: '模型推理', icon: 'market', hint: '部署 LLM / Embedding 模型', color: '#6366f1' },
  { typeKey: 'mq', label: '消息队列', icon: 'message', hint: '创建 Topic / 申请 MQ 实例', color: '#10b981' },
  { typeKey: 'dal', label: '数据访问层', icon: 'database', hint: '接入数据源 / SQL 工作台', color: '#f59e0b' },
  { typeKey: 'gov', label: '服务治理', icon: 'service', hint: '注册发现 / 配置中心', color: '#ec4899' },
]

function pick(opt: (typeof addOptions)[0]) {
  showAdd.value = false
  ElMessage.success(`已为「${app.value.name}」发起新增 ${opt.label} 资源（演示）`)
}

const topo = computed(() => {
  const total = groups.value.reduce((s, g) => s + g.items.length, 0)
  return total
})
</script>

<template>
  <div class="detail">
    <button class="back" @click="router.push('/applications')">
      <Icon name="chevron" :size="16" style="transform: rotate(90deg)" /> 返回应用列表
    </button>

    <header class="head">
      <div class="a-icon" :style="{ background: app.gradient }">{{ app.initial }}</div>
      <div class="head-info">
        <div class="name-row">
          <h2>{{ app.name }}</h2>
          <span class="env">{{ app.env }}</span>
          <span class="health"><span class="pulse-dot" /> {{ app.status }}</span>
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
        <span v-if="t === '资源绑定'" class="tab-count mono">{{ topo }}</span>
      </button>
    </div>

    <!-- 资源绑定（核心） -->
    <div v-if="activeTab === '资源绑定'" class="bind-view">
      <div class="bind-head">
        <div class="bind-title">绑定的资源</div>
        <button class="add-btn" @click="showAdd = true">+ 绑定资源</button>
      </div>

      <section v-for="g in groups" :key="g.key" class="res-group">
        <div class="group-head">
          <Icon :name="g.icon" :size="16" :style="{ color: g.color }" />
          <span class="group-label">{{ g.label }}</span>
          <span class="group-count mono">{{ g.items.length }}</span>
        </div>
        <div class="group-items">
          <div v-for="it in g.items" :key="it.name" class="res-card">
            <div class="res-card-head">
              <span class="res-name mono">{{ it.name }}</span>
              <span class="res-status">{{ it.status }}</span>
            </div>
            <div class="res-type">{{ it.type }}</div>
            <div class="res-detail">{{ it.detail }}</div>
            <button class="unbind">解绑</button>
          </div>
        </div>
      </section>
    </div>

    <!-- 概览 -->
    <div v-else-if="activeTab === '概览'" class="overview">
      <div class="metrics">
        <div class="metric"><div class="m-v mono">1.2k</div><div class="m-k">请求/秒</div></div>
        <div class="metric"><div class="m-v mono">86ms</div><div class="m-k">P95 延迟</div></div>
        <div class="metric"><div class="m-v mono">0.2%</div><div class="m-k">错误率</div></div>
        <div class="metric"><div class="m-v mono">{{ topo }}</div><div class="m-k">绑定资源</div></div>
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
              <Icon :name="g.icon" :size="16" :style="{ color: g.color }" />
              <span>{{ g.label }}</span>
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

    <!-- 添加资源浮层 -->
    <Teleport to="body">
      <div v-if="showAdd" class="overlay" @click.self="showAdd = false">
        <div class="sheet">
          <div class="sheet-head">
            <h3>为「{{ app.name }}」绑定资源</h3>
            <button class="close" @click="showAdd = false">×</button>
          </div>
          <p class="sheet-sub">资源将归属该应用，随应用生命周期管理。选择资源类型开始申请：</p>
          <div class="opt-grid">
            <button v-for="o in addOptions" :key="o.typeKey" class="opt" @click="pick(o)">
              <div class="opt-icon" :style="{ background: o.color }"><Icon :name="o.icon" :size="18" /></div>
              <div class="opt-text">
                <div class="opt-label">{{ o.label }}</div>
                <div class="opt-hint">{{ o.hint }}</div>
              </div>
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
.back :deep(svg) {
  transform: rotate(90deg);
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

/* 添加资源浮层 */
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
  background: var(--brand-soft);
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
</style>
