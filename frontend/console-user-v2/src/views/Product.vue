<script setup lang="ts">
// 产品页（spec §9.4）：三面 = 服务（默认）/ 交付 / 设置。
// 第一批：服务面真实现（服务表 + 本环境运行态 + 域名 + 下一步动作）；
// 交付/设置低频能力并行期桥接老版对应 tab（诚实占位，不造假数据）。
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import ServiceRunDrawer from '@/components/ServiceRunDrawer.vue'
import ServiceCreateDialog from '@/components/ServiceCreateDialog.vue'
import CdDeployDialog from '@/components/CdDeployDialog.vue'
import ProductDelivery from './ProductDelivery.vue'
import ProductSettings from './ProductSettings.vue'
import { fetchJSON, apiError } from '@/api'
import { listServices, type ServiceEntity } from '@/api/service'
import type { Workload } from '@/api/workload'
import { useEnvStore } from '@/stores/env'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

interface Product {
  id: string
  name: string
  desc?: string
  status?: 'healthy' | 'degraded' | 'idle'
  bindings?: { type: string; name: string; note?: string }[]
}
const product = ref<Product | null>(null)
const loading = ref(true)
const notFound = ref(false)

const services = ref<ServiceEntity[]>([])
const workloads = ref<Workload[]>([])
// 治理 Route host 按服务名索引（正式对外域名真源；workload.domain 是开发/泳道域名）。
const routeHosts = ref<Map<string, Set<string>>>(new Map())

