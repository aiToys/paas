<script setup lang="ts">
// 产品交付面（spec §9.4 facet=delivery）：回答「代码怎么变成线上服务」。
// 三块：智能研发一条龙（差异化入口，需求文本直触 tpl-agent-ship）/ 最近运行（状态一览，
// 轮询运行中的 run，条目落 v2 简版 RunDetail）/ 深度操作（构建/镜像/发布/变更批次低频管理，并行期桥接老版）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiError } from '@/api'
import { listRuns, triggerAgentShipRun, type PipelineRun, type RunStatus } from '@/api/pipeline'

const router = useRouter()

const props = defineProps<{ appId: string }>()

// —— 最近运行（高频信号：跑没跑/过没过；运行中 5s 轮询，全终态停）——
const runs = ref<PipelineRun[]>([])
const runsLoading = ref(true)
const runsFailed = ref(false)
let timer: number | undefined

async function loadRuns() {
  try {
    runs.value = await listRuns({ appId: props.appId })
    runsFailed.value = false
  } catch {
    runsFailed.value = true
  } finally {
    runsLoading.value = false
  }
}
onMounted(async () => {
  await loadRuns()
  // 有进行中（running/paused）的 run 才轮询；页面不可见跳过；全终态停表。
  timer = window.setInterval(() => {
    if (document.hidden) return
    if (!runs.value.some((r) => r.status === 'running' || r.status === 'paused')) return
    void loadRuns()
  }, 5000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })

// 默认收 8 条，「全部 N 条」本地展开（产品内全量，不跳老版）。
const showAllRuns = ref(false)
const recentRuns = computed(() => {
  const sorted = [...runs.value].sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  return showAllRuns.value ? sorted : sorted.slice(0, 8)
})
const activeCount = computed(
  () => runs.value.filter((r) => r.status === 'running' || r.status === 'paused').length,
)

const RUN_STATUS: Record<RunStatus, { label: string; cls: string }> = {
  running: { label: '运行中', cls: 'warn' },
  paused: { label: '等待审批', cls: 'warn' },
  succeeded: { label: '成功', cls: 'ok' },
  failed: { label: '失败', cls: 'err' },
  aborted: { label: '已中止', cls: 'idle' },
}

function fmtAgo(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const m = Math.floor(Math.max(0, Date.now() - t) / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}

// —— 智能研发一条龙（2026-10-13 能力，v2 首页级入口）：需求 + 勾选段直触 ——
const shipRequest = ref('')
const shipBusy = ref(false)
// 段语义（tpl-agent-ship v2）：AI 开发恒留；审查/上线（部署+冒烟+审批+金丝雀）/发布（release+合主干）可选。
const shipReview = ref(true)
const shipShip = ref(true)
const shipRelease = ref(false)

async function runAgentShip() {
  if (!shipRequest.value.trim()) {
    ElMessage.warning('请描述你要交付的需求')
    return
  }
  if (!shipShip.value && shipRelease.value) {
    ElMessage.warning('「发布」依赖「上线」，请先勾选上线')
    return
  }
  shipBusy.value = true
  try {
    const run = await triggerAgentShipRun(props.appId, {
      request: shipRequest.value.trim(),
      review: shipReview.value,
      ship: shipShip.value,
      release: shipRelease.value,
    })
    ElMessage.success('已触发智能研发，正在跳转运行详情…')
    shipRequest.value = ''
    router.push(`/runs/${run.id}`)
    await loadRuns()
  } catch (e) {
    ElMessage.error(apiError(e, '触发失败'))
  } finally {
    shipBusy.value = false
  }
}

// 深度操作（低频管理面）：并行期桥接老版对应 tab。
const BRIDGE_LINKS = [
  { label: '变更与发车', desc: '变更登记 / 集成批次 / 火车发车', to: 'changes' },
  { label: '流水线管理', desc: '模板绑定 / 参数覆盖 / 触发器', to: 'pipelines' },
  { label: '构建与镜像', desc: '构建记录 / 镜像 digest', to: 'builds' },
  { label: '发布', desc: '版本里程碑 / 回滚 / 晋升链', to: 'releases' },
  { label: '代码仓库', desc: '仓库接入 / 分支 / PR', to: 'repositories' },
]
</script>

<template>
  <div class="delivery">
    <!-- 智能研发一条龙 -->
    <div class="card ship-card">
      <div class="card-head">
        <span class="card-title">✨ 智能研发</span>
        <span class="card-sub">描述需求 → AI 写代码 → 审查 → 上线，一条龙交付</span>
      </div>
      <textarea
        v-model="shipRequest"
        class="ship-input"
        rows="3"
        placeholder="用一句话描述要交付的需求，例如：商品详情页增加「猜你喜欢」推荐位，数据来自向量检索 top5"
      />
      <div class="ship-segs">
        <label class="seg"><input v-model="shipReview" type="checkbox" /> AI 代码审查</label>
        <label class="seg"><input v-model="shipShip" type="checkbox" /> 上线（部署测试 → 审批 → 金丝雀）</label>
        <label class="seg"><input v-model="shipRelease" type="checkbox" /> 发布（版本里程碑 + 合主干）</label>
        <button class="act primary run-btn" :disabled="shipBusy" @click="runAgentShip">
          {{ shipBusy ? '触发中…' : '开始交付' }}
        </button>
      </div>
    </div>

    <!-- 最近运行（条目落 v2 简版 RunDetail，审批/终止/重试在详情页闭环） -->
    <div class="card">
      <div class="card-head">
        <span class="card-title">最近运行</span>
        <span v-if="activeCount" class="card-live">● {{ activeCount }} 个进行中</span>
        <a v-if="runs.length > 8" class="card-more" @click="showAllRuns = !showAllRuns">
          {{ showAllRuns ? '收起' : `全部 ${runs.length} 条 ▸` }}
        </a>
      </div>
      <div v-if="runsLoading" class="loading-block" v-loading="true"></div>
      <div v-else-if="runsFailed" class="empty-hint">运行记录加载失败 <a class="retry" @click="loadRuns">重试</a></div>
      <div v-else-if="!recentRuns.length" class="empty-hint">
        还没有运行——从上方「智能研发」开始第一次交付
      </div>
      <div v-else class="run-list">
        <a
          v-for="r in recentRuns" :key="r.id"
          class="run-row"
          @click="router.push(`/runs/${r.id}`)"
        >
          <span class="r-status" :class="RUN_STATUS[r.status]?.cls">{{ RUN_STATUS[r.status]?.label ?? r.status }}</span>
          <span class="r-branch mono">{{ r.branch || '-' }}</span>
          <span class="r-trigger">{{ r.trigger }}</span>
          <span class="r-stage">阶段 {{ r.currentStage + 1 }}/{{ r.stageRuns.length }}</span>
          <span class="r-time">{{ fmtAgo(r.createdAt) }}</span>
          <span class="r-open">→</span>
        </a>
      </div>
    </div>

    <!-- 深度操作：桥接老版 -->
    <div class="card">
      <div class="card-head">
        <span class="card-title">深度操作</span>
        <span class="card-sub">低频管理面，在经典版中打开</span>
      </div>
      <div class="bridge-grid">
        <a
          v-for="l in BRIDGE_LINKS" :key="l.to"
          :href="`/console/applications/${appId}?tab=${l.to}`"
          class="bridge-item"
        >
          <span class="b-label">{{ l.label }} ↗</span>
          <span class="b-desc">{{ l.desc }}</span>
        </a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.delivery {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.card {
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
}
.card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.card-title {
  font-size: 14px;
  font-weight: 600;
}
.card-sub {
  font-size: 12px;
  color: var(--text-faint);
}
.card-live {
  margin-left: auto;
  font-size: 12px;
  color: var(--warning, #f59e0b);
}
.card-more {
  margin-left: auto;
  font-size: 12px;
  color: var(--brand);
  text-decoration: none;
}
.card-live + .card-more { margin-left: 0; }
/* 智能研发 */
.ship-input {
  width: 100%;
  box-sizing: border-box;
  margin-bottom: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
  resize: vertical;
}
.ship-input:focus {
  outline: none;
  border-color: var(--brand);
}
.ship-input::placeholder {
  color: var(--text-faint);
}
.ship-segs {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 14px;
  font-size: 12.5px;
  color: var(--text-dim);
}
.seg {
  display: flex;
  align-items: center;
  gap: 5px;
  cursor: pointer;
}
.run-btn {
  margin-left: auto;
}
.act {
  padding: 6px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12.5px;
  cursor: pointer;
}
.act.primary {
  border-color: var(--brand);
  background: var(--brand);
  color: #fff;
}
.act.primary:hover { opacity: 0.9; }
.act:disabled { opacity: 0.5; cursor: not-allowed; }
/* 运行列表 */
.loading-block { height: 120px; }
.empty-hint {
  padding: 22px 0;
  font-size: 12.5px;
  color: var(--text-faint);
  text-align: center;
}
.retry {
  color: var(--brand);
  cursor: pointer;
}
.run-list {
  display: flex;
  flex-direction: column;
}
.run-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 6px;
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
  color: var(--text);
  text-decoration: none;
}
.run-row:last-child { border-bottom: none; }
.run-row:hover { background: var(--surface-2); }
.r-status { width: 64px; flex-shrink: 0; font-weight: 600; }
.r-status.ok { color: var(--success, #16a34a); }
.r-status.warn { color: var(--warning, #f59e0b); }
.r-status.err { color: var(--danger, #f43f5e); }
.r-status.idle { color: var(--text-faint); }
.r-branch {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.r-trigger {
  flex-shrink: 0;
  color: var(--text-faint);
  font-size: 11.5px;
}
.r-stage {
  flex-shrink: 0;
  color: var(--text-dim);
  font-size: 11.5px;
}
.r-time {
  margin-left: auto;
  flex-shrink: 0;
  color: var(--text-faint);
  font-size: 11.5px;
}
.r-open {
  flex-shrink: 0;
  color: var(--brand);
  font-size: 11px;
}
/* 桥接 */
.bridge-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
}
.bridge-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  text-decoration: none;
  transition: border-color 0.12s, background 0.12s;
}
.bridge-item:hover {
  border-color: var(--border-strong);
  background: var(--surface-2);
}
.b-label {
  font-size: 13px;
  color: var(--brand);
}
.b-desc {
  font-size: 11.5px;
  color: var(--text-faint);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
