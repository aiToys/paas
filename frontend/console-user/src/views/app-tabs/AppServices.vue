<script setup lang="ts">
// 应用详情 - 服务 tab（服务模型 Phase 1）：服务卡片 grid + 新建服务 + 实例抽屉聚合。
// 服务是应用的组成单元（web/backend/agent/static/cron）；「查看实例」按 serviceId 过滤
// 该服务工作负载；工作负载详情/日志走 WorkloadDetailDrawer 组件（与 Workloads 视图单一真源）。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import { fetchJSON, apiError } from '@/api'
import { listPipelines, triggerRun, type Pipeline } from '@/api/pipeline'
import { createService, deleteService, listServices, type ServiceEntity, type ServiceType } from '@/api/service'
import { updateWorkload, type Workload } from '@/api/workload'
import WorkloadDetailDrawer from '@/components/WorkloadDetailDrawer.vue'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const props = defineProps<{ appId: string; appName: string }>()
const emit = defineEmits<{ (e: 'switch-tab', tab: string): void }>()
const router = useRouter()

// 类型元信息：标签 + tag 色（web 蓝/backend 绿/agent 紫/static 橙/cron 灰）。
const TYPE_META: Record<ServiceType, { label: string; tag: 'primary' | 'success' | 'warning' | 'danger' | 'info' }> = {
  web: { label: 'Web', tag: 'primary' },
  backend: { label: 'Backend', tag: 'success' },
  agent: { label: 'Agent', tag: 'warning' },
  static: { label: 'Static', tag: 'danger' },
  cron: { label: 'Cron', tag: 'info' },
}

const services = ref<ServiceEntity[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    services.value = await listServices(props.appId)
  } catch (e) {
    ElMessage.error(apiError(e, '加载服务失败'))
  } finally {
    loading.value = false
  }
}
onMounted(() => { load(); loadExposure() })

// —— 新建服务 ——
interface Repo { id: string; gitUrl: string; source?: string; giteaRepo?: string }
const repos = ref<Repo[]>([])
const showCreate = ref(false)
const submitting = ref(false)
const form = ref<{
  name: string; type: ServiceType; repoId: string; repoPath: string
  port?: number; replicas?: number; modelRef: string; tools: string; schedule: string
}>({ name: '', type: 'web', repoId: '', repoPath: '', port: 8080, replicas: 1, modelRef: '', tools: '', schedule: '' })

async function openCreate() {
  form.value = { name: '', type: 'web', repoId: '', repoPath: '', port: 8080, replicas: 1, modelRef: '', tools: '', schedule: '' }
  showCreate.value = true
  // 仓库下拉懒加载（失败不阻塞表单，仅无选项）。
  if (!repos.value.length) {
    try {
      repos.value = await fetchJSON<Repo[]>(`/api/applications/${props.appId}/repositories`)
    } catch { /* 静默降级 */ }
  }
}

const canSubmit = computed(() => !!form.value.name.trim() && (form.value.type === 'static' || !!form.value.repoId))

async function submitCreate() {
  if (!canSubmit.value) {
    ElMessage.warning(form.value.type === 'static' ? '请填写服务名称' : '请填写服务名称并选择代码仓库')
    return
  }
  submitting.value = true
  try {
    const f = form.value
    const body: Partial<ServiceEntity> = {
      name: f.name.trim(),
      type: f.type,
      repoId: f.repoId || undefined,
      repoPath: f.repoPath.trim() || undefined,
      port: f.type === 'static' ? undefined : f.port,
      replicas: f.replicas,
      modelRef: f.type === 'agent' ? f.modelRef.trim() || undefined : undefined,
      tools: f.type === 'agent' && f.tools.trim()
        ? f.tools.split(/[,，]/).map((t) => t.trim()).filter(Boolean)
        : undefined,
      schedule: f.type === 'cron' ? f.schedule.trim() || undefined : undefined,
    }
    await createService(props.appId, body)
    ElMessage.success(`服务「${body.name}」已创建`)
    showCreate.value = false
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    submitting.value = false
  }
}