async function load() {
  loading.value = true
  notFound.value = false
  const id = route.params.id as string
  try {
    const p = await fetchJSON<Product>(`/api/applications/${id}`)
    product.value = p
    document.title = `${p.name} · 控制台`
    const [svcs, wls, govSvc, rts] = await Promise.allSettled([
      listServices(id),
      fetchJSON<Workload[]>(`/api/applications/${id}/workloads`),
      fetchJSON<{ id: string; name: string }[]>('/api/services'),
      fetchJSON<{ host: string; serviceId: string; enabled: boolean }[]>('/api/routes'),
    ])
    services.value = svcs.status === 'fulfilled' ? svcs.value : []
    workloads.value = wls.status === 'fulfilled' ? wls.value : []
    // 关联链 Route.serviceId → 治理 Service.name（部署即注册：Name=工作负载名）→ hosts。
    if (govSvc.status === 'fulfilled' && rts.status === 'fulfilled') {
      const nameById = new Map(govSvc.value.map((s) => [s.id, s.name]))
      const m = new Map<string, Set<string>>()
      for (const r of rts.value) {
        if (!r.enabled) continue
        const name = nameById.get(r.serviceId)
        if (!name) continue
        const set = m.get(name) ?? new Set<string>()
        set.add(r.host)
        m.set(name, set)
      }
      routeHosts.value = m
    }
  } catch (e) {
    const msg = apiError(e)
    if (msg.includes('not found') || msg.includes('404')) {
      notFound.value = true
      return
    }
    ElMessage.error(apiError(e, '加载产品失败'))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => route.params.id, load)

// 每服务的本环境基线运行态（当前环境 × 基线泳道的工作负载）。
function runOf(s: ServiceEntity): Workload | undefined {
  return workloads.value.find(
    (w) => w.serviceId === s.id && (!envStore.currentEnv || w.envId === envStore.currentEnv.id),
  )
}

// 每服务的正式域名（治理 Route host；关联键=运行工作负载名，泳道/金丝雀副本同享基线 host）。
function hostsOf(s: ServiceEntity): string[] {
  const wl = runOf(s)
  if (!wl) return []
  const hosts = routeHosts.value.get(wl.name)
  return hosts ? [...hosts] : []
}

// 服务运行抽屉：点行打开（spec §9.5 运行主语——实例/日志/伸缩/暴露在 v2 内闭环）。
// 未部署行点击直接弹 CD 部署确认（v2 内闭环，不跳老版）。
const showRun = ref(false)
const runWlId = ref('')
const showDeploy = ref(false)
const deployHint = ref('')
function openRun(s: ServiceEntity) {
  const wl = runOf(s)
  if (!wl) {
    deployHint.value = s.name
    showDeploy.value = true
    return
  }
  runWlId.value = wl.id
  showRun.value = true
}
// 部署触发成功 → 跳 v2 交付面看运行进度（最近运行列表带轮询）。
function onDeployed() {
  facet.value = '交付'
}
// 新建服务（v2 原生表单）；创建成功后回填服务列表。
const showCreateSvc = ref(false)
async function refreshServices() {
  try {
    services.value = await listServices(route.params.id as string)
  } catch { /* 刷新失败不打断；下次进页面重拉 */ }
}
// 抽屉写操作（伸缩/暴露）后刷新服务表。
async function refreshWorkloads() {
  try {
    workloads.value = await fetchJSON<Workload[]>(`/api/applications/${route.params.id}/workloads`)
  } catch {
    // 刷新失败不打断抽屉；下次进页面重拉
  }
}

const TYPE_LABEL: Record<string, string> = {
  web: 'Web', backend: 'Backend', agent: 'Agent', static: 'Static', cron: 'Cron',
}

// —— 三面 ——
type Facet = '服务' | '交付' | '设置'
const FACET_Q: Record<Facet, string> = { 服务: 'services', 交付: 'delivery', 设置: 'settings' }
// 老版 15 tab 深链兼容（spec §9.4：旧 tab=builds 打开「交付」）：旧 q 值 → 三面。
// 含 v2 自身三个值（services/delivery/settings）——否则 query.tab=delivery 初始解析落「服务」
// 且被 watch(facet) 覆写回 services（RunDetail 返回交付面必现）。
const LEGACY_TAB_FACET: Record<string, Facet> = {
  services: '服务', delivery: '交付', settings: '设置',
  deploy: '服务', instances: '服务', logs: '服务', governance: '服务',
  pipelines: '交付', changes: '交付', builds: '交付', images: '交付', releases: '交付', repositories: '交付',
  bindings: '设置', configs: '设置', members: '设置', usage: '设置',
}
function facetOfQ(q: unknown): Facet {
  if (typeof q !== 'string') return '服务'
  return LEGACY_TAB_FACET[q] ?? '服务'
}
const facet = ref<Facet>(facetOfQ(route.query.tab))
watch(facet, (f) => {
  const q = FACET_Q[f]
  if ((route.query.tab as string) !== q) router.replace({ query: { ...route.query, tab: q } })
})
watch(() => route.query.tab, (q) => {
  const f = facetOfQ(q)
  if (f !== facet.value) facet.value = f
})

// 设置面绑定变更后合并回产品实体（刷新绑定计数）。
function onBindingsUpdated(a: { id: string; bindings?: Product['bindings'] }) {
  if (product.value && a.id === product.value.id) {
    product.value = { ...product.value, bindings: a.bindings }
  }
}
</script>

<template>
  <div class="product-page">
    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载产品…"></div>

    <div v-else-if="notFound" class="empty">
      <Icon name="rocket" :size="28" />
      <p>产品不存在或已删除</p>
      <a class="ghost-btn" @click="router.push('/')">← 返回首页</a>
    </div>

    <template v-else-if="product">
      <div class="crumb">
        <a @click="router.push('/')">← 首页</a><span>/</span><span>{{ product.name }}</span>
        <span v-if="product.status" class="p-status" :class="product.status">
          {{ product.status === 'healthy' ? '健康' : product.status === 'degraded' ? '有异常' : '空闲' }}
        </span>
        <a class="new-svc-btn" @click="showCreateSvc = true">新建服务</a>
      </div>

      <div class="facets">
        <button
          v-for="f in (['服务', '交付', '设置'] as Facet[])"
          :key="f"
          class="facet"
          :class="{ on: facet === f }"
          @click="facet = f"
        >
          {{ f }}
        </button>
      </div>

      <!-- 服务面：运行主语表 -->
      <div v-if="facet === '服务'" class="svc-face">
        <div v-if="!services.length" class="empty">
          <Icon name="server" :size="28" />
          <p>产品还没有服务——服务是真正部署运行的东西</p>
          <a class="primary-btn" @click="showCreateSvc = true">去创建服务</a>
        </div>
        <table v-else class="svc-table">
          <thead>
            <tr>
              <th>服务</th>
              <th>类型</th>
              <th>本环境</th>
              <th>域名</th>
              <th class="col-next"></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="s in services" :key="s.id"
              :class="{ clickable: !!runOf(s) }"
              @click="openRun(s)"
            >
              <td><span class="s-name mono">{{ s.name }}</span></td>
              <td><span class="s-type" :class="s.type">{{ TYPE_LABEL[s.type] ?? s.type }}</span></td>
              <td>
                <template v-if="runOf(s)">
                  <span class="s-run" :class="{ bad: (runOf(s)!.ready ?? 0) < (runOf(s)!.replicas ?? 0) }">
                    {{ runOf(s)!.ready }}/{{ runOf(s)!.replicas }}
                  </span>
                </template>
                <span v-else class="s-idle">未部署</span>
              </td>
              <td>
                <!-- 正式域名（治理 Route）优先；开发域名（workload.domain，泳道/一键暴露）降级显示并标注 -->
                <template v-if="hostsOf(s).length">
                  <a
                    v-for="h in hostsOf(s)" :key="h"
                    :href="`http://${h}`" target="_blank" rel="noopener"
                    class="s-domain mono" @click.stop
                  >{{ h }} ↗</a>
                </template>
                <a
                  v-else-if="runOf(s)?.domain"
                  :href="`http://${runOf(s)!.domain}`" target="_blank" rel="noopener"
                  class="s-domain dev mono" @click.stop
                >{{ runOf(s)!.domain }} <span class="dev-tag">开发</span></a>
                <span v-else class="s-idle">—</span>
              </td>
              <td class="col-next"><span class="s-next" :class="{ idle: !runOf(s) }">{{ runOf(s) ? '查看运行' : '去部署' }}</span></td>
            </tr>
          </tbody>
        </table>
        <p class="env-hint">
          环境信号随顶栏切换（当前：{{ envStore.currentEnv?.name ?? '全部环境' }}；未部署 = 该环境尚无基线运行）。
        </p>
      </div>

      <!-- 交付面：一条龙 + 最近运行（v2 原生）+ 深度操作桥接 -->
      <ProductDelivery v-else-if="facet === '交付'" :app-id="product.id" />

      <!-- 设置面：绑定资源（v2 原生）+ 低频设置桥接 -->
      <ProductSettings v-else :app="product" @updated="onBindingsUpdated" />
    </template>

    <!-- 服务运行抽屉：实例/日志/部署/伸缩/对外暴露（v2 内闭环，不跳老版） -->
    <ServiceRunDrawer
      v-model="showRun"
      :workload-id="runWlId"
      :app-id="product?.id"
      @changed="refreshWorkloads"
      @deployed="onDeployed"
    />
    <!-- 新建服务 / CD 部署（v2 原生，创作路径不跳老版） -->
    <ServiceCreateDialog v-model="showCreateSvc" :app-id="product?.id ?? ''" @created="refreshServices" />
    <CdDeployDialog v-model="showDeploy" :app-id="product?.id" :hint="deployHint" @deployed="onDeployed" />
  </div>
</template>

<style scoped>
.product-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.loading-block { height: 240px; }
.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--text-faint);
}
.crumb a {
  color: var(--brand);
  cursor: pointer;
}
.p-status {
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
}
.p-status.healthy { background: rgba(22, 163, 74, 0.12); color: var(--success, #16a34a); }
.p-status.degraded { background: rgba(244, 63, 94, 0.12); color: var(--danger, #f43f5e); }
.p-status.idle { background: var(--surface-2); color: var(--text-faint); }
.new-svc-btn {
  margin-left: auto;
  padding: 6px 14px;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff !important;
  font-size: 12.5px;
  text-decoration: none;
  cursor: pointer;
}
.new-svc-btn:hover {
  opacity: 0.9;
}
.facets {
  display: flex;
  gap: 2px;
  border-bottom: 1px solid var(--border);
}
.facet {
  position: relative;
  padding: 10px 14px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13.5px;
  cursor: pointer;
}
.facet.on {
  color: var(--text);
  font-weight: 600;
}
.facet.on::after {
  content: '';
  position: absolute;
  left: 14px;
  right: 14px;
  bottom: -1px;
  height: 2px;
  background: var(--brand);
  border-radius: 2px;
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
.empty p { margin: 0; font-size: 13px; }
.ghost-btn {
  padding: 7px 14px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}
.primary-btn {
  padding: 7px 14px;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-size: 13px;
  text-decoration: none;
  cursor: pointer;
}
.svc-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.svc-table th {
  text-align: left;
  padding: 8px 10px;
  color: var(--text-faint);
  font-weight: 500;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
}
.svc-table td {
  padding: 12px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.svc-table tr.clickable { cursor: pointer; }
.svc-table tr.clickable:hover td { background: var(--surface-2); }
.s-name { font-weight: 600; }
.s-type {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--brand-soft);
  color: var(--brand);
}
.s-type.agent { background: rgba(245, 158, 11, 0.12); color: #f59e0b; }
.s-type.cron { background: rgba(6, 182, 212, 0.12); color: #06b6d4; }
.s-run { font-weight: 600; color: var(--success, #16a34a); font-family: 'JetBrains Mono', monospace; }
.s-run.bad { color: var(--warning, #f59e0b); }
.s-idle { color: var(--text-faint); font-size: 12px; }
.s-domain {
  display: block;
  font-size: 12px;
  color: var(--success, #16a34a);
  text-decoration: none;
  line-height: 1.6;
}
.s-domain.dev { color: var(--text-dim); }
.dev-tag {
  padding: 0 4px;
  border: 1px solid var(--border);
  border-radius: 3px;
  font-size: 10px;
  color: var(--text-faint);
}
.s-next { font-size: 12px; color: var(--brand); }
.s-next.idle { color: var(--text-faint); }
.col-next { text-align: right; }
.env-hint {
  margin: 0;
  font-size: 12px;
  color: var(--text-faint);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
