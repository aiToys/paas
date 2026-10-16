<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
// 应用详情 - 动态配置（热更新）区块：应用维度动态配置（scope=app，按环境隔离）。
// 与上方 AppConfigs（工作负载级静态 env/Secret，重启注入）正交：本区块是
// 版本化动态配置——draft 编辑 → 发布出不可变快照 → 客户端按版本发现 → 可回滚。
//
// UX（对标 Nacos/Apollo 及格线 + 泳道灰度产品化，2026-08-30 L1+L2）：
// ① 页内显式 env tab（基线固定第一，承载 env 隔离前的存量草稿；与顶栏 scope 解耦，
//    本页 env 是局部状态，配置查看是读操作不改全局操作面）；
// ② 草稿 vs 生效三态列表（修改/新增高亮，数据前端 diff）；
// ③ 发布确认弹窗带完整 diff（prod 走 confirmDangerous 输入环境名）；
// ④ 发布目标二选一：基线（全量生效·新版本）或泳道（灰度验证·key 级覆盖即时生效）；
// ⑤ 灰度验证视图：覆盖 vs 基线对照 + 服务端真实 merge 预览 + 「提升到基线」。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useEnvStore, type Env } from '@/stores/env'
import { listLanes } from '@/api/lane'
import { apiError } from '@/api'
import {
  fetchAppDynamicConfigs, upsertAppDynamicConfig, deleteAppDynamicConfig,
  publishAppDynamicConfigs, fetchAppPublishes, fetchAppPublished, rollbackAppPublish,
  fetchLaneOverrides, upsertLaneOverride, deleteLaneOverride, promoteLaneOverrides,
  fetchSharedRefs, addSharedRef, deleteSharedRef, fetchNamespaces,
  type DynamicConfigItem, type ConfigPublish, type ConfigPublished, type LaneOverride, type SharedRef,
} from '@/api/configcenter'

const props = defineProps<{ appId: string }>()

const route = useRoute()
const envStore = useEnvStore()

// ---- 页内 env 选择（局部状态：query ?env= > 顶栏 scope > 基线）----
// 基线（id=''）固定第一个 tab：承载 env 隔离上线前的存量草稿。
interface EnvTab { id: string; name: string; isProd: boolean }
const selEnv = ref('') // '' = 基线
const envTabs = computed<EnvTab[]>(() => [
  { id: '', name: '基线', isProd: false },
  ...envStore.envs.map((e: Env) => ({ id: e.id, name: e.name, isProd: e.type === 'prod' })),
])
const curTab = computed(() => envTabs.value.find(t => t.id === selEnv.value) ?? envTabs.value[0])
const isProdEnv = computed(() => curTab.value.isProd)
const envLabel = computed(() => curTab.value.name)

// 各 env 的 active 版本（tab 徽标 + 状态条用）；key: envId（''=基线）
const envActiveVer = ref<Record<string, number | null>>({})
function setActiveVer(envId: string, pub: ConfigPublished | null) {
  envActiveVer.value = { ...envActiveVer.value, [envId]: pub?.published ? (pub.version ?? null) : null }
}
const curActiveVer = computed(() => envActiveVer.value[selEnv.value] ?? null)

// 顶部 prod 视觉提示跟随本页选中 env（与全局生产视觉语言一致）
const prodWarn = computed(() => isProdEnv.value ? '生产环境：发布/回滚/提升均需输入环境名确认' : '')

const items = ref<DynamicConfigItem[]>([])
const publishes = ref<ConfigPublish[]>([])
const published = ref<ConfigPublished | null>(null)
const loading = ref(false)
const publishing = ref(false)
const removing = ref('')
const rollingBack = ref('')

const showEdit = ref(false)
const submitting = ref(false)
const form = ref<{ id: string; key: string; value: string; type: string }>({ id: '', key: '', value: '', type: 'text' })

