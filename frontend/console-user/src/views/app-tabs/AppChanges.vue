<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'
// 应用详情 - 变更 tab（火车发车模型）：变更列表（创建/放弃）+ 集成批次（发车：collecting→integrate→testing→tested→approve→released）。
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  type Change, type IntegrationBatch,
  listChanges, createChange, abandonChange,
  listBatches, createBatch, getBatch, abandonBatch,
  addChangeToBatch, removeChangeFromBatch, integrateBatch, approveBatch, releaseBatch,
} from '@/api/change'
import { triggerAgentShipRun, listPipelines, listRuns, type PipelineRun } from '@/api/pipeline'
import { apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { usePolling } from '@/composables/usePolling'
import { CHANGE_STATUS, BATCH_STATUS, statusOf } from '@/composables/useStatus'
import { confirmAbandon } from '@/composables/useAbandonConfirm'

const props = defineProps<{ appId: string }>()
const router = useRouter()

const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const [cs, bs] = await Promise.all([listChanges(props.appId), listBatches(props.appId)])
    changes.value = cs
    batches.value = bs
  } catch (e) {
    ElMessage.error(apiError(e, '加载变更失败'))
  } finally {
    loading.value = false
  }
  loadShipRun() // 状态卡 best-effort（失败隐藏，不阻塞列表）
}
onMounted(load)

// ---- 创建变更 ----
const createDlg = ref(false)
const creating = ref(false)
const createForm = ref<{ title: string; type: 'feat' | 'hotfix'; branchMode: 'create' | 'existing'; branch: string; baseBranch: string }>({
  title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main',
})

function openCreate() {
  createForm.value = { title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main' }
  createDlg.value = true
}

// ---- 智能研发一条龙（2026-10-13）：需求 + 勾选段直触 tpl-agent-ship → 跳 run 详情看时间线。
// 变更由 agent stage registerChange 自动注册（产出分支 open 态入本 tab），无需手动预建；
// 变更详情页「智能研发」按钮保留作单变更二次委派（delegateAgentRun 通道）。
const agentDlg = ref(false)
const agentSubmitting = ref(false)
const agentForm = ref<{ request: string; review: boolean; ship: boolean; release: boolean }>({
  request: '', review: true, ship: true, release: false,
})

function openAgentCreate() {
  agentForm.value = { request: '', review: true, ship: true, release: false }
  agentDlg.value = true
}

async function doAgentCreate() {
  const f = agentForm.value
  const req = f.request.trim()
  if (!req) { ElMessage.warning('请描述需求'); return }
  if (f.release && !f.ship) { ElMessage.warning('发布上线依赖构建部署（ship），请同时勾选'); return }
  agentSubmitting.value = true
  try {
    const run = await triggerAgentShipRun(props.appId, {
      request: req, review: f.review, ship: f.ship, release: f.release,
    })
    agentDlg.value = false
    ElMessage.success('智能研发一条龙已启动，进度见流水线运行页')
    router.push(`/devops/runs/${run.id}`)
  } catch (e) {
    ElMessage.error(apiError(e, '启动一条龙失败'))
  } finally {
    agentSubmitting.value = false
  }
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
    await load()
    const cmd = `git fetch origin && git checkout ${created.branch}`
    try {
      await ElMessageBox.alert(cmd, '分支已就绪，本地开始开发：', {
        confirmButtonText: '复制命令',
        distinguishCancelAndClose: true,
      })
      await navigator.clipboard.writeText(cmd)
      ElMessage.success('已复制')
    } catch { /* 用户直接关闭 */ }
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    creating.value = false
  }
}

async function abandon(c: Change) {
  try {
    if (!(await confirmAbandon('change', c.title))) return
    await abandonChange(props.appId, c.id)
    ElMessage.success('已放弃')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e))
  }
}

// 统一走 utils/clipboard（含非 secure context 降级 + 失败反馈，R10-2/3）

const batchById = (id: string) => batches.value.find((b) => b.id === id)
const changeById = (id: string) => changes.value.find((c) => c.id === id)

// ---- 批次详情抽屉 ----
const drawerBid = ref<string | null>(null)
const batch = computed(() => batches.value.find((b) => b.id === drawerBid.value) ?? null)

