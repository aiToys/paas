<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
// DevOps 中心：值班台 + 档案室。
// 默认 tab=值班台（聚合「需要我关注」的进行中/失败/待审批三列，通知驱动，点击直达详情）——
// 打开就知道该干什么。其余六 tab 是档案室（排障视角的全量单据）：运行/变更/批次/构建/镜像/发布，
// 每行可进独立详情页（/devops/{runs|changes|batches|builds|releases}/:id），详情间有链路串联。
// 发布回滚走 useDangerConfirm（生产按目标 env.type 显式 isProd，覆盖顶栏 scope）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { usePolling } from '@/composables/usePolling'
import { listRuns, type PipelineRun } from '@/api/pipeline'
import { listAllChanges, listAllBatches, listNotifications, type Change, type IntegrationBatch, type Notification } from '@/api/change'
import { listGlobalPulls, type GlobalPull } from '@/api/pulls'
import { imageLink, repoLink } from '@/composables/useDevopsLinks'
import {
  BUILD_STATUS, RELEASE_STATUS, RUN_STATUS, CHANGE_STATUS, BATCH_STATUS, statusOf,
} from '@/composables/useStatus'

const buildStatus = (s: string) => statusOf(BUILD_STATUS, s)
const releaseStatus = (s: string) => statusOf(RELEASE_STATUS, s)
const runStatus = (s: string) => statusOf(RUN_STATUS, s)
const changeStatus = (s: string) => statusOf(CHANGE_STATUS, s)
const batchStatus = (s: string) => statusOf(BATCH_STATUS, s)

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
const route = useRoute()
const envStore = useEnvStore()
// ?tab= 深链支持（详情页跳转定位，如构建详情→镜像库）+ 切换写回 URL（刷新/分享保持 tab）
const tab = ref((route.query.tab as string) || 'board')
watch(tab, (t) => {
  if ((route.query.tab as string || 'board') !== t) {
    router.replace({ query: { ...route.query, tab: t === 'board' ? undefined : t } })
  }
})
const builds = ref<BuildRun[]>([])
// 镜像库实时视图：registry v2 catalog（PAAS_REGISTRY 配置的镜像仓库），展开行按需加载 tag + digest
const registryRepos = ref<{ name: string }[]>([])
const expandedTags = ref<Record<string, { tag: string; digest: string }[]>>({})
const releases = ref<Release[]>([])
const recentRuns = ref<PipelineRun[]>([])
const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const notifications = ref<Notification[]>([])
const globalPulls = ref<GlobalPull[]>([])
const loading = ref(false)
const busy = ref(false) // 重新构建/回滚 进行中（防重复点击）

// 应用名映射：onMounted 时一次拉取应用列表建 Map。
const appNames = ref<Record<string, string>>({})
const appName = (id: string) => appNames.value[id] ?? id
const envName = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

// ---------- 值班台（默认 tab）：通知驱动三列 ----------
const boardErrors = computed(() => notifications.value.filter((n) => n.severity === 'error'))
const boardWarnings = computed(() => notifications.value.filter((n) => n.severity === 'warning'))
const boardInfos = computed(() => notifications.value.filter((n) => n.severity === 'info'))
const boardTotal = computed(() => boardErrors.value.length + boardWarnings.value.length + boardInfos.value.length + boardPulls.value.length)

function notifGo(n: Notification) {
  if (n.targetType === 'run') router.push(`/devops/runs/${n.targetId}`)
  else if (n.targetType === 'batch') router.push(`/devops/batches/${n.targetId}`)
  else router.push(`/devops/changes/${n.targetId}`)
}

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
async function loadChanges() {
  try { changes.value = await listAllChanges() } catch { /* 非关键 */ }
}
async function loadBatches() {
  try { batches.value = await listAllBatches() } catch { /* 非关键 */ }
}
async function loadNotifications() {
  try { notifications.value = await listNotifications() } catch { /* 非关键 */ }
}
async function loadPulls() {
  try { globalPulls.value = await listGlobalPulls() } catch { /* 非关键 */ }
}
// 等评审：open PR（跨应用），进入值班台待处理视图
const boardPulls = computed(() => globalPulls.value)
// 当前阶段名（运行中显示 stageRuns[currentStage].name）
function currentStageName(r: PipelineRun): string {
  return r.stageRuns[r.currentStage]?.name ?? '-'
}

async function load() {
  loading.value = true
  try {
    await Promise.all([
      loadBuilds(), loadRegistry(), loadReleases(), loadAppNames(), loadRuns(),
      loadChanges(), loadBatches(), loadNotifications(), loadPulls(),
    ])
  } finally {
    loading.value = false
  }
}

