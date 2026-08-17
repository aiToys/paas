<script setup lang="ts">
// 应用详情 - 流水线 tab（变更驱动发布，参考 cicd.png / OnePaaS deploy 页）：
// ① 顶部：默认「测试环境发布流水线」卡（CI 流水线 stages + 最近运行状态 + 集成发车按钮）
// ② 集成区（上）：当前收集中的批次及其变更（可移除）——集成区非空才能运行流水线
// ③ 待发布变更（下）：open 未入批变更，逐个「添加」进集成区
// 批次 = 集成区（首次添加自动创建）；integrate = 提交发车（merge → CI 构建部署测试环境）。
// 其余流水线管理（CD/自定义）折叠在底部「全部流水线」。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  type Change, type IntegrationBatch,
  listChanges, createChange, abandonChange, listBatches, createBatch,
  addChangeToBatch, removeChangeFromBatch, integrateBatch, abandonBatch,
} from '@/api/change'
import {
  type Pipeline, type PipelineTemplate, type StageDef, type PipelineRun,
  listPipelines, listTemplates, deletePipeline, triggerRun,
} from '@/api/pipeline'
import { fetchAuth } from '@/api'
import PipelineDesigner from './PipelineDesigner.vue'

const props = defineProps<{ appId: string }>()
const router = useRouter()

const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const pipelines = ref<Pipeline[]>([])
const templates = ref<PipelineTemplate[]>([])
const latestRun = ref<PipelineRun | null>(null)
const loading = ref(false)
const busy = ref(false)

// ---- 状态映射 ----
const CHANGE_STATUS: Record<string, { type: string; label: string }> = {
  open: { type: 'info', label: '开发中' },
  integrated: { type: 'warning', label: '已集成' },
  tested: { type: 'warning', label: '已测试' },
  released: { type: 'success', label: '已上线' },
  reverted: { type: 'danger', label: '已回退' },
  abandoned: { type: 'info', label: '已放弃' },
}
const BATCH_STATUS: Record<string, { type: string; label: string }> = {
  collecting: { type: 'info', label: '收集中' },
  building: { type: 'warning', label: '合并中' },
  conflict: { type: 'danger', label: '合并冲突' },
  testing: { type: 'warning', label: '集成测试中' },
  tested: { type: 'warning', label: '测试通过' },
  releasing: { type: 'warning', label: '上线中' },
  released: { type: 'success', label: '已上线' },
  failed: { type: 'danger', label: '测试失败' },
  abandoned: { type: 'info', label: '已放弃' },
}
const RUN_STATUS: Record<string, { type: string; label: string }> = {
  running: { type: 'warning', label: '运行中' },
  paused: { type: 'warning', label: '等审批' },
  succeeded: { type: 'success', label: '成功' },
  failed: { type: 'danger', label: '失败' },
  aborted: { type: 'info', label: '已中止' },
}

// ---- 默认测试环境发布流水线（应用第一条 CI） ----
const ciPipeline = computed(() => pipelines.value.find((p) => p.kind === 'ci') ?? null)
function templateStages(tid?: string): StageDef[] {
  if (!tid) return []
  return templates.value.find((t) => t.id === tid)?.stages ?? []
}
// 集成区可操作态（collecting/conflict/failed 可继续增删/发车）
const ACTIVE_BATCH_ST = ['collecting', 'conflict', 'failed']
const activeBatch = computed(() => batches.value.find((b) => ACTIVE_BATCH_ST.includes(b.status)) ?? null)
// 进行中的批次（testing/releasing 等，集成区只读 + 展示进度）
const runningBatch = computed(() =>
  batches.value.find((b) => ['building', 'testing', 'tested', 'releasing'].includes(b.status)) ?? null)

const stagedChanges = computed(() => {
  const b = activeBatch.value
  if (!b) return []
  return b.changeIds.map((cid) => changes.value.find((c) => c.id === cid)).filter((c): c is Change => !!c)
})
// 待发布变更：open 且不在当前活动批次（含已在其他历史批次的 open 变更不重复列）
const pendingChanges = computed(() =>
  changes.value.filter((c) => c.status === 'open' && (!activeBatch.value || c.batchId !== activeBatch.value.id)))

