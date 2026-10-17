<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
import StatePanel from './StatePanel.vue'
// 交付·变更与集成批次（v2 原生）：Change（分支粒度）+ IntegrationBatch（火车发车状态机）。
// 闭环补齐（spec §B/评审 F2）：新建变更（人工分支 / Agent 委派在详情页）、新建批次并
// 勾选 open 变更自动入批——此前组织入口缺失，人工研发路径被削弱。
import { nextTick, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  listChanges, listBatches, integrateBatch, approveBatch, releaseBatch,
  createChange, createBatch, addChangeToBatch,
  type Change, type IntegrationBatch,
} from '@/api/change'
import { apiError } from '@/api'
import { statusOf, tagTypeToCls, CHANGE_STATUS, BATCH_STATUS } from '@/composables/useStatus'

const route = useRoute()
const props = defineProps<{ appId: string }>()

const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const loading = ref(true)
const error = ref('')
const acting = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [cs, bs] = await Promise.all([
      listChanges(props.appId),
      listBatches(props.appId),
    ])
    changes.value = cs
    batches.value = bs
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
    await nextTick()
    const focus = route.query.focus
    if (typeof focus === 'string') { const row = document.getElementById('entity-' + focus); row?.scrollIntoView({ block: 'center' }); row?.focus() }
  }
}
onMounted(load)

async function act(kind: 'integrate' | 'approve' | 'release', b: IntegrationBatch) {
  if (!await confirmAction(`确认对批次「${b.title}」执行「${kind === 'integrate' ? '集成' : kind === 'approve' ? '审批发车' : '发布'}」？`)) return
  acting.value = `${kind}:${b.id}`
  try {
    if (kind === 'integrate') await integrateBatch(props.appId, b.id)
    else if (kind === 'approve') await approveBatch(props.appId, b.id)
    else await releaseBatch(props.appId, b.id)
    ElMessage.success('操作成功')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  } finally {
    acting.value = ''
  }
}

// ---------- 新建变更（人工研发路径入口） ----------
const changeDlg = ref(false)
const savingChange = ref(false)
const changeForm = ref<{ title: string; type: 'feat' | 'hotfix'; branchMode: 'create' | 'existing'; branch: string; baseBranch: string }>({
  title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main',
})
function openChangeDlg() {
  changeForm.value = { title: '', type: 'feat', branchMode: 'create', branch: '', baseBranch: 'main' }
  changeDlg.value = true
}
async function saveChange() {
  const f = changeForm.value
  if (!f.title.trim()) { ElMessage.warning('请输入变更标题'); return }
  if (!f.branch.trim()) { ElMessage.warning('请输入分支名'); return }
  savingChange.value = true
  try {
    const created = await createChange(props.appId, {
      title: f.title.trim(), type: f.type, branch: f.branch.trim(),
      baseBranch: f.baseBranch.trim() || 'main', createBranch: f.branchMode === 'create',
    })
    ElMessage.success(`变更已创建（分支 ${created.branch}）——克隆推送代码，或在详情页委派智能研发`)
    changeDlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建变更失败'))
  } finally { savingChange.value = false }
}

