<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { copyText } from '@/utils/clipboard'
import { fetchList, fetchCommand, apiError } from '@/api'
import {
  listMetrics,
  listLogs,
  listTraces,
  getTrace,
  listAlerts,
  listAlertRules,
  listAlertEvents,
  createAlertRule,
  updateAlertRule,
  listChannels,
  createChannel,
  updateChannel,
  deleteChannel,
  probeMetricsQuality,
  type MetricSeries,
  type LogEntry,
  type Alert,
  type AlertRule,
  type AlertEvent,
  type NotificationChannel,
  type QualityReport,
} from '@/api/observability'
import {
  buildSpanTree,
  flattenSpanTree,
  spanServiceLabel,
  spanKindBadge,
  traceErrSummary,
  spanLane,
  spanInstanceLane,
  spanRoute,
  traceContext,
  kindGlyph,
  type Span,
  type Trace,
} from '@/composables/useSpanTree'
import { useEnvStore } from '@/stores/env'
import { usePolling } from '@/composables/usePolling'
import { confirmAction } from '@/composables/useConfirmAction'
import {
  observationTargets,
  metricDefinitions,
  metricLabel,
  type ObservationScope,
  type ObservedWorkload,
  type ObservedDataService,
} from '@/domain/observability'
import { fmtMetric } from '@/composables/useMetricFormat'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import MetricChart from '@/components/MetricChart.vue'
import SkillTraceEvidence from '@/components/SkillTraceEvidence.vue'
const route = useRoute(),
  router = useRouter(),
  envStore = useEnvStore()
const tabs = [
  { id: 'overview', label: '总览' },
  { id: 'metrics', label: '指标' },
  { id: 'logs', label: '日志' },
  { id: 'traces', label: '链路' },
  { id: 'alerts', label: '告警' },
]
const query = (key: string, fallback = '') =>
  typeof route.query[key] === 'string' ? String(route.query[key]) : fallback
const tab = computed(() =>
  tabs.some((t) => t.id === query('tab')) ? query('tab') : 'overview',
)
const scope = computed<ObservationScope>(() => ({
  app: query('app'),
  ds: query('ds'),
  workload: query('workload'),
  lane: query('lane'),
  env: query('env', envStore.currentEnvId),
}))
const range = computed(() =>
  ['15m', '1h', '6h', '24h'].includes(query('range')) ? query('range') : '1h',
)
function update(values: Record<string, string | undefined>) {
  void router.replace({ query: { ...route.query, ...values } })
}
const entity = computed(() =>
  scope.value.ds
    ? 'dataservice:' + scope.value.ds
    : scope.value.workload
      ? 'workload:' + scope.value.workload
      : scope.value.app
        ? 'app:' + scope.value.app
        : '',
)
function selectEntity(value: string) {
  const [kind, ...rest] = value.split(':')
  const id = rest.join(':')
  update({
    app: kind === 'app' ? id : undefined,
    ds: kind === 'dataservice' ? id : undefined,
    workload: kind === 'workload' ? id : undefined,
    traceId: undefined,
  })
}
const apps = ref<{ id: string; name: string }[]>([]),
  services = ref<ObservedDataService[]>([]),
  workloads = ref<ObservedWorkload[]>([])
const ready = ref(false),
  catalogError = ref('')
const metrics = ref<MetricSeries[]>([]),
  logs = ref<LogEntry[]>([]),
  traces = ref<Trace[]>([]),
  alerts = ref<Alert[]>([]),
  rules = ref<AlertRule[]>([]),
  events = ref<AlertEvent[]>([]),
  channels = ref<NotificationChannel[]>([])
const errors = reactive<Record<string, string>>({}),
  busy = reactive<Record<string, boolean>>({})
const refreshed = ref('')
const names = computed(() =>
  Object.fromEntries([
    ...apps.value.map((a) => ['app:' + a.id, a.name]),
    ...services.value.map((d) => ['dataservice:' + d.id, d.name]),
    ...workloads.value.map((w) => [
      'workload:' + w.id,
      (apps.value.find((a) => a.id === w.appId)?.name || w.appId) +
        ' / ' +
        (w.name || w.id) +
        ' · ' +
        (envStore.envs.find((e) => e.id === w.envId)?.name || w.envId) +
        ' / ' +
        (w.laneId || 'default'),
    ]),
  ]),
)
const scopedWorkloads = computed(() =>
  workloads.value.filter(
    (w) =>
      (!scope.value.env || w.envId === scope.value.env) &&
      (!scope.value.app || w.appId === scope.value.app) &&
      (!scope.value.lane || (w.laneId || 'default') === scope.value.lane),
  ),
)
const targetName = (type: string, id: string) =>
  names.value[type + ':' + id] || id || '全部实体'
const targets = computed(() =>
  observationTargets(scope.value, workloads.value, services.value),
)
// 资源解析失败时绝不退回租户全量，避免看错环境。
async function loadCatalog() {
  ready.value = false
  catalogError.value = ''
  try {
    const [a, d, w] = await Promise.all([
      fetchList<{ id: string; name: string }>('/api/applications'),
      fetchList<ObservedDataService>('/api/dataservices'),
      fetchList<ObservedWorkload>('/api/workloads'),
      envStore.loadEnvs(),
    ])
    apps.value = a
    services.value = d
    workloads.value = w
    ready.value = true
    await refresh()
  } catch (e) {
    catalogError.value = apiError(e, '实体列表加载失败')
  }
}
let revision = 0
let silentRefresh = false
async function capture<T>(
  key: string,
  task: () => Promise<T>,
  commit: (value: T) => void,
  rev: number,
) {
  busy[key] = !silentRefresh
  errors[key] = ''
  try {
    const result = await task()
    if (rev === revision) commit(result)
  } catch (e) {
    if (rev === revision) errors[key] = apiError(e)
  } finally {
    if (rev === revision) busy[key] = false
  }
}
async function queryTargets<T>(
  fn: (p: Record<string, string>) => Promise<T[]>,
  base: Record<string, string>,
) {
  const list = targets.value.map((t) => ({ ...base, ...t }))
  const result: T[] = []
  let index = 0
  await Promise.all(
    Array.from({ length: Math.min(5, list.length) }, async () => {
      while (index < list.length) {
        const p = list[index++]
        if (p) result.push(...(await fn(p)))
      }
    }),
  )
  return result
}
const logText = ref(query('q')),
  traceText = ref(query('traceId')),
  laneText = ref(query('lane'))
watch(
  () => route.query,
  () => {
    logText.value = query('q')
    traceText.value = query('traceId')
    laneText.value = query('lane')
  },
)
const expanded = ref(''),
  selectedSpan = ref<Span>(),
  traceLogs = ref<LogEntry[]>([])
let traceLogSeq = 0
async function loadAssociated(traceId: string) {
  const my = ++traceLogSeq
  traceLogs.value = []
  errors.traceLogs = ''
  busy.traceLogs = true
  try {
    const rows = await listLogs({ traceId, range: range.value, limit: '20' })
    if (my === traceLogSeq && expanded.value === traceId) traceLogs.value = rows
  } catch (e) {
    if (my === traceLogSeq) errors.traceLogs = apiError(e)
  } finally {
    if (my === traceLogSeq) busy.traceLogs = false
  }
}
async function copyTrace(id: string) {
  await copyText(id, 'TraceID 已复制')
}
const flat = (t: Trace) => flattenSpanTree(buildSpanTree(t.spans || []))
const duration = (t: Trace) =>
  Math.max(
    t.durationMs || 0,
    ...(t.spans || []).map((s) => s.startMs + s.durationMs),
    1,
  )
