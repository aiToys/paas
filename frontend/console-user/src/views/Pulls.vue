<script setup lang="ts">
// 单仓库 PR 列表（应用内「代码评审」入口）。真源 Gitea。
import { onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useRoute, useRouter } from 'vue-router'
import { listPulls, type PullRequest } from '@/api/pulls'

const route = useRoute()
const router = useRouter()
// 路由是 /apps/:appId/repositories/:repoId/pulls——appId/repoId 均为 path param
const appId = route.params.appId as string || ''
const repoId = route.params.repoId as string

const state = ref<'open' | 'closed' | 'all'>('open')
const pulls = ref<PullRequest[]>([])
const loading = ref(false)
// 请求序号防竞态：快速切换 state tab 时旧慢响应不覆盖新结果
let seq = 0

const load = async () => {
  const cur = ++seq
  loading.value = true
  try {
    const list = await listPulls(appId, repoId, state.value)
    if (cur === seq) pulls.value = list
  } catch (e) {
    if (cur === seq) {
      pulls.value = []
      ElMessage.error('加载 PR 列表失败：' + (e as Error).message)
    }
  } finally {
    if (cur === seq) loading.value = false
  }
}

const stateType = (p: PullRequest) => (p.merged ? 'success' : p.state === 'open' ? 'primary' : 'info')
const stateLabel = (p: PullRequest) => (p.merged ? '已合并' : p.state === 'open' ? '开放' : '已关闭')

onMounted(load)
watch(state, load)
</script>

<template>
  <div class="page">
    <header class="crumb">
      <button class="back" @click="router.back()">←</button>
      <span>代码评审</span>
      <span class="sep">/</span>
      <span class="mono dim">{{ repoId }}</span>
    </header>

    <el-tabs v-model="state">
      <el-tab-pane label="开放" name="open" />
      <el-tab-pane label="已关闭" name="closed" />
      <el-tab-pane label="全部" name="all" />
    </el-tabs>

    <el-table :data="pulls" v-loading="loading" empty-text="暂无 PR">
      <el-table-column label="#" width="70">
        <template #default="{ row }"><span class="mono">#{{ row.number }}</span></template>
      </el-table-column>
      <el-table-column label="标题" min-width="260">
        <template #default="{ row }">
          <a class="link" @click="router.push(`/devops/pulls/${repoId}/${row.number}?appId=${appId}`)">{{ row.title }}</a>
        </template>
      </el-table-column>
      <el-table-column label="分支" min-width="180">
        <template #default="{ row }"><code class="mono">{{ row.head }} → {{ row.base }}</code></template>
      </el-table-column>
      <el-table-column prop="user" label="作者" width="100" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }"><el-tag size="small" :type="stateType(row)">{{ stateLabel(row) }}</el-tag></template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="90">
        <template #default="{ row }">
          <el-button
text size="small" type="primary"
            @click="router.push(`/devops/pulls/${repoId}/${row.number}?appId=${appId}`)"
>
查看
</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.crumb { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.back { border: none; background: none; cursor: pointer; font-size: 16px; }
.sep, .dim { color: var(--el-text-color-secondary); }
.mono { font-family: monospace; font-size: 12px; }
.link { color: var(--el-color-primary); cursor: pointer; }
</style>
