<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
// 平台能力 → 服务治理 → 注册中心。
// 服务定义列表（租户私有，按顶栏 scope 过滤）+ 注册/注销服务 + 进入详情（实例发现）。
// 生产注册/注销受 prod:write 保护（后端）；前端注销走 confirmDangerous（生产输入名称确认）。
// 治理是横切平台能力，独立菜单（非应用子页），体现其正交定位。
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { useEnvStore } from '@/stores/env'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const router = useRouter()
const envStore = useEnvStore()

interface Service {
  id: string
  name: string
  appId?: string
  envId: string
  protocol: string
  port: number
  desc?: string
  updatedAt: string
}
interface Env { id: string; name: string; type: string }
interface Route {
  id: string; name: string; host?: string; path: string; serviceId: string
  methods: string[]; stripPath: boolean; enabled: boolean; updatedAt: string
}
interface WindowStats { requests: number; failures: number; slowCalls: number; rate: number }
interface Breaker {
  id: string; name: string; serviceId: string; strategy: string
  threshold: number; minRequests: number; windowSecs: number; enabled: boolean
  state: string; stats: WindowStats; updatedAt: string
}

const services = ref<Service[]>([])
const envs = ref<Env[]>([])
const routes = ref<Route[]>([])
const breakers = ref<Breaker[]>([])
const loading = ref(false)
const showCreate = ref(false)
const submitting = ref(false)
const form = ref({ name: '', appId: '', envId: '', protocol: 'http', port: 8080, desc: '' })

// 路由创建弹窗
const showRoute = ref(false)
const routeForm = ref({ name: '', host: '', path: '', serviceId: '', methods: ['ANY'] as string[], stripPath: true })
const routeSubmitting = ref(false)
const methodOpts = ['GET', 'POST', 'PUT', 'DELETE', 'ANY']
// Ingress Controller 提示文案（前端无此配置，用通用值；实际 class 由后端 PAAS_INGRESS_CLASS 决定）。
const ingressHint = 'Ingress Controller'

// 熔断器创建弹窗
const showBreaker = ref(false)
const breakerSubmitting = ref(false)
const breakerForm = ref({
  name: '', serviceId: '', strategy: 'error_rate',
  threshold: 50, minRequests: 20, windowSecs: 60,
})
const strategyOpts = [
  { value: 'error_rate', label: '错误率（5xx/异常）' },
  { value: 'slow_call', label: '慢调用率' },
]
const stateMeta: Record<string, { label: string; type: TagType }> = {
  closed: { label: '放行', type: 'success' },
  open: { label: '熔断', type: 'danger' },
  'half-open': { label: '半开', type: 'warning' },
}

const protocols = [
  { value: 'http', label: 'HTTP' },
  { value: 'grpc', label: 'gRPC' },
]

function envName(id: string) {
  return envs.value.find((e) => e.id === id)?.name ?? id
}

async function loadEnvs() {
  const resp = await fetchAuth('/api/environments')
  if (resp.ok) envs.value = (await resp.json()).data ?? []
}

async function load() {
  loading.value = true
  try {
    const q = envStore.currentEnvId ? `?envId=${envStore.currentEnvId}` : ''
    const [svcResp, routeResp, breakerResp] = await Promise.all([
      fetchAuth(`/api/services${q}`),
      fetchAuth('/api/routes'),
      fetchAuth('/api/breakers'),
    ])
    if (svcResp.ok) services.value = (await svcResp.json()).data ?? []
    if (routeResp.ok) routes.value = (await routeResp.json()).data ?? []
    if (breakerResp.ok) breakers.value = (await breakerResp.json()).data ?? []
  } finally {
    loading.value = false
  }
}

async function loadBreakers() {
  const resp = await fetchAuth('/api/breakers')
  if (resp.ok) breakers.value = (await resp.json()).data ?? []
}

async function loadRoutes() {
  const resp = await fetchAuth('/api/routes')
  if (resp.ok) routes.value = (await resp.json()).data ?? []
}

const serviceName = (id: string) => services.value.find((s) => s.id === id)?.name ?? id

function openRoute() {
  routeForm.value = { name: '', host: '', path: '', serviceId: services.value[0]?.id ?? '', methods: ['ANY'], stripPath: true }
  showRoute.value = true
}

