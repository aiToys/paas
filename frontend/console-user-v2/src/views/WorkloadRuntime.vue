<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiError, fetchList } from '@/api'
import { asList } from '@/api/response'
import {
  getWorkloadRuntime, listWorkloadOperations, restartWorkload,
  type Operation, type Runtime, type RuntimeInstance,
} from '@/api/workload'
import { useEnvStore } from '@/stores/env'
import { useAssistantStore } from '@/stores/assistant'
import { statusOf, WORKLOAD_STATUS } from '@/composables/useStatus'
import { confirmAction } from '@/composables/useConfirmAction'
import { instanceState, runtimeObservationLink } from '@/domain/serviceRuntime'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import RuntimeInstanceTable from '@/components/RuntimeInstanceTable.vue'
import RuntimeInstanceDrawer from '@/components/RuntimeInstanceDrawer.vue'
import RuntimeLogs from '@/components/RuntimeLogs.vue'
import ServiceRunDrawer from '@/components/ServiceRunDrawer.vue'
import Icon from '@/components/Icon.vue'

// 运行态真源：GET /runtime（observation 来源/时间 + 实例 UID/容器三态 + 能力 + 活跃操作）。
const route = useRoute()
const envStore = useEnvStore()
const assistant = useAssistantStore()
const runtime = ref<Runtime | null>(null)
const loading = ref(false)
const error = ref('')
const fetchedAt = ref('')
const appName = ref('')
const settingsOpen = ref(false)
const inspected = ref<RuntimeInstance>()
const diagnosticSection = ref('details')
const diagnosticOpen = ref(false)
// 操作记录（operations tab）与重启受理状态。
const operations = ref<Operation[]>([])
const operationsLoading = ref(false)
const restarting = ref(false)
const workloadId = computed(() => String(route.params.id || ''))
const workload = computed(() => runtime.value?.workload)
const instances = computed(() => runtime.value?.instances || [])
const observation = computed(() => runtime.value?.observation)
const capabilities = computed(() => runtime.value?.capabilities)
const activeOperation = computed(() => runtime.value?.activeOperation)
const environment = computed(() => envStore.envs.find((e) => e.id === workload.value?.envId))
const isProd = computed(() => environment.value?.type === 'prod')
const environmentName = computed(() => environment.value?.name || workload.value?.envId || '环境未知')
const laneName = computed(() => !workload.value?.laneId || workload.value.laneId === 'default' ? '基线' : workload.value.laneId)
const context = computed(() => `${appName.value || workload.value?.appId || ''} / ${workload.value?.name || ''} / ${environmentName.value} / ${laneName.value}`)
const types: Record<string, string> = { service: '常驻服务', job: '一次性任务', cronjob: '定时任务' }
const tabs = [{ id: 'overview', label: '运行概览' }, { id: 'instances', label: '实例' }, { id: 'logs', label: '日志' }, { id: 'operations', label: '操作记录' }]
const activeTab = computed(() => tabs.some((t) => t.id === route.query.view) ? String(route.query.view) : 'overview')
const needsAttention = computed(() => instances.value.filter((i) => ['warn', 'err'].includes(instanceState(i).cls)))
// 观察来源可读化：kubernetes=集群直读，external=外部实例，unavailable=读取不可用。
const observationText = computed(() => {
  const o = observation.value
  if (!o) return ''
  if (o.source === 'kubernetes') return o.state === 'fresh' ? '集群直读（实时）' : `集群直读（${o.state === 'stale' ? '数据可能滞后' : '状态异常'}）`
  if (o.source === 'external') return '外部实例（平台仅代理只读观察）'
  return '运行态读取不可用'
})
const restartCap = computed(() => capabilities.value?.restart || { allowed: false })
const restartDisabledReason = computed(() => {
  if (workload.value?.type !== 'service') return '任务类型不适用'
  if (workload.value?.replicas === 0) return '先恢复副本，再执行重启'
  if (activeOperation.value) return `有进行中的${activeOperation.value.kind === 'restart' ? '重启' : '重建'}操作`
  if (!restartCap.value.allowed) return restartCap.value.message || '当前不可用'
  return ''
})
let sequence = 0
async function load(reset = false) {
  const current = ++sequence
  const id = workloadId.value
  loading.value = true; error.value = ''
  if (reset) { runtime.value = null; appName.value = ''; fetchedAt.value = ''; diagnosticOpen.value = false; settingsOpen.value = false; inspected.value = undefined; operations.value = [] }
  try {
    const result = await getWorkloadRuntime(id)
    if (current !== sequence) return
    if (!result?.workload || result.workload.id !== id) throw new Error('运行态响应格式或目标不匹配')
    runtime.value = { ...result, instances: asList<RuntimeInstance>(result.instances) }
    fetchedAt.value = new Date().toLocaleTimeString()
    if (inspected.value) {
      inspected.value = runtime.value.instances.find((i) => i.name === inspected.value?.name)
      if (!inspected.value) diagnosticOpen.value = false
    }
    // 名称辅助查询失败时保留真实 ID；不阻断运行页，也不覆盖其它目标的上下文。
    void fetchList<{ id: string; name: string }>('/api/applications').then((apps) => {
      if (current === sequence) appName.value = apps.find((a) => a.id === result.workload.appId)?.name || ''
    }).catch(() => {})
    // 活跃操作存在时轻微加密轮询（受理→完成一般 10~60s），终态即停。
    if (runtime.value.activeOperation) scheduleOpPoll()
  } catch (e) { if (current === sequence) error.value = apiError(e, '运行详情读取失败') }
  finally { if (current === sequence) loading.value = false }
}
// 操作轮询：活跃操作期间 8s 刷新 runtime + operations；终态后再刷一次收尾。
let opTimer: ReturnType<typeof setTimeout> | null = null
function scheduleOpPoll() {
  if (opTimer) return
  opTimer = setTimeout(async () => {
    opTimer = null
    if (!activeOperation.value) return
    await load()
    if (activeTab.value === 'operations') await loadOperations(false)
    if (activeOperation.value) scheduleOpPoll()
  }, 8000)
}
async function loadOperations(showLoading = true) {
  if (showLoading) operationsLoading.value = true
  try { operations.value = await listWorkloadOperations(workloadId.value, 30) }
  catch { /* 操作记录加载失败不阻断主视图；tab 内提示 */ }
  finally { if (showLoading) operationsLoading.value = false }
}
watch(workloadId, () => { clearTimeout(opTimer ?? undefined); opTimer = null; void load(true) }, { immediate: true })
watch(activeTab, (tab) => { if (tab === 'operations' && !operations.value.length) void loadOperations() })
void envStore.loadEnvs().catch(() => { /* 环境名称失败时回显资源实际 envId，不猜测是否生产。 */ })
onUnmounted(() => { sequence++; if (opTimer) { clearTimeout(opTimer); opTimer = null } })
function inspect(instance: RuntimeInstance, section: 'details' | 'logs' | 'events' | 'terminal') {
  inspected.value = instance; diagnosticSection.value = section; diagnosticOpen.value = true
}
// 滚动重启：确认（生产强警示）→ reason 必填 → 202 受理 → 轮询跟进。
async function rollingRestart() {
  if (restartDisabledReason.value) return
  const ok = await confirmAction(
    `将对 ${environmentName.value} / ${laneName.value} 的 ${workload.value?.name}（${workload.value?.replicas} 副本）执行滚动重启：逐个重建 Pod，服务不完全中断。${isProd.value ? '此操作作用于生产环境。' : ''}`,
    '滚动重启',
  )
  if (!ok) return
  let reason = ''
  try {
    const { value } = await ElMessageBox.prompt('填写重启原因（记入审计与操作记录）', '重启原因', {
      confirmButtonText: '确认重启', cancelButtonText: '取消', inputPlaceholder: '例如：配置更新后生效',
      inputValidator: (v: string) => (v && v.trim().length >= 2 ? true : '原因至少 2 个字符'),
    })
    reason = value.trim()
  } catch { return }
  restarting.value = true
  try {
    const op = await restartWorkload(workloadId.value, reason, `restart-${workloadId.value}-${Date.now()}`)
    notifyAccepted(`重启已受理（操作 ${op.id}），正在滚动替换实例`)
    await load()
    scheduleOpPoll()
  } catch (e) {
    ElMessageBox.alert(apiError(e, '重启受理失败'), '重启失败', { type: 'error' })
  } finally { restarting.value = false }
}
// 轻提示（不阻断）；受理成功的 202 语义与完成态由操作记录呈现。
function notifyAccepted(text: string) { ElMessage.success(text) }
const opStatusMeta: Record<string, { label: string; cls: string }> = {
  accepted: { label: '已受理', cls: 'idle' }, reconciling: { label: '数据面生效中', cls: 'warn' },
  running: { label: '执行中', cls: 'warn' }, succeeded: { label: '已完成', cls: 'ok' },
  failed: { label: '失败', cls: 'err' }, superseded: { label: '被新发布覆盖', cls: 'idle' },
  unknown: { label: '结果待核实', cls: 'warn' },
}
</script>

