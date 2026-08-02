<script setup lang="ts">
// 资源中心 -> 数据服务详情。
// 基本信息 + 连接信息（敏感字段后端掩码，前端不可 reveal）+ 监控指标（4 指标卡 + sparkline，10s 轮询）
// + 告警规则（针对该数据服务，targetType=dataservice）。
// 详情接口 GET /api/dataservices/{id} 返回掩码连接信息（password/secretKey/token/uri 敏感字段掩码）；
// host/port/user/database 等非敏感字段明文可复制；应用绑定经后端自动注入 appconfig，无需手动复制凭证。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

interface SpecField { key: string; label: string; type: string; options?: string[]; default: string }
interface KindMeta { kind: string; label: string; icon: string; fields: SpecField[] }
interface DataService {
  id: string; kind: string; name: string
  spec: Record<string, string>
  connection?: Record<string, string>
  status: string; envId: string; createdAt: string; updatedAt: string
}
interface MetricPoint { ts: string; value: number }
interface MetricSeries {
  targetType: string; targetId: string; name: string; unit: string
  current: number; points: MetricPoint[]
}
interface AlertRule {
  id: string; name: string; metricName: string; targetType: string; targetId?: string
  operator: string; threshold: number; severity: string; enabled: boolean
}

const props = defineProps<{ kind: string; id: string }>()
const router = useRouter()
const envStore = useEnvStore()

// 敏感字段掩码（与后端 SecretMask 一致，前端独立常量避免跨层耦合）。
// uri 含 user:password@ 整串掩码（后端 MaskConnection 已掩码，前端固定显示占位）。
const SECRET_MASK = '••••••'
const SECRET_KEYS = new Set(['password', 'secretKey', 'token', 'uri'])

const metas = ref<KindMeta[]>([])
const ds = ref<DataService | null>(null)
const metrics = ref<MetricSeries[]>([])
const rules = ref<AlertRule[]>([])
const loading = ref(false)
const errorMsg = ref('') // 加载失败提示（404/网络错误），避免静默空状态

const STATUS_LABEL: Record<string, string> = { running: '运行中', stopped: '已停止', creating: '创建中' }
const STATUS_TYPE: Record<string, string> = { running: 'success', stopped: 'info', creating: 'warning' }

// 按 kind 定义连接字段顺序与标签（敏感字段标记 secret）。
// 任务约束：host/port 通用；db=user/database/password/uri；cache=password/uri；
// mq=token/uri；storage=accessKey/secretKey/endpoint。vector/search 复用 cache 形态。
const FIELD_DEFS: Record<string, { key: string; label: string; secret?: boolean }[]> = {
  db: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'user', label: '用户名' },
    { key: 'database', label: '数据库' },
    { key: 'password', label: '密码', secret: true },
    { key: 'uri', label: '连接 URI', secret: true },
  ],
  cache: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'password', label: '密码', secret: true },
    { key: 'uri', label: '连接 URI', secret: true },
  ],
  mq: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'token', label: 'Token', secret: true },
    { key: 'uri', label: '连接 URI', secret: true },
  ],
  storage: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'endpoint', label: 'Endpoint' },
    { key: 'accessKey', label: 'Access Key' },
    { key: 'secretKey', label: 'Secret Key', secret: true },
  ],
  vector: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'password', label: '密码', secret: true },
    { key: 'uri', label: '连接 URI', secret: true },
  ],
  search: [
    { key: 'host', label: '主机' },
    { key: 'port', label: '端口' },
    { key: 'password', label: '密码', secret: true },
    { key: 'uri', label: '连接 URI', secret: true },
  ],
}

const meta = computed(() => metas.value.find((m) => m.kind === props.kind) ?? null)
const kindLabel = computed(() => meta.value?.label ?? props.kind)
const envLabel = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

// 按 FIELD_DEFS 顺序渲染连接字段；connection 中存在但未列出的字段兜底追加在末尾。
const connectionFields = computed(() => {
  const conn = ds.value?.connection ?? {}
  const defs = FIELD_DEFS[props.kind] ?? []
  const known = new Set(defs.map((d) => d.key))
  const extras = Object.keys(conn).filter((k) => !known.has(k) && conn[k] !== '' && conn[k] !== undefined)
  return [
    ...defs.filter((d) => conn[d.key] !== undefined && conn[d.key] !== ''),
    ...extras.map((k) => ({ key: k, label: k })),
  ]
})