async function createRoute() {
  if (!routeForm.value.name.trim() || !routeForm.value.path.trim() || !routeForm.value.serviceId) {
    ElMessage.warning('请填写名称、路径并选择目标服务')
    return
  }
  routeSubmitting.value = true
  try {
    const resp = await fetchAuth('/api/routes', {
      method: 'POST',
      body: JSON.stringify({ ...routeForm.value, enabled: true }),
    })
    if (resp.ok) { ElMessage.success('路由已创建'); showRoute.value = false; loadRoutes() }
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '创建失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    routeSubmitting.value = false
  }
}

async function toggleRoute(row: Route) {
  try {
    const resp = await fetchAuth(`/api/routes/${row.id}`, {
      method: 'PUT',
      body: JSON.stringify({ enabled: !row.enabled }),
    })
    if (resp.ok) loadRoutes()
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '操作失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  }
}

async function deleteRoute(row: Route) {
  const ok = await confirmDangerous({ action: '删除路由', target: row.name })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/routes/${row.id}`, { method: 'DELETE' })
    if (resp.ok) { ElMessage.success('已删除'); loadRoutes() }
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '删除失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

function openBreaker() {
  breakerForm.value = {
    name: '', serviceId: services.value[0]?.id ?? '',
    strategy: 'error_rate', threshold: 50, minRequests: 20, windowSecs: 60,
  }
  showBreaker.value = true
}

async function createBreaker() {
  const f = breakerForm.value
  if (!f.name.trim() || !f.serviceId) {
    ElMessage.warning('请填写名称并选择目标服务')
    return
  }
  breakerSubmitting.value = true
  try {
    const resp = await fetchAuth('/api/breakers', {
      method: 'POST',
      body: JSON.stringify({ ...f, enabled: true }),
    })
    if (resp.ok) { ElMessage.success('熔断器已创建'); showBreaker.value = false; loadBreakers() }
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '创建失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    breakerSubmitting.value = false
  }
}

async function toggleBreaker(row: Breaker) {
  try {
    const resp = await fetchAuth(`/api/breakers/${row.id}`, {
      method: 'PUT',
      body: JSON.stringify({ enabled: !row.enabled }),
    })
    if (resp.ok) loadBreakers()
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '操作失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  }
}

async function deleteBreaker(row: Breaker) {
  const ok = await confirmDangerous({ action: '删除熔断器', target: row.name })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/breakers/${row.id}`, { method: 'DELETE' })
    if (resp.ok) { ElMessage.success('已删除'); loadBreakers() }
    else { const e = await resp.json().catch(() => ({})); ElMessage.error(e.error || '删除失败') }
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

function openCreate() {
  form.value = {
    name: '',
    appId: '',
    envId: envStore.currentEnvId || '',
    protocol: 'http',
    port: 8080,
    desc: '',
  }
  showCreate.value = true
}

async function create() {
  if (!form.value.name.trim() || !form.value.envId || !form.value.port) {
    ElMessage.warning('请填写服务名、环境与端口')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth('/api/services', {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    if (resp.ok) {
      ElMessage.success('已注册服务')
      showCreate.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '注册失败')
    }
  } finally {
    submitting.value = false
  }
}

async function remove(row: Service) {
  // 按服务自身 envId 判定生产（非顶栏 scope），防顶栏与资源环境不一致时防护削弱。
  const rowEnv = envs.value.find((e) => e.id === row.envId)
  const isProd = rowEnv?.type === 'prod'
  const ok = await confirmDangerous({
    action: '注销服务',
    target: row.name,
    requireNameConfirm: isProd,
    isProd,
  })
  if (!ok) return
  const resp = await fetchAuth(`/api/services/${row.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已注销')
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '注销失败')
  }
}

onMounted(async () => {
  await Promise.all([loadEnvs(), load()])
})
watch(() => envStore.currentEnvId, load)
</script>

<template>
  <div class="gov-page">
    <div class="page-head">
      <div>
        <h2>服务治理 · 注册中心</h2>
        <p class="sub">服务注册与发现。租户私有横切能力 · 当前环境：{{ envStore.currentEnv?.name || '全部' }}</p>
      </div>
      <el-button type="primary" @click="openCreate">+ 注册服务</el-button>
    </div>

    <el-table :data="services" v-loading="loading" size="default" empty-text="当前范围暂无注册服务">
      <el-table-column label="服务名" min-width="180">
        <template #default="{ row }">
          <a class="svc-link" @click="router.push(`/platform/governance/services/${row.id}`)">{{ row.name }}</a>
        </template>
      </el-table-column>
      <el-table-column label="协议" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.protocol === 'grpc' ? 'warning' : 'info'">{{ row.protocol.toUpperCase() }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="port" label="端口" width="80" />
      <el-table-column label="环境" width="140">
        <template #default="{ row }">{{ envName(row.envId) }}</template>
      </el-table-column>
      <el-table-column prop="appId" label="归属应用" width="140" />
      <el-table-column prop="desc" label="描述" min-width="140" show-overflow-tooltip />
      <el-table-column label="更新时间" width="160">
        <template #default="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="120">
        <template #default="{ row }">
          <el-button text type="primary" size="small" @click="router.push(`/platform/governance/services/${row.id}`)">实例</el-button>
          <el-button text type="danger" size="small" @click="remove(row)">注销</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showCreate" title="注册服务" width="480px">
      <el-form label-width="80px">
        <el-form-item label="服务名">
          <el-input v-model="form.name" placeholder="如 customer-svc（租户内唯一）" />
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.envId" placeholder="选择环境" style="width: 100%">
            <el-option v-for="e in envs" :key="e.id" :label="e.name" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="归属应用">
          <el-input v-model="form.appId" placeholder="可选，如 app-cs" />
        </el-form-item>
        <el-form-item label="协议">
          <el-radio-group v-model="form.protocol">
            <el-radio v-for="p in protocols" :key="p.value" :value="p.value">{{ p.label }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.desc" placeholder="可选" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="create">
          {{ submitting ? '注册中…' : '注册' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- API 网关路由规则（治理四件套之 API 网关） -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">API 网关路由</span>
        <el-button size="small" @click="openRoute">+ 创建路由</el-button>
      </div>
      <div class="block-desc">填了「对外域名」的路由会自动下发为集群 Ingress（Host + 路径 → 目标服务端口），经 Ingress Controller 对外暴露；未填域名的路由仅作配置记录。</div>
      <el-table :data="routes" size="small" empty-text="暂无路由规则">
        <el-table-column label="名称" min-width="140">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="对外域名" min-width="160">
          <template #default="{ row }">
            <span v-if="row.host" class="mono">{{ row.host }}</span>
            <span v-else class="dim">不限</span>
          </template>
        </el-table-column>
        <el-table-column label="路径" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.path }}</span></template>
        </el-table-column>
        <el-table-column label="目标服务" min-width="140">
          <template #default="{ row }">{{ serviceName(row.serviceId) }}</template>
        </el-table-column>
        <el-table-column label="方法" width="160">
          <template #default="{ row }">
            <el-tag v-for="m in row.methods" :key="m" size="small" style="margin-right:4px">{{ m }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="剥离前缀" width="90">
          <template #default="{ row }">{{ row.stripPath ? '是' : '否' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? '启用' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button text :type="row.enabled ? 'warning' : 'success'" size="small" @click="toggleRoute(row)">
              {{ row.enabled ? '禁用' : '启用' }}
            </el-button>
            <el-button text type="danger" size="small" @click="deleteRoute(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 熔断器（治理四件套之熔断）：状态由后端即时评估填充 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">熔断器</span>
        <el-button size="small" @click="openBreaker">+ 创建熔断器</el-button>
      </div>
      <el-table :data="breakers" size="small" empty-text="暂无熔断规则">
        <el-table-column label="名称" min-width="140">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="目标服务" min-width="130">
          <template #default="{ row }">{{ serviceName(row.serviceId) }}</template>
        </el-table-column>
        <el-table-column label="策略" width="100">
          <template #default="{ row }">
            {{ row.strategy === 'slow_call' ? '慢调用率' : '错误率' }}
          </template>
        </el-table-column>
        <el-table-column label="阈值/窗口" width="120">
          <template #default="{ row }">≥{{ row.threshold }}% / {{ row.windowSecs }}s</template>
        </el-table-column>
        <el-table-column label="即时统计" min-width="170">
          <template #default="{ row }">
            <span v-if="row.enabled" class="mono">
              请求 {{ row.stats.requests }}
              · {{ row.strategy === 'slow_call' ? '慢调用' : '失败' }}
              {{ row.strategy === 'slow_call' ? row.stats.slowCalls : row.stats.failures }}
              · {{ row.stats.rate }}%
            </span>
            <span v-else class="dim">已禁用</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag
              v-if="row.enabled"
              :type="(stateMeta[row.state]?.type) || 'info'"
              size="small"
            >
{{ stateMeta[row.state]?.label || row.state }}
</el-tag>
            <el-tag v-else type="info" size="small">停用</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button text :type="row.enabled ? 'warning' : 'success'" size="small" @click="toggleBreaker(row)">
              {{ row.enabled ? '停用' : '启用' }}
            </el-button>
            <el-button text type="danger" size="small" @click="deleteBreaker(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 路由创建弹窗 -->
    <el-dialog v-model="showRoute" title="创建路由规则" width="500px">
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="routeForm.name" placeholder="租户内唯一，如 chat-api" />
        </el-form-item>
        <el-form-item label="对外域名">
          <el-input v-model="routeForm.host" placeholder="可选，如 api.acme.com；空=不限 Host" />
          <div class="form-hint">填 Host 后该路由会下发到集群 Ingress（{{ ingressHint }}）对外暴露；留空则不下发。</div>
        </el-form-item>
        <el-form-item label="路径">
          <el-input v-model="routeForm.path" placeholder="如 /api/v1/chat/*" />
          <div class="form-hint">Host + 路径会下发为标准 K8s Ingress 规则，转发到目标服务端口。</div>
        </el-form-item>
        <el-form-item label="目标服务">
          <el-select v-model="routeForm.serviceId" style="width: 100%">
            <el-option v-for="s in services" :key="s.id" :label="s.name" :value="s.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="方法">
          <el-select v-model="routeForm.methods" multiple style="width: 100%">
            <el-option v-for="m in methodOpts" :key="m" :label="m" :value="m" />
          </el-select>
          <div class="form-hint">标准 K8s Ingress 不支持按 HTTP 方法过滤，此字段仅作配置记录，不影响实际下发。</div>
        </el-form-item>
        <el-form-item label="剥离前缀">
          <el-switch v-model="routeForm.stripPath" />
          <div class="form-hint">标准 K8s Ingress 不支持前缀剥离（需 ingress controller 专属注解），此开关仅作配置记录。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRoute = false">取消</el-button>
        <el-button type="primary" :disabled="routeSubmitting" @click="createRoute">
          {{ routeSubmitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- 熔断器创建弹窗 -->
    <el-dialog v-model="showBreaker" title="创建熔断器" width="500px">
      <el-form label-width="100px">
        <el-form-item label="名称">
          <el-input v-model="breakerForm.name" placeholder="租户内唯一，如 cs-error-breaker" />
        </el-form-item>
        <el-form-item label="目标服务">
          <el-select v-model="breakerForm.serviceId" style="width: 100%">
            <el-option v-for="s in services" :key="s.id" :label="s.name" :value="s.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="策略">
          <el-select v-model="breakerForm.strategy" style="width: 100%">
            <el-option v-for="o in strategyOpts" :key="o.value" :label="o.label" :value="o.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="触发阈值">
          <el-input-number v-model="breakerForm.threshold" :min="1" :max="100" />
          <span class="hint">% 达到即熔断</span>
        </el-form-item>
        <el-form-item label="最少样本">
          <el-input-number v-model="breakerForm.minRequests" :min="1" />
          <span class="hint">窗口内不足此数不熔断</span>
        </el-form-item>
        <el-form-item label="统计窗口">
          <el-input-number v-model="breakerForm.windowSecs" :min="1" />
          <span class="hint">秒</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showBreaker = false">取消</el-button>
        <el-button type="primary" :disabled="breakerSubmitting" @click="createBreaker">
          {{ breakerSubmitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.gov-page {
  max-width: 1100px;
  margin: 0 auto;
}
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 18px;
}
.page-head h2 {
  margin: 0 0 4px;
  font-size: 18px;
}
.sub {
  margin: 0;
  font-size: 12.5px;
  color: var(--text-dim);
}
.svc-link {
  font-weight: 600;
  color: var(--brand);
  cursor: pointer;
}
.svc-link:hover {
  text-decoration: underline;
}
.block { margin-top: 24px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-title { font-size: 14px; font-weight: 600; }
.block-desc { font-size: 12px; color: var(--text-faint); margin: -4px 0 10px; line-height: 1.5; }
.form-hint { font-size: 11.5px; color: var(--text-faint); line-height: 1.5; margin-top: 2px; }
.mono { font-family: var(--font-mono); }
.dim { color: var(--text-dim); }
.hint { margin-left: 8px; font-size: 12px; color: var(--text-dim); }
</style>
