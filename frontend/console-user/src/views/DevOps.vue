<script setup lang="ts">
// DevOps 中心：跨应用 CI/CD 指挥台。
// 默认 tab=运行记录（流水线引擎：build/deploy/test/approve/promote/baseline），点行进独立运行详情页
// （GitHub Actions 式全节点日志）。另含：构建 / 镜像库 / 发布（回滚）。
// 发布回滚走 useDangerConfirm（生产按目标 env.type 显式 isProd，覆盖顶栏 scope）。
// 环境逐级提升（promote）归流水线引擎 promote stage（运行记录 tab），本页不再提供旧的手动提升矩阵。
import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { listRuns, type PipelineRun } from '@/api/pipeline'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface BuildRun {
  id: string; appId: string; repoId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string
}
interface Release {
  id: string; appId: string; envId: string; imageId: string; imageDigest: string
  strategy: string; status: string; isRollback: boolean; promotedFrom?: string
  previousImageId?: string; createdAt: string; createdBy: string
}

const router = useRouter()
const envStore = useEnvStore()
const tab = ref('runs')
const builds = ref<BuildRun[]>([])
// 镜像库实时视图：registry v2 catalog（PAAS_REGISTRY 配置的镜像仓库），展开行按需加载 tag + digest
const registryRepos = ref<{ name: string }[]>([])
const expandedTags = ref<Record<string, { tag: string; digest: string }[]>>({})
const releases = ref<Release[]>([])
const recentRuns = ref<PipelineRun[]>([])
const loading = ref(false)
const busy = ref(false) // 重新构建/提升 进行中（防重复点击）

const BUILD_STATUS: Record<string, { label: string; type: TagType }> = {
  pending: { label: '排队', type: 'info' },
  running: { label: '构建中', type: 'warning' },
  success: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'danger' },
}
const RELEASE_STATUS: Record<string, { label: string; type: TagType }> = {
  succeeded: { label: '已生效', type: 'success' },
  'rolled-back': { label: '已回滚', type: 'info' },
  deploying: { label: '部署中', type: 'warning' },
}
const RUN_STATUS: Record<string, { label: string; type: TagType }> = {
  running: { label: '运行中', type: 'warning' },
  paused: { label: '等待审批', type: 'warning' },
  succeeded: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'danger' },
  aborted: { label: '已中止', type: 'info' },
}

// 应用名映射：onMounted 时一次拉取应用列表建 Map。
const appNames = ref<Record<string, string>>({})
const appName = (id: string) => appNames.value[id] ?? id
const envName = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

async function loadAppNames() {
  const resp = await fetchAuth('/api/applications')
  if (resp.ok) {
    const list = (await resp.json()).data ?? []
    appNames.value = Object.fromEntries(list.map((a: { id: string; name: string }) => [a.id, a.name]))
  }
}
async function loadBuilds() {
  const resp = await fetchAuth('/api/buildruns')
  if (resp.ok) builds.value = (await resp.json()).data ?? []
}
async function loadRegistry() {
  const resp = await fetchAuth('/api/registry/repositories')
  if (resp.ok) registryRepos.value = (await resp.json()).data ?? []
}
async function loadTags(repo: string) {
  const resp = await fetchAuth(`/api/registry/tags?repository=${encodeURIComponent(repo)}`)
  if (resp.ok) expandedTags.value[repo] = (await resp.json()).data ?? []
}
async function onExpand(row: { name: string }, expanded: readonly { name: string }[]) {
  const isOpen = expanded.some((r) => r.name === row.name)
  if (isOpen && !expandedTags.value[row.name]) await loadTags(row.name)
}
async function loadReleases() {
  const resp = await fetchAuth('/api/releases')
  if (resp.ok) releases.value = (await resp.json()).data ?? []
}
async function loadRuns() {
  try {
    const all = await listRuns()
    // 取最近 20 条（按 createdAt 倒序）
    recentRuns.value = [...all].sort((a, b) => +new Date(b.createdAt) - +new Date(a.createdAt)).slice(0, 20)
  } catch { /* 非关键 */ }
}
// 当前阶段名（运行中显示 stageRuns[currentStage].name）
function currentStageName(r: PipelineRun): string {
  return r.stageRuns[r.currentStage]?.name ?? '-'
}

