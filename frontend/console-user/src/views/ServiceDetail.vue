<script setup lang="ts">
// 服务治理 → 服务详情：实例列表（发现）+ 注册/注销实例 + 心跳。
// 生产注册/注销实例受 prod:write 保护（后端）；前端注销走 confirmDangerous（生产输入地址确认）。
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const route = useRoute()
const router = useRouter()

interface Service {
  id: string
  name: string
  appId?: string
  envId: string
  protocol: string
  port: number
  desc?: string
}
interface Instance {
  id: string
  serviceId: string
  addr: string
  status: string
  laneId: string
  updatedAt: string
}
interface Env { id: string; name: string; type: string }
interface Published { published: boolean; version?: number; snapshot?: Record<string, string> }
interface ConfigNs { id: string; name: string; desc?: string; published: Published }

const svc = ref<Service | null>(null)
const instances = ref<Instance[]>([])
const instancesDiscovered = ref(false) // 实例来自数据面 Endpoint（discovered）时隐藏「心跳」按钮
const envs = ref<Env[]>([])
const configNs = ref<ConfigNs[]>([])
const loading = ref(false)
const showCreate = ref(false)
const submitting = ref(false)
const form = ref({ addr: '', laneId: 'default' })

const envName = (id: string) => envs.value.find((e) => e.id === id)?.name ?? id
const isProd = () => envs.value.find((e) => e.id === svc.value?.envId)?.type === 'prod'

async function loadEnvs() {
  const resp = await fetchAuth('/api/environments')
  if (resp.ok) envs.value = (await resp.json()).data ?? []
}

async function load() {
  const id = route.params.id as string
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/services/${id}`)
    if (resp.ok) {
      const json = await resp.json()
      // 兼容两种契约：fetchJSON 智能解包 {data:T}；此端点历史形态 {service,instances}。
      // 双重兜底：data 形态优先，否则原样。
      const payload = json && typeof json === 'object' && 'service' in json ? json : (json?.data ?? {})
      svc.value = payload.service ?? null
      instances.value = payload.instances ?? []
      // 实例来源：discovered=数据面 Endpoint 真源（readiness probe 维活，心跳无意义）；
      // manual=手动注册表（应用上报，心跳维活）。discovered 模式隐藏「心跳」按钮。
      instancesDiscovered.value = payload.instancesSource === 'discovered'
    } else if (resp.status === 404) {
      // 已删实体：明确引导返回，不留静默空页（书签/外链进来 404 可感知）。
      ElMessage.error('服务不存在或已删除')
      router.push('/platform/governance')
      return
    }
    await loadConfigNs()
  } catch (e) {
    ElMessage.error('加载服务详情失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

// 关联配置：拉该服务关联的 configcenter namespaces + 各自 active 配置（双向显示）。
async function loadConfigNs() {
  const id = route.params.id as string
  if (!id) return
  try {
    const resp = await fetchAuth(`/api/configcenter/namespaces?serviceId=${id}`)
    if (!resp.ok) return
    const list: { id: string; name: string; desc?: string }[] = (await resp.json()).data ?? []
    configNs.value = await Promise.all(list.map(async (ns) => {
      const pr = await fetchAuth(`/api/configcenter/namespaces/${ns.id}/published`)
      const pub: Published = pr.ok ? await pr.json() : { published: false }
      return { id: ns.id, name: ns.name, desc: ns.desc, published: pub }
    }))
  } catch {
    configNs.value = []
  }
}

function openCreate() {
  form.value = { addr: '', laneId: 'default' }
  showCreate.value = true
}

async function create() {
  if (!form.value.addr.trim()) {
    ElMessage.warning('请填写实例地址')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth(`/api/services/${svc.value!.id}/instances`, {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    if (resp.ok) {
      ElMessage.success('已注册实例')
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

async function heartbeat(row: Instance) {
  try {
    const resp = await fetchAuth(`/api/instances/${row.id}/heartbeat`, { method: 'PUT' })
    if (resp.ok) {
      ElMessage.success('心跳已更新')
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '心跳失败')
    }
  } catch (e) {
    ElMessage.error('心跳失败：' + (e as Error).message)
  }
}

async function remove(row: Instance) {
  const ok = await confirmDangerous({
    action: '注销实例',
    target: row.addr,
    requireNameConfirm: isProd(),
    isProd: isProd(),
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/services/${svc.value!.id}/instances/${row.id}`, { method: 'DELETE' })
    if (resp.ok) {
      ElMessage.success('已注销实例')
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '注销失败')
    }
  } catch (e) {
    ElMessage.error('注销失败：' + (e as Error).message)
  }
}

onMounted(async () => {
  await Promise.all([loadEnvs(), load()])
})
watch(() => route.params.id, load)
</script>