function scopedRecord(r: { targetType?: string; targetId: string }) {
  const s = scope.value
  if (s.ds)
    return (
      r.targetType === 'dataservice' && (!r.targetId || r.targetId === s.ds)
    )
  if (s.workload)
    return (
      r.targetType === 'workload' && (!r.targetId || r.targetId === s.workload)
    )
  if (s.app && !s.env && !s.lane)
    return (
      (r.targetType || 'app') === 'app' && (!r.targetId || r.targetId === s.app)
    )
  if (s.env || s.lane || s.app)
    return (
      targets.value.some(
        (t) =>
          t.targetType === (r.targetType || 'app') &&
          (!r.targetId || t.targetId === r.targetId),
      ) ||
      ((r.targetType || 'app') === 'app' &&
        scopedWorkloads.value.some(
          (w) => !r.targetId || w.appId === r.targetId,
        ))
    )
  return true
}
const activeAlerts = computed(() => alerts.value.filter(scopedRecord)),
  scopedRules = computed(() => rules.value.filter(scopedRecord)),
  scopedEvents = computed(() => events.value.filter(scopedRecord))
const firing = computed(
  () => activeAlerts.value.filter((a) => a.status === 'firing').length,
)
const traceNotice = computed(() =>
  query('traceId')
    ? '正在按 TraceID 精确查询；实体、时间和泳道筛选不用于隐藏此查询结果。'
    : scope.value.ds
      ? '数据服务尚无可关联的链路查询接口；请从调用它的产品查看请求链路。'
      : scope.value.env || scope.value.lane || scope.value.workload
        ? '链路先查询最近 100 条候选，再按上报的环境 / Pod / 泳道标签筛选；无对应标签的链路不会混入结果。需要更早的记录请使用 TraceID 精确查询。'
        : '列表最多返回最近 100 条；更早的链路可通过 TraceID 精确查询。',
)
function traceMatches(t: Trace) {
  const s = scope.value
  if (s.app && t.appId !== s.app) return false
  if (s.env || s.workload) {
    const ids = new Set(
      scopedWorkloads.value
        .filter((w) => !s.workload || w.id === s.workload)
        .map((w) => w.id),
    )
    if (
      !t.spans?.some((sp) => {
        const tags = sp.tags || {},
          pod = tags['k8s.pod.name'] || '',
          wid = tags['paas.workload'] || tags['paas.workload_id'] || ''
        return (
          (s.env &&
            !s.workload &&
            (tags['paas.env'] === s.env || tags['paas.env_id'] === s.env)) ||
          ids.has(wid) ||
          [...ids].some((id) => pod.startsWith(id + '-'))
        )
      })
    )
      return false
  }
  return !s.lane || !!t.spans?.some((sp) => sp.tags?.['paas.lane'] === s.lane)
}
// ---- 采集覆盖（spec 专项04 §5：回答「为什么没有数据」）----
// 独立诊断端点（不在主查询路径）：展开时对当前实体逐 metric 即时探测。
// empty=查询成功无数据（检查时间范围/目标/埋点，非平台故障）；failed=采集源问题。
const showQuality = ref(false)
const quality = ref<QualityReport | null>(null)
const qualityBusy = ref(false)
const qualityError = ref('')
const qualityTargetType = computed(() =>
  scope.value.ds ? 'dataservice' : scope.value.workload ? 'workload' : scope.value.app ? 'app' : '',
)
const qualityTargetId = computed(
  () => scope.value.ds || scope.value.workload || scope.value.app || '',
)
async function toggleQuality() {
  showQuality.value = !showQuality.value
  if (!showQuality.value || qualityBusy.value) return
  // 切实体后重探测（缓存上次 targetType+id 防重复拉取）
  if (quality.value && qualityTargetType.value + qualityTargetId.value === qualityCacheKey) return
  qualityBusy.value = true
  qualityError.value = ''
  try {
    quality.value = await probeMetricsQuality(qualityTargetType.value, qualityTargetId.value)
    qualityCacheKey = qualityTargetType.value + qualityTargetId.value
  } catch (e) {
    qualityError.value = apiError(e, '采集质量探测失败')
  } finally {
    qualityBusy.value = false
  }
}
let qualityCacheKey = ''
watch([qualityTargetType, qualityTargetId], () => {
  quality.value = null
  qualityCacheKey = ''
  if (showQuality.value) toggleQuality()
})
const qualityStatusLabel: Record<string, string> = {
  ok: '有数据',
  empty: '查询成功，无数据',
  failed: '查询失败',
}
const qualityStatusClass: Record<string, string> = {
  ok: 'q-ok',
  empty: 'q-empty',
  failed: 'q-failed',
}

async function refresh(silent = false) {
  silentRefresh = silent
  if (!ready.value) return
  const rev = ++revision
  Object.keys(busy).forEach((k) => {
    busy[k] = false
  })
  const jobs: Promise<unknown>[] = []
  const needs = (...ids: string[]) => ids.includes(tab.value)
  if (needs('overview', 'metrics'))
    jobs.push(
      capture(
        'metrics',
        () => queryTargets(listMetrics, { range: range.value }),
        (v) => {
          metrics.value = v
        },
        rev,
      ),
    )
  if (needs('overview', 'alerts'))
    jobs.push(
      capture(
        'alerts',
        () => listAlerts(),
        (v) => {
          alerts.value = v
        },
        rev,
      ),
    )
  if (needs('alerts')) {
    jobs.push(
      capture(
        'rules',
        listAlertRules,
        (v) => {
          rules.value = v
        },
        rev,
      ),
    )
    jobs.push(
      capture(
        'events',
        () => listAlertEvents(100),
        (v) => {
          events.value = v
        },
        rev,
      ),
    )
    // 通知渠道：低频配置，与告警 tab 同批加载；失败独立结算（表单显示失败而非假空）。
    jobs.push(
      capture(
        'channels',
        listChannels,
        (v) => {
          channels.value = v
        },
        rev,
      ),
    )
  }
  if (needs('logs'))
    jobs.push(
      capture(
        'logs',
        async () => {
          const base: Record<string, string> = {
            range: range.value,
            limit: '100',
          }
          if (scope.value.app) base.appId = scope.value.app
          if (scope.value.lane) base.lane = scope.value.lane
          if (query('q')) base.q = query('q')
          if (query('level')) base.level = query('level')
          if (query('logTrace')) base.traceId = query('logTrace')
          return (await queryTargets(listLogs, base)).sort((a, b) =>
            b.timestamp.localeCompare(a.timestamp),
          )
        },
        (v) => {
          logs.value = v
        },
        rev,
      ),
    )
  if (needs('traces'))
    jobs.push(
      capture(
        'traces',
        async () => {
          if (query('traceId')) return [await getTrace<Trace>(query('traceId'))]
          if (scope.value.ds) return []
          const p: Record<string, string> = { range: range.value, limit: '100' }
          if (scope.value.app) p.appId = scope.value.app
          if (query('status')) p.status = query('status')
          return (await listTraces<Trace>(p)).filter(traceMatches)
        },
        (v) => {
          traces.value = v
          const direct = query('traceId')
          if (direct) {
            expanded.value = direct
            void loadAssociated(direct)
          } else if (!v.some((t) => t.id === expanded.value)) {
            expanded.value = ''
            selectedSpan.value = undefined
          }
        },
        rev,
      ),
    )
  await Promise.all(jobs)
  if (rev === revision) refreshed.value = new Date().toLocaleTimeString('zh-CN')
}
onMounted(loadCatalog)
watch(
  () => route.fullPath,
  () => {
    page.value = 1
    void refresh()
  },
)
watch(
  () => envStore.currentEnvId,
  () => {
    page.value = 1
    void refresh()
  },
)
usePolling(() => refresh(true), 15000, { active: () => ready.value })
const page = ref(1),
  compare = ref(false)