// —— 部署：触发 app 的 CI 流水线（构建->部署->冒烟），直达运行页看进度 ——
// 服务实体（含 Port）经 R1 后由 deploy stage 自动采用（单服务零操作）。
// 二次确认（2026-10-13 用户实测反馈：无变更也可无限点，误触即触发完整 CI——
// 重复构建浪费 + 运行记录噪音；确认框让语义诚实：这是「构建并部署」不是仅部署）。
async function onDeploy(s: ServiceEntity) {
  try {
    await ElMessageBox.confirm(
      `将触发「${s.name}」的完整 CI 流水线（构建 → 部署到开发环境 → 冒烟测试 → 写基线）。无新提交时重复触发只会重复构建，确认继续？`,
      '构建并部署确认',
      { type: 'warning', confirmButtonText: '构建并部署', cancelButtonText: '取消' }
    )
  } catch {
    return // 取消
  }
  deployingId.value = s.id
  try {
    const pipes = await listPipelines(props.appId)
    const ci = pipes.find((p: Pipeline) => p.kind === 'ci')
    if (!ci) { ElMessage.warning('未找到 CI 流水线（应用创建时自动绑定，可去「流水线」tab 查看）'); return }
    const r = await triggerRun(props.appId, ci.id, { branch: ci.trigger?.branch || 'main' })
    ElMessage.success(`已触发「${s.name}」构建并部署`)
    router.push(`/devops/runs/${r.id}`)
  } catch (e) {
    ElMessage.error(apiError(e, '触发部署失败'))
  } finally {
    deployingId.value = ''
  }
}
const deployingId = ref('')

// —— 对外暴露（一键）：设置工作负载 domain → reconciler 自动建 host=domain 的 Ingress ——
// 域名自动生成 <应用名>-<服务名>.<集群后缀>；后缀真源 env PAAS_DOMAIN_SUFFIX（GET /api/governance/domain）。
// 前提：服务已部署（工作负载存在且 Port>0 才有 Ingress backend）；未配置后缀时降级提示。
// DNS 是环境层（平台改不了）：提示 *.<后缀> 需解析到集群入口（dev：/etc/hosts）。
const domainSuffix = ref('')
const exposedHosts = ref<Record<string, string>>({}) // serviceId → domain（工作负载 domain 非空）
const workloadIds = ref<Record<string, string>>({})  // serviceId → 基线工作负载 ID（Set 用）
const exposingId = ref('')

async function loadExposure() {
  try {
    const [d, wls] = await Promise.all([
      fetchJSON<{ suffix: string }>('/api/governance/domain'),
      fetchJSON<Workload[]>(`/api/applications/${props.appId}/workloads`)
    ])
    domainSuffix.value = d.suffix || ''
    const hosts: Record<string, string> = {}
    const wids: Record<string, string> = {}
    for (const wl of wls) {
      const sid = wl.serviceId
      if (!sid) continue
      // 基线负载（无泳道/default）才可设对外域名；泳道负载是联调态不对外
      if (!wl.laneId || wl.laneId === 'default') wids[sid] = wids[sid] || wl.id
      if (wl.domain) hosts[sid] = wl.domain
    }
    exposedHosts.value = hosts
    workloadIds.value = wids
  } catch { /* 暴露态加载失败不阻断服务列表（治理端点旧版本可能 404） */ }
}

const sanitizeSeg = (s: string) => s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '') || 'svc'

async function onExpose(s: ServiceEntity) {
  if (!domainSuffix.value) {
    ElMessage.warning('平台未配置域名后缀（PAAS_DOMAIN_SUFFIX），请联系管理员配置后使用一键暴露')
    return
  }
  const wlId = workloadIds.value[s.id]
  if (!wlId) {
    ElMessage.warning('该服务尚无基线工作负载，请先「构建并部署」再对外暴露')
    return
  }
  exposingId.value = s.id
  try {
    const host = `${sanitizeSeg(props.appName)}-${sanitizeSeg(s.name)}.${domainSuffix.value}`
    await updateWorkload(wlId, { domain: host })
    exposedHosts.value = { ...exposedHosts.value, [s.id]: host }
    ElMessage.success(`已暴露：http://${host}/ （Ingress 自动下发；需 *.${domainSuffix.value} 解析到集群入口后可访问）`)
  } catch (e) {
    ElMessage.error(apiError(e, '暴露失败'))
  } finally {
    exposingId.value = ''
  }
}