// ---------- 新建批次（勾选 open 变更自动入批） ----------
const batchDlg = ref(false)
const savingBatch = ref(false)
const batchForm = ref<{ title: string; branch: string; changeIds: string[] }>({ title: '', branch: '', changeIds: [] })
// 可入批变更：open 且未在其他批次（batchId 空）。
const openChanges = ref<Change[]>([])
async function openBatchDlg() {
  batchForm.value = { title: '', branch: `integration/${new Date().toISOString().slice(0, 10).replaceAll('-', '')}`, changeIds: [] }
  try {
    openChanges.value = (await listChanges(props.appId, 'open')).filter((c) => !c.batchId)
  } catch { openChanges.value = [] }
  batchDlg.value = true
}
async function saveBatch() {
  const f = batchForm.value
  if (!f.title.trim()) { ElMessage.warning('请输入批次标题'); return }
  if (!f.branch.trim()) { ElMessage.warning('请输入集成分支名'); return }
  savingBatch.value = true
  try {
    const b = await createBatch(props.appId, { title: f.title.trim(), branch: f.branch.trim() })
    for (const cid of f.changeIds) {
      await addChangeToBatch(props.appId, b.id, cid)
    }
    ElMessage.success(f.changeIds.length ? `批次已创建并入批 ${f.changeIds.length} 个变更` : '批次已创建（可稍后在批次详情入批）')
    batchDlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建批次失败'))
  } finally { savingBatch.value = false }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">变更与集成批次</span>
      <span class="card-sub">代码分支 → 集成验证 → 审批发布</span>
      <span class="head-acts">
        <button class="act" @click="openBatchDlg">新建批次</button>
        <button class="act primary" @click="openChangeDlg">新建变更</button>
      </span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <template v-else>
      <div v-if="!changes.length && !batches.length" class="empty-hint">
        暂无变更——点击「新建变更」发起人工研发（克隆推送代码），或从「智能研发」由 AI 在变更分支上工作
      </div>
      <template v-if="changes.length">
        <div class="sec-title">变更（{{ changes.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>变更</th><th>分支</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="c in changes" :key="c.id" :id="'entity-' + c.id" tabindex="-1" :class="{ 'focus-row': route.query.focus === c.id }">
              <td class="t-title"><RouterLink :to="'/changes/' + c.id">{{ c.title || c.id }}</RouterLink></td>
              <td><span class="mono dim">{{ c.branch }}</span></td>
              <td>
                <span class="status" :class="tagTypeToCls(statusOf(CHANGE_STATUS, c.status).type)">
                  {{ statusOf(CHANGE_STATUS, c.status).label }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
      <template v-if="batches.length">
        <div class="sec-title">集成批次（{{ batches.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>批次</th><th>状态</th><th class="col-act"></th></tr></thead>
          <tbody>
            <tr v-for="b in batches" :key="b.id" :id="'entity-' + b.id" tabindex="-1" :class="{ 'focus-row': route.query.focus === b.id }">
              <td class="t-title"><RouterLink :to="'/batches/' + b.id">{{ b.title || b.id }}</RouterLink></td>
              <td>
                <span class="status" :class="tagTypeToCls(statusOf(BATCH_STATUS, b.status).type)">
                  {{ statusOf(BATCH_STATUS, b.status).label }}
                </span>
              </td>
              <td class="col-act">
                <button
                  v-if="b.status === 'tested'" class="act primary"
                  :disabled="acting === `approve:${b.id}`" @click="act('approve', b)"
                >
审批发车
                </button>
                <button
                  v-else-if="b.status === 'collecting'" class="act"
                  :disabled="acting === `integrate:${b.id}`" @click="act('integrate', b)"
                >
集成
                </button>
                <button
                  v-else-if="b.status === 'releasing'" class="act" disabled
                >
发布中
                </button>
              </td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
    </template>

    <!-- 新建变更对话框 -->
    <el-dialog v-model="changeDlg" title="新建变更" width="520px">
      <el-form label-width="92px">
        <el-form-item label="标题"><el-input v-model="changeForm.title" placeholder="如：导出用户列表" /></el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="changeForm.type">
            <el-radio value="feat">功能</el-radio>
            <el-radio value="hotfix">修复</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="分支">
          <el-radio-group v-model="changeForm.branchMode">
            <el-radio value="create">平台代建</el-radio>
            <el-radio value="existing">关联已有</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="changeForm.branchMode === 'create' ? '新分支名' : '已有分支'">
          <el-input v-model="changeForm.branch" :placeholder="changeForm.branchMode === 'create' ? 'feat/user-export' : 'feature/login-v2'" />
        </el-form-item>
        <el-form-item v-if="changeForm.branchMode === 'create'" label="基于分支">
          <el-input v-model="changeForm.baseBranch" placeholder="main" />
        </el-form-item>
      </el-form>
      <p class="dlg-hint">创建后：本地克隆该分支推送代码（详情页有克隆命令），或在变更详情点「智能研发」委派 AI 在分支上工作；完成后经批次集成发车。</p>
      <template #footer>
        <el-button @click="changeDlg = false">取消</el-button>
        <el-button type="primary" :loading="savingChange" @click="saveChange">创建</el-button>
      </template>
    </el-dialog>

    <!-- 新建批次对话框（勾选 open 变更自动入批） -->
    <el-dialog v-model="batchDlg" title="新建集成批次" width="520px">
      <el-form label-width="92px">
        <el-form-item label="标题"><el-input v-model="batchForm.title" placeholder="如：8 月下旬发车" /></el-form-item>
        <el-form-item label="集成分支"><el-input v-model="batchForm.branch" placeholder="integration/20260815-1" /></el-form-item>
        <el-form-item label="入批变更">
          <template v-if="openChanges.length">
            <el-checkbox-group v-model="batchForm.changeIds">
              <el-checkbox v-for="c in openChanges" :key="c.id" :value="c.id">{{ c.title }}（{{ c.branch }}）</el-checkbox>
            </el-checkbox-group>
          </template>
          <span v-else class="dlg-hint">暂无可入批的 open 变更（可创建后再从批次详情入批）</span>
        </el-form-item>
      </el-form>
      <p class="dlg-hint">集成分支即联调泳道名——批次 CI 在该泳道验证，冲突或测试不通过不会发车。</p>
      <template #footer>
        <el-button @click="batchDlg = false">取消</el-button>
        <el-button type="primary" :loading="savingBatch" @click="saveBatch">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.col-act { text-align: right; }
.head-acts { display: flex; gap: 8px; }
.dlg-hint { font-size: 12px; color: var(--text-faint); margin: 4px 0 0; }
</style>