const groups = ['全部指标', '资源', '请求', '引擎', '其他']
const selectedGroup = computed(() => query('group', '全部指标'))
const filteredMetrics = computed(() =>
  metrics.value.filter(
    (m) =>
      (selectedGroup.value === '全部指标' ||
        (metricDefinitions[m.name]?.group || '其他') === selectedGroup.value) &&
      (!query('metric') || m.name === query('metric')),
  ),
)
const chartGroups = computed(() => {
  const map = new Map<string, MetricSeries[]>()
  for (const m of filteredMetrics.value) {
    const key = compare.value
      ? m.name + ':' + (m.unit || '')
      : m.targetType + ':' + m.targetId + ':' + m.name + ':' + (m.unit || '')
    map.set(key, [...(map.get(key) || []), m])
  }
  return [...map.entries()].map(([key, series]) => ({
    key,
    series,
    title: metricLabel(series[0]!.name),
  }))
})
watch([selectedGroup, () => query('metric'), compare], () => {
  page.value = 1
})
const health = computed(() => {
  const map = new Map<
    string,
    { key: string; type: string; id: string; series: MetricSeries[] }
  >()
  for (const m of metrics.value) {
    const key = m.targetType + ':' + m.targetId
    const row = map.get(key) || {
      key,
      type: m.targetType,
      id: m.targetId,
      series: [],
    }
    row.series.push(m)
    map.set(key, row)
  }
  return [...map.values()]
})
const current = (row: (typeof health.value)[number], name: string) => {
  const s = row.series.find((m) => m.name === name)
  return s
    ? fmtMetric(s.current, s.unit || '') + (s.unit ? ' ' + s.unit : '')
    : '未上报'
}
function drill(type: string, id: string) {
  update({
    tab: 'metrics',
    app: type === 'app' ? id : undefined,
    ds: type === 'dataservice' ? id : undefined,
    workload: type === 'workload' ? id : undefined,
    traceId: undefined,
  })
}
async function openTrace(t: Trace) {
  expanded.value = expanded.value === t.id ? '' : t.id
  selectedSpan.value = undefined
  if (expanded.value) await loadAssociated(t.id)
}
const showRule = ref(false),
  editing = ref(''),
  saving = ref(false)
const ruleForm = ref({
  name: '',
  metricName: 'cpu',
  targetType: 'app',
  targetId: '',
  operator: '>',
  threshold: 0.8,
  severity: 'warning',
  enabled: true,
  webhookUrl: '',
  channelIds: [] as string[],
})
const metricOptions = computed(() =>
  Object.entries(metricDefinitions).filter(
    ([name]) =>
      ruleForm.value.targetType === 'dataservice' ||
      ['cpu', 'mem', 'rps', 'latency', 'errorRate'].includes(name),
  ),
)
const ruleTargets = computed(() =>
  ruleForm.value.targetType === 'dataservice'
    ? services.value
    : ruleForm.value.targetType === 'workload'
      ? workloads.value.map((w) => ({
          id: w.id,
          name: targetName('workload', w.id),
        }))
      : apps.value,
)
function resetRuleTarget() { ruleForm.value.targetId = ''; ruleForm.value.metricName = 'cpu' }
function editRule(rule?: AlertRule) {
  editing.value = rule?.id || ''
  ruleForm.value = rule
    ? { ...rule, webhookUrl: rule.webhookUrl || '', channelIds: rule.channelIds ?? [] }
    : {
        name: '',
        metricName: 'cpu',
        targetType: scope.value.ds
          ? 'dataservice'
          : scope.value.workload
            ? 'workload'
            : 'app',
        targetId: scope.value.ds || scope.value.workload || scope.value.app,
        operator: '>',
        threshold: 0.8,
        severity: 'warning',
        enabled: true,
        webhookUrl: '',
        channelIds: [],
      }
  showRule.value = true
}
async function saveRule() {
  if (!ruleForm.value.name.trim()) return
  saving.value = true
  try {
    if (editing.value) await updateAlertRule(editing.value, ruleForm.value)
    else await createAlertRule(ruleForm.value)
    showRule.value = false
    ElMessage.success('告警规则已保存')
    await refresh()
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    saving.value = false
  }
}
async function toggleRule(r: AlertRule) {
  try {
    await updateAlertRule(r.id, { ...r, enabled: !r.enabled })
    await refresh()
  } catch (e) {
    ElMessage.error(apiError(e))
  }
}
async function removeRule(r: AlertRule) {
  if (!(await confirmAction(`删除告警规则「${r.name}」？此操作不可恢复。`)))
    return
  try {
    await fetchCommand('/api/observability/alert-rules/' + r.id, {
      method: 'DELETE',
    })
    await refresh()
  } catch (e) {
    ElMessage.error(apiError(e))
  }
}

// ---- 通知渠道（租户级派发目标 + 抑制策略） ----
const showChannel = ref(false),
  editingChannel = ref(''),
  channelSaving = ref(false)
// headersText：每行「Key: Value」。编辑回填值为 ***（服务端保留原值，不回填明文）。
const channelForm = ref({
  name: '',
  url: '',
  headersText: '',
  enabled: true,
  cooldownMinutes: 0,
  muteUntil: '',
})
function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t) continue
    const i = t.indexOf(':')
    if (i <= 0) continue
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return out
}
const headersToText = (h?: Record<string, string>) =>
  h ? Object.entries(h).map(([k, v]) => `${k}: ${v}`).join('\n') : ''
function channelState(ch: NotificationChannel) {
  if (!ch.enabled) return { label: '停用', cls: 'muted' }
  if (ch.muteUntil && !ch.muteUntil.startsWith('0001-') && new Date(ch.muteUntil) > new Date())
    return { label: '静默中', cls: 'warn' }
  return { label: '正常', cls: 'ok' }
}
function editChannel(ch?: NotificationChannel) {
  editingChannel.value = ch?.id || ''
  channelForm.value = ch
    ? {
        name: ch.name,
        url: ch.url,
        headersText: headersToText(ch.headers),
        enabled: ch.enabled,
        cooldownMinutes: ch.cooldownMinutes,
        muteUntil:
          ch.muteUntil && !ch.muteUntil.startsWith('0001-') ? ch.muteUntil : '',
      }
    : { name: '', url: '', headersText: '', enabled: true, cooldownMinutes: 0, muteUntil: '' }
  showChannel.value = true
}
async function saveChannel() {
  if (!channelForm.value.name.trim() || !channelForm.value.url.trim()) {
    ElMessage.warning('请填写渠道名称与通知地址')
    return
  }
  channelSaving.value = true
  try {
    const body: Record<string, unknown> = {
      name: channelForm.value.name.trim(),
      kind: 'webhook',
      url: channelForm.value.url.trim(),
      enabled: channelForm.value.enabled,
      cooldownMinutes: channelForm.value.cooldownMinutes,
    }
    const hdrs = parseHeaders(channelForm.value.headersText)
    if (Object.keys(hdrs).length) body.headers = hdrs
    // 空值不发送：服务端零值即「无静默」（空串会触发 JSON 时间解析失败）。
    if (channelForm.value.muteUntil) body.muteUntil = channelForm.value.muteUntil
    if (editingChannel.value) await updateChannel(editingChannel.value, body)
    else await createChannel(body)
    showChannel.value = false
    ElMessage.success('通知渠道已保存')
    await refresh()
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    channelSaving.value = false
  }
}
async function removeChannel(ch: NotificationChannel) {
  if (
    !(await confirmAction(
      `删除通知渠道「${ch.name}」？引用该渠道的告警规则将不再向它投递（其余渠道不受影响）。`,
    ))
  )
    return
  try {
    await deleteChannel(ch.id)
    ElMessage.success('已删除')
    await refresh()
  } catch (e) {
    ElMessage.error(apiError(e))
  }
}
const time = (iso: string) => new Date(iso).toLocaleString('zh-CN')
</script>
<template>
  <div class="page observation-page">
    <PageHeader
      title="可观测"
      description="先发现异常，再从指标趋势进入日志与请求链路。"
      >
<div class="refresh-action">
        <span>{{ refreshed ? '更新于 ' + refreshed : '加载中' }} · 15s 刷新</span><button class="act" @click="refresh()">刷新</button>
      </div>