async function load() {
  loading.value = true
  try {
    if (!envStore.envs.length) await envStore.loadEnvs()
    await Promise.all([loadBuilds(), loadRegistry(), loadReleases(), loadAppNames(), loadRuns()])
  } finally {
    loading.value = false
  }
}

// 构建状态轮询（5s）+ 发布状态轮询（10s，流水线/发布/运行记录 tab 用）
let buildTimer: number | undefined
let releaseTimer: number | undefined
function startPoll() {
  buildTimer = window.setInterval(loadBuilds, 5000)
  releaseTimer = window.setInterval(() => {
    loadReleases()
    loadRuns()
  }, 10000)
}

// 重新构建（构建行操作：用原 repoId 触发新构建）。
async function rebuild(row: BuildRun) {
  if (!row.repoId) {
    ElMessage.warning('该构建记录无仓库信息，请在应用详情触发')
    return
  }
  busy.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${row.appId}/buildruns`, {
      method: 'POST',
      body: JSON.stringify({ repoId: row.repoId }),
    })
    if (resp.ok) { ElMessage.success('已触发重新构建'); loadBuilds() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '触发失败') }
  } catch (e) {
    ElMessage.error('触发失败：' + (e as Error).message)
  } finally {
    busy.value = false
  }
}

async function rollback(row: Release) {
  const env = envStore.envs.find((e) => e.id === row.envId)
  const isProdEnv = env?.type === 'prod'
  const ok = await confirmDangerous({
    action: '回滚发布', target: `${appName(row.appId)} @ ${envName(row.envId)}`,
    requireNameConfirm: isProdEnv,
    isProd: isProdEnv,
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/releases/${row.id}/rollback`, { method: 'POST' })
    if (resp.ok) { ElMessage.success('已回滚'); loadReleases() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '回滚失败') }
  } catch (e) {
    ElMessage.error('回滚失败：' + (e as Error).message)
  }
}

function goApp(appId: string) {
  if (appId) router.push(`/applications/${appId}`)
}
function shortDigest(d: string) {
  return d && d.length > 19 ? d.slice(0, 19) + '…' : (d || '—')
}

onMounted(() => { load(); startPoll() })
onUnmounted(() => {
  if (buildTimer) clearInterval(buildTimer)
  if (releaseTimer) clearInterval(releaseTimer)
})
</script>

