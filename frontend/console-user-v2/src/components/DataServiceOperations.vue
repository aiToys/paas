<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchList, fetchCommand, fetchAuth, apiError } from '@/api'
import { listMetrics, type MetricSeries } from '@/api/observability'
import { usePolling } from '@/composables/usePolling'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { confirmAction } from '@/composables/useConfirmAction'
import { useEnvStore } from '@/stores/env'
import { dataServiceCommand } from '@/domain/dataservice'
import MetricChart from './MetricChart.vue'
import StatePanel from './StatePanel.vue'
const props = defineProps<{
  service: {
    id: string
    name: string
    kind: string
    envId: string
    image?: string
    source?: string
  }
}>()
const emit = defineEmits<{ (e: 'changed'): void }>()
const env = useEnvStore(),
  tab = ref('metrics'),
  error = ref(''),
  loading = ref(false),
  acting = ref(false),
  metrics = ref<MetricSeries[]>([])
const tabs = computed(() => [
  { id: 'metrics', label: '指标' },
  { id: 'pods', label: '运行诊断' },
  { id: 'backup', label: '备份记录' },
  ...(props.service.kind === 'mq' ? [{ id: 'topics', label: '消息主题' }] : []),
])
interface Pod {
  name: string
  status: string
  ready: string
  restarts: number
  node?: string
  message?: string
}
interface Backup {
  id: string
  type: string
  status: string // accepted|running|verifying|completed|failed
  mode?: string // record|snapshot|restore（''=存量记录）
  sizeMB: number
  sizeBytes?: number
  checksum?: string
  error?: string
  sourceBackupId?: string
  restoredResourceId?: string
  createdAt: string
}
interface Caps {
  backup: { supported: boolean; mode: string; reason: string }
  restore: { supported: boolean; mode: string; reason: string }
}
interface Topic {
  id: string
  name: string
  partitions: number
  retention: string
  status: string
}
interface Group {
  id: string
  name: string
  mode: string
  members: number
}
const pods = ref<Pod[]>([]),
  backups = ref<Backup[]>([]),
  caps = ref<Caps | null>(null),
  topics = ref<Topic[]>([]),
  groups = ref<Record<string, Group[]>>({})
const names = computed(() => ({
  ['dataservice:' + props.service.id]: props.service.name,
}))
const range = ref('1h'),
  image = ref(props.service.image || ''),
  newTopic = ref({ name: '', partitions: 1, retention: '7d' }),
  newGroup = ref<Record<string, { name: string; mode: string }>>({})
