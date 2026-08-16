<template>
  <div class="page detail-page">
    <!-- 面包屑身份条 -->
    <header class="crumb">
      <button class="back" @click="goBack">←</button>
      <span>变更</span>
      <span class="sep">/</span>
      <span class="mono">{{ change?.id }}</span>
      <el-tag v-if="change" :type="statusType(change.status)" size="small">{{ statusLabel(change.status) }}</el-tag>
      <span v-if="change" class="grow"></span>
      <el-button v-if="change?.status === 'open' && !change.batchId" size="small" type="danger" plain @click="abandon">放弃变更</el-button>
    </header>

    <div v-if="change" class="inbox">
      <!-- ① 我的代码 -->
      <section class="card">
        <h3>① 我的代码</h3>
        <div class="kv"><span>标题</span><b>{{ change.title }}</b></div>
        <div class="kv"><span>类型</span><el-tag size="small" :type="change.type === 'hotfix' ? 'danger' : 'primary'">{{ change.type }}</el-tag></div>
        <div class="kv"><span>分支</span><code class="mono">{{ change.branch }}</code><span class="dim">（基于 {{ change.baseBranch }}{{ change.branchCreated ? '，平台代建' : '' }}）</span></div>
        <div class="kv"><span>克隆</span><code class="mono clone">{{ cloneCmd }}</code>
          <el-button size="small" text @click="copyClone">复制</el-button></div>
        <div v-if="commits.length" class="commits">
          <div class="sub">最近提交</div>
          <div v-for="c in commits" :key="c.sha" class="commit">
            <code class="mono sha">{{ c.sha.slice(0, 8) }}</code>
            <span class="msg">{{ c.message }}</span>
          </div>
        </div>
      </section>

      <!-- ② 集成批次 -->
      <section class="card">
        <h3>② 集成批次</h3>
        <template v-if="batch">
          <div class="kv"><span>批次</span>
            <a class="link" @click="router.push(`/devops/batches/${batch.id}`)">{{ batch.title }}</a>
            <el-tag size="small" :type="batchStatusType(batch.status)">{{ batchStatusLabel(batch.status) }}</el-tag>
          </div>
          <div class="kv"><span>集成分支</span><code class="mono">{{ batch.branch }}</code></div>
          <div v-if="batch.runId" class="kv"><span>关联运行</span>
            <a class="link" @click="router.push(`/devops/runs/${batch.runId}`)">查看运行 →</a>
          </div>
          <div v-if="change.conflictWith" class="conflict">⚠️ 与变更 <code class="mono">{{ change.conflictWith }}</code> 冲突，解决冲突后重新集成</div>
        </template>
        <div v-else class="dim">未入批——进入应用「变更」tab 创建批次并入批</div>
      </section>

      <!-- ③ 测试验证 / ④ 发布状态 -->
      <section class="card">
        <h3>③ 测试验证 → ④ 发布状态</h3>
        <el-steps v-if="batch" :active="stepActive" align-center finish-status="success" class="steps">
          <el-step title="集成" :description="batchStatusLabel(batch.status)" />
          <el-step title="测试" :description="batch.status === 'tested' || batch.status === 'releasing' || batch.status === 'released' ? '通过' : '—'" />
          <el-step title="审批" :description="batch.status === 'releasing' || batch.status === 'released' ? '已批准' : '待审批'" />
          <el-step title="发布" :description="batch.status === 'released' ? '已上线' : '—'" />
        </el-steps>
        <div v-if="batch?.releaseIds?.length" class="kv"><span>发布记录</span>
          <a v-for="rid in batch.releaseIds" :key="rid" class="link mono" @click="router.push(`/devops/releases/${rid}`)">{{ rid }}</a>
        </div>
        <div v-if="!batch" class="dim">入批集成后在此展示测试与发布进度</div>
      </section>

      <!-- ⑤ 时间线 -->
      <section class="card">
        <h3>⑤ 时间线</h3>
        <el-timeline>
          <el-timeline-item :timestamp="change.createdAt" type="primary">创建变更（{{ change.type }}）</el-timeline-item>
          <el-timeline-item v-if="change.status !== 'open'" :timestamp="change.updatedAt" type="success">入批集成</el-timeline-item>
          <el-timeline-item v-if="['tested', 'released'].includes(change.status)" :timestamp="change.updatedAt" type="success">批次测试通过</el-timeline-item>
          <el-timeline-item v-if="change.status === 'released'" :timestamp="change.updatedAt" type="success">随批次发布上线</el-timeline-item>
          <el-timeline-item v-if="change.status === 'abandoned'" :timestamp="change.updatedAt" type="info">变更被放弃</el-timeline-item>
        </el-timeline>
      </section>
    </div>

    <el-skeleton v-else :rows="6" animated />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getChange, abandonChange, getBatch, listAllBatches, listAllChanges, type Change, type IntegrationBatch } from '@/api/change'
