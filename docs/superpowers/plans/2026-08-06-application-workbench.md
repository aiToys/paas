# 应用工作台（Application Workbench）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 console-user 应用详情升级为「应用工作台」——概览真实化 + 收敛服务治理/可观测/用量/密钥到应用内，开发者进入应用即得全貌，减少顶级菜单跳转。

**Architecture:** 纯前端聚合（零后端改动）。新增 3 个 app-tab 组件（AppGovernance / AppObservability / AppUsage）+ AppConfigs 重组凭证分组 + ApplicationDetail tab 视觉分组与概览工作台改造。所有数据维度（治理 appId 过滤、可观测 appId 过滤、billing byApp 归因）后端均已就绪。

**Tech Stack:** Vue 3 + Element Plus + TypeScript + Pinia + fetchAuth/fetchJSON（智能解包 `{data:T}`）。

## Global Constraints

- **零后端改动**：所有端点已存在（`GET /api/services?appId=`、`GET /api/observability/{metrics,logs,traces}?appId=`、`GET /api/billing/usage` 返回 `usage.byApp`）。
- **响应契约**：成功 `{data:T}`（`fetchAuth` 后 `(await resp.json()).data ?? []`；`fetchJSON<T>` 自动解包）。`GET /api/services/{id}` 返裸 `{service,instances}`（双重兜底）。
- **注释中文**，与代码库一致。
- **不引入新依赖**，复用 Element Plus 已有组件。
- **prod 校验**：本计划新增 tab 均为只读/展示（治理实例注册归 `/platform/governance`，应用内不写），不触发 prod:write。
- **PriceTable 前端对齐**（`internal/billing/model.go:31-38`）：`applications:10, workloads:5, models:20, gpu:100, tokens:0.001, storage_gb:0.5`。
- **验证门**：前端无单测设施（现有 app-tabs 均无测试），每 task 以 `vue-tsc --noEmit` + `vite build` 为类型/编译验证，最终 k8s e2e 集成验证。
- **不主动 git commit**（遵循项目约束），commit 步骤仅作建议，由用户决定。

## File Structure

| 文件 | 职责 | 动作 |
|---|---|---|
| `frontend/console-user/src/views/app-tabs/AppConfigs.vue` | 配置 tab：env/secret 分两组渲染 | Modify |
| `frontend/console-user/src/views/app-tabs/AppGovernance.vue` | 应用治理 tab：按 appId 的服务+实例+路由+熔断（只读） | Create |
| `frontend/console-user/src/views/app-tabs/AppObservability.vue` | 应用可观测 tab：4 指标卡+日志+trace（预选 app） | Create |
| `frontend/console-user/src/views/app-tabs/AppUsage.vue` | 应用用量 tab：byApp 精确归因+资源占用+预估成本 | Create |
| `frontend/console-user/src/views/ApplicationDetail.vue` | tab 视觉分组 + 新 tab 挂载 + 概览工作台改造 | Modify |

---

### Task 1: AppConfigs 重组凭证分组

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/AppConfigs.vue`

**Interfaces:**
- Consumes: `props.appId`（string），`envStore`，`fetchAuth`，`confirmDangerous`
- Produces: 同组件（tab 内部重组，无对外签名变化）

**Why:** spec §5——应用引用的密钥 = `appconfig(type=secret)`。把 env/secret 混合表拆成「环境变量」+「凭证 / 密钥」两组，让"安全密钥引用"在应用内可见且与普通配置分离。

- [ ] **Step 1: 加 computed 分组**

在 `AppConfigs.vue` `<script setup>` 内 `const items = ref<ConfigItem[]>([])` 之后，加两个 computed：

```ts
// 按 type 分两组：环境变量（明文）+ 凭证/密钥（掩码）。
// 凭证组即「应用引用的密钥」——appconfig(type=secret) 是工作负载启动注入的真实敏感凭证。
const envItems = computed(() => items.value.filter((i) => i.type === TYPE_ENV))
const secretItems = computed(() => items.value.filter((i) => i.type === TYPE_SECRET))
```

- [ ] **Step 2: 模板拆成两个 section**

把 `<template>` 内单个 `<el-table :data="items" ...>`（约 146-171 行）替换为两个 section（共享同一 `showEdit` 弹窗，弹窗代码不动）：

```html
    <!-- 环境变量 -->
    <section v-if="hasEnv" class="cfg-group">
      <div class="group-title">环境变量（明文）<span class="group-cnt mono">{{ envItems.length }}</span></div>
      <el-table :data="envItems" v-loading="loading" size="small" empty-text="尚无环境变量">
        <el-table-column prop="key" label="Key" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column label="值" min-width="220">
          <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="160">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 凭证 / 密钥 -->
    <section v-if="hasEnv" class="cfg-group">
      <div class="group-title">
        凭证 / 密钥<span class="group-cnt mono">{{ secretItems.length }}</span>
      </div>
      <div class="secret-tip">应用工作负载启动时注入的敏感凭证。解绑数据服务会同步清除注入的连接凭证。</div>
      <el-table :data="secretItems" size="small" empty-text="尚无凭证">
        <el-table-column prop="key" label="Key" min-width="200">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column label="值（掩码）" min-width="200">
          <template #default="{ row }"><span class="mono masked">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="160">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 空态（两组皆空） -->
    <div v-if="hasEnv && !items.length && !loading" class="cfg-empty">
      <p>该环境尚无配置项，点右上角「+ 新增配置」添加。</p>
    </div>
