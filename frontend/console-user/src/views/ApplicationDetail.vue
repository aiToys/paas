<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth, fetchJSON } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import AppRepositories from './app-tabs/AppRepositories.vue'
import AppPipelines from './app-tabs/AppPipelines.vue'
import AppChanges from './app-tabs/AppChanges.vue'
import AppBuilds from './app-tabs/AppBuilds.vue'
import AppImages from './app-tabs/AppImages.vue'
import AppReleases from './app-tabs/AppReleases.vue'
import AppConfigs from './app-tabs/AppConfigs.vue'
import AppGovernance from './app-tabs/AppGovernance.vue'
import AppObservability from './app-tabs/AppObservability.vue'
import AppUsage from './app-tabs/AppUsage.vue'

const route = useRoute()
const router = useRouter()

type TypeKey = 'models' | 'db' | 'cache' | 'mq' | 'storage' | 'vector' | 'search' | 'dal' | 'gov'

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

// 资源类型元信息：图标 / 标签 / 主色。
// 资源中心 = 数据服务；dal 兼容历史 seed，gov 兼容「应用接入治理」语义。
const typeMeta: Record<string, { label: string; icon: string; color: string }> = {
  models: { label: '模型推理', icon: 'market', color: '#6366f1' },
  db: { label: '数据库', icon: 'database', color: '#f59e0b' },
  cache: { label: '缓存', icon: 'zap', color: '#ef4444' },
  mq: { label: '消息队列', icon: 'message', color: '#10b981' },
  storage: { label: '对象存储', icon: 'storage', color: '#0ea5e9' },
  vector: { label: '向量数据库', icon: 'layers', color: '#8b5cf6' },
  search: { label: '搜索引擎', icon: 'search', color: '#06b6d4' },
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
  for (const b of app.value.bindings ?? []) {
    const arr = byType.get(b.type) ?? []
    arr.push(b)
    byType.set(b.type, arr)
  }
  // 固定顺序：数据服务全集
  const order = ['models', 'db', 'cache', 'mq', 'storage', 'vector', 'search', 'dal', 'gov']
  return order
    .filter((t) => byType.has(t))
    .map((t) => ({ key: t, meta: typeMeta[t], items: byType.get(t)! }))
})

const totalBindings = computed(() => app.value?.bindings?.length ?? 0)

// 概览真实聚合：副本就绪比 + sparkline（去 seed 假数据 rps/replicas）。
const replicaStat = computed(() => {
  const total = workloads.value.reduce((s, w) => s + w.replicas, 0)
  const ready = workloads.value.reduce((s, w) => s + w.ready, 0)
  return { ready, total }
})

// 访问入口：聚合对外暴露的工作负载（domain 非空 → reconciler 自动建 Ingress）。
// 暴露该应用的对外访问地址，让用户在概览一眼看到「怎么访问这个应用」。
interface AccessEntry { workload: string; domain: string; port?: number }
const accessEntries = computed<AccessEntry[]>(() =>
  workloads.value
    .filter((w) => w.type === 'service' && w.domain)
    .map((w) => ({ workload: w.name, domain: w.domain!, port: w.port }))
)
interface MetricPoint { ts: string; value: number }
interface MetricSeries { name: string; unit: string; current: number; points: MetricPoint[] }
function sparkHeights(points?: MetricPoint[]): number[] {
  if (!points || points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}
const cpuSeries = computed(() => metrics.value.find((m) => m.name === 'cpu'))
const rpsSeries = computed(() => metrics.value.find((m) => m.name === 'rps'))

interface Workload {
  id: string
  envId: string
  laneId: string
  type: string
  name: string
  image: string
  replicas: number
  ready: number
  status: string
  schedule?: string
  port?: number
  domain?: string
}
interface Env { id: string; name: string; type: string }
const workloads = ref<Workload[]>([])
const envs = ref<Env[]>([])

// 概览工作台：真实运行态指标 + 最新发布/构建（去 seed 假数据 rps/replicas）。
const metrics = ref<MetricSeries[]>([])
interface Release { id: string; status: string; envId: string; createdAt: string }
interface Build { id: string; status: string; startedAt: string }
const latestRelease = ref<Release | null>(null)
const latestBuild = ref<Build | null>(null)

// 部署 tab：按环境分组（基线 default 不单显，归到所属环境）
const workloadsByEnv = computed(() => {
  const groups = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    if (!groups.has(w.envId)) groups.set(w.envId, [])
    groups.get(w.envId)!.push(w)
  }
  return [...groups.entries()].map(([envId, items]) => ({
    envId,
    name: envs.value.find((e) => e.id === envId)?.name ?? envId,
    type: envs.value.find((e) => e.id === envId)?.type ?? '',
    items,
  }))
})