<template>
  <div class="page runtime-page">
    <nav class="runtime-breadcrumb" aria-label="面包屑"><RouterLink to="/workloads">工作负载</RouterLink><span>/</span><RouterLink v-if="workload" :to="'/products/' + encodeURIComponent(workload.appId)">{{ appName || workload.appId }}</RouterLink><span v-if="workload">/</span><span>{{ workload?.name || workloadId }}</span></nav>
    <PageHeader :title="workload?.name || '服务运行'" eyebrow="服务运行" description="查看此工作负载的实例、定位故障并管理运行配置。"><button class="act" :disabled="loading" @click="load()">{{ loading ? '读取中…' : '刷新' }}</button><button v-if="workload" class="act" :disabled="!!error" @click="settingsOpen = true">运行设置</button><button v-if="workload" class="act" @click="assistant.openPanel({ workloadId: workload.id, workloadName: workload.name, service: workload.service || undefined, envName: environmentName, symptom: needsAttention.length ? 'crash' : 'unknown' })"><Icon name="bot" :size="15" />智能诊断</button><RouterLink v-if="workload" class="act primary" :to="{ path: '/products/' + encodeURIComponent(workload.appId), query: { tab: 'delivery', section: 'pipelines' } }"><Icon name="pipeline" :size="16" />前往交付</RouterLink></PageHeader>
    <div v-if="loading && !runtime" class="loading-block" v-loading="true" role="status" aria-label="读取运行详情"></div>
    <StatePanel v-else-if="error && !runtime" error title="运行详情读取失败" :description="error"><button class="act" @click="load()">重试</button><RouterLink to="/workloads" class="act">返回工作负载</RouterLink></StatePanel>
    <template v-if="workload">
      <section class="runtime-scope" aria-label="当前操作目标"><div class="scope-main"><Icon name="server" :size="20" /><strong>{{ environmentName }}</strong><span class="status" :class="isProd ? 'warn' : 'idle'">{{ isProd ? '生产环境' : environment?.type === 'test' ? '测试环境' : '环境类型待确认' }}</span><span class="scope-lane">{{ laneName }}</span><span class="scope-type">{{ types[workload.type] || workload.type }}</span></div><span class="mono scope-id" :title="workload.id">{{ workload.id }}</span></section>
      <div v-if="error" class="runtime-notice error" role="alert"><strong>刷新失败，正在展示上次读取结果</strong><span>{{ error }}。下方数据可能已变化，运行设置暂不可用。</span></div>
      <div v-if="activeOperation" class="runtime-op-banner" role="status"><strong>{{ activeOperation.kind === 'restart' ? '滚动重启' : '实例重建' }}进行中</strong><span>{{ opStatusMeta[activeOperation.status]?.label || activeOperation.status }} · 受理于 {{ new Date(activeOperation.acceptedAt).toLocaleString() }}{{ activeOperation.reason ? ` · ${activeOperation.reason}` : '' }}</span><button class="act small" @click="load()">刷新进度</button></div>
      <div class="runtime-notice" :class="{ stale: observation?.state !== 'fresh' }" role="note"><Icon name="activity" :size="17" /><span>运行态来源：{{ observationText || '未返回' }}{{ observation?.observedAt ? `，观测于 ${new Date(observation.observedAt).toLocaleTimeString()}` : '' }}{{ observation?.error ? `，读取错误：${observation.error}` : '' }}</span><span class="fetch-time">页面读取于 {{ fetchedAt }}</span></div>
      <div class="runtime-layout">
        <div class="runtime-main">
          <nav class="tab-bar runtime-tabs" aria-label="服务运行分区"><RouterLink v-for="tab in tabs" :key="tab.id" :to="{ path: route.path, query: { ...route.query, view: tab.id } }" class="tab-btn" :class="{ active: activeTab === tab.id }" :aria-current="activeTab === tab.id ? 'page' : undefined">{{ tab.label }}<span v-if="tab.id === 'instances'" class="tab-count">{{ instances.length }}</span></RouterLink></nav>
          <template v-if="activeTab === 'overview'">
            <section class="card runtime-summary"><div class="card-head"><span class="card-title">运行摘要</span><span class="card-sub">本次接口返回</span></div><div class="summary-grid"><div><span class="summary-label">就绪 / 期望副本</span><strong class="summary-value mono">{{ workload.ready }}<span> / {{ workload.replicas }}</span></strong></div><div><span class="summary-label">工作负载状态</span><strong class="summary-state">{{ statusOf(WORKLOAD_STATUS, workload.status).label || '未返回' }}</strong></div><div><span class="summary-label">需要关注的 Pod</span><strong class="summary-value mono">{{ instances.length ? needsAttention.length : '—' }}</strong></div></div><p v-if="workload.replicas === 0" class="summary-note">期望副本为 0。服务恢复前需要调整副本数。</p><p v-else class="summary-note">副本数量、Pod Ready 与业务可用性需要分别核实。</p></section>
            <section v-if="needsAttention.length" class="attention-box"><div><strong>先处理未就绪实例</strong><p>{{ needsAttention.length }} 个 Pod 正在等待启动、失败或未完全就绪。先查看日志和事件，再决定是否重建。</p></div><RouterLink class="act" :to="{ path: route.path, query: { view: 'instances' } }">检查实例</RouterLink></section>
            <section class="card runtime-config"><div class="card-head"><span class="card-title">当前运行配置</span><button class="text-button" :disabled="!!error" @click="settingsOpen = true">调整配置</button></div><dl><dt>镜像</dt><dd class="mono">{{ workload.imageRef || workload.image || '未返回' }}</dd><dt>服务标识</dt><dd>{{ workload.service || workload.serviceId || '未关联服务' }}</dd><dt>访问域名</dt><dd class="mono">{{ workload.domain || '未配置' }}</dd><dt>容器端口</dt><dd>{{ workload.containerPort || workload.port || '未配置' }}</dd><dt>指标采集路径</dt><dd class="mono">{{ workload.metricsPath || '未配置' }}</dd><template v-if="workload.schedule"><dt>执行计划</dt><dd class="mono">{{ workload.schedule }}</dd></template></dl></section>
            <section class="card instance-preview"><div class="card-head"><span class="card-title">运行实例</span><RouterLink class="text-button" :to="{ path: route.path, query: { view: 'instances' } }">进入实例视图 →</RouterLink></div><RuntimeInstanceTable :instances="instances" @inspect="inspect" /></section>
          </template>
          <section v-else-if="activeTab === 'instances'" class="card"><div class="card-head"><span class="card-title">实例与诊断</span><span class="card-sub">从 Pod 下钻到容器</span></div><RuntimeInstanceTable :instances="instances" @inspect="inspect" /></section>
          <section v-else-if="activeTab === 'logs'" class="card"><div class="card-head"><span class="card-title">实例日志</span><RouterLink class="text-button" :to="runtimeObservationLink(workload, 'logs')">查询聚合日志 →</RouterLink></div><RuntimeLogs :key="workload.id" :workload-id="workload.id" :instances="instances" /></section>
          <section v-else class="card operations-card">
