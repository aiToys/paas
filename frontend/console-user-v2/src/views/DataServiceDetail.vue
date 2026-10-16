<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 数据服务详情（v2 原生，功能对等迁移）：连接信息（掩码+复制）/ 生命周期（启停/重启）/
// 规格+伸缩 / 绑定应用（谁在用）。指标卡与告警关联在 /observability（维度过滤）。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchJSON, fetchAuth, apiError, fetchList } from '@/api'
import { statusOf, tagTypeToCls, DATASERVICE_STATUS } from '@/composables/useStatus'
import { useEnvStore } from '@/stores/env'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
// 路由参数而非 props（router-view 不自动注入，同 RunDetail 模式）。
const id = computed(() => String(route.params.id ?? ''))

interface DataService {
  id: string; kind: string; name: string
  spec: Record<string, string>
  connection?: Record<string, string>
  status: string; envId: string; createdAt: string
  source?: string; engineId?: string
  replicas?: number | null; cpu?: string; memory?: string; storageGb?: number; image?: string
}
interface AppLite { id: string; name: string; bindings?: { type: string; name: string }[] }

const ds = ref<DataService | null>(null)
const boundApps = ref<{ id: string; name: string }[]>([])
const loading = ref(true)
const loadFailed = ref(false)

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    ds.value = await fetchJSON<DataService>(`/api/dataservices/${id.value}`)
    // 绑定应用：拉应用列表过滤 bindings 引用（前端聚合，避免跨模块后端耦合）。
    const apps = await fetchList<AppLite>('/api/applications').catch(() => [])
    boundApps.value = apps.filter((a) => a.bindings?.some((b) => b.name === ds.value?.name || b.name === ds.value?.id))
  } catch (e) {
    loadFailed.value = true
    ElMessage.error(apiError(e, '加载数据服务失败'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

const KIND_LABEL: Record<string, string> = {
  db: '数据库', cache: '缓存', mq: '消息', storage: '对象存储', vector: '向量', search: '搜索',
}
const kindLabel = computed(() => KIND_LABEL[ds.value?.kind ?? ''] ?? ds.value?.kind ?? '')
const envName = computed(() => envStore.envs.find((e) => e.id === ds.value?.envId)?.name ?? ds.value?.envId ?? '-')

// 连接字段渲染顺序（与后端返回对齐；敏感字段已由后端掩码，前端只标注不可复制）。
const FIELD_DEFS: { key: string; label: string; secret?: boolean }[] = [
  { key: 'host', label: '主机' }, { key: 'port', label: '端口' }, { key: 'endpoint', label: 'Endpoint' },
  { key: 'accessKey', label: 'Access Key' }, { key: 'username', label: '用户名' },
  { key: 'password', label: '密码', secret: true }, { key: 'token', label: 'Token', secret: true },
  { key: 'secretKey', label: 'Secret Key', secret: true }, { key: 'uri', label: '连接 URI', secret: true },
  { key: 'database', label: '数据库' },
]
const connectionFields = computed(() => {
  const conn = ds.value?.connection ?? {}
  const known = new Set(FIELD_DEFS.map((d) => d.key))
  const extras = Object.keys(conn).filter((k) => !known.has(k) && conn[k] !== '' && conn[k] !== undefined)
  return [
    ...FIELD_DEFS.filter((d) => conn[d.key] !== undefined && conn[d.key] !== ''),
    ...extras.map((k) => ({ key: k, label: k })),
  ]
})

async function copyText(t: string) {
  try {
    await navigator.clipboard.writeText(t)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

// —— 生命周期（生产 stop/restart 走危险确认） ——
const acting = ref('')
async function lifecycle(action: 'start' | 'stop' | 'restart') {
  const verbs = { start: '启动', stop: '停止', restart: '重启' } as const
  if (action !== 'start' && !await confirmAction(`确认${verbs[action]}「${ds.value?.name}」？${action === 'stop' ? '使用该实例的服务将不可用。' : ''}`)) return
  acting.value = action
  try {
    await fetchAuth(`/api/dataservices/${id.value}/${action}`, { method: 'POST' })
    ElMessage.success(`${verbs[action]}指令已下发`)
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, `${verbs[action]}失败`))
  } finally {
    acting.value = ''
  }
}

// —— 伸缩（replicas） ——
const scaling = ref(false)
const replicas = ref(1)
function openScale() {
  replicas.value = ds.value?.replicas ?? 1
  scaling.value = true
}
const scalingBusy = ref(false)
async function submitScale() {
  scalingBusy.value = true
  try {
    await fetchAuth(`/api/dataservices/${id.value}/scale`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ replicas: replicas.value }),
    })
    ElMessage.success('伸缩指令已下发')
    scaling.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '伸缩失败'))
  } finally {
    scalingBusy.value = false
  }
}