```

注意：原 `<div v-if="!hasEnv" class="cfg-empty">…请在顶栏选择一个环境…</div>` 保留在两个 section 之前不变。

- [ ] **Step 3: 加分组样式**

在 `<style scoped>` 末尾追加：

```css
.cfg-group { margin-bottom: 22px; }
.group-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-dim);
  margin-bottom: 8px;
}
.group-cnt {
  font-size: 11px;
  color: var(--text-faint);
  padding: 1px 7px;
  background: var(--surface-2, transparent);
  border-radius: 8px;
}
.secret-tip {
  margin-bottom: 8px;
  padding: 8px 10px;
  font-size: 12px;
  color: var(--text-dim);
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
```

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd frontend && pnpm --filter console-user exec vue-tsc --noEmit`
Expected: PASS（无类型错误）

Run: `cd frontend && pnpm --filter console-user build`
Expected: PASS（构建成功）

- [ ] **Step 5: Commit（建议）**

```bash
git add frontend/console-user/src/views/app-tabs/AppConfigs.vue
git commit -m "feat(console-user): AppConfigs 拆分环境变量/凭证密钥两组"
```

---

### Task 2: AppGovernance 新建（应用治理 tab）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/AppGovernance.vue`

**Interfaces:**
- Consumes: `props.appId`（string），`envStore`（scope 过滤），`fetchAuth`
- Produces: 默认导出 Vue 组件，props `{ appId: string }`

**Why:** spec §3——`GET /api/services?appId=` + `GET /api/services/{id}`（含 instances）+ 路由/熔断按 serviceID 归属过滤。应用内只读查看该应用注册的服务、实例健康、路由、熔断状态。注册/注销归 `/platform/governance`。

- [ ] **Step 1: 创建组件文件**

写入 `frontend/console-user/src/views/app-tabs/AppGovernance.vue`：

```vue
<script setup lang="ts">
// 应用详情 - 服务治理 tab：该应用注册的服务 + 实例发现 + 路由 + 熔断（只读）。
// 复用 GET /api/services?appId=（按应用过滤）+ GET /api/services/{id}（聚合实例）。
// 注册/注销/路由/熔断的写操作归 /platform/governance；本 tab 仅应用内可见性。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

const props = defineProps<{ appId: string }>()
const router = useRouter()
const envStore = useEnvStore()

interface Service {
  id: string; name: string; appId?: string; envId: string
  protocol: string; port: number; desc?: string; updatedAt: string
}
interface Instance {
  id: string; serviceId: string; addr: string; status: string
  laneId: string; updatedAt: string
}
interface Route {
  id: string; name: string; path: string; serviceId: string
  methods: string[]; stripPath: boolean; enabled: boolean
}
interface WindowStats { requests: number; failures: number; slowCalls: number; rate: number }
interface Breaker {
  id: string; name: string; serviceId: string; strategy: string
  threshold: number; windowSecs: number; enabled: boolean
  state: string; stats: WindowStats
}

const services = ref<Service[]>([])
// 实例按 serviceID 缓存（懒加载：仅展开某服务时拉取）。
const instancesBySvc = ref<Record<string, Instance[]>>({})
const expanded = ref<Record<string, boolean>>({})
const routes = ref<Route[]>([])
const breakers = ref<Breaker[]>([])
const loading = ref(false)

const stateMeta: Record<string, { label: string; type: TagType }> = {
  closed: { label: '放行', type: 'success' },
  open: { label: '熔断', type: 'danger' },
  'half-open': { label: '半开', type: 'warning' },
}

// 该应用服务 ID 集合（用于过滤归属本应用的路由/熔断）。
const svcIds = computed(() => new Set(services.value.map((s) => s.id)))
const appRoutes = computed(() => routes.value.filter((r) => svcIds.value.has(r.serviceId)))
const appBreakers = computed(() => breakers.value.filter((b) => svcIds.value.has(b.serviceId)))

async function load() {
  loading.value = true
  try {
    const q = envStore.currentEnvId
      ? `?appId=${props.appId}&envId=${envStore.currentEnvId}`
      : `?appId=${props.appId}`
    const [svcResp, routeResp, breakerResp] = await Promise.all([
      fetchAuth(`/api/services${q}`),
      fetchAuth('/api/routes'),
      fetchAuth('/api/breakers'),
    ])
    if (svcResp.ok) services.value = (await svcResp.json()).data ?? []
    if (routeResp.ok) routes.value = (await routeResp.json()).data ?? []
    if (breakerResp.ok) breakers.value = (await breakerResp.json()).data ?? []
    instancesBySvc.value = {}
    expanded.value = {}
  } finally {
    loading.value = false
  }
}

// 懒加载某服务的实例（首次展开时）。GET /api/services/{id} 返 {service,instances}。
async function toggleExpand(s: Service) {
  const open = !expanded.value[s.id]
  expanded.value[s.id] = open
  if (open && !instancesBySvc.value[s.id]) {
    try {
      const resp = await fetchAuth(`/api/services/${s.id}`)
      if (resp.ok) {
        const json = await resp.json()
        const payload = json && typeof json === 'object' && 'service' in json ? json : (json?.data ?? {})
        instancesBySvc.value[s.id] = payload.instances ?? []
      }
    } catch {
      instancesBySvc.value[s.id] = []
    }
  }
}

function goRegistry() {
  router.push('/platform/governance')
}

onMounted(load)
watch(() => props.appId, load)
watch(() => envStore.currentEnvId, load)
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">服务治理</span>
      <span class="tab-hint">该应用注册的服务 · 实例 · 路由 · 熔断（只读）</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goRegistry">
        在服务治理中管理 →
      </el-button>
    </div>

    <div v-if="!services.length && !loading" class="empty">
      <p>该应用尚未注册服务</p>
      <el-button size="small" @click="goRegistry">去服务治理注册</el-button>
    </div>

    <el-table v-else :data="services" v-loading="loading" size="small" row-key="id">
      <el-table-column type="expand">
        <template #default="{ row }">
          <div class="inst-sub">
            <span v-if="!expanded[row.id]" class="faint">点击行首展开加载实例</span>
            <el-table v-else :data="instancesBySvc[row.id] || []" size="small" empty-text="该服务暂无实例">
              <el-table-column label="地址" min-width="200">
                <template #default="{ row: ins }"><span class="mono">{{ ins.addr }}</span></template>
              </el-table-column>
              <el-table-column label="状态" width="100">
                <template #default="{ row: ins }">
                  <el-tag :type="ins.status === 'healthy' ? 'success' : 'danger'" size="small">{{ ins.status }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="laneId" label="泳道" width="100" />
            </el-table>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="服务名" min-width="180">
        <template #default="{ row }">
          <span class="mono">{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="协议" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.protocol === 'grpc' ? 'warning' : 'info'">{{ row.protocol.toUpperCase() }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="port" label="端口" width="70" />
      <el-table-column prop="desc" label="描述" min-width="140" show-overflow-tooltip />
    </el-table>

    <!-- 路由 -->
    <section v-if="services.length" class="sub-block">
      <div class="sub-title">API 网关路由（{{ appRoutes.length }}）</div>
      <el-table :data="appRoutes" size="small" empty-text="该应用服务暂无路由">
        <el-table-column label="名称" min-width="120">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="路径" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.path }}</span></template>
        </el-table-column>
        <el-table-column label="方法" width="140">
          <template #default="{ row }">
            <el-tag v-for="m in row.methods" :key="m" size="small" style="margin-right:4px">{{ m }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 熔断 -->
    <section v-if="services.length" class="sub-block">
      <div class="sub-title">熔断器（{{ appBreakers.length }}）</div>
      <el-table :data="appBreakers" size="small" empty-text="该应用服务暂无熔断规则">
        <el-table-column label="名称" min-width="120">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="策略" width="90">
          <template #default="{ row }">{{ row.strategy === 'slow_call' ? '慢调用率' : '错误率' }}</template>
        </el-table-column>
        <el-table-column label="阈值" width="80">
          <template #default="{ row }">≥{{ row.threshold }}%</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.enabled" :type="(stateMeta[row.state]?.type) || 'info'" size="small">
              {{ stateMeta[row.state]?.label || row.state }}
            </el-tag>
            <el-tag v-else type="info" size="small">停用</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.empty { padding: 40px 0; text-align: center; color: var(--text-faint); font-size: 13px; }
.empty p { margin: 0 0 10px; }
.inst-sub { padding: 8px 16px 8px 40px; }
.faint { font-size: 12px; color: var(--text-faint); }
.sub-block { margin-top: 20px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.mono { font-family: var(--font-mono); }
</style>
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend && pnpm --filter console-user exec vue-tsc --noEmit`
Expected: PASS

- [ ] **Step 3: Commit（建议）**

```bash
git add frontend/console-user/src/views/app-tabs/AppGovernance.vue
git commit -m "feat(console-user): 新增 AppGovernance 应用治理 tab（服务/实例/路由/熔断只读）"
```

---

### Task 3: AppObservability 新建（应用可观测 tab）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/AppObservability.vue`

**Interfaces:**
- Consumes: `props.appId`，`fetchAuth`，`useRouter`（跳大盘）
- Produces: Vue 组件，props `{ appId: string }`

**Why:** spec §4——复用 `/api/observability/{metrics,logs,traces}?appId=`，固定预选当前 app（无 target 选择器）。4 指标卡 10s 轮询 + 最近日志 + 最近 trace。顶部「在监控大屏中打开」保留深度排查出口。

- [ ] **Step 1: 创建组件文件**

写入 `frontend/console-user/src/views/app-tabs/AppObservability.vue`：

```vue
<script setup lang="ts">
// 应用详情 - 可观测 tab：4 指标卡 + 最近日志 + 最近 trace（预选当前应用）。
// 复用 /api/observability/{metrics,logs,traces}?appId=（已按应用过滤）。
// 10s 轮询指标；深度排查去 /platform/observability?app=。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

const props = defineProps<{ appId: string }>()
const router = useRouter()

interface MetricPoint { ts: string; value: number }
interface MetricSeries {
  targetType: string; targetId: string; name: string; unit: string
  current: number; points: MetricPoint[]
}
interface LogEntry { id: string; appId: string; level: string; message: string; traceId?: string; timestamp: string }
interface Span { id: string; parentId?: string; operation: string; service: string; startMs: number; durationMs: number }
interface Trace { id: string; appId: string; operation: string; status: string; durationMs: number; startedAt: string; spans: Span[] }

const metrics = ref<MetricSeries[]>([])
const logs = ref<LogEntry[]>([])
const traces = ref<Trace[]>([])
const loading = ref(false)

const metricOrder = ['cpu', 'mem', 'rps', 'latency']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟' }
const logLevelLabel: Record<string, string> = { info: '信息', warn: '警告', error: '错误' }
const logLevelType: Record<string, TagType> = { info: 'info', warn: 'warning', error: 'danger' }
const traceStatusLabel: Record<string, string> = { success: '成功', error: '错误' }
const traceStatusType: Record<string, TagType> = { success: 'success', error: 'danger' }

const cards = computed(() =>
  metricOrder
    .map((name) => {
      const m = metrics.value.find((x) => x.name === name)
      return m ? { name, label: metricLabel[name], unit: m.unit, current: m.current, points: m.points } : null
    })
    .filter(Boolean) as { name: string; label: string; unit: string; current: number; points: MetricPoint[] }[],
)

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// sparkline 高度（最近 24 点映射到 20-100% 区间）。
function sparkHeights(points: MetricPoint[]): number[] {
  if (points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}

async function loadMetrics() {
  const resp = await fetchAuth(`/api/observability/metrics?targetType=app&targetId=${props.appId}`)
  if (resp.ok) metrics.value = (await resp.json()).data ?? []
}
async function loadLogs() {
  const resp = await fetchAuth(`/api/observability/logs?appId=${props.appId}&limit=50`)
  if (resp.ok) logs.value = (await resp.json()).data ?? []
}
async function loadTraces() {
  const resp = await fetchAuth(`/api/observability/traces?appId=${props.appId}&limit=20`)
  if (resp.ok) traces.value = (await resp.json()).data ?? []
}

async function loadAll(silent = false) {
  if (!silent) loading.value = true
  try {
    await Promise.all([loadMetrics(), loadLogs(), loadTraces()])
  } finally {
    if (!silent) loading.value = false
  }
}

function goDashboard() {
  router.push(`/platform/observability?app=${props.appId}`)
}

let timer: number | undefined
onMounted(() => {
  loadAll()
  timer = window.setInterval(() => loadAll(true), 10000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })
watch(() => props.appId, () => loadAll())
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">可观测</span>
      <span class="tab-hint">指标 · 日志 · 链路（10s 自动刷新）</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goDashboard">
        在监控大屏中打开 →
      </el-button>
    </div>

    <!-- 指标卡 -->
    <section v-loading="loading">
      <div v-if="cards.length" class="metric-grid">
        <div v-for="c in cards" :key="c.name" class="metric-card">
          <div class="m-label">{{ c.label }}</div>
          <div class="m-value mono">{{ fmtVal(c.current) }}<span class="m-unit">{{ c.unit }}</span></div>
          <div class="spark">
            <span v-for="(h, idx) in sparkHeights(c.points)" :key="idx" class="spark-bar" :style="{ height: h + '%' }" />
          </div>
        </div>
      </div>
      <div v-else class="empty">该应用暂无指标</div>
    </section>

    <!-- 日志 -->
    <section class="sub-block">
      <div class="sub-title">最近日志</div>
      <el-table :data="logs" size="small" height="260" empty-text="暂无日志">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ new Date(row.timestamp).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="(logLevelType[row.level]) || 'info'" size="small">{{ logLevelLabel[row.level] || row.level }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="消息" min-width="280" show-overflow-tooltip />
      </el-table>
    </section>

    <!-- trace -->
    <section class="sub-block">
      <div class="sub-title">最近链路</div>
      <el-table :data="traces" size="small" row-key="id" empty-text="暂无链路">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="span-list">
              <div v-for="sp in row.spans" :key="sp.id" class="span-row">
                <span class="mono span-svc">{{ sp.service }}</span>
                <span class="span-op">{{ sp.operation }}</span>
                <span class="mono span-dur">{{ sp.durationMs }}ms</span>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="开始" width="170">
          <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" min-width="200">
          <template #default="{ row }"><span class="mono">{{ row.operation }}</span></template>
        </el-table-column>
        <el-table-column label="时长" width="80">
          <template #default="{ row }"><span class="mono">{{ row.durationMs }}ms</span></template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="(traceStatusType[row.status]) || 'info'" size="small">{{ traceStatusLabel[row.status] || row.status }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.empty { padding: 32px 0; text-align: center; color: var(--text-faint); font-size: 13px; }
.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; margin-bottom: 20px; }
.metric-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 22px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 12px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 30px; margin-top: 6px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.sub-block { margin-top: 18px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.mono { font-family: var(--font-mono); }
.span-list { padding: 6px 20px; display: flex; flex-direction: column; gap: 4px; }
.span-row { display: flex; align-items: center; gap: 10px; font-size: 12px; }
.span-svc { color: var(--brand); min-width: 100px; }
.span-op { flex: 1; color: var(--text-dim); }
.span-dur { color: var(--text-faint); }
</style>
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend && pnpm --filter console-user exec vue-tsc --noEmit`
Expected: PASS

- [ ] **Step 3: Commit（建议）**

```bash
git add frontend/console-user/src/views/app-tabs/AppObservability.vue
git commit -m "feat(console-user): 新增 AppObservability 应用可观测 tab"
```

---

### Task 4: AppUsage 新建（应用用量 tab）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/AppUsage.vue`

**Interfaces:**
- Consumes: `props.appId`，`fetchAuth`；PriceTable 前端常量（对齐 `billing/model.go:31-38`）
- Produces: Vue 组件，props `{ appId: string }`

**Why:** spec §6——`GET /api/billing/usage` 返回 `usage.byApp[appID]`（gateway 经应用级 Key 归因的 token/gpu 精确用量）+ 应用资源占用（工作负载数/绑定资源数，从 application detail 已有数据 + 独立加载 workloads）+ PriceTable 预估月成本。

- [ ] **Step 1: 创建组件文件**

写入 `frontend/console-user/src/views/app-tabs/AppUsage.vue`：

```vue
<script setup lang="ts">
// 应用详情 - 用量 tab：精确归因（token/gpu 走 billing.byApp）+ 资源占用 + 预估月成本。
// GET /api/billing/usage 返 {usage:{counts, byApp:{[appId]:{tokens,gpu,...}}}}。
// byApp 由 gateway 经应用级 API Key 归因落库（模型推理 token 计费真源）。
// PriceTable 是 mock 单价，成本为「预估」（真实计费引擎留后续）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string }>()
const router = useRouter()

// PriceTable 与 internal/billing/model.go:31-38 对齐（元/单位）。
const PRICE: Record<string, number> = {
  applications: 10, workloads: 5, models: 20, gpu: 100, tokens: 0.001, storage_gb: 0.5,
}
const RES_LABEL: Record<string, string> = {
  applications: '应用', workloads: '工作负载', models: '模型部署', gpu: 'GPU（卡·时）', tokens: 'Token（千次）', storage_gb: '存储（GB）',
}

interface Workload { id: string; envId: string; type: string; replicas: number; ready: number }

const appUsage = ref<Record<string, number>>({})   // 该应用 byApp 归因（token/gpu 精确）
const workloads = ref<Workload[]>([])
const bindingCounts = ref<Record<string, number>>({}) // 按 type 计数的绑定资源
const loading = ref(false)

// 该应用精确归因用量行（token/gpu 等真有 byApp 数据的维度）。
const usageLines = computed(() => {
  return Object.entries(appUsage.value)
    .filter(([, n]) => n > 0)
    .map(([res, n]) => ({
      res, label: RES_LABEL[res] || res, count: n,
      unitPrice: PRICE[res] ?? 0, amount: (PRICE[res] ?? 0) * n,
    }))
})

// 资源占用（应用自身计数）。
const totalReplicas = computed(() => workloads.value.reduce((s, w) => s + w.replicas, 0))
const totalReady = computed(() => workloads.value.reduce((s, w) => s + w.ready, 0))
const resourceStats = computed(() => [
  { label: '工作负载', count: workloads.value.length },
  { label: '副本', count: totalReplicas.value },
  { label: '就绪', count: totalReady.value },
  ...Object.entries(bindingCounts.value)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => ({ label: RES_LABEL[k] || k, count: n })),
])

// 预估月成本（归因用量 × 单价求和）。标注预估（PriceTable 是 mock 单价）。
const estCost = computed(() => usageLines.value.reduce((s, l) => s + l.amount, 0))

async function load() {
  loading.value = true
  try {
    const [usageResp, wlResp] = await Promise.all([
      fetchAuth('/api/billing/usage'),
      fetchAuth(`/api/applications/${props.appId}/workloads`),
    ])
    if (usageResp.ok) {
      const json = await usageResp.json()
      const usage = json?.data?.usage ?? json?.usage ?? {}
      appUsage.value = usage.byApp?.[props.appId] ?? {}
    }
    if (wlResp.ok) workloads.value = (await wlResp.json()).data ?? []
    // 绑定资源计数：从 application detail 的 bindings（独立拉一次应用详情）。
    const appResp = await fetchAuth(`/api/applications/${props.appId}`)
    if (appResp.ok) {
      const app = (await appResp.json()).data
      const counts: Record<string, number> = {}
      // 绑定 type 映射到计费维度：models→models，其余按 type 计数（db/cache 等非计费维度忽略）。
      for (const b of app?.bindings ?? []) {
        if (b.type === 'models') counts.models = (counts.models ?? 0) + 1
      }
      bindingCounts.value = counts
    }
  } finally {
    loading.value = false
  }
}

function goBilling() { router.push('/settings/billing') }

onMounted(load)
watch(() => props.appId, load)
</script>

<template>
  <div class="devops-tab" v-loading="loading">
    <div class="tab-head">
      <span class="tab-title">用量与成本</span>
      <span class="tab-hint">精确归因（token/GPU）+ 资源占用 + 预估月成本</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goBilling">
        查看租户账单 →
      </el-button>
    </div>

    <!-- 资源占用 -->
    <section class="sub-block">
      <div class="sub-title">资源占用</div>
      <div class="stat-grid">
        <div v-for="s in resourceStats" :key="s.label" class="stat-card">
          <div class="stat-v mono">{{ s.count }}</div>
          <div class="stat-k">{{ s.label }}</div>
        </div>
      </div>
    </section>

    <!-- 精确归因用量 -->
    <section class="sub-block">
      <div class="sub-title">归因用量（应用级 API Key 计费维度）</div>
      <el-table :data="usageLines" size="small" empty-text="暂无归因用量（应用级 Key 调用 /v1 推理后产生）">
        <el-table-column label="资源" min-width="160">
          <template #default="{ row }">{{ row.label }}</template>
        </el-table-column>
        <el-table-column label="用量" width="140">
          <template #default="{ row }"><span class="mono">{{ row.count }}</span></template>
        </el-table-column>
        <el-table-column label="单价" width="120">
          <template #default="{ row }"><span class="mono">¥{{ row.unitPrice }}</span></template>
        </el-table-column>
        <el-table-column label="金额" width="120">
          <template #default="{ row }"><span class="mono">¥{{ row.amount.toFixed(2) }}</span></template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 预估成本 -->
    <section class="sub-block cost-card">
      <div class="cost-label">预估月成本</div>
      <div class="cost-val mono">¥{{ estCost.toFixed(2) }}</div>
      <div class="cost-note">基于平台单价表预估，非精确计费（计费引擎留后续）</div>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.sub-block { margin-top: 18px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.stat-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; }
.stat-card { padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.stat-v { font-size: 22px; font-weight: 700; }
.stat-k { font-size: 12px; color: var(--text-faint); margin-top: 2px; }
.cost-card { padding: 18px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.cost-label { font-size: 13px; color: var(--text-dim); }
.cost-val { font-size: 30px; font-weight: 700; letter-spacing: -0.02em; margin: 4px 0; color: var(--brand); }
.cost-note { font-size: 11.5px; color: var(--text-faint); }
.mono { font-family: var(--font-mono); }
</style>
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend && pnpm --filter console-user exec vue-tsc --noEmit`
Expected: PASS

- [ ] **Step 3: Commit（建议）**

```bash
git add frontend/console-user/src/views/app-tabs/AppUsage.vue
git commit -m "feat(console-user): 新增 AppUsage 应用用量 tab（byApp 归因 + 预估成本）"
```

---

### Task 5: ApplicationDetail 集成（tab 分组 + 概览工作台 + 挂载新 tab）

**Files:**
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`

**Interfaces:**
- Consumes: Task 1-4 产出的 4 个组件 + 已有 app-tabs
- Produces: 改造后的应用详情（10 tab 分组 + 真实化概览）

**Why:** spec §1（tab IA 重组）+ §2（概览真实化）。把 10 tab 按「运行态/资源/DevOps」视觉分组；概览去 seed 假数据（`app.rps`/`app.replicas`），改为聚合真实 workloads/bindings + sparkline（metrics）+ 最新发布/构建卡。

- [ ] **Step 1: 加新组件 import**

在 `ApplicationDetail.vue` `<script setup>` 顶部 import 区（`AppConfigs` 之后）加：

```ts
import AppGovernance from './app-tabs/AppGovernance.vue'
import AppObservability from './app-tabs/AppObservability.vue'
import AppUsage from './app-tabs/AppUsage.vue'
```

- [ ] **Step 2: tabs 定义改为分组结构**

把 `const tabs = [...]` 那行（约 214 行）替换为分组定义 + 扁平 tabs 数组：

```ts
// tab 视觉分组（运行态/资源/DevOps）：防 10 tab 平铺膨胀。
const tabGroups = [
  { label: '运行态', tabs: ['概览', '部署', '服务治理', '可观测'] as const },
  { label: '资源', tabs: ['资源绑定', '配置'] as const },
  { label: 'DevOps', tabs: ['代码仓库', '构建', '镜像', '发布'] as const },
]
type TabName = '概览' | '部署' | '服务治理' | '可观测' | '资源绑定' | '配置' | '代码仓库' | '构建' | '镜像' | '发布'
const activeTab = ref<TabName>('概览')
```

删除原 `const tabs = [...] as const` 与原 `const activeTab = ref<(typeof tabs)[number]>('概览')`。

- [ ] **Step 3: 概览真实化 —— 加 metrics + 最新发布/构建加载**

在 `load()` 函数内（`workloads.value = ...` / `envs.value = ...` 之后）追加并行加载 metrics/最新发布/最新构建，并新增 ref。先在 `const envs = ref<Env[]>([])`（约 94 行）之后加 ref 声明：

```ts
// 概览工作台：真实运行态指标 + 最新发布/构建（去 seed 假数据 rps/replicas）。
interface MetricPoint { ts: string; value: number }
interface MetricSeries { name: string; unit: string; current: number; points: MetricPoint[] }
const metrics = ref<MetricSeries[]>([])
interface Release { id: string; status: string; envId: string; createdAt: string }
interface Build { id: string; status: string; startedAt: string }
const latestRelease = ref<Release | null>(null)
const latestBuild = ref<Build | null>(null)
```

在 `load()` 函数的 `Promise.allSettled` 数组里追加这三项（把原 `[ws, es] = await Promise.allSettled([...])` 扩展为 5 项）：

```ts
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
```

- [ ] **Step 4: 概览聚合 computed**

在 `const totalBindings = computed(...)`（约 78 行）之后加：

```ts
// 概览真实聚合：副本就绪比 + 服务数（治理）+ sparkline。
const replicaStat = computed(() => {
  const total = workloads.value.reduce((s, w) => s + w.replicas, 0)
  const ready = workloads.value.reduce((s, w) => s + w.ready, 0)
  return { ready, total }
})
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
```

- [ ] **Step 5: tab 条模板改分组渲染**

把 `<div class="tabs">…</div>`（约 321-326 行）替换为分组渲染：

```html
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
```

- [ ] **Step 6: 概览模板改真实工作台**

把 `<!-- 概览 -->` 的 `<div v-else-if="activeTab === '概览'" class="overview">…</div>`（约 372-395 行）替换为：

```html
      <!-- 概览 = 真实工作台 -->
      <div v-else-if="activeTab === '概览'" class="overview">
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
```

- [ ] **Step 7: 挂载 3 个新 tab**

在 `<template>` 内「部署」tab 区块之后、「代码仓库」之前，加「服务治理」「可观测」两个 tab 区块；在「配置」tab 区块之后加「用量」tab 区块。

部署 tab 之后插入（在 `<!-- 代码仓库 -->` 注释之前）：

```html
      <!-- 服务治理 -->
      <div v-else-if="activeTab === '服务治理'">
        <AppGovernance :app-id="app.id" />
      </div>

      <!-- 可观测 -->
      <div v-else-if="activeTab === '可观测'">
        <AppObservability :app-id="app.id" />
      </div>
```

配置 tab 之后插入（在配置 `</div>` 之后、`</template>` 之前）：

```html
      <!-- 用量 -->
      <div v-else-if="activeTab === '用量'">
        <AppUsage :app-id="app.id" />
      </div>
```

注意：`activeTab` 类型已扩展为含 `'用量'`，但 tabGroups 里「资源」组只有 `['资源绑定','配置']`——需要把「用量」加入某组。更新 Step 2 的 tabGroups「资源」组为 `['资源绑定', '配置', '用量']`，并对应 `TabName` 加 `'用量'`。

- [ ] **Step 8: 分组 + 概览新样式**

在 `<style scoped>` 内追加（不删原有）：

```css
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
```

- [ ] **Step 9: 类型检查 + 构建**

Run: `cd frontend && pnpm --filter console-user exec vue-tsc --noEmit`
Expected: PASS（注意检查 `TabName` 联合类型与所有 `activeTab ===` 比较分支一致）

Run: `cd frontend && pnpm --filter console-user build`
Expected: PASS

- [ ] **Step 10: Commit（建议）**

```bash
git add frontend/console-user/src/views/ApplicationDetail.vue
git commit -m "feat(console-user): 应用详情升级为工作台（tab 分组 + 概览真实化 + 挂载治理/可观测/用量）"
```

---

### Task 6: 全量验证 + k8s 部署 e2e

**Files:**
- 无新文件，全量验证 + 部署

**Why:** 确认 console-user 构建 + 三套前端一致 + k8s 部署后端到端可用。

- [ ] **Step 1: 三套前端全量构建**

Run: `cd frontend && pnpm install && pnpm build`
Expected: 三套（landing/console-user/console-admin）全部 PASS

- [ ] **Step 2: 后端全量测试（确认零回归）**

Run: `make test`
Expected: PASS（本计划零后端改动，应全绿）

- [ ] **Step 3: k8s 部署**

Run: `./scripts/deploy-k8s.sh`
Expected: 构建前端 embed 镜像 + push registry + helm upgrade + 新 paas-core Pod Running

- [ ] **Step 4: e2e 验证应用工作台**

Run（替换 `<key>` 为演示 key，`<appid>` 为 `app-cs`）:

```bash
# 核心可达
curl -s -o /dev/null -w "%{http_code}\n" -H "Cookie: paas_access=<jwt>" http://paas.k8s.dd/api/applications/app-cs
# 治理按 appId 过滤（应只返归属 app-cs 的服务）
curl -s -H "Cookie: paas_access=<jwt>" "http://paas.k8s.dd/api/services?appId=app-cs" | head -c 300
# 可观测按 app 过滤
curl -s -o /dev/null -w "%{http_code}\n" -H "Cookie: paas_access=<jwt>" "http://paas.k8s.dd/api/observability/metrics?targetType=app&targetId=app-cs"
# billing usage 含 byApp 字段
curl -s -H "Cookie: paas_access=<jwt>" http://paas.k8s.dd/api/billing/usage | grep -o "byApp"
```

Expected: applications 200；services 返回 app-cs 归属服务；metrics 200；byApp 字段存在。

浏览器手验（http://paas.k8s.dd/console/ 登录后进应用详情）：
- 10 tab 按「运行态/资源/DevOps」三组渲染，一屏可见
- 概览：副本就绪比/绑定资源数/请求秒/CPU 真实展示（无 seed 假值），最新发布/构建卡有数据
- 服务治理 tab：列出 app-cs 的服务，展开见实例
- 可观测 tab：4 指标卡 + 日志 + trace 渲染
- 用量 tab：资源占用卡 + 归因用量表 + 预估成本
- 配置 tab：环境变量 / 凭证密钥 两组分离

- [ ] **Step 5: 更新 CLAUDE.md**

在 `docs/superpowers/...` 或 CLAUDE.md「垂直切片」相关章节追加「P1.5 应用工作台」小节，记录：tab 分组 IA + 概览真实化 + 收敛治理/可观测/用量/凭证到应用内（零后端，纯前端聚合 byApp 归因）+ 留后续（audit app_id、方案 C 全局反查、Prom 应用级指标）。

- [ ] **Step 6: Commit（建议，由用户决定）**

```bash
git add -A
git commit -m "feat(console-user): 应用工作台（tab 分组 + 概览真实化 + 收敛治理/可观测/用量/凭证）"
```

---

## Self-Review 记录

**Spec 覆盖**：spec §1 tab 分组 → Task 5 Step 2/5；§2 概览真实化 → Task 5 Step 3/4/6；§3 服务治理 → Task 2；§4 可观测 → Task 3；§5 配置重组凭证 → Task 1；§6 用量 → Task 4。全覆盖。

**类型一致性**：`TabName` 联合含 10 个 tab 名（含「用量」），与 tabGroups 三组展开一致；`activeTab === 'XXX'` 分支名逐一对应。`Release`/`Build`/`MetricSeries` 接口在 ApplicationDetail 内局部定义，与各 app-tab 组件内部接口解耦（无跨文件类型依赖）。

**Placeholder 扫描**：无 TBD/TODO；每个 Step 含完整代码或确切命令。

**已知风险**：
- ApplicationDetail 的 `App` 接口仍保留 `rps/replicas` seed 字段（其他地方可能引用），Step 6 模板不再展示假值，但字段保留不删（避免连带改 Applications.vue 列表）。若 vue-tsc 报未使用不影响构建。
- Task 5 Step 7 的「用量」tab 归属组：spec §1 把用量归「资源」组（资源/计费语义），plan 据此将「用量」放入「资源」组。若你倾向独立「计费」组，调整 tabGroups 即可。