// 抽屉打开/切换时拉详情（含 changeIds/pipelineId/runId）
watch(drawerBid, async (bid) => {
  if (!bid) return
  try {
    const b = await getBatch(props.appId, bid)
    const i = batches.value.findIndex((x) => x.id === bid)
    if (i >= 0) batches.value[i] = b; else batches.value.push(b)
  } catch { /* 列表数据兜底 */ }
})

// ---- 一条龙运行状态卡（2026-10-13）：run 详情页无固定菜单入口（独立页），触发后想回访
// 难找——回访地放触发地：本 tab 顶部展示最近一条一条龙 run 的状态与当前 stage，点击深链。
const shipRun = ref<PipelineRun | null>(null)
const shipRunActive = computed(() => shipRun.value?.status === 'running' || shipRun.value?.status === 'paused')
async function loadShipRun() {
  try {
    const pipes = await listPipelines(props.appId)
    const p = pipes.find((x) => x.templateId === 'tpl-agent-ship')
    if (!p) { shipRun.value = null; return }
    const runs = await listRuns({ appId: props.appId, pipelineId: p.id })
    shipRun.value = runs[0] ?? null
  } catch { /* 状态卡降级隐藏 */ }
}
const SHIP_RUN_LABEL: Record<string, string> = {
  running: '运行中', paused: '等待人工确认', succeeded: '已成功', failed: '失败', aborted: '已中止',
}
const shipRunLabel = computed(() => SHIP_RUN_LABEL[shipRun.value?.status || ''] ?? shipRun.value?.status ?? '')
const shipRunTag = computed(() => {
  const s = shipRun.value?.status
  if (s === 'running') return 'primary'
  if (s === 'paused') return 'warning'
  if (s === 'succeeded') return 'success'
  return 'danger'
})
const shipCurrentStage = computed(() => {
  const cur = shipRun.value?.stageRuns[shipRun.value.currentStage]
  return cur?.name ?? ''
})

// testing/releasing 轮询（10s，silent；不可见自动暂停）。一条龙 running 时同频刷状态卡。
async function pollActive() {
  await load()
  if (shipRunActive.value) loadShipRun()
  if (drawerBid.value) {
    try {
      const b = await getBatch(props.appId, drawerBid.value)
      const i = batches.value.findIndex((x) => x.id === b.id)
      if (i >= 0) batches.value[i] = b
    } catch { /* 列表数据兜底 */ }
  }
}
usePolling(pollActive, 10_000, {
  active: () =>
    batches.value.some((b) => b.status === 'testing' || b.status === 'releasing') || shipRunActive.value,
})

// 步骤条：collecting→testing→tested→released；conflict/failed 显异常态
const stepActive = computed(() => {
  const s = batch.value?.status
  if (s === 'released') return 3
  if (s === 'tested') return 2
  if (s === 'testing' || s === 'building') return 1
  return 0
})
const stepError = computed(() => ['conflict', 'failed'].includes(batch.value?.status || ''))

// 抽屉内变更 chips 是否可移出
const chipsRemovable = computed(() => !!batch.value && ['collecting', 'conflict', 'failed'].includes(batch.value.status))

async function removeFromBatch(cid: string) {
  if (!batch.value) return
  try {
    await removeChangeFromBatch(props.appId, batch.value.id, cid)
    ElMessage.success('已移出')
    const b = await getBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '移出失败'))
  }
}

async function doIntegrate() {
  if (!batch.value) return
  try {
    const b = await integrateBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    ElMessage.success('已发起集成（合并 → 集成测试）')
  } catch (e) {
    ElMessage.error(apiError(e, '集成失败'))
  }
}

async function doApprove() {
  if (!batch.value) return
  const ok = await confirmDangerous({ action: '批准上线', target: batch.value.title, isProd: true })
  if (!ok) return
  try {
    const b = await approveBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    ElMessage.success('已批准，进入上线')
  } catch (e) {
    ElMessage.error(apiError(e, '批准失败'))
  }
}

async function doReleaseRetry() {
  if (!batch.value) return
  try {
    const b = await releaseBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    ElMessage.success('已重试上线')
  } catch (e) {
    ElMessage.error(apiError(e, '重试失败'))
  }
}

async function abandonB() {
  if (!batch.value) return
  try {
    if (!(await confirmAbandon('batch', batch.value.title))) return
    await abandonBatch(props.appId, batch.value.id)
    ElMessage.success('已放弃')
    drawerBid.value = null
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e))
  }
}

