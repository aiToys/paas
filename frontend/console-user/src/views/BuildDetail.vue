<template>
  <div class="page detail-page">
    <DetailShell
      :crumbs="[{ label: '构建', to: '/devops' }, { label: build?.id ?? (route.params.id as string) }]"
      :tags="build ? [{ label: build.status, type: stType(build.status) as ShellTag['type'] }] : []"
    >
      <template #actions>
        <el-button v-if="build?.status === 'failed'" size="small" type="primary" plain :loading="retrying" @click="retry">
          重新构建
        </el-button>
      </template>
    </DetailShell>

    <div v-if="build" class="body">
      <section class="card">
        <div class="grid">
          <div class="kv"><span>应用</span><a class="link" @click="router.push(`/applications/${build.appId}`)">{{ build.appId }}</a></div>
          <div class="kv"><span>分支</span><code class="mono">{{ build.branch }}</code></div>
          <div class="kv"><span>提交</span><code class="mono">{{ build.commit?.slice(0, 8) }}</code></div>
          <div class="kv"><span>信息</span><span>{{ build.message }}</span></div>
          <div class="kv"><span>开始</span><span>{{ build.startedAt }}</span></div>
          <div class="kv"><span>结束</span><span>{{ build.finishedAt || '—' }}</span></div>
          <div class="kv">
<span>仓库</span>
            <a class="link mono" @click="router.push(repoLink(build.appId, build.repoId))">浏览代码 →</a>
          </div>
          <div v-if="build.imageId" class="kv">
<span>产出镜像</span>
            <a class="link mono" @click="router.push(imageLink(build.appId, build.imageId))">{{ build.imageId }}</a>
          </div>
        </div>
      </section>
      <section class="card">
        <h3>构建日志<span v-if="streaming" class="live-dot">● 实时</span></h3>
        <pre ref="logBox" class="log mono">{{ logText || '（无日志）' }}</pre>
      </section>
    </div>
    <el-skeleton v-else :rows="5" animated />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { imageLink, repoLink } from '@/composables/useDevopsLinks'
import DetailShell from '@/components/DetailShell.vue'
import type { ShellTag } from '@/components/DetailShell.vue'

interface BuildRunFull {
  id: string; appId: string; repoId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string; log?: string
  buildArgs?: Record<string, string>
}

const route = useRoute()
const router = useRouter()
const build = ref<BuildRunFull>()
const logBox = ref<HTMLPreElement | null>(null)
const retrying = ref(false)
const streaming = ref(false)

// 实时日志片段（构建中 SSE 追加）；终态时回落 build.log 全量。
const liveLog = ref('')
const logText = computed(() => liveLog.value || build.value?.log || '')

const stType = (s?: string) => ({ success: 'success', failed: 'danger', running: 'warning' } as Record<string, string>)[s ?? ''] ?? 'info'
const isTerminal = (s?: string) => s === 'success' || s === 'failed'

// ---- SSE 实时日志（旅程审计 R5-M6：构建中详情页 follow Pod logs，与 PipelineRunView 同模式） ----
let logES: EventSource | null = null
function closeStream() {
  if (logES) { logES.close(); logES = null }
  streaming.value = false
}

function startStream(id: string) {
  closeStream()
  const es = new EventSource(`/api/buildruns/${id}/logs/stream`)
  logES = es
  streaming.value = true
  es.onmessage = (ev) => {
    liveLog.value += ev.data + '\n'
    scrollBottom()
  }
  es.addEventListener('end', () => {
    closeStream()
    // 流结束拉终态全量（Pod 完成后 Log 落库），顺带刷新状态卡
    load()
  })
  es.addEventListener('error', () => {
    closeStream()
    if (!liveLog.value) liveLog.value = '（实时日志不可用，构建完成后可刷新拉全量）\n'
  })
}

function scrollBottom() {
  void nextTick(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  })
}

async function load() {
  const resp = await fetchAuth(`/api/buildruns/${route.params.id}`)
  if (!resp.ok) {
    ElMessage.error('构建不存在')
    return
  }
  const j = await resp.json()
  const prev = build.value
  const fresh: BuildRunFull = j?.data ?? j
  build.value = fresh
  // 状态推进时清实时片段回落全量（避免新旧叠加）
  if (prev && prev.status !== fresh.status) liveLog.value = ''
  // 构建中：开 SSE 流；终态：关流
  if (!isTerminal(fresh.status)) {
    if (!logES) { liveLog.value = ''; startStream(fresh.id) }
  } else {
    closeStream()
  }
}

// 重试：按同仓库/分支/参数重新触发（非断点续跑；429 并发超限时后端拒绝）
async function retry() {
  const b = build.value
  if (!b || retrying.value) return
  retrying.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${b.appId}/buildruns`, {
      method: 'POST',
      body: JSON.stringify({ repoId: b.repoId, branch: b.branch, buildArgs: b.buildArgs }),
    })
    if (resp.ok) {
      const j = await resp.json()
      ElMessage.success('已重新触发')
      // 跳到新构建详情（旧记录保持终态不变）
      if (j?.data?.id && j.data.id !== b.id) router.replace(`/devops/builds/${j.data.id}`)
      else load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '触发失败')
    }
  } catch (e) {
    ElMessage.error(apiError(e, '触发失败'))
  } finally {
    retrying.value = false
  }
}

onMounted(load)
// 同路由不同 id 复用组件（详情互链点另一构建）时重载
watch(() => route.params.id, () => { closeStream(); liveLog.value = ''; load() })
onUnmounted(closeStream)
</script>

<style scoped>
.detail-page { padding: 20px; max-width: 960px; margin: 0 auto; }
.body { display: grid; gap: 14px; }
.card { background: var(--el-bg-color); border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 16px 20px; }
.card h3 { margin: 0 0 12px; font-size: 14px; color: var(--el-text-color-secondary); display: flex; align-items: center; gap: 8px; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 4px 24px; }
.kv { display: flex; align-items: center; gap: 8px; margin: 4px 0; font-size: 13px; }
.kv > span:first-child { color: var(--el-text-color-secondary); min-width: 64px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.link { color: var(--el-color-primary); cursor: pointer; }
.log { background: var(--el-fill-color-darker); color: var(--el-text-color-primary); padding: 12px; border-radius: 6px; max-height: 480px; overflow: auto; white-space: pre-wrap; word-break: break-all; margin: 0; }
.live-dot { color: var(--el-color-success); font-size: 12px; animation: pulse 1.5s infinite; }
@keyframes pulse { 50% { opacity: 0.4; } }
</style>