const isManaged = computed(
  () => !props.service.source || props.service.source === 'managed',
)
let seq = 0
async function load() {
  const rev = ++seq
  loading.value = true
  error.value = ''
  try {
    if (tab.value === 'metrics') {
      const v = await listMetrics({
        targetType: 'dataservice',
        targetId: props.service.id,
        range: range.value,
      })
      if (rev === seq) metrics.value = v
    }
    if (tab.value === 'pods') {
      const v = await fetchList<Pod>(
        `/api/dataservices/${props.service.id}/pods`,
      )
      if (rev === seq) pods.value = v
    }
    if (tab.value === 'backup') {
      const [v, c] = await Promise.all([
        fetchList<Backup>(
          `/api/backups?resourceId=${encodeURIComponent(props.service.id)}`,
        ),
        fetchAuth(
          `/api/dataservices/${encodeURIComponent(props.service.id)}/capabilities`,
        )
          .then((r) => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
          .then((j) => j.data as Caps)
          .catch(() => null), // 能力面失败不阻塞列表（按钮按 unknown 处理）
      ])
      if (rev === seq) {
        backups.value = v
        caps.value = c
      }
    }
    if (tab.value === 'topics') {
      const v = await fetchList<Topic>(
        `/api/mq-topics?mqId=${encodeURIComponent(props.service.id)}`,
      )
      if (rev === seq) topics.value = v
    }
  } catch (e) {
    if (rev === seq) error.value = apiError(e)
  } finally {
    if (rev === seq) loading.value = false
  }
}
watch([tab, range], load)
usePolling(load, 15000, {
  active: () => tab.value === 'metrics' || tab.value === 'pods',
})
async function mutate(task: () => Promise<unknown>, message: string) {
  if (acting.value) return
  acting.value = true
  try {
    await task()
    ElMessage.success(message)
    await load()
    emit('changed')
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    acting.value = false
  }
}
// isProdEnv 按资源实际环境判定（非顶栏 scope——资源可能在其他环境）。
const isProdEnv = computed(
  () => env.envs.find((e) => e.id === props.service.envId)?.type === 'prod',
)
async function upgrade() {
  if (!image.value.trim()) return
  const isProd = isProdEnv.value
  if (
    !(await confirmDangerous({
      action: '升级镜像',
      target: props.service.name,
      isProd,
      requireNameConfirm: isProd,
      message: `目标镜像：${image.value}。环境：${props.service.envId}。`,
    }))
  )
    return
  const r = dataServiceCommand(props.service.id, 'upgrade', {
    image: image.value.trim(),
  })
  await mutate(() => fetchCommand(r.path, r.options), '升级请求已提交')
}
// 备份模式标签/状态中文映射（mode ''=存量记录按 record 解释，与后端 EffectiveMode 一致）。
function modeLabel(b: Backup): string {
  const m = b.mode || 'record'
  return m === 'snapshot' ? '快照' : m === 'restore' ? '恢复' : '记录'
}
function statusLabel(s: string): string {
  return (
    {
      accepted: '已受理',
      running: '执行中',
      verifying: '校验中',
      completed: '已完成',
      failed: '失败',
    }[s] || s
  )
}
function sizeText(b: Backup): string {
  if (b.sizeBytes && b.sizeBytes > 0) {
    return b.sizeBytes >= 1048576
      ? (b.sizeBytes / 1048576).toFixed(1) + ' MB'
      : b.sizeBytes + ' B'
  }
  return '—' // 记录模式无产物大小（不显示历史模拟值）
}
const hasActiveJob = computed(() =>
  backups.value.some((b) => !['completed', 'failed'].includes(b.status)),
)
async function createBackup(mode: 'record' | 'snapshot') {
  if (mode === 'snapshot') {
    const ok = await confirmDangerous({
      action: '执行真实备份',
      target: props.service.name,
      isProd: isProdEnv.value,
      requireNameConfirm: isProdEnv.value,
      message:
        '将对该数据服务执行引擎级快照（pg_dump），产物落平台 PVC 并记录校验和。执行期间会产生额外 I/O 负载，请按容量选择时间。',
    })
    if (!ok) return
  }
  await mutate(
    () =>
      fetchCommand('/api/backups', {
        method: 'POST',
        body: JSON.stringify({ resourceId: props.service.id, type: 'full', mode }),
      }),
    mode === 'snapshot'
      ? '备份已受理（快照作业执行中，完成后列表更新）'
      : '备份记录已创建（记录模式：仅登记意图，无产物无恢复证明）',
  )
}
const restoreKeys = new Map<string, string>()
async function restoreBackup(b: Backup) {
  if (backups.value.some(row => row.sourceBackupId === b.id && !['completed', 'failed'].includes(row.status))) { ElMessage.info('该快照已有恢复任务，请先查看操作记录'); return }
  const ok = await confirmDangerous({
    action: '从快照恢复',
    target: props.service.name,
    isProd: isProdEnv.value,
    requireNameConfirm: isProdEnv.value,
    message:
      '将创建一个新的数据服务实例并从快照恢复数据（原实例不受影响，不支持覆盖恢复）。恢复期间新实例经历拉起与导入，耗时取决于数据量。',
  })
  if (!ok) return
  await mutate(
    async () => {
      const storageKey = 'paas.restore.' + b.id
      let key = restoreKeys.get(b.id)
      try { key ||= sessionStorage.getItem(storageKey) || undefined } catch { /* 存储受限时保留内存重试键 */ }
      if (!key) key = Array.from(crypto.getRandomValues(new Uint32Array(4)), n => n.toString(16).padStart(8, '0')).join('')
      restoreKeys.set(b.id, key)
      try { sessionStorage.setItem(storageKey, key) } catch { /* 不影响请求 */ }
      await fetchCommand('/api/backups/' + b.id + '/restore', { method: 'POST', headers: { 'Idempotency-Key': key }, body: '{}' })
      restoreKeys.delete(b.id)
      try { sessionStorage.removeItem(storageKey) } catch { /* 不影响受理结果 */ }
    },
    '恢复已受理（新实例创建与数据导入执行中，完成后显示新实例 ID；文件完整性与表读取检查不等于业务数据一致性验收）',
  )
}
async function removeBackup(b: Backup) {
  const hint =
    b.mode === 'snapshot'
      ? '删除的记录对应的 PVC 产物仍保留（产物清理待后续提供）'
      : ''
  if (await confirmAction(`删除备份记录「${b.id}」？${hint}`))
    await mutate(
      () => fetchCommand('/api/backups/' + b.id, { method: 'DELETE' }),
      '记录已删除',
    )
}
async function createTopic() {
  if (!newTopic.value.name.trim()) return
  await mutate(
    () =>
      fetchCommand('/api/mq-topics', {
        method: 'POST',
        body: JSON.stringify({ mqId: props.service.id, ...newTopic.value }),
      }),
    '主题已创建',
  )
}
async function removeTopic(t: Topic) {
  if (await confirmAction(`删除消息主题「${t.name}」？`))
    await mutate(
      () => fetchCommand('/api/mq-topics/' + t.id, { method: 'DELETE' }),
      '主题已删除',
    )
}
async function loadGroups(t: Topic) {
  try {
    groups.value[t.id] = await fetchList<Group>(
      '/api/consumer-groups?topicId=' + encodeURIComponent(t.id),
    )
    newGroup.value[t.id] ||= { name: '', mode: 'clustering' }
  } catch (e) {
    ElMessage.error(apiError(e))
  }
}
async function createGroup(t: Topic) {
  const f = newGroup.value[t.id]
  if (!f?.name.trim()) return
  await mutate(
    () =>
      fetchCommand('/api/consumer-groups', {
        method: 'POST',
        body: JSON.stringify({ topicId: t.id, ...f }),
      }),
    '消费组已创建',
  )
  await loadGroups(t)
}
async function removeGroup(t: Topic, g: Group) {
  if (!(await confirmAction(`删除消费组「${g.name}」？`))) return
  await mutate(
    () => fetchCommand('/api/consumer-groups/' + g.id, { method: 'DELETE' }),
    '消费组已删除',
  )
  await loadGroups(t)
}
</script>
<template>
  <section class="operations">
    <nav class="section-nav" aria-label="数据服务运维">
      <button
        v-for="t in tabs"
        :key="t.id"
        :class="{ active: tab === t.id }"
        @click="tab = t.id"
      >
        {{ t.label }}
      </button>
    </nav>
    <div class="ops-heading">
      <p>
        {{
          tab === 'metrics'
            ? '仅展示此实例实际采集到的指标，不把缺失数据补成 0。'
            : tab === 'backup'
              ? '目前仅登记备份记录（状态「已记录」≠ 快照已执行/可恢复），不执行数据库备份或存储快照。'
              : tab === 'pods'
                ? 'Pod 状态来自集群；未接入集群时无法提供诊断。'
                : '消息主题与消费组管理'
        }}
      </p>
      <button class="act" @click="load">刷新</button>
    </div>
    <div v-if="tab === 'metrics'" class="ops-controls">
      <select v-model="range" aria-label="数据服务指标时间">
        <option value="15m">15 分钟</option>
        <option value="1h">1 小时</option>
        <option value="6h">6 小时</option>
        <option value="24h">24 小时</option>
</select><RouterLink
        :to="{
          path: '/observability',
          query: { ds: service.id, tab: 'metrics', env: service.envId },
        }"
        class="act"
        >
