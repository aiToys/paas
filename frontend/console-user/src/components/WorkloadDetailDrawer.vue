<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
import { statusOf as statusOfCore, tagTypeToCls, WORKLOAD_STATUS, POD_STATUS } from '@/composables/useStatus'
// 工作负载详情抽屉（单一真源组件）：期望态 + 运行实例（Pod 级）+ 实例日志。
// 从 Workloads.vue / app-tabs/AppServices.vue 两份复制实现收编（DRY）；
// 应用详情「部署」tab 同款复用，三处交互一致（点行开抽屉，不再跳列表）。
import { onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { apiError } from '@/api'
import { getWorkload, getWorkloadLogs, type WorkloadDetail } from '@/api/workload'

const props = withDefaults(
  defineProps<{
    /** 抽屉显示（v-model） */
    modelValue: boolean
    /** 工作负载 ID；变化即重新拉详情 */
    workloadId?: string
    /** > 0 时周期刷新详情（创建后部署观察场景）；页面不可见跳过，关闭/卸载自动停 */
    pollMs?: number
  }>(),
  { workloadId: '', pollMs: 0 },
)
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  /** 每次拉到详情触发（消费方可做部署终态判断） */
  (e: 'detail', d: WorkloadDetail): void
  (e: 'close'): void
}>()

const detailLoading = ref(false)
const detail = ref<WorkloadDetail | null>(null)

// 竞态守卫：快速连续切换 workloadId 时只保留最后一次响应（先发后到会错位详情）。
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
    emit('detail', d)
  } catch (e) {
    if (cur !== seq) return
    ElMessage.error(apiError(e, '加载详情失败'))
  } finally {
    if (cur === seq) detailLoading.value = false
  }
}

// 打开抽屉或切换工作负载即拉详情；换 ID 先清旧数据防闪上一负载内容。
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

// 状态映射：工作负载态 / Pod 态均收编 useStatus 字典（label 单一真源），dot 色经 tagTypeToCls。
function wlStatusOf(s: string): { label: string; cls: string } {
  const m = statusOfCore(WORKLOAD_STATUS, s)
  return { label: m.label, cls: tagTypeToCls(m.type) }
}
function podStatusOf(s: string): { label: string; cls: string } {
  const m = statusOfCore(POD_STATUS, s)
  return { label: m.label, cls: tagTypeToCls(m.type) }
}
const fmtTime = (t?: string) => {
  if (!t) return '-'
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '-' : formatDateTime(d)
}

// 日志查看（按实例/Pod；GET /api/workloads/{id}/logs?pod=）
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
  <!-- append-to-body：嵌套抽屉场景（服务 tab 实例抽屉内再开本抽屉）层叠正常 -->
  <el-drawer
    :model-value="modelValue"
    title="工作负载详情"
    size="640px"
    direction="rtl"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
    @close="emit('close')"
  >
    <div v-loading="detailLoading">
      <template v-if="detail">
        <div class="detail-info">
          <div class="info-row"><span class="k">名称</span><span class="v">{{ detail.workload.name }}</span></div>
          <div class="info-row"><span class="k">ID</span><span class="v mono">{{ detail.workload.id }}</span></div>
          <div class="info-row"><span class="k">镜像</span><span class="v mono">{{ detail.workload.image }}</span></div>
          <div class="info-row">
            <span class="k">副本</span>
            <span class="v mono">{{ detail.workload.ready }}/{{ detail.workload.replicas }}</span>
          </div>
          <div class="info-row">
            <span class="k">状态</span>
            <span class="status" :class="wlStatusOf(detail.workload.status).cls">{{ wlStatusOf(detail.workload.status).label }}</span>
          </div>
        </div>

        <div class="section-title">运行实例（{{ detail.instances.length }}）</div>
        <div v-if="detail.instances.length === 0" class="empty-hint">
          暂无运行实例（非集群部署或 Pod 未就绪）
        </div>
        <table v-else class="instances-table">
          <thead>
            <tr>
              <th>实例（Pod）</th>
              <th>状态</th>
              <th>就绪</th>
              <th>重启</th>
              <th>节点</th>
              <th>启动时间</th>
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
              <td class="mono">{{ ins.ready || '-' }}</td>
              <td class="mono" :class="{ err: ins.restarts > 0 }">{{ ins.restarts }}</td>
              <td class="mono">{{ ins.node || '-' }}</td>
              <td class="mono small">{{ fmtTime(ins.startedAt) }}</td>
              <td class="col-act"><button class="act" @click="viewLogs(ins.name)">日志</button></td>
            </tr>
          </tbody>
        </table>
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
.detail-info {
  margin-bottom: 14px;
}
.info-row {
  display: flex;
  gap: 10px;
  padding: 5px 0;
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
.section-title {
  margin: 14px 0 8px;
  font-size: 13px;
  font-weight: 600;
}
.empty-hint {
  padding: 24px 0;
  font-size: 12.5px;
  color: var(--text-faint);
  text-align: center;
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
.status.ok { color: var(--success); }
.status.warn { color: var(--warning); }
.status.err { color: var(--danger); }
.status.done { color: var(--success); }
.status.idle { color: var(--text-faint); }
.mono { font-family: 'JetBrains Mono', monospace; }
.small { font-size: 11px; }
.err { color: var(--danger); }
.col-act { text-align: right; }
.act {
  padding: 3px 10px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.act:hover {
  border-color: var(--brand);
  color: var(--brand);
}
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
