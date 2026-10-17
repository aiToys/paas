<script setup lang="ts">
// 流水线运行详情（spec 2026-10-17 §四）：
// - 头卡 + 等待决策横幅（按当前 stage 类型给正确动作：manual-test/approve/canary 三种，
//   决策带理由落 run.Decisions——修复「所有暂停都叫通过审批」的误导）
// - graph/steps/timing 三视图，URL ?view=&stage= 保留定位（视图/选中切换不入历史）
// - 阶段详情四 tab：结果（Output 结构化 + Checks 质量证据）/ 日志 / 输入输出 / 关联资源深链
// - canary 面板：基线/候选 digest（run.artifact 锁定产物）/ 验证域名
// - 轮询治理：请求序号防竞态 + 同路由切 runId 重载 + 终态停止
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  buildRunIdOf, getRun, abortRun, retryRun, decideStage, canaryDecision,
  type PipelineRun, type StageRun,
} from '@/api/pipeline'
import { statusOf, tagTypeToCls, RUN_STATUS, STAGE_STATUS, waitingLabel } from '@/composables/useStatus'
import { apiError } from '@/api'

const route = useRoute()
const router = useRouter()

const run = ref<PipelineRun | null>(null)
const loading = ref(true)
const notFound = ref(false)
const acting = ref('')
const rejectDlg = ref(false) // 拒绝理由对话框（manual-test/approve reject 共用）
const rejectReason = ref('')

let timer: number | undefined
let seq = 0 // 请求序号：慢响应回来时若已过期（切换 run/卸载）直接丢弃，防旧数据写穿
const ACTIVE = new Set(['pending', 'running', 'paused'])

// —— 视图与选中（URL 持久化；replace 避免浏览历史噪声）——
type View = 'graph' | 'steps' | 'timing'
const view = computed<View>(() => {
  const v = route.query.view
  return v === 'steps' || v === 'timing' ? v : 'graph'
})
const selectedIdx = computed<number>(() => {
  const n = Number(route.query.stage)
  return Number.isInteger(n) && n >= 0 && n < (run.value?.stageRuns.length ?? 0) ? n
    : run.value?.stageRuns.find((s) => s.status === 'failed' || s.status === 'waiting')?.index ?? 0
})
function setView(v: View) { void router.replace({ query: { ...route.query, view: v } }) }
function selectStage(i: number) { void router.replace({ query: { ...route.query, stage: String(i) } }) }

async function load() {
  const my = ++seq
  try {
    const r = await getRun(route.params.runId as string)
    if (my !== seq) return // 已切走：丢弃过期响应
    run.value = r
    document.title = `运行 ${r.id.slice(0, 12)} · 控制台`
  } catch (e) {
    if (my !== seq) return
    const msg = apiError(e)
    if (msg.includes('not found') || msg.includes('404')) notFound.value = true
    else ElMessage.error(apiError(e, '加载运行失败'))
  } finally {
    if (my === seq) loading.value = false
  }
}
function schedule() {
  if (timer) { window.clearInterval(timer); timer = undefined }
  if (run.value && ACTIVE.has(run.value.status)) {
    timer = window.setInterval(() => { if (!document.hidden) void load() }, 5000)
  }
}
onMounted(async () => { await load(); schedule() })
onUnmounted(() => { if (timer) window.clearInterval(timer); seq++ })
// 同路由不同 runId（列表互链）重载并重置终态残留。
watch(() => route.params.runId, async () => {
  if (timer) window.clearInterval(timer)
  loading.value = true; notFound.value = false; run.value = null; seq++
  await load(); schedule()
})

const runMeta = computed(() => statusOf(RUN_STATUS, run.value?.status ?? ''))
function stageMeta(s: StageRun) { return statusOf(STAGE_STATUS, s.status) }
function stageLabel(s: StageRun): string {
  return s.status === 'waiting' ? waitingLabel(s) : statusOf(STAGE_STATUS, s.status).label
}
function fmtDuration(s: StageRun): string {
  if (!s.startedAt) return '—'
  const end = s.finishedAt ? new Date(s.finishedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - new Date(s.startedAt).getTime()) / 1000))
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