// 指标卡：4 个（CPU/内存/RPS/延迟），未返回的指标显示「-」
const metricOrder = ['cpu', 'mem', 'rps', 'latency']
const metricLabel: Record<string, string> = { cpu: 'CPU', mem: '内存', rps: '请求/秒', latency: 'P95 延迟' }
const cards = computed(() =>
  metricOrder.map((name) => {
    const m = metrics.value.find((x) => x.name === name)
    return {
      name,
      label: metricLabel[name],
      unit: m?.unit ?? '',
      current: m?.current as number | undefined,
      points: m?.points ?? [],
    }
  }),
)
const hasMetrics = computed(() => cards.value.some((c) => c.current !== undefined))

const fmtVal = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1))

// sparkline：把 points 映射成 20-100% 的高度数组（取最近 24 点）
function sparkHeights(points: MetricPoint[]): number[] {
  if (points.length < 2) return []
  const vals = points.map((p) => p.value)
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  return vals.slice(-24).map((v) => 20 + ((v - min) / span) * 80)
}

function isSecret(key: string): boolean {
  return SECRET_KEYS.has(key)
}

function fieldValue(key: string, val: string): string {
  // 敏感字段（password/secretKey/token/uri）后端已掩码，前端固定显示 SecretMask（不可 reveal）
  if (isSecret(key)) return SECRET_MASK
  return val
}

// 复制：敏感字段后端掩码（SecretMask）无复制意义，提示走应用配置；非敏感字段复制明文
async function copyField(key: string, val: string) {
  if (isSecret(key)) {
    ElMessage.info('敏感字段已掩码，连接信息已通过应用绑定自动注入应用配置')
    return
  }
  try {
    await navigator.clipboard.writeText(val)
    ElMessage.success('已复制到剪贴板')
  } catch {
    ElMessage.error('复制失败，请手动选择文本复制')
  }
}

async function loadMeta() {
  const resp = await fetchAuth('/api/dataservices/meta')
  if (resp.ok) metas.value = (await resp.json()).data ?? []
}

async function loadDetail() {
  const resp = await fetchAuth(`/api/dataservices/${props.id}`)
  if (resp.ok) {
    ds.value = (await resp.json()).data ?? null
    if (!ds.value) errorMsg.value = '数据服务不存在'
  } else if (resp.status === 404) {
    ds.value = null
    errorMsg.value = '数据服务不存在或已被删除'
  } else {
    ds.value = null
    errorMsg.value = '加载数据服务失败'
  }
}

async function loadMetrics() {
  // targetId 用数据服务 id（= K8s 资源名/Pod 名 <id>-0）：reconciler 以 d.ID 建 Service/STS，
  // memory seed targetId 也是 d.ID（如 ds-acme-db）。领域 name 非 K8s 资源名，用 name 会查空。
  if (!ds.value?.id) return
  const resp = await fetchAuth(`/api/observability/metrics?targetType=dataservice&targetId=${encodeURIComponent(ds.value.id)}`)
  if (resp.ok) metrics.value = (await resp.json()).data ?? []
  else metrics.value = []
}

async function loadRules() {
  const resp = await fetchAuth('/api/observability/alert-rules?targetType=dataservice')
  if (resp.ok) rules.value = (await resp.json()).data ?? []
}

// 针对该数据服务的告警规则（targetId 为空=全部 dataservice，或等于当前 ds.id）
const dsRules = computed(() =>
  rules.value.filter((r) => !r.targetId || r.targetId === ds.value?.id),
)

let timer: number | undefined
let alive = false

async function loadPollingData() {
  if (!alive) return
  await Promise.all([loadMetrics(), loadRules()])
}

async function bootstrap() {
  // 重入（watch 切换 ds）时先停旧轮询回调 + 清旧状态，避免 timer 与 bootstrap 并发写状态覆盖。
  alive = false
  errorMsg.value = ''
  ds.value = null
  metrics.value = []
  rules.value = []
  loading.value = true
  try {
    if (!metas.value.length) await loadMeta()
    await loadDetail()
    if (!ds.value) return // 加载失败（errorMsg 已设），不启轮询
    alive = true
    await loadPollingData()
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await bootstrap()
  // 10s 轮询指标/告警（与 Observability 同款）
  timer = window.setInterval(loadPollingData, 10000)
})

onUnmounted(() => {
  // alive 守卫：防止卸载后异步回调仍写状态
  alive = false
  if (timer) window.clearInterval(timer)
})

// 切换数据服务（kind/id 变化）时重新加载
watch([() => props.kind, () => props.id], () => { bootstrap() })

