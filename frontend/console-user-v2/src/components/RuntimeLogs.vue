<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { apiError, respError } from '@/api'
import { getWorkloadLogs, type RuntimeInstance } from '@/api/workload'
import StatePanel from './StatePanel.vue'

const props = withDefaults(defineProps<{ workloadId: string; instances: RuntimeInstance[]; initialPod?: string }>(), { initialPod: '' })
const pod = ref('')
const container = ref('')
const previous = ref(false)
const tail = ref('300')
const text = ref('')
const error = ref('')
const loading = ref(false)
const loaded = ref(false)
const loadedAt = ref('')
const selected = computed(() => props.instances.find((i) => i.name === pod.value))
const containers = computed(() => selected.value?.containers || [])
let sequence = 0
function clear() { sequence++; text.value = ''; error.value = ''; loaded.value = false; loading.value = false; loadedAt.value = '' }
watch(() => [props.workloadId, props.initialPod], () => {
  clear()
  previous.value = false
  pod.value = props.instances.some((i) => i.name === props.initialPod) ? props.initialPod : props.instances[0]?.name || ''
}, { immediate: true })
watch(() => props.instances, () => {
  if (!props.instances.some((i) => i.name === pod.value)) { clear(); pod.value = props.instances[0]?.name || '' }
})
watch(pod, () => { container.value = '' }) // 切实例后容器选择重置，避免带着上一个 Pod 的容器名请求
watch([pod, container, previous, tail], clear)
async function load() {
  if (!selected.value) return
  const current = ++sequence
  loading.value = true
  text.value = ''; error.value = ''; loaded.value = false
  try {
    // podUID 让后端精确校验目标实例未漂移；container 支持多容器 Pod 选源。
    const params: Record<string, string> = { pod: pod.value, tail: tail.value, previous: String(previous.value) }
    if (selected.value.uid) params.podUid = selected.value.uid
    if (container.value) params.container = container.value
    const response = await getWorkloadLogs(props.workloadId, params)
    if (!response.ok) throw new Error(await respError(response))
    const value = await response.text()
    if (current !== sequence) return
    text.value = value
    loaded.value = true
    loadedAt.value = new Date().toLocaleTimeString()
  } catch (e) { if (current === sequence) error.value = apiError(e, '日志读取失败') }
  finally { if (current === sequence) loading.value = false }
}
onUnmounted(() => { sequence++ })
</script>

<template>
  <section class="runtime-logs" aria-label="实例日志">
    <div class="filter-bar">
      <label class="log-field">实例<select v-model="pod" class="filter-select" :disabled="!instances.length"><option v-if="!instances.length" value="">没有可选择的实例</option><option v-for="i in instances" :key="i.uid || i.name" :value="i.name">{{ i.name }}</option></select></label>
      <label v-if="containers.length > 1" class="log-field">容器<select v-model="container" class="filter-select"><option value="">全部容器</option><option v-for="c in containers" :key="c.name" :value="c.name">{{ c.name }}（{{ c.state }}）</option></select></label>
      <label class="log-field">日志来源<select v-model="previous" class="filter-select"><option :value="false">当前容器</option><option :value="true">上次终止的容器</option></select></label>
      <label class="log-field">行数<select v-model="tail" class="filter-select"><option value="100">100 行</option><option value="300">300 行</option><option value="1000">1,000 行</option></select></label>
      <button class="act primary" :disabled="loading || !selected" @click="load">{{ loading ? '读取中…' : '读取日志' }}</button>
    </div>
    <p class="log-hint">按需读取日志快照（非实时流）。多容器 Pod 可选择单一容器；容器重启后优先查看上次终止日志。</p>
    <StatePanel v-if="error" compact error title="日志读取失败" :description="error"><button class="act" :disabled="loading" @click="load">重试</button></StatePanel>
    <div v-else-if="loading" class="log-loading" role="status">正在读取 {{ pod }}{{ container ? ' / ' + container : '' }} 的日志…</div>
    <template v-else-if="loaded"><div class="log-caption"><span>{{ previous ? '上次终止日志' : '当前日志' }}{{ container ? ' · ' + container : '' }}</span><span>读取于 {{ loadedAt }} · 非实时</span></div><pre v-if="text" class="log-output" tabindex="0" aria-label="日志内容">{{ text }}</pre><StatePanel v-else compact title="没有日志输出" description="本次读取成功，但未返回文本。可切换当前 / 上次终止日志或调整容器。" /></template>
    <StatePanel v-else compact :title="selected ? '选择日志来源后读取' : '尚无可选择的实例'" :description="selected ? '容器发生重启时，优先查看上次终止日志。' : '实例列表为空也可能由运行态读取不可用造成，请先检查数据来源。'" />
  </section>
</template>

<style scoped>
.log-field { display: flex; flex-direction: column; gap: 5px; color: var(--text-faint); font-size: 12px; min-width: 0; }
.filter-bar { align-items: flex-end; }
.log-field:first-child { flex: 1 1 260px; }
.log-hint, .log-caption, .log-loading { color: var(--text-faint); font-size: 12px; }
.log-caption { display: flex; justify-content: space-between; gap: 12px; margin: 16px 0 8px; }
.log-output { margin: 0; min-height: 260px; max-height: 60vh; overflow: auto; padding: 18px; background: var(--inset-bg); border: 1px solid var(--border); border-radius: var(--radius); color: var(--text); font: 12px/1.8 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
.log-loading { min-height: 180px; display: grid; place-content: center; }
</style>
