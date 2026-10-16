<script setup lang="ts">
import { apiError } from '@/api'
import StatePanel from './StatePanel.vue'
// 交付·发布（v2 原生）：版本里程碑列表（环境/镜像 digest/策略/状态/回滚指针）。
import { computed, onMounted, ref } from 'vue'
import { listReleases, type Release } from '@/api/pipeline'
import { statusOf, tagTypeToCls, RELEASE_STATUS } from '@/composables/useStatus'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

const releases = ref<Release[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    releases.value = await listReleases(props.appId)
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
  }
}
onMounted(load)

const envName = computed(() => (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id)
const shortDigest = (d: string) => (d ? d.replace('sha256:', '').slice(0, 12) : '-')
const STRATEGY_LABEL: Record<string, string> = { rolling: '滚动', canary: '金丝雀', blue_green: '蓝绿' }
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">发布</span>
      <span class="card-sub">版本里程碑（git tag + 镜像版本），可回滚</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <div v-else-if="!releases.length" class="empty-hint">
      暂无发布——流水线 release 阶段产出版本里程碑
    </div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="mini-table">
      <thead><tr><th>状态</th><th>环境</th><th>镜像</th><th>策略</th><th>回滚自</th></tr></thead>
      <tbody>
        <tr v-for="r in releases" :key="r.id">
          <td>
            <span class="status" :class="tagTypeToCls(statusOf(RELEASE_STATUS, r.status).type)">
              {{ statusOf(RELEASE_STATUS, r.status).label }}
            </span>
          </td>
          <td>{{ envName(r.envId) }}</td>
          <td><span class="mono dim" :title="r.imageDigest">{{ shortDigest(r.imageDigest) }}</span></td>
          <td class="dim">{{ STRATEGY_LABEL[r.strategy] ?? r.strategy }}</td>
          <td>
            <span v-if="r.previousImageId" class="mono dim">{{ shortDigest(r.previousImageId) }}</span>
            <span v-else class="dim">-</span>
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
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