// 告警规则创建弹窗
const showRule = ref(false)
const ruleForm = ref({
  name: '', metricName: 'cpu', operator: '>', threshold: 80, severity: 'warning', enabled: true,
})
const ruleSubmitting = ref(false)
const metricsOpts = [
  { value: 'cpu', label: 'CPU' },
  { value: 'mem', label: '内存' },
  { value: 'rps', label: '请求/秒' },
  { value: 'latency', label: 'P95 延迟' },
]
const ops = [
  { value: '>', label: '> 大于' },
  { value: '>=', label: '≥ 大于等于' },
  { value: '<', label: '< 小于' },
  { value: '<=', label: '≤ 小于等于' },
]
const severities = [
  { value: 'critical', label: '严重' },
  { value: 'warning', label: '警告' },
]

function openRule() {
  ruleForm.value = {
    name: '', metricName: 'cpu', operator: '>', threshold: 80,
    severity: 'warning', enabled: true,
  }
  showRule.value = true
}

async function saveRule() {
  if (!ruleForm.value.name.trim()) {
    ElMessage.warning('请填写规则名称')
    return
  }
  if (!ds.value?.id) {
    ElMessage.warning('数据服务未加载')
    return
  }
  ruleSubmitting.value = true
  try {
    const resp = await fetchAuth('/api/observability/alert-rules', {
      method: 'POST',
      body: JSON.stringify({
        name: ruleForm.value.name,
        metricName: ruleForm.value.metricName,
        targetType: 'dataservice',
        targetId: ds.value.id, // = K8s 资源名/monitoring targetId（与 dsRules 过滤一致）
        operator: ruleForm.value.operator,
        threshold: ruleForm.value.threshold,
        severity: ruleForm.value.severity,
        enabled: ruleForm.value.enabled,
      }),
    })
    if (resp.ok) {
      ElMessage.success('规则已创建')
      showRule.value = false
      loadRules()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败')
    }
  } finally {
    ruleSubmitting.value = false
  }
}

