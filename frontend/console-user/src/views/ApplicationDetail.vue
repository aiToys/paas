<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import DetailShell from '@/components/DetailShell.vue'
import { fetchAuth, fetchJSON, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import type { Workload } from '@/api/workload'
import AppServices from './app-tabs/AppServices.vue'
import AppOverview from './app-tabs/AppOverview.vue'
import AppDeploy from './app-tabs/AppDeploy.vue'
import AppBindings from './app-tabs/AppBindings.vue'
import AppMembers from './app-tabs/AppMembers.vue'
import AppRepositories from './app-tabs/AppRepositories.vue'
import AppPipelines from './app-tabs/AppPipelines.vue'
import AppChanges from './app-tabs/AppChanges.vue'
import AppBuilds from './app-tabs/AppBuilds.vue'
import AppImages from './app-tabs/AppImages.vue'
import AppReleases from './app-tabs/AppReleases.vue'
import AppConfigs from './app-tabs/AppConfigs.vue'
import AppDynamicConfigs from './app-tabs/AppDynamicConfigs.vue'
import AppGovernance from './app-tabs/AppGovernance.vue'
import AppObservability from './app-tabs/AppObservability.vue'
import AppUsage from './app-tabs/AppUsage.vue'
import type { App } from './app-tabs/resource-meta'

const route = useRoute()
const router = useRouter()

const app = ref<App | null>(null)
const loading = ref(true)

// —— 资源规格默认值（应用级 ResourceTemplate：deploy 未显式指定时继承）——
const resFields = [
  { key: 'cpuRequest' as const, label: 'CPU 请求', ph: '如 500m' },
  { key: 'cpuLimit' as const, label: 'CPU 上限', ph: '如 2' },
  { key: 'memRequest' as const, label: '内存请求', ph: '如 256Mi' },
  { key: 'memLimit' as const, label: '内存上限', ph: '如 1Gi' },
]
const resForm = ref({ cpuRequest: '', cpuLimit: '', memRequest: '', memLimit: '' })
const resSaving = ref(false)

async function saveResTemplate() {
  if (!app.value) return
  resSaving.value = true
  try {
    app.value = await fetchJSON<App>(`/api/applications/${app.value.id}/resource-template`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(resForm.value),
    })
    ElMessage.success('资源规格默认值已保存')
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    resSaving.value = false
  }
}

const statusLabel: Record<string, string> = {
  healthy: '健康',
  degraded: '降级',
  idle: '空闲',
}

const totalBindings = computed(() => app.value?.bindings?.length ?? 0)

// —— 副数据：工作负载（部署 tab + 概览副本统计共享）与环境（分组映射）——
// 概览专属数据（指标/发布/构建）由 AppOverview 自拉（切到概览 tab 才加载，首屏不白拉）。
interface Env { id: string; name: string; type: string }
const workloads = ref<Workload[]>([])
const envs = ref<Env[]>([])
// 加载失败态：与「确无数据」区分——失败静默置空会让部署 tab 显示「尚未部署工作负载」，
// 与真实空态语义相反，误导排障方向（以为是没部署，实际是请求失败）。
const workloadsFailed = ref(false)

async function loadSecondary() {
  const id = route.params.id as string
  workloadsFailed.value = false
  const [ws, es] = await Promise.allSettled([
    fetchJSON<Workload[]>(`/api/applications/${id}/workloads`),
    fetchJSON<Env[]>('/api/environments'),
  ])
  // 已切换应用（路由参数变了）则丢弃过期响应，防旧应用数据覆盖新应用。
  if (route.params.id !== id) return
  workloads.value = ws.status === 'fulfilled' ? ws.value : []
  workloadsFailed.value = ws.status === 'rejected'
  envs.value = es.status === 'fulfilled' ? es.value : []
}