完整指标工作台 →
</RouterLink><RouterLink
        :to="{
          path: '/observability',
          query: { ds: service.id, tab: 'alerts', env: service.envId },
        }"
        class="act"
        >
配置实例告警 →
</RouterLink>
    </div>
    <StatePanel v-if="error" title="运维数据加载失败" :description="error" error>
<button class="act" @click="load">重试</button>
</StatePanel>
    <div v-else-if="loading" v-loading="true" style="height: 140px" />
    <template v-else-if="tab === 'metrics'">
<div v-if="metrics.length" class="ops-charts">
        <MetricChart
          v-for="m in metrics"
          :key="m.id"
          :series="[m]"
          :names="names"
        />
      </div>
      <StatePanel
        v-else
        title="该实例尚无指标"
        description="检查采集配置与 exporter；也可前往指标工作台排查。"
    />
</template>
    <template v-else-if="tab === 'pods'">
<div class="table-scroll" tabindex="0">
        <table class="list-table">
          <thead>
            <tr>
              <th>Pod</th>
              <th>状态</th>
              <th>就绪</th>
              <th>重启</th>
              <th>节点</th>
              <th>原因</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in pods" :key="p.name">
              <td>{{ p.name }}</td>
              <td>{{ p.status }}</td>
              <td>{{ p.ready }}</td>
              <td>{{ p.restarts }}</td>
              <td>{{ p.node || '—' }}</td>
              <td>{{ p.message || '—' }}</td>
            </tr>
            <tr v-if="!pods.length">
              <td colspan="6">暂无 Pod 数据，请检查集群接入与实例状态</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="isManaged" class="upgrade-form">
        <label for="upgrade-image">目标镜像</label><input
          id="upgrade-image"
          v-model="image"
          placeholder="registry/repository:tag"
        /><button
          class="act"
          :disabled="acting || !image.trim()"
          @click="upgrade"
        >
          升级镜像
        </button>
      </div>
      <p v-else>外部实例的启停、规格和升级请在其所属集群操作。</p>