// ---- 草稿 vs 生效 diff（纯前端，数据都在手里）----
interface DiffRow {
  key: string
  type: string
  effective?: string   // 生效值（active snapshot），undefined = 新增
  draft: string
  state: 'modified' | 'added' | 'clean'
}
const diffRows = computed<DiffRow[]>(() => {
  const snap = published.value?.snapshot ?? {}
  // published.snapshot 含 shared 引用并入的 key（三层 merge 结果），这些 key 的真源在
  // shared ns——不参与「草稿 vs 生效」diff（否则被误显示为「发布后将移除」，且实际
  // 发布也不会移除它们——发布快照只含 draft，shared 层独立存在）。悬挂引用
  // （shared 已删）时其 key 已不在发现响应中，天然不进此分支。
  const sharedKeys = new Set(Object.keys(published.value?.sharedSnapshot ?? {}))
  const rows: DiffRow[] = []
  const seen = new Set<string>()
  for (const it of items.value) {
    seen.add(it.key)
    const eff = snap[it.key]
    rows.push({
      key: it.key, type: it.type, draft: it.value,
      effective: it.key in snap ? eff : undefined,
      state: !(it.key in snap) ? 'added' : (eff !== it.value ? 'modified' : 'clean'),
    })
  }
  // 生效有但 draft 已删的 key：提示「发布后将移除」（shared 来源除外）
  for (const k of Object.keys(snap)) {
    if (!seen.has(k) && !sharedKeys.has(k)) rows.push({ key: k, type: 'text', effective: snap[k], draft: '', state: 'modified' })
  }
  return rows
})
const pendingCount = computed(() => ({
  modified: diffRows.value.filter(r => r.state === 'modified').length,
  added: diffRows.value.filter(r => r.state === 'added').length,
}))
const hasPending = computed(() => pendingCount.value.modified + pendingCount.value.added > 0)

const types = [
  { value: 'text', label: 'Text' },
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
]

const snapshotEntries = (snap?: Record<string, string>) => (snap ? Object.entries(snap) : [])

async function load() {
  loading.value = true
  try {
    const [its, pubs, pub, refs] = await Promise.all([
      fetchAppDynamicConfigs(props.appId, selEnv.value),
      fetchAppPublishes(props.appId, selEnv.value),
      fetchAppPublished(props.appId, { envId: selEnv.value }).catch(() => null),
      fetchSharedRefs(props.appId, selEnv.value).catch(() => []),
    ])
    items.value = its ?? []
    publishes.value = pubs ?? []
    published.value = pub
    sharedRefs.value = refs ?? []
    setActiveVer(selEnv.value, pub)
  } catch (e) {
    ElMessage.error(apiError(e, '加载动态配置失败'))
  } finally {
    loading.value = false
  }
}

// ---- 共享配置引用子区（shared ns 作为发现 merge 基础层，应用自身 key 优先） ----
const sharedRefs = ref<SharedRef[]>([])
const showRefAdd = ref(false)
const refForm = ref<{ sharedNsId: string }>({ sharedNsId: '' })
const sharedOptions = ref<{ id: string; name: string }[]>([])

async function openRefAdd() {
  // 数据源：租户内 shared ns（排除已引用的）
  const [all, refs] = await Promise.all([
    fetchNamespaces().catch(() => []),
    Promise.resolve(sharedRefs.value),
  ])
  const used = new Set(refs.map(r => r.sharedNsId))
  sharedOptions.value = (all ?? []).filter(n => !used.has(n.id))
  refForm.value = { sharedNsId: '' }
  showRefAdd.value = true
}

async function submitRefAdd() {
  if (!refForm.value.sharedNsId) {
    ElMessage.warning('请选择共享配置')
    return
  }
  try {
    await addSharedRef(props.appId, refForm.value.sharedNsId, selEnv.value)
    ElMessage.success('已引用，发现时共享值自动合并进快照')
    showRefAdd.value = false
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '引用失败'))
  }
}

async function removeRef(r: SharedRef) {
  const ok = await confirmDangerous({ action: '解除引用', target: r.sharedName || r.sharedNsId, isProd: isProdEnv.value })
  if (!ok) return
  try {
    await deleteSharedRef(props.appId, r.id, selEnv.value)
    ElMessage.success('已解除引用')
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '解除失败'))
  }
}

// 轻量加载各 env 的 active 版本（tab 徽标；失败静默——非关键信息）
async function loadAllActiveVers() {
  for (const t of envTabs.value) {
    if (!t.id) continue
    fetchAppPublished(props.appId, { envId: t.id })
      .then(pub => setActiveVer(t.id, pub))
      .catch(() => setActiveVer(t.id, null))
  }
}