<div class="card-head"><span class="card-title">运行操作记录</span><button class="text-button" :disabled="operationsLoading" @click="loadOperations()">{{ operationsLoading ? '读取中…' : '刷新' }}</button></div>
            <StatePanel v-if="!operations.length && !operationsLoading" compact title="暂无操作记录" description="滚动重启与实例重建的受理、执行与结果会记录在这里（含操作者与原因）。" />
            <div v-else-if="operations.length" class="table-scroll" tabindex="0" aria-label="操作记录表格"><table class="list-table ops-table"><thead><tr><th scope="col">操作</th><th scope="col">状态</th><th scope="col">目标</th><th scope="col">原因</th><th scope="col">操作者</th><th scope="col">受理时间</th><th scope="col">证据</th></tr></thead><tbody><tr v-for="op in operations" :key="op.id"><td>{{ op.kind === 'restart' ? '滚动重启' : '实例重建' }}</td><td><span class="status" :class="opStatusMeta[op.status]?.cls || 'idle'">{{ opStatusMeta[op.status]?.label || op.status }}</span></td><td class="mono">{{ op.podName || workload.name }}</td><td class="ops-reason">{{ op.reason || '—' }}</td><td class="mono">{{ op.actor || '—' }}</td><td class="mono">{{ new Date(op.acceptedAt).toLocaleString() }}</td><td class="ops-evidence">{{ op.evidence || '—' }}</td></tr></tbody></table></div>
            <RouterLink class="text-button delivery-history" :to="{ path: '/products/' + encodeURIComponent(workload.appId), query: { tab: 'delivery', section: 'runs' } }">查看已有交付记录 →</RouterLink>
          </section>
        </div>
        <aside class="runtime-aside" aria-label="运行操作与排障入口">
          <section class="card">
