<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiError } from '@/api'
import { triggerAgentShipRun, type PipelineRun } from '@/api/pipeline'
import { useListFilters } from '@/composables/useListFilters'
import DeliveryChanges from '@/components/DeliveryChanges.vue'
import DeliveryBuilds from '@/components/DeliveryBuilds.vue'
import DeliveryMatrix from '@/components/DeliveryMatrix.vue'
import DeliveryReleases from '@/components/DeliveryReleases.vue'
import DeliveryPipelines from '@/components/DeliveryPipelines.vue'
import DeliveryRepos from '@/components/DeliveryRepos.vue'
import DeliveryHistory from '@/components/DeliveryHistory.vue'
import CdDeployDialog from '@/components/CdDeployDialog.vue'
import type { DeliveryAction } from '@/domain/delivery'
const props = defineProps<{ appId: string; appName?: string }>()
const route = useRoute()
const router = useRouter()
const { filters } = useListFilters({ section: 'runs', q: '', status: '' })
const tabs = [{ id: 'runs', label: '交付记录' }, { id: 'agent', label: '智能研发' }, { id: 'changes', label: '变更与批次' }, { id: 'pipelines', label: '流水线' }, { id: 'builds', label: '构建与镜像' }, { id: 'releases', label: '部署' }, { id: 'repos', label: '代码仓库' }]
const legacy: Record<string, string> = { repositories: 'repos', images: 'builds', pipelines: 'pipelines', changes: 'changes', builds: 'builds', releases: 'releases' }
const section = computed(() => !route.query.section && legacy[String(route.query.tab)] ? legacy[String(route.query.tab)] : tabs.some((t) => t.id === filters.section) ? filters.section : 'runs')
function pick(id: string) { void router.replace({ query: { ...route.query, tab: 'delivery', section: id, focus: undefined } }) }
const showDelivery = ref(false)
const action = ref<DeliveryAction>('build-deploy')
function start(kind: DeliveryAction) { action.value = kind; showDelivery.value = true }
function opened(run: PipelineRun) { void router.push('/runs/' + run.id) }
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
    <div class="section-heading"><div><h2>产品交付</h2><p>选择操作，预览服务、环境与产物后执行标准流水线。</p></div><div class="delivery-actions"><button class="act" @click="start('build')">构建</button><button class="act" @click="start('deploy')">部署已有镜像</button><button class="act primary" @click="start('build-deploy')">构建并部署</button></div></div>
    <CdDeployDialog v-model="showDelivery" :app-id="appId" :app-name="appName" :action="action" @deployed="opened" />
    <nav class="delivery-navigation" aria-label="产品交付功能"><button v-for="t in tabs" :key="t.id" :class="{ active: section === t.id }" :aria-current="section === t.id ? 'page' : undefined" @click="pick(t.id)">{{ t.label }}</button></nav>
    <DeliveryHistory v-if="section === 'runs'" :app-id="appId" />
    <section v-else-if="section === 'agent'" class="card agent-form">
      <div class="section-heading"><div><h2>智能研发</h2><p>提交需求，生成代码分支。审查、上线和版本发布可按需选择，结果在运行详情中跟进。</p></div></div>
      <label for="ship-request" class="field-label">需求描述</label><textarea id="ship-request" v-model="shipRequest" rows="5" placeholder="描述预期行为、涉及的服务，以及可验证的验收条件。" />
      <fieldset><legend>执行范围</legend><label><input type="checkbox" checked disabled /> AI 开发（生成代码分支）</label><label><input v-model="shipReview" type="checkbox" /> AI 代码审查</label><label><input v-model="shipShip" type="checkbox" /> 上线验证（测试部署、审批、金丝雀）</label><label><input v-model="shipRelease" type="checkbox" :disabled="!shipShip" /> 版本发布（合入主干，需先选择上线验证）</label></fieldset>
      <p class="operation-note">实际服务和环境由此产品的智能研发流水线配置决定。运行完成后，仍需检查构建、部署与验证结果。</p>
      <button class="act primary" :disabled="shipBusy || !shipRequest.trim()" @click="runAgentShip">{{ shipBusy ? '创建运行中…' : '提交研发需求' }}</button>
    </section>
    <DeliveryChanges v-else-if="section === 'changes'" :app-id="appId" />
    <DeliveryBuilds v-else-if="section === 'builds'" :app-id="appId" />
    <template v-else-if="section === 'releases'">
      <DeliveryMatrix :app-id="appId" />
      <DeliveryReleases :app-id="appId" />
    </template>
    <DeliveryPipelines v-else-if="section === 'pipelines'" :app-id="appId" />
    <DeliveryRepos v-else :app-id="appId" :focus-repo-id="typeof route.query.repo === 'string' ? route.query.repo : undefined" />
  </div>
</template>
<style scoped>
.delivery-actions { display: flex; flex-wrap: wrap; gap: 8px; }
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
