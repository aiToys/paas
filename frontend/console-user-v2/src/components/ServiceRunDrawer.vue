<script setup lang="ts">
// 服务运行抽屉（spec §9.5 运行主语）：服务的本环境运行详情——
// 运行态（镜像/副本/域名）+ 实例（Pod）+ 实例日志 + 伸缩 + 对外暴露。
// 自老版 WorkloadDetailDrawer 收编适配（该组件自 2026-10-14 起为三处单一真源），
// v2 增补：操作区（伸缩 PUT replicas / 对外暴露 PUT domain——平台仅有的两个运行态写操作，
// 停止/重启语义后端不存在，不造假按钮）。
import { computed, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { apiError } from '@/api'
import { useEnvStore } from '@/stores/env'
import { getWorkload, getWorkloadLogs, updateWorkload, type WorkloadDetail } from '@/api/workload'
import { type PipelineRun } from '@/api/pipeline'
import CdDeployDialog from '@/components/CdDeployDialog.vue'
import { statusOf, tagTypeToCls, WORKLOAD_STATUS, POD_STATUS } from '@/composables/useStatus'

const props = withDefaults(
  defineProps<{
    /** 抽屉显示（v-model） */
    modelValue: boolean
    /** 工作负载 ID；变化即重新拉详情 */
    workloadId?: string
    /** 所属应用 ID（部署按钮找应用的 CD 流水线） */
    appId?: string
    appName?: string
    /** > 0 时周期刷新（部署观察场景）；页面不可见跳过，关闭/卸载自动停 */
    pollMs?: number
  }>(),
  { workloadId: '', appId: '', appName: '', pollMs: 0 },
)
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  /** 运行态变化（伸缩/暴露写成功后触发，消费方刷新服务表） */
  (e: 'changed'): void
  /** 部署已触发（CdDeployDialog 成功后透传，消费方跳交付面看进度） */
  (e: 'deployed', run: PipelineRun): void
}>()

const envStore = useEnvStore()
const detailLoading = ref(false)
const detail = ref<WorkloadDetail | null>(null)

// 竞态守卫：快速连续切换 workloadId 时只保留最后一次响应。
let seq = 0
async function loadDetail() {
  const id = props.workloadId
  if (!id) return
  const cur = ++seq
  detailLoading.value = true
  try {
    const d = await getWorkload(id)
    if (cur !== seq) return
    detail.value = d
  } catch (e) {
    if (cur !== seq) return
    ElMessage.error(apiError(e, '加载运行详情失败'))
  } finally {
    if (cur === seq) detailLoading.value = false
  }
}

watch(
  () => props.workloadId,
  () => { detail.value = null },
)
watch(
  () => [props.modelValue, props.workloadId] as const,
  ([open, id]) => { if (open && id) loadDetail() },
  { immediate: true },
)

let timer: number | undefined
watch(
  () => [props.modelValue, props.pollMs] as const,
  ([open, ms]) => {
    if (timer) { window.clearInterval(timer); timer = undefined }
    if (open && ms > 0) {
      timer = window.setInterval(() => { if (!document.hidden) loadDetail() }, ms)
    }
  },
  { immediate: true },
)
onUnmounted(() => { if (timer) window.clearInterval(timer) })

function wlStatusOf(s: string): { label: string; cls: string } {
  const m = statusOf(WORKLOAD_STATUS, s)
  return { label: m.label, cls: tagTypeToCls(m.type) }
}
function podStatusOf(s: string): { label: string; cls: string } {
  const m = statusOf(POD_STATUS, s)
  return { label: m.label, cls: tagTypeToCls(m.type) }
}

// —— 伸缩（PUT replicas；生产环境后端 prod:write 拦截，前端不做双闸）——
const scaling = ref(false)
const scaleInput = ref<number>(1)
watch(detail, (d) => { if (d) scaleInput.value = d.workload.replicas })
async function applyScale() {
  if (!detail.value) return
  const n = Math.max(0, Math.floor(scaleInput.value))
  scaling.value = true
  try {
    await updateWorkload(detail.value.workload.id, { replicas: n })
    ElMessage.success(`已调整为 ${n} 副本`)
    emit('changed')
    await loadDetail()
  } catch (e) {
    ElMessage.error(apiError(e, '伸缩失败'))
  } finally {
    scaling.value = false
  }
}

// —— 对外暴露（PUT domain；空=撤下）——
const domainEdit = ref(false)
const domainInput = ref('')
watch(detail, (d) => { if (d) domainInput.value = d.workload.domain ?? '' })
async function applyDomain() {
  if (!detail.value) return
  scaling.value = true
  try {
    await updateWorkload(detail.value.workload.id, { domain: domainInput.value.trim() })
    ElMessage.success(domainInput.value.trim() ? '已暴露域名（Ingress 生效需约十几秒）' : '已撤下对外暴露')
    domainEdit.value = false
    emit('changed')
    await loadDetail()
  } catch (e) {
    ElMessage.error(apiError(e, '域名设置失败'))
  } finally {
    scaling.value = false
  }
}

