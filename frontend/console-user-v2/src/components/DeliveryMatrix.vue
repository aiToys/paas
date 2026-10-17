<script setup lang="ts">
// 交付·服务×环境版本矩阵（spec 2026-10-17 §5）：基线各环境运行哪个 digest、
// 是否同一候选、发布记录深链与回退入口。数据源 GET /api/applications/{id}/delivery-matrix。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getDeliveryMatrix, type MatrixCell } from '@/api/pipeline'
import { apiError } from '@/api'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string }>()
const router = useRouter()
const envStore = useEnvStore()

const cells = ref<MatrixCell[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    cells.value = await getDeliveryMatrix(props.appId)
  } catch (e) {
    error.value = apiError(e, '无法加载版本矩阵')
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.appId, load)

// 行=服务（空 service 归「（单服务）」），列=环境按 PromoteOrder（envStore 已按阶序）。
const serviceNames = computed(() => {
  const set = new Map<string, string>()
  for (const c of cells.value) set.set(c.serviceName, c.serviceName || '（单服务）')
  return [...set.entries()].map(([key, label]) => ({ key, label }))
})
const envs = computed(() => {
  const ids = new Set(cells.value.map((c) => c.envId))
  const present = envStore.envs.filter((e) => ids.has(e.id))
  return present.length ? present : envStore.envs.slice(0, 2)
})
function cellOf(serviceKey: string, envId: string): MatrixCell | undefined {
  return cells.value.find((c) => (c.serviceName || '') === serviceKey && c.envId === envId)
}
// 同一服务跨环境是否同一 digest（晋升一致性视角）。
function digestOf(c?: MatrixCell): string { return c?.imageDigest ?? '' }
function sameCandidate(serviceKey: string): boolean {
  const ds = envs.value.map((e) => digestOf(cellOf(serviceKey, e.id))).filter(Boolean)
  return ds.length > 1 && new Set(ds).size === 1
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">环境版本矩阵</span>
      <span class="card-sub">各环境基线运行的镜像版本（digest），跨环境一致即可安全晋升</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <div v-else-if="error" class="empty-hint">{{ error }} <button class="act" @click="load">重试</button></div>
    <div v-else-if="!cells.length" class="empty-hint">尚无基线部署——发布或流水线部署后在此展示各环境版本</div>
    <div v-else class="table-scroll" data-table-scroll tabindex="0">
      <table class="mini-table matrix">
        <thead>
          <tr>
            <th>服务</th>
            <th v-for="e in envs" :key="e.id">{{ e.name }}<span class="env-type" :class="e.type">{{ e.type }}</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in serviceNames" :key="s.key">
            <td class="t-name">
              {{ s.label }}
              <span v-if="sameCandidate(s.key)" class="same-chip">各环境一致</span>
            </td>
            <td v-for="e in envs" :key="e.id">
              <template v-if="cellOf(s.key, e.id)">
                <div class="cell">
                  <span class="digest mono" :title="cellOf(s.key, e.id)!.imageDigest || cellOf(s.key, e.id)!.imageTag">
                    {{ cellOf(s.key, e.id)!.imageDigest ? cellOf(s.key, e.id)!.imageDigest!.slice(7, 19) : (cellOf(s.key, e.id)!.imageTag || '—') }}
                  </span>
                  <span class="cell-status" :class="cellOf(s.key, e.id)!.ready >= cellOf(s.key, e.id)!.replicas && cellOf(s.key, e.id)!.replicas > 0 ? 'ok' : 'warn'">
                    {{ cellOf(s.key, e.id)!.ready }}/{{ cellOf(s.key, e.id)!.replicas }}
                  </span>
                  <a v-if="cellOf(s.key, e.id)!.releaseId" class="rel-link" @click="router.push(`/releases/${cellOf(s.key, e.id)!.releaseId}`)">发布单 →</a>
                  <span v-else class="dim">无发布记录</span>
                </div>
              </template>
              <span v-else class="dim">未部署</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.env-type { margin-left: 6px; font-size: 10.5px; padding: 0 5px; border-radius: 3px; }
.env-type.prod { background: rgba(244,63,94,.1); color: #be123c; }
.env-type.test { background: rgba(16,185,129,.1); color: #059669; }
.cell { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.digest { font-size: 12px; color: var(--text); background: var(--surface-2); padding: 1px 6px; border-radius: 4px; }
.cell-status { font-size: 11.5px; }
.cell-status.ok { color: #059669; }
.cell-status.warn { color: #b45309; }
.rel-link { color: var(--brand); font-size: 12px; cursor: pointer; }
.same-chip { margin-left: 6px; font-size: 10.5px; padding: 0 6px; border-radius: 999px; background: rgba(16,185,129,.1); color: #059669; }
.dim { color: var(--text-faint); font-size: 12px; }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