</PageHeader>
    <nav class="section-nav" aria-label="可观测功能">
      <button
        v-for="t in tabs"
        :key="t.id"
        :class="{ active: tab === t.id }"
        :aria-current="tab === t.id ? 'page' : undefined"
        @click="update({ tab: t.id })"
      >
        {{ t.label
        }}<span v-if="t.id === 'alerts' && firing" class="alert-count">{{
          firing
        }}</span>
      </button>
    </nav>
    <div class="scope-bar">
      <label>实体<select
          :value="entity"
          aria-label="观测实体"
          @change="selectEntity(($event.target as HTMLSelectElement).value)"
        >
          <option value="">全部实体</option>
          <optgroup label="产品">
            <option v-for="a in apps" :key="a.id" :value="'app:' + a.id">
              {{ a.name }}
            </option>
          </optgroup>
          <optgroup label="数据服务">
            <option
              v-for="d in services.filter(
                (d) => !scope.env || d.envId === scope.env,
              )"
              :key="d.id"
              :value="'dataservice:' + d.id"
            >
              {{ d.name }}
            </option>
          </optgroup>
          <optgroup label="工作负载">
            <option
              v-for="w in scopedWorkloads"
              :key="w.id"
              :value="'workload:' + w.id"
            >
              {{ targetName('workload', w.id) }}
            </option>
          </optgroup>
        </select></label>
      <label>时间<select
          :value="range"
          aria-label="观测时间范围"
          @change="
            update({ range: ($event.target as HTMLSelectElement).value })
          "
        >
          <option value="15m">最近 15 分钟</option>
          <option value="1h">最近 1 小时</option>
          <option value="6h">最近 6 小时</option>
          <option value="24h">最近 24 小时</option>
        </select></label>
      <label v-if="!scope.ds">泳道<input
          v-model="laneText"
          placeholder="全部泳道"
          aria-label="观测泳道"
          @keyup.enter="update({ lane: laneText || undefined })"
          @change="update({ lane: laneText || undefined })"
      /></label>
      <span class="scope-env">{{
          envStore.envs.find((e) => e.id === scope.env)?.name ||
          scope.env ||
          '全部环境'
        }}<small v-if="scope.env"> · 指标和日志按该环境的实例查询</small></span>
    </div>
    <StatePanel
      v-if="catalogError"
      title="观测实体加载失败"
      :description="catalogError"
      error
      >
<button class="act" @click="loadCatalog">重新加载</button>
</StatePanel>
    <div v-else-if="!ready" class="loading-block" v-loading="true" />
    <template v-else>
      <template v-if="tab === 'overview'">
        <div class="overview-heading">
          <div>
            <span class="eyebrow">当前范围</span>
            <h2>运行健康概览</h2>
            <p>查看真实采集的实体指标；缺少采集不会被判定为健康。</p>
          </div>
          <button class="act" @click="update({ tab: 'alerts' })">
            {{ firing }} 条触发中告警 →
          </button>
        </div>
        <StatePanel
          v-if="errors.alerts"
          compact
          title="实时告警暂不可用"
          :description="errors.alerts"
          error
        />
        <div v-else-if="firing" class="active-alerts">
          <button
            v-for="(a, i) in activeAlerts
              .filter((a) => a.status === 'firing')
              .slice(0, 5)"
            :key="i"
            @click="drill(a.targetType || 'app', a.targetId)"
          >
            <span class="status err">{{
              a.severity === 'critical' ? '严重' : '警告'
            }}</span><strong>{{ a.ruleName }}</strong><span>{{ targetName(a.targetType || 'app', a.targetId) }}</span><span>{{ a.value }} {{ a.operator }} {{ a.threshold }} →</span>
          </button>
        </div>
        <StatePanel
          v-if="errors.metrics"
          title="指标加载失败"
          :description="errors.metrics"
          error
          >
<button class="act" @click="refresh()">重试</button>
</StatePanel>
        <div v-else-if="busy.metrics" class="loading-block" v-loading="true" />
        <StatePanel
          v-else-if="!health.length"
          title="当前范围尚无指标"
          description="请检查采集配置、实体部署状态和时间范围。没有数据不等于没有异常。"
          >
<button class="act" @click="update({ tab: 'metrics' })">
            进入指标
          </button>
</StatePanel>
        <div v-else class="table-scroll" tabindex="0">
          <table class="list-table health-table">
            <thead>
              <tr>
                <th>实体</th>
                <th>CPU</th>
                <th>内存</th>
                <th>请求速率</th>
                <th>已上报指标</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in health" :key="r.key">
                <td>
                  <strong>{{ targetName(r.type, r.id) }}</strong><small>{{
                    r.type === 'app'
                      ? '产品'
                      : r.type === 'dataservice'
                        ? '数据服务'
                        : '工作负载'
                  }}</small>
                </td>
                <td>{{ current(r, 'cpu') }}</td>
                <td>{{ current(r, 'mem') }}</td>
                <td>{{ current(r, 'rps') }}</td>
                <td>{{ r.series.length }} 项</td>
                <td>
                  <button class="act" @click="drill(r.type, r.id)">
                    查看趋势 →
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <template v-if="tab === 'metrics'">
        <div class="quality-panel">
          <button class="quality-toggle" :aria-expanded="showQuality" @click="toggleQuality">
            采集覆盖{{ showQuality ? '▾' : '▸' }}
          </button>
          <span v-if="!qualityTargetType" class="quality-hint">选择应用/工作负载/数据服务后可探测各指标采集状态</span>
          <div v-if="showQuality && qualityTargetType" class="quality-body">
            <p v-if="qualityBusy" class="quality-note">探测中…</p>
            <StatePanel
              v-else-if="qualityError"
              title="采集质量探测失败"
              :description="qualityError"
              error
            />
            <template v-else-if="quality">
              <p v-if="quality.source === 'memory'" class="quality-note quality-warn">
                {{ quality.note }}
              </p>
              <p v-else-if="quality.note" class="quality-note">{{ quality.note }}</p>
              <table v-if="quality.metrics.length" class="quality-table">
                <thead>
                  <tr>
                    <th>指标</th>
                    <th>采集状态</th>
                    <th>说明</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="m in quality.metrics" :key="m.metric">
                    <td>{{ metricLabel(m.metric) }}</td>
                    <td>
                      <span :class="['quality-badge', qualityStatusClass[m.status]]">{{
                        qualityStatusLabel[m.status]
                      }}</span>
                    </td>
                    <td>
                      <template v-if="m.status === 'ok'">{{ m.sampleSeries }} 组序列</template>
                      <template v-else-if="m.status === 'empty'">
查询成功无数据——检查时间范围/目标/埋点（RED 类依赖应用暴露
                        /metrics），非平台故障
</template>
                      <template v-else>{{ m.error }}</template>
                    </td>
                  </tr>
                </tbody>
              </table>
            </template>
          </div>
        </div>
        <div class="metric-toolbar">
          <div class="metric-categories" role="group" aria-label="指标分类">
            <button
              v-for="g in groups"
              :key="g"
              :class="{ active: selectedGroup === g }"
              @click="update({ group: g === '全部指标' ? undefined : g })"
            >
              {{ g }}
            </button>
          </div>
          <select
            :value="query('metric')"
            aria-label="指标名称"
            @change="
              update({
                metric: ($event.target as HTMLSelectElement).value || undefined,
              })
            "
          >
            <option value="">全部名称</option>
            <option
              v-for="m in [...new Set(metrics.map((m) => m.name))]"
              :key="m"
              :value="m"
            >
              {{ metricLabel(m) }}
            </option>
</select><label class="compare-switch"><input v-model="compare" type="checkbox" /> 同指标多实体比较</label>
        </div>
        <p class="data-note">
          仅展示已采集的指标，缺失数据不记为 0。同指标比较保留独立的实体与单位。
        </p>
        <StatePanel
          v-if="errors.metrics"
          title="指标加载失败"
          :description="errors.metrics"
          error
          >
<button class="act" @click="refresh()">重新加载</button>
</StatePanel>
        <div v-else-if="busy.metrics" class="loading-block" v-loading="true" />
        <StatePanel
          v-else-if="!chartGroups.length"
          title="当前筛选没有指标"
          description="调整实体、时间或分类；若实例已运行，请检查采集器与 exporter。"
        />
        <div v-else class="chart-grid">
          <MetricChart
            v-for="g in chartGroups.slice((page - 1) * 12, page * 12)"
            :key="g.key"
            :series="g.series"
            :names="names"
            :title="g.title"
          />
        </div>
        <TablePager v-model="page" :size="12" :total="chartGroups.length" />
      </template>
      <template v-if="tab === 'logs'">
        <div class="filter-bar">
          <select
            :value="query('level')"
            aria-label="日志级别"
            @change="
              update({
                level: ($event.target as HTMLSelectElement).value || undefined,
              })
            "
          >
            <option value="">全部级别</option>
            <option value="error">ERROR</option>
            <option value="warn">WARN</option>
            <option value="info">INFO</option>
