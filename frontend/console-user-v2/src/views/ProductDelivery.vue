<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiError } from '@/api'
import { listRuns, triggerAgentShipRun, type PipelineRun } from '@/api/pipeline'
import { usePolling } from '@/composables/usePolling'
import { useListFilters } from '@/composables/useListFilters'
import { statusOf, tagTypeToCls, RUN_STATUS } from '@/composables/useStatus'
import DeliveryChanges from '@/components/DeliveryChanges.vue'
import DeliveryBuilds from '@/components/DeliveryBuilds.vue'
import DeliveryReleases from '@/components/DeliveryReleases.vue'
import DeliveryPipelines from '@/components/DeliveryPipelines.vue'
import DeliveryRepos from '@/components/DeliveryRepos.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import Icon from '@/components/Icon.vue'
const props = defineProps<{ appId: string }>()
const route = useRoute()
const router = useRouter()
const { filters } = useListFilters({ section: 'runs', q: '', status: '' })
const tabs = [{ id: 'runs', label: '运行记录' }, { id: 'agent', label: '智能研发' }, { id: 'changes', label: '变更与批次' }, { id: 'pipelines', label: '流水线' }, { id: 'builds', label: '构建与镜像' }, { id: 'releases', label: '发布' }, { id: 'repos', label: '代码仓库' }]
const legacy: Record<string, string> = { repositories: 'repos', images: 'builds', pipelines: 'pipelines', changes: 'changes', builds: 'builds', releases: 'releases' }
const section = computed(() => !route.query.section && legacy[String(route.query.tab)] ? legacy[String(route.query.tab)] : tabs.some((t) => t.id === filters.section) ? filters.section : 'runs')
function pick(id: string) { void router.replace({ query: { ...route.query, tab: 'delivery', section: id, focus: undefined } }) }
const runs = ref<PipelineRun[]>([])
const loading = ref(true)
const error = ref('')
const page = ref(1)
async function loadRuns() {
  try { runs.value = await listRuns({ appId: props.appId }); error.value = '' }
  catch (e) { error.value = apiError(e, '运行记录加载失败') }
  finally { loading.value = false }
}
usePolling(loadRuns, 5000, { active: () => section.value === 'runs' && runs.value.some((r) => r.status === 'running' || r.status === 'paused') })
const filtered = computed(() => runs.value.filter((r) => (!filters.status || r.status === filters.status) && (!filters.q || (r.id + r.branch + r.trigger).toLowerCase().includes(filters.q.trim().toLowerCase()))).sort((a, b) => b.createdAt.localeCompare(a.createdAt)))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch(filters, () => { page.value = 1 })
const shipRequest = ref('')
const shipBusy = ref(false)
const shipReview = ref(true)
const shipShip = ref(true)
const shipRelease = ref(false)
watch(shipShip, (v) => { if (!v) shipRelease.value = false })
async function runAgentShip() {
  if (!shipRequest.value.trim()) { ElMessage.warning('请描述要交付的需求'); return }
  shipBusy.value = true
  try {
    const run = await triggerAgentShipRun(props.appId, { request: shipRequest.value.trim(), review: shipReview.value, ship: shipShip.value, release: shipRelease.value })
    ElMessage.success('已创建智能研发运行')
    await router.push('/runs/' + run.id)
  } catch (e) { ElMessage.error(apiError(e, '触发失败')) }
  finally { shipBusy.value = false }
}
onMounted(() => { document.title = '产品交付 · PaaS' })
</script>
<template>
  <div class="delivery-workspace">
    <nav class="delivery-navigation" aria-label="产品交付功能"><button v-for="t in tabs" :key="t.id" :class="{ active: section === t.id }" :aria-current="section === t.id ? 'page' : undefined" @click="pick(t.id)">{{ t.label }}</button></nav>
    <section v-if="section === 'runs'">
      <div class="section-heading"><div><h2>运行记录</h2><p>跟进执行进度，从失败阶段直接查看日志。</p></div><button class="act" @click="loadRuns">刷新</button></div>
      <div class="filter-bar"><label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" placeholder="搜索分支或运行 ID" aria-label="搜索产品交付记录" /></label><select v-model="filters.status" class="filter-select" aria-label="运行状态"><option value="">全部状态</option><option v-for="s in ['running', 'paused', 'succeeded', 'failed', 'aborted']" :key="s" :value="s">{{ statusOf(RUN_STATUS, s).label }}</option></select></div>
      <div v-if="loading" class="loading-block" v-loading="true"></div>
      <StatePanel v-else-if="error" title="运行记录加载失败" :description="error" error><button class="act" @click="loadRuns">重试</button></StatePanel>
      <StatePanel v-else-if="!filtered.length" title="没有匹配的运行记录" description="通过流水线运行现有代码，或从智能研发提交新需求。"><button class="act" @click="pick('pipelines')">查看流水线</button><button class="act" @click="pick('agent')">智能研发</button></StatePanel>
      <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table runs-table"><thead><tr><th scope="col">运行</th><th scope="col">状态</th><th scope="col">分支</th><th scope="col">阶段</th><th scope="col">触发方式</th><th scope="col">创建时间</th></tr></thead><tbody><tr v-for="r in rows" :key="r.id"><td><RouterLink :to="'/runs/' + r.id" class="text-button mono">{{ r.id }}</RouterLink></td><td><span class="status" :class="tagTypeToCls(statusOf(RUN_STATUS, r.status).type)">{{ statusOf(RUN_STATUS, r.status).label }}</span></td><td class="mono">{{ r.branch || '—' }}</td><td>{{ Math.min(r.currentStage + 1, r.stageRuns.length) }}/{{ r.stageRuns.length }}</td><td class="dim">{{ r.trigger }}</td><td class="dim">{{ new Date(r.createdAt).toLocaleString('zh-CN') }}</td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
    </section>
    <section v-else-if="section === 'agent'" class="card agent-form">
      <div class="section-heading"><div><h2>智能研发</h2><p>提交需求，生成代码分支。审查、上线和版本发布可按需选择，结果在运行详情中跟进。</p></div></div>
      <label for="ship-request" class="field-label">需求描述</label><textarea id="ship-request" v-model="shipRequest" rows="5" placeholder="描述预期行为、涉及的服务，以及可验证的验收条件。" />
      <fieldset><legend>执行范围</legend><label><input type="checkbox" checked disabled /> AI 开发（生成代码分支）</label><label><input v-model="shipReview" type="checkbox" /> AI 代码审查</label><label><input v-model="shipShip" type="checkbox" /> 上线验证（测试部署、审批、金丝雀）</label><label><input v-model="shipRelease" type="checkbox" :disabled="!shipShip" /> 版本发布（合入主干，需先选择上线验证）</label></fieldset>
      <p class="operation-note">实际服务和环境由此产品的智能研发流水线配置决定。运行完成后，仍需检查构建、部署与验证结果。</p>
      <button class="act primary" :disabled="shipBusy || !shipRequest.trim()" @click="runAgentShip">{{ shipBusy ? '创建运行中…' : '提交研发需求' }}</button>
    </section>
    <DeliveryChanges v-else-if="section === 'changes'" :app-id="appId" />
    <DeliveryBuilds v-else-if="section === 'builds'" :app-id="appId" />
    <DeliveryReleases v-else-if="section === 'releases'" :app-id="appId" />
    <DeliveryPipelines v-else-if="section === 'pipelines'" :app-id="appId" />
    <DeliveryRepos v-else :app-id="appId" />
  </div>