const domainHref = computed(() =>
  detail.value?.workload.domain ? `http://${detail.value.workload.domain}` : '',
)

// —— 部署（spec §9.5 主按钮）：有 CD 流水线才显示按钮，确认弹窗复用 CdDeployDialog ——
const showDeploy = ref(false)


// —— 高级折叠（spec：泳道不进一级界面，藏进服务详情「高级」）——
const showAdvanced = ref(false)

// —— 实例日志（GET /api/workloads/{id}/logs?pod=&tail=&previous=）——
const showLogs = ref(false)
const logsLoading = ref(false)
const logsPod = ref('')
const logsText = ref('')
const logsPrevious = ref(false)

async function viewLogs(pod: string) {
  showLogs.value = true
  logsLoading.value = true
  logsPod.value = pod
  logsText.value = ''
  try {
    const params: Record<string, string> = { pod, tail: '1000' }
    if (logsPrevious.value) params.previous = 'true'
    const resp = await getWorkloadLogs(detail.value?.workload.id ?? '', params)
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    logsText.value = await resp.text()
    if (!logsText.value) logsText.value = '（暂无日志输出）'
  } catch (e) {
    logsText.value = apiError(e, '日志加载失败') + '\n（非集群部署或 Pod 已清理时不可用）'
  } finally {
    logsLoading.value = false
  }
}
async function togglePrevious() {
  logsPrevious.value = !logsPrevious.value
  if (logsPod.value) await viewLogs(logsPod.value)
}
</script>

<template>
  <el-drawer
    :model-value="modelValue"
    title="服务运行"
    size="640px"
    direction="rtl"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="detailLoading">
      <template v-if="detail">
        <div class="runtime-entry"><RouterLink :to="'/workloads/' + encodeURIComponent(detail.workload.id)" class="act primary" @click="emit('update:modelValue', false)">打开完整运行视图 →</RouterLink><RouterLink :to="{ path: '/observability', query: { workload: detail.workload.id, env: detail.workload.envId, tab: 'metrics' } }" class="text-button" @click="emit('update:modelValue', false)">指标与告警</RouterLink></div>
        <div class="detail-info">
          <div class="info-row"><span class="k">名称</span><span class="v">{{ detail.workload.name }}</span></div>
          <div class="info-row"><span class="k">环境</span><span class="v">{{ envStore.envs.find((e) => e.id === detail?.workload.envId)?.name ?? detail.workload.envId }} · {{ detail.workload.laneId === 'default' ? '基线' : detail.workload.laneId }}</span></div>
          <div class="info-row"><span class="k">镜像</span><span class="v mono">{{ detail.workload.image }}</span></div>
          <div class="info-row">
            <span class="k">状态</span>
            <span class="status" :class="wlStatusOf(detail.workload.status).cls">{{ wlStatusOf(detail.workload.status).label }}</span>
            <span class="v mono ready">{{ detail.workload.ready }}/{{ detail.workload.replicas }}</span>
          </div>
          <div class="info-row">
            <span class="k">域名</span>
            <a v-if="detail.workload.domain" :href="domainHref" target="_blank" rel="noopener" class="v domain mono">{{ detail.workload.domain }} ↗</a>
            <span v-else class="v faint">未暴露</span>
            <button class="link-btn" @click="domainEdit = !domainEdit">{{ domainEdit ? '收起' : '修改' }}</button>
          </div>
        </div>

        <!-- 对外暴露编辑（空=撤下） -->
        <div v-if="domainEdit" class="domain-edit">
          <input
            v-model="domainInput"
            class="domain-input mono"
            placeholder="例如 shop.paas.internal（留空撤下暴露）"
            @keyup.enter="applyDomain"
          />
          <button class="act primary" :disabled="scaling" @click="applyDomain">保存</button>
        </div>

        <!-- 主操作行：部署是主按钮（spec §9.5「部署是按钮不是并列 tab」）；伸缩次级 -->
        <div class="ops-row">
          <button v-if="props.appId" class="act primary deploy-btn" @click="showDeploy = true">发起交付</button>
          <span class="ops-label">伸缩</span>
          <input v-model.number="scaleInput" type="number" min="0" class="scale-input mono" />
          <button class="act" :disabled="scaling" @click="applyScale">应用</button>
          <span class="ops-hint">0 副本 = 不占用资源</span>
        </div>

        <!-- 高级折叠：泳道/镜像全名（spec：泳道不进任何一级界面） -->
        <div class="adv-toggle" @click="showAdvanced = !showAdvanced">
          {{ showAdvanced ? '▾' : '▸' }} 高级
        </div>
        <div v-if="showAdvanced" class="adv-body">
          <div class="info-row">
            <span class="k">泳道</span>
            <span class="v mono">{{ detail.workload.laneId || 'default（基线）' }}</span>
          </div>
          <div class="info-row">
            <span class="k">环境</span>
            <span class="v mono">{{ detail.workload.envId }}</span>
          </div>
        </div>

        <!-- 部署确认（CdDeployDialog 复用：产品服务面未部署行同款） -->
        <CdDeployDialog
          v-model="showDeploy"
          :app-id="props.appId"
          :hint="detail?.workload.service || detail?.workload.name"
          :service-id="detail?.workload.serviceId" :env-id="detail?.workload.envId"
          @deployed="(run) => { emit('update:modelValue', false); emit('deployed', run) }"
        />

        <div class="section-title">运行实例（{{ detail.instances.length }}）</div>
        <div v-if="detail.instances.length === 0" class="empty-hint">
          本次未返回实例。当前接口无法区分没有 Pod 与运行态读取不可用，请在完整运行视图中检查数据来源。
        </div>
        <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="instances-table">
          <thead>
            <tr>
              <th>实例（Pod）</th>
              <th>状态</th>
              <th>重启</th>
              <th>节点</th>
              <th class="col-act"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="ins in detail.instances" :key="ins.name">
              <td>
                <div class="name-cell">
                  <span class="name mono">{{ ins.name }}</span>
                  <span v-if="ins.message" class="msg">{{ ins.message }}</span>
                </div>
              </td>
              <td><span class="status" :class="podStatusOf(ins.status).cls">{{ podStatusOf(ins.status).label }}</span></td>
              <td class="mono" :class="{ err: ins.restarts > 0 }">{{ ins.restarts }}</td>
              <td class="mono small">{{ ins.node || '-' }}</td>
              <td class="col-act"><button class="act" @click="viewLogs(ins.name)">日志</button></td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
    </div>
  </el-drawer>

  <!-- 实例日志 -->
  <el-dialog v-model="showLogs" :title="`日志：${logsPod}`" width="780px" top="6vh" append-to-body>
    <div class="logs-toolbar">
      <span class="pod-name mono">{{ logsPod }}</span>
      <el-button size="small" :type="logsPrevious ? 'warning' : 'default'" @click="togglePrevious">
        {{ logsPrevious ? '查看当前日志' : '查看上次终止日志' }}
      </el-button>
      <span v-if="logsPrevious" class="prev-hint">（已退出/重启容器的上次日志，排查崩溃关键）</span>
    </div>
    <div v-loading="logsLoading" class="logs-body">
      <pre>{{ logsText }}</pre>
    </div>
  </el-dialog>
