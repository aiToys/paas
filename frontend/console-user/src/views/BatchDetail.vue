<template>
  <div class="page detail-page">
    <header class="crumb">
      <button class="back" @click="goBack">←</button>
      <span>集成批次</span>
      <span class="sep">/</span>
      <span class="mono">{{ batch?.id }}</span>
      <el-tag v-if="batch" :type="statusType(batch.status)" size="small">{{ statusLabel(batch.status) }}</el-tag>
    </header>

    <div v-if="batch" class="body">
      <!-- 状态机 -->
      <section class="card">
        <el-steps :active="stepActive" align-center finish-status="success">
          <el-step title="收集" />
          <el-step title="集成测试" />
          <el-step title="待审批" />
          <el-step title="发布" />
        </el-steps>
        <div class="row">
          <div class="kv"><span>标题</span><b>{{ batch.title }}</b></div>
          <div class="kv"><span>集成分支</span><code class="mono">{{ batch.branch }}</code></div>
          <div class="kv"><span>应用</span>
            <a class="link" @click="router.push(`/applications/${batch.appId}`)">{{ batch.appId }}</a>
          </div>
          <div v-if="batch.runId" class="kv"><span>关联运行</span>
            <a class="link" @click="router.push(`/devops/runs/${batch.runId}`)">{{ batch.runId }}</a>
          </div>
        </div>
        <!-- 操作：按状态给下一步 -->
        <div class="actions">
          <el-button v-if="canIntegrate" type="primary" size="small" @click="doAction('integrate')">开始集成</el-button>
          <el-button v-if="batch.status === 'tested'" type="primary" size="small" @click="doAction('approve')">审批发布</el-button>
          <el-button v-if="batch.status === 'releasing'" type="primary" size="small" @click="doAction('release')">执行发布</el-button>
          <el-button v-if="canAbandon" type="danger" plain size="small" @click="doAbandon">放弃批次</el-button>
        </div>
      </section>

      <!-- 批内变更 -->
      <section class="card">
        <h3>批内变更（{{ changes.length }}）</h3>
        <el-table :data="changes" size="small" empty-text="空批次">
          <el-table-column prop="title" label="标题" min-width="160" />
          <el-table-column prop="branch" label="分支" min-width="140">
            <template #default="{ row }"><code class="mono">{{ row.branch }}</code></template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="100">
            <template #default="{ row }"><el-tag size="small">{{ row.status }}</el-tag></template>
          </el-table-column>
          <el-table-column label="冲突" width="140">
            <template #default="{ row }">
              <code v-if="row.conflictWith" class="mono danger">{{ row.conflictWith }}</code>
              <span v-else class="dim">—</span>
            </template>
          </el-table-column>
          <el-table-column label="" width="90">
            <template #default="{ row }">
              <el-button size="small" text type="primary" @click="router.push(`/devops/changes/${row.id}`)">详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </section>

      <!-- 发布记录 -->
      <section v-if="batch.releaseIds?.length" class="card">
        <h3>发布记录</h3>
        <div v-for="rid in batch.releaseIds" :key="rid" class="kv">
          <a class="link mono" @click="router.push(`/devops/releases/${rid}`)">{{ rid }}</a>
        </div>
      </section>
    </div>
    <el-skeleton v-else :rows="5" animated />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getBatch, listAllBatches, listAllChanges, integrateBatch, approveBatch, releaseBatch, abandonBatch, type IntegrationBatch, type Change } from '@/api/change'
import { usePolling } from '@/composables/usePolling'

const route = useRoute()
const router = useRouter()
const batch = ref<IntegrationBatch>()
const changes = ref<Change[]>([])

function goBack() {
  if (history.length > 1) history.back()
  else router.push('/devops')
}