// 集成区空 → 流水线不可运行（参考「需添加变更至集成区」）
const canRun = computed(() => !!stagedChanges.value.length && !!ciPipeline.value && !busy.value)
const batchRunning = computed(() => !!runningBatch.value)

async function load() {
  loading.value = true
  try {
    const [cs, bs, ps, ts] = await Promise.all([
      listChanges(props.appId), listBatches(props.appId),
      listPipelines(props.appId), listTemplates(),
    ])
    changes.value = cs
    batches.value = bs
    pipelines.value = ps
    templates.value = ts
    await loadLatestRun()
  } catch (e: any) {
    ElMessage.error(e.message || '加载失败')
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 最近一次运行（发车状态 + 运行详情链接）
async function loadLatestRun() {
  try {
    const runs: PipelineRun[] = await fetchAuth('/api/pipelineruns?appId=' + props.appId)
      .then((r) => r.json()).then((j) => j.data ?? [])
    latestRun.value = runs[0] ?? null // 列表按时间倒序
  } catch { /* 非关键 */ }
}

// testing/releasing 轮询（10s，终态自停）
let pollTimer: ReturnType<typeof setInterval> | null = null
function syncPoll() {
  const active = batches.value.some((b) => ['building', 'testing', 'releasing'].includes(b.status))
  if (active && !pollTimer) pollTimer = setInterval(load, 10_000)
  else if (!active && pollTimer) { clearInterval(pollTimer); pollTimer = null }
}
watch(() => batches.value.map((b) => b.status).join(','), syncPoll, { immediate: true })
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })

