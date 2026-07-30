<script setup lang="ts">
// DevOps 中心：跨应用 CI/CD 总览（构建 / 镜像 / 发布），消除侧栏 DevOps「即将」。
// 复用既有 devops 后端跨应用列表端点；发布回滚走 useDangerConfirm（生产输入名称）。
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface BuildRun {
  id: string; appId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string
}
interface Image { id: string; appId: string; tag: string; digest: string; source: string; builtAt: string }
interface Release {
  id: string; appId: string; envId: string; imageDigest: string
  strategy: string; status: string; isRollback: boolean; createdAt: string; createdBy: string
}

const envStore = useEnvStore()
const tab = ref('builds')
const builds = ref<BuildRun[]>([])
const images = ref<Image[]>([])
const releases = ref<Release[]>([])
const loading = ref(false)

const BUILD_STATUS: Record<string, { label: string; type: string }> = {
  pending: { label: '排队', type: 'info' },
  running: { label: '构建中', type: 'warning' },
  success: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'danger' },
}
const RELEASE_STATUS: Record<string, { label: string; type: string }> = {
  succeeded: { label: '已生效', type: 'success' },
  'rolled-back': { label: '已回滚', type: 'info' },
  deploying: { label: '部署中', type: 'warning' },
}

const appName = (id: string) => id // 跨应用总览用 appId 直展示（应用名需额外查询，本期用 id）
const envName = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

async function loadBuilds() {
  const resp = await fetchAuth('/api/buildruns')
  if (resp.ok) builds.value = (await resp.json()).data ?? []
}
async function loadImages() {
  const resp = await fetchAuth('/api/images')
  if (resp.ok) images.value = (await resp.json()).data ?? []
}
async function loadReleases() {
  const resp = await fetchAuth('/api/releases')
  if (resp.ok) releases.value = (await resp.json()).data ?? []
}

async function load() {
  loading.value = true
  try {
    await Promise.all([loadBuilds(), loadImages(), loadReleases()])
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
  const resp = await fetchAuth(`/api/releases/${row.id}/rollback`, { method: 'POST' })
  if (resp.ok) { ElMessage.success('已回滚'); loadReleases() }
  else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '回滚失败') }
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
              <el-tag :type="(BUILD_STATUS[row.status]?.type as any) || 'info'" size="small">
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

      <!-- 镜像 -->
      <el-tab-pane label="镜像" name="images">
        <el-table :data="images" size="small" empty-text="暂无镜像">
          <el-table-column label="应用" width="140">
            <template #default="{ row }"><span class="mono">{{ appName(row.appId) }}</span></template>
          </el-table-column>
          <el-table-column label="Tag" min-width="160">
            <template #default="{ row }"><span class="mono">{{ row.tag }}</span></template>
          </el-table-column>
          <el-table-column label="Digest（不可变）" min-width="200">
            <template #default="{ row }"><span class="mono">{{ shortDigest(row.digest) }}</span></template>
          </el-table-column>
          <el-table-column label="来源" width="100">
            <template #default="{ row }">{{ row.source }}</template>
          </el-table-column>
          <el-table-column label="构建时间" width="170">
            <template #default="{ row }">{{ new Date(row.builtAt).toLocaleString() }}</template>
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
              <el-tag :type="(RELEASE_STATUS[row.status]?.type as any) || 'info'" size="small">
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
</style>
