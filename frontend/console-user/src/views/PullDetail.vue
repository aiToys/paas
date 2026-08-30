<script setup lang="ts">
// PR 详情页（Code Review 核心）：meta + diff 渲染 + 整体评审 + merge。
// 真源 Gitea；diff 自研轻量解析（utils/diff.ts）。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { getPullDetail, mergePull, reviewPull, type PullDetail as PD } from '@/api/pulls'
import { parseDiff } from '@/utils/diff'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const route = useRoute()
const router = useRouter()
const appId = route.query.appId as string || ''
const repoId = route.params.repoId as string
const number = Number(route.params.number)

const detail = ref<PD | null>(null)
const loading = ref(false)
const reviewBody = ref('')
const reviewBusy = ref(false)
const mergeBusy = ref(false)
// 折叠状态（文件路径 -> 是否展开，默认全展开）
const collapsed = ref<Record<string, boolean>>({})

const load = async () => {
  if (!appId) {
    ElMessage.warning('缺少 appId 参数，请从评审列表进入')
    return
  }
  loading.value = true
  try {
    detail.value = await getPullDetail(appId, repoId, number)
  } catch (e) {
    ElMessage.error('加载 PR 失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

const files = computed(() => parseDiff(detail.value?.diff || ''))

const stateType = computed(() => {
  const pr = detail.value?.pr
  if (!pr) return 'info'
  return pr.merged ? 'success' : pr.state === 'open' ? 'primary' : 'info'
})
const stateLabel = computed(() => {
  const pr = detail.value?.pr
  if (!pr) return ''
  return pr.merged ? '已合并' : pr.state === 'open' ? '开放' : '已关闭'
})

const doReview = async (doAction: string, label: string) => {
  reviewBusy.value = true
  try {
    await reviewPull(appId, repoId, number, doAction, reviewBody.value)
    ElMessage.success(`已${label}`)
    reviewBody.value = ''
    await load()
  } catch (e) {
    ElMessage.error(`${label}失败：` + (e as Error).message)
  } finally {
    reviewBusy.value = false
  }
}

const doMerge = async () => {
  // merge 到主干（main）一律危险确认（输入 PR#number）
  const ok = await confirmDangerous({
    action: '合并',
    target: `PR#${number}`,
    isProd: true, // merge 直接影响主干代码，按生产语义防护
    requireNameConfirm: true,
  })
  if (!ok) return
  mergeBusy.value = true
  try {
    await mergePull(appId, repoId, number)
    ElMessage.success('合并成功')
    await load()
  } catch (e) {
    ElMessage.error('合并失败：' + (e as Error).message)
  } finally {
    mergeBusy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page detail-page" v-loading="loading">
    <!-- meta 身份条 -->
    <header class="crumb">
      <button class="back" @click="router.back()">←</button>
      <span>PR</span>
      <span class="sep">/</span>
      <span class="mono">#{{ number }}</span>
      <span v-if="detail" class="title">{{ detail.pr.title }}</span>
      <el-tag v-if="detail" :type="stateType" size="small">{{ stateLabel }}</el-tag>
      <span class="grow"></span>
      <el-button
v-if="detail?.pr.state === 'open'" size="small" type="primary"
        :loading="mergeBusy" @click="doMerge"
>
合并
</el-button>
    </header>

    <template v-if="detail">
      <div class="meta-card">
        <div class="kv"><span>分支</span><code class="mono">{{ detail.pr.head }} → {{ detail.pr.base }}</code></div>
        <div class="kv"><span>作者</span>{{ detail.pr.user }}</div>
        <div class="kv"><span>创建</span>{{ new Date(detail.pr.createdAt).toLocaleString() }}</div>
        <div v-if="detail.pr.body" class="kv"><span>说明</span>{{ detail.pr.body }}</div>
      </div>

      <el-alert
v-if="detail.truncated" type="warning" :closable="false" show-icon
        title="diff 过大已截断（>2MB），请到 Git 平台查看完整内容" class="truncate-tip"
/>

      <!-- diff 渲染区 -->
      <div v-for="f in files" :key="f.path" class="diff-file">
        <div class="diff-head" @click="collapsed[f.path] = !collapsed[f.path]">
          <span class="chev">{{ collapsed[f.path] ? '▸' : '▾' }}</span>
          <code class="mono path">{{ f.path }}</code>
          <span class="stat">+{{ f.adds }} <span class="del-count">−{{ f.dels }}</span></span>
        </div>
        <div v-show="!collapsed[f.path]" class="diff-body">
          <div v-for="(l, i) in f.lines" :key="i" class="diff-line" :class="'diff-' + l.type">{{ l.text }}</div>
        </div>
      </div>
      <el-empty v-if="!files.length" description="无差异内容" />

      <!-- 评审操作条 -->
      <div v-if="detail.pr.state === 'open'" class="review-bar">
        <el-input v-model="reviewBody" type="textarea" :rows="2" placeholder="评审意见（可选）" />
        <div class="review-actions">
          <el-button size="small" @click="doReview('COMMENT', '评论')" :loading="reviewBusy">仅评论</el-button>
          <el-button size="small" type="warning" @click="doReview('REQUEST_CHANGES', '要求修改')" :loading="reviewBusy">要求修改</el-button>
          <el-button size="small" type="success" @click="doReview('APPROVE', '批准')" :loading="reviewBusy">批准</el-button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.crumb { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.back { border: none; background: none; cursor: pointer; font-size: 16px; }
.sep, .grow { color: var(--el-text-color-secondary); }
.grow { flex: 1; }
.title { font-weight: 600; }
.mono { font-family: monospace; font-size: 12px; }

.meta-card { border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 12px; margin-bottom: 12px; }
.kv { display: flex; gap: 8px; align-items: baseline; margin: 4px 0; font-size: 13px; }
.kv span:first-child { color: var(--el-text-color-secondary); min-width: 40px; }

.truncate-tip { margin-bottom: 12px; }

.diff-file { border: 1px solid var(--el-border-color-lighter); border-radius: 6px; margin-bottom: 12px; overflow: hidden; }
.diff-head { display: flex; align-items: center; gap: 8px; padding: 6px 12px; background: var(--el-fill-color-light); cursor: pointer; }
.diff-head .path { font-weight: 600; }
.diff-head .stat { margin-left: auto; color: var(--el-color-success); font-size: 12px; }
.diff-head .del-count { color: var(--el-color-danger); }
.diff-body { max-height: 480px; overflow: auto; }
.diff-line { font-family: monospace; font-size: 12px; padding: 0 12px; white-space: pre; line-height: 1.6; }
.diff-add { background: var(--el-color-success-light-9); }
.diff-del { background: var(--el-color-danger-light-9); }
.diff-meta { color: var(--el-text-color-secondary); font-size: 11px; }
.diff-ctx { color: var(--el-text-color-regular); }

.review-bar { margin-top: 16px; display: flex; flex-direction: column; gap: 8px; }
.review-actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