// 当前等待决策的 stage（paused 才有；按类型派生动作组）。
const waitingStage = computed<StageRun | null>(() => {
  if (!run.value || run.value.status !== 'paused') return null
  return run.value.stageRuns[run.value.currentStage] ?? null
})
type DecisionKind = 'manual-test' | 'approve' | 'canary' | null
const decisionKind = computed<DecisionKind>(() => {
  const s = waitingStage.value
  if (!s) return null
  if (s.type === 'canary') return 'canary'
  if (s.type === 'approve') return 'approve'
  if (s.type === 'test' && s.input?.mode === 'manual') return 'manual-test'
  return null
})
const DECISION_COPY: Record<string, { title: string; confirm: string; reject?: string; hint: string }> = {
  'manual-test': { title: '等待人工确认测试结果', confirm: '确认测试通过', reject: '测试不通过', hint: '确认前请核对该部署的验证地址与检查记录；不通过将终止本次交付并记录理由。' },
  approve: { title: '等待上线审批', confirm: '批准上线', reject: '拒绝上线', hint: '批准后将按锁定候选产物继续执行（金丝雀验证 → 正式发布）；拒绝将中止运行。' },
  canary: { title: '等待金丝雀放量决策', confirm: '确认放量', reject: '终止验证', hint: '放量=候选滚动到基线全量；终止=仅删除金丝雀并行负载，基线零风险退出。' },
}

// —— 决策动作（spec §7 decisions 端点；canary 走专属两段锁端点）——
async function doConfirm() {
  if (!run.value || !waitingStage.value) return
  const kind = decisionKind.value
  acting.value = 'confirm'
  try {
    if (kind === 'canary') {
      await canaryDecision(run.value.id, waitingStage.value.index, 'promote')
      ElMessage.success('已确认放量，基线滚动更新中')
    } else {
      await decideStage(run.value.id, waitingStage.value.index, 'confirm')
      ElMessage.success(kind === 'approve' ? '已批准上线' : '已确认测试通过')
    }
    await load(); schedule()
  } catch (e) {
    ElMessage.error(apiError(e, '决策失败'))
  } finally { acting.value = '' }
}
async function doReject() {
  if (!run.value || !waitingStage.value) return
  const kind = decisionKind.value
  acting.value = 'reject'
  try {
    if (kind === 'canary') {
      await canaryDecision(run.value.id, waitingStage.value.index, 'terminate')
      ElMessage.success('已终止验证（基线未受影响）')
    } else {
      await decideStage(run.value.id, waitingStage.value.index, 'reject', rejectReason.value.trim() || undefined)
      ElMessage.success(kind === 'approve' ? '已拒绝上线，运行中止' : '已记录测试不通过')
    }
    rejectDlg.value = false; rejectReason.value = ''
    await load(); schedule()
  } catch (e) {
    ElMessage.error(apiError(e, '决策失败'))
  } finally { acting.value = '' }
}
function openReject() { rejectReason.value = ''; rejectDlg.value = true }
async function doAbort() {
  if (!run.value) return
  acting.value = 'abort'
  try {
    await abortRun(run.value.id)
    ElMessage.success('已终止运行')
    await load(); schedule()
  } catch (e) { ElMessage.error(apiError(e, '终止失败')) } finally { acting.value = '' }
}
async function doRetry() {
  if (!run.value) return
  acting.value = 'retry'
  try {
    await retryRun(run.value.id)
    ElMessage.success('已重试，从失败阶段续跑（沿用锁定产物）')
    await load(); schedule()
  } catch (e) { ElMessage.error(apiError(e, '重试失败')) } finally { acting.value = '' }
}

// —— 阶段详情：Output 结构化行（含深链）+ Checks ——
interface OutRow { label: string; value: string; to?: string; href?: string }
function outputRows(s: StageRun): OutRow[] {
  const out = s.output ?? {}
  const rows: OutRow[] = []
  const str = (k: string) => { const v = out[k]; return typeof v === 'string' ? v : '' }
  const put = (label: string, k: string, opt: Partial<OutRow> = {}) => {
    const v = str(k)
    if (v) rows.push({ label, value: v, ...opt })
  }
  put('镜像 ID', 'imageId')
  put('发布记录', 'releaseId', { to: '/releases/' + str('releaseId') })
  put('工作负载', 'workloadId', { to: '/workloads' })
  put('访问地址', 'workloadDomain', { href: domainHref(str('workloadDomain')) })
  put('金丝雀负载', 'canaryWorkloadId', { to: '/workloads' })
  put('金丝雀验证地址', 'canaryDomain', { href: domainHref(str('canaryDomain')) })
  put('产出分支', 'branch')
  put('Agent 运行', 'runName', { to: '/ai/runs/' + encodeURIComponent(str('runName')) })
  put('版本号', 'version')
  put('合并提交', 'mergeSha')
  put('探活地址', 'url', { href: domainHref(str('url')) })
  return rows
}
function domainHref(d: string): string | undefined {
  if (!d || d.includes('.svc.')) return undefined // 集群内地址外链无意义
  return d.startsWith('http') ? d : `http://${d}`
}
// 关联资源 tab：output 深链 + buildRun 全量日志 + 锁定产物。
const relatedRows = computed<OutRow[]>(() => {
  const s = run.value?.stageRuns[selectedIdx.value]
  if (!s) return []
  const rows = outputRows(s)
  const br = buildRunIdOf(s)
  if (br) rows.push({ label: '完整构建日志', value: br, to: '/builds/' + br })
  return rows
})

