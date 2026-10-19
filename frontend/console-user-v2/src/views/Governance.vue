<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 流量（v2 原生，治理三视图 2026-10-16）：对外路由（Route→Ingress 域名，创建/上下线）+
// 服务目录（部署即注册，行展开看实例——真源 K8s Endpoints）+ 熔断器（即时评估策略）。
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError, fetchList } from '@/api'

interface GovService { id: string; name: string; appId?: string; envId?: string; protocol?: string; port?: number }
interface GovRoute {
  id: string
  host: string
  path?: string
  serviceId: string
  enabled: boolean
  // 数据面下发状态（spec 专项04 §4）：''=未下发（数据面未启用）、applied、failed
  applyStatus?: string
  applyError?: string
  appliedAt?: string
}
interface Instance { id: string; address?: string; status?: string }
interface Breaker {
  id: string; name: string; serviceId: string; strategy: string
  threshold: number; minRequests: number; windowSecs: number; enabled: boolean
  state: string; updatedAt?: string
}

const services = ref<GovService[]>([])
const routes = ref<GovRoute[]>([])
const breakers = ref<Breaker[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const [svcs, rts, brs] = await Promise.all([
      fetchList<GovService>('/api/services'),
      fetchList<GovRoute>('/api/routes'),
      fetchList<Breaker>('/api/breakers').catch(() => []),
    ])
    services.value = svcs
    routes.value = rts
    breakers.value = brs
  } catch (e) {
    ElMessage.error(apiError(e, '加载流量配置失败'))
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  document.title = '流量 · 控制台'
  await load()
})

function serviceName(id: string): string {
  return services.value.find((s) => s.id === id)?.name ?? id
}

// —— 服务实例（GET /api/services/{id} 返回 {service, instances}，真源 Endpoints）——
const expandedSvc = ref('')
const svcInstances = ref<Instance[]>([])
const instancesSource = ref('')
async function toggleSvc(s: GovService) {
  if (expandedSvc.value === s.id) {
    expandedSvc.value = ''
    return
  }
  expandedSvc.value = s.id
  try {
    // 历史形态 {service,instances,instancesSource}（fetchJSON 的 {data:T} 解包兜底两者）。
    const payload = await fetchJSON<{ instances?: Instance[]; instancesSource?: string } & Partial<{ data: unknown }>>(
      `/api/services/${s.id}`,
    )
    svcInstances.value = (payload as { instances?: Instance[] }).instances ?? []
    instancesSource.value = (payload as { instancesSource?: string }).instancesSource ?? ''
  } catch {
    svcInstances.value = []
  }
}

// —— 路由创建（host 绑定服务 → K8sRouteApplier 聚合 Ingress 下发）——
const showCreate = ref(false)
const submitting = ref(false)
const form = ref({ host: '', path: '', serviceId: '' })

function openCreate() {
  form.value = { host: '', path: '', serviceId: services.value[0]?.id ?? '' }
  showCreate.value = true
}
async function create() {
  if (!form.value.host.trim() || !form.value.serviceId) {
    ElMessage.warning('请填写域名并选择服务')
    return
  }
  submitting.value = true
  try {
    await fetchJSON('/api/routes', {
      method: 'POST',
      body: JSON.stringify({
        host: form.value.host.trim(),
        path: form.value.path.trim() || '/',
        serviceId: form.value.serviceId,
        enabled: true,
      }),
    })
    ElMessage.success('路由已创建（Ingress 生效需约十几秒）')
    showCreate.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    submitting.value = false
  }
}

async function toggle(r: GovRoute) {
  try {
    await fetchJSON(`/api/routes/${r.id}`, {
      method: 'PUT',
      body: JSON.stringify({ enabled: !r.enabled }),
    })
    ElMessage.success(r.enabled ? '已下线' : '已上线')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  }
}