function switchTab(id: string) {
  selEnv.value = id
  // 同步到 URL query（可分享/刷新保持）。router.replace 会触发路由守卫/重渲染，
  // 这里只改 query 用 replaceState 拼 URL 字符串（保持当前 tab/path 不动）。
  const q = new URLSearchParams(route.query as Record<string, string>)
  if (id) q.set('env', id)
  else q.delete('env')
  const qs = q.toString()
  history.replaceState(null, '', window.location.pathname + (qs ? `?${qs}` : '') + window.location.hash)
}

// ---- 泳道（灰度验证）子区 ----
const lanes = ref<{ name: string; mode: string }[]>([])
const laneSel = ref('')
const overrides = ref<LaneOverride[]>([])
const laneLoading = ref(false)
const ovRemoving = ref('')
const showOvEdit = ref(false)
const ovSubmitting = ref(false)
const ovForm = ref<{ key: string; value: string }>({ key: '', value: '' })
// merge 预览：服务端 MergeSnapshot 真实结果（泳道实例实际拿到的完整配置）
const mergedPreview = ref<ConfigPublished | null>(null)

async function loadLanes() {
  if (!selEnv.value) { lanes.value = []; return }
  try {
    lanes.value = (await listLanes(selEnv.value) ?? [])
      .filter(l => l.status === 'active')
      .map(l => ({ name: l.name, mode: l.mode }))
  } catch { lanes.value = [] }
}

async function loadOverrides() {
  if (!laneSel.value || !selEnv.value) { overrides.value = []; mergedPreview.value = null; return }
  laneLoading.value = true
  try {
    const [ovs, merged] = await Promise.all([
      fetchLaneOverrides(props.appId, selEnv.value, laneSel.value),
      fetchAppPublished(props.appId, { envId: selEnv.value, lane: laneSel.value }).catch(() => null),
    ])
    overrides.value = ovs ?? []
    mergedPreview.value = merged
  } catch (e) {
    ElMessage.error(apiError(e, '加载泳道覆盖失败'))
  } finally {
    laneLoading.value = false
  }
}

watch(laneSel, loadOverrides)

function openAdd() {
  form.value = { id: '', key: '', value: '', type: 'text' }
  showEdit.value = true
}

function openEdit(row: DiffRow) {
  // 从 diff 行编辑：查 draft item（「发布后移除」的行无 draft，不可编辑，只能重新新增）
  const it = items.value.find(i => i.key === row.key)
  if (!it) { openAdd(); form.value.key = row.key; return }
  form.value = { id: it.id, key: it.key, value: it.value, type: it.type }
  showEdit.value = true
}

async function submit() {
  if (!form.value.key.trim() || !form.value.value) {
    ElMessage.warning('请填写 Key 和 Value')
    return
  }
  submitting.value = true
  try {
    await upsertAppDynamicConfig(props.appId, {
      key: form.value.key.trim(),
      value: form.value.value,
      type: form.value.type,
    }, selEnv.value)
    ElMessage.success('已保存草稿，发布后生效')
    showEdit.value = false
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '保存失败'))
  } finally {
    submitting.value = false
  }
}

async function remove(row: DiffRow) {
  const it = items.value.find(i => i.key === row.key)
  if (!it) return
  const ok = await confirmDangerous({
    action: '删除动态配置项',
    target: row.key,
    requireNameConfirm: isProdEnv.value,
  })
  if (!ok) return
  removing.value = it.id
  try {
    await deleteAppDynamicConfig(props.appId, it.id, selEnv.value)
    ElMessage.success('已删除草稿，发布后生效')
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  } finally {
    removing.value = ''
  }
}

// ---- 发布：确认弹窗带 diff + 目标二选一（基线/泳道）----
const showPub = ref(false)
const pubTarget = ref<'baseline' | 'lane'>('baseline')
const pubLane = ref('')

function openPublish() {
  if (!hasPending.value) {
    ElMessage.info('草稿与生效版本一致，无需发布')
    return
  }
  pubTarget.value = 'baseline'
  pubLane.value = lanes.value[0]?.name ?? ''
  showPub.value = true
}