</select><input
            v-model="logText"
            aria-label="日志关键词"
            placeholder="搜索日志，Enter 查询"
            @keyup.enter="update({ q: logText || undefined })"
          /><button class="act" @click="update({ q: logText || undefined })">
            查询
</button><button
            v-if="query('logTrace')"
            class="act"
            @click="update({ logTrace: undefined })"
          >
            清除 Trace 筛选
          </button>
        </div>
        <p class="data-note">
          每个实体最多返回最近 100 条日志；时间与实体上下文保留在地址中。
        </p>
        <StatePanel
          v-if="errors.logs"
          title="日志加载失败"
          :description="errors.logs"
          error
        />
        <div v-else-if="busy.logs" class="loading-block" v-loading="true" />
        <StatePanel v-else-if="!logs.length" title="当前范围无日志" />
        <div v-else class="table-scroll" tabindex="0">
          <table class="list-table log-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>级别</th>
                <th>实体</th>
                <th>消息</th>
                <th>链路</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(l, i) in logs.slice((page - 1) * 50, page * 50)"
                :key="l.id || i"
              >
                <td>{{ time(l.timestamp) }}</td>
                <td>
                  <span
                    class="status"
                    :class="
                      l.level === 'error'
                        ? 'err'
                        : l.level === 'warn'
                          ? 'warn'
                          : 'idle'
                    "
                    >{{ l.level }}</span>
                </td>
                <td>
                  {{ targetName(l.targetType || 'app', l.targetId || l.appId) }}
                </td>
                <td class="mono log-message">{{ l.message }}</td>
                <td>
                  <button
                    v-if="l.traceId"
                    class="act"
                    @click="update({ tab: 'traces', traceId: l.traceId })"
                  >
                    查看链路 →
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <TablePager v-model="page" :total="logs.length" :size="50" />
      </template>
      <template v-if="tab === 'traces'">
        <div class="filter-bar">
          <input
            v-model="traceText"
            aria-label="TraceID 精确查询"
            placeholder="输入完整 TraceID"
            @keyup.enter="update({ traceId: traceText || undefined })"
          /><button
            class="act"
            @click="update({ traceId: traceText || undefined })"
          >
            精确查询
</button><button
            v-if="query('traceId')"
            class="act"
            @click="update({ traceId: undefined })"
          >
            返回列表
</button><select
            :value="query('status')"
            aria-label="链路状态"
            @change="
              update({
                status: ($event.target as HTMLSelectElement).value || undefined,
              })
            "
          >
            <option value="">全部状态</option>
            <option value="error">含错误</option>
            <option value="success">正常</option>
          </select>
        </div>
        <p class="data-note">{{ traceNotice }}</p>
        <StatePanel
          v-if="errors.traces"
          title="链路查询失败"
          :description="errors.traces"
          error
        />
        <div v-else-if="busy.traces" class="loading-block" v-loading="true" />
        <StatePanel
          v-else-if="!traces.length"
          title="当前范围无匹配链路"
          description="确认采集与标签，或输入 TraceID 精确查询。"
        />
        <article
          v-for="t in traces.slice((page - 1) * 20, page * 20)"
          v-else
          :key="t.id"
          class="trace-card"
        >
          <button
            class="trace-heading"
            :aria-expanded="expanded === t.id"
            @click="openTrace(t)"
          >
            <span class="status" :class="t.status === 'error' ? 'err' : 'ok'">{{
              t.status === 'error' ? '错误' : '正常'
            }}</span><strong>{{ t.service || targetName('app', t.appId) }} ·
              {{ t.operation }}</strong><code>{{ t.id }}</code><span>{{ t.durationMs }} ms</span><span>{{ time(t.startedAt) }}</span>
          </button>
          <div v-if="expanded === t.id" class="trace-body">
            <div class="trace-summary">
              <span class="trace-ctx">
                <em
                  v-if="traceContext(t).env"
                  class="ctx-chip env"
                  :title="'环境（paas.env）：' + traceContext(t).env"
                >{{ traceContext(t).env }}</em>
                <em
                  v-for="ln in traceContext(t).trafficLanes"
                  :key="ln"
                  class="ctx-chip"
                  :class="{ base: ln === 'default' }"
                  :title="'流量泳道标记，不代表实例落点：' + ln"
                >流量：{{ ln === 'default' ? '基线' : ln }}</em>
                <em
                  v-for="ln in traceContext(t).instanceLanes"
                  :key="'instance-' + ln"
                  class="ctx-chip"
                  :class="{ base: ln === 'default' }"
                  :title="'实例部署泳道（paas.instance_lane）：' + ln"
                >实例：{{ ln === 'default' ? '基线' : ln }}</em>
                <em v-if="!traceContext(t).instanceLanes.length" class="ctx-chip base">实例泳道：未知</em>
                <em
                  v-for="ln in traceContext(t).resolvedLanes"
                  :key="'resolved-' + ln"
                  class="ctx-chip"
                  :class="{ base: ln === 'default' }"
                  :title="'路由到下游的命中泳道（paas.resolved_lane）：' + ln"
                >路由命中：{{ ln === 'default' ? '基线' : ln }}</em>
                <em
                  v-if="traceContext(t).fallback"
                  class="ctx-chip fb"
                  title="部分调用请求了泳道但泳道内无该服务实例，已回退基线（baseline-fallback）"
                >↩ 泳道回退</em>
              </span>
              <span>{{ t.spans?.length || 0 }} 个 span ·
                {{ new Set(t.spans?.map((s) => s.service)).size }} 个服务 ·
                {{ (t.spans || []).filter((s) => s.isError).length }} 个错误
                span</span><button class="act small" @click="copyTrace(t.id)">
                复制 TraceID
              </button>
            </div>
            <p v-if="traceErrSummary(t)" class="trace-error" role="alert">
              {{ traceErrSummary(t) }}
            </p>
            <SkillTraceEvidence
              :spans="t.spans || []"
              @select="selectedSpan = (t.spans || []).find((span) => span.id === $event)"
            />
            <p v-if="!t.spans?.length" class="data-note">
              此记录没有
              span；请检查采样、导出与保留策略。平台无法补回未上报的调用。
            </p>
            <div class="waterfall-scale">
              <span>0 ms</span><span>{{ Math.round(duration(t) / 2) }} ms</span><span>{{ duration(t) }} ms</span>
            </div>
            <button
              v-for="f in flat(t)"
              :key="f.span.id"
              class="span-line"
              :class="{ selected: selectedSpan?.id === f.span.id }"
              :aria-pressed="selectedSpan?.id === f.span.id"
              @click="selectedSpan = f.span"
            >
              <span
                class="span-label"
                :style="{ '--span-indent': `min(${f.depth * 16}px, 25%)` }"
                :title="spanServiceLabel(f.span) + ' / ' + f.span.operation"
              >
                <span v-if="f.depth" class="tree-guides" aria-hidden="true">
                  <i v-for="n in f.depth" :key="n" class="tree-line" />
                </span>
                <em
                  v-if="kindGlyph(f.span.kind)"
                  class="kind-glyph"
                  :title="spanKindBadge(f.span.kind)?.title"
                >{{ kindGlyph(f.span.kind) }}</em>
                <em
                  v-if="spanLane(f.span) && spanLane(f.span) !== 'default'"
                  class="lane-dot"
                  :title="'流量泳道标记，不代表实例落点：' + spanLane(f.span)"
                />
                <em
                  v-if="spanRoute(f.span)?.reason === 'baseline-fallback'"
                  class="fb-mark"
                  :title="
                    '请求泳道 ' + spanRoute(f.span)!.requested +
                    ' 但泳道内无该服务实例，已回退基线 ' +
                    (spanRoute(f.span)!.resolved || 'default')
                  "
                >↩ 基线</em>
                <span class="span-text">{{ f.span.isError ? '错误 · ' : ''
                }}{{ spanServiceLabel(f.span) }} / {{ f.span.operation }}</span>
              </span>
              <div class="span-track">
                <i
                  :class="{ failed: f.span.isError }"
                  :style="{
                    left: (f.span.startMs / duration(t)) * 100 + '%',
                    width:
                      Math.max(0.5, (f.span.durationMs / duration(t)) * 100) +
                      '%',
                  }"
                />
              </div>
              <code>{{ f.span.durationMs }} ms</code>
            </button>
            <section
              v-if="selectedSpan"
              class="span-inspector"
              aria-label="Span 详情"
            >
              <div class="trace-summary">
                <h3>{{ selectedSpan.operation }}</h3>
                <button class="act small" @click="selectedSpan = undefined">
                  关闭详情
                </button>
              </div>
              <dl>
                <dt>服务 / 调用方</dt>
                <dd>{{ spanServiceLabel(selectedSpan) }}</dd>
                <dt>类型</dt>
                <dd>
                  {{
                    spanKindBadge(selectedSpan.kind)?.title ||
                    selectedSpan.kind ||
                    '未上报'
                  }}
                </dd>
                <template v-if="spanRoute(selectedSpan)">
                  <dt>泳道路由</dt>
                  <dd>
                    请求 <strong>{{ spanRoute(selectedSpan)!.requested }}</strong>
                    → 命中 <strong>{{ spanRoute(selectedSpan)!.resolved }}</strong>（{{ ({
                      matched: '泳道命中',
                      'baseline-fallback': '泳道无该服务实例，回退基线',
                      static: '静态模式',
                      unknown: '未知',
                    } as Record<string, string>)[spanRoute(selectedSpan)!.reason] ||
                      spanRoute(selectedSpan)!.reason }}）
                  </dd>
                </template>
                <dt v-if="spanLane(selectedSpan)">流量泳道标记</dt>
                <dd v-if="spanLane(selectedSpan)">{{ spanLane(selectedSpan) }}</dd>
                <dt>实例部署泳道</dt>
                <dd>{{ spanInstanceLane(selectedSpan) || '未知（未上报实例泳道）' }}</dd>
                <dt>SpanID / ParentID</dt>
                <dd>
                  {{ selectedSpan.id }} /
                  {{ selectedSpan.parentId || '根 span' }}
                </dd>
                <dt>偏移 / 耗时</dt>
                <dd>
                  {{ selectedSpan.startMs }} ms /
                  {{ selectedSpan.durationMs }} ms
                </dd>
                <dt>错误</dt>
                <dd>
                  {{
                    selectedSpan.errorMessage ||
                    selectedSpan.errorType ||
                    (selectedSpan.isError
                      ? '标记错误，未上报原因'
                      : '未标记错误')
                  }}
                </dd>
              </dl>
              <details>
                <summary>
                  全部属性（{{ Object.keys(selectedSpan.tags || {}).length }}）
                </summary>
                <dl>
                  <template v-for="(value, key) in selectedSpan.tags" :key="key">