// ---- 添加进集成区（无活动批次时自动创建） ----
async function addToStage(c: Change) {
  busy.value = true
  try {
    let b = activeBatch.value
    if (!b) {
      const now = new Date()
      const stamp = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}`
      b = await createBatch(props.appId, {
        title: `${stamp} 发车`,
        branch: `integration/${stamp}-1`,
      })
    }
    await addChangeToBatch(props.appId, b.id, c.id)
    ElMessage.success(`已添加「${c.title}」到集成区`)
    await load()
  } catch (e: any) {
    ElMessage.error(e.message || '添加失败')
  } finally {
    busy.value = false
  }
}

async function removeFromStage(cid: string) {
  if (!activeBatch.value) return
  busy.value = true
  try {
    await removeChangeFromBatch(props.appId, activeBatch.value.id, cid)
    ElMessage.success('已移出集成区')
    await load()
  } catch (e: any) {
    ElMessage.error(e.message || '移出失败')
  } finally {
    busy.value = false
  }
}

// ---- 发车：集成（merge → CI 部署测试环境） ----
async function runPipeline() {
  const b = activeBatch.value
  if (!b || !canRun.value) return
  try {
    await ElMessageBox.confirm(
      `发车批次「${b.title}」？将按序合并 ${stagedChanges.value.length} 个变更到集成分支，并触发测试环境发布流水线。`,
      '集成发车', { type: 'info' },
    )
  } catch { return }
  busy.value = true
  try {
    const nb = await integrateBatch(props.appId, b.id)
    ElMessage.success('已发车（合并 → 集成测试）')
    if (nb.runId) router.push(`/devops/runs/${nb.runId}`)
    await load()
  } catch (e: any) {
    ElMessage.error(e.message || '发车失败')
    await load() // 冲突态回读
  } finally {
    busy.value = false
  }
}

// ---- 新建变更 ----
const createDlg = ref(false)
const creating = ref(false)
const createForm = ref<{ title: string; type: 'feat' | 'hotfix'; branchMode: 'create' | 'existing'; branch: string; baseBranch: string }>({
  title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main',
})
function openCreate() {
  createForm.value = { title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main' }
  createDlg.value = true
}
async function doCreate() {
  const f = createForm.value
  if (!f.title.trim()) { ElMessage.warning('请输入变更标题'); return }
  if (!f.branch.trim()) { ElMessage.warning('请输入分支名'); return }
  creating.value = true
  try {
    const created = await createChange(props.appId, {
      title: f.title.trim(), type: f.type, branch: f.branch.trim(),
      baseBranch: f.baseBranch.trim() || 'main', createBranch: f.branchMode === 'create',
    })
    createDlg.value = false
    const cmd = `git fetch origin && git checkout ${created.branch}`
    try {
      await ElMessageBox.alert(cmd, '分支已就绪，本地开始开发：', {
        confirmButtonText: '复制命令', distinguishCancelAndClose: true,
      })
      await navigator.clipboard.writeText(cmd)
      ElMessage.success('已复制')
    } catch { /* 用户直接关闭 */ }
    await load()
  } catch (e: any) {
    ElMessage.error(e.message || '创建失败')
  } finally {
    creating.value = false
  }
}

async function abandonC(c: Change) {
  try {
    await ElMessageBox.confirm(`放弃变更「${c.title}」？`, '放弃确认', { type: 'warning' })
    await abandonChange(props.appId, c.id)
    ElMessage.success('已放弃')
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

async function abandonStage() {
  const b = activeBatch.value
  if (!b) return
  try {
    await ElMessageBox.confirm(`放弃批次「${b.title}」？集成区清空，变更回到待发布列表。`, '放弃确认', { type: 'warning' })
    await abandonBatch(props.appId, b.id)
    ElMessage.success('已放弃')
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

// ---- 底部「全部流水线」管理（CD + 自定义） ----
const showAll = ref(false)
const designerPid = ref<string | null>(null)
async function remove(p: Pipeline) {
  try {
    await ElMessageBox.confirm(`删除流水线「${p.name}」？此操作不可逆。`, '删除确认', { type: 'warning' })
    await deletePipeline(props.appId, p.id)
    ElMessage.success('已删除')
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

// CD 等手动流水线：收集 version + branch 触发（变更驱动发车只覆盖默认 CI）
const cdRunDlg = ref(false)
const cdRunForm = ref<{ pipeline: Pipeline | null; branch: string; version: string }>({ pipeline: null, branch: 'main', version: '' })
function runManual(p: Pipeline) {
  cdRunForm.value = { pipeline: p, branch: p.trigger?.branch || 'main', version: '' }
  cdRunDlg.value = true
}
async function confirmCdRun() {
  const p = cdRunForm.value.pipeline
  if (!p) return
  if (!cdRunForm.value.branch.trim()) { ElMessage.error('请填写分支'); return }
  cdRunDlg.value = false
  try {
    const r = await triggerRun(props.appId, p.id, {
      branch: cdRunForm.value.branch.trim(),
      version: cdRunForm.value.version.trim() || undefined,
    })
    ElMessage.success('已触发运行')
    router.push(`/devops/runs/${r.id}`)
  } catch (e: any) {
    ElMessage.error(e.message || '触发失败')
  }
}

const fmtTime = (t?: string) => (t ? new Date(t).toLocaleString() : '-')
</script>

<template>
  <div class="app-pipelines" v-loading="loading">
    <!-- ① 默认测试环境发布流水线 -->
    <div class="release-card" v-if="ciPipeline">
      <div class="release-head">
        <div>
          <span class="release-title">测试环境发布流水线</span>
          <el-tag v-if="latestRun && RUN_STATUS[latestRun.status]" size="small"
                  :type="RUN_STATUS[latestRun.status].type" class="run-tag"
                  @click="latestRun && router.push(`/devops/runs/${latestRun.id}`)">
            {{ RUN_STATUS[latestRun.status].label }}
          </el-tag>
        </div>
        <div class="release-actions">
          <span v-if="!canRun && !stagedChanges.length" class="hint-warning">需添加变更至集成区</span>
          <span v-else-if="batchRunning" class="hint-warning">批次进行中</span>
          <el-button type="primary" :disabled="!canRun || batchRunning" :loading="busy" @click="runPipeline">
            {{ latestRun?.status === 'failed' ? '重新发车' : '集成发车' }}
          </el-button>
        </div>
      </div>
      <div class="release-stages">
        <template v-for="(s, i) in templateStages(ciPipeline.templateId)" :key="i">
          <span class="stage-chip">{{ s.name }}</span>
          <span v-if="i < templateStages(ciPipeline.templateId).length - 1" class="stage-arrow">→</span>
        </template>
      </div>
    </div>
    <el-alert v-else type="info" :closable="false" title="应用尚无 CI 流水线（新建应用会自动绑定 tpl-ci，如缺失请联系管理员）" style="margin-bottom: 14px;" />

    <!-- ② 集成区（上） -->
    <div class="zone zone-stage">
      <div class="zone-head">
        <span class="zone-title">集成区</span>
        <template v-if="activeBatch">
          <el-tag size="small" :type="BATCH_STATUS[activeBatch.status]?.type || 'info'">
            {{ BATCH_STATUS[activeBatch.status]?.label || activeBatch.status }}
          </el-tag>
          <span class="mono zone-branch">{{ activeBatch.branch }}</span>
          <el-button size="small" text type="danger" @click="abandonStage">放弃批次</el-button>
        </template>
        <span v-else class="faint">（空——从下方待发布变更添加）</span>
      </div>
      <el-table :data="stagedChanges" size="small" empty-text="集成区为空">
        <el-table-column label="变更" min-width="180">
          <template #default="{ row }">
            <a class="link" @click="router.push(`/devops/changes/${row.id}`)">{{ row.title }}</a>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.type === 'hotfix' ? 'danger' : 'success'">{{ row.type }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="分支" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
        </el-table-column>
        <el-table-column label="冲突" width="140">
          <template #default="{ row }">
            <code v-if="row.conflictWith" class="mono conflict">{{ row.conflictWith }}</code>
            <span v-else class="faint">—</span>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button size="small" text type="danger" :disabled="busy" @click="removeFromStage(row.id)">移除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="activeBatch?.runId" class="zone-run">
        关联运行：<a class="link mono" @click="router.push(`/devops/runs/${activeBatch.runId}`)">{{ activeBatch.runId }}</a>
      </div>
    </div>

    <!-- 进行中批次提示（testing 等，集成区只读态） -->
    <div v-if="runningBatch" class="zone zone-running">
      <div class="zone-head">
        <span class="zone-title">进行中批次</span>
        <el-tag size="small" :type="BATCH_STATUS[runningBatch.status]?.type || 'info'">
          {{ BATCH_STATUS[runningBatch.status]?.label || runningBatch.status }}
        </el-tag>
        <span class="mono zone-branch">{{ runningBatch.branch }}</span>
        <a v-if="runningBatch.runId" class="link" @click="router.push(`/devops/runs/${runningBatch.runId}`)">查看运行 →</a>
      </div>
    </div>

    <!-- ③ 待发布变更（下） -->
    <div class="zone zone-pending">
      <div class="zone-head">
        <span class="zone-title">待发布变更（{{ pendingChanges.length }}）</span>
        <el-button size="small" type="primary" @click="openCreate">＋ 新建变更</el-button>
      </div>
      <el-table :data="pendingChanges" size="small" empty-text="暂无待发布变更">
        <el-table-column label="变更" min-width="180">
          <template #default="{ row }">
            <a class="link" @click="router.push(`/devops/changes/${row.id}`)">{{ row.title }}</a>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.type === 'hotfix' ? 'danger' : 'success'">{{ row.type }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="分支" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="CHANGE_STATUS[row.status]?.type || 'info'">
              {{ CHANGE_STATUS[row.status]?.label || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button size="small" text type="primary" :disabled="busy || batchRunning" @click="addToStage(row)">添加</el-button>
            <el-button v-if="row.status === 'open' && !row.batchId" size="small" text type="danger" @click="abandonC(row)">放弃</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="batchRunning" class="faint zone-note">*当前批次进行中，不能添加变更</div>
    </div>

    <!-- 底部：全部流水线（CD + 自定义管理） -->
    <div class="all-pipes">
      <div class="all-head" @click="showAll = !showAll">
        <span>全部流水线（{{ pipelines.length }}）</span>
        <span class="fold">{{ showAll ? '收起 ▴' : '展开 ▾' }}</span>
      </div>
      <div v-if="showAll" class="all-body">
        <div v-for="p in pipelines" :key="p.id" class="pipe-row">
          <el-tag size="small" :type="p.kind === 'ci' ? 'success' : 'warning'">{{ p.kind.toUpperCase() }}</el-tag>
          <span class="pipe-name">{{ p.name }}</span>
          <span class="pipe-stages-hint">{{ templateStages(p.templateId).map((s) => s.name).join(' → ') }}</span>
          <span class="grow"></span>
          <el-button size="small" @click="designerPid = p.id">编辑</el-button>
          <el-button v-if="p.kind !== 'ci'" size="small" type="primary" @click="runManual(p)">运行</el-button>
          <el-button size="small" text type="danger" @click="remove(p)">删除</el-button>
        </div>
        <div v-if="!pipelines.length" class="faint">暂无流水线</div>
      </div>
    </div>

    <!-- CD 手动运行对话框 -->
    <el-dialog v-model="cdRunDlg" :title="`运行发布流水线：${cdRunForm.pipeline?.name || ''}`" width="460px">
      <el-form label-width="80px">
        <el-form-item label="分支">
          <el-input v-model="cdRunForm.branch" placeholder="如 main" />
        </el-form-item>
        <el-form-item label="版本">
          <el-input v-model="cdRunForm.version" placeholder="如 v1.2.0（留空则不写基线版本）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="cdRunDlg = false">取消</el-button>
        <el-button type="primary" @click="confirmCdRun">运行</el-button>
      </template>
    </el-dialog>

    <!-- 新建变更弹窗 -->
    <el-dialog v-model="createDlg" title="新建变更" width="480px">
      <el-form label-width="90px">
        <el-form-item label="标题">
          <el-input v-model="createForm.title" placeholder="如 用户导出功能" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="createForm.type">
            <el-radio value="feat">feat 功能</el-radio>
            <el-radio value="hotfix">hotfix 修复</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="分支方式">
          <el-radio-group v-model="createForm.branchMode">
            <el-radio value="create">平台创建分支</el-radio>
            <el-radio value="existing">引用已有分支</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="分支名">
          <el-input v-model="createForm.branch" placeholder="feat/user-export" class="mono-input" />
        </el-form-item>
        <el-form-item label="基分支">
          <el-input v-model="createForm.baseBranch" placeholder="main" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="doCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 设计器抽屉 -->
    <el-drawer v-model="designerPid" size="60%" title="流水线设计器" @close="designerPid = null">
      <PipelineDesigner v-if="designerPid" :app-id="appId" :pid="designerPid" @saved="load" />
    </el-drawer>
  </div>
</template>

<style scoped>
.app-pipelines { display: flex; flex-direction: column; gap: 14px; }
.release-card { border: 1px solid var(--el-border-color); border-radius: 8px; padding: 14px 16px; background: var(--el-bg-color); }
.release-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
.release-title { font-weight: 600; margin-right: 10px; }
.run-tag { cursor: pointer; }
.release-actions { display: flex; align-items: center; gap: 10px; }
.hint-warning { font-size: 12px; color: var(--el-color-warning); }
.release-stages { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.stage-chip { background: var(--el-fill-color-light); padding: 2px 10px; border-radius: 10px; font-size: 12px; }
.stage-arrow { color: var(--el-text-color-placeholder); font-size: 12px; }
.zone { border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 12px 14px; background: var(--el-bg-color); }
.zone-stage { border-top: 3px solid var(--el-color-primary); }
.zone-pending { border-top: 3px solid var(--el-border-color); }
.zone-running { border-top: 3px solid var(--el-color-warning); }
.zone-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.zone-title { font-weight: 600; font-size: 13px; }
.zone-branch { font-size: 12px; color: var(--el-text-color-secondary); }
.zone-run { margin-top: 8px; font-size: 12.5px; }
.zone-note { margin-top: 6px; font-size: 12px; }
.mono { font-family: var(--el-font-family-mono, monospace); font-size: 12px; }
.mono-input :deep(input) { font-family: monospace; }
.link { color: var(--el-color-primary); cursor: pointer; }
.link:hover { text-decoration: underline; }
.faint { color: var(--el-text-color-placeholder); font-size: 12px; }
.conflict { color: var(--el-color-danger); font-size: 12px; }
.all-pipes { border-top: 1px dashed var(--el-border-color-lighter); padding-top: 10px; }
.all-head { display: flex; justify-content: space-between; cursor: pointer; font-size: 13px; color: var(--el-text-color-secondary); }
.all-head:hover { color: var(--el-color-primary); }
.all-body { margin-top: 10px; display: flex; flex-direction: column; gap: 8px; }
.pipe-row { display: flex; align-items: center; gap: 10px; padding: 6px 10px; border: 1px solid var(--el-border-color-lighter); border-radius: 6px; }
.pipe-name { font-weight: 500; }
.pipe-stages-hint { font-size: 12px; color: var(--el-text-color-placeholder); }
.grow { flex: 1; }
</style>