// 构建状态轮询（5s）+ 发布/运行/批次/通知轮询（10s，值班台与档案室共用）。
// 页面不可见自动暂停（usePolling 统一治理，防后台 tab 请求风暴）。
usePolling(loadBuilds, 5000)
usePolling(() => {
  loadReleases()
  loadRuns()
  loadChanges()
  loadBatches()
  loadNotifications()
  loadPulls()
}, 10000)

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
    ElMessage.error(apiError(e, '触发失败'))
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
    ElMessage.error(apiError(e, '回滚失败'))
  }
}

function goApp(appId: string) {
  if (appId) router.push(`/applications/${appId}`)
}
function shortDigest(d: string) {
  return d && d.length > 19 ? d.slice(0, 19) + '…' : (d || '—')
}

// 环境列表一次性加载（此前嵌在轮询的 load() 里，环境为空/失败时每 10s 重发）
onMounted(async () => { await envStore.loadEnvs(); load() })
// 顶栏切环境联动（R7-2：build/release/run 均带 envId，切环境必须重拉）
watch(() => envStore.currentEnvId, () => load())
</script>

<template>
  <div class="devops-page">
    <div class="page-head">
      <div>
        <h2>DevOps 中心</h2>
        <p class="sub">值班台 + 档案室：待办一览 · 运行 · 变更 · 批次 · 构建 · 镜像 · 发布</p>
      </div>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-tabs v-model="tab" class="devops-tabs">
      <!-- 值班台（默认）：三列「需要我关注」，点击直达详情——打开就知道该干什么 -->
      <el-tab-pane name="board">
        <template #label>
          值班台
          <el-badge v-if="boardErrors.length" :value="boardErrors.length" type="danger" class="board-badge" />
        </template>
        <p class="tab-hint">全租户需关注事件（10s 轮询）：🔴 失败待处理 · ⏸ 等审批 · 🏃 进行中。点击直达对应详情。</p>
        <div v-if="!boardTotal" class="board-empty">
          <el-empty description="一切正常，没有需要关注的事件 🎉" :image-size="80" />
        </div>
        <div v-else class="board">
          <div class="board-col err">
            <div class="col-head">🔴 失败待处理（{{ boardErrors.length }}）</div>
            <div v-for="n in boardErrors" :key="n.id" class="board-item" @click="notifGo(n)">
              <div class="item-title">{{ n.title }}</div>
              <div class="item-meta">{{ appName(n.appId) }}</div>
            </div>
          </div>
          <div class="board-col warn">
            <div class="col-head">⏸ 等待审批（{{ boardWarnings.length }}）</div>
            <div v-for="n in boardWarnings" :key="n.id" class="board-item" @click="notifGo(n)">
              <div class="item-title">{{ n.title }}</div>
              <div class="item-meta">{{ appName(n.appId) }}</div>
            </div>
          </div>
          <div class="board-col info">
            <div class="col-head">🏃 进行中（{{ boardInfos.length }}）</div>
            <div v-for="n in boardInfos" :key="n.id" class="board-item" @click="notifGo(n)">
              <div class="item-title">{{ n.title }}</div>
              <div class="item-meta">{{ appName(n.appId) }}</div>
            </div>
          </div>
          <div class="board-col pull">
            <div class="col-head">🔀 等评审（{{ boardPulls.length }}）</div>
            <div
v-for="p in boardPulls" :key="p.repoId + ':' + p.pr.number" class="board-item"
              @click="router.push(`/devops/pulls/${p.repoId}/${p.pr.number}?appId=${p.appId}`)"
>
              <div class="item-title">#{{ p.pr.number }} {{ p.pr.title }}</div>
              <div class="item-meta">{{ appName(p.appId) }} · {{ p.pr.head }} → {{ p.pr.base }} · {{ p.pr.user }}</div>
            </div>
          </div>
        </div>
      </el-tab-pane>

      <!-- 运行记录：跨应用最近流水线运行。点「查看详情」进独立运行详情页。 -->
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
              <el-tag :type="(runStatus(row.status).type)" size="small">
                {{ runStatus(row.status).label }}
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
            <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="110">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/runs/${row.id}`)">查看详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 变更（档案室）：跨应用变更列表，点行进收件箱详情页 -->
      <el-tab-pane label="变更" name="changes">
        <el-table :data="changes" size="small" empty-text="暂无变更">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="title" label="标题" min-width="160" show-overflow-tooltip />
          <el-table-column label="类型" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="row.type === 'hotfix' ? 'danger' : 'primary'">{{ row.type }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="分支" width="150">
            <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag :type="(changeStatus(row.status).type)" size="small">
                {{ changeStatus(row.status).label }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="创建时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/changes/${row.id}`)">详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 评审（档案室）：跨应用 open PR 聚合，点「查看」进 PR 详情（diff + 评审 + merge） -->
      <el-tab-pane label="评审" name="pulls">
        <p class="tab-hint">跨应用待评审 PR（内置仓库，10s 轮询）。点「查看」进 PR 详情看 diff 并评审/合并。</p>
        <el-table :data="globalPulls" size="small" empty-text="暂无待评审 PR">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="PR" width="70">
            <template #default="{ row }"><span class="mono">#{{ row.pr.number }}</span></template>
          </el-table-column>
          <el-table-column prop="pr.title" label="标题" min-width="180" show-overflow-tooltip />
          <el-table-column label="分支" width="170">
            <template #default="{ row }"><span class="mono">{{ row.pr.head }} → {{ row.pr.base }}</span></template>
          </el-table-column>
          <el-table-column prop="pr.user" label="作者" width="100" />
          <el-table-column label="创建时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.pr.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button