import { fetchAuth } from '@/api'

const route = useRoute()
const router = useRouter()
const change = ref<Change>()
const batch = ref<IntegrationBatch>()
const commits = ref<{ sha: string; message: string }[]>([])

const cloneCmd = computed(() => `git clone -b ${change.value?.branch ?? ''} <仓库地址>`)

function goBack() {
  if (history.length > 1) history.back()
  else router.push('/devops')
}

const statusMap: Record<string, [string, string]> = {
  open: ['进行中', 'primary'], integrated: ['已集成', 'success'], tested: ['测试通过', 'success'],
  released: ['已发布', 'success'], reverted: ['已回退', 'warning'], abandoned: ['已放弃', 'info'],
}
const statusType = (s: string) => (statusMap[s] ?? [s, 'info'])[1]
const statusLabel = (s: string) => (statusMap[s] ?? [s, 'info'])[0]

const batchMap: Record<string, [string, string]> = {
  collecting: ['收集中', 'info'], conflict: ['集成冲突', 'danger'], testing: ['测试中', 'warning'],
  tested: ['测试通过·待审批', 'warning'], releasing: ['发布中', 'warning'], released: ['已发布', 'success'],
  failed: ['失败', 'danger'], abandoned: ['已放弃', 'info'],
}
const batchStatusType = (s: string) => (batchMap[s] ?? [s, 'info'])[1]
const batchStatusLabel = (s: string) => (batchMap[s] ?? [s, 'info'])[0]

// steps 活跃序：collecting/conflict=0，testing=1，tested=2，releasing/released=4
const stepActive = computed(() => {
  const s = batch.value?.status
  if (!s) return 0
  if (s === 'testing') return 1
  if (s === 'tested') return 2
  if (s === 'releasing') return 3
  if (s === 'released') return 4
  return 0
})

function copyClone() {
  navigator.clipboard?.writeText(cloneCmd.value)
  ElMessage.success('已复制')
}

async function abandon() {
  if (!change.value) return
  try {
    await ElMessageBox.confirm(`放弃变更「${change.value.title}」？分支保留，可重新引用。`, '放弃确认', { type: 'warning' })
    await abandonChange(change.value.appId, change.value.id)
    ElMessage.success('已放弃')
    router.push(`/applications/${change.value.appId}`)
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

async function load() {
  const id = route.params.id as string
  // 变更详情需要 appId 前缀；先跨应用列表定位归属应用
  const all = await listAllChanges()
  const c = all.find((x) => x.id === id)
  if (!c) {
    ElMessage.error('变更不存在')
    return
  }
  change.value = await getChange(c.appId, id)
  // 批次：经跨应用批次列表定位（change.batchId 已知）
  if (change.value.batchId) {
    const bs = await listAllBatches(change.value.appId)
    const b = bs.find((x) => x.id === change.value!.batchId)
    if (b) batch.value = await getBatch(b.appId, b.id)
  }
  // 最近 commits：应用内置仓库浏览端点（repoId 非内置时静默跳过）
  try {
    const resp = await fetchAuth(
      `/api/applications/${change.value.appId}/repositories/${change.value.repoId}/commits?limit=5&branch=${encodeURIComponent(change.value.branch)}`,
    )
    if (resp.ok) {
      const j = await resp.json()
      commits.value = j?.data ?? []
    }
  } catch { /* 非关键 */ }
}

onMounted(load)
</script>

<style scoped>
.detail-page { padding: 20px; max-width: 960px; margin: 0 auto; }
.crumb { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.crumb .sep { color: var(--el-text-color-placeholder); }
.back { border: none; background: none; cursor: pointer; font-size: 16px; color: var(--el-text-color-primary); }
.grow { flex: 1; }
.inbox { display: grid; gap: 14px; }
.card { background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 16px 20px; }
.card h3 { margin: 0 0 12px; font-size: 14px; color: var(--el-text-color-secondary); }
.kv { display: flex; align-items: center; gap: 8px; margin: 6px 0; font-size: 13px; }
.kv > span:first-child { color: var(--el-text-color-secondary); min-width: 64px; }
.clone { background: var(--el-fill-color-light); padding: 2px 8px; border-radius: 4px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.dim { color: var(--el-text-color-placeholder); font-size: 13px; }
.link { color: var(--el-color-primary); cursor: pointer; }
.commits { margin-top: 10px; border-top: 1px dashed var(--el-border-color-lighter); padding-top: 10px; }
.sub { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 6px; }
.commit { display: flex; gap: 10px; font-size: 13px; padding: 2px 0; }
.sha { color: var(--el-color-primary); }
.conflict { margin-top: 8px; padding: 8px 12px; background: var(--el-color-danger-light-9); border-radius: 6px; font-size: 13px; color: var(--el-color-danger); }
.steps { margin: 8px 0; }
</style>