</template>
    <template v-else-if="tab === 'backup'">
      <div class="ops-controls">
        <button
          class="act"
          :disabled="acting || !caps?.backup.supported"
          :title="caps ? caps.backup.reason : '能力面加载失败，操作仍受服务端鉴权'"
          @click="createBackup('snapshot')"
        >
          执行真实备份
        </button>
        <button class="act" :disabled="acting" @click="createBackup('record')">
          仅添加记录
        </button>
        <span v-if="caps && !caps.backup.supported" class="hint">{{
          caps.backup.reason
        }}</span>
        <span v-if="hasActiveJob" class="hint"
          >有执行中的作业，列表随轮询自动更新</span
        >
      </div>
      <div class="table-scroll" tabindex="0">
        <table class="list-table">
          <thead>
            <tr>
              <th>模式</th>
              <th>状态</th>
              <th>大小</th>
              <th>校验和</th>
              <th>时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="b in backups" :key="b.id">
              <td>
                <span :class="['b-mode', b.mode || 'record']">{{ modeLabel(b) }}</span>
              </td>
              <td>
                <span :class="['b-status', b.status]">{{ statusLabel(b.status) }}</span>
                <span v-if="b.error" class="hint" :title="b.error">（{{ b.error.slice(0, 40) }}）</span>
              </td>
              <td>
                {{ sizeText(b) }}
                <span v-if="b.mode === 'record' && b.status === 'completed'" class="hint"
                  >（无产物，不可恢复）</span
                >
                <span v-if="b.restoredResourceId" class="hint"
                  >→ 恢复目标 {{ b.restoredResourceId }}</span
                >
              </td>
              <td>
                <span v-if="b.checksum" class="mono" :title="b.checksum">{{
                  b.checksum.slice(0, 10) + '…'
                }}</span>
                <span v-else class="hint">—</span>
              </td>
              <td>{{ b.createdAt?.replace('T', ' ').slice(0, 19) }}</td>
              <td>
                <button
                  v-if="b.mode === 'snapshot' && b.status === 'completed'"
                  class="act"
                  :disabled="acting || !caps?.restore.supported"
                  @click="restoreBackup(b)"
                >
                  恢复到新实例
                </button>
                <button class="act danger" :disabled="acting" @click="removeBackup(b)">
                  删除
                </button>
              </td>
            </tr>
            <tr v-if="!backups.length">
              <td colspan="6">暂无备份记录（真实备份产生可恢复快照；记录模式仅登记意图）</td>
            </tr>
          </tbody>
        </table>
      </div>
