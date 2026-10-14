<script setup lang="ts">
// 应用详情 - 部署 tab：工作负载按环境分组（基线 default 不单显，归到所属环境）。
// 数据由父组件传入（应用详情副数据，与概览副本统计共享）；点行打开 WorkloadDetailDrawer
// 详情抽屉（与 Workloads 视图/服务 tab 单一真源交互）。
import { computed, ref } from 'vue'
import Icon from '@/components/Icon.vue'
import WorkloadDetailDrawer from '@/components/WorkloadDetailDrawer.vue'
import type { Workload } from '@/api/workload'

interface Env { id: string; name: string; type: string }
const props = defineProps<{ workloads: Workload[]; envs: Env[]; failed: boolean }>()
defineEmits<{ (e: 'retry'): void }>()

const workloadsByEnv = computed(() => {
  const groups = new Map<string, Workload[]>()
  for (const w of props.workloads) {
    if (!groups.has(w.envId)) groups.set(w.envId, [])
    groups.get(w.envId)!.push(w)
  }
  return [...groups.entries()].map(([envId, items]) => ({
    envId,
    name: props.envs.find((e) => e.id === envId)?.name ?? envId,
    type: props.envs.find((e) => e.id === envId)?.type ?? '',
    items,
  }))
})

const showWlDetail = ref(false)
const wlDetailId = ref('')
function openWlDetail(w: Workload) {
  wlDetailId.value = w.id
  showWlDetail.value = true
}
</script>

<template>
  <div class="deploy-view">
    <div v-if="failed" class="load-fail">
      <span>工作负载加载失败（网络或服务异常，并非「未部署」）</span>
      <button class="retry-btn" @click="$emit('retry')">重试</button>
    </div>
    <div v-else-if="!workloads.length" class="empty">
      <Icon name="server" :size="28" />
      <p>该应用尚未部署工作负载</p>
    </div>
    <div v-else class="env-groups">
      <section v-for="g in workloadsByEnv" :key="g.envId" class="env-group">
        <div class="env-group-head">
          <Icon v-if="g.type === 'prod'" name="shield" :size="14" />
          <span class="env-group-name">{{ g.name }}</span>
          <span class="env-group-count mono">{{ g.items.length }}</span>
        </div>
        <div class="wl-list">
          <div v-for="w in g.items" :key="w.id" class="wl-row clickable" @click="openWlDetail(w)">
            <div class="wl-main">
              <span class="wl-name">{{ w.name }}</span>
              <span class="wl-type">{{ w.type }}</span>
              <span v-if="w.laneId && w.laneId !== 'default'" class="wl-lane">泳道 {{ w.laneId }}</span>
              <a v-if="w.domain" :href="'http://' + w.domain" target="_blank" rel="noopener" class="wl-domain" @click.stop>
                <Icon name="link" :size="12" />{{ w.domain }}
              </a>
              <span class="wl-img mono">{{ w.image }}</span>
              <span v-if="w.schedule" class="wl-sched mono">{{ w.schedule }}</span>
            </div>
            <div class="wl-side">
              <span class="reps mono" :class="{ notready: w.ready < w.replicas }">{{ w.ready }}/{{ w.replicas }}</span>
              <span class="wl-status">{{ w.status }}</span>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 工作负载详情抽屉（点行打开；与 Workloads/服务 tab 单一真源组件） -->
    <WorkloadDetailDrawer v-model="showWlDetail" :workload-id="wlDetailId" />
  </div>
</template>

<style scoped>
/* 副数据加载失败提示条：区别于空态（空态会被误读为「未部署」），带重试出口 */
.load-fail {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  margin-bottom: 16px;
  border: 1px dashed var(--warning, #f59e0b);
  border-radius: var(--radius);
  background: rgba(245, 158, 11, 0.08);
  font-size: 12.5px;
  color: var(--warning, #f59e0b);
}
.retry-btn {
  flex-shrink: 0;
  padding: 4px 12px;
  border: 1px solid var(--warning, #f59e0b);
  border-radius: 5px;
  background: transparent;
  color: var(--warning, #f59e0b);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
}
.retry-btn:hover {
  background: rgba(245, 158, 11, 0.12);
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 40px;
  color: var(--text-faint);
}
.empty :deep(svg) {
  color: var(--brand);
}
.env-groups {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.env-group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  color: var(--text-dim);
}
.env-group-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.env-group-count {
  font-size: 11px;
  color: var(--text-faint);
  padding: 1px 7px;
  background: var(--surface-2, transparent);
  border-radius: 8px;
}
.wl-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.wl-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.wl-row.clickable {
  cursor: pointer;
  transition: background 0.12s, border-color 0.12s;
}
.wl-row.clickable:hover {
  background: var(--surface-2);
}
.wl-main {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.wl-name {
  font-weight: 600;
  color: var(--text);
}
.wl-type {
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--brand-soft);
  color: var(--brand);
  font-size: 11px;
}
.wl-lane {
  padding: 2px 8px;
  border-radius: 4px;
  background: rgba(245, 158, 11, 0.12);
  color: #f59e0b;
  font-size: 11px;
}
.wl-domain {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 2px 8px; border-radius: var(--radius);
  background: rgba(34, 197, 94, 0.1); color: #16a34a;
  font-size: 11px; text-decoration: none;
}
.wl-domain:hover { opacity: 0.8; }
.wl-img {
  font-size: 12px;
  color: var(--text-dim);
}
.wl-sched {
  font-size: 12px;
  color: var(--brand);
}
.wl-side {
  display: flex;
  align-items: center;
  gap: 14px;
}
.reps {
  font-weight: 600;
  color: var(--success);
}
.reps.notready {
  color: var(--warning);
}
.wl-status {
  font-size: 12px;
  color: var(--text-dim);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
