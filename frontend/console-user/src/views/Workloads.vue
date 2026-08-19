<script setup lang="ts">
// 工作负载视图：跨应用列表，按类型分 Tab（服务/Job/CronJob）。
// 数据来自 /api/workloads?type=；扩缩容 PUT、删除 DELETE。换 Key（租户）自动重载。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import { fetchAuth } from '@/api'
import {
  listWorkloads, updateWorkload, updateSchedule, deleteWorkload,
  getWorkload, getWorkloadLogs, createWorkload,
  type Workload, type WorkloadDetail,
} from '@/api/workload'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const props = defineProps<{ type?: string }>()

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

// 跳转归属应用详情（工作负载→应用 反向导航）。
function goApp(appId: string) {
  if (appId) router.push(`/applications/${appId}`)
}

const tabs = [
  { key: 'service', label: '服务', icon: 'server', desc: '长驻工作负载（Deployment 语义）' },
  { key: 'job', label: '任务', icon: 'job', desc: '一次性批处理' },
  { key: 'cronjob', label: '定时', icon: 'clock', desc: 'Cron 调度' },
] as const

const activeType = ref<string>(props.type || 'service')
// 三个路由（/workloads/services|jobs|cronjobs）共用本组件 + props.type 区分。
// 组件复用时 setup 不重新执行，必须 watch prop 才能在侧栏菜单切换时同步 tab + 数据。
watch(
  () => props.type,
  (t) => {
    const next = t || 'service'
    if (next !== activeType.value) {
      activeType.value = next
      load()
    }
  },
)
// 环境来自全局 store（顶栏环境选择器，唯一环境切换入口）；页面不再有环境切换控件
const activeEnv = computed(() => envStore.currentEnvId)
const items = ref<Workload[]>([])
const loading = ref(true)
// 应用上下文过滤（从应用详情「部署 tab」跳来带 ?app=）：只显示该应用工作负载，保留上下文。
const appFilter = computed(() => (route.query.app as string) || '')
const filteredItems = computed(() =>
  appFilter.value ? items.value.filter((w) => w.appId === appFilter.value) : items.value,
)
const scaling = ref<string>('') // 正在扩缩容的 id

// statusMeta 仅覆盖已知枚举值；后端返回空串/未知状态时必须兜底，否则
// statusMeta[status] 为 undefined，模板访问 .cls 崩溃整个工作负载列表（与 Applications 同款）。
const STATUS_META: Record<string, { label: string; cls: string }> = {
  // running=进行中语义（黄），与 DevOps/流水线一致；绿仅留给完全就绪的「已完成」。
  // 此前 running=绿与 succeeded=灰并存，同状态跨页两种色。
  running: { label: '运行中', cls: 'ok' },
  deploying: { label: '部署中', cls: 'warn' },
  failed: { label: '异常', cls: 'err' },
  succeeded: { label: '已完成', cls: 'done' },
  pending: { label: '等待', cls: 'idle' },
}
const STATUS_UNKNOWN = { label: '未知', cls: 'idle' }
function statusOf(s: string): { label: string; cls: string } {
  return STATUS_META[s] ?? STATUS_UNKNOWN
}

