<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchJSON, apiError, fetchList } from '@/api'
import { baselineRunsFor } from '@/domain/runtime'
import { listServices, type ServiceEntity } from '@/api/service'
import type { Workload } from '@/api/workload'
import { useEnvStore } from '@/stores/env'
import { statusOf, tagTypeToCls, WORKLOAD_STATUS } from '@/composables/useStatus'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import Icon from '@/components/Icon.vue'
import ServiceRunDrawer from '@/components/ServiceRunDrawer.vue'
import ServiceCreateDialog from '@/components/ServiceCreateDialog.vue'
import CdDeployDialog from '@/components/CdDeployDialog.vue'
import ProductDelivery from './ProductDelivery.vue'
import ProductSettings from './ProductSettings.vue'
interface Product { id: string; name: string; desc?: string; restricted?: boolean; bindings?: { type: string; name: string; note?: string }[] }
const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
const product = ref<Product | null>(null)
const services = ref<ServiceEntity[]>([])
const workloads = ref<Workload[]>([])
const routeHosts = ref(new Map<string, string[]>())
const loading = ref(true)
const error = ref('')
const servicesError = ref('')
const workloadsError = ref('')
const addressError = ref(false)
const q = ref('')
const showRun = ref(false)
const runWlId = ref('')
const showDeploy = ref(false)
const deployHint = ref('')
const showCreate = ref(false)
const tabs = [{ id: 'services', label: '服务运行' }, { id: 'delivery', label: '交付' }, { id: 'resources', label: '共享资源' }, { id: 'configuration', label: '配置' }, { id: 'members', label: '成员与权限' }]
const LEGACY: Record<string, string> = { services: 'services', overview: 'services', deploy: 'services', settings: 'resources', bindings: 'resources', configs: 'configuration', members: 'members', pipelines: 'delivery', changes: 'delivery', repositories: 'delivery', builds: 'delivery', images: 'delivery', releases: 'delivery' }
const facet = computed(() => { const tab = String(route.query.tab || 'services'); return tabs.some((t) => t.id === tab) ? tab : LEGACY[tab] || 'services' })
function pickTab(id: string) { void router.replace({ query: { ...route.query, tab: id, section: undefined, focus: undefined } }) }
let seq = 0
async function load() {
  const cur = ++seq
  loading.value = true
  error.value = ''
  const id = String(route.params.id)
  try {
    const p = await fetchJSON<Product>('/api/applications/' + id)
    if (cur !== seq) return
    product.value = p
    const results = await Promise.allSettled([listServices(id), fetchList<Workload>('/api/applications/' + id + '/workloads'), fetchList<{ id: string; name: string }>('/api/services'), fetchList<{ host: string; serviceId: string; enabled: boolean }>('/api/routes')])
    if (cur !== seq) return
    const [ss, ws, gs, rs] = results
    servicesError.value = ss.status === 'rejected' ? apiError(ss.reason) : ''
    workloadsError.value = ws.status === 'rejected' ? apiError(ws.reason) : ''
    if (ss.status === 'fulfilled') services.value = ss.value
    if (ws.status === 'fulfilled') workloads.value = ws.value
    addressError.value = gs.status === 'rejected' || rs.status === 'rejected'
    if (gs.status === 'fulfilled' && rs.status === 'fulfilled') {
      const names = new Map(gs.value.map((s) => [s.id, s.name]))
      const hosts = new Map<string, string[]>()
      for (const r of rs.value) {
        const name = names.get(r.serviceId)
        if (r.enabled && r.host && name) hosts.set(name, [...(hosts.get(name) ?? []), r.host])
      }
      routeHosts.value = hosts
    }
    openFromLink()
  } catch (e) { if (cur === seq) error.value = apiError(e, '无法加载产品') }
  finally { if (cur === seq) loading.value = false }
}
onMounted(async () => { await Promise.all([load(), envStore.loadEnvs()]) })
watch(() => route.params.id, () => { product.value = null; services.value = []; workloads.value = []; showRun.value = false; void load() })
const TYPE_LABEL: Record<string, string> = { web: 'Web', backend: '后端', agent: '智能体', static: '静态站点', cron: '定时任务' }
const rows = computed(() => services.value.filter((s) => s.name.toLowerCase().includes(q.value.trim().toLowerCase())).flatMap((s) => {
  const runs = baselineRunsFor(s.id, workloads.value, envStore.currentEnvId)
  return runs.length ? runs.map((w) => ({ key: s.id + ':' + w.id, service: s, workload: w as Workload | undefined })) : [{ key: s.id, service: s, workload: undefined as Workload | undefined }]
}))
const envName = (id?: string) => envStore.envs.find((e) => e.id === id)?.name ?? id ?? envStore.currentEnv?.name ?? '—'
function addresses(w?: Workload): string[] { if (!w) return []; return routeHosts.value.get(w.name) ?? (w.domain ? [w.domain] : []) }
function open(w: Workload) { runWlId.value = w.id; showRun.value = true }
function openFromLink() {
  const id = route.query.workload
  if (typeof id === 'string' && workloads.value.some((w) => w.id === id)) { runWlId.value = id; showRun.value = true }
}
watch(() => route.query.workload, openFromLink)
async function refreshWorkloads() { await load() }
function deploy(name: string) { deployHint.value = name; showDeploy.value = true }
function updated(a: { id: string; bindings?: Product['bindings'] }) { if (product.value?.id === a.id) product.value = { ...product.value, bindings: a.bindings } }
</script>
<template>
  <div class="product-page">
    <div class="product-breadcrumb"><RouterLink to="/products">产品</RouterLink><span aria-hidden="true">/</span><span>{{ product?.name || '产品详情' }}</span></div>
    <div v-if="loading && !product" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="无法打开产品" :description="error" error><button class="act" @click="load">重试</button><RouterLink to="/products" class="act">返回产品列表</RouterLink></StatePanel>
    <template v-else-if="product">
      <PageHeader :title="product.name" :description="product.desc || '在这里管理服务运行、交付过程和共享资源。'">
        <button v-if="facet === 'services'" class="act primary" @click="showCreate = true">＋ 新建服务</button>
        <RouterLink v-else-if="facet === 'delivery'" to="/delivery" class="act">跨产品交付</RouterLink>
      </PageHeader>
      <nav class="section-nav" aria-label="产品工作区"><button v-for="t in tabs" :key="t.id" :class="{ active: facet === t.id }" :aria-current="facet === t.id ? 'page' : undefined" @click="pickTab(t.id)">{{ t.label }}</button></nav>
      <section v-if="facet === 'services'">
        <div class="filter-bar"><label class="search-field"><Icon name="search" :size="17" /><input v-model="q" aria-label="搜索产品内服务" placeholder="搜索服务名称" /></label><span class="scope-description">{{ envStore.currentEnv?.name || '全部环境' }} · 基线运行</span><button class="act" :disabled="loading" @click="load">刷新</button><RouterLink :to="{ path: '/workloads', query: { app: product.id } }" class="text-button">全部实例与泳道 →</RouterLink></div>
        <StatePanel v-if="servicesError" title="服务列表加载失败" :description="servicesError" error><button class="act" @click="load">重新加载</button></StatePanel>
        <template v-else>
          <div v-if="workloadsError" class="inline-warning" role="alert">运行状态暂时无法加载，不代表服务未部署。<button class="text-button" @click="load">重试</button></div>
          <StatePanel v-if="!services.length" title="为产品添加第一个服务" description="服务是可部署的单元，可以是 Web、后端、智能体或定时任务。"><button class="act primary" @click="showCreate = true">新建服务</button></StatePanel>
          <StatePanel v-else-if="!rows.length" title="没有匹配的服务"><button class="act" @click="q = ''">清除搜索</button></StatePanel>
          <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table service-table">