// —— 熔断器（POST /api/breakers 创建；DELETE /api/breakers/{id}）——
const showBreaker = ref(false)
const breakerForm = ref({ name: '', serviceId: '', strategy: 'error_rate', threshold: 50, windowSecs: 60 })
async function createBreaker() {
  if (!breakerForm.value.name.trim() || !breakerForm.value.serviceId) {
    ElMessage.warning('请填写名称并选择服务')
    return
  }
  try {
    await fetchJSON('/api/breakers', {
      method: 'POST',
      body: JSON.stringify({
        name: breakerForm.value.name.trim(),
        serviceId: breakerForm.value.serviceId,
        strategy: breakerForm.value.strategy,
        threshold: Number(breakerForm.value.threshold),
        windowSecs: Number(breakerForm.value.windowSecs),
        minRequests: 5,
        enabled: true,
      }),
    })
    ElMessage.success('熔断器已创建')
    showBreaker.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  }
}
async function removeBreaker(b: Breaker) {
  if (!await confirmAction(`删除熔断器「${b.name}」？`)) return
  try {
    await fetchJSON(`/api/breakers/${b.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}
const STRATEGY_LABEL: Record<string, string> = { error_rate: '错误率%', slow_rate: '慢调用%', consecutive: '连续失败' }
const BREAKER_STATE: Record<string, { label: string; cls: string }> = {
  open: { label: '熔断中', cls: 'err' }, half_open: { label: '半开', cls: 'warn' }, closed: { label: '闭合', cls: 'ok' },
}
</script>

<template>
  <div class="page">
    <div class="head-row">
      <h2 class="title">流量</h2>
      <span class="sub">对外域名路由——host 绑定服务，Ingress 下发</span>
      <button class="head-create" @click="openCreate">新建路由</button>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载流量配置…"></div>
    <div v-else-if="!routes.length" class="empty">暂无对外路由——服务通过路由获得线上域名</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table">
      <thead>
        <tr>
          <th>域名</th>
          <th>路径</th>
          <th>服务</th>
          <th>配置</th>
          <th>下发</th>
          <th class="col-act"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in routes" :key="r.id">
          <td><a class="s-name mono host-link" :href="`http://${r.host}`" target="_blank" rel="noopener">{{ r.host }} ↗</a></td>
          <td class="mono dim">{{ r.path || '/' }}</td>
          <td><span class="s-name mono">{{ serviceName(r.serviceId) }}</span></td>
          <td>
            <span class="status" :class="r.enabled ? 'ok' : 'idle'">{{ r.enabled ? '启用' : '已停用' }}</span>
          </td>
          <td>
            <el-tooltip v-if="r.applyStatus === 'failed'" :content="r.applyError || '下发失败'" placement="top">
              <span class="status err">下发失败 ?</span>
            </el-tooltip>
            <el-tooltip
              v-else-if="r.applyStatus === 'applied'"
              content="Ingress 已聚合下发（配置层证据）；域名实际可达性未探测"
              placement="top"
            >
              <span class="status ok">已下发</span>
            </el-tooltip>
            <el-tooltip v-else content="尚未向数据面下发（无 Host 或数据面未启用）" placement="top">
              <span class="status idle">未下发</span>
            </el-tooltip>
          </td>
          <td class="col-act"><button class="act" @click="toggle(r)">{{ r.enabled ? '下线' : '上线' }}</button></td>
        </tr>
      </tbody>
    </table>
</div>

    <el-dialog v-model="showCreate" title="新建路由" width="480px" append-to-body>
      <el-form label-position="top" @submit.prevent="create">
        <el-form-item label="域名" required>
          <el-input v-model="form.host" class="mono" placeholder="如 shop.example.com" />
        </el-form-item>
        <el-form-item label="路径">
          <el-input v-model="form.path" class="mono" placeholder="/（默认）" />
        </el-form-item>
        <el-form-item label="服务" required>
          <el-select v-model="form.serviceId" style="width: 100%">
            <el-option v-for="s in services" :key="s.id" :value="s.id" :label="s.name" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="create">创建</el-button>
      </template>
    </el-dialog>

    <!-- 服务目录（部署即注册；行展开实例，真源 K8s Endpoints） -->
    <div class="section-title">服务目录（{{ services.length }}）</div>
    <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table">
      <thead><tr><th>服务名</th><th>协议</th><th>端口</th><th></th></tr></thead>
      <tbody>
        <template v-for="s in services" :key="s.id">
          <tr class="clickable" @click="toggleSvc(s)">
            <td><span class="s-name mono">{{ s.name }}</span></td>
            <td class="dim">{{ s.protocol ?? 'http' }}</td>
            <td class="mono dim">{{ s.port ?? '-' }}</td>
            <td class="dim arrow">{{ expandedSvc === s.id ? '收起 ▴' : '实例 ▾' }}</td>
          </tr>
          <tr v-if="expandedSvc === s.id" class="inst-row">
            <td colspan="4">
              <div v-if="!svcInstances.length" class="inst-empty">暂无实例（Pod 未就绪或已被回收）</div>
              <div v-else class="inst-list">
                <span v-for="i in svcInstances" :key="i.id ?? i.address" class="inst-chip">
                  <span class="mono">{{ i.address ?? i.id }}</span>
                  <span v-if="i.status" class="st" :class="i.status === 'healthy' ? 'ok' : 'warn'">{{ i.status }}</span>
                </span>
              </div>
              <div v-if="instancesSource" class="inst-src">来源：{{ instancesSource === 'discovered' ? 'K8s Endpoints 自动发现' : instancesSource }}</div>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
</div>

    <!-- 熔断器 -->
    <div class="section-title-row">
      <div class="section-title">熔断器（{{ breakers.length }}）</div>
      <button class="act" @click="showBreaker = true">新建熔断器</button>
    </div>
    <div v-if="!breakers.length" class="empty small-empty">暂无熔断器——为高频依赖服务配置错误率/慢调用保护</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table">
      <thead><tr><th>名称</th><th>服务</th><th>策略</th><th>状态</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="b in breakers" :key="b.id">
          <td><span class="s-name">{{ b.name }}</span></td>
          <td><span class="mono dim">{{ serviceName(b.serviceId) }}</span></td>
          <td class="dim">{{ STRATEGY_LABEL[b.strategy] ?? b.strategy }} ≥ {{ b.threshold }}（{{ b.windowSecs }}s 窗口）</td>
          <td>
            <span class="status" :class="BREAKER_STATE[b.state]?.cls ?? 'idle'">{{ BREAKER_STATE[b.state]?.label ?? b.state }}</span>
          </td>
          <td class="col-act"><button class="act" @click="removeBreaker(b)">删除</button></td>
        </tr>
      </tbody>
    </table>
</div>

    <!-- 熔断器创建弹窗 -->
    <el-dialog v-model="showBreaker" title="新建熔断器" width="480px" append-to-body>
      <el-form label-position="top" @submit.prevent="createBreaker">
        <el-form-item label="名称" required>
          <el-input v-model="breakerForm.name" placeholder="如 shop-order-cb" />
        </el-form-item>
        <el-form-item label="服务" required>
          <el-select v-model="breakerForm.serviceId" style="width: 100%">
            <el-option v-for="s in services" :key="s.id" :value="s.id" :label="s.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="策略">
          <select v-model="breakerForm.strategy" class="f-select">
            <option value="error_rate">错误率 %</option>
            <option value="slow_rate">慢调用 %</option>
            <option value="consecutive">连续失败次数</option>
          </select>
        </el-form-item>
        <el-form-item :label="breakerForm.strategy === 'consecutive' ? '阈值（次数）' : '阈值（%）'">
          <el-input-number v-model="breakerForm.threshold" :min="1" :max="100" />
        </el-form-item>
        <el-form-item label="统计窗口（秒）">
          <el-input-number v-model="breakerForm.windowSecs" :min="10" :max="600" :step="10" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showBreaker = false">取消</el-button>
        <el-button type="primary" @click="createBreaker">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import './list-page.css';
.host-link {
  color: var(--success, #16a34a);
  text-decoration: none;
}
.col-act { text-align: right; }
.section-title-row { display: flex; align-items: center; justify-content: space-between; }
.small-empty { padding: 18px 0; font-size: 12.5px; color: var(--text-faint); text-align: center; }
.arrow { font-size: 11.5px; }
.inst-row td { background: var(--surface-2); padding: 10px 16px; }
.inst-empty { font-size: 12px; color: var(--text-faint); }
.inst-list { display: flex; flex-wrap: wrap; gap: 6px; }
.inst-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 3px 10px; border: 1px solid var(--border); border-radius: 999px;
  font-size: 11.5px;
}
.inst-chip .st.ok { color: var(--success, #16a34a); }
.inst-chip .st.warn { color: var(--warning, #f59e0b); }
.inst-src { margin-top: 6px; font-size: 11px; color: var(--text-faint); }
.f-select {
  width: 100%; box-sizing: border-box; padding: 7px 10px;
  border: 1px solid var(--border-strong); border-radius: var(--radius);
  background: var(--surface-2); color: var(--text); font-family: inherit; font-size: 13px;
}
</style>