</template>
<style scoped>
.delivery-navigation { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 24px; }
.delivery-navigation button { border: 1px solid transparent; background: transparent; color: var(--text-faint); padding: 7px 12px; border-radius: 5px; font-size: 13px; }
.delivery-navigation button:hover { background: var(--surface-hover); }
.delivery-navigation button.active { background: var(--surface); color: var(--text); border-color: var(--border-strong); font-weight: 600; }
.section-heading { display: flex; justify-content: space-between; align-items: flex-start; gap: 20px; margin-bottom: 20px; }
.section-heading h2 { font-size: 17px; margin: 0; }
.section-heading p { color: var(--text-faint); font-size: 13px; margin: 6px 0 0; }
.runs-table { min-width: 900px; }
.runs-table td:first-child { max-width: 250px; overflow-wrap: anywhere; }
.loading-block { height: 200px; }
.agent-form { max-width: 900px; }
.field-label { display: block; font-size: 13px; margin-bottom: 8px; font-weight: 500; }
.agent-form textarea { width: 100%; padding: 12px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); background: var(--surface); color: var(--text); resize: vertical; }
.agent-form fieldset { margin: 24px 0; padding: 16px; border: 1px solid var(--border); border-radius: var(--radius-sm); display: flex; flex-direction: column; gap: 12px; }
.agent-form legend { font-size: 13px; padding: 0 7px; }
.agent-form fieldset label { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.operation-note { color: var(--text-faint); font-size: 12px; margin-bottom: 20px; }
</style>