const artifact = computed(() => run.value?.artifact ?? null)
const canaryPanel = computed<StageRun | null>(() => {
  // canary 面板：等待决策时展示当前 canary stage；否则展示最近一个 canary stage 的产出
  if (decisionKind.value === 'canary') return waitingStage.value
  const stages = run.value?.stageRuns ?? []
  for (let i = stages.length - 1; i >= 0; i--) {
    if (stages[i]!.type === 'canary') return stages[i]
  }
  return null
})

// timing 视图数据：各 stage 起止（未开始无条）。
function stageOffsetPct(s: StageRun, total: number): number {
  if (!s.startedAt || !total) return 0
  return Math.min(100, (new Date(s.startedAt).getTime() - runStart()) / total * 100)
}
function stageWidthPct(s: StageRun, total: number): number {
  if (!s.startedAt || !total) return 0
  const end = s.finishedAt ? new Date(s.finishedAt).getTime() : Date.now()
  return Math.max(0.5, Math.min(100, (end - new Date(s.startedAt).getTime()) / total * 100))
}
function runStart(): number {
  const first = run.value?.stageRuns.find((s) => s.startedAt)
  return first ? new Date(first.startedAt!).getTime() : Date.now()
}
const totalSpan = computed(() => {
  const r = run.value
  if (!r) return 0
  const end = r.finishedAt ? new Date(r.finishedAt).getTime() : Date.now()
  return Math.max(1, end - runStart())
})
</script>

