<script setup lang="ts">
// Agent 运行详情（agentrun 观测通道）：状态 KV + 结论 + 产物查看 + 取消（非终态）。
// 运行中 5s 轮询至终态；产物文本/diff 在 dialog 内 <pre> 展示（契约 §5.2 页面 3）。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { formatDateTime } from '@/utils/format'
import { usePolling } from '@/composables/usePolling'
import { getRun, cancelRun, listArtifacts, getArtifact, getRunLogs, phaseTagType, type AgentRunItem, type AgentArtifact } from '@/api/agentrun'

const route = useRoute()
const router = useRouter()
const name = computed(() => decodeURIComponent(String(route.params.name || '')))
// 可选时间字段的安全格式化（对齐 AppPipelines fmtTime 模式）
const fmtTime = (t?: string) => (t ? formatDateTime(t) : '-')

const run = ref<AgentRunItem | null>(null)
const loadErr = ref('')
const artifacts = ref<AgentArtifact[]>([])
const artDlg = ref(false)
const artKey = ref('')
const artContent = ref('')
const artLoading = ref(false)
// 工作日志（AI 执行过程）：运行中随轮询刷新；TTL 回收后对方返提示文本（保留 30 分钟语义见 api 注释）
const logs = ref('')
const logsLoading = ref(false)

const isTerminal = (p?: string) => p === 'Succeeded' || p === 'Failed'
const active = () => !!run.value && !isTerminal(run.value.phase)

async function load(): Promise<void> {
  try {
    run.value = await getRun(name.value)
    loadErr.value = ''
    await loadLogs()
    if (isTerminal(run.value.phase) && artifacts.value.length === 0) await loadArtifacts()
  } catch (e) {
    loadErr.value = e instanceof Error ? e.message : String(e)
  }
}
async function loadLogs(): Promise<void> {
  logsLoading.value = true
  try { logs.value = await getRunLogs(name.value) } finally { logsLoading.value = false }
}
async function loadArtifacts(): Promise<void> {
  try { artifacts.value = await listArtifacts(name.value) } catch { /* 产物不可用不阻断详情 */ }
}
usePolling(load, 5000, { active })

async function onCancel(): Promise<void> {
  try {
    await cancelRun(name.value)
    ElMessage.success('已请求取消')
    await load()
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '取消失败')
  }
}
async function openArtifact(key: string): Promise<void> {
  artKey.value = key
  artContent.value = ''
  artDlg.value = true
  artLoading.value = true
  try {
    const c = await getArtifact(name.value, key)
    // 产物可能是文本或 JSON 字符串：JSON 字符串美化展示，其余原样
    if (typeof c === 'string' && (c.startsWith('{') || c.startsWith('['))) {
      try { artContent.value = JSON.stringify(JSON.parse(c), null, 2); return } catch { /* 非 JSON 原样 */ }
    }
    artContent.value = typeof c === 'string' ? c : JSON.stringify(c, null, 2)
  } catch (e) {
    artContent.value = `加载失败: ${e instanceof Error ? e.message : String(e)}`
  } finally {
    artLoading.value = false
  }
}
onMounted(load)
</script>

<template>
  <div class="run-detail">
    <div class="detail-head">
      <el-page-header @back="router.push('/ai/runs')">
        <template #content>
          <span class="mono">{{ name }}</span>
          <el-tag v-if="run?.phase" :type="phaseTagType(run.phase)" size="small" style="margin-left: 8px">{{ run.phase }}</el-tag>
        </template>
      </el-page-header>
      <div class="head-actions" v-if="run && !isTerminal(run.phase)">
        <el-button size="small" type="danger" plain @click="onCancel">取消运行</el-button>
      </div>
    </div>

    <el-alert v-if="loadErr" type="error" :title="loadErr" :closable="false" show-icon style="margin: 12px 0" />
    <el-skeleton v-if="!run && !loadErr" :rows="4" animated style="margin: 12px 0" />

    <template v-if="run">
      <el-descriptions :column="2" border size="small" style="margin-top: 12px">
        <el-descriptions-item label="阶段">{{ run.stage || '-' }}</el-descriptions-item>
        <el-descriptions-item label="模式">{{ run.mode || '-' }}</el-descriptions-item>
        <el-descriptions-item label="runtime">{{ run.runtime || '-' }}</el-descriptions-item>
        <el-descriptions-item label="判定">
          <span v-if="run.verdict" :style="run.verdict !== 'pass' ? 'color: var(--el-color-danger)' : ''">{{ run.verdict }}</span>
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="产出分支">
          <span v-if="run.branch" class="mono">{{ run.branch }}</span>
          <span v-else>-</span>
        </el-descriptions-item>
        <el-descriptions-item label="tokens">{{ run.tokens || '-' }}</el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ fmtTime(run.createdAt) }}</el-descriptions-item>
        <el-descriptions-item label="结束时间">{{ fmtTime(run.finishedAt) }}</el-descriptions-item>
        <el-descriptions-item label="需求" :span="2">
          <span class="request-text">{{ run.request || '-' }}</span>
        </el-descriptions-item>
      </el-descriptions>

      <el-card v-if="run.message" shadow="never" style="margin-top: 12px">
        <template #header>结论 / 消息</template>
        <pre class="msg-pre">{{ run.message }}</pre>
      </el-card>

      <!-- 工作日志：AI 执行过程（运行中随轮询自动刷新；Pod TTL 回收后显示平台提示） -->
      <el-card shadow="never" style="margin-top: 12px">
        <template #header>
          <span>工作日志</span>
          <el-button size="small" text style="margin-left: 8px" :loading="logsLoading" @click="loadLogs">刷新</el-button>
        </template>
        <pre class="msg-pre logs-pre" :class="{ 'logs-hint': logs.startsWith('ℹ️') }">{{ logs || '（加载中…）' }}</pre>
      </el-card>

      <el-card shadow="never" style="margin-top: 12px">
        <template #header>产物（{{ artifacts.length }}）</template>
        <el-empty v-if="artifacts.length === 0" description="无产物（或运行未完成）" :image-size="48" />
        <div v-else class="art-list">
          <el-button
v-for="a in artifacts" :key="a.key" size="small" plain style="margin: 0 8px 8px 0"
            @click="openArtifact(a.key)"
>
            <span class="mono">{{ a.key }}</span>
          </el-button>
        </div>
      </el-card>
    </template>

    <el-dialog v-model="artDlg" :title="artKey" width="72%" top="6vh">
      <div v-loading="artLoading" class="art-body">
        <pre>{{ artContent }}</pre>
      </div>
    </el-dialog>
  </div>
</template>

<style scoped>
.run-detail { padding: 16px; max-width: 1100px; margin: 0 auto; }
.detail-head { display: flex; align-items: center; justify-content: space-between; }
.mono { font-family: var(--font-mono, monospace); }
.request-text { white-space: pre-wrap; word-break: break-all; }
.msg-pre, .art-body pre {
  white-space: pre-wrap; word-break: break-all; margin: 0;
  font-family: var(--font-mono, monospace); font-size: 12px; line-height: 1.6;
}
.art-body { max-height: 68vh; overflow: auto; }
/* 工作日志：限高滚动（日志可长）；TTL 回收提示走弱化样式 */
.logs-pre { max-height: 48vh; overflow: auto; }
.logs-hint { color: var(--el-text-color-secondary); }
</style>