async function refreshBatch(b: IntegrationBatch) {
  const i = batches.value.findIndex((x) => x.id === b.id)
  if (i >= 0) batches.value[i] = b; else batches.value.push(b)
}

// ---- 创建批次 ----
const batchDlg = ref(false)
const batchCreating = ref(false)
const batchForm = ref<{ title: string; branch: string; changeIds: string[] }>({ title: '', branch: '', changeIds: [] })
const openChanges = computed(() => changes.value.filter((c) => c.status === 'open'))

function openBatchCreate() {
  batchForm.value = { title: '', branch: '', changeIds: [] }
  batchDlg.value = true
}

async function doCreateBatch() {
  const f = batchForm.value
  if (!f.title.trim()) { ElMessage.warning('请输入批次标题'); return }
  if (!f.branch.trim()) { ElMessage.warning('请输入集成分支名'); return }
  batchCreating.value = true
  try {
    const b = await createBatch(props.appId, { title: f.title.trim(), branch: f.branch.trim() })
    // 勾选的 open 变更加入批次
    for (const cid of f.changeIds) await addChangeToBatch(props.appId, b.id, cid)
    batchDlg.value = false
    ElMessage.success('批次已创建')
    await load()
    drawerBid.value = b.id
  } catch (e) {
    ElMessage.error(apiError(e, '创建批次失败'))
  } finally {
    batchCreating.value = false
  }
}

const fmtTime = (t?: string) => (t ? formatDateTime(t) : '-')
</script>