async function load() {
  loading.value = true
  const id = route.params.id as string
  try {
    // fetchJSON 自动解包 {data:T} 契约，杜绝手动 json.data ?? json 的契约遗漏。
    app.value = await fetchJSON<App>(`/api/applications/${id}`)
    // 并行加载该应用的工作负载（部署 tab）与环境（分组映射）；任一失败不阻塞主信息。
    const [ws, es, mt, rels, blds] = await Promise.allSettled([
      fetchJSON<Workload[]>(`/api/applications/${id}/workloads`),
      fetchJSON<Env[]>('/api/environments'),
      fetchJSON<MetricSeries[]>(`/api/observability/metrics?targetType=app&targetId=${id}`),
      fetchJSON<Release[]>(`/api/applications/${id}/releases`),
      fetchJSON<Build[]>(`/api/applications/${id}/buildruns`),
    ])
    workloads.value = ws.status === 'fulfilled' ? ws.value : []
    envs.value = es.status === 'fulfilled' ? es.value : []
    metrics.value = mt.status === 'fulfilled' ? mt.value : []
    latestRelease.value = rels.status === 'fulfilled' && rels.value.length ? rels.value[0] : null
    latestBuild.value = blds.status === 'fulfilled' && blds.value.length ? blds.value[0] : null
  } catch (e) {
    ElMessage.error('加载应用失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(load)
// 同组件复用（列表切不同应用详情）时 watch 路由参数刷新，避免显示上一张应用陈旧数据。
watch(() => route.params.id, load)

// —— 绑定资源浮层 ——
const showAdd = ref(false)
const form = ref<{ type: TypeKey; name: string }>({ type: 'models', name: '' })
const submitting = ref(false)

// 数据服务 kind 集合：绑定后后端自动注入连接信息到 appconfig；前端据此提示 + placeholder。
const DS_KINDS: TypeKey[] = ['db', 'cache', 'mq', 'storage', 'vector', 'search']
// 数据服务类型支持名称或 ID（后端 resolveDS 容错）；其他类型填名称。
const namePlaceholder = computed(() =>
  DS_KINDS.includes(form.value.type)
    ? '数据服务名称或 ID（如 acme-orders-db 或 ds-acme-db）'
    : '如 qwen-cs-route、mq-order-events',
)

const addOptions: { typeKey: TypeKey; label: string; icon: string; hint: string; color: string }[] = [
  { typeKey: 'models', label: '模型推理', icon: 'market', hint: '部署 LLM / Embedding 模型', color: '#6366f1' },
  { typeKey: 'db', label: '数据库', icon: 'database', hint: 'PostgreSQL / MySQL 实例', color: '#f59e0b' },
  { typeKey: 'cache', label: '缓存', icon: 'zap', hint: 'Redis 实例与集群', color: '#ef4444' },
  { typeKey: 'mq', label: '消息队列', icon: 'message', hint: '创建 Topic / 申请 MQ 实例', color: '#10b981' },
  { typeKey: 'storage', label: '对象存储', icon: 'storage', hint: 'Bucket / CDN / 生命周期', color: '#0ea5e9' },
  { typeKey: 'vector', label: '向量数据库', icon: 'layers', hint: '索引 / 检索 / Embedding', color: '#8b5cf6' },
  { typeKey: 'search', label: '搜索引擎', icon: 'search', hint: 'Elasticsearch / OpenSearch', color: '#06b6d4' },
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
    app.value = await fetchJSON<App>(`/api/applications/${app.value.id}/bindings`, {
      method: 'POST',
      body: JSON.stringify({ type: form.value.type, name }),
    })
    showAdd.value = false
    // 数据服务绑定时后端 best-effort 注入连接信息（OnBind 失败仅 log 不阻断绑定）；
    // 故文案用「将注入」不断言「已注入」，引导用户去「配置」tab 验证。
    if (DS_KINDS.includes(form.value.type)) {
      ElMessage.success(`已绑定 ${typeMeta[form.value.type].label}：${name}（连接信息将注入应用配置，可在「配置」tab 查看）`)
    } else {
      ElMessage.success(`已绑定 ${typeMeta[form.value.type].label}：${name}`)
    }
  } catch (e) {
    ElMessage.error('绑定失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

async function unbind(b: Binding) {
  if (!app.value) return
  // 解绑属高危操作（数据服务解绑会清除 DATABASE_URL/REDIS_URL 等连接注入，影响线上 Pod）：
  // 统一走 confirmDangerous，生产环境要求输入绑定名称确认（与其他资源删除一致）。
  const isDsUnbind = DS_KINDS.includes(b.type as TypeKey)
  const ok = await confirmDangerous({
    action: '解绑',
    target: b.name,
    requireNameConfirm: true,
  })
  if (!ok) return
  try {
    app.value = await fetchJSON<App>(
      `/api/applications/${app.value.id}/bindings/${b.type}/${encodeURIComponent(b.name)}`,
      { method: 'DELETE' },
    )
    ElMessage.success(`已解绑：${b.name}` + (isDsUnbind ? '（连接信息将同步清除）' : ''))
  } catch (e) {
    ElMessage.error('解绑失败：' + (e as Error).message)
  }
}

// tab 视觉分组（运行态/资源/DevOps）：防 10 tab 平铺膨胀。
const tabGroups = [
  { label: '运行态', tabs: ['概览', '部署', '服务治理', '可观测'] as const },
  { label: '资源', tabs: ['资源绑定', '配置', '用量'] as const },
  { label: 'DevOps', tabs: ['流水线', '变更', '代码仓库', '构建', '镜像', '发布'] as const },
]
type TabName = '概览' | '部署' | '服务治理' | '可观测' | '资源绑定' | '配置' | '用量' | '流水线' | '变更' | '代码仓库' | '构建' | '镜像' | '发布'

// tab 名 ↔ URL query 值的双向映射（query 用英文短名，避免中文 URL 编码臃肿 + 利于分享）。
const TAB_TO_Q: Record<TabName, string> = {
  概览: 'overview',
  部署: 'deploy',
  服务治理: 'governance',
  可观测: 'observability',
  资源绑定: 'bindings',
  配置: 'configs',
  用量: 'usage',
  流水线: 'pipelines',
  变更: 'changes',
  代码仓库: 'repositories',
  构建: 'builds',
  镜像: 'images',
  发布: 'releases',
}
const Q_TO_TAB: Record<string, TabName> = Object.fromEntries(
  Object.entries(TAB_TO_Q).map(([t, q]) => [q, t as TabName]),
) as Record<string, TabName>

// activeTab 与 URL ?tab= 双向同步：初始化从 query 读（分享/刷新直达指定 tab），切换时写回 query。
const activeTab = ref<TabName>(Q_TO_TAB[(route.query.tab as string) ?? ''] ?? '概览')
watch(activeTab, (t) => {
  const q = TAB_TO_Q[t]
  // 仅在 query 缺失或不一致时 replace（避免每帧推历史致后退栈膨胀）。
  if ((route.query.tab as string) !== q) {
    router.replace({ query: { ...route.query, tab: q } })
  }
})
// 浏览器前进/后退（query 变）时同步 activeTab，保持 URL 与视图一致。
watch(
  () => route.query.tab,
  (q) => {
    const t = Q_TO_TAB[(q as string) ?? ''] ?? '概览'
    if (t !== activeTab.value) activeTab.value = t
  },
)

// 镜像 tab 点「发布」-> 切到发布 tab 并预选镜像（pickedImageId 变化触发 AppReleases 打开创建弹窗）
const pickedImageId = ref('')
async function pickImage(img: { id: string }) {
  pickedImageId.value = ''
  await nextTick()
  pickedImageId.value = img.id
  activeTab.value = '发布'
}

// 资源绑定卡片点击：数据服务类型解析 name→id 跳详情（带 app 上下文），
// 其他类型（models/gov）跳列表。DS_KINDS 见上方 addOptions 区定义。
async function onBindingClick(g: { key: string }, it: { name: string }) {
  const t = g.key
  if (DS_KINDS.includes(t as TypeKey)) {
    // 拉该 kind DS 列表，按 name 或 id 匹配解析出 dsId（绑定时 name/id 都可能）。
    try {
      const list = await fetchJSON<{ id: string; name: string }[]>(`/api/dataservices?kind=${t}`)
      const ds = list.find((d) => d.name === it.name || d.id === it.name)
      if (ds) {
        router.push(`/resources/${t}/${ds.id}?app=${app.value?.id ?? ''}`)
        return
      }
    } catch {
      /* 解析失败降级到列表 */
    }
  }
  router.push(bindingListRoute(t))
}
// 兜底：列表路由（解析失败或非 DS 类型）。
function bindingListRoute(t: string): string {
  if (t === 'models') return '/resources/models'
  if (t === 'gov') return '/platform/governance'
  if (t === 'dal') return '/resources/db'
  return `/resources/${t}`
}
// 部署 tab 工作负载行 → 该类型工作负载列表（带 ?app= 过滤，保留应用上下文）。
function workloadListRoute(t: string): string {
  const kind = t === 'job' ? 'jobs' : t === 'cronjob' ? 'cronjobs' : 'services'
  return `/workloads/${kind}?app=${route.params.id}`
}
// 跳 DevOps 跨应用总览。
function goDevOps() {
  router.push('/devops')
}

// 删除应用（级联清工作负载+配置）：高危，统一走 confirmDangerous（输入应用名确认）。
// 删除后返回应用列表。
async function deleteApp() {
  if (!app.value) return
  const ok = await confirmDangerous({
    action: '删除应用',
    target: app.value.name,
    requireNameConfirm: true,
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/applications/${app.value!.id}`, { method: 'DELETE' })
    if (!resp.ok) {
      const e = await resp.json().catch(() => ({}))
      ElMessage.error('删除失败：' + (e.error || resp.statusText))
      return
    }
    ElMessage.success('应用已删除')
    router.push('/applications')
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}
</script>

<template>
  <div class="detail">
    <div v-if="loading" class="crumb skel-bar" />
    <template v-else-if="app">
      <header class="crumb">
        <div class="crumb-left">
          <button class="crumb-back" @click="router.push('/applications')" title="返回应用列表">
            <Icon name="chevron" :size="16" style="transform: rotate(-90deg)" />
          </button>
          <span class="crumb-root">应用</span>
          <Icon name="chevron" :size="13" class="crumb-sep" />
          <span class="crumb-name">{{ app.name }}</span>
          <span class="env" :class="{ prodenv: app.env === 'prod' }">{{ app.env }}</span>
          <span class="health"><span class="pulse-dot" /> {{ statusLabel[app.status] ?? app.status }}</span>
        </div>
        <el-dropdown trigger="click" placement="bottom-end">
          <button class="crumb-more" title="更多操作">
            <span class="crumb-dots">⋯</span>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item class="danger-item" @click="deleteApp">🗑 删除应用</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </header>

      <div class="tabs">
        <template v-for="g in tabGroups" :key="g.label">
          <span class="tab-group-label">{{ g.label }}</span>
          <button
            v-for="t in g.tabs"
            :key="t"
            class="tab"
            :class="{ on: activeTab === t }"
            @click="activeTab = t"
          >
            {{ t }}
            <span v-if="t === '资源绑定'" class="tab-count mono">{{ totalBindings }}</span>
          </button>
        </template>
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
            <div v-for="it in g.items" :key="it.name" class="res-card clickable" @click="onBindingClick(g, it)">
              <div class="res-card-head">
                <span class="res-name mono">{{ it.name }}</span>
                <span class="res-status">已绑定</span>
              </div>
              <div class="res-type">{{ g.meta.label }}</div>
              <div v-if="it.note" class="res-detail">{{ it.note }}</div>
              <button class="unbind" @click.stop="unbind(it)">解绑</button>
            </div>
          </div>
          <!-- 模型推理绑定用法说明（绑定后自动生成应用级 Key + 注入配置） -->
          <div v-if="g.key === 'models'" class="usage-tip">
            <span class="tip-icon">💡</span>
            <div class="tip-body">
              <strong>用法：</strong>绑定模型后，平台已为本应用生成专属 API Key 并注入「配置」tab
              （<code class="mono">PAAS_LLM_API_KEY</code> / <code class="mono">PAAS_LLM_BASE_URL</code>）。
              应用工作负载重启后自动获得该凭证，代码用 OpenAI SDK 指向
              <code class="mono">PAAS_LLM_BASE_URL</code> 即可调用，<strong>token 用量自动归因到本应用</strong>（见「配额与账单」）。
            </div>
          </div>
        </section>
      </div>

      <!-- 概览 = 真实工作台 -->
      <div v-else-if="activeTab === '概览'" class="overview">
        <p v-if="app.desc" class="overview-desc">{{ app.desc }}</p>
        <div class="metrics">
          <div class="metric">
            <div class="m-v mono">{{ replicaStat.ready }}/{{ replicaStat.total }}</div>
            <div class="m-k">副本就绪</div>
          </div>
          <div class="metric">
            <div class="m-v mono">{{ totalBindings }}</div>
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
            <a v-for="e in accessEntries" :key="e.workload"
               :href="'http://' + e.domain" target="_blank" rel="noopener" class="access-item">
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
                <div v-for="g in groups" :key="g.key" class="topo-res">
                  <Icon :name="g.meta.icon" :size="16" :style="{ color: g.meta.color }" />
                  <span>{{ g.meta.label }}</span>
                  <span class="topo-n mono">{{ g.items.length }}</span>
                </div>
                <div v-if="!groups.length" class="topo-empty">尚未绑定资源</div>
              </div>
            </div>
          </div>

          <div class="side-cards">
            <div class="side-card">
              <div class="side-label">最新发布</div>
              <div v-if="latestRelease" class="side-body">
                <el-tag size="small" :type="latestRelease.status === 'succeeded' ? 'success' : 'info'">{{ latestRelease.status }}</el-tag>
                <span class="side-time">{{ new Date(latestRelease.createdAt).toLocaleString() }}</span>
              </div>
              <div v-else class="side-empty">暂无发布</div>
            </div>
            <div class="side-card">
              <div class="side-label">最新构建</div>
              <div v-if="latestBuild" class="side-body">
                <el-tag size="small" :type="latestBuild.status === 'success' ? 'success' : 'info'">{{ latestBuild.status }}</el-tag>
                <span class="side-time">{{ new Date(latestBuild.startedAt).toLocaleString() }}</span>
              </div>
              <div v-else class="side-empty">暂无构建</div>
            </div>
          </div>
        </div>
      </div>

      <!-- 部署 = 工作负载运行形态 -->
      <div v-else-if="activeTab === '部署'" class="deploy-view">
        <div v-if="!workloads.length" class="empty">
          <Icon name="server" :size="28" />
          <p>该应用尚未部署工作负载</p>
        </div>
        <div v-else class="env-groups">
          <section v-for="g in workloadsByEnv" :key="g.envId" class="env-group">
            <div class="env-group-head">
              <Icon v-if="g.type === 'prod'" name="shield" :size="14" />
              <span class="env-group-name">{{ g.name }}</span>
              <span class="env-group-count mono">{{ g.items.length }}</span>
            </div>
            <div class="wl-list">
              <div v-for="w in g.items" :key="w.id" class="wl-row clickable" @click="router.push(workloadListRoute(w.type))">
                <div class="wl-main">
                  <span class="wl-name">{{ w.name }}</span>
                  <span class="wl-type">{{ w.type }}</span>
                  <span v-if="w.laneId && w.laneId !== 'default'" class="wl-lane">泳道 {{ w.laneId }}</span>
                  <a v-if="w.domain" :href="'http://' + w.domain" target="_blank" rel="noopener" class="wl-domain" @click.stop>
                    <Icon name="link" :size="12" />{{ w.domain }}
                  </a>
                  <span class="wl-img mono">{{ w.image }}</span>
                  <span v-if="w.schedule" class="wl-sched mono">{{ w.schedule }}</span>
                </div>
                <div class="wl-side">
                  <span class="reps mono" :class="{ notready: w.ready < w.replicas }">{{ w.ready }}/{{ w.replicas }}</span>
                  <span class="wl-status">{{ w.status }}</span>
                </div>
              </div>
            </div>
          </section>
        </div>
      </div>

      <!-- 服务治理 -->
      <div v-else-if="activeTab === '服务治理'">
        <AppGovernance :app-id="app.id" />
      </div>

      <!-- 可观测 -->
      <div v-else-if="activeTab === '可观测'">
        <AppObservability :app-id="app.id" :bindings="app.bindings ?? []" />
      </div>

      <!-- 变更（火车发车模型：变更 + 集成批次） -->
      <div v-else-if="activeTab === '变更'">
        <AppChanges :app-id="app.id" />
      </div>

      <!-- 代码仓库 -->
      <div v-else-if="activeTab === '代码仓库'">
        <AppRepositories :app-id="app.id" />
      </div>

      <!-- 流水线（DevOps 分组首位，主线） -->
      <div v-else-if="activeTab === '流水线'">
        <AppPipelines :app-id="app.id" />
      </div>

      <!-- 构建 -->
      <div v-else-if="activeTab === '构建'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用构建总览 →</a></div>
        <AppBuilds :app-id="app.id" @pick="pickImage" />
      </div>

      <!-- 镜像（构建产物） -->
      <div v-else-if="activeTab === '镜像'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用镜像总览 →</a></div>
        <AppImages :app-id="app.id" @pick="pickImage" />
      </div>

      <!-- 发布 -->
      <div v-else-if="activeTab === '发布'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用发布总览 →</a></div>
        <AppReleases :app-id="app.id" :picked-image-id="pickedImageId" />
      </div>

      <!-- 配置（工作负载级 env/Secret） -->
      <div v-else-if="activeTab === '配置'">
        <AppConfigs :app-id="app.id" />
      </div>

      <!-- 用量 -->
      <div v-else-if="activeTab === '用量'">
        <AppUsage :app-id="app.id" />
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
            :placeholder="namePlaceholder"
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
/* 面包屑紧凑身份条：替代旧 header 大卡片，回收首屏垂直空间。 */
.crumb {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 14px;
}
.crumb.skel-bar {
  height: 44px;
  border: none;
}
.crumb-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.crumb-back {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.12s;
}
.crumb-back:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.crumb-root {
  font-size: 13px;
  color: var(--text-faint);
}
.crumb-sep {
  color: var(--text-faint);
}
.crumb-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: -0.01em;
  color: var(--text);
}
.crumb .env {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--success-soft);
  color: var(--success);
}
.crumb .env.prodenv {
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}
.crumb .health {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--success);
}
.crumb-more {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.12s;
}
.crumb-more:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.crumb-dots {
  font-size: 20px;
  line-height: 1;
  color: var(--text-dim);
}
.overview-desc {
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.6;
}
:deep(.danger-item) {
  color: var(--el-color-danger, #f43f5e);
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
.wl-domain {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 2px 8px; border-radius: var(--radius);
  background: rgba(34, 197, 94, 0.1); color: #16a34a;
  font-size: 11px; text-decoration: none;
}
.wl-domain:hover { opacity: 0.8; }
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

/* —— 部署 tab：工作负载列表 —— */
.deploy-view .empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 40px;
  color: var(--text-faint);
}
.env-groups {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.env-group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  color: var(--text-dim);
}
.env-group-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.env-group-count {
  font-size: 11px;
  color: var(--text-faint);
  padding: 1px 7px;
  background: var(--surface-2, transparent);
  border-radius: 8px;
}
.wl-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.wl-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.wl-main {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.wl-name {
  font-weight: 600;
  color: var(--text);
}
.wl-type {
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--brand-soft);
  color: var(--brand);
  font-size: 11px;
}
.wl-img {
  font-size: 12px;
  color: var(--text-dim);
}
.wl-lane {
  padding: 2px 8px;
  border-radius: 4px;
  background: rgba(245, 158, 11, 0.12);
  color: #f59e0b;
  font-size: 11px;
}
.wl-sched {
  font-size: 12px;
  color: var(--brand);
}
.wl-side {
  display: flex;
  align-items: center;
  gap: 14px;
}
.reps {
  font-weight: 600;
  color: var(--success);
}
.reps.notready {
  color: var(--warning);
}
.wl-status {
  font-size: 12px;
  color: var(--text-dim);
}
.clickable {
  cursor: pointer;
  transition: background 0.12s, border-color 0.12s;
}
.res-card.clickable:hover {
  border-color: var(--brand);
  background: var(--surface-2);
}
.wl-row.clickable:hover {
  background: var(--surface-2);
}
.cross-link {
  margin-bottom: 12px;
  font-size: 12px;
}
.cross-link a {
  color: var(--brand);
  cursor: pointer;
}
.cross-link a:hover {
  text-decoration: underline;
}
.usage-tip {
  display: flex;
  gap: 10px;
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-dim);
}
.usage-tip .tip-icon { flex-shrink: 0; }
.usage-tip code {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--surface);
  color: var(--brand);
  font-size: 11px;
}

/* —— tab 分组标签 + 概览工作台补样式 —— */
.tab-group-label {
  font-size: 11px;
  color: var(--text-faint);
  padding: 0 8px 0 0;
  margin-right: 4px;
  border-right: 1px solid var(--border);
  align-self: center;
  height: 16px;
  line-height: 16px;
}
.overview-row { display: flex; gap: 16px; flex-wrap: wrap; }
.overview-row .topo-card { flex: 1; min-width: 320px; }
.side-cards { display: flex; flex-direction: column; gap: 12px; min-width: 220px; }
.side-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.side-label { font-size: 12px; color: var(--text-faint); margin-bottom: 8px; }
.side-body { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.side-time { font-size: 12px; color: var(--text-dim); }
.side-empty { font-size: 13px; color: var(--text-faint); }
.mini-spark { display: flex; align-items: flex-end; gap: 2px; height: 24px; margin-top: 6px; }
.mini-spark span { flex: 1; background: var(--brand); opacity: 0.6; border-radius: 2px 2px 0 0; min-width: 2px; }
.topo-empty { font-size: 12px; color: var(--text-faint); }
</style>