text type="primary" size="small"
                @click="router.push(`/devops/pulls/${row.repoId}/${row.pr.number}?appId=${row.appId}`)"
>
查看
</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 批次（档案室）：跨应用集成批次，点行进状态机详情页 -->
      <el-tab-pane label="批次" name="batches">
        <el-table :data="batches" size="small" empty-text="暂无批次">
          <el-table-column label="应用" width="140">
            <template #default="{ row }">
              <span class="mono clickable" @click="goApp(row.appId)">{{ appName(row.appId) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="title" label="标题" min-width="160" show-overflow-tooltip />
          <el-table-column label="集成分支" width="170">
            <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
          </el-table-column>
          <el-table-column label="变更数" width="80">
            <template #default="{ row }">{{ row.changeIds?.length ?? 0 }}</template>
          </el-table-column>
          <el-table-column label="状态" width="130">
            <template #default="{ row }">
              <el-tag :type="(batchStatus(row.status).type)" size="small">
                {{ batchStatus(row.status).label }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="创建时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/batches/${row.id}`)">详情</el-button>
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
              <el-tag :type="(buildStatus(row.status).type)" size="small">
                {{ buildStatus(row.status).label }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="分支" width="120">
            <template #default="{ row }"><span class="mono">{{ row.branch }}</span></template>
          </el-table-column>
          <el-table-column label="Commit" width="110">
            <template #default="{ row }">
              <a class="mono clickable" @click="router.push(repoLink(row.appId))">{{ row.commit?.slice(0, 8) }}</a>
            </template>
          </el-table-column>
          <el-table-column prop="message" label="说明" min-width="180" show-overflow-tooltip />
          <el-table-column label="开始时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.startedAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="150">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/builds/${row.id}`)">详情</el-button>
              <el-button
                size="small" plain :disabled="busy || row.status === 'pending' || row.status === 'running'"
                @click="rebuild(row)"
              >
重新构建
</el-button>
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
            <template #default="{ row }">
              <a class="mono clickable" :title="row.imageDigest" @click="router.push(imageLink(row.appId, row.imageId))">{{ shortDigest(row.imageDigest) }}</a>
            </template>
          </el-table-column>
          <el-table-column label="策略" width="100">
            <template #default="{ row }">{{ row.strategy }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag :type="(releaseStatus(row.status).type)" size="small">
                {{ releaseStatus(row.status).label }}
              </el-tag>
              <el-tag v-if="row.isRollback" type="warning" size="small" style="margin-left:4px">回滚</el-tag>
              <el-tag v-if="row.promotedFrom" type="primary" size="small" style="margin-left:4px">提升</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="发布时间" width="170">
            <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="150">
            <template #default="{ row }">
              <el-button text type="primary" size="small" @click="router.push(`/devops/releases/${row.id}`)">详情</el-button>
              <el-button
                v-if="row.status === 'succeeded' && row.previousImageId"
                text type="warning" size="small" :disabled="busy"
                @click="rollback(row)"
              >
回滚
</el-button>
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

/* 值班台三列 */
.board { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; }
.board-col { border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 10px; min-height: 120px; background: var(--el-bg-color); }
.board-col.err { border-top: 3px solid var(--el-color-danger); }
.board-col.warn { border-top: 3px solid var(--el-color-warning); }
.board-col.info { border-top: 3px solid var(--el-color-primary); }
.board-col.pull { border-top: 3px solid var(--el-color-success); }
.col-head { font-size: 13px; font-weight: 600; margin-bottom: 8px; color: var(--el-text-color-secondary); }
.board-item { padding: 8px 10px; border-radius: 6px; cursor: pointer; margin-bottom: 6px; background: var(--el-fill-color-lighter); }
.board-item:hover { background: var(--el-fill-color); }
.item-title { font-size: 13px; line-height: 1.4; }
.item-meta { font-size: 12px; color: var(--el-text-color-placeholder); margin-top: 2px; }
.board-empty { padding: 20px 0; }
.board-badge { margin-left: 4px; transform: translateY(-6px); }

.tag-list { padding: 4px 12px; display: flex; flex-direction: column; gap: 6px; }
.tag-row { display: flex; align-items: center; gap: 12px; font-size: 12.5px; }
.tag-name { min-width: 120px; color: var(--text); }
.digest { word-break: break-all; }
.faint { color: var(--text-faint); }
.mono { font-family: var(--mono, ui-monospace, monospace); }
</style>