<div class="card-head"><span class="card-title">运行操作</span></div><button class="operation-row" :disabled="!!error" @click="settingsOpen = true"><Icon name="sliders" :size="18" /><span><strong>调整运行配置</strong><small>副本数与对外域名</small></span><span aria-hidden="true">→</span></button>
            <div class="operation-row"><Icon name="deploy" :size="18" /><span><strong>滚动重启</strong><small>{{ restartDisabledReason || '逐个重建 Pod，服务不完全中断' }}</small></span><button class="act small" :disabled="restarting || !!restartDisabledReason || !!error" @click="rollingRestart">{{ restarting ? '提交中…' : '重启' }}</button></div>
            <p class="aside-note">滚动重启作用于整个服务；重建单个 Pod 位于实例详情。受理（202）后异步执行，结果见操作记录。</p>
</section>
          <section class="card"><div class="card-head"><span class="card-title">继续排障</span></div><RouterLink class="evidence-link" :to="runtimeObservationLink(workload, 'metrics')"><Icon name="activity" :size="18" /><span>指标与趋势<small>当前工作负载的资源指标</small></span><span>→</span></RouterLink><RouterLink class="evidence-link" :to="runtimeObservationLink(workload, 'logs')"><Icon name="prompt" :size="18" /><span>聚合日志<small>保留环境与泳道范围</small></span><span>→</span></RouterLink><RouterLink class="evidence-link" :to="runtimeObservationLink(workload, 'traces')"><Icon name="service" :size="18" /><span>请求链路<small>按 TraceID 查询关联请求</small></span><span>→</span></RouterLink></section>
          <div class="scope-footnote">本页固定到上方工作负载。顶栏环境用于浏览筛选，不会改变这里的操作目标。</div>
        </aside>
      </div>
      <RuntimeInstanceDrawer v-model="diagnosticOpen" :workload-id="workload.id" :context="context" :instance="inspected" :section="diagnosticSection" :capabilities="capabilities" :is-prod="isProd" :recreate-eligible="capabilities?.recreate?.allowed !== false" @changed="() => { void load(); scheduleOpPoll() }" />
      <ServiceRunDrawer v-model="settingsOpen" :workload-id="workload.id" :app-id="workload.appId" :app-name="appName" @changed="load()" @deployed="load()" />
    </template>
  </div>
