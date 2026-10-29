<script setup lang="ts">
// 诊断报告卡（spec §7.1 用户侧投影）：结论 + 假设（evidenceIds 可点开证据片段）+
// 建议（站内白名单跳转）+ 数据源覆盖 + 质量与用量口径（estimated 不冒充 provider）。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import { AssistantRun, Evidence, qualityLabel } from '@/api/assistant'
import { useAssistantStore } from '@/stores/assistant'

const props = defineProps<{ run: AssistantRun; workloadId?: string }>()
const router = useRouter()
const store = useAssistantStore()
const expandedEv = ref<string | null>(null)

const report = computed(() => props.run.report)
const evidenceById = computed(() => {
  const m: Record<string, Evidence | undefined> = {}
  for (const e of props.run.evidence || []) m[e.id] = e
  return m
})
const coverage = computed(() => {
  // 由证据推导源覆盖（与后端 Coverage 同口径的前端只读视图）。
  const src: Record<string, string> = {}
  const srcOf: Record<string, string> = {
    get_workload_status: 'K8s 状态', get_workload_spec: '部署配置', get_k8s_events: 'K8s 事件',
    get_pod_logs: '实例日志', query_loki: '日志检索', get_pod_metrics: '资源指标', get_recent_release: '发布记录',
  }
  for (const e of props.run.evidence || []) {
    const s = srcOf[e.tool]; if (!s) continue
    if (!(s in src) || (src[s] !== "error" && (src[s] === "empty" || e.status === "error"))) src[s] = e.status
  }
  if (report.value?.dataSourceCoverage?.traces) src["链路（隔离校验）"] = report.value.dataSourceCoverage.traces.split("/")[0]
 return src
})
const severityLabel: Record<string, string> = { critical: '严重', warning: '警告', info: '提示' }
const statusChip: Record<string, string> = { ok: '有数据', empty: '查询无数据', missing: '源未覆盖', error: '查询失败' }

function confidenceTier(c: number) {
  if (c >= 0.7) return '较高'
  if (c >= 0.4) return '中等'
  return '较低'
}

// destination 白名单 → 站内深链（不执行 LLM 给的任意 URL）。有 workloadId 时直达
// 该负载对应分区，否则退回全局列表页。跳转前关助手面板——面板是全屏遮罩，
// 不关的话导航发生在遮罩下面，用户看不见（「点了没反应」的根因）。
function go(dest?: string) {
  const wl = props.workloadId
  const dests: Record<string, string> = {
    workload_logs: wl ? `/workloads/${wl}?view=logs` : '/workloads',
    workload_instances: wl ? `/workloads/${wl}?view=instances` : '/workloads',
    workload_metrics: wl ? `/workloads/${wl}` : '/workloads',
    workload_spec: wl ? `/workloads/${wl}` : '/workloads',
    releases: '/delivery',
  }
  const to = dest ? dests[dest] : ''
  if (!to) return
  store.closePanel()
  router.push(to)
}
</script>

<template>
  <article v-if="report" class="report" :data-quality="run.quality">
    <header class="verdict" :class="report.verdict.severity">
      <span class="sev">{{ severityLabel[report.verdict.severity] || '提示' }}</span>
      <b>{{ report.verdict.summary || '（无结论摘要）' }}</b>
      <span class="quality" :title="'报告质量：' + (qualityLabel[run.quality || ''] || run.quality)">{{ qualityLabel[run.quality || ''] || run.quality }}</span>
    </header>

    <section v-if="report.hypotheses?.length" class="hyps">
      <h4>根因假设</h4>
      <div v-for="(h, i) in report.hypotheses" :key="i" class="hyp">
        <div class="hyp-head">
          <span class="tier" :class="confidenceTier(h.confidence)">{{ confidenceTier(h.confidence) }}</span>
          <p>{{ h.hypothesis }}</p>
        </div>
        <div class="ev-refs">
          <button v-for="id in h.evidenceIds" :key="id" class="ev-chip" @click="expandedEv = expandedEv === id ? null : id">
            {{ id }}
          </button>
          <div v-if="expandedEv && evidenceById[expandedEv]" class="ev-detail">
            <header><b>{{ evidenceById[expandedEv]!.summary }}</b><span class="chip" :class="evidenceById[expandedEv]!.status">{{ statusChip[evidenceById[expandedEv]!.status] }}</span></header>
            <pre v-if="evidenceById[expandedEv]!.snippet">{{ evidenceById[expandedEv]!.snippet }}</pre>
            <p v-else class="quiet">无片段（可能因容量上限被截断；内容哈希 {{ evidenceById[expandedEv]!.contentHash || '—' }}）</p>
          </div>
        </div>
      </div>
    </section>
    <p v-else class="quiet inconclusive">证据不足：本轮采集未能形成可支撑的根因假设（不等于「系统健康」）。</p>

    <section v-if="report.suggestions?.length" class="sugs">
      <h4>建议动作</h4>
      <ul>
        <li v-for="(s, i) in report.suggestions" :key="i">
          <span>{{ s.text }}</span>
          <button v-if="s.destination" class="link" @click="go(s.destination)">前往处理</button>
        </li>
      </ul>
    </section>

    <section class="meta">
      <h4>数据源覆盖</h4>
      <div class="cover">
        <span v-for="(st, src) in coverage" :key="src" class="chip" :class="st">{{ src }} · {{ statusChip[st] || st }}</span>
        <span class="chip missing">链路 Trace · 首期不支持</span>
      </div>
      <p v-if="report.confidenceNote" class="note"><Icon name="alert" :size="13" />{{ report.confidenceNote }}</p>
      <p class="usage">
        窗口 {{ new Date(run.windowStart).toLocaleString('zh-CN') }} ~ {{ new Date(run.windowEnd).toLocaleString('zh-CN') }}
        <template v-if="run.usage"> · Token 用量 ≈{{ run.usage.tokens }}（{{ run.usage.usageSource === 'estimated' ? '估算口径' : '网关计量' }}）</template>
        <template v-if="run.model"> · 模型 {{ run.model }}</template>
      </p>
    </section>
  </article>