<template>
  <div class="devops-page">
    <div class="page-head">
      <div>
        <h2>DevOps 中心</h2>
        <p class="sub">跨应用 CI/CD 指挥台：流水线运行 · 构建 · 镜像 · 发布 · 回滚</p>
      </div>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-tabs v-model="tab" class="devops-tabs">
      <!-- 运行记录（默认）：跨应用最近流水线运行（新 pipeline 引擎：build/deploy/test/approve/promote/baseline）。
           点「查看详情」进独立运行详情页（/devops/runs/:id，GitHub Actions 式全节点时间线 + stage 日志）。 -->
      <el-tab-pane label="运行记录" name="runs">
        <p class="tab-hint">最近流水线运行，点「查看详情」进运行详情页（全节点时间线 + 日志）。运行中/等待审批自动轮询（10s）。</p>
        <el-table :data="recentRuns" size="small" v-loading="loading" empty-text="暂无运行记录">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="(RUN_STATUS[row.status]?.type) || 'info'" size="small">
                {{ RUN_STATUS[row.status]?.label || row.status }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="当前阶段" min-width="120">
            <template #default="{ row }">{{ currentStageName(row) }}</template>
          </el-table-column>
          <el-table-column label="分支" width="110">
            <template #default="{ row }">
              <el-tag v-if="row.branch?.startsWith('integration/')" size="small" type="warning">集成</el-tag>
              <span class="mono">{{ row.branch }}</span>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="100">
            <template #default="{ row }">
              <span v-if="row.version" class="mono">{{ row.version }}</span>
              <span v-else class="faint">-</span>
            </template>
          </el-table-column>
          <el-table-column label="开始时间" width="170">
            <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="操作" width="110">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/runs/${row.id}`)">查看详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 构建 -->
      <el-tab-pane label="构建" name="builds">
        <el-table :data="builds" size="small" v-loading="loading" empty-text="暂无构建">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="(BUILD_STATUS[row.status]?.type) || 'info'" size="small">
                {{ BUILD_STATUS[row.status]?.label || row.status }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="分支" width="120">
            <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
          </el-table-column>
          <el-table-column label="Commit" width="110">
            <template #default="{ row }"><span class="mono">{{ row.commit?.slice(0, 8) }}</span></template>
          </el-table-column>
          <el-table-column prop="message" label="说明" min-width="180" show-overflow-tooltip />
          <el-table-column label="开始时间" width="170">
            <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button
                size="small" plain :disabled="busy || row.status === 'pending' || row.status === 'running'"
                @click="rebuild(row)"
              >重新构建</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 镜像库（registry v2 实时视图） -->
      <el-tab-pane label="镜像库" name="images">
        <p class="tab-hint">镜像仓库实时列表（地址取自平台 PAAS_REGISTRY 配置），展开行查看 tag 与 digest</p>
        <el-table :data="registryRepos" size="small" empty-text="镜像库为空或未启用" @expand-change="onExpand">
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="tag-list">
                <div v-for="t in expandedTags[row.name] || []" :key="t.tag" class="tag-row">
                  <span class="mono tag-name">{{ t.tag }}</span>
                  <span class="mono faint digest">{{ t.digest }}</span>
                </div>
                <span v-if="row.name && !expandedTags[row.name]" class="faint">加载中…</span>
                <span v-else-if="!expandedTags[row.name]?.length" class="faint">无 tag</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="镜像仓库" min-width="300">
            <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 发布 -->
      <el-tab-pane label="发布" name="releases">
        <el-table :data="releases" size="small" empty-text="暂无发布记录">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="环境" width="130">
            <template #default="{ row }">{{ envName(row.envId) }}</template>
          </el-table-column>
          <el-table-column label="镜像 Digest" min-width="180">
            <template #default="{ row }"><span class="mono">{{ shortDigest(row.imageDigest) }}</span></template>
          </el-table-column>
          <el-table-column label="策略" width="100">
            <template #default="{ row }">{{ row.strategy }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag :type="(RELEASE_STATUS[row.status]?.type) || 'info'" size="small">
                {{ RELEASE_STATUS[row.status]?.label || row.status }}
              </el-tag>
              <el-tag v-if="row.isRollback" type="warning" size="small" style="margin-left:4px">回滚</el-tag>
              <el-tag v-if="row.promotedFrom" type="primary" size="small" style="margin-left:4px">提升</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="发布时间" width="170">
            <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="操作" width="120">
            <template #default="{ row }">
              <el-button
                v-if="row.status === 'succeeded' && row.previousImageId"
                text type="warning" size="small" :disabled="busy"
                @click="rollback(row)"
              >回滚</el-button>
              <span v-else-if="row.status !== 'succeeded'" class="text-faint">—</span>
              <span v-else class="text-faint">最新</span>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.devops-page { max-width: 1200px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.devops-tabs { margin-top: 4px; }
.text-faint { color: var(--text-faint); }
.clickable { cursor: pointer; color: var(--brand); }
.clickable:hover { text-decoration: underline; }
.tab-hint { font-size: 12.5px; color: var(--text-dim); margin: 0 0 10px; }
.env-prod { color: var(--danger); font-weight: 600; }
.empty-hint { padding: 32px; text-align: center; color: var(--text-faint); font-size: 13px; }

.tag-list { padding: 4px 12px; display: flex; flex-direction: column; gap: 6px; }
.tag-row { display: flex; align-items: center; gap: 12px; font-size: 12.5px; }
.tag-name { min-width: 120px; color: var(--text); }
.digest { word-break: break-all; }
.faint { color: var(--text-faint); }
.mono { font-family: var(--mono, ui-monospace, monospace); }
</style>
