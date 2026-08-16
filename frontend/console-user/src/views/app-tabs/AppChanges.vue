<script setup lang="ts">
// 应用详情 - 变更 tab（火车发车模型）：变更列表（创建/放弃）+ 集成批次（发车：collecting→integrate→testing→tested→approve→released）。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  type Change, type IntegrationBatch,
  listChanges, createChange, abandonChange,
  listBatches, createBatch, getBatch, abandonBatch,
  addChangeToBatch, removeChangeFromBatch, integrateBatch, approveBatch, releaseBatch,
} from '@/api/change'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const props = defineProps<{ appId: string }>()
const router = useRouter()

const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const loading = ref(false)

// ---- 变更状态/类型映射 ----
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

async function load() {
  loading.value = true
  try {
    const [cs, bs] = await Promise.all([listChanges(props.appId), listBatches(props.appId)])
    changes.value = cs
    batches.value = bs
  } catch (e: any) {
    ElMessage.error(e.message || '加载变更失败')
  } finally {
    loading.value = false
  }
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
  } catch (e: any) {
    ElMessage.error(e.message || '创建失败')
  } finally {
    creating.value = false
  }
}

async function abandon(c: Change) {
  try {
    await ElMessageBox.confirm(`放弃变更「${c.title}」？`, '放弃确认', { type: 'warning' })
    await abandonChange(props.appId, c.id)
    ElMessage.success('已放弃')
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

function copyText(t: string) {
  navigator.clipboard.writeText(t).then(() => ElMessage.success('已复制'))
}

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

// testing/releasing 轮询（10s，silent）
let pollTimer: ReturnType<typeof setInterval> | null = null
function startPollIfNeeded() {
  const active = batches.value.some((b) => b.status === 'testing' || b.status === 'releasing')
  if (active && !pollTimer) {
    pollTimer = setInterval(async () => {
      try {
        await load()
        if (drawerBid.value) {
          const b = await getBatch(props.appId, drawerBid.value)
          const i = batches.value.findIndex((x) => x.id === b.id)
          if (i >= 0) batches.value[i] = b
        }
      } catch { /* silent */ }
    }, 10_000)
  } else if (!active && pollTimer) {
    clearInterval(pollTimer); pollTimer = null
  }
}
watch(() => batches.value.map((b) => b.status).join(','), startPollIfNeeded, { immediate: true })
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })

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
  } catch (e: any) {
    ElMessage.error(e.message || '移出失败')
  }
}

async function doIntegrate() {
  if (!batch.value) return
  try {
    const b = await integrateBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    ElMessage.success('已发起集成（合并 → 集成测试）')
  } catch (e: any) {
    ElMessage.error(e.message || '集成失败')
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
  } catch (e: any) {
    ElMessage.error(e.message || '批准失败')
  }
}

async function doReleaseRetry() {
  if (!batch.value) return
  try {
    const b = await releaseBatch(props.appId, batch.value.id)
    await refreshBatch(b)
    ElMessage.success('已重试上线')
  } catch (e: any) {
    ElMessage.error(e.message || '重试失败')
  }
}

async function abandonB() {
  if (!batch.value) return
  try {
    await ElMessageBox.confirm(`放弃批次「${batch.value.title}」？`, '放弃确认', { type: 'warning' })
    await abandonBatch(props.appId, batch.value.id)
    ElMessage.success('已放弃')
    drawerBid.value = null
    await load()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
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
  } catch (e: any) {
    ElMessage.error(e.message || '创建批次失败')
  } finally {
    batchCreating.value = false
  }
}

const fmtTime = (t?: string) => (t ? new Date(t).toLocaleString() : '-')
</script>

<template>
  <div class="app-changes" v-loading="loading">
    <div class="actions">
      <el-button type="primary" @click="openCreate">＋ 新建变更</el-button>
      <el-button @click="openBatchCreate">创建集成批次</el-button>
      <el-button text @click="load">刷新</el-button>
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
          <el-tag size="small" :type="(CHANGE_STATUS[row.status]?.type) || 'info'">
            {{ CHANGE_STATUS[row.status]?.label || row.status }}
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
          <el-tag size="small" :type="(BATCH_STATUS[row.status]?.type) || 'info'">
            {{ BATCH_STATUS[row.status]?.label || row.status }}
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
        <el-steps :active="stepActive" align-center finish-status="success"
                  :process-status="stepError ? 'error' : 'process'">
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
          <el-tag size="small" :type="(BATCH_STATUS[batch.status]?.type) || 'info'">
            {{ BATCH_STATUS[batch.status]?.label || batch.status }}
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
          <el-button v-if="['collecting', 'conflict', 'failed'].includes(batch.status)"
                     type="primary" @click="doIntegrate">集成（发车）</el-button>
          <el-button v-if="batch.status === 'tested'" type="danger" @click="doApprove">批准上线</el-button>
          <el-button v-if="batch.status === 'releasing'" type="warning" @click="doReleaseRetry">重试上线</el-button>
          <el-button v-if="!['released', 'abandoned'].includes(batch.status)"
                     text type="danger" @click="abandonB">放弃批次</el-button>
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
</style>