async function confirmPublish() {
  showPub.value = false
  // 泳道目标：逐项写 key 级覆盖（即时生效，无版本记录）
  if (pubTarget.value === 'lane') {
    if (!pubLane.value) { ElMessage.warning('请选择泳道'); showPub.value = true; return }
    const changed = diffRows.value.filter(r => r.state !== 'clean')
    const ok = await confirmDangerous({
      action: `发布 ${changed.length} 项覆盖到泳道`,
      target: pubLane.value,
      requireNameConfirm: isProdEnv.value,
    })
    if (!ok) { showPub.value = true; return }
    publishing.value = true
    try {
      for (const r of changed) {
        if (r.state === 'added') {
          // 新增 key 基线无值：先写基线 draft（占位当前值），再写泳道覆盖——
          // 保证提升到基线时该 key 有 draft 可合并。
          await upsertAppDynamicConfig(props.appId, { key: r.key, value: r.draft, type: r.type }, selEnv.value)
        }
        await upsertLaneOverride(props.appId, selEnv.value, pubLane.value, { key: r.key, value: r.draft })
      }
      ElMessage.success(`已发布 ${changed.length} 项覆盖到泳道 ${pubLane.value}（即时生效，基线不变）`)
      await load()
      laneSel.value = pubLane.value
    } catch (e) {
      ElMessage.error(apiError(e, '泳道发布失败'))
    } finally {
      publishing.value = false
    }
    return
  }
  // 基线目标：新版本（prod 需输入环境名）
  const ok = await confirmDangerous({
    action: '发布动态配置',
    target: envLabel.value || props.appId,
    requireNameConfirm: isProdEnv.value,
  })
  if (!ok) { showPub.value = true; return }
  publishing.value = true
  try {
    const pub = await publishAppDynamicConfigs(props.appId, selEnv.value)
    ElMessage.success(`已发布 v${pub.version}`)
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '发布失败'))
  } finally {
    publishing.value = false
  }
}

async function rollback(p: ConfigPublish) {
  // 回滚会将草稿重置为目标版本快照——真有未发布编辑时显式警示（防静默丢弃）
  const pending = pendingCount.value.modified + pendingCount.value.added
  const pendingHint = pending > 0
    ? `当前有 ${pending} 项未发布的草稿修改，回滚后将被重置为 v${p.version} 的内容。`
    : ''
  const ok = await confirmDangerous({
    action: '回滚到', target: `v${p.version}`, requireNameConfirm: isProdEnv.value,
    message: pendingHint,
  })
  if (!ok) return
  rollingBack.value = p.id
  try {
    await rollbackAppPublish(props.appId, p.id, selEnv.value)
    ElMessage.success(`已回滚到 v${p.version}，草稿已同步为该版本内容`)
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '回滚失败'))
  } finally {
    rollingBack.value = ''
  }
}

// ---- 泳道覆盖操作（即时生效，无「发布」步骤）----

function openOvAdd() {
  ovForm.value = { key: '', value: '' }
  showOvEdit.value = true
}

async function submitOverride() {
  if (!ovForm.value.key.trim() || !ovForm.value.value) {
    ElMessage.warning('请填写 Key 和 Value')
    return
  }
  const ok = await confirmDangerous({
    action: '保存泳道覆盖', target: `${laneSel.value} / ${ovForm.value.key}`,
    requireNameConfirm: isProdEnv.value,
  })
  if (!ok) return
  ovSubmitting.value = true
  try {
    await upsertLaneOverride(props.appId, selEnv.value, laneSel.value, {
      key: ovForm.value.key.trim(),
      value: ovForm.value.value,
    })
    ElMessage.success('覆盖已生效（即时生效，无需发布）')
    showOvEdit.value = false
    loadOverrides()
  } catch (e) {
    ElMessage.error(apiError(e, '保存失败'))
  } finally {
    ovSubmitting.value = false
  }
}

async function removeOverride(row: LaneOverride) {
  const ok = await confirmDangerous({
    action: '删除泳道覆盖',
    target: row.key,
    requireNameConfirm: isProdEnv.value,
  })
  if (!ok) return
  ovRemoving.value = row.key
  try {
    await deleteLaneOverride(props.appId, selEnv.value, laneSel.value, row.key)
    loadOverrides()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  } finally {
    ovRemoving.value = ''
  }
}

