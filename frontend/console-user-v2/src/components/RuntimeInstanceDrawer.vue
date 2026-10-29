<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessageBox } from 'element-plus'
import { apiError } from '@/api'
import { getPodEvents, recreateInstance, type PodEvent, type RuntimeCapabilities, type RuntimeInstance } from '@/api/workload'
import { instanceState } from '@/domain/serviceRuntime'
import { confirmAction } from '@/composables/useConfirmAction'
import RuntimeLogs from './RuntimeLogs.vue'
import StatePanel from './StatePanel.vue'
import TerminalPanel from './TerminalPanel.vue'
import Icon from '@/components/Icon.vue'
import { useAssistantStore } from '@/stores/assistant'

const props = defineProps<{
  modelValue: boolean; workloadId: string; context: string
  instance?: RuntimeInstance; section: string
  capabilities?: RuntimeCapabilities
  isProd?: boolean
  recreateEligible?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; changed: [detail: string] }>()
const active = ref('details')
const assistant = useAssistantStore()
watch(() => [props.section, props.modelValue, props.instance?.name], () => { active.value = props.section }, { immediate: true })
const sections = [{ id: 'details', label: '实例详情' }, { id: 'logs', label: '日志' }, { id: 'events', label: '事件' }, { id: 'terminal', label: '容器终端' }]

// 事件：按 Pod 读取（后端按归属 + UID 校验），展示调度/镜像/探针/退出原因。
const events = ref<PodEvent[]>([])
const eventsLoading = ref(false)
const eventsError = ref('')
const eventsLoadedAt = ref('')
async function loadEvents() {
  if (!props.instance) return
  eventsLoading.value = true; eventsError.value = ''
  try {
    events.value = await getPodEvents(props.workloadId, props.instance.name)
    eventsLoadedAt.value = new Date().toLocaleTimeString()
  } catch (e) { eventsError.value = apiError(e, '事件读取失败') }
  finally { eventsLoading.value = false }
}
watch(() => [active.value, props.instance?.name, props.modelValue], () => {
  if (props.modelValue && active.value === 'events' && props.instance) { events.value = []; void loadEvents() }
})

// 重建：确认（生产二次警示 + UID 快照防漂移）→ reason 必填 → POST（202 受理）。
const recreating = ref(false)
async function recreate() {
  const inst = props.instance
  if (!inst || !inst.uid) return
  const warned = await confirmAction(
    `将驱逐 Pod ${inst.name} 并由控制器重建替代实例。${props.isProd ? '此操作作用于生产环境实例。' : ''}该实例上的进行中连接会中断；本地临时数据（emptyDir）会丢失。`,
    '重建实例',
  )
  if (!warned) return
  let reason = ''
  try {
    const { value } = await ElMessageBox.prompt('填写重建原因（记入审计与操作记录）', '重建原因', {
      confirmButtonText: '确认重建', cancelButtonText: '取消', inputPlaceholder: '例如：容器 CrashLoop，排查后恢复',
      inputValidator: (v: string) => (v && v.trim().length >= 2 ? true : '原因至少 2 个字符'),
    })
    reason = value.trim()
  } catch { return }
  recreating.value = true
  try {
    const op = await recreateInstance(props.workloadId, inst.name, inst.uid, reason, `recreate-${inst.uid}-${Date.now()}`)
    ElMessageBox.close()
    emit('changed', `重建已受理（操作 ${op.id}），控制器将补齐替代实例`)
  } catch (e) {
    ElMessageBox.alert(apiError(e, '重建受理失败') + (String(e).includes('UID') || String(e).includes('409') ? '实例可能已变化，请刷新后重试。' : ''), '重建失败', { type: 'error' })
  } finally { recreating.value = false }
}