const statusMap: Record<string, [string, string]> = {
  collecting: ['收集中', 'info'], conflict: ['集成冲突', 'danger'], testing: ['测试中', 'warning'],
  tested: ['测试通过·待审批', 'warning'], releasing: ['发布中', 'warning'], released: ['已发布', 'success'],
  failed: ['失败', 'danger'], abandoned: ['已放弃', 'info'],
}
const statusType = (s: string) => (statusMap[s] ?? [s, 'info'])[1]
const statusLabel = (s: string) => (statusMap[s] ?? [s, 'info'])[0]

const stepActive = computed(() => {
  const s = batch.value?.status
  if (s === 'testing' || s === 'conflict') return 1
  if (s === 'tested') return 2
  if (s === 'releasing') return 3
  if (s === 'released') return 4
  return 0
})
const canIntegrate = computed(() => ['collecting', 'conflict', 'failed'].includes(batch.value?.status ?? '') && changes.value.length > 0)
const canAbandon = computed(() => ['collecting', 'conflict', 'failed'].includes(batch.value?.status ?? ''))

async function load(silent = false) {
  try {
    const id = route.params.id as string
    const all = await listAllBatches()
    const b = all.find((x) => x.id === id)
    if (!b) {
      if (!silent) ElMessage.error('批次不存在')
      return
    }
    batch.value = await getBatch(b.appId, b.id)
    // 批内变更（批次详情触发后端惰性状态推进，回读最新）
    const cs = await listAllChanges(b.appId)
    changes.value = cs.filter((c) => batch.value!.changeIds.includes(c.id))
  } catch (e: any) {
    if (!silent) ElMessage.error(e?.message || '加载失败')
  }
}

async function doAction(kind: 'integrate' | 'approve' | 'release') {
  if (!batch.value) return
  const b = batch.value
  const confirmText: Record<string, string> = {
    integrate: `集成批次「${b.title}」？将重建集成分支并按序合并 ${b.changeIds.length} 个变更，然后触发 CI。`,
    approve: `审批通过批次「${b.title}」？审批后即可执行发布（合并到 main + 触发 CD）。`,
    release: `执行发布批次「${b.title}」？将把 ${b.changeIds.length} 个变更合并到 main 并触发 CD 上线。`,
  }
  try {
    await ElMessageBox.confirm(confirmText[kind], '操作确认', { type: kind === 'release' ? 'warning' : 'info' })
    const fn = { integrate: integrateBatch, approve: approveBatch, release: releaseBatch }[kind]
    await fn(b.appId, b.id)
    ElMessage.success('操作成功')
    await load(true)
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

async function doAbandon() {
  if (!batch.value) return
  try {
    await ElMessageBox.confirm(`放弃批次「${batch.value.title}」？批内变更将回退为 open 可重新入批。`, '放弃确认', { type: 'warning' })
    await abandonBatch(batch.value.appId, batch.value.id)
    ElMessage.success('已放弃')
    goBack()
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

onMounted(load)
// testing/releasing 进行中 10s 轮询（GET 详情触发后端惰性推进；页面不可见自动暂停）
usePolling(() => {
  if (['testing', 'releasing'].includes(batch.value?.status ?? '')) load(true)
}, 10000)
</script>

<style scoped>
.detail-page { padding: 20px; max-width: 960px; margin: 0 auto; }
.crumb { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.crumb .sep { color: var(--el-text-color-placeholder); }
.back { border: none; background: none; cursor: pointer; font-size: 16px; color: var(--el-text-color-primary); }
.body { display: grid; gap: 14px; }
.card { background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 16px 20px; }
.card h3 { margin: 0 0 12px; font-size: 14px; color: var(--el-text-color-secondary); }
.row { margin-top: 12px; }
.kv { display: flex; align-items: center; gap: 8px; margin: 6px 0; font-size: 13px; }
.kv > span:first-child { color: var(--el-text-color-secondary); min-width: 64px; }
.actions { margin-top: 12px; display: flex; gap: 8px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.dim { color: var(--el-text-color-placeholder); }
.danger { color: var(--el-color-danger); }
.link { color: var(--el-color-primary); cursor: pointer; }
</style>