// 提升：覆盖合并进基线草稿 + 发新版本 + 清覆盖（后端单端点完成）
const promoting = ref(false)
async function promoteLane() {
  if (!laneSel.value || !overrides.value.length) return
  const ok = await confirmDangerous({
    action: `提升泳道 ${laneSel.value} 的 ${overrides.value.length} 项覆盖到基线`,
    target: envLabel.value || props.appId,
    requireNameConfirm: isProdEnv.value,
  })
  if (!ok) return
  promoting.value = true
  try {
    const pub = await promoteLaneOverrides(props.appId, selEnv.value, laneSel.value)
    ElMessage.success(`已提升到基线并发布 v${pub.version}（泳道覆盖已清空）`)
    await Promise.all([load(), loadOverrides()])
  } catch (e) {
    ElMessage.error(apiError(e, '提升失败（泳道覆盖保持不变，可重试）'))
  } finally {
    promoting.value = false
  }
}

function init() {
  // 初值：query ?env= > 顶栏 scope env > 基线
  const q = route.query.env
  selEnv.value = typeof q === 'string' ? q : (envStore.currentEnvId || '')
  load()
  loadLanes()
  laneSel.value = ''
  overrides.value = []
}

onMounted(async () => {
  await envStore.loadEnvs()
  init()
  loadAllActiveVers()
})
watch(() => props.appId, init)
watch(selEnv, () => { load(); loadLanes() })
</script>