</template>

<style scoped>
@import '../components/runtime-tabs.css';
.runtime-breadcrumb { display: flex; align-items: center; flex-wrap: wrap; gap: 9px; color: var(--text-faint); font-size: 12px; margin-bottom: 20px; overflow-wrap: anywhere; }
.runtime-breadcrumb a { text-decoration: none; } .runtime-breadcrumb a:hover { color: var(--brand); }
.runtime-scope { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 12px; padding: 15px 18px; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface); }
.scope-main { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.scope-lane, .scope-type { font-size: 12px; color: var(--text-dim); border-left: 1px solid var(--border); padding-left: 12px; }
.scope-id { color: var(--text-faint); font-size: 11px; overflow-wrap: anywhere; }
.runtime-notice { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; color: var(--text-faint); font-size: 12px; margin: 12px 0 20px; }
.runtime-notice svg { flex-shrink: 0; } .runtime-notice > span:first-of-type { flex: 1 1 320px; }
.runtime-notice.stale > span:first-of-type { color: var(--warning); }
.runtime-notice.error { background: var(--danger-soft); border: 1px solid var(--danger); padding: 12px; border-radius: var(--radius); color: var(--danger); }
.runtime-op-banner { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; border-left: 3px solid var(--warning); background: var(--warning-soft); border-radius: var(--radius-sm); padding: 12px 16px; margin-bottom: 12px; font-size: 12px; }
.runtime-op-banner strong { color: var(--warning); }
.runtime-op-banner span { flex: 1 1 260px; color: var(--text-dim); overflow-wrap: anywhere; }
.fetch-time { margin-left: auto; white-space: nowrap; }
.runtime-layout { display: grid; grid-template-columns: minmax(0, 1fr) 280px; gap: 24px; align-items: start; }
.runtime-main, .runtime-aside { min-width: 0; }
.runtime-tabs { margin-bottom: 20px; overflow-x: auto; }
.runtime-tabs a { text-decoration: none; white-space: nowrap; }
.tab-count { margin-left: 6px; font: 11px var(--font-mono); border-radius: var(--radius-sm); padding: 1px 5px; background: var(--surface-2); }
.runtime-main > .card { margin-bottom: 20px; }
.summary-grid { display: grid; grid-template-columns: 1.15fr 1fr 1fr; gap: 20px; padding: 12px 0; }
.summary-grid > div + div { border-left: 1px solid var(--border); padding-left: 24px; }
.summary-label { display: block; font-size: 12px; color: var(--text-faint); margin-bottom: 12px; }
.summary-value { display: block; font-size: 32px; line-height: 1.2; font-weight: 600; }
.summary-value span { font-size: 20px; color: var(--text-faint); font-weight: 400; }
.summary-state { display: block; font-size: 18px; line-height: 38px; font-weight: 500; }
.summary-note { margin: 16px 0 0; padding-top: 12px; border-top: 1px solid var(--border); color: var(--text-faint); font-size: 12px; }
.attention-box { display: flex; align-items: center; justify-content: space-between; gap: 20px; border-left: 3px solid var(--warning); background: var(--warning-soft); border-radius: var(--radius-sm); padding: 16px 20px; margin-bottom: 20px; }
.attention-box strong { color: var(--warning); font-size: 13px; } .attention-box p { margin: 5px 0 0; font-size: 12px; color: var(--text-dim); }
.runtime-config .card-head { justify-content: space-between; }
.runtime-config dl { display: grid; grid-template-columns: 112px minmax(0, 1fr); gap: 15px 20px; margin: 0; font-size: 13px; }
.runtime-config dt { color: var(--text-faint); } .runtime-config dd { margin: 0; overflow-wrap: anywhere; }
.instance-preview .card-head { justify-content: space-between; }
.runtime-aside { padding-top: 57px; } .runtime-aside .card { padding: 18px; margin-bottom: 16px; }
.operation-row { display: flex; align-items: center; gap: 10px; padding: 12px 0; width: 100%; border: 0; border-top: 1px solid var(--border); background: transparent; text-align: left; color: var(--text); }
.operation-row > span:nth-child(2) { flex: 1; } .operation-row strong { display: block; font-size: 13px; font-weight: 500; }
.operation-row small, .evidence-link small { display: block; font-size: 11px; color: var(--text-faint); margin-top: 4px; }
.operation-row svg, .evidence-link svg { flex-shrink: 0; color: var(--text-faint); }
.aside-note, .scope-footnote { font-size: 12px; color: var(--text-faint); line-height: 1.7; margin: 8px 0 0; }
.scope-footnote { padding: 0 6px; }
.evidence-link { display: flex; align-items: center; gap: 10px; padding: 12px 0; color: var(--text); text-decoration: none; font-size: 13px; border-top: 1px solid var(--border); }
.evidence-link > span:first-of-type { flex: 1; } .evidence-link:hover { color: var(--brand); }
.ops-table { min-width: 820px; }
.ops-reason, .ops-evidence { max-width: 220px; overflow-wrap: anywhere; font-size: 12px; color: var(--text-dim); white-space: pre-wrap; }
.delivery-history { display: inline-block; margin-top: 16px; }
@media (min-width: 1700px) { .runtime-layout { grid-template-columns: minmax(0, 1fr) 310px; } }
@media (max-width: 1380px) { .runtime-layout { grid-template-columns: minmax(0, 1fr); } .runtime-aside { padding-top: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; } .scope-footnote { grid-column: 1 / -1; } }
@media (max-width: 640px) { .summary-grid { grid-template-columns: 1fr; gap: 16px; } .summary-grid > div + div { border-left: 0; padding-left: 0; border-top: 1px solid var(--border); padding-top: 14px; } .summary-value { font-size: 28px; } .runtime-aside { grid-template-columns: minmax(0, 1fr); } .attention-box { align-items: flex-start; flex-direction: column; gap: 12px; } .runtime-config dl { grid-template-columns: 90px minmax(0, 1fr); gap: 12px; } }
</style>