</template>
    <template v-else-if="tab === 'topics'">
<div class="ops-controls">
        <input
          v-model="newTopic.name"
          aria-label="主题名称"
          placeholder="主题名称"
        /><input
          v-model.number="newTopic.partitions"
          type="number"
          min="1"
          aria-label="分区数"
        /><input
          v-model="newTopic.retention"
          aria-label="保留时间"
          placeholder="保留时间，例如 7d"
        /><button
          class="act"
          :disabled="acting || !newTopic.name.trim()"
          @click="createTopic"
        >
          创建主题
        </button>
      </div>
      <article v-for="t in topics" :key="t.id" class="topic-card">
        <div class="ops-controls">
          <strong>{{ t.name }}</strong><span>{{ t.partitions }} 分区 · {{ t.retention }} · {{ t.status }}</span><button class="act" @click="loadGroups(t)">消费组</button><button
            class="act danger"
            :disabled="acting"
            @click="removeTopic(t)"
          >
            删除主题
          </button>
        </div>
        <template v-if="groups[t.id]">
<div v-for="g in groups[t.id]" :key="g.id" class="ops-controls">
            {{ g.name }} · {{ g.mode }} · {{ g.members }} 成员<button
              class="act danger"
              @click="removeGroup(t, g)"
            >
              删除消费组
            </button>
          </div>
          <div v-if="newGroup[t.id]" class="ops-controls">
            <input
              v-model="newGroup[t.id]!.name"
              aria-label="消费组名称"
              placeholder="消费组名称"
            /><select v-model="newGroup[t.id]!.mode" aria-label="消费模式">
              <option value="clustering">集群消费</option>
              <option value="broadcasting">广播消费</option>
</select><button class="act" :disabled="acting" @click="createGroup(t)">
              添加消费组
            </button>
          </div>
</template>
      </article>
      <StatePanel v-if="!topics.length" title="暂无消息主题" />
</template>
  </section>
</template>
<style scoped>
.ops-heading,
.ops-controls,
.upgrade-form {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  margin: 16px 0;
}
.ops-heading p {
  flex: 1;
  font-size: 12px;
  color: var(--text-faint);
  line-height: 1.8;
}
.ops-charts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.operations {
  margin-top: 20px;
}
select,
input {
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 7px 10px;
  background: var(--surface);
  color: var(--text);
  max-width: 100%;
}
.upgrade-form input {
  flex: 1;
  min-width: 180px;
}
.topic-card {
  border: 1px solid var(--border);
  padding: 16px;
  background: var(--surface);
  border-radius: var(--radius);
  margin: 12px 0;
}
.topic-card span {
  font-size: 12px;
  color: var(--text-faint);
}
.table-scroll {
  margin-top: 16px;
}
.list-table {
  min-width: 780px;
}
@media (max-width: 1100px) {
  .ops-charts {
    grid-template-columns: 1fr;
  }
}
/* DS-01：备份状态用「已记录」弱化——completed 不冒充可恢复证据 */
.b-status { color: var(--text-dim); }
.b-status.completed { color: var(--success); }
.b-status.failed { color: var(--danger); }
.b-status.running,
.b-status.accepted,
.b-status.verifying { color: var(--warning); }
.b-mode {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--surface-2, #1e2433);
  color: var(--text-dim);
}
.b-mode.snapshot {
  background: rgba(16, 185, 129, 0.12);
  color: var(--success);
}
.b-mode.restore {
  background: rgba(59, 130, 246, 0.12);
  color: var(--brand);
}
.mono {
  font-family: var(--mono, monospace);
  font-size: 12px;
}
</style>