<template>
  <div class="app-changes" v-loading="loading">
    <div class="actions">
      <el-button type="primary" @click="openCreate">＋ 新建变更</el-button>
      <!-- 智能研发主入口（能力曝光对账：委派端点此前藏在变更详情两层下）：填需求一键三连
           （建变更 → 委派 agentrun → 跳详情看进度），把四步旅程压成一步。 -->
      <el-button type="primary" plain @click="openAgentCreate">✨ 智能研发</el-button>
      <el-button @click="openBatchCreate">创建集成批次</el-button>
      <el-button text @click="load">刷新</el-button>
    </div>

    <!-- 一条龙运行状态卡：触发地在变更 tab，回访地也在变更 tab（run 详情页无菜单入口） -->
    <div v-if="shipRun" class="ship-run-card" @click="router.push(`/devops/runs/${shipRun.id}`)">
      <span class="ship-run-icon">✨</span>
      <span class="ship-run-title">智能研发一条龙</span>
      <el-tag :type="shipRunTag" size="small">{{ shipRunLabel }}</el-tag>
      <span v-if="shipRunActive && shipCurrentStage" class="ship-run-stage">当前：{{ shipCurrentStage }}</span>
      <span class="ship-run-link">查看详情 →</span>
    </div>

    <!-- 变更列表：标题可点进收件箱详情页（全生命周期一站式视图） -->
    <div class="section-title">变更</div>
    <el-table :data="changes" size="small" empty-text="暂无变更">
      <el-table-column label="标题" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">
          <a class="link" @click="router.push(`/devops/changes/${row.id}`)">{{ row.title }}</a>
        </template>
      </el-table-column>
      <el-table-column label="类型" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.type === 'hotfix' ? 'danger' : 'success'">{{ row.type }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="分支" min-width="180">
        <template #default="{ row }">
          <span class="mono">{{ row.branch }}</span>
          <el-button size="small" text @click="copyText(row.branch)">复制</el-button>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag size="small" :type="statusOf(CHANGE_STATUS, row.status).type">
            {{ statusOf(CHANGE_STATUS, row.status).label ?? row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="所属批次" width="160">
        <template #default="{ row }">
          <a v-if="row.batchId && batchById(row.batchId)" class="link" @click="drawerBid = row.batchId">
            {{ batchById(row.batchId)!.title }}
          </a>
          <span v-else class="faint">-</span>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column label="下一步" width="200">
        <template #default="{ row }">
          <el-button v-if="row.status === 'open' && !row.batchId" size="small" text type="primary" @click="openBatchCreate">入批集成</el-button>
          <el-button v-else-if="row.conflictWith" size="small" text type="danger" @click="drawerBid = row.batchId">解决冲突</el-button>
          <span v-else-if="row.status === 'open' && row.batchId" class="faint">批次收集中</span>
          <span v-else-if="row.status === 'integrated'" class="faint">集成测试中</span>
          <span v-else-if="row.status === 'tested'" class="faint">待审批</span>
          <span v-else-if="row.status === 'released'" class="faint">✓ 已上线</span>
          <span v-else class="faint">—</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="130">
        <template #default="{ row }">
          <el-button size="small" text type="primary" @click="router.push(`/devops/changes/${row.id}`)">详情</el-button>
          <el-button v-if="row.status === 'open'" size="small" text type="danger" @click="abandon(row)">放弃</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 集成批次 -->
    <div class="section-title section-gap">集成批次（火车发车）</div>
    <el-table :data="batches" size="small" empty-text="暂无批次">
      <el-table-column label="标题" min-width="160">
        <template #default="{ row }">
          <a class="link" @click="drawerBid = row.id">{{ row.title }}</a>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag size="small" :type="statusOf(BATCH_STATUS, row.status).type">
            {{ statusOf(BATCH_STATUS, row.status).label ?? row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="集成分支" min-width="180">
        <template #default="{ row }">
          <span class="mono">{{ row.branch }}</span>
          <el-button size="small" text @click="copyText(row.branch)">复制</el-button>
        </template>
      </el-table-column>
      <el-table-column label="变更数" width="80">
        <template #default="{ row }">{{ row.changeIds?.length ?? 0 }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
      </el-table-column>
    </el-table>

    <!-- 创建变更弹窗 -->
    <!-- 智能研发一条龙：需求文本 + 勾选段 → tpl-agent-ship run → run 详情页看时间线 -->
    <el-dialog v-model="agentDlg" title="智能研发一条龙" width="560px">
      <el-form label-width="72px">
        <el-form-item label="需求描述">
          <el-input
            v-model="agentForm.request"
            type="textarea" :rows="5" maxlength="2000" show-word-limit
            placeholder="描述要实现的需求，AI 从 main 拉出变更分支完成编码（如：给订单列表加一个按状态筛选的下拉框）。完成后自动注册变更。"
          />
        </el-form-item>
        <el-form-item label="执行段">
          <div class="ship-segments">
            <el-checkbox v-model="agentForm.review">AI 代码审查</el-checkbox>
            <el-checkbox v-model="agentForm.ship">构建 + 部署测试泳道 + 冒烟验证</el-checkbox>
            <el-checkbox v-model="agentForm.release">发布上线（金丝雀 + 审批 + 版本 + 合主干）</el-checkbox>
            <div class="ship-hint">
              发布上线前固定有「上线审批」人工卡点；勾选发布需先勾选构建部署。
            </div>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="agentDlg = false">取消</el-button>
        <el-button type="primary" :loading="agentSubmitting" @click="doAgentCreate">开始执行</el-button>
      </template>
    </el-dialog>

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

    <!-- 创建批次弹窗 -->
    <el-dialog v-model="batchDlg" title="创建集成批次" width="480px">
      <el-form label-width="90px">
        <el-form-item label="标题">
          <el-input v-model="batchForm.title" placeholder="如 2026-08-15 发车" />
        </el-form-item>
        <el-form-item label="集成分支">
          <el-input v-model="batchForm.branch" placeholder="integration/20260815-1" class="mono-input" />
        </el-form-item>
        <el-form-item label="加入变更">
          <div v-if="openChanges.length" class="check-group">
            <el-checkbox-group v-model="batchForm.changeIds">
              <el-checkbox v-for="c in openChanges" :key="c.id" :value="c.id">
                {{ c.title }}（{{ c.branch }}）
              </el-checkbox>
            </el-checkbox-group>
          </div>
          <span v-else class="faint">暂无开发中的变更</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchDlg = false">取消</el-button>
        <el-button type="primary" :loading="batchCreating" @click="doCreateBatch">创建</el-button>
      </template>
    </el-dialog>

    <!-- 批次详情抽屉 -->
    <el-drawer v-model="drawerBid" size="46%" :title="batch ? `集成批次：${batch.title}` : '集成批次'">
      <div v-if="batch" class="batch-detail">
        <el-steps
:active="stepActive" align-center finish-status="success"
                  :process-status="stepError ? 'error' : 'process'"
>
          <el-step title="收集中" />
          <el-step title="集成测试" />
          <el-step title="测试通过" />
          <el-step title="已上线" />
        </el-steps>
        <div v-if="stepError" class="error-line">
          {{ batch.status === 'conflict' ? '合并冲突：请移出冲突变更后重新集成' : '集成测试失败：可移出问题变更后重新集成' }}
          <span v-if="batch.status === 'conflict'">(冲突变更：{{ changes.filter(c => c.conflictWith).map(c => c.title).join('、') || '-' }})</span>
        </div>

        <div class="detail-row">
          <span class="label">集成分支：</span>
          <span class="mono">{{ batch.branch }}</span>
          <el-button size="small" text @click="copyText(batch.branch)">复制</el-button>
        </div>
        <div class="detail-row">
          <span class="label">状态：</span>
          <el-tag size="small" :type="statusOf(BATCH_STATUS, batch.status).type">
            {{ statusOf(BATCH_STATUS, batch.status).label ?? batch.status }}
          </el-tag>
        </div>
        <div class="detail-row" v-if="batch.runId">
          <span class="label">关联运行：</span>
          <router-link :to="'/devops/runs/' + batch.runId" class="link">查看运行</router-link>
        </div>

        <div class="chips-title">批次变更（{{ batch.changeIds?.length ?? 0 }}）</div>
        <div class="chips">
          <span v-for="cid in batch.changeIds" :key="cid" class="chip">
            {{ changeById(cid)?.title || cid }}
            <span v-if="chipsRemovable" class="chip-x" @click="removeFromBatch(cid)">×</span>
          </span>
          <span v-if="!batch.changeIds?.length" class="faint">（空批次，可从上方变更列表加入）</span>
        </div>

        <div class="detail-actions">
          <el-button
v-if="['collecting', 'conflict', 'failed'].includes(batch.status)"
                     type="primary" @click="doIntegrate"
>
集成（发车）
</el-button>
          <el-button v-if="batch.status === 'tested'" type="danger" @click="doApprove">批准上线</el-button>
          <el-button v-if="batch.status === 'releasing'" type="warning" @click="doReleaseRetry">重试上线</el-button>
          <el-button
v-if="!['released', 'abandoned'].includes(batch.status)"
                     text type="danger" @click="abandonB"
>
放弃批次
</el-button>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.actions { display: flex; gap: 8px; margin-bottom: 16px; }
.section-title { font-weight: 600; margin-bottom: 10px; }
.section-gap { margin-top: 28px; }
.mono { font-family: var(--el-font-family-mono, monospace); font-size: 12px; }
.mono-input :deep(input) { font-family: monospace; }
.link { color: var(--el-color-primary); cursor: pointer; }
.faint { color: var(--el-text-color-placeholder); font-size: 12px; }
.check-group { max-height: 220px; overflow: auto; border: 1px solid var(--el-border-color-lighter); border-radius: 4px; padding: 8px 12px; }
.check-group :deep(.el-checkbox) { display: flex; }
.batch-detail { display: flex; flex-direction: column; gap: 14px; }
.error-line { color: var(--el-color-danger); font-size: 13px; }
.detail-row { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.detail-row .label { color: var(--el-text-color-secondary); }
.chips-title { font-weight: 600; margin-top: 6px; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chip { background: var(--el-fill-color-light); border-radius: 12px; padding: 3px 10px; font-size: 12px; display: inline-flex; align-items: center; gap: 4px; }
.chip-x { cursor: pointer; color: var(--el-color-danger); font-weight: 700; }
.detail-actions { margin-top: 12px; display: flex; gap: 8px; }
.ship-segments { display: flex; flex-direction: column; gap: 8px; width: 100%; }
.ship-hint { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.5; }
.ship-run-card {
  display: flex; align-items: center; gap: 10px; padding: 10px 14px; margin-bottom: 12px;
  background: var(--el-color-primary-light-9); border: 1px solid var(--el-color-primary-light-7);
  border-radius: 8px; cursor: pointer;
}
.ship-run-card:hover { border-color: var(--el-color-primary-light-5); }
.ship-run-icon { font-size: 16px; }
.ship-run-title { font-weight: 600; font-size: 13px; }
.ship-run-stage { font-size: 12px; color: var(--el-text-color-secondary); }
.ship-run-link { margin-left: auto; font-size: 12px; color: var(--el-color-primary); }
</style>
