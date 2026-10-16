<script setup lang="ts">
// 构建详情（v2 原生，功能对等迁移）：状态头 + 失败重试 + 实时日志
// （构建中 SSE follow Pod logs，终态回落全量落库日志——与老版同模式）。
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { statusOf, tagTypeToCls, BUILD_STATUS } from '@/composables/useStatus'

interface BuildRunFull {
  id: string; appId: string; repoId: string; commit: string; branch: string; message: string
  status: string; imageId?: string; startedAt: string; finishedAt?: string; log?: string
  buildArgs?: Record<string, string>
}

const route = useRoute()
const router = useRouter()
const build = ref<BuildRunFull | null>(null)
const loadFailed = ref(false)
const logBox = ref<HTMLPreElement | null>(null)

// 实时日志片段（SSE 追加）；终态回落 build.log 全量。
const liveLog = ref('')
const logText = computed(() => liveLog.value || build.value?.log || '')
const streaming = ref(false)
const isTerminal = (s?: string) => s === 'success' || s === 'failed'

let logES: EventSource | null = null
function closeStream() {
  if (logES) { logES.close(); logES = null }
  streaming.value = false
}
function scrollBottom() {
  void nextTick(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  })
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
    void load() // 流结束拉终态全量（Pod 完成后 Log 落库）
  })
  es.addEventListener('error', () => {
    closeStream()
    if (!liveLog.value) liveLog.value = '（实时日志不可用，构建完成后刷新拉全量）\n'
  })
}

async function load() {
  try {
    const resp = await fetchAuth(`/api/buildruns/${route.params.id}`)
    if (!resp.ok) throw new Error('not found')
    const j = await resp.json()
    const fresh: BuildRunFull = j?.data ?? j
    const prev = build.value
    build.value = fresh
    if (prev && prev.status !== fresh.status) liveLog.value = ''
    if (!isTerminal(fresh.status)) {
      if (!logES) { liveLog.value = ''; startStream(fresh.id) }
    } else {
      closeStream()
    }
    loadFailed.value = false
  } catch {
    loadFailed.value = true
  }
}
onMounted(load)
onUnmounted(closeStream)

const retrying = ref(false)
async function retry() {
  const b = build.value
  if (!b || retrying.value) return
  retrying.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${b.appId}/buildruns`, {
      method: 'POST',
      body: JSON.stringify({ repoId: b.repoId, branch: b.branch, buildArgs: b.buildArgs }),
    })
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}))
      throw new Error(err.error || '触发失败')
    }
    const j = await resp.json()
    ElMessage.success('已重新触发')
    if (j?.data?.id && j.data.id !== b.id) router.replace(`/builds/${j.data.id}`)
    else await load()
  } catch (e) {
    ElMessage.error(apiError(e, '触发失败'))
  } finally {
    retrying.value = false
  }
}

const fmtTime = (iso?: string) => (iso ?? '').replace('T', ' ').slice(0, 19) || '—'
</script>

<template>
  <div class="page">
    <div class="head-row">
      <a class="back" @click="router.back()">← 返回</a>
    </div>

    <div v-if="loadFailed" class="empty">构建不存在或已被删除</div>
    <template v-else-if="build">
      <div class="card head-card">
        <div class="head-main">
          <div class="title-row">
            <h2 class="name mono">构建 {{ build.id.slice(0, 14) }}</h2>
            <span class="status" :class="tagTypeToCls(statusOf(BUILD_STATUS, build.status).type)">
              {{ statusOf(BUILD_STATUS, build.status).label }}
            </span>
            <span v-if="streaming" class="live-dot">● 实时</span>
          </div>
          <div class="meta-row">
            <span class="meta mono">{{ build.branch }}</span>
            <span v-if="build.commit" class="meta mono dim">{{ build.commit.slice(0, 8) }}</span>
            <span class="meta">{{ fmtTime(build.startedAt) }}</span>
          </div>
          <p v-if="build.message" class="msg">{{ build.message }}</p>
        </div>
        <button v-if="build.status === 'failed'" class="act primary" :disabled="retrying" @click="retry">
          {{ retrying ? '触发中…' : '重试构建' }}
        </button>
      </div>

      <div class="card log-card">
        <div class="card-head">
          <span class="card-title">日志</span>
          <span class="card-sub">{{ streaming ? '实时跟随中…' : logText ? `${logText.split('\n').length} 行` : '无日志' }}</span>
        </div>
        <pre ref="logBox" class="log mono">{{ logText || '（暂无日志）' }}</pre>
      </div>
    </template>
    <div v-else class="loading-block" v-loading="true"></div>
  </div>
</template>

<style scoped>
@import './list-page.css';
.page { display: flex; flex-direction: column; gap: 14px; }
.back { font-size: 12.5px; color: var(--brand); cursor: pointer; text-decoration: none; }
.head-card { display: flex; align-items: flex-start; gap: 16px; }
.head-main { flex: 1; min-width: 0; }
.title-row { display: flex; align-items: center; gap: 10px; }
.name { margin: 0; font-size: 16px; font-weight: 600; }
.live-dot { font-size: 11px; color: var(--warning, #f59e0b); }
.meta-row { display: flex; gap: 14px; margin-top: 8px; flex-wrap: wrap; }
.meta { font-size: 12px; color: var(--text-dim); }
.msg { margin: 8px 0 0; font-size: 12.5px; color: var(--text-faint); }
.empty { padding: 60px 0; text-align: center; font-size: 13px; color: var(--text-faint); }
.loading-block { height: 200px; }
.log-card { padding-bottom: 8px; }
.log {
  margin: 0;
  max-height: 560px;
  overflow: auto;
  padding: 12px 14px;
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 12px;
  line-height: 1.55;
  color: var(--text-dim);
  white-space: pre-wrap;
  word-break: break-all;
}
.mono { font-family: 'JetBrains Mono', monospace; }
.dim { color: var(--text-faint); }
</style>