</template>

<style scoped>
.report { border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
.verdict { display: flex; align-items: center; gap: 10px; padding: 12px 14px; font-size: 13px; background: var(--surface-2); }
.verdict .sev { font-size: 11px; padding: 2px 8px; border-radius: 10px; flex: none; }
.verdict.critical .sev { background: var(--danger); color: #fff; }
.verdict.warning .sev { background: #b8860b; color: #fff; }
.verdict.info .sev { background: var(--brand-soft); color: var(--brand); }
.verdict .quality { margin-left: auto; font-size: 11px; color: var(--text-faint); flex: none; }
section { padding: 12px 14px; border-top: 1px solid var(--border); }
h4 { margin: 0 0 8px; font-size: 12px; color: var(--text-dim); }
.hyp { margin-bottom: 10px; }
.hyp-head { display: flex; gap: 8px; align-items: baseline; }
.hyp-head p { margin: 0; font-size: 13px; }
.tier { font-size: 11px; padding: 1px 7px; border-radius: 9px; flex: none; }
.tier.较高 { background: var(--brand-soft); color: var(--brand); }
.tier.中等 { background: var(--surface-2); color: var(--text-dim); }
.tier.较低 { background: var(--surface-2); color: var(--text-faint); }
.ev-refs { margin-top: 6px; display: flex; flex-wrap: wrap; gap: 6px; }
.ev-chip { font-size: 11px; border: 1px dashed var(--border-strong); background: transparent; color: var(--text-dim); border-radius: 5px; padding: 2px 7px; cursor: pointer; font-family: ui-monospace, monospace; }
.ev-detail { width: 100%; margin-top: 6px; border: 1px solid var(--border); border-radius: 6px; padding: 8px; }
.ev-detail header { display: flex; justify-content: space-between; gap: 8px; align-items: baseline; font-size: 12px; }
.ev-detail pre { max-height: 200px; overflow: auto; font-size: 11px; background: var(--surface-2); padding: 8px; border-radius: 4px; white-space: pre-wrap; word-break: break-all; margin: 6px 0 0; }
.sugs ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.sugs li { display: flex; justify-content: space-between; gap: 10px; font-size: 13px; align-items: center; }
.link { border: 0; background: none; color: var(--brand); cursor: pointer; font: inherit; font-size: 12px; flex: none; }
.cover { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { font-size: 11px; padding: 2px 8px; border-radius: 9px; background: var(--surface-2); color: var(--text-dim); }
.chip.ok { color: var(--ok, #22a06b); }
.chip.error, .chip.failed { color: var(--danger); }
.chip.missing { color: var(--text-faint); }
.note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; color: var(--text-faint); margin: 8px 0 0; }
.usage { font-size: 11px; color: var(--text-faint); margin: 8px 0 0; }
.quiet { color: var(--text-faint); font-size: 12px; }
.inconclusive { padding: 0 14px 12px; margin: 0; border-top: 1px solid var(--border); padding-top: 12px; }
</style>