<dt>{{ key }}</dt>
                    <dd>{{ value }}</dd>
</template>
                </dl>
              </details>
            </section>
            <h3>关联日志</h3>
            <p v-if="errors.traceLogs" role="alert">{{ errors.traceLogs }}</p>
            <p v-else-if="busy.traceLogs">加载中…</p>
            <p v-else-if="!traceLogs.length">当前时间范围无关联日志</p>
            <pre v-for="l in traceLogs" v-else :key="l.id">{{ l.timestamp }} [{{ l.level }}] {{ l.message }}</pre>
            <button
              class="act"
              @click="update({ tab: 'logs', logTrace: t.id })"
            >
              在日志中查看 →
            </button>
          </div>
        </article>
        <TablePager v-model="page" :total="traces.length" />
      </template>
      <template v-if="tab === 'alerts'">
        <div class="section-heading">
          <div>
            <h2>实时告警</h2>
            <p>
              状态来自实时评估，不依赖历史事件是否可用。{{
                scope.env || scope.lane
                  ? '产品级规则按相关产品展示，实际评估仍是规则定义的全产品范围。'
                  : ''
              }}
            </p>
          </div>
          <button class="act primary" @click="editRule()">新建规则</button>
        </div>
        <StatePanel
          v-if="errors.alerts"
          title="实时告警加载失败"
          :description="errors.alerts"
          error
        /><StatePanel
          v-else-if="!activeAlerts.length && !busy.alerts"
          title="当前范围没有实时告警"
        />
        <div v-else class="active-alerts">
          <button
            v-for="(a, i) in activeAlerts"
            :key="i"
            @click="drill(a.targetType || 'app', a.targetId)"
          >
            <span
              class="status"
              :class="a.status === 'firing' ? 'err' : 'warn'"
              >{{
                a.status === 'firing'
                  ? '触发中'
                  : a.status === 'pending'
                    ? '观察中'
                    : '已恢复'
              }}</span><strong>{{ a.ruleName }}</strong><span>{{ targetName(a.targetType || 'app', a.targetId) }}</span><span>{{ a.value }} {{ a.operator }} {{ a.threshold }} →</span>
          </button>
        </div>
        <h2 class="subheading">告警规则</h2>
        <StatePanel
          v-if="errors.rules"
          title="规则加载失败"
          :description="errors.rules"
          error
        />
        <div v-else class="table-scroll" tabindex="0">
          <table class="list-table">
            <thead>
              <tr>
                <th>规则</th>
                <th>目标</th>
                <th>条件</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in scopedRules" :key="r.id">
                <td>{{ r.name }}</td>
                <td>{{ targetName(r.targetType, r.targetId) }}</td>
                <td>
                  {{ metricLabel(r.metricName) }} {{ r.operator }}
                  {{ r.threshold }}
                </td>
                <td>{{ r.enabled ? '启用' : '停用' }}</td>
                <td class="row-actions">
                  <button class="act" @click="editRule(r)">编辑</button><button class="act" @click="toggleRule(r)">
                    {{ r.enabled ? '停用' : '启用' }}
</button><button class="act danger" @click="removeRule(r)">
                    删除
                  </button>
                </td>
              </tr>
              <tr v-if="!scopedRules.length">
                <td colspan="5">当前范围暂无规则</td>
              </tr>
            </tbody>
          </table>
        </div>
        <h2 class="subheading">通知渠道</h2>
        <p class="channel-note">
          规则选中渠道后，firing/resolved 通知投递到该地址。抑制策略：停用（整体不投递）、
          静默截止（临时不投递）、重复提醒间隔（0 = 只在状态转变时通知）。地址与请求头仅显示掩码。
        </p>
        <StatePanel
          v-if="errors.channels"
          title="渠道加载失败"
          :description="errors.channels"
          error
        />
        <div v-else class="table-scroll" tabindex="0">
          <table class="list-table">
            <thead>
              <tr>
                <th>渠道</th>
                <th>地址</th>
                <th>状态</th>
                <th>重复提醒</th>
                <th>静默至</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in channels" :key="c.id">
                <td>{{ c.name }}</td>
                <td class="mono">{{ c.url }}</td>
                <td><span class="channel-state" :class="channelState(c).cls">{{ channelState(c).label }}</span></td>
                <td>{{ c.cooldownMinutes ? `每 ${c.cooldownMinutes} 分钟` : '仅状态转变' }}</td>
                <td>
                  {{ c.muteUntil && !c.muteUntil.startsWith('0001-') ? time(c.muteUntil) : '—' }}
                </td>
                <td class="row-actions">
                  <button class="act" @click="editChannel(c)">编辑</button>
                  <button class="act danger" @click="removeChannel(c)">删除</button>
                </td>
              </tr>
              <tr v-if="!channels.length">
                <td colspan="6">暂无渠道，规则只能使用直连 Webhook 地址</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="channel-actions">
          <button class="act" @click="editChannel()">+ 通知渠道</button>
        </div>
        <h2 class="subheading">最近告警历史</h2>
        <StatePanel
          v-if="errors.events"
          title="历史暂不可用"
          :description="errors.events"
          compact
        />
        <div v-else class="table-scroll" tabindex="0">
          <table class="list-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>规则</th>
                <th>目标</th>
                <th>事件</th>
                <th>值</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in scopedEvents" :key="e.id">
                <td>{{ time(e.occurredAt || e.firedAt) }}</td>
                <td>{{ e.ruleName }}</td>
                <td>{{ targetName(e.targetType, e.targetId) }}</td>
                <td>{{ e.status === 'firing' ? '触发' : '恢复' }}</td>
                <td>{{ e.value }}</td>
              </tr>
              <tr v-if="!scopedEvents.length">
                <td colspan="5">暂无历史事件</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
    <el-dialog
      v-model="showRule"
      :title="editing ? '编辑告警规则' : '新建告警规则'"
      width="min(560px, 94vw)"
      >
