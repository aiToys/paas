<script setup lang="ts">
import { computed, ref } from 'vue'
import type { RuntimeInstance } from '@/api/workload'
import { instanceState } from '@/domain/serviceRuntime'
import StatePanel from './StatePanel.vue'
const props = defineProps<{ instances: RuntimeInstance[] }>()
const emit = defineEmits<{ inspect: [instance: RuntimeInstance, section: 'details' | 'logs' | 'events' | 'terminal'] }>()
const query = ref('')
const attention = ref(false)
const filtered = computed(() => props.instances.filter((i) => (!query.value || `${i.name} ${i.node || ''} ${i.message || ''}`.toLowerCase().includes(query.value.trim().toLowerCase())) && (!attention.value || ['warn', 'err'].includes(instanceState(i).cls) || i.restarts > 0)))
// 容器级等待/终止原因（服务端给出，非前端猜测）：取第一个非 running 容器的 reason。
function containerReason(i: RuntimeInstance): string {
  return i.reason || i.containers?.find((c) => c.state !== 'running' && c.reason)?.reason || ''
}
</script>
<template>
  <section aria-label="Pod 实例">
    <div class="filter-bar"><label class="search-field"><input v-model="query" aria-label="搜索 Pod 或节点" placeholder="搜索 Pod、节点或异常原因" /></label><label class="attention-filter"><input v-model="attention" type="checkbox" /> 仅看未就绪 / 失败 / 曾重启</label><span class="result-count">本次返回 {{ instances.length }} 个 Pod</span></div>
    <StatePanel v-if="!instances.length" compact title="未返回实例数据" description="结合运行态来源判断：读取不可用与没有 Pod 的展示相同，不能据此判断服务已停止。" />
    <StatePanel v-else-if="!filtered.length" compact title="没有匹配的实例" description="调整搜索条件，或取消关注筛选。" />
    <div v-else class="table-scroll" tabindex="0" aria-label="实例表格，可横向滚动"><table class="list-table instance-table"><thead><tr><th scope="col">实例 / 异常原因</th><th scope="col">容器就绪</th><th scope="col">累计重启</th><th scope="col">节点 / IP</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="i in filtered" :key="i.uid || i.name"><td><button class="text-button pod-name mono" @click="emit('inspect', i, 'details')">{{ i.name }}</button><div v-if="containerReason(i)" class="pod-message">{{ containerReason(i) }}</div><div v-if="i.startedAt" class="subtext">启动于 {{ new Date(i.startedAt).toLocaleString() }}</div><div v-if="i.deletionTimestamp" class="subtext terminating">终止中</div></td><td><span class="status" :class="instanceState(i).cls">{{ instanceState(i).label }}</span><div class="subtext mono">{{ i.ready || '—' }} · {{ i.status }}</div><template v-if="i.containers && i.containers.length > 1"><div v-for="c in i.containers" :key="c.name" class="container-line"><span class="mono">{{ c.name }}</span><span class="status" :class="c.state === 'running' && c.ready ? 'ok' : c.state === 'waiting' ? 'warn' : 'idle'">{{ c.state }}{{ c.ready ? '·ready' : '' }}</span></div></template></td><td><span class="mono" :class="{ 'restart-warning': i.restarts > 0 }">{{ i.restarts }}</span></td><td><div class="mono">{{ i.node || '未调度' }}</div><div class="subtext mono">{{ i.ip || '尚未分配 IP' }}</div></td><td><div class="instance-actions"><button class="text-button" @click="emit('inspect', i, 'logs')">日志</button><button class="text-button" @click="emit('inspect', i, 'events')">事件</button><button class="text-button" @click="emit('inspect', i, 'terminal')">终端</button></div></td></tr></tbody></table></div>
    <p class="table-note">“容器就绪”依据本次返回的 Ready 数量，不等于业务健康；累计重启不代表当前仍在故障。</p>
  </section>
</template>
<style scoped>
.instance-table { min-width: 780px; }
.pod-name { text-align: left; max-width: 360px; overflow-wrap: anywhere; font-weight: 600; }
.pod-message { max-width: 390px; margin-top: 6px; color: var(--warning); overflow-wrap: anywhere; font-size: 12px; }
.subtext { margin-top: 5px; font-size: 12px; color: var(--text-faint); }
.subtext.terminating { color: var(--warning); }
.container-line { display: flex; justify-content: space-between; gap: 8px; margin-top: 4px; font-size: 11px; color: var(--text-dim); }
.instance-actions { display: flex; gap: 14px; white-space: nowrap; }
.attention-filter { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-dim); }
.restart-warning { color: var(--warning); }
.table-note { color: var(--text-faint); font-size: 12px; margin: 12px 0 0; }
</style>