const execAllowed = computed(() => props.capabilities?.exec?.allowed || false)
const recreateAllowed = computed(() => props.recreateEligible !== false)
const containerLines = computed(() => props.instance?.containers || [])
</script>
<template>
  <el-drawer :model-value="modelValue" title="实例诊断" size="min(900px, 100vw)" append-to-body destroy-on-close @update:model-value="emit('update:modelValue', $event)">
    <template v-if="instance">
      <div class="diagnostic-target"><span class="eyebrow">{{ context }}</span><strong class="mono">{{ instance.name }}</strong><span class="status" :class="instanceState(instance).cls">{{ instanceState(instance).label }}</span></div>
      <nav class="tab-bar" aria-label="实例诊断分区"><button v-for="s in sections" :key="s.id" class="tab-btn" :class="{ active: active === s.id }" :aria-current="active === s.id ? 'page' : undefined" @click="active = s.id">{{ s.label }}</button></nav>
      <RuntimeLogs v-if="active === 'logs'" :key="workloadId + instance.name" :workload-id="workloadId" :instances="[instance]" :initial-pod="instance.name" />
      <div v-else-if="active === 'details'" class="instance-details">
        <dl>
          <dt>Pod Phase</dt><dd>{{ instance.status }}</dd>
          <dt>容器就绪</dt><dd>{{ instance.ready || '未知' }}</dd>
          <dt>累计重启</dt><dd>{{ instance.restarts }}</dd>
          <dt>节点</dt><dd class="mono">{{ instance.node || '未调度' }}</dd>
          <dt>IP</dt><dd class="mono">{{ instance.ip || '未分配' }}</dd>
          <dt>启动时间</dt><dd>{{ instance.startedAt ? new Date(instance.startedAt).toLocaleString() : '未返回' }}</dd>
          <dt>状态说明</dt><dd>{{ instance.reason || instance.message || '本次响应没有附加说明' }}</dd>
          <template v-for="c in containerLines" :key="c.name">
            <dt class="mono">容器 {{ c.name }}</dt>
            <dd>{{ c.state }}{{ c.ready ? ' · ready' : '' }}{{ c.restarts ? ` · 重启 ${c.restarts}` : '' }}{{ c.reason ? ` · ${c.reason}` : '' }}{{ c.lastExitCode ? ` · 退出码 ${c.lastExitCode}` : '' }}</dd>
          </template>
        </dl>
        <div class="recreate-box">
          <div><strong>重建此实例</strong><p>驱逐该 Pod 由控制器补齐替代实例（尊重 PDB）；中断此实例上的连接。</p></div>
          <button class="act" :disabled="recreating || !instance.uid || !recreateAllowed" @click="recreate">{{ recreating ? '提交中…' : '重建实例' }}</button>
        </div>
        <p class="capability-note">重建请求绑定当前实例 UID；实例已变化（409）或被 PDB 阻断（429）时需要处理后再试。</p>
        <div class="recreate-box diagnose-box">
          <div><strong>智能诊断</strong><p>以该工作负载为诊断目标，带入实例 {{ instance?.name }} 的上下文做只读根因分析。</p></div>
          <button class="act" :disabled="!instance" @click="assistant.openPanel({ workloadId: props.workloadId, workloadName: props.workloadId, podName: instance?.name, podUID: instance?.uid, symptom: 'crash', question: instance ? `实例 ${instance.name} 反复异常（${instance.reason || instance.status}），请分析根因。` : '' })"><Icon name="bot" :size="15" />诊断</button>
        </div>
      </div>
      <div v-else-if="active === 'events'" class="events-panel">
        <div class="events-toolbar"><button class="act" :disabled="eventsLoading" @click="loadEvents">{{ eventsLoading ? '读取中…' : '刷新事件' }}</button><span v-if="eventsLoadedAt" class="events-meta">读取于 {{ eventsLoadedAt }}</span></div>
        <StatePanel v-if="eventsError" compact error title="事件读取失败" :description="eventsError"><button class="act" @click="loadEvents">重试</button></StatePanel>
        <StatePanel v-else-if="!events.length && !eventsLoading" compact title="没有返回事件" description="该 Pod 无近期事件，或事件已过期（默认保留 1 小时）。" />
        <div v-else-if="events.length" class="table-scroll" tabindex="0" aria-label="事件表格"><table class="list-table events-table"><thead><tr><th scope="col">级别</th><th scope="col">原因</th><th scope="col">内容</th><th scope="col">次数</th><th scope="col">时间</th></tr></thead><tbody><tr v-for="(e, idx) in events" :key="idx"><td><span class="status" :class="e.type === 'Warning' ? 'warn' : 'idle'">{{ e.type }}</span></td><td class="mono">{{ e.reason }}</td><td class="events-message">{{ e.message }}</td><td class="mono">{{ e.count || 1 }}</td><td class="mono">{{ new Date(e.at).toLocaleString() }}</td></tr></tbody></table></div>
      </div>
      <TerminalPanel v-else :workload-id="workloadId" :instance="instance" :enabled="execAllowed" :disabled-reason="capabilities?.exec?.message" @operated="(d) => emit('changed', d)" />
    </template>
  </el-drawer>
</template>
<style scoped>
@import './runtime-tabs.css';
.diagnostic-target { display: flex; align-items: flex-start; flex-wrap: wrap; gap: 10px; margin-bottom: 20px; }
.diagnostic-target .eyebrow { flex-basis: 100%; margin-bottom: 0; letter-spacing: 0; }
.diagnostic-target strong { overflow-wrap: anywhere; max-width: 100%; }
.tab-bar { margin-bottom: 20px; overflow-x: auto; }
.instance-details dl { display: grid; grid-template-columns: 130px minmax(0, 1fr); gap: 15px 20px; margin: 24px 0 32px; }
dt, .capability-note { color: var(--text-faint); } dd { margin: 0; overflow-wrap: anywhere; }
.recreate-box { display: flex; justify-content: space-between; align-items: center; gap: 16px; border-top: 1px solid var(--border); padding-top: 20px; }
.recreate-box p, .capability-note { font-size: 12px; }
.recreate-box p { color: var(--text-faint); margin: 5px 0 0; }
.capability-note { margin-top: 12px; }
.events-panel .events-toolbar { display: flex; align-items: center; gap: 14px; margin-bottom: 14px; }
.events-meta { font-size: 12px; color: var(--text-faint); }
.events-table { min-width: 640px; }
.events-message { overflow-wrap: anywhere; max-width: 420px; }
</style>
