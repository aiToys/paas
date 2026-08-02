<script setup lang="ts">
// 应用详情 - 构建 tab：触发构建 + 列表 + 状态轮询 + 日志展开。
// mock CI runner 异步流转 pending->running->success，前端轮询直到全部终态。
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string }>()

interface BuildRun {
  id: string
  repoId: string
  trigger: string
  commit: string
  branch: string
  message: string
  status: string
  imageId: string
  log: string
  startedAt: string
  finishedAt: string
}
interface Repo {
  id: string
  gitUrl: string
  branch: string
}

const builds = ref<BuildRun[]>([])
const repos = ref<Repo[]>([])
const loading = ref(false)
const showTrigger = ref(false)
const selectedRepo = ref('')
let pollTimer: number | null = null

async function loadRepos() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/repositories`)
  if (resp.ok) repos.value = (await resp.json()).data ?? []
  if (repos.value.length && !selectedRepo.value) selectedRepo.value = repos.value[0].id
}

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/buildruns`)
    if (resp.ok) builds.value = (await resp.json()).data ?? []
    schedulePoll()
  } finally {
    loading.value = false
  }
}

function schedulePoll() {
  const hasPending = builds.value.some((b) => b.status === 'pending' || b.status === 'running')
  if (hasPending && pollTimer === null) {
    pollTimer = window.setInterval(async () => {
      try {
        const resp = await fetchAuth(`/api/applications/${props.appId}/buildruns`)
        if (resp.ok) {
          builds.value = (await resp.json()).data ?? []
          const still = builds.value.some((b) => b.status === 'pending' || b.status === 'running')
          if (!still && pollTimer !== null) {
            clearInterval(pollTimer)
            pollTimer = null
          }
        }
      } catch {
        // 网络错误静默：轮询不阻塞用户，下次 tick 重试
      }
    }, 2000)
  }
}

async function trigger() {
  if (!selectedRepo.value) {
    ElMessage.warning('请先选择仓库')
    return
  }
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/buildruns`, {
      method: 'POST',
      body: JSON.stringify({ repoId: selectedRepo.value }),
    })
    if (resp.ok) {
      ElMessage.success('构建已触发')
      showTrigger.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '触发失败')
    }
  } catch (e) {
    ElMessage.error('触发失败：' + (e as Error).message)
  }
}

const statusType = (s: string) =>
  (({ success: 'success', failed: 'danger', running: 'warning', pending: 'info' } as Record<string, string>)[s] || 'info')

onMounted(async () => {
  await loadRepos()
  await load()
})
onUnmounted(() => {
  if (pollTimer !== null) clearInterval(pollTimer)
})
watch(() => props.appId, async () => {
  await loadRepos()
  await load()
})
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">构建</span>
      <el-button type="primary" size="small" :disabled="!repos.length" @click="showTrigger = true">
        触发构建
      </el-button>
    </div>
    <el-table :data="builds" v-loading="loading" size="small" empty-text="尚无构建记录" row-key="id">
      <el-table-column type="expand">
        <template #default="{ row }">
          <pre class="build-log mono">{{ row.log || '（无日志）' }}</pre>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="commit" width="100">
        <template #default="{ row }"><span class="mono">{{ (row.commit || '').slice(0, 8) }}</span></template>
      </el-table-column>
      <el-table-column prop="branch" label="分支" width="90" />
      <el-table-column prop="message" label="提交信息" min-width="180" />
      <el-table-column label="产出镜像" width="130">
        <template #default="{ row }"><span class="mono">{{ (row.imageId || '').slice(0, 14) || '—' }}</span></template>
      </el-table-column>
      <el-table-column label="开始时间" width="160">
        <template #default="{ row }">{{ new Date(row.startedAt).toLocaleString() }}</template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showTrigger" title="触发构建" width="440px">
      <el-form label-width="70px">
        <el-form-item label="仓库">
          <el-select v-model="selectedRepo" placeholder="选择仓库" style="width: 100%">
            <el-option v-for="r in repos" :key="r.id" :label="r.gitUrl" :value="r.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <div v-if="!repos.length" class="empty-hint">请先在「代码仓库」tab 绑定仓库</div>
      <template #footer>
        <el-button @click="showTrigger = false">取消</el-button>
        <el-button type="primary" :disabled="!selectedRepo" @click="trigger">触发</el-button>
      </template>
    </el-dialog>
  </div>
</template>