async function onDelete(s: ServiceEntity) {
  const ok = await confirmDangerous({ action: '删除服务', target: s.name, requireNameConfirm: true })
  if (!ok) return
  try {
    await deleteService(props.appId, s.id)
    ElMessage.success(`服务「${s.name}」已删除`)
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

// —— 实例抽屉（按 serviceId 过滤该服务工作负载；详情/日志复用 WorkloadDetailDrawer 组件）——
const showInstances = ref(false)
const instancesLoading = ref(false)
const focusService = ref<ServiceEntity | null>(null)
const svcWorkloads = ref<Workload[]>([])

const showDetail = ref(false)
const detailWlId = ref('')

function openDetail(w: Workload) {
  detailWlId.value = w.id
  showDetail.value = true
}

async function openInstances(s: ServiceEntity) {
  focusService.value = s
  showInstances.value = true
  instancesLoading.value = true
  try {
    const wls = await fetchJSON<Workload[]>(`/api/applications/${props.appId}/workloads`)
    svcWorkloads.value = wls.filter((w) => w.serviceId === s.id)
  } catch (e) {
    ElMessage.error(apiError(e, '加载工作负载失败'))
    svcWorkloads.value = []
  } finally {
    instancesLoading.value = false
  }
}
</script>

<template>
  <div class="svc-view" v-loading="loading">
    <div class="svc-head">
      <div class="svc-title">服务</div>
      <button class="add-btn" @click="openCreate">+ 新建服务</button>
    </div>

    <div v-if="!services.length && !loading" class="empty">
      <el-empty description="应用由一个或多个服务组成，创建你的第一个服务" />
      <button class="add-btn" @click="openCreate">创建第一个服务</button>
    </div>

    <div v-else class="svc-grid">
      <div v-for="s in services" :key="s.id" class="svc-card">
        <div class="svc-card-head">
          <span class="svc-name mono">{{ s.name }}</span>
          <el-tag size="small" :type="TYPE_META[s.type]?.tag ?? 'info'">{{ TYPE_META[s.type]?.label ?? s.type }}</el-tag>
        </div>
        <div class="svc-meta">
          <span v-if="s.port" class="meta-item">端口 <span class="mono">{{ s.port }}</span></span>
          <span v-if="s.replicas" class="meta-item">副本 <span class="mono">{{ s.replicas }}</span></span>
          <span v-if="s.repoId" class="meta-item faint mono">repo {{ s.repoId }}</span>
          <span v-if="s.repoPath" class="meta-item faint mono">{{ s.repoPath }}</span>
        </div>
        <div v-if="s.type === 'agent' && s.modelRef" class="svc-meta">
          <el-tag size="small" type="warning" effect="plain">模型 {{ s.modelRef }}</el-tag>
          <el-tag v-for="t in s.tools ?? []" :key="t" size="small" effect="plain" class="tool-tag">{{ t }}</el-tag>
        </div>
        <div v-if="s.type === 'cron' && s.schedule" class="svc-meta">
          <span class="meta-item sched mono">{{ s.schedule }}</span>
        </div>
        <div v-if="exposedHosts[s.id]" class="svc-exposed">
          <span class="exposed-tag">已暴露</span>
          <a class="exposed-host mono" :href="`http://${exposedHosts[s.id]}/`" target="_blank" rel="noopener">{{ exposedHosts[s.id] }}</a>
        </div>
        <div class="svc-actions">
          <button class="act primary" :disabled="deployingId === s.id" @click="onDeploy(s)">{{ deployingId === s.id ? '触发中…' : '构建并部署' }}</button>
          <button v-if="!exposedHosts[s.id]" class="act" :disabled="exposingId === s.id" @click="onExpose(s)">{{ exposingId === s.id ? '暴露中…' : '对外暴露' }}</button>
          <button class="act" @click="openInstances(s)">实例</button>
          <button class="act danger" @click="onDelete(s)">删除</button>
        </div>
      </div>
    </div>

    <!-- 服务实例抽屉：该服务的 Workload 列表 → 工作负载详情（实例 + 日志） -->
    <el-drawer v-model="showInstances" :title="`服务实例：${focusService?.name ?? ''}`" size="640px" direction="rtl">
      <div v-loading="instancesLoading">
        <div v-if="!svcWorkloads.length && !instancesLoading" class="empty-hint">
          该服务尚无关联工作负载（未部署或存量未回填）
        </div>
        <div v-else class="wl-list">
          <div v-for="w in svcWorkloads" :key="w.id" class="wl-row clickable" @click="openDetail(w)">
            <div class="wl-main">
              <span class="wl-name">{{ w.name }}</span>
              <span class="wl-type">{{ w.envId }}</span>
              <span v-if="w.laneId && w.laneId !== 'default'" class="wl-lane">泳道 {{ w.laneId }}</span>
              <span class="wl-img mono">{{ w.image }}</span>
            </div>
            <div class="wl-side">
              <span class="reps mono" :class="{ notready: w.ready < w.replicas }">{{ w.ready }}/{{ w.replicas }}</span>
              <span class="wl-status">{{ w.status }}</span>
            </div>
          </div>
        </div>
      </div>
    </el-drawer>

    <!-- 工作负载详情：期望态 + 运行实例（Pod 级）+ 实例日志（组件单一真源） -->
    <WorkloadDetailDrawer v-model="showDetail" :workload-id="detailWlId" />

    <!-- 新建服务 -->
    <el-dialog v-model="showCreate" title="新建服务" width="560px">
      <el-form label-width="92px" label-position="right">
        <el-form-item label="服务名称" required>
          <el-input v-model="form.name" placeholder="应用内唯一，如 shop-web（DNS-1035）" />
        </el-form-item>
        <el-form-item label="服务类型" required>
          <el-select v-model="form.type">
            <el-option v-for="(m, t) in TYPE_META" :key="t" :value="t" :label="m.label" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.type !== 'static'" label="代码仓库" required>
          <el-select v-model="form.repoId" placeholder="选择应用绑定的仓库" clearable>
            <el-option v-for="r in repos" :key="r.id" :value="r.id" :label="r.giteaRepo || r.gitUrl" />
          </el-select>
          <!-- F4：仓库为空时给出行动出口（下拉 No data 死路的引导）——跳代码仓库 tab 绑定后回来。 -->
          <div v-if="!repos.length" class="field-hint">
            应用尚未绑定代码仓库——<a class="link" @click="emit('switch-tab', 'repositories')">去绑定仓库</a>后回来创建服务
          </div>
        </el-form-item>
        <el-form-item v-if="form.type !== 'static'" label="仓库内路径">
          <el-input v-model="form.repoPath" placeholder="monorepo 子目录，如 services/api（可空）" />
        </el-form-item>
        <el-form-item v-if="form.type !== 'static' && form.type !== 'cron'" label="端口">
          <el-input-number v-model="form.port" :min="0" :max="65535" />
        </el-form-item>
        <el-form-item v-if="form.type !== 'cron'" label="副本数">
          <el-input-number v-model="form.replicas" :min="0" :max="64" />
        </el-form-item>
        <el-form-item v-if="form.type === 'agent'" label="模型">
          <el-input v-model="form.modelRef" placeholder="如 glm-5.2（可空，部署时可覆盖）" />
        </el-form-item>
        <el-form-item v-if="form.type === 'agent'" label="工具">
          <el-input v-model="form.tools" placeholder="逗号分隔，如 search_kb, get_product（可空）" />
        </el-form-item>
        <el-form-item v-if="form.type === 'cron'" label="调度" required>
          <el-input v-model="form.schedule" placeholder="Cron 表达式，如 */5 * * * *" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.svc-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.svc-title {
  font-size: 14px;
  font-weight: 600;
}
.add-btn {
  padding: 7px 14px;
  border: none;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 40px 0;
}
.svc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.svc-card {
  padding: 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  transition: border-color 0.12s;
}
.svc-card:hover {
  border-color: var(--border-strong);
}
.svc-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.svc-name {
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
}
.svc-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-dim);
}
.meta-item.faint {
  color: var(--text-faint);
  overflow: hidden;
  text-overflow: ellipsis;
}
.meta-item.sched {
  color: var(--brand);
}
.tool-tag {
  font-size: 11px;
}
/* 已暴露条：绿点 + 可点域名（Ingress 已下发；DNS 解析到集群入口后可访问） */
.svc-exposed {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  padding: 6px 8px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
}
.exposed-tag {
  font-size: 12px;
  color: var(--el-color-success);
  font-weight: 600;
  flex-shrink: 0;
}
.exposed-host {
  font-size: 12px;
  color: var(--el-color-primary);
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.exposed-host:hover { text-decoration: underline; }

.svc-actions {
  display: flex;
  gap: 8px;
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--border);
}
.act {
  padding: 3px 10px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.act:hover {
  border-color: var(--brand);
  color: var(--brand);
}
.field-hint {
  width: 100%;
  font-size: 12px;
  color: var(--text-dim);
  line-height: 1.6;
}
.field-hint .link {
  color: var(--brand);
  cursor: pointer;
}
/* 部署主操作：品牌色实底，与卡片其余轻量动作区分（旅程审计 R2） */
.act.primary {
  border-color: var(--brand);
  background: var(--brand);
  color: #fff;
}
.act.primary:hover {
  filter: brightness(1.1);
  color: #fff;
}
.act.primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.act.danger:hover {
  border-color: var(--danger);
  color: var(--danger);
}

/* —— 实例抽屉（复用 Workloads 模式） —— */
.empty-hint {
  padding: 24px 0;
  font-size: 12.5px;
  color: var(--text-faint);
  text-align: center;
}
.wl-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.wl-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.wl-row.clickable {
  cursor: pointer;
}
.wl-row.clickable:hover {
  background: var(--surface-2);
}
.wl-main {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  min-width: 0;
}
.wl-name {
  font-weight: 600;
  color: var(--text);
}
.wl-type {
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--brand-soft);
  color: var(--brand);
  font-size: 11px;
}
.wl-lane {
  padding: 2px 8px;
  border-radius: 4px;
  background: rgba(245, 158, 11, 0.12);
  color: #f59e0b;
  font-size: 11px;
}
.wl-img {
  font-size: 12px;
  color: var(--text-dim);
  overflow: hidden;
  text-overflow: ellipsis;
}
.wl-side {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.reps {
  font-weight: 600;
  color: var(--success);
}
.reps.notready {
  color: var(--warning);
}
.wl-status {
  font-size: 12px;
  color: var(--text-dim);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