async function deleteRule(r: AlertRule) {
  const resp = await fetchAuth(`/api/observability/alert-rules/${r.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已删除')
    loadRules()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '删除失败')
  }
}
</script>

<template>
  <div class="ds-detail-page">
    <button class="back" @click="router.push(`/resources/${kind}`)">← 返回{{ kindLabel }}列表</button>

    <div v-if="ds" class="page-head">
      <h2>{{ ds.name }}</h2>
      <p class="sub">资源中心 · {{ kindLabel }} 详情</p>
    </div>

    <!-- 加载失败兜底（404/网络错误），避免静默空状态 -->
    <section v-if="errorMsg && !loading" class="block">
      <el-empty :description="errorMsg" :image-size="64" />
      <div class="err-actions">
        <el-button size="small" @click="bootstrap">重试</el-button>
        <el-button size="small" @click="router.push(`/resources/${kind}`)">返回列表</el-button>
      </div>
    </section>

    <!-- 基本信息 -->
    <section v-if="!errorMsg" class="block" v-loading="loading">
      <div class="block-title">基本信息</div>
      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="类型">{{ kindLabel }}</el-descriptions-item>
        <el-descriptions-item label="名称">
          <span class="mono">{{ ds?.name }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag v-if="ds" :type="(STATUS_TYPE[ds.status] as any) || 'info'" size="small">
            {{ STATUS_LABEL[ds?.status] || ds?.status }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="环境">{{ envLabel(ds?.envId ?? '') }}</el-descriptions-item>
        <el-descriptions-item label="创建时间">
          {{ ds ? new Date(ds.createdAt).toLocaleString() : '-' }}
        </el-descriptions-item>
        <el-descriptions-item label="ID">
          <span class="mono faint">{{ ds?.id }}</span>
        </el-descriptions-item>
      </el-descriptions>
    </section>

    <!-- 连接信息 -->
    <section class="block" v-if="!errorMsg && connectionFields.length">
      <div class="block-title">连接信息</div>
      <el-descriptions :column="1" border size="small">
        <el-descriptions-item v-for="f in connectionFields" :key="f.key" :label="f.label">
          <div class="conn-cell">
            <span class="mono conn-val">{{ fieldValue(f.key, ds?.connection?.[f.key] ?? '') }}</span>
            <span class="conn-actions">
              <el-button text size="small" @click="copyField(f.key, ds?.connection?.[f.key] ?? '')">
                📋 复制
              </el-button>
            </span>
          </div>
        </el-descriptions-item>
      </el-descriptions>
    </section>
    <section class="block" v-else-if="!errorMsg && !loading">
      <div class="block-title">连接信息</div>
      <el-empty description="暂无连接信息" :image-size="48" />
    </section>

    <!-- 监控指标 -->
    <section v-if="!errorMsg" class="block">
      <div class="block-title">监控指标 · {{ ds?.name ?? '' }}</div>
      <div v-if="hasMetrics" class="metric-grid">
        <div v-for="c in cards" :key="c.name" class="metric-card">
          <div class="m-label">{{ c.label }}</div>
          <div v-if="c.current !== undefined" class="m-value mono">
            {{ fmtVal(c.current) }}<span class="m-unit">{{ c.unit }}</span>
          </div>
          <div v-else class="m-value mono">-</div>
          <div class="spark" v-if="c.points.length >= 2">
            <span
              v-for="(h, idx) in sparkHeights(c.points)"
              :key="idx"
              class="spark-bar"
              :style="{ height: h + '%' }"
            />
          </div>
        </div>
      </div>
      <el-empty v-else description="暂无监控数据" :image-size="48" />
    </section>

    <!-- 告警规则 -->
    <section v-if="!errorMsg" class="block">
      <div class="block-head">
        <span class="block-title">告警规则（当前数据服务）</span>
        <el-button type="primary" size="small" @click="openRule">+ 新建规则</el-button>
      </div>
      <el-table :data="dsRules" size="small" empty-text="该数据服务暂无告警规则">
        <el-table-column prop="name" label="规则名" min-width="160" />
        <el-table-column label="条件" min-width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.metricName }} {{ row.operator }} {{ row.threshold }}</span>
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="row.severity === 'critical' ? 'danger' : 'warning'" size="small">
              {{ row.severity === 'critical' ? '严重' : '警告' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '停用' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button text type="danger" size="small" @click="deleteRule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 规则创建弹窗 -->
    <el-dialog v-model="showRule" title="创建告警规则" width="480px">
      <el-form label-width="80px">
        <el-form-item label="规则名">
          <el-input v-model="ruleForm.name" placeholder="如 CPU 偏高" />
        </el-form-item>
        <el-form-item label="目标">
          <el-input :model-value="`dataservice / ${ds?.name ?? ''}`" disabled />
        </el-form-item>
        <el-form-item label="指标">
          <el-select v-model="ruleForm.metricName" style="width: 100%">
            <el-option v-for="m in metricsOpts" :key="m.value" :label="m.label" :value="m.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="条件">
          <div class="cond-row">
            <el-select v-model="ruleForm.operator" style="width: 140px">
              <el-option v-for="o in ops" :key="o.value" :label="o.label" :value="o.value" />
            </el-select>
            <el-input-number v-model="ruleForm.threshold" :min="0" style="flex: 1" />
          </div>
        </el-form-item>
        <el-form-item label="级别">
          <el-radio-group v-model="ruleForm.severity">
            <el-radio v-for="s in severities" :key="s.value" :value="s.value">{{ s.label }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="ruleForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRule = false">取消</el-button>
        <el-button type="primary" :disabled="ruleSubmitting" @click="saveRule">
          {{ ruleSubmitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ds-detail-page { max-width: 1100px; margin: 0 auto; }
.back { border: none; background: transparent; color: var(--text-faint); font-family: inherit; font-size: 13px; cursor: pointer; margin-bottom: 12px; }
.back:hover { color: var(--text); }
.page-head { margin-bottom: 20px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.block { margin-bottom: 24px; }
.block-title { display: flex; align-items: center; gap: 10px; font-size: 14px; font-weight: 600; margin-bottom: 10px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-head .block-title { margin-bottom: 0; }
.mono { font-family: var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace); }
.faint { color: var(--text-faint); }

.conn-cell { display: flex; align-items: center; gap: 8px; }
.conn-val { flex: 1; word-break: break-all; }
.conn-actions { display: flex; gap: 4px; flex-shrink: 0; }

.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.metric-card { padding: 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); }
.m-label { font-size: 12px; color: var(--text-dim); }
.m-value { font-size: 26px; font-weight: 700; letter-spacing: -0.02em; margin-top: 2px; }
.m-unit { font-size: 13px; font-weight: 400; color: var(--text-faint); margin-left: 4px; }
.spark { display: flex; align-items: flex-end; gap: 2px; height: 36px; margin-top: 8px; }
.spark-bar { flex: 1; background: var(--brand); opacity: 0.7; border-radius: 2px 2px 0 0; min-width: 2px; }
.cond-row { display: flex; gap: 8px; width: 100%; }
.err-actions { display: flex; justify-content: center; gap: 8px; margin-top: 12px; }
</style>