async function removeDs() {
  if (!ds.value) return
  if (!await confirmAction(`删除数据服务「${ds.value.name}」？数据与实例将一并回收，不可恢复。`)) return
  try {
    await fetchAuth(`/api/dataservices/${id.value}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    router.push('/dataservices')
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

const fmtTime = (iso: string) => (iso ?? '').replace('T', ' ').slice(0, 16)
</script>

<template>
  <div class="page">
    <div class="head-row">
      <a class="back" @click="router.push('/dataservices')">← 数据服务</a>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载实例…"></div>
    <div v-else-if="loadFailed || !ds" class="empty">实例不存在或已被删除</div>

    <template v-else>
      <!-- 头部：标识 + 状态 + 操作 -->
      <div class="card head-card">
        <div class="head-main">
          <div class="head-title-row">
            <h2 class="name">{{ ds.name }}</h2>
            <span class="kind-chip">{{ kindLabel }}</span>
            <span class="status" :class="tagTypeToCls(statusOf(DATASERVICE_STATUS, ds.status).type)">
              {{ statusOf(DATASERVICE_STATUS, ds.status).label }}
            </span>
          </div>
          <div class="meta-row">
            <span class="meta">环境：{{ envName }}</span>
            <span v-if="ds.source" class="meta">来源：{{ ds.source === 'managed' ? '平台托管' : ds.source }}</span>
            <span class="meta">创建于 {{ fmtTime(ds.createdAt) }}</span>
          </div>
        </div>
        <div class="head-acts">
          <button
            v-if="ds.status === 'running'" class="act danger"
            :disabled="!!acting" @click="lifecycle('stop')"
          >
{{ acting === 'stop' ? '停止中…' : '停止' }}
</button>
          <button
            v-else-if="ds.status === 'stopped'" class="act primary"
            :disabled="!!acting" @click="lifecycle('start')"
          >
{{ acting === 'start' ? '启动中…' : '启动' }}
</button>
          <button class="act" :disabled="!!acting" @click="lifecycle('restart')">
            {{ acting === 'restart' ? '重启中…' : '重启' }}
          </button>
          <button class="act" @click="openScale">伸缩</button>
          <button class="act danger" @click="removeDs">删除</button>
        </div>
      </div>

      <!-- 连接信息（使用刚需：绑定后从这拿连接串） -->
      <div class="card">
        <div class="card-head">
          <span class="card-title">连接信息</span>
          <span class="card-sub">敏感字段已掩码（平台侧注入应用配置，无需明文）</span>
        </div>
        <div v-if="!connectionFields.length" class="empty-hint">暂无连接信息（实例拉起后生成）</div>
        <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="kv-table">
          <tbody>
            <tr v-for="f in connectionFields" :key="f.key">
              <th>{{ ('secret' in f && f.secret) || /password|token|secret|uri/i.test(f.key) ? f.label : f.label }}</th>
              <td>
                <span class="mono" :class="{ dim: 'secret' in f && f.secret }">{{ ds.connection?.[f.key] }}</span>
              </td>
              <td class="col-act">
                <button
                  v-if="!('secret' in f && f.secret) && ds.connection?.[f.key]"
                  class="act small" @click="copyText(ds.connection![f.key])"
                >
复制
</button>
              </td>
            </tr>
          </tbody>
        </table>
</div>
      </div>

      <!-- 规格 -->
      <div class="card">
        <div class="card-head">
          <span class="card-title">规格</span>
        </div>
        <div class="spec-grid">
          <div class="spec-item"><span class="s-label">副本数</span><span class="s-value mono">{{ ds.replicas ?? '-' }}</span></div>
          <div class="spec-item"><span class="s-label">CPU</span><span class="s-value mono">{{ ds.cpu || '-' }}</span></div>
          <div class="spec-item"><span class="s-label">内存</span><span class="s-value mono">{{ ds.memory || '-' }}</span></div>
          <div class="spec-item"><span class="s-label">存储</span><span class="s-value mono">{{ ds.storageGb ? ds.storageGb + ' GB' : '-' }}</span></div>
          <div v-if="ds.image" class="spec-item wide"><span class="s-label">镜像</span><span class="s-value mono">{{ ds.image }}</span></div>
        </div>
      </div>

      <!-- 绑定应用（谁在用） -->
      <div class="card">
        <div class="card-head">
          <span class="card-title">绑定应用</span>
          <span class="card-sub">{{ boundApps.length ? `${boundApps.length} 个产品在使用` : '连接信息未注入任何产品' }}</span>
        </div>
        <div v-if="!boundApps.length" class="empty-hint">暂无产品绑定——在产品「设置 → 绑定资源」接入</div>
        <div v-else class="app-list">
          <a v-for="a in boundApps" :key="a.id" class="app-row" @click="router.push(`/products/${a.id}?tab=settings`)">
            <span class="a-name">{{ a.name }}</span>
            <span class="a-open">设置 →</span>
          </a>
        </div>
      </div>
    </template>

    <!-- 伸缩弹窗 -->
    <el-dialog v-model="scaling" title="伸缩" width="400px" append-to-body>
      <el-form label-position="top" @submit.prevent="submitScale">
        <el-form-item label="副本数（0-3）">
          <el-input-number v-model="replicas" :min="0" :max="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="scaling = false">取消</button>
        <button class="act primary" :disabled="scalingBusy" @click="submitScale">{{ scalingBusy ? '下发中…' : '确认' }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import './list-page.css';
.page { display: flex; flex-direction: column; gap: 14px; }
.back { font-size: 12.5px; color: var(--brand); cursor: pointer; text-decoration: none; }
.head-card { display: flex; align-items: flex-start; gap: 16px; }
.head-main { flex: 1; min-width: 0; }
.head-title-row { display: flex; align-items: center; gap: 10px; }
.name { margin: 0; font-size: 17px; font-weight: 600; }
.kind-chip {
  padding: 2px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 11.5px;
  color: var(--text-dim);
}
.meta-row { display: flex; gap: 14px; margin-top: 8px; }
.meta { font-size: 12px; color: var(--text-faint); }
.head-acts { display: flex; gap: 8px; flex-shrink: 0; }
.loading-block { height: 200px; }
.empty { padding: 60px 0; text-align: center; font-size: 13px; color: var(--text-faint); }
.kv-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.kv-table th {
  text-align: left; width: 140px; padding: 8px; font-weight: 500; font-size: 12.5px;
  color: var(--text-faint); border-bottom: 1px solid var(--border); vertical-align: top;
}
.kv-table td { padding: 8px; border-bottom: 1px solid var(--border); word-break: break-all; }
.dim { color: var(--text-faint); }
.col-act { text-align: right; width: 60px; }
.spec-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 10px; }
.spec-item {
  display: flex; flex-direction: column; gap: 4px;
  padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius);
}
.spec-item.wide { grid-column: 1 / -1; }
.s-label { font-size: 11.5px; color: var(--text-faint); }
.s-value { font-size: 13px; word-break: break-all; }
.app-list { display: flex; flex-direction: column; }
.app-row {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 6px; border-bottom: 1px solid var(--border);
  font-size: 12.5px; cursor: pointer;
}
.app-row:last-child { border-bottom: none; }
.app-row:hover { background: var(--surface-2); }
.a-open { margin-left: auto; color: var(--brand); font-size: 11.5px; }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
