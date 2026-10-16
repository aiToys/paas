<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { listModels, type ModelInfo } from '@/api/platform'
import { apiError } from '@/api'
import { useListFilters } from '@/composables/useListFilters'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import Icon from '@/components/Icon.vue'
const models = ref<ModelInfo[]>([])
const loading = ref(true)
const error = ref('')
const page = ref(1)
const { filters, reset } = useListFilters({ q: '', vendor: '', capability: '' })
const vendors = computed(() => [...new Set(models.value.map((m) => m.vendor))])
const capabilities = computed(() => [...new Set(models.value.flatMap((m) => m.capabilities ?? []))])
const filtered = computed(() => models.value.filter((m) =>
  (!filters.q || (m.id + (m.name ?? '') + (m.description ?? '')).toLowerCase().includes(filters.q.trim().toLowerCase())) &&
  (!filters.vendor || m.vendor === filters.vendor) && (!filters.capability || m.capabilities?.includes(filters.capability)),
))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch(filters, () => { page.value = 1 })
async function load() {
  loading.value = true
  error.value = ''
  try { models.value = await listModels() }
  catch (e) { error.value = apiError(e, '无法加载模型目录') }
  finally { loading.value = false }
}
onMounted(load)
function fmtContext(n?: number) { return !n ? '—' : n >= 1024 ? Math.round(n / 1024) + 'K' : String(n) }
function channelBadge(m: ModelInfo) {
  const ch = m.channels ?? []
  if (!ch.length) return { label: '无通道', cls: 'idle' }
  if (ch.some((c) => c.status === 'healthy')) return { label: '可用', cls: 'ok' }
  if (ch.some((c) => c.status === 'degraded')) return { label: '降级', cls: 'warn' }
  return { label: '异常', cls: 'err' }
}
</script>
<template>
  <div class="page">
    <PageHeader title="模型目录" description="按供应商、能力与价格选择模型。目录由组织共享，试玩使用当前账户权限。" eyebrow="AI 平台"><RouterLink to="/api-keys" class="act">管理 API 密钥</RouterLink></PageHeader>
    <div class="filter-bar">
      <label class="search-field"><Icon name="search" :size="17" /><input v-model="filters.q" aria-label="搜索模型" placeholder="搜索模型名称或描述" /></label>
      <select v-model="filters.vendor" class="filter-select" aria-label="模型供应商"><option value="">全部供应商</option><option v-for="v in vendors" :key="v">{{ v }}</option></select>
      <select v-model="filters.capability" class="filter-select" aria-label="模型能力"><option value="">全部能力</option><option v-for="c in capabilities" :key="c">{{ c }}</option></select>
      <button v-if="filters.q || filters.vendor || filters.capability" class="text-button" @click="reset">清除筛选</button><span class="result-count">{{ filtered.length }} 个模型</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="模型目录加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <StatePanel v-else-if="!models.length" title="暂无可用模型" description="请联系平台管理员配置模型与供应商通道。" />
    <StatePanel v-else-if="!filtered.length" title="没有匹配的模型" description="尝试其他供应商或能力。"><button class="act" @click="reset">清除筛选</button></StatePanel>
    <template v-else>
      <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table model-table">
<thead><tr><th scope="col">模型</th><th scope="col">供应商</th><th scope="col">上下文</th><th scope="col">能力</th><th scope="col">价格（¥ / 百万 Token）</th><th scope="col">通道状态</th><th scope="col">操作</th></tr></thead>
        <tbody><tr v-for="m in rows" :key="m.id"><td><div class="name-cell"><strong>{{ m.name || m.id }}</strong><code class="s-desc" :title="m.id">{{ m.id }}</code><span v-if="m.description" class="s-desc" :title="m.description">{{ m.description }}</span></div></td><td>{{ m.vendor }}</td><td class="mono">{{ fmtContext(m.contextWindow) }}</td><td><span v-for="c in m.capabilities ?? []" :key="c" class="cap-tag">{{ c }}</span></td><td><div class="price-pair"><span>输入 <b>{{ m.inputPrice ?? '—' }}</b></span><span>输出 <b>{{ m.outputPrice ?? '—' }}</b></span></div></td><td><span class="status" :class="channelBadge(m).cls">{{ channelBadge(m).label }}</span></td><td><RouterLink :to="{ path: '/playground', query: { model: m.id } }" class="act small">试玩 →</RouterLink></td></tr></tbody>
      </table>
</div><TablePager v-model="page" :total="filtered.length" />
    </template>
  </div>
</template>
<style scoped>
@import './list-page.css';
.model-table { min-width: 1000px; }
.model-table td:first-child { max-width: 280px; }
.price-pair { display: flex; flex-direction: column; gap: 4px; white-space: nowrap; font-size: 12px; color: var(--text-faint); }
.price-pair b { color: var(--text-dim); font-weight: 500; font-family: var(--font-mono); }
</style>