<el-form label-position="top">
<el-form-item label="规则名称" required>
<el-input v-model="ruleForm.name" />
</el-form-item><el-form-item label="目标类型">
<el-select
            v-model="ruleForm.targetType"
            @change="resetRuleTarget"
            >
<el-option label="产品" value="app" /><el-option
              label="数据服务"
              value="dataservice"
/><el-option
              label="工作负载"
              value="workload"
/>
</el-select>
</el-form-item><el-form-item label="目标实体">
<el-select
            v-model="ruleForm.targetId"
            filterable
            clearable
            placeholder="全部该类型实体"
            >
<el-option
              v-for="t in ruleTargets"
              :key="t.id"
              :label="t.name"
              :value="t.id"
/>
</el-select>
</el-form-item><el-form-item label="指标">
<el-select v-model="ruleForm.metricName">
<el-option
              v-for="[name, def] in metricOptions"
              :key="name"
              :value="name"
              :label="
                def.label +
                (name === 'cpu' ? '（核）' : name === 'mem' ? '（MiB）' : '')
              "
/>
</el-select>
</el-form-item>
        <div class="rule-condition">
          <el-select v-model="ruleForm.operator">
<el-option
              v-for="op in ['>', '>=', '<', '<=']"
              :key="op"
              :value="op"
              :label="op"
/>
</el-select><el-input-number
            v-model="ruleForm.threshold"
            :min="0"
            aria-label="告警阈值"
          />
        </div>
        <el-form-item label="级别">
<el-select v-model="ruleForm.severity">
<el-option label="警告" value="warning" /><el-option
              label="严重"
              value="critical"
/>
</el-select>
</el-form-item>
        <el-form-item label="通知渠道（可选）">
          <el-select
            v-model="ruleForm.channelIds"
            multiple
            clearable
            collapse-tags
            :disabled="!!errors.channels"
            :placeholder="errors.channels ? '渠道列表加载失败，暂不可选' : '选中渠道接收本规则通知'"
          >
            <el-option
              v-for="c in channels"
              :key="c.id"
              :label="c.name + (c.enabled ? '' : '（停用）')"
              :value="c.id"
            />
          </el-select>
          <p v-if="errors.channels" class="channel-inline-err" role="alert">{{ errors.channels }}</p>
          <p v-else-if="ruleForm.channelIds.length" class="channel-inline-hint">
            已选渠道时不再使用下方直连地址；停用/静默/重复提醒在「通知渠道」中配置。
          </p>
        </el-form-item>
        <el-form-item label="Webhook 通知地址（可选）">
<el-input
            v-model="ruleForm.webhookUrl"
            :placeholder="ruleForm.channelIds.length ? '已选渠道，此地址不再使用' : 'https://…'"
/>
</el-form-item><el-switch
          v-model="ruleForm.enabled"
          active-text="启用规则"
/>
</el-form><template #footer>
<el-button @click="showRule = false">取消</el-button><el-button
          type="primary"
          :disabled="!ruleForm.name.trim()"
          :loading="saving"
          @click="saveRule"
          >