<template>
  <div class="run-page">
    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载运行…"></div>

    <div v-else-if="notFound" class="empty">
      <p>运行不存在或已清理</p>
      <a class="ghost-btn" @click="router.push('/')">← 返回首页</a>
    </div>

    <template v-else-if="run">
      <div class="crumb">
        <a v-if="run.appId" @click="router.push(`/products/${run.appId}?tab=delivery`)">← 产品交付面</a>
        <a v-else @click="router.push('/')">← 首页</a>
        <span>/</span><span class="mono run-id">{{ run.id }}</span>
      </div>

      <!-- 头卡：元信息 + 锁定产物 + 全局操作 -->
      <div class="head-card">
        <div class="head-main">
          <span class="r-status" :class="tagTypeToCls(runMeta.type)">{{ runMeta.label }}</span>
          <span class="h-kv">分支 <b class="mono">{{ run.branch }}</b></span>
          <span class="h-kv">触发 <b>{{ run.trigger }}</b></span>
          <span v-if="run.version" class="h-kv">版本 <b class="mono">{{ run.version }}</b></span>
          <span v-if="run.templateId" class="h-kv">模板 <b class="mono">{{ run.templateId }}<template v-if="run.templateVersion"> v{{ run.templateVersion }}</template></b></span>
        </div>
        <div class="head-acts">
          <button v-if="ACTIVE.has(run.status)" class="act danger" :disabled="acting === 'abort'" @click="doAbort">
            {{ acting === 'abort' ? '终止中…' : '终止' }}
          </button>
          <button v-if="run.status === 'failed' || run.status === 'aborted'" class="act" :disabled="acting === 'retry'" @click="doRetry">
            {{ acting === 'retry' ? '重试中…' : '重试' }}
          </button>
        </div>
      </div>

      <!-- 锁定产物（spec §6）：触发时冻结的候选五元组 -->
      <div v-if="artifact" class="artifact-card">
        <span class="a-tag">锁定产物</span>
        <span class="h-kv">镜像 <b class="mono">{{ artifact.imageId }}</b></span>
        <span v-if="artifact.digest" class="h-kv">digest <b class="mono">{{ artifact.digest.slice(0, 19) }}…</b></span>
        <span v-if="artifact.commit" class="h-kv">commit <b class="mono">{{ artifact.commit.slice(0, 8) }}</b></span>
        <span v-if="artifact.buildRunId" class="h-kv"><RouterLink class="mini-link" :to="'/builds/' + artifact.buildRunId">来源构建 →</RouterLink></span>
        <span class="a-hint">审批/重试/放量均引用此快照，审批等待期间的新构建不改变实际发布产物</span>
      </div>

      <!-- 等待决策横幅（阻塞置顶 + 证据直达 + 按类型正确文案） -->
      <div v-if="decisionKind" class="wait-card">
        <div class="wait-main">
          <div class="wait-title">{{ DECISION_COPY[decisionKind].title }}<span class="wait-stage">（阶段 {{ waitingStage!.index + 1 }} · {{ waitingStage!.name }}）</span></div>
          <div class="wait-hint">{{ DECISION_COPY[decisionKind].hint }}</div>
          <div v-if="decisionKind === 'canary' && canaryPanel" class="wait-links">
            <a v-if="domainHref(String(canaryPanel.output?.canaryDomain ?? ''))" :href="domainHref(String(canaryPanel.output?.canaryDomain))" target="_blank" rel="noopener" class="mini-link">打开验证地址 ↗</a>
            <RouterLink to="/observability" class="mini-link">查看观测指标 →</RouterLink>
          </div>
          <div v-else class="wait-links">
            <button class="mini-btn" @click="selectStage(Math.max(0, (waitingStage!.index) - 1))">查看验证证据（上一阶段结果）</button>
          </div>
        </div>
        <div class="wait-acts">
          <button class="act primary" :disabled="acting === 'confirm'" @click="doConfirm">
            {{ acting === 'confirm' ? '处理中…' : DECISION_COPY[decisionKind].confirm }}
          </button>
          <button v-if="DECISION_COPY[decisionKind].reject" class="act danger-plain" :disabled="acting === 'reject'" @click="decisionKind === 'canary' ? doReject() : openReject()">
            {{ DECISION_COPY[decisionKind].reject }}
          </button>
        </div>
      </div>

      <!-- canary 面板：基线不动 / 候选并行 / 验证地址 -->
      <div v-if="canaryPanel && canaryPanel.status !== 'pending'" class="canary-card">
        <div class="canary-title">金丝雀并行验证</div>
        <div class="canary-grid">
          <div class="c-cell"><span class="c-k">候选镜像</span><b class="mono">{{ canaryPanel.output?.canaryWorkloadId ? (artifact?.imageId ?? '（前序产物）') : '—' }}</b></div>
          <div class="c-cell">
<span class="c-k">验证地址</span>
            <a v-if="domainHref(String(canaryPanel.output?.canaryDomain ?? ''))" :href="domainHref(String(canaryPanel.output?.canaryDomain))" target="_blank" rel="noopener" class="mini-link mono">{{ canaryPanel.output?.canaryDomain }}</a>
            <b v-else class="mono dim">部署后生成</b>
          </div>
          <div class="c-cell">
