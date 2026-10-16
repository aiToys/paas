<script setup lang="ts">
import { apiError } from '@/api'
import StatePanel from './StatePanel.vue'
// 交付·代码仓库（v2 原生）：接入的仓库列表（内置 Gitea / 外部仓库）。
import { onMounted, ref } from 'vue'
import { listRepositories, type Repo } from '@/api/pipeline'

const props = defineProps<{ appId: string }>()

const repos = ref<Repo[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    repos.value = await listRepositories(props.appId)
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">代码仓库</span>
      <span class="card-sub">构建与流水线的代码源</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <div v-else-if="!repos.length" class="empty-hint">暂未接入仓库</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="mini-table">
      <thead><tr><th>名称</th><th>来源</th><th>默认分支</th></tr></thead>
      <tbody>
        <tr v-for="r in repos" :key="r.id">
          <td class="t-name">{{ r.name }}</td>
          <td class="dim">{{ r.internal ? '内置 Gitea' : '外部' }}</td>
          <td><span class="mono dim">{{ r.defaultBranch || 'main' }}</span></td>
        </tr>
      </tbody>
    </table>
</div>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.dim { color: var(--text-faint); }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