保存规则
</el-button>
</template>
</el-dialog>
    <el-dialog
      v-model="showChannel"
      :title="editingChannel ? '编辑通知渠道' : '新建通知渠道'"
      width="min(560px, 94vw)"
    >
      <el-form label-position="top">
        <el-form-item label="渠道名称" required>
          <el-input v-model="channelForm.name" placeholder="如 值班群机器人" />
        </el-form-item>
        <el-form-item label="通知地址" required>
          <el-input
            v-model="channelForm.url"
            :placeholder="editingChannel ? '保持 *** 不变则沿用原地址' : 'https://…'"
          />
          <p v-if="editingChannel" class="channel-inline-hint">
            带 *** 的地址表示保留原值（不回填明文）。改投递目标请填写完整新地址。
          </p>
        </el-form-item>
        <el-form-item label="自定义请求头（每行 Key: Value）">
          <el-input
            v-model="channelForm.headersText"
            type="textarea"
            :rows="3"
            placeholder="Authorization: Bearer xxx"
          />
          <p class="channel-inline-hint">值为 *** 的行保留原值；未出现的行会被删除（整体替换）。</p>
        </el-form-item>
        <el-form-item label="重复提醒间隔（分钟，0 = 只在状态转变时通知）">
          <el-input-number v-model="channelForm.cooldownMinutes" :min="0" :max="10080" />
        </el-form-item>
        <el-form-item label="静默至（留空 = 不静默）">
          <el-date-picker
            v-model="channelForm.muteUntil"
            type="datetime"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            clearable
            style="width: 100%"
          />
          <p class="channel-inline-hint">静默期内不投递（运维窗口）；到期后不补发积压通知。</p>
        </el-form-item>
        <el-switch v-model="channelForm.enabled" active-text="启用渠道" />
      </el-form>
      <template #footer>
        <el-button @click="showChannel = false">取消</el-button>
        <el-button
          type="primary"
          :disabled="!channelForm.name.trim() || !channelForm.url.trim()"
          :loading="channelSaving"
          @click="saveChannel"
        >
          保存渠道
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>
<style scoped>
@import './list-page.css';
.observation-page {
  gap: 0;
}
.observation-page > .page-heading {
  margin-bottom: 20px;
}
.observation-page > .section-nav {
  margin-bottom: 16px;
}
.refresh-action,
.metric-toolbar,
.scope-bar,
.row-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.refresh-action span {
  color: var(--text-faint);
  font-size: 11px;
}
.scope-bar {
  padding: 12px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 16px;
}
.scope-bar label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-dim);
}
.scope-bar select {
  max-width: 330px;
}
.scope-bar input {
  width: 150px;
}
.scope-env {
  margin-left: auto;
  font-size: 12px;
  color: var(--text-dim);
}
.scope-env small {
  font-size: 11px;
}
select,
input:not([type='checkbox']) {
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  padding: 7px 10px;
  max-width: 100%;
}
input[type='checkbox'] {
  accent-color: var(--brand-solid);
}
.overview-heading,
.section-heading {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 20px;
  margin: 24px 0 20px;
}
.overview-heading h2 {
  font-size: 22px;
  margin: 8px 0;
}
.overview-heading p,
.section-heading p {
  color: var(--text-faint);
  font-size: 12px;
}
.eyebrow {
  color: var(--text-faint);
  font-size: 11px;
}
.health-table {
  min-width: 960px;
}
.health-table small {
  display: block;
  color: var(--text-faint);
  margin-top: 5px;
}
.chart-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 18px;
}
.metric-toolbar {
  justify-content: space-between;
}
/* 采集覆盖折叠区（spec 专项04 §5）*/
.quality-panel {
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  padding: 8px 12px;
  background: var(--bg-soft, var(--bg));
}
.quality-toggle {
  background: transparent;
  border: none;
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  padding: 2px 0;
}
.quality-toggle:hover {
  color: var(--accent);
}
.quality-hint {
  margin-left: 10px;
  font-size: 12px;
  color: var(--text-dim);
}
.quality-body {
  margin-top: 8px;
}
.quality-note {
  font-size: 12px;
  color: var(--text-dim);
  margin: 0 0 8px;
}
.quality-note.quality-warn {
  color: var(--warn, #b8860b);
}
.quality-table {
  width: 100%;
  font-size: 12.5px;
}
.quality-table th {
  text-align: left;
  color: var(--text-dim);
  font-weight: 500;
  padding: 4px 8px;
  border-bottom: 1px solid var(--border);
}
.quality-table td {
  padding: 5px 8px;
  border-bottom: 1px solid var(--border);
  color: var(--text);
}
.quality-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 12px;
}
.quality-badge.q-ok {
  color: #1a7f37;
  background: rgba(26, 127, 55, 0.12);
}
.quality-badge.q-empty {
  color: var(--text-dim);
  background: rgba(127, 127, 127, 0.12);
}
.quality-badge.q-failed {
  color: #c0392b;
  background: rgba(192, 57, 43, 0.12);
}
.metric-categories {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.metric-categories button {
  padding: 7px 12px;
  background: transparent;
  border: 1px solid transparent;
  color: var(--text-dim);
  border-radius: var(--radius-sm);
}
.metric-categories .active {
  background: var(--brand-soft);
  color: var(--brand);
  border-color: var(--brand);
}
.compare-switch {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
}
.data-note {
  font-size: 12px;
  line-height: 1.8;
  color: var(--text-faint);
  margin: 12px 0;
}
.loading-block {
  height: 180px;
}
.alert-count {
  padding: 1px 5px;
  background: var(--danger-soft);
  color: var(--danger);
  margin-left: 5px;
  border-radius: 4px;
}
.active-alerts {
  display: grid;
  gap: 8px;
  margin-bottom: 24px;
}
.active-alerts button {
  display: flex;
  align-items: center;
  gap: 14px;
  text-align: left;
  padding: 14px;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  border-radius: var(--radius-sm);
  flex-wrap: wrap;
}
.active-alerts button:hover {
  border-color: var(--brand);
}
.active-alerts button > span:last-child {
  margin-left: auto;
}
.subheading,
.section-heading h2 {
  font-size: 16px;
  margin: 24px 0 12px;
}
.rule-condition {
  display: flex;
  gap: 12px;
  margin-bottom: 20px;
}
.log-table {
  min-width: 1000px;
}
.log-message {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  min-width: 300px;
}
.trace-card {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  margin-bottom: 12px;
  overflow: hidden;
}
.trace-heading {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  border: 0;
  background: none;
  color: var(--text);
  width: 100%;
  padding: 16px;
  text-align: left;
}
.trace-heading code {
  font-size: 11px;
  color: var(--text-faint);
  overflow-wrap: anywhere;
}
.trace-heading > span:last-child {
  font-size: 11px;
  color: var(--text-faint);
  margin-left: auto;
}
.trace-body {
  border-top: 1px solid var(--border);
  padding: 16px;
}
.trace-body h3 {
  font-size: 13px;
}
.trace-body pre {
  font-size: 11px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.span-line {
  display: flex;
  gap: 12px;
  align-items: center;
  font-size: 11px;
  margin: 8px 0;
}
.span-label {
  flex: 0 0 38%;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  display: flex;
  align-items: center;
  gap: 3px;
}
/* 树线集中在单个缩进区，不再叠加 padding；最多占标签宽度的 25%。 */
.tree-guides {
  flex: none;
  width: var(--span-indent, 0px);
  height: 14px;
  display: flex;
  align-items: center;
  overflow: hidden;
  pointer-events: none;
}
.tree-line {
  flex: none;
  width: 16px;
  height: 14px;
  border-right: 1px dashed var(--border-strong);
  opacity: 0.45;
}
.span-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* span.kind 行内符号（⇅ 入口 / → 出站）：低存在感，悬停有完整语义。 */
.kind-glyph {
  flex: none;
  width: 14px;
  text-align: center;
  font-style: normal;
  color: var(--text-faint);
}
/* 泳道色点：非 default 泳道的 span 才点亮（基线不标，减少噪音）。 */
.lane-dot {
  flex: none;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--chart-violet);
}
/* 该 span 发生了泳道回退（route_reason=baseline-fallback）的小标记。 */
.fb-mark {
  flex: none;
  font-style: normal;
  font-size: 10px;
  color: var(--text-faint);
}
/* trace 头部上下文 chips：环境（teal）/ 泳道（violet）/ 基线（灰）/ 回退（warning）。 */
.trace-ctx {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
}
.ctx-chip {
  font-style: normal;
  font-size: 10.5px;
  line-height: 1;
  padding: 3px 9px;
  border-radius: 999px;
  color: var(--chart-violet);
  background: rgba(193, 162, 255, 0.13);
  border: 1px solid rgba(193, 162, 255, 0.32);
}
.ctx-chip.env {
  color: var(--chart-teal);
  background: rgba(92, 213, 186, 0.11);
  border-color: rgba(92, 213, 186, 0.3);
}
.ctx-chip.base {
  color: var(--text-dim);
  background: var(--surface-2);
  border-color: var(--border);
}
.ctx-chip.fb {
  color: var(--warning);
  background: var(--warning-soft);
  border-color: transparent;
}
.span-track {
  position: relative;
  height: 10px;
  flex: 1;
  background: var(--surface-2);
}
.span-track i {
  position: absolute;
  height: 100%;
  background: var(--brand-solid);
}
.span-track i.failed {
  background: var(--danger);
}
.span-line code {
  width: 70px;
}
.row-actions {
  flex-wrap: nowrap;
}
/* 通知渠道区块：状态态与说明文案（失败态用 danger，不与环境静默混淆） */
.channel-note {
  margin: -8px 0 12px;
  font-size: 12px;
  color: var(--text-faint);
  line-height: 1.6;
}
.channel-state {
  font-size: 12px;
  font-weight: 500;
}
.channel-state.ok {
  color: var(--success, #2f9e44);
}
.channel-state.warn {
  color: var(--warning, #b8860b);
}
.channel-state.muted {
  color: var(--text-faint);
}
.channel-actions {
  margin-top: 10px;
}
.channel-inline-hint {
  margin: 4px 0 0;
  font-size: 11.5px;
  color: var(--text-faint);
  line-height: 1.5;
}
.channel-inline-err {
  margin: 4px 0 0;
  font-size: 11.5px;
  color: var(--danger);
}
.trace-summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 12px;
  flex-wrap: wrap;
}
.trace-error {
  font-size: 12px;
  color: var(--danger);
  background: var(--danger-soft);
  padding: 10px;
  border-radius: var(--radius-sm);
}
.waterfall-scale {
  display: flex;
  justify-content: space-between;
  margin-left: 38%;
  font-size: 10px;
  color: var(--text-faint);
  padding: 6px 0;
  border-bottom: 1px dashed var(--border);
}
button.span-line {
  width: 100%;
  background: transparent;
  border: 1px solid transparent;
  color: var(--text);
  text-align: left;
  padding: 6px;
  border-radius: 4px;
}
button.span-line:hover,
button.span-line.selected {
  background: var(--brand-soft);
  border-color: var(--brand);
}
.span-inspector {
  padding: 16px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  margin: 16px 0;
}
.span-inspector dl {
  display: grid;
  grid-template-columns: minmax(100px, 30%) minmax(0, 1fr);
  gap: 8px;
  font-size: 12px;
}
.span-inspector dt {
  color: var(--text-faint);
  overflow-wrap: anywhere;
}
.span-inspector dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.span-inspector summary {
  font-size: 12px;
  cursor: pointer;
}
@media (max-width: 1100px) {
  .chart-grid {
    grid-template-columns: 1fr;
  }
  .scope-env {
    margin-left: 0;
  }
  .scope-bar select {
    max-width: 240px;
  }
}
@media (max-width: 600px) {
  .scope-bar label {
    width: 100%;
  }
  .scope-bar select,
  .scope-bar input {
    flex: 1;
    min-width: 0;
  }
  .overview-heading {
    align-items: start;
    flex-direction: column;
  }
  .metric-toolbar {
    align-items: start;
    flex-direction: column;
  }
  .refresh-action span {
    display: none;
  }
}
</style>