<template>
  <div class="dyn-block" v-loading="loading">
    <!-- 页内显式 env tab（与顶栏 scope 解耦；基线固定第一，承载存量草稿） -->
    <div class="env-tabs">
      <button
        v-for="t in envTabs" :key="t.id"
        class="env-tab" :class="{ active: t.id === selEnv, prod: t.isProd }"
        @click="switchTab(t.id)"
      >
        {{ t.name }}
        <span v-if="envActiveVer[t.id]" class="tab-ver mono">v{{ envActiveVer[t.id] }}</span>
        <span v-else class="tab-ver none">未发布</span>
      </button>
    </div>
    <div v-if="prodWarn" class="prod-warn">{{ prodWarn }}</div>

    <div class="block-head">
      <div>
        <span class="block-title">动态配置（热更新）</span>
        <span class="block-hint">{{ envLabel }}（按环境独立）· 草稿编辑后发布生效</span>
      </div>
      <div>
        <span v-if="curActiveVer" class="ver-tag mono" style="margin-right: 10px">生效中 v{{ curActiveVer }}</span>
        <span v-else class="none" style="margin-right: 10px">未发布</span>
        <el-button size="small" type="primary" :disabled="!hasPending" :loading="publishing" @click="openPublish">
          发布{{ hasPending ? `（${pendingCount.modified + pendingCount.added} 项变更）` : '' }}
        </el-button>
        <el-button size="small" @click="openAdd">+ 新增配置</el-button>
      </div>
    </div>

    <!-- 配置项：草稿 vs 生效三态（修改/新增高亮） -->
    <section class="cfg-group">
      <div class="group-title">
        配置项<span class="group-cnt mono">{{ items.length }}</span>
        <span v-if="hasPending" class="pending-hint">
          {{ pendingCount.modified }} 修改 · {{ pendingCount.added }} 新增（待发布）
        </span>
      </div>
      <el-table :data="diffRows" size="small" empty-text="动态配置用于运行时热更新（无需重启）。添加第一项配置后发布生效。">
        <el-table-column prop="key" label="Key" min-width="170">
          <template #default="{ row }">
            <span class="mono">{{ row.key }}</span>
            <el-tag v-if="row.state === 'modified'" type="warning" size="small" style="margin-left: 6px">已修改</el-tag>
            <el-tag v-else-if="row.state === 'added'" type="success" size="small" style="margin-left: 6px">新增</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="生效值" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.effective !== undefined" class="mono dim">{{ row.effective }}</span>
            <span v-else class="none">—（无）</span>
          </template>
        </el-table-column>
        <el-table-column label="草稿值" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.draft" class="mono" :class="{ hl: row.state !== 'clean' }">{{ row.draft }}</span>
            <span v-else class="none">（发布后将移除）</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">{{ row.draft ? '编辑' : '重新新增' }}</el-button>
            <el-button v-if="row.draft" text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 发布历史 -->
    <section class="cfg-group" v-if="publishes.length">
      <div class="group-title">发布历史</div>
      <el-table :data="publishes" size="small">
        <el-table-column label="版本" width="80">
          <template #default="{ row }"><span class="mono">v{{ row.version }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="配置项数" width="100">
          <template #default="{ row }">{{ snapshotEntries(row.snapshot).length }}</template>
        </el-table-column>
        <el-table-column label="发布时间" width="180">
          <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-button v-if="row.status !== 'active'" text type="warning" size="small" :loading="rollingBack === row.id" @click="rollback(row)">回滚到此</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 共享配置引用（shared ns 作为发现 merge 基础层；应用自身 key 优先） -->
    <section class="cfg-group">
      <div class="group-title">
        共享配置引用<span class="group-cnt mono">{{ sharedRefs.length }}</span>
        <span class="lane-hint">共享值作为底层默认并入发现快照 · 应用自身配置优先于共享值</span>
        <el-button size="small" style="margin-left: auto" @click="openRefAdd">+ 引用共享配置</el-button>
      </div>
      <el-table :data="sharedRefs" size="small" empty-text="未引用共享配置。引用后共享值自动并入本应用发现快照（应用同名 key 优先）。">
        <el-table-column label="共享配置" min-width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.sharedName || row.sharedNsId }}</span>
            <el-tag v-if="!row.sharedName" type="danger" size="small" style="margin-left: 6px">已删除</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="生效版本" width="100">
          <template #default="{ row }">
            <span v-if="row.sharedVersion" class="mono">v{{ row.sharedVersion }}</span>
            <span v-else class="none">未发布</span>
          </template>
        </el-table-column>
        <el-table-column label="配置项数" width="90">
          <template #default="{ row }">{{ row.sharedKeys ?? 0 }}</template>
        </el-table-column>
        <el-table-column label="引用时间" width="180">
          <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button text type="danger" size="small" @click="removeRef(row)">解除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 泳道灰度验证（key 级覆盖，即时生效，随泳道回收消失；验证后可提升到基线） -->
    <section class="cfg-group" v-if="selEnv">
      <div class="group-title">
        泳道灰度验证
        <span class="lane-hint">先发泳道验证 → 提升到基线 · 覆盖即时生效 · 随泳道回收消失</span>
      </div>
      <div class="lane-bar">
        <el-select v-model="laneSel" placeholder="选择泳道" size="small" style="width: 240px" clearable>
          <el-option v-for="l in lanes" :key="l.name" :value="l.name" :label="l.name + (l.mode === 'permanent' ? '（常驻）' : '')" />
        </el-select>
        <el-button size="small" :disabled="!laneSel" @click="openOvAdd">+ 单项覆盖</el-button>
        <el-button v-if="laneSel && overrides.length" size="small" type="warning" :loading="promoting" @click="promoteLane">
          提升到基线（{{ overrides.length }} 项）
        </el-button>
      </div>
      <template v-if="laneSel">
        <el-table
v-loading="laneLoading" :data="overrides" size="small"
          empty-text="该泳道暂无覆盖。可在发布弹窗选择「泳道」目标，把当前待发布变更作为覆盖先发到此泳道验证。"