<thead><tr><th scope="col">服务</th><th scope="col">类型</th><th scope="col">环境</th><th scope="col">运行状态</th><th scope="col">访问地址</th><th scope="col">操作</th></tr></thead><tbody>
            <tr v-for="row in rows" :key="row.key"><td><button v-if="row.workload" class="text-button service-name" @click="open(row.workload)">{{ row.service.name }}</button><strong v-else>{{ row.service.name }}</strong></td><td class="dim">{{ TYPE_LABEL[row.service.type] || row.service.type }}</td><td>{{ row.workload ? envName(row.workload.envId) : envName() }}</td><td><span v-if="workloadsError" class="status warn">状态未知</span><template v-else-if="row.workload"><span class="status" :class="tagTypeToCls(statusOf(WORKLOAD_STATUS, row.workload.status).type)">{{ statusOf(WORKLOAD_STATUS, row.workload.status).label }}</span><small class="replica-info">{{ row.workload.ready }}/{{ row.workload.replicas }} 副本就绪</small></template><span v-else class="status idle">未部署</span></td><td><template v-if="addresses(row.workload).length"><a v-for="h in addresses(row.workload)" :key="h" :href="'http://' + h" target="_blank" rel="noopener" class="domain-link" :title="h">{{ h }} ↗</a></template><span v-else class="dim">{{ addressError ? '地址暂不可用' : '未暴露' }}</span></td><td><button v-if="row.workload" class="act small" @click="open(row.workload)">实例与日志</button><button v-else class="act small" :disabled="!!workloadsError" @click="deploy(row.service.name)">运行部署流水线</button></td></tr>
          </tbody>
</table>
</div>
          <p class="view-note">每行对应一个服务在具体环境中的基线运行；泳道和任务实例可从“全部实例与泳道”查看。</p>
        </template>
      </section>
      <ProductDelivery v-else-if="facet === 'delivery'" :app-id="product.id" />
      <ProductSettings v-else :key="facet" :app="product" :section="facet" @updated="updated" />
    </template>
    <ServiceRunDrawer v-model="showRun" :workload-id="runWlId" :app-id="product?.id" :app-name="product?.name" @changed="refreshWorkloads" @deployed="pickTab('delivery')" />
    <ServiceCreateDialog v-model="showCreate" :app-id="product?.id || ''" @created="load" />
    <CdDeployDialog v-model="showDeploy" :app-id="product?.id" :app-name="product?.name" :hint="deployHint" @deployed="pickTab('delivery')" />
  </div>
</template>
<style scoped>
.product-breadcrumb { display: flex; gap: 10px; align-items: center; color: var(--text-faint); font-size: 12px; margin-bottom: 20px; }
.product-breadcrumb a { color: var(--brand); text-decoration: none; }
.service-table { min-width: 900px; }
.service-name { font-weight: 600; }
.replica-info { display: block; font-size: 11px; color: var(--text-faint); margin-top: 5px; }
.domain-link { display: block; color: var(--brand); font-size: 12px; text-decoration: none; max-width: 270px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.scope-description, .view-note { color: var(--text-faint); font-size: 12px; }
.view-note { margin-top: 14px; }
.loading-block { height: 240px; }
.inline-warning { background: var(--warning-soft); color: var(--warning); padding: 12px; border-radius: var(--radius); margin-bottom: 16px; font-size: 13px; }
</style>
