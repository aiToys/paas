<script setup lang="ts">
// 应用列表（主线）。应用是逻辑跨环境实体，列表不按 scope 过滤；
// 每个应用卡片显示「在当前 scope 环境的部署徽标」（前端聚合应用 + 工作负载）。
// scope 全部时显示「部署在 N 个环境」。环境切换统一走顶栏，本页无环境控件。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { useUrlState } from '@/composables/useUrlState'

// 新建应用弹窗：POST /api/applications（name+env+desc，ID 后端生成 + ApplyDefaults 补展示）。
// 模板模式（旅程审计 R7）：POST /api/applications/from-template 一键建应用+仓库+服务+首轮 CI，
// 冷启动从 14 步降到 1 步；「空白应用」保持原路径（自定义仓库/多服务场景）。
const createVisible = ref(false)
const creating = ref(false)
const createForm = reactive({ name: '', desc: '' })
type TplOpt = { slug: ''; label: '空白应用'; hint: '仅创建应用，代码仓库和服务稍后配置' } | { slug: 'hello-web'; label: 'Hello Web'; hint: '静态页模板——建仓+seed 代码+首轮构建部署全自动' }
const TEMPLATES: TplOpt[] = [
  { slug: 'hello-web', label: 'Hello Web', hint: '静态页模板——建仓+seed 代码+首轮构建部署全自动' },
  { slug: '', label: '空白应用', hint: '仅创建应用，代码仓库和服务稍后配置' },
]
const createTpl = ref<string>('hello-web')

function openCreate() {
  createForm.name = ''
  createForm.desc = ''
  createTpl.value = 'hello-web'
  createVisible.value = true
}

async function createFromTemplate() {
  try {
    const resp = await fetchAuth('/api/applications/from-template', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        templateSlug: createTpl.value,
        name: createForm.name.trim(),
        desc: createForm.desc.trim(),
      }),
    })
    const body = await resp.json().catch(() => ({}))
    if (!resp.ok) {
      ElMessage.error('创建失败：' + (body.error || resp.statusText))
      return
    }
    createVisible.value = false
    const d = body.data || {}
    ElMessage.success('应用已创建，首轮构建部署已触发')
    if (d.runId) {
      router.push(`/devops/runs/${d.runId}`) // 直达运行页看构建部署进度
    } else {
      load()
    }
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    creating.value = false
  }
}

async function createApp() {
  if (!createForm.name.trim()) {
    ElMessage.warning('请输入应用名称')
    return
  }
  creating.value = true
  try {
    // 模板模式：一键端点聚合建应用+仓库+seed+服务+首轮 CI，直达运行页看进度。
    if (createTpl.value) {
      await createFromTemplate()
      return
    }
    // 应用是跨环境的逻辑实体（应用×环境多对多），创建时不绑定单一环境；
    // 环境归属由工作负载/绑定的 envId 决定（见环境模型）。
    const resp = await fetchAuth('/api/applications', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: createForm.name.trim(),
        desc: createForm.desc.trim(),
      }),
    })
    if (!resp.ok) {
      const e = await resp.json().catch(() => ({}))
      ElMessage.error('创建失败：' + (e.error || resp.statusText))
      return
    }
    ElMessage.success('应用创建成功')
    createVisible.value = false
    load()
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    creating.value = false
  }
}

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

// 应用名/ID 过滤（搜索词进 URL ?q=，分享链接带筛选）
const { value: searchQ } = useUrlState('q', '')
const filteredApps = computed(() => {
  const q = searchQ.value.toLowerCase().trim()
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

// statusMeta 只覆盖已知枚举值；后端若返回空串/未知值（历史脏数据、迁移残留）必须兜底，
// 否则 statusMeta[status] 为 undefined，模板访问 .cls 直接崩溃整个应用列表。
const STATUS_META: Record<string, { label: string; cls: string }> = {
  healthy: { label: '健康', cls: 'ok' },
  degraded: { label: '降级', cls: 'warn' },
  idle: { label: '空闲', cls: 'idle' },
}
const STATUS_UNKNOWN = { label: '未知', cls: 'idle' }
function statusOf(s: string): { label: string; cls: string } {
  return STATUS_META[s] ?? STATUS_UNKNOWN
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
      <input
        v-model="searchQ"
        class="search-input"
        placeholder="搜索应用名或 ID…"
      />
      <div class="right">
        <span class="count mono">{{ apps.length }} 个应用</span>
        <button class="new-btn" @click="openCreate">+ 新建应用</button>
      </div>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 6" :key="i" class="skel" />
    </div>
    <div v-else-if="filteredApps.length === 0" class="empty-state">
      <div class="empty-icon">🚀</div>
      <h3>暂无应用</h3>
      <p>创建你的第一个应用，开始管理模型路由、数据服务与部署。</p>
      <button class="new-btn" @click="openCreate">+ 新建应用</button>
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
          <span class="status" :class="statusOf(a.status).cls">
            <span v-if="a.status === 'healthy'" class="pulse-dot" />
            {{ statusOf(a.status).label }}
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

      <button class="add-card" @click="openCreate">
        <div class="add-icon">+</div>
        <div class="add-text">新建应用</div>
        <div class="add-hint">申请资源、部署服务</div>
      </button>
    </div>

    <!-- 新建应用弹窗 -->
    <el-dialog v-model="createVisible" title="新建应用" width="460px">
      <el-form label-position="top">
        <el-form-item label="从模板开始">
          <el-radio-group v-model="createTpl">
            <el-radio v-for="t in TEMPLATES" :key="t.slug" :value="t.slug">
              <div class="tpl-line">
                <span class="tpl-label">{{ t.label }}</span>
                <span class="tpl-hint">{{ t.hint }}</span>
              </div>
            </el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="应用名称" required>
          <el-input v-model="createForm.name" placeholder="如 智能客服" maxlength="32" show-word-limit />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="createForm.desc" type="textarea" :rows="3" placeholder="应用用途简述" maxlength="200" show-word-limit />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="createApp">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.tpl-line {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.tpl-label {
  font-size: 13px;
}
.tpl-hint {
  font-size: 11.5px;
  color: var(--text-dim);
}
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 20px;
  text-align: center;
  color: var(--text-faint);
}
.empty-state .empty-icon {
  font-size: 48px;
  margin-bottom: 16px;
}
.empty-state h3 {
  margin: 0 0 8px;
  color: var(--text);
  font-size: 18px;
}
.empty-state p {
  margin: 0 0 24px;
  font-size: 14px;
  max-width: 360px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 20px;
}
.search-input {
  flex: 0 1 280px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-card);
  color: var(--text);
  font-size: 13px;
  outline: none;
}
.search-input:focus {
  border-color: var(--brand);
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