>
          <el-table-column prop="key" label="Key" min-width="150">
            <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
          </el-table-column>
          <el-table-column label="覆盖值" min-width="160" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono hl">{{ row.value }}</span></template>
          </el-table-column>
          <el-table-column label="基线值" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="published?.snapshot && row.key in published.snapshot" class="mono dim">{{ published.snapshot[row.key] }}</span>
              <span v-else class="none">—（基线无此 key）</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button text type="danger" size="small" :loading="ovRemoving === row.key" @click="removeOverride(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <!-- 服务端真实 merge 预览：泳道实例实际拿到的完整配置 -->
        <div v-if="mergedPreview?.published" class="merge-preview">
          <div class="mp-title">泳道实例实际生效（服务端 merge 结果 · v{{ mergedPreview.version }}<template v-if="mergedPreview.overrideHash"> + 覆盖</template>）：</div>
          <div class="mp-body mono">
            <div v-for="[k, v] in snapshotEntries(mergedPreview.snapshot)" :key="k">
              <span class="dim">{{ k }}</span> = <span :class="{ hl: overrides.some(o => o.key === k) }">{{ v }}</span>
            </div>
          </div>
        </div>
      </template>
    </section>

    <!-- 编辑弹窗（基线 draft） -->
    <el-dialog v-model="showEdit" :title="form.id ? '编辑动态配置' : '新增动态配置'" width="500px">
      <el-form label-width="70px">
        <el-form-item label="Key">
          <el-input v-model="form.key" :disabled="!!form.id" placeholder="如 feature.newui" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%">
            <el-option v-for="t in types" :key="t.value" :label="t.label" :value="t.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="Value">
          <el-input v-model="form.value" type="textarea" :rows="4" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEdit = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="submit">
          {{ submitting ? '保存中…' : '保存' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- 泳道单项覆盖弹窗 -->
    <el-dialog v-model="showOvEdit" title="新增泳道覆盖" width="500px">
      <div class="ov-hint">覆盖即时生效：带泳道 <code>{{ laneSel }}</code> 发现的客户端立即拿到该值；其余实例不受影响。</div>
      <el-form label-width="70px">
        <el-form-item label="Key">
          <el-input v-model="ovForm.key" placeholder="如 recommend_topk" />
        </el-form-item>
        <el-form-item label="覆盖值">
          <el-input v-model="ovForm.value" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showOvEdit = false">取消</el-button>
        <el-button type="primary" :disabled="ovSubmitting" @click="submitOverride">
          {{ ovSubmitting ? '保存中…' : '保存并生效' }}
        </el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showRefAdd" title="引用共享配置" width="480px">
      <div class="ov-hint">引用后共享配置的已发布值自动并入本应用发现快照（<b>应用自身同名 key 优先</b>，可覆盖共享默认值）。共享配置重新发布后自动热更新，不改变应用自身版本号。</div>
      <el-form label-width="90px">
        <el-form-item label="共享配置">
          <el-select v-model="refForm.sharedNsId" placeholder="选择共享配置" style="width: 100%" filterable>
            <el-option v-for="n in sharedOptions" :key="n.id" :value="n.id" :label="n.name" />
          </el-select>
        </el-form-item>
      </el-form>
      <div v-if="!sharedOptions.length" class="none" style="padding: 8px 0 0">
        暂无可引用的共享配置。可在「平台能力 → 配置中心 → 共享配置」创建。
      </div>
      <template #footer>
        <el-button @click="showRefAdd = false">取消</el-button>
        <el-button type="primary" :disabled="!refForm.sharedNsId" @click="submitRefAdd">引用</el-button>
      </template>
    </el-dialog>

    <!-- 发布确认弹窗（带 diff + 目标二选一） -->
    <el-dialog v-model="showPub" title="发布动态配置" width="620px">
      <div class="pub-target">
        <span class="pt-label">发布到：</span>
        <el-radio-group v-model="pubTarget">
          <el-radio value="baseline">基线（全量生效 · 新版本 v{{ (curActiveVer ?? 0) + 1 }}）</el-radio>
          <el-radio value="lane" :disabled="!lanes.length">泳道（灰度验证 · 仅该泳道实例）</el-radio>
        </el-radio-group>
        <el-select v-if="pubTarget === 'lane'" v-model="pubLane" size="small" style="width: 220px; margin-left: 12px" placeholder="选择泳道">
          <el-option v-for="l in lanes" :key="l.name" :value="l.name" :label="l.name + (l.mode === 'permanent' ? '（常驻）' : '')" />
        </el-select>
      </div>
      <div class="pub-diff-title">
        即将发布 {{ pendingCount.modified + pendingCount.added }} 项变更
        <template v-if="curActiveVer">（v{{ curActiveVer }} → v{{ curActiveVer + 1 }}）</template>：
      </div>
      <div class="pub-diff">
        <div v-for="r in diffRows.filter(x => x.state !== 'clean')" :key="r.key" class="diff-row">
          <el-tag :type="r.state === 'added' ? 'success' : 'warning'" size="small">{{ r.state === 'added' ? '新增' : '修改' }}</el-tag>
          <span class="mono dk">{{ r.key }}</span>
          <span v-if="r.effective !== undefined" class="mono dim">{{ r.effective }}</span>
          <span v-if="r.effective !== undefined" class="arrow">→</span>
          <span class="mono hl">{{ r.draft || '（移除）' }}</span>
        </div>
      </div>
      <div v-if="pubTarget === 'lane'" class="ov-hint" style="margin-top: 10px">
        泳道发布以 key 级覆盖写入（即时生效、无版本记录、随泳道回收消失）；验证通过后在「泳道灰度验证」区提升到基线。
      </div>
      <template #footer>
        <el-button @click="showPub = false">取消</el-button>
        <el-button type="primary" :disabled="publishing" @click="confirmPublish">
          {{ publishing ? '发布中…' : (pubTarget === 'lane' ? `发布到泳道` : '确认发布') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.dyn-block { margin-top: 8px; }
.env-tabs { display: flex; gap: 6px; margin-bottom: 12px; flex-wrap: wrap; }
.env-tab {
  padding: 5px 14px; border: 1px solid var(--border, #dcdfe6); border-radius: 6px;
  background: transparent; cursor: pointer; font-size: 12.5px; color: var(--text-dim, #909399);
  display: inline-flex; align-items: center; gap: 6px;
}
.env-tab.active { border-color: var(--el-color-primary); color: var(--el-color-primary); font-weight: 600; }
.env-tab.prod { border-left: 3px solid var(--danger, #f56c6c); }
.env-tab.prod.active { color: var(--danger, #f56c6c); border-color: var(--danger, #f56c6c); }
.tab-ver { font-size: 11px; }
.prod-warn {
  font-size: 12px; color: var(--danger, #f56c6c); margin-bottom: 10px;
  padding: 6px 10px; background: rgba(245, 108, 108, 0.08); border-radius: 4px;
}
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.block-title { font-size: 14px; font-weight: 600; margin-right: 10px; }
.block-hint { font-size: 12px; color: var(--text-dim); }
.cfg-group { margin-bottom: 20px; }
.group-title {
  display: flex; align-items: center; gap: 8px;
  font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px;
}
.group-cnt { font-size: 11px; color: var(--text-faint); padding: 1px 7px; background: var(--surface-2, transparent); border-radius: 8px; }
.pending-hint { font-size: 11.5px; font-weight: 400; color: var(--warning, #e6a23c); }
.lane-hint { font-size: 11.5px; font-weight: 400; color: var(--text-faint); }
.lane-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.ver-tag { padding: 2px 8px; background: var(--success-soft); color: var(--success); border-radius: 4px; font-size: 12px; }
.none { font-size: 12px; color: var(--text-faint); font-weight: 400; }
.ov-hint { font-size: 12.5px; color: var(--text-dim); margin-bottom: 12px; }
.mono { font-family: var(--font-mono, monospace); }
.mono.dim { color: var(--text-faint); }
.mono.hl { background: rgba(230, 162, 60, 0.12); border-radius: 3px; padding: 0 3px; }
.merge-preview { margin-top: 10px; padding: 10px 12px; border: 1px dashed var(--border, #dcdfe6); border-radius: 6px; }
.mp-title { font-size: 12px; color: var(--text-dim); margin-bottom: 6px; }
.mp-body { font-size: 11.5px; max-height: 180px; overflow: auto; line-height: 1.8; }
.mp-body .dim { color: var(--text-faint); }
.pub-target { display: flex; align-items: center; margin-bottom: 14px; flex-wrap: wrap; }
.pt-label { font-size: 13px; margin-right: 8px; }
.pub-diff-title { font-size: 13px; margin-bottom: 8px; }
.pub-diff { max-height: 260px; overflow: auto; border: 1px solid var(--border, #dcdfe6); border-radius: 6px; padding: 8px 12px; }
.diff-row { display: flex; align-items: center; gap: 10px; padding: 4px 0; font-size: 12px; }
.diff-row .dk { font-weight: 600; min-width: 120px; }
.diff-row .arrow { color: var(--text-faint); }
</style>