<span class="c-k">部署负载</span>
            <RouterLink v-if="canaryPanel.output?.canaryWorkloadId" to="/workloads" class="mini-link mono">{{ canaryPanel.output.canaryWorkloadId }}</RouterLink>
            <b v-else class="dim">—</b>
          </div>
          <div class="c-cell"><span class="c-k">基线</span><span class="dim">不受影响（并行泳道 1 副本，放量才滚动基线）</span></div>
        </div>
      </div>

      <!-- 视图切换 -->
      <div class="view-bar">
        <div class="view-switch">
          <button v-for="v in (['graph', 'steps', 'timing'] as const)" :key="v" class="v-btn" :class="{ on: view === v }" @click="setView(v)">
            {{ v === 'graph' ? '流程' : v === 'steps' ? '步骤' : '耗时' }}
          </button>
        </div>
      </div>

      <!-- graph：横向节点流 -->
      <div v-if="view === 'graph'" class="graph-scroll" data-table-scroll tabindex="0">
        <div class="graph">
          <template v-for="(s, i) in run.stageRuns" :key="s.index">
            <button class="node" :class="{ sel: i === selectedIdx, cur: i === run.currentStage && ACTIVE.has(run.status) }" @click="selectStage(i)">
              <span class="n-dot" :class="tagTypeToCls(stageMeta(s).type)"></span>
              <span class="n-body">
                <span class="n-name">{{ s.name }}</span>
                <span class="n-status">{{ stageLabel(s) }}<template v-if="s.startedAt"> · {{ fmtDuration(s) }}</template></span>
              </span>
            </button>
            <span v-if="i < run.stageRuns.length - 1" class="n-arrow" :class="{ done: s.status === 'success' || s.status === 'succeeded' }">→</span>
          </template>
        </div>
      </div>

      <!-- steps：表格 -->
      <div v-else-if="view === 'steps'" class="table-scroll" data-table-scroll tabindex="0">
        <table class="stage-table">
          <thead>
            <tr>
              <th class="col-idx">#</th><th>阶段</th><th>状态</th><th>耗时</th><th>说明</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in run.stageRuns" :key="s.index" class="clickable" :class="{ sel: s.index === selectedIdx }" @click="selectStage(s.index)">
              <td class="col-idx mono">{{ s.index + 1 }}</td>
              <td><span class="s-name">{{ s.name }}</span><span class="s-type">{{ s.type }}</span></td>
              <td><span class="status" :class="tagTypeToCls(stageMeta(s).type)">{{ stageLabel(s) }}</span></td>
              <td class="mono">{{ fmtDuration(s) }}</td>
              <td class="s-note">
                <span v-if="s.error" class="err-text">{{ s.error }}</span>
                <span v-else-if="s.output?.branch" class="mono dim">→ {{ s.output.branch }}</span>
                <span v-else-if="s.status === 'waiting'" class="dim">{{ waitingLabel(s) }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- timing：甘特横条 -->
      <div v-else class="timing-card">
        <div v-for="s in run.stageRuns" :key="s.index" class="t-row" :class="{ sel: s.index === selectedIdx }" @click="selectStage(s.index)">
          <span class="t-name">{{ s.index + 1 }}. {{ s.name }}</span>
          <div class="t-track">
            <div
class="t-bar" :class="tagTypeToCls(stageMeta(s).type)"
                 :style="{ marginLeft: stageOffsetPct(s, totalSpan) + '%', width: stageWidthPct(s, totalSpan) + '%' }"
></div>
          </div>
          <span class="t-dur mono">{{ fmtDuration(s) }}</span>
        </div>
        <p class="t-note">等待决策的阶段耗时含人工等待时间（未开始的阶段无条）；无排队时间字段的历史运行不虚构排队。</p>
      </div>

      <!-- 阶段详情：结果 / 日志 / 输入输出 / 关联资源 -->
      <div v-if="run.stageRuns[selectedIdx]" class="detail-card">
        <div class="d-head">
          <span class="d-title">阶段 {{ selectedIdx + 1 }} · {{ run.stageRuns[selectedIdx].name }}</span>
          <span class="status" :class="tagTypeToCls(stageMeta(run.stageRuns[selectedIdx]).type)">{{ stageLabel(run.stageRuns[selectedIdx]) }}</span>
        </div>
        <el-tabs value="result">
          <el-tab-pane label="结果" name="result">
            <div v-if="run.stageRuns[selectedIdx].checks?.length" class="checks">
              <div class="sub-title">质量检查</div>
              <table class="mini-table">
                <tbody>
                  <tr v-for="(c, ci) in run.stageRuns[selectedIdx].checks" :key="ci">
                    <td><span class="status" :class="c.status === 'pass' ? 'ok' : c.status === 'fail' ? 'err' : 'idle'">{{ c.status === 'pass' ? '通过' : c.status === 'fail' ? '未通过' : '未知' }}</span></td>
                    <td>{{ c.type === 'smoke' ? 'HTTP 探活' : c.type === 'manual' ? '人工确认' : c.type }}</td>
                    <td class="mono dim">{{ c.target }}</td>
                    <td class="dim">预期 {{ c.expected ?? '—' }} / 实际 {{ c.actual ?? '—' }}</td>
                    <td v-if="c.note" class="dim">{{ c.note }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="outputRows(run.stageRuns[selectedIdx]).length" class="outs">
              <div class="sub-title">产出</div>
              <div v-for="row in outputRows(run.stageRuns[selectedIdx])" :key="row.label" class="kv-row">
                <span class="k">{{ row.label }}</span>
                <RouterLink v-if="row.to" :to="row.to" class="mini-link mono">{{ row.value }}</RouterLink>
                <a v-else-if="row.href" :href="row.href" target="_blank" rel="noopener" class="mini-link mono">{{ row.value }} ↗</a>
                <b v-else class="mono">{{ row.value }}</b>
              </div>
            </div>
            <p v-if="!run.stageRuns[selectedIdx].checks?.length && !outputRows(run.stageRuns[selectedIdx]).length" class="dim d-empty">
              该阶段暂无结构化结果（未执行或无产出）。
            </p>
            <div v-if="run.stageRuns[selectedIdx].error" class="stage-err">{{ run.stageRuns[selectedIdx].error }}</div>
          </el-tab-pane>
          <el-tab-pane label="日志" name="log">
            <RouterLink v-if="buildRunIdOf(run.stageRuns[selectedIdx])" :to="'/builds/' + buildRunIdOf(run.stageRuns[selectedIdx])" class="text-button">打开完整构建日志 →</RouterLink>
            <pre v-if="run.stageRuns[selectedIdx].log" class="log-pre mono">{{ run.stageRuns[selectedIdx].log }}</pre>
            <p v-else class="log-empty">该阶段暂无事件日志。</p>
          </el-tab-pane>
          <el-tab-pane label="输入输出" name="io">
            <div class="io-grid">
              <div><div class="sub-title">输入（触发时固化）</div><pre class="io-pre mono">{{ JSON.stringify(run.stageRuns[selectedIdx].input ?? {}, null, 2) }}</pre></div>
              <div><div class="sub-title">输出</div><pre class="io-pre mono">{{ JSON.stringify(run.stageRuns[selectedIdx].output ?? {}, null, 2) }}</pre></div>
            </div>
          </el-tab-pane>
          <el-tab-pane label="关联资源" name="related">
            <div v-if="relatedRows.length">
              <div v-for="row in relatedRows" :key="row.label" class="kv-row">
                <span class="k">{{ row.label }}</span>
                <RouterLink v-if="row.to" :to="row.to" class="mini-link mono">{{ row.value }}</RouterLink>
                <a v-else-if="row.href" :href="row.href" target="_blank" rel="noopener" class="mini-link mono">{{ row.value }} ↗</a>
                <b v-else class="mono">{{ row.value }}</b>
              </div>
            </div>
            <p v-else class="dim d-empty">无关联资源。</p>
          </el-tab-pane>
        </el-tabs>
      </div>

      <!-- 决策时间线（结构化记录，非伪造事件） -->
      <div v-if="run.decisions?.length" class="detail-card">
        <div class="d-head"><span class="d-title">决策记录</span></div>
        <div class="dec-list">
          <div v-for="(d, i) in run.decisions" :key="i" class="dec-row">
            <span class="status" :class="d.action === 'reject' || d.action === 'terminate' ? 'idle' : 'ok'">
              {{ d.type === 'manual-test' ? '人工测试' : d.type === 'approve' ? '审批' : '金丝雀' }}·{{ d.action === 'confirm' ? '通过' : d.action === 'reject' ? '拒绝' : d.action }}
            </span>
            <span class="dim">阶段 {{ d.stageIndex + 1 }}</span>
            <span v-if="d.actor" class="dim">by {{ d.actor }}</span>
            <span class="mono dim">{{ new Date(d.at).toLocaleString() }}</span>
            <span v-if="d.reason" class="dec-reason">「{{ d.reason }}」</span>
          </div>
        </div>
      </div>
    </template>

    <!-- 拒绝理由对话框（manual-test/approve 拒绝路径） -->
    <el-dialog v-model="rejectDlg" title="拒绝理由" width="440px">
      <p class="dim" style="margin-top: 0">理由将记录到运行详情与审计日志，帮助交付成员理解拒绝原因。</p>
      <el-input v-model="rejectReason" type="textarea" :rows="3" placeholder="如：验证地址下单接口返回 500" />
      <template #footer>
        <el-button @click="rejectDlg = false">取消</el-button>
        <el-button type="danger" :loading="acting === 'reject'" @click="doReject">确认拒绝</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.run-page { display: flex; flex-direction: column; gap: 14px; }
.loading-block { height: 240px; }
.crumb { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--text-faint); }
.crumb a { color: var(--brand); cursor: pointer; }
.run-id { font-size: 12px; }
.empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 64px 0; color: var(--text-faint); }
.ghost-btn { padding: 7px 14px; border: 1px solid var(--border-strong); border-radius: var(--radius); color: var(--text); font-size: 13px; cursor: pointer; }