<template>
  <div class="gov-page">
    <button class="back" @click="router.push('/platform/governance')">← 返回服务列表</button>
    <div v-if="svc" class="svc-head">
      <h2>{{ svc.name }}</h2>
      <div class="svc-meta">
        <el-tag size="small" :type="svc.protocol === 'grpc' ? 'warning' : 'info'">{{ svc.protocol.toUpperCase() }}:{{ svc.port }}</el-tag>
        <span class="kv">环境：<b>{{ envName(svc.envId) }}</b></span>
        <span v-if="svc.appId" class="kv">应用：<b>{{ svc.appId }}</b></span>
        <span v-if="svc.desc" class="kv">{{ svc.desc }}</span>
      </div>
    </div>

    <div class="inst-head">
      <span class="inst-title">服务实例（{{ instances.length }}）</span>
      <el-button type="primary" size="small" @click="openCreate">+ 注册实例</el-button>
    </div>

    <el-table :data="instances" v-loading="loading" size="default" empty-text="该服务暂无实例">
      <el-table-column label="地址" min-width="200">
        <template #default="{ row }"><span class="mono">{{ row.addr }}</span></template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag :type="row.status === 'healthy' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="laneId" label="泳道" width="110" />
      <el-table-column label="最后心跳" width="180">
        <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150">
        <template #default="{ row }">
          <!-- discovered 模式实例由 readiness probe 维活，应用主动心跳无意义（去手动表查必 not found） -->
          <el-button v-if="!instancesDiscovered" text type="primary" size="small" @click="heartbeat(row)">心跳</el-button>
          <el-button text type="danger" size="small" @click="remove(row)">注销</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 实例来源说明 -->
    <p v-if="instancesDiscovered" class="inst-hint">
      实例来自数据面 K8s Endpoints（就绪探针驱动，自动维活，无需手动心跳）
    </p>

    <!-- 关联配置（配置中心双向显示） -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">关联配置（配置中心）</span>
      </div>
      <el-empty v-if="!configNs.length" description="无关联配置命名空间" :image-size="48" />
      <div v-else class="config-ns-list">
        <div v-for="ns in configNs" :key="ns.id" class="config-ns-card">
          <div class="config-ns-head">
            <a class="link" @click="router.push(`/platform/config-center/${ns.id}`)">{{ ns.name }}</a>
            <span v-if="ns.published.published" class="ver-tag mono">v{{ ns.published.version }}</span>
            <span v-else class="dim">未发布</span>
          </div>
          <div v-if="ns.published.published && Object.keys(ns.published.snapshot || {}).length" class="kv-list">
            <div v-for="(v, k) in ns.published.snapshot" :key="k" class="kv-row">
              <span class="kv-key mono">{{ k }}</span>
              <span class="kv-val mono">{{ v }}</span>
            </div>
          </div>
          <div v-else class="dim" style="margin-top:6px;font-size:12px">无配置项</div>
        </div>
      </div>
    </section>

    <el-dialog v-model="showCreate" title="注册实例" width="440px">
      <el-form label-width="80px">
        <el-form-item label="地址">
          <el-input v-model="form.addr" placeholder="host:port，如 10.0.1.20:8080" />
        </el-form-item>
        <el-form-item label="泳道">
          <el-input v-model="form.laneId" placeholder="default=基线（本期仅基线）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="create">
          {{ submitting ? '注册中…' : '注册' }}
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
.back {
  border: none;
  background: transparent;
  color: var(--text-faint);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
  margin-bottom: 12px;
}
.back:hover {
  color: var(--text);
}
.svc-head {
  margin-bottom: 20px;
}
.svc-head h2 {
  margin: 0 0 8px;
  font-size: 18px;
}
.svc-meta {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  font-size: 12.5px;
  color: var(--text-dim);
}
.kv b {
  color: var(--text);
}
.inst-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.inst-title {
  font-size: 14px;
  font-weight: 600;
}
.inst-hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.block { margin-top: 24px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-title { font-size: 14px; font-weight: 600; }
.link { font-weight: 600; color: var(--brand); cursor: pointer; }
.link:hover { text-decoration: underline; }
.ver-tag { padding: 2px 8px; background: var(--success-soft); color: var(--success); border-radius: 4px; font-size: 12px; }
.dim { color: var(--text-dim); }
.config-ns-list { display: flex; flex-direction: column; gap: 12px; }
.config-ns-card { padding: 12px 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.config-ns-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.kv-list { display: flex; flex-direction: column; gap: 4px; }
.kv-row { display: flex; gap: 16px; font-size: 12.5px; }
.kv-key { color: var(--brand); min-width: 180px; }
.kv-val { color: var(--text); word-break: break-all; }
</style>