async function load() {
  loading.value = true
  const id = route.params.id as string
  try {
    // fetchJSON 自动解包 {data:T} 契约，杜绝手动 json.data ?? json 的契约遗漏。
    app.value = await fetchJSON<App>(`/api/applications/${id}`)
    // 资源规格默认值回填表单（应用级 ResourceTemplate）
    const rt = app.value.resourceTemplate
    resForm.value.cpuRequest = rt?.cpuRequest ?? ''
    resForm.value.cpuLimit = rt?.cpuLimit ?? ''
    resForm.value.memRequest = rt?.memRequest ?? ''
    resForm.value.memLimit = rt?.memLimit ?? ''
    // 副数据不阻塞首屏：主信息到了即关 loading，副数据并行补齐。
    void loadSecondary()
  } catch (e) {
    // 已删实体（书签/外链 404）：明确引导返回应用列表，不留静默半空页。
    if (apiError(e).includes('not found') || apiError(e).includes('404')) {
      ElMessage.error('应用不存在或已删除')
      router.push('/applications')
      return
    }
    ElMessage.error(apiError(e, '加载应用失败'))
  } finally {
    loading.value = false
  }
}

onMounted(load)
// 同组件复用（列表切不同应用详情）时 watch 路由参数刷新，避免显示上一张应用陈旧数据。
watch(() => route.params.id, load)

// —— tab 壳：单一真源派生（名称 + URL query 短名 + 视觉分组）——
// tabGroups / TAB_TO_Q / TabName 均从 TABS 派生，新增 tab 只改这一处（防多处漂移）。
const TABS = [
  { name: '服务', q: 'services', group: '运行态' },
  { name: '概览', q: 'overview', group: '运行态' },
  { name: '部署', q: 'deploy', group: '运行态' },
  { name: '服务治理', q: 'governance', group: '运行态' },
  { name: '可观测', q: 'observability', group: '运行态' },
  { name: '资源绑定', q: 'bindings', group: '资源' },
  { name: '配置', q: 'configs', group: '资源' },
  { name: '成员与权限', q: 'members', group: '资源' },
  { name: '用量', q: 'usage', group: '资源' },
  { name: '流水线', q: 'pipelines', group: 'DevOps' },
  { name: '变更', q: 'changes', group: 'DevOps' },
  { name: '代码仓库', q: 'repositories', group: 'DevOps' },
  { name: '构建', q: 'builds', group: 'DevOps' },
  { name: '镜像', q: 'images', group: 'DevOps' },
  { name: '发布', q: 'releases', group: 'DevOps' },
] as const
type TabName = (typeof TABS)[number]['name']
const TAB_TO_Q = Object.fromEntries(TABS.map((t) => [t.name, t.q])) as Record<TabName, string>
const Q_TO_TAB: Record<string, TabName> = Object.fromEntries(TABS.map((t) => [t.q, t.name]))
const tabGroups = (['运行态', '资源', 'DevOps'] as const).map((label) => ({
  label,
  tabs: TABS.filter((t) => t.group === label).map((t) => t.name),
}))

// activeTab 与 URL ?tab= 双向同步：初始化从 query 读（分享/刷新直达指定 tab），切换时写回 query。
const activeTab = ref<TabName>(Q_TO_TAB[(route.query.tab as string) ?? ''] ?? '服务')
watch(activeTab, (t) => {
  const q = TAB_TO_Q[t]
  // 仅在 query 缺失或不一致时 replace（避免每帧推历史致后退栈膨胀）。
  if ((route.query.tab as string) !== q) {
    router.replace({ query: { ...route.query, tab: q } })
  }
})
// 浏览器前进/后退（query 变）时同步 activeTab，保持 URL 与视图一致。
watch(
  () => route.query.tab,
  (q) => {
    const t = Q_TO_TAB[(q as string) ?? ''] ?? '服务'
    if (t !== activeTab.value) activeTab.value = t
  },
)

// 深链定位：?repo=<id> 打开仓库浏览抽屉 / ?image=<id> 展开镜像行（从构建/发布/变更等详情页跳入精确定位）
const focusRepoId = ref((route.query.repo as string) || '')
const focusImageId = ref((route.query.image as string) || '')
watch(() => route.query, (q) => {
  focusRepoId.value = (q.repo as string) || ''
  focusImageId.value = (q.image as string) || ''
})

// 镜像 tab 点「发布」-> 切到发布 tab 并预选镜像（pickedImageId 变化触发 AppReleases 打开创建弹窗）
const pickedImageId = ref('')
async function pickImage(img: { id: string }) {
  pickedImageId.value = ''
  await nextTick()
  pickedImageId.value = img.id
  activeTab.value = '发布'
}