const envName = computed(() => (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id)

async function load() {
  loading.value = true
  try {
    const params: Record<string, string> = { type: activeType.value }
    if (activeEnv.value) params.envId = activeEnv.value
    items.value = await listWorkloads(params)
  } catch (e) {
    ElMessage.error('加载工作负载失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

async function scale(w: Workload) {
  // prompt 本身就是确认（输入新副本数）；不再叠加前置 confirm，避免连续弹两个模态。
  // 生产环境通过标题前缀 + isProd 上下文体现警示，操作仍受后端 prod:write 兜底。
  try {
    const { value } = await ElMessageBox.prompt(
      `${envStore.isProd ? '⚠️ [生产环境] ' : ''}调整「${w.name}」的副本数`,
      `${envStore.isProd ? '⚠️ [生产环境] ' : ''}扩缩容`,
      {
        confirmButtonText: '应用',
        cancelButtonText: '取消',
        inputValue: String(w.replicas),
        inputType: 'number',
      },
    )
    const replicas = parseInt(value, 10)
    if (Number.isNaN(replicas) || replicas < 0) {
      ElMessage.warning('请输入有效副本数')
      return
    }
    scaling.value = w.id
    await updateWorkload(w.id, { replicas })
    ElMessage.success(`已调整为 ${replicas} 副本`)
    await load()
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      ElMessage.error('扩缩容失败：' + (e as Error).message)
    }
  } finally {
    scaling.value = ''
  }
}

// editSchedule 修改 cronjob 的 cron 表达式（PUT /api/workloads/{id}/schedule）。
// cronjob 专属；生产改调度走 confirmDangerous 二次确认 + 后端 prod:write 兜底。
const editingSchedule = ref<string>('')
async function editSchedule(w: Workload) {
  if (envStore.isProd) {
    const ok = await confirmDangerous({
      action: '修改调度',
      target: w.name,
      requireNameConfirm: true,
      isProd: true,
    })
    if (!ok) return
  }
  try {
    const { value } = await ElMessageBox.prompt(
      `${envStore.isProd ? '⚠️ [生产环境] ' : ''}修改「${w.name}」的 cron 调度表达式`,
      `${envStore.isProd ? '⚠️ [生产环境] ' : ''}改调度`,
      {
        confirmButtonText: '应用',
        cancelButtonText: '取消',
        inputValue: w.schedule || '',
        inputPlaceholder: '如 7 * * * *（每小时第 7 分钟）或 */5 * * * *（每 5 分钟）',
      },
    )
    const schedule = value.trim()
    if (!schedule) {
      ElMessage.warning('调度表达式不能为空')
      return
    }
    editingSchedule.value = w.id
    await updateSchedule(w.id, schedule)
    ElMessage.success('调度已更新')
    await load()
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      ElMessage.error('改调度失败：' + (e as Error).message)
    }
  } finally {
    editingSchedule.value = ''
  }
}

async function remove(w: Workload) {
  // 删除属高危：生产环境要求输入名称确认（防误操作生产）；工作负载按顶栏 scope 过滤，isProd 用 scope
  const ok = await confirmDangerous({
    action: '删除',
    target: w.name,
    requireNameConfirm: envStore.isProd,
    isProd: envStore.isProd,
  })
  if (!ok) return
  try {
    const resp = await deleteWorkload(w.id)
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

function switchType(key: string) {
  activeType.value = key
  load()
}

// —— 工作负载详情（运行实例 + 日志）——
// GET /api/workloads/{id} 返回 {workload, instances}；Job/CronJob 的每次执行对应一个 Pod（实例），
// 点「详情」查看实例列表（状态/重启/节点），每行可查运行日志（GET /api/workloads/{id}/logs?pod=）。
const showDetail = ref(false)
const detailLoading = ref(false)
const detail = ref<WorkloadDetail | null>(null)

// 日志查看（按实例/Pod）
const showLogs = ref(false)
const logsLoading = ref(false)
const logsPod = ref('')
const logsText = ref('')
const logsPrevious = ref(false)

const POD_STATUS_META: Record<string, { label: string; cls: string }> = {
  Running: { label: '运行中', cls: 'ok' },
  Pending: { label: '等待', cls: 'warn' },
  Failed: { label: '失败', cls: 'err' },
  Succeeded: { label: '成功', cls: 'done' },
  Unknown: { label: '未知', cls: 'idle' },
}
function podStatusOf(s: string): { label: string; cls: string } {
  return POD_STATUS_META[s] ?? { label: s || '未知', cls: 'idle' }
}
function fmtTime(t?: string): string {
  if (!t) return '-'
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleString()
}

async function openDetail(w: Workload) {
  showDetail.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await getWorkload(w.id)
  } catch (e) {
    ElMessage.error('加载详情失败：' + (e as Error).message)
  } finally {
    detailLoading.value = false
  }
}

async function viewLogs(pod: string) {
  showLogs.value = true
  logsLoading.value = true
  logsPod.value = pod
  logsText.value = ''
  try {
    const params: Record<string, string> = { pod, tail: '1000' }
    if (logsPrevious.value) params.previous = 'true'
    const resp = await getWorkloadLogs(detail.value?.workload.id ?? '', params)
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    logsText.value = await resp.text()
    if (!logsText.value) logsText.value = '（暂无日志输出）'
  } catch (e) {
    logsText.value = '日志加载失败：' + (e as Error).message + '\n（非集群部署或 Pod 已清理时不可用）'
  } finally {
    logsLoading.value = false
  }
}

async function togglePrevious() {
  logsPrevious.value = !logsPrevious.value
  if (logsPod.value) await viewLogs(logsPod.value)
}

// 创建工作负载：跨应用视图需选应用 + 环境；提交 POST /api/applications/{appId}/workloads。
interface App { id: string; name: string }
const apps = ref<App[]>([])
const showCreate = ref(false)
const submitting = ref(false)
const createForm = ref({
  appId: '',
  name: '',
  image: '',
  replicas: 1,
  port: 0,
  schedule: '',
})

// 镜像候选（对标 Render/Railway 零手填）：所选应用的 ready 构建产物下拉选择，
// 手输仍保留（「自定义镜像」选项）——外部镜像/调试场景。
interface AppImage { id: string; registry: string; tag: string; status: string }
const imageOptions = ref<AppImage[]>([])
async function loadImages(appId: string) {
  if (!appId) { imageOptions.value = []; return }
  try {
    const resp = await fetchAuth(`/api/applications/${appId}/images`)
    if (resp.ok) {
      const list: AppImage[] = (await resp.json()).data ?? []
      imageOptions.value = list.filter((i) => i.status === 'ready')
    }
  } catch { /* 镜像候选加载失败不阻断（可手输） */ }
}
// imageRef：'' = 自定义手输；否则 <registry>/<app>/<tag> 形态的完整引用由下拉驱动 image 字段
const imageRef = ref('')
watch(imageRef, (v) => {
  if (!v) return // 自定义模式不动 image
  const img = imageOptions.value.find((i) => i.id === v)
  if (img) createForm.value.image = `${img.registry}/${img.tag}`
})

async function loadApps() {
  const resp = await fetchAuth('/api/applications')
  if (resp.ok) apps.value = (await resp.json()).data ?? []
}

function openCreate() {
  createForm.value = {
    appId: apps.value[0]?.id ?? '',
    name: '',
    image: '',
    replicas: activeType.value === 'service' ? 1 : 1,
    port: activeType.value === 'service' ? 80 : 0,
    schedule: activeType.value === 'cronjob' ? '*/5 * * * *' : '',
  }
  imageRef.value = ''
  if (apps.value[0]?.id) loadImages(apps.value[0].id)
  showCreate.value = true
}

// Cron 表达式即时校验（5 段：分 时 日 月 周，支持 * / - , 数字）
function validCron(s: string): boolean {
  const re = /^(\S+\s+){4}\S+$/
  return re.test(s.trim())
}

// 创建后部署进度反馈（对标 Vercel Building→Ready）：自动打开详情抽屉，
// 轮询副本就绪，就绪/失败给终态提示，抽屉关闭即停。
let deployWatchTimer: number | undefined
async function watchDeploy(id: string) {
  showDetail.value = true
  detailLoading.value = true
  detail.value = null
  if (deployWatchTimer) window.clearInterval(deployWatchTimer)
  deployWatchTimer = window.setInterval(async () => {
    if (document.hidden) return
    try {
      const d = await getWorkload(id)
      detail.value = d
      detailLoading.value = false
      const w = d.workload
      if (w.status === 'running' && w.ready >= w.replicas) {
        window.clearInterval(deployWatchTimer); deployWatchTimer = undefined
        ElMessage.success(`「${w.name}」已就绪（${w.ready}/${w.replicas}）`)
      } else if (w.status === 'failed') {
        window.clearInterval(deployWatchTimer); deployWatchTimer = undefined
        ElMessage.error(`「${w.name}」部署失败`)
      }
    } catch { /* 短暂网络错误继续轮询 */ }
  }, 2000)
}
onUnmounted(() => { if (deployWatchTimer) window.clearInterval(deployWatchTimer) })

async function submitCreate() {
  const f = createForm.value
  if (!f.appId) { ElMessage.warning('请选择归属应用'); return }
  if (!activeEnv.value) { ElMessage.warning('请先在顶栏选择部署环境（工作负载必须归属一个环境）'); return }
  if (!f.name.trim()) { ElMessage.warning('请输入工作负载名称'); return }
  if (!f.image.trim()) { ElMessage.warning('请选择或输入镜像'); return }
  if (activeType.value === 'cronjob') {
    if (!f.schedule.trim()) { ElMessage.warning('请输入 Cron 调度表达式'); return }
    if (!validCron(f.schedule)) { ElMessage.warning('Cron 表达式格式不正确（应为 5 段：分 时 日 月 周，如 */5 * * * *）'); return }
  }
  const body: Record<string, unknown> = {
    name: f.name.trim(),
    type: activeType.value,
    image: f.image.trim(),
    replicas: Number(f.replicas) || 1,
    port: Number(f.port) || 0,
    envId: activeEnv.value || '',
  }
  if (activeType.value === 'cronjob') body.schedule = f.schedule.trim()
  submitting.value = true
  try {
    const created = await createWorkload(f.appId, body as Partial<Workload>)
    ElMessage.success('已提交部署，等待副本就绪…')
    showCreate.value = false
    await load()
    watchDeploy(created.id)
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

function onKeyChanged() {
  envStore.loadEnvs()
  load()
}
function onEnvChanged() {
  load()
}
onMounted(async () => {
  // 确保 env 列表已加载（App.vue 也加载，并发时兜底）。
  // 环境上下文从 ?env= 恢复统一由 App.vue 处理（避免重复 switchEnv + 二次生产确认弹窗）。
  if (!envStore.envs.length) {
    await envStore.loadEnvs()
  }
  loadApps()
  load()
  window.addEventListener('paas:key-changed', onKeyChanged)
  window.addEventListener('paas:env-changed', onEnvChanged)
})
onUnmounted(() => {
  window.removeEventListener('paas:key-changed', onKeyChanged)
  window.removeEventListener('paas:env-changed', onEnvChanged)
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div class="head-titles">
        <h2>{{ tabs.find((t) => t.key === activeType)?.label }}工作负载</h2>
        <p class="sub">{{ tabs.find((t) => t.key === activeType)?.desc }}</p>
      </div>
      <button class="create-btn" @click="openCreate">+ 部署工作负载</button>
    </div>
    <div class="tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        class="tab"
        :class="{ on: activeType === t.key }"
        @click="switchType(t.key)"
      >
        <Icon :name="t.icon" :size="16" />
        <span>{{ t.label }}</span>
        <span class="tab-desc">{{ t.desc }}</span>
      </button>
    </div>

    <div v-if="loading" class="table-wrap">
      <div v-for="i in 4" :key="i" class="skel-row" />
    </div>

    <EmptyState
      v-else-if="items.length === 0"
      icon="server"
      :text="`当前租户下暂无${tabs.find((t) => t.key === activeType)?.label}工作负载`"
    />

    <div v-else class="table-wrap">
      <table class="tbl">
        <thead>
          <tr>
            <th>名称</th>
            <th>归属应用</th>
            <th>环境</th>
            <th>泳道</th>
            <th>镜像</th>
            <th v-if="activeType === 'service'">入口</th>
            <th v-if="activeType === 'cronjob'">调度</th>
            <th>副本</th>
            <th>状态</th>
            <th class="col-act"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in filteredItems" :key="w.id">
            <td>
              <div class="name-cell">
                <span class="name">{{ w.name }}</span>
                <span class="id mono">{{ w.id }}</span>
              </div>
            </td>
            <td class="mono app-id"><a class="link" @click="goApp(w.appId)">{{ w.appId }}</a></td>
            <td class="env-cell">{{ envName(w.envId) }}</td>
            <td>
              <span v-if="w.laneId && w.laneId !== 'default'" class="lane-tag">{{ w.laneId }}</span>
              <span v-else class="faint">基线</span>
            </td>
            <td class="mono img">{{ w.image }}</td>
            <td v-if="activeType === 'service'">
              <a v-if="w.domain" class="link domain-link" :href="`http://${w.domain}`" target="_blank" rel="noopener"
                :title="w.domain">{{ w.domain.split('.')[0] }} ↗</a>
              <span v-else class="faint">—</span>
            </td>
            <td v-if="activeType === 'cronjob'" class="mono sched">{{ w.schedule }}</td>
            <td>
              <span class="reps mono" :class="{ notready: w.ready < w.replicas }">
                {{ w.ready }}/{{ w.replicas }}
              </span>
            </td>
            <td>
              <span class="status" :class="statusOf(w.status).cls">
                <span v-if="w.status === 'running'" class="pulse-dot" />
                {{ statusOf(w.status).label }}
              </span>
            </td>
            <td class="col-act">
              <button class="act" @click="openDetail(w)">详情</button>
              <button class="act" :disabled="scaling === w.id || activeType === 'cronjob'" @click="scale(w)">
                扩缩容
              </button>
              <button v-if="activeType === 'cronjob'" class="act" :disabled="editingSchedule === w.id" @click="editSchedule(w)">
                改调度
              </button>
              <button class="act danger" @click="remove(w)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 创建工作负载对话框 -->
    <el-dialog v-model="showCreate" :title="`部署${tabs.find((t) => t.key === activeType)?.label}工作负载`" width="520px">
      <el-form label-width="92px">
        <el-form-item label="归属应用" required>
          <el-select v-model="createForm.appId" placeholder="选择应用" style="width: 100%"
            @change="imageRef = ''; createForm.image = ''; loadImages(createForm.appId)">
            <el-option v-for="a in apps" :key="a.id" :label="a.name" :value="a.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="名称" required>
          <el-input v-model="createForm.name" placeholder="如 rec-svc" />
        </el-form-item>
        <el-form-item label="镜像" required>
          <el-select v-if="imageOptions.length" v-model="imageRef" style="width: 100%"
            placeholder="选择构建产物镜像">
            <el-option v-for="i in imageOptions" :key="i.id" :value="i.id" :label="i.tag">
              <span class="mono">{{ i.tag }}</span>
              <span class="img-opt-hint">{{ i.status }}</span>
            </el-option>
            <el-option value="" label="自定义镜像…">自定义镜像…</el-option>
          </el-select>
          <el-input v-if="!imageOptions.length || !imageRef" v-model="createForm.image"
            :placeholder="imageOptions.length ? '自定义镜像，如 nginx:stable' : '暂无构建产物，输入镜像，如 nginx:stable'" />
        </el-form-item>
        <el-form-item v-if="activeType === 'service'" label="副本数">
          <el-input-number v-model="createForm.replicas" :min="1" :max="20" />
        </el-form-item>
        <el-form-item v-if="activeType === 'service'" label="端口">
          <el-input-number v-model="createForm.port" :min="0" :max="65535" />
          <span class="hint">0 = 不建 Service</span>
        </el-form-item>
        <el-form-item v-if="activeType === 'cronjob'" label="调度" required>
          <el-input v-model="createForm.schedule" placeholder="*/5 * * * *" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 工作负载详情：期望态 + 运行实例（Pod 级）+ 实例日志 -->
    <el-drawer v-model="showDetail" title="工作负载详情" size="640px" direction="rtl">
      <div v-loading="detailLoading">
        <template v-if="detail">
          <div class="detail-info">
            <div class="info-row"><span class="k">名称</span><span class="v">{{ detail.workload.name }}</span></div>
            <div class="info-row"><span class="k">ID</span><span class="v mono">{{ detail.workload.id }}</span></div>
            <div class="info-row"><span class="k">镜像</span><span class="v mono">{{ detail.workload.image }}</span></div>
            <div class="info-row">
              <span class="k">副本</span>
              <span class="v mono">{{ detail.workload.ready }}/{{ detail.workload.replicas }}</span>
            </div>
            <div class="info-row"><span class="k">状态</span>
              <span class="status" :class="statusOf(detail.workload.status).cls">{{ statusOf(detail.workload.status).label }}</span>
            </div>
          </div>

          <div class="section-title">运行实例（{{ detail.instances.length }}）</div>
          <div v-if="detail.instances.length === 0" class="empty-hint">
            暂无运行实例（非集群部署或 Pod 未就绪）
          </div>
          <table v-else class="instances-table">
            <thead>
              <tr>
                <th>实例（Pod）</th>
                <th>状态</th>
                <th>就绪</th>
                <th>重启</th>
                <th>节点</th>
                <th>启动时间</th>
                <th class="col-act"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="ins in detail.instances" :key="ins.name">
                <td>
                  <div class="name-cell">
                    <span class="name mono">{{ ins.name }}</span>
                    <span v-if="ins.message" class="msg">{{ ins.message }}</span>
                  </div>
                </td>
                <td>
                  <span class="status" :class="podStatusOf(ins.status).cls">{{ podStatusOf(ins.status).label }}</span>
                </td>
                <td class="mono">{{ ins.ready || '-' }}</td>
                <td class="mono" :class="{ err: ins.restarts > 0 }">{{ ins.restarts }}</td>
                <td class="mono">{{ ins.node || '-' }}</td>
                <td class="mono small">{{ fmtTime(ins.startedAt) }}</td>
                <td class="col-act">
                  <button class="act" @click="viewLogs(ins.name)">日志</button>
                </td>
              </tr>
            </tbody>
          </table>
        </template>
      </div>
    </el-drawer>

    <!-- 实例日志 -->
    <el-dialog v-model="showLogs" :title="`日志：${logsPod}`" width="780px" top="6vh">
      <div class="logs-toolbar">
        <span class="pod-name mono">{{ logsPod }}</span>
        <el-button size="small" :type="logsPrevious ? 'warning' : 'default'" @click="togglePrevious">
          {{ logsPrevious ? '查看当前日志' : '查看上次终止日志' }}
        </el-button>
        <span v-if="logsPrevious" class="prev-hint">（已退出/重启容器的上次日志，排查崩溃关键）</span>
      </div>
      <div v-loading="logsLoading" class="logs-body">
        <pre>{{ logsText }}</pre>
      </div>
    </el-dialog>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}
.page-head h2 {
  margin: 0 0 2px;
  font-size: 18px;
  color: var(--text);
}
.page-head .sub {
  margin: 0;
  font-size: 12px;
  color: var(--text-faint);
}
.create-btn {
  padding: 8px 16px;
  border: 1px solid var(--brand);
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: opacity 0.12s;
  white-space: nowrap;
}
.create-btn:hover {
  opacity: 0.9;
}
.hint {
  margin-left: 10px;
  font-size: 11px;
  color: var(--text-faint);
}
.env-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 14px;
  flex-wrap: wrap;
}
.env-label {
  font-size: 12px;
  color: var(--text-faint);
  margin-right: 4px;
}
.env-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--surface);
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.env-pill:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.env-pill.on {
  background: var(--brand-soft);
  border-color: var(--brand);
  color: var(--brand);
}
.env-pill.prod.on {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  border-color: var(--warning, #f59e0b);
  color: var(--warning, #f59e0b);
}
.env-cluster {
  font-size: 10px;
  opacity: 0.7;
  font-family: var(--mono, monospace);
}
.env-cell {
  font-size: 12px;
  color: var(--text-dim);
  white-space: nowrap;
}
/* 泳道列：feature 泳道 warning tag（基线不显，与 ApplicationDetail 部署 tab 一致） */
.lane-tag {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 4px;
  background: var(--warning-soft, rgba(230, 162, 60, 0.15));
  color: var(--warning, #e6a23c);
  white-space: nowrap;
}
.faint { color: var(--text-dim); font-size: 12px; }
/* 入口链接列（domain 可点开新窗，Railway 式列表直达入口） */
.domain-link { color: var(--brand); text-decoration: none; white-space: nowrap; }
.domain-link:hover { text-decoration: underline; }
.img-opt-hint { float: right; font-size: 11px; color: var(--text-faint); }
.tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 18px;
}
.tab {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.12s;
}
.tab:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.tab.on {
  background: var(--brand-soft);
  border-color: var(--brand);
  color: var(--brand);
}
.tab-desc {
  font-size: 11px;
  color: var(--text-faint);
  font-weight: 400;
}
.table-wrap {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.tbl {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.tbl th {
  text-align: left;
  padding: 12px 16px;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-faint);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  border-bottom: 1px solid var(--border);
  background: var(--surface-2, transparent);
}
.tbl td {
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
  color: var(--text-dim);
}
.tbl tbody tr:last-child td {
  border-bottom: none;
}
.tbl tbody tr:hover td {
  background: var(--surface-2, rgba(255, 255, 255, 0.02));
}
.name-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.name {
  color: var(--text);
  font-weight: 600;
}
.id {
  font-size: 11px;
  color: var(--text-faint);
}
.app-id,
.img {
  color: var(--text-dim);
}
.app-id .link {
  color: var(--brand);
  cursor: pointer;
}
.app-id .link:hover {
  text-decoration: underline;
}
.sched {
  color: var(--brand);
}
.reps {
  font-weight: 600;
  color: var(--success);
}
.reps.notready {
  color: var(--warning);
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}
.status.ok {
  color: var(--success);
}
.status.warn {
  color: var(--warning);
}
.status.err {
  color: var(--danger, #f43f5e);
}
.status.done {
  color: var(--text-faint);
}
.status.idle {
  color: var(--text-faint);
}
.pulse-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 0 0 currentColor;
  animation: pulse 1.6s infinite;
}
@keyframes pulse {
  0% {
    box-shadow: 0 0 0 0 currentColor;
  }
  70% {
    box-shadow: 0 0 0 5px transparent;
  }
  100% {
    box-shadow: 0 0 0 0 transparent;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pulse-dot {
    animation: none;
  }
}
.col-act {
  text-align: right;
  white-space: nowrap;
}
.act {
  padding: 5px 12px;
  margin-left: 8px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.act:hover:not(:disabled) {
  border-color: var(--brand);
  color: var(--brand);
}
.act.danger:hover:not(:disabled) {
  border-color: var(--danger, #f43f5e);
  color: var(--danger, #f43f5e);
}
.act:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.skel-row {
  height: 56px;
  border-bottom: 1px solid var(--border);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2, #1a1f2e) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 60px 20px;
  color: var(--text-faint);
}
.empty p {
  margin: 0;
  font-size: 13px;
}

/* 详情抽屉 */
.detail-info {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 0 16px;
  border-bottom: 1px solid var(--border);
}
.info-row {
  display: flex;
  gap: 12px;
  font-size: 13px;
  align-items: center;
}
.info-row .k {
  width: 48px;
  color: var(--text-faint);
  flex-shrink: 0;
}
.info-row .v {
  color: var(--text);
  word-break: break-all;
}
.section-title {
  margin: 18px 0 10px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.empty-hint {
  padding: 24px;
  text-align: center;
  font-size: 12px;
  color: var(--text-faint);
  background: var(--surface);
  border-radius: var(--radius);
}
.instances-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.instances-table th {
  text-align: left;
  padding: 8px 8px;
  color: var(--text-faint);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.instances-table td {
  padding: 8px 8px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
  color: var(--text);
}
.instances-table .msg {
  display: block;
  font-size: 11px;
  color: var(--danger, #f43f5e);
  margin-top: 2px;
  word-break: break-all;
}
.instances-table .err {
  color: var(--danger, #f43f5e);
}
.mono.small {
  font-size: 11px;
  color: var(--text-faint);
}

/* 日志对话框 */
.logs-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.logs-toolbar .pod-name {
  font-size: 12px;
  color: var(--text-faint);
}
.logs-toolbar .prev-hint {
  font-size: 11px;
  color: var(--text-faint);
}
.logs-body {
  max-height: 60vh;
  overflow: auto;
  background: var(--surface);
  border-radius: var(--radius);
  border: 1px solid var(--border);
}
.logs-body pre {
  margin: 0;
  padding: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text);
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