</template>

<style scoped>
.runtime-entry { display: flex; align-items: center; flex-wrap: wrap; gap: 14px; margin-bottom: 18px; }
.detail-info {
  margin-bottom: 14px;
}
.info-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px dashed var(--border);
}
.info-row .k {
  width: 48px;
  flex-shrink: 0;
  color: var(--text-faint);
}
.info-row .v {
  color: var(--text);
  word-break: break-all;
}
.ready {
  margin-left: auto;
  font-size: 12.5px;
}
.faint {
  color: var(--text-faint);
}
.domain {
  color: var(--success, #16a34a);
  text-decoration: none;
}
.link-btn {
  margin-left: auto;
  border: none;
  background: transparent;
  color: var(--brand);
  font-size: 12px;
  cursor: pointer;
}
.domain-edit {
  display: flex;
  gap: 8px;
  margin: 10px 0;
}
.domain-input {
  flex: 1;
  padding: 6px 10px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-size: 12.5px;
}
.deploy-btn {
  min-width: 72px;
}
.adv-toggle {
  padding: 8px 0 4px;
  font-size: 12px;
  color: var(--text-faint);
  cursor: pointer;
  user-select: none;
}
.adv-toggle:hover {
  color: var(--text-dim);
}
.adv-body {
  padding: 4px 0;
}
.ops-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 0;
  font-size: 13px;
}
.ops-label {
  color: var(--text-faint);
}
.ops-hint {
  font-size: 11.5px;
  color: var(--text-faint);
}
.scale-input {
  width: 72px;
  padding: 5px 8px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-size: 12.5px;
}
.section-title {
  margin: 14px 0 8px;
  font-size: 13px;
  font-weight: 600;
}
.instances-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
.instances-table th {
  text-align: left;
  padding: 6px 8px;
  color: var(--text-faint);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
}
.instances-table td {
  padding: 7px 8px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.name-cell .name {
  color: var(--text);
}
.name-cell .msg {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: var(--warning);
}
.small { font-size: 11px; }
.err { color: var(--danger, #f43f5e); }
.col-act { text-align: right; }
.logs-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.pod-name {
  font-size: 12px;
  color: var(--text-dim);
}
.prev-hint {
  font-size: 11.5px;
  color: var(--text-faint);
}
.logs-body {
  max-height: 60vh;
  overflow: auto;
  padding: 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.logs-body pre {
  margin: 0;
  font-family: 'JetBrains Mono', monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--text);
}
</style>