.head-card { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.head-main { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; min-width: 0; }
.h-kv { font-size: 12.5px; color: var(--text-faint); }
.h-kv b { color: var(--text); font-weight: 500; }
.head-acts { display: flex; gap: 8px; flex-shrink: 0; }
.r-status { padding: 2px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
.r-status.ok { background: rgba(16,185,129,.12); color: #059669; }
.r-status.warn { background: rgba(245,158,11,.14); color: #b45309; }
.r-status.err { background: rgba(244,63,94,.12); color: #e11d48; }
.r-status.idle { background: rgba(100,116,139,.12); color: #64748b; }

.artifact-card { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; padding: 10px 16px; background: var(--surface); border: 1px dashed var(--border-strong); border-radius: var(--radius); font-size: 12.5px; }
.a-tag { padding: 1px 8px; border-radius: 4px; background: rgba(59,130,246,.1); color: var(--brand); font-size: 11.5px; font-weight: 600; }
.a-hint { color: var(--text-faint); font-size: 12px; }

.wait-card { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding: 14px 16px; background: rgba(245,158,11,.06); border: 1px solid rgba(245,158,11,.35); border-radius: var(--radius); }
.wait-title { font-weight: 600; font-size: 14px; }
.wait-stage { color: var(--text-faint); font-weight: 400; font-size: 12.5px; }
.wait-hint { margin-top: 4px; color: var(--text-dim); font-size: 12.5px; max-width: 640px; }
.wait-links { margin-top: 8px; display: flex; gap: 12px; flex-wrap: wrap; }
.wait-acts { display: flex; gap: 8px; flex-shrink: 0; align-items: center; }

.act { padding: 7px 14px; border: 1px solid var(--border-strong); border-radius: var(--radius); background: var(--surface); color: var(--text); font-size: 13px; cursor: pointer; }
.act:hover { background: var(--surface-2); }
.act.primary { background: var(--brand); border-color: var(--brand); color: #fff; }
.act.primary:hover { opacity: .9; }
.act.danger { background: transparent; border-color: rgba(244,63,94,.5); color: #e11d48; }
.act.danger-plain { background: transparent; border-color: rgba(244,63,94,.5); color: #e11d48; }
.act:disabled { opacity: .55; cursor: not-allowed; }
.mini-btn { padding: 0; border: none; background: none; color: var(--brand); font-size: 12.5px; cursor: pointer; }
.mini-link { color: var(--brand); font-size: 12.5px; text-decoration: none; }

.canary-card { padding: 12px 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.canary-title { font-weight: 600; font-size: 13px; margin-bottom: 8px; }
.canary-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 10px; }
.c-cell { display: flex; flex-direction: column; gap: 3px; font-size: 12.5px; }
.c-k { color: var(--text-faint); font-size: 11.5px; }

.view-bar { display: flex; justify-content: flex-end; }
.view-switch { display: inline-flex; border: 1px solid var(--border); border-radius: var(--radius); overflow: hidden; }
.v-btn { padding: 5px 14px; border: none; background: var(--surface); color: var(--text-faint); font-size: 12.5px; cursor: pointer; }
.v-btn + .v-btn { border-left: 1px solid var(--border); }
.v-btn.on { background: var(--surface-2); color: var(--brand); font-weight: 600; }

.graph-scroll { overflow-x: auto; padding-bottom: 4px; }
.graph { display: flex; align-items: stretch; gap: 6px; min-width: min-content; }
.node { display: flex; align-items: center; gap: 8px; padding: 9px 12px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); cursor: pointer; text-align: left; }
.node:hover { background: var(--surface-2); }
.node.sel { border-color: var(--brand); box-shadow: 0 0 0 1px var(--brand); }
.node.cur { border-color: rgba(245,158,11,.55); }
.n-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.n-dot.ok { background: #10b981; } .n-dot.warn { background: #f59e0b; } .n-dot.err { background: #f43f5e; } .n-dot.idle { background: #94a3b8; }
.n-body { display: flex; flex-direction: column; gap: 1px; }
.n-name { font-size: 13px; font-weight: 600; color: var(--text); white-space: nowrap; }
.n-status { font-size: 11.5px; color: var(--text-faint); white-space: nowrap; }
.n-arrow { align-self: center; color: var(--text-faint); font-size: 13px; }
.n-arrow.done { color: #10b981; }

.table-scroll { overflow-x: auto; }
.stage-table { width: 100%; border-collapse: collapse; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.stage-table th { text-align: left; padding: 8px 10px; color: var(--text-faint); font-weight: 500; font-size: 12px; border-bottom: 1px solid var(--border); background: var(--surface-2); }
.stage-table td { padding: 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
.stage-table tr.clickable { cursor: pointer; }
.stage-table tr.clickable:hover td, .stage-table tr.sel td { background: var(--surface-2); }
.col-idx { width: 36px; color: var(--text-faint); }
.s-name { font-weight: 600; margin-right: 8px; }
.s-type { padding: 1px 6px; border: 1px solid var(--border); border-radius: 3px; font-size: 10.5px; color: var(--text-faint); }
.err-text { color: var(--danger, #f43f5e); font-size: 12px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.dim { color: var(--text-dim); font-size: 12px; }

.timing-card { padding: 14px 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); display: flex; flex-direction: column; gap: 8px; }
.t-row { display: grid; grid-template-columns: 160px 1fr 64px; align-items: center; gap: 12px; padding: 4px 6px; border-radius: 6px; cursor: pointer; }
.t-row:hover, .t-row.sel { background: var(--surface-2); }
.t-name { font-size: 12.5px; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.t-track { position: relative; height: 14px; background: var(--surface-2); border-radius: 4px; overflow: hidden; }
.t-bar { position: absolute; top: 2px; bottom: 2px; border-radius: 3px; min-width: 3px; }
.t-bar.ok { background: #10b981; } .t-bar.warn { background: #f59e0b; } .t-bar.err { background: #f43f5e; } .t-bar.idle { background: #cbd5e1; }
.t-dur { font-size: 12px; color: var(--text-faint); text-align: right; }
.t-note { margin: 4px 0 0; font-size: 11.5px; color: var(--text-faint); }

.detail-card { padding: 14px 16px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.d-head { display: flex; align-items: center; gap: 10px; margin-bottom: 6px; }
.d-title { font-weight: 600; font-size: 13.5px; }
.d-empty { margin: 8px 0; }
.sub-title { font-size: 12px; color: var(--text-faint); margin: 10px 0 6px; }
.mini-table { width: 100%; border-collapse: collapse; }
.mini-table td { padding: 6px 8px; border-bottom: 1px solid var(--border); font-size: 12.5px; }
.kv-row { display: flex; align-items: baseline; gap: 10px; padding: 4px 0; font-size: 12.5px; }
.kv-row .k { color: var(--text-faint); min-width: 96px; }
.outs .kv-row b { word-break: break-all; }
.status { padding: 1px 8px; border-radius: 999px; font-size: 11.5px; }
.status.ok { background: rgba(16,185,129,.12); color: #059669; }
.status.warn { background: rgba(245,158,11,.14); color: #b45309; }
.status.err { background: rgba(244,63,94,.12); color: #e11d48; }
.status.idle { background: rgba(100,116,139,.12); color: #64748b; }
.stage-err { margin-top: 10px; padding: 10px 12px; background: rgba(244,63,94,.06); border: 1px solid rgba(244,63,94,.3); border-radius: 6px; color: #be123c; font-size: 12.5px; word-break: break-all; }
.log-pre { max-height: 320px; overflow: auto; margin: 0; padding: 12px; background: var(--surface-2); border-radius: 6px; font-size: 12px; line-height: 1.55; white-space: pre-wrap; word-break: break-all; color: var(--text); }
.log-empty { margin: 0; padding: 12px; font-size: 12px; color: var(--text-faint); }
.io-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 768px) { .io-grid { grid-template-columns: 1fr; } }
.io-pre { max-height: 260px; overflow: auto; margin: 0; padding: 10px; background: var(--surface-2); border-radius: 6px; font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-all; }
.dec-list { display: flex; flex-direction: column; gap: 6px; }
.dec-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12.5px; }
.dec-reason { color: var(--text-dim); }
.text-button { color: var(--brand); font-size: 12.5px; text-decoration: none; display: inline-block; margin-bottom: 8px; }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