// 跳 DevOps 跨应用总览。
function goDevOps() {
  router.push('/devops')
}

// 删除应用（级联清工作负载+配置）：高危，统一走 confirmDangerous（输入应用名确认）。
// 删除后返回应用列表。
async function deleteApp() {
  if (!app.value) return
  const ok = await confirmDangerous({
    action: '删除应用',
    target: app.value.name,
    requireNameConfirm: true,
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/applications/${app.value!.id}`, { method: 'DELETE' })
    if (!resp.ok) {
      const e = await resp.json().catch(() => ({}))
      ElMessage.error('删除失败：' + (e.error || resp.statusText))
      return
    }
    ElMessage.success('应用已删除')
    router.push('/applications')
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}
</script>

<template>
  <div class="detail">
    <DetailShell
      :crumbs="[{ label: '应用', to: '/applications' }, { label: app?.name ?? '' }]"
      :tags="app ? [
        { label: app.env, cls: app.env === 'prod' ? 'prodenv' : 'envchip' },
        { label: statusLabel[app.status] ?? app.status, cls: 'health' },
      ] : []"
      :loading="loading"
    >
      <template #actions>
        <el-dropdown trigger="click" placement="bottom-end">
          <button class="crumb-more" title="更多操作">
            <span class="crumb-dots">⋯</span>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item class="danger-item" @click="deleteApp">🗑 删除应用</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </template>
    </DetailShell>
    <template v-if="app">
      <div class="tabs">
        <template v-for="g in tabGroups" :key="g.label">
          <span class="tab-group-label">{{ g.label }}</span>
          <button
            v-for="t in g.tabs"
            :key="t"
            class="tab"
            :class="{ on: activeTab === t }"
            @click="activeTab = t"
          >
            {{ t }}
            <span v-if="t === '资源绑定'" class="tab-count mono">{{ totalBindings }}</span>
          </button>
        </template>
      </div>

      <!-- 服务（应用组成单元，Phase 1 置顶默认） -->
      <div v-if="activeTab === '服务'">
        <AppServices :app-id="app.id" :app-name="app.name" @switch-tab="t => activeTab = t as TabName" />
      </div>

      <!-- 概览 = 真实工作台（概览专属数据子组件自拉） -->
      <div v-else-if="activeTab === '概览'">
        <AppOverview :app="app" :workloads="workloads" />
      </div>

      <!-- 部署 = 工作负载运行形态（点行开详情抽屉，子组件内） -->
      <div v-else-if="activeTab === '部署'">
        <AppDeploy :workloads="workloads" :envs="envs" :failed="workloadsFailed" @retry="loadSecondary" />
      </div>

      <!-- 服务治理 -->
      <div v-else-if="activeTab === '服务治理'">
        <AppGovernance :app-id="app.id" />
      </div>

      <!-- 可观测 -->
      <div v-else-if="activeTab === '可观测'">
        <AppObservability :app-id="app.id" :bindings="app.bindings ?? []" />
      </div>

      <!-- 变更（火车发车模型：变更 + 集成批次） -->
      <div v-else-if="activeTab === '变更'">
        <AppChanges :app-id="app.id" />
      </div>

      <!-- 代码仓库 -->
      <div v-else-if="activeTab === '代码仓库'">
        <AppRepositories :app-id="app.id" :focus-repo-id="focusRepoId" @repo-focused="focusRepoId = ''" />
      </div>

      <!-- 流水线（DevOps 分组首位，主线） -->
      <div v-else-if="activeTab === '流水线'">
        <AppPipelines :app-id="app.id" />
      </div>

      <!-- 构建 -->
      <div v-else-if="activeTab === '构建'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用构建总览 →</a></div>
        <AppBuilds :app-id="app.id" @pick="pickImage" />
      </div>

      <!-- 镜像（构建产物） -->
      <div v-else-if="activeTab === '镜像'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用镜像总览 →</a></div>
        <AppImages :app-id="app.id" :focus-image-id="focusImageId" @image-focused="focusImageId = ''" @pick="pickImage" />
      </div>

      <!-- 发布 -->
      <div v-else-if="activeTab === '发布'">
        <div class="cross-link"><a @click="goDevOps">查看跨应用发布总览 →</a></div>
        <AppReleases :app-id="app.id" :picked-image-id="pickedImageId" />
      </div>

      <!-- 资源绑定（核心） -->
      <div v-else-if="activeTab === '资源绑定'">
        <AppBindings :app="app" @updated="a => app = a" />
      </div>

      <!-- 配置（工作负载级 env/Secret + 应用级资源规格默认值） -->
      <div v-else-if="activeTab === '配置'">
        <div class="card" style="margin-bottom: 16px; padding: 16px">
          <h3 class="card-title">资源规格默认值（新部署继承）</h3>
          <el-form inline>
            <el-form-item v-for="f in resFields" :key="f.key" :label="f.label">
              <el-input v-model="resForm[f.key]" :placeholder="f.ph" style="width: 120px" />
            </el-form-item>
            <el-form-item>
              <el-button size="small" type="primary" :loading="resSaving" @click="saveResTemplate">保存</el-button>
            </el-form-item>
          </el-form>
          <div style="font-size: 12px; color: var(--el-text-color-secondary)">
            留空 = 不继承（BestEffort）。生产环境部署必须有 CPU/内存规格，建议至少填 CPU 请求/内存请求。
          </div>
        </div>
        <AppConfigs :app-id="app.id" />
        <AppDynamicConfigs :app-id="app.id" />
      </div>

      <!-- 成员与权限（应用级权限：成员角色 + 受限模式开关） -->
      <div v-else-if="activeTab === '成员与权限'">
        <AppMembers :app="app" />
      </div>

      <!-- 用量 -->
      <div v-else-if="activeTab === '用量'">
        <AppUsage :app-id="app.id" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.detail {
  max-width: 1100px;
  margin: 0 auto;
}
/* 资源规格默认值卡（配置 tab 顶部） */
.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.card-title {
  font-size: 14px;
  margin: 0 0 12px;
}
/* 身份 chips（DetailShell cls 自定义变体）：env 绿/prod 红、health 带脉动点 */
:deep(.shell-chip.envchip) {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--success-soft);
  color: var(--success);
}
:deep(.shell-chip.prodenv) {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 11px;
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}
:deep(.shell-chip.health) {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  padding: 0;
  background: transparent;
  color: var(--success);
}
:deep(.shell-chip.health)::before {
  content: '';
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--success);
  animation: pulse 1.8s infinite;
}
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
.crumb-more {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.12s;
}
.crumb-more:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.crumb-dots {
  font-size: 20px;
  line-height: 1;
  color: var(--text-dim);
}
:deep(.danger-item) {
  color: var(--el-color-danger, #f43f5e);
}
.tabs {
  display: flex;
  flex-wrap: nowrap;
  gap: 2px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 20px;
  overflow-x: auto;
  scrollbar-width: none; /* Firefox 隐藏滚动条，靠拖拽/滚轮横向滚动 */
}
.tabs::-webkit-scrollbar {
  display: none;
}
.tab {
  position: relative;
  padding: 10px 12px;
  white-space: nowrap;
  flex-shrink: 0;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13.5px;
  font-weight: 500;
  cursor: pointer;
  transition: color 0.12s;
}
.tab:hover {
  color: var(--text);
}
.tab.on {
  color: var(--text);
}
.tab.on::after {
  content: '';
  position: absolute;
  left: 12px;
  right: 12px;
  bottom: -1px;
  height: 2px;
  background: var(--brand);
  border-radius: 2px;
}
.tab-count {
  margin-left: 4px;
  font-size: 11px;
  color: var(--text-faint);
}
.tab-group-label {
  font-size: 11px;
  color: var(--text-faint);
  padding: 0 6px 0 0;
  margin-right: 2px;
  border-right: 1px solid var(--border);
  align-self: center;
  height: 16px;
  line-height: 16px;
  white-space: nowrap;
  flex-shrink: 0;
}
.cross-link {
  margin-bottom: 12px;
  font-size: 12px;
}
.cross-link a {
  color: var(--brand);
  cursor: pointer;
}
.cross-link a:hover {
  text-decoration: underline;
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
