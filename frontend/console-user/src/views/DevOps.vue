<script setup lang="ts">
// DevOps 中心：跨应用 CI/CD 总览（构建 / 镜像 / 发布），消除侧栏 DevOps「即将」。
// 复用既有 devops 后端跨应用列表端点；发布回滚走 useDangerConfirm（生产输入名称）。
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface BuildRun {
  id: string; appId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string
}
interface Release {
  id: string; appId: string; envId: string; imageDigest: string
  strategy: string; status: string; isRollback: boolean; createdAt: string; createdBy: string
}

const envStore = useEnvStore()
const tab = ref('builds')
const builds = ref<BuildRun[]>([])
// 镜像库实时视图：registry v2 catalog（hub.wang.dd 真实镜像），展开行按需加载 tag + digest
const registryRepos = ref<{ name: string }[]>([])
const expandedTags = ref<Record<string, { tag: string; digest: string }[]>>({})
const releases = ref<Release[]>([])
const loading = ref(false)

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

// 应用名映射：onMounted 时一次拉取应用列表建 Map，跨应用总览用应用名而非裸 ID 展示。
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
// 展开行按需加载 tag（首次展开才请求，避免全量 N+1）
async function onExpand(row: { name: string }, expanded: readonly { name: string }[]) {
  const isOpen = expanded.some((r) => r.name === row.name)
  if (isOpen && !expandedTags.value[row.name]) await loadTags(row.name)
}
async function loadReleases() {
  const resp = await fetchAuth('/api/releases')
  if (resp.ok) releases.value = (await resp.json()).data ?? []
}

async function load() {
  loading.value = true
  try {
    await Promise.all([loadBuilds(), loadRegistry(), loadReleases(), loadAppNames()])
  } finally {
    loading.value = false
  }
}

// 构建状态轮询（模拟 CI 异步流转，5s 刷新）
let timer: number | undefined
function startPoll() {
  timer = window.setInterval(loadBuilds, 5000)
}

async function rollback(row: Release) {
  const ok = await confirmDangerous({
    action: '回滚发布', target: `${appName(row.appId)} @ ${envName(row.envId)}`,
    requireNameConfirm: envStore.isProd && row.envId === envStore.currentEnv?.id,
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

function shortDigest(d: string) {
  return d && d.length > 19 ? d.slice(0, 19) + '…' : d
}

onMounted(() => { load(); startPoll() })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="devops-page">
    <div class="page-head">
      <div>
        <h2>DevOps 中心</h2>
        <p class="sub">跨应用 CI/CD 总览：代码 → 构建 → 镜像 → 发布 → 回滚</p>
      </div>
      <el-button @click="load">刷新</el-button>
    </div>

    <el-tabs v-model="tab" class="devops-tabs">
      <!-- 构建 -->
      <el-tab-pane label="构建" name="builds">
        <el-table :data="builds" size="small" v-loading="loading" empty-text="暂无构建">
          <el-table-column label="应用" width="140">
            <template #default="{ row }"><span class="mono">{{ appName(row.appId) }}</span></template>
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
        </el-table>
      </el-tab-pane>

      <!-- 镜像库（registry v2 实时视图） -->
      <el-tab-pane label="镜像库" name="images">
        <p class="tab-hint">镜像仓库实时列表（hub.wang.dd:5000），展开行查看 tag 与 digest</p>
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
            <template #default="{ row }"><span class="mono">{{ appName(row.appId) }}</span></template>
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
            </template>
          </el-table-column>
          <el-table-column label="发布时间" width="170">
            <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button v-if="row.status === 'succeeded'" text type="warning" size="small" @click="rollback(row)">回滚</el-button>
              <span v-else class="text-faint">—</span>
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
.tab-hint { font-size: 12.5px; color: var(--text-dim); margin: 0 0 10px; }
.tag-list { padding: 4px 12px; display: flex; flex-direction: column; gap: 6px; }
.tag-row { display: flex; align-items: center; gap: 12px; font-size: 12.5px; }
.tag-name { min-width: 120px; color: var(--text); }
.digest { word-break: break-all; }
.faint { color: var(--text-faint); }
.mono { font-family: var(--mono, ui-monospace, monospace); }
</style>
