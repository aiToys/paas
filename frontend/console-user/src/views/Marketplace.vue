<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'

interface Channel {
  id: string
  type: string
  priority: number
  status: 'healthy' | 'degraded' | 'offline'
}

interface Model {
  id: string
  name: string
  vendor: string
  contextWindow: number
  capabilities: string[]
  inputPrice: number
  outputPrice: number
  description: string
  channels: Channel[]
}

type Category = 'dialogue' | 'reasoning' | 'code' | 'embed'

// capability → 分类映射（优先级：embedding > code > reasoning > chat）
function categoryOf(caps: string[]): Category {
  if (caps.includes('embedding')) return 'embed'
  if (caps.includes('code')) return 'code'
  if (caps.includes('reasoning')) return 'reasoning'
  return 'dialogue'
}

// 分类配色（与原卡片视觉一致）
const gradOf: Record<Category, string> = {
  dialogue: 'linear-gradient(135deg,#6366f1,#8b5cf6)',
  reasoning: 'linear-gradient(135deg,#10b981,#06b6d4)',
  code: 'linear-gradient(135deg,#06b6d4,#3b82f6)',
  embed: 'linear-gradient(135deg,#64748b,#475569)',
}

// 供应商首字作图标文字
function shortOf(vendor: string): string {
  return vendor ? vendor[0] : '?'
}

function activeOf(channels: Channel[]): number {
  return channels.filter((c) => c.status === 'healthy').length
}

function ctxLabel(n: number): string {
  return n >= 1024 ? `${Math.round(n / 1024)}K` : String(n)
}

const models = ref<Model[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth('/api/models')
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const json = await resp.json()
    models.value = (json.data ?? []) as Model[]
  } catch (e) {
    ElMessage.error('加载模型失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const categories = [
  { key: 'all', label: '全部' },
  { key: 'dialogue', label: '对话' },
  { key: 'reasoning', label: '推理' },
  { key: 'code', label: '代码' },
  { key: 'embed', label: 'Embedding' },
] as const

const activeCat = ref<string>('all')
const filtered = computed(() => {
  const all = models.value.map((m) => ({ m, cat: categoryOf(m.capabilities) }))
  return activeCat.value === 'all' ? all : all.filter((x) => x.cat === activeCat.value)
})

const router = useRouter()
function tryout(id: string) {
  router.push({ path: '/playground', query: { model: id } })
}
</script>

<template>
  <div class="page">
    <div class="toolbar">
      <div class="cats">
        <button
          v-for="c in categories"
          :key="c.key"
          class="cat"
          :class="{ on: activeCat === c.key }"
          @click="activeCat = c.key"
        >
          {{ c.label }}
        </button>
      </div>
      <div class="count mono">{{ filtered.length }} 个模型</div>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 8" :key="i" class="skel" />
    </div>
    <div v-else class="grid">
      <article
        v-for="{ m, cat } in filtered"
        :key="m.id"
        class="card"
        :class="{ off: activeOf(m.channels) === 0 }"
      >
        <div class="card-head">
          <div class="m-icon" :style="{ background: gradOf[cat] }">{{ shortOf(m.vendor) }}</div>
          <div class="m-titles">
            <h3 class="m-name">{{ m.name }}</h3>
            <div class="m-provider">{{ m.vendor }}</div>
          </div>
          <span v-if="activeOf(m.channels) > 0" class="live" :title="`${activeOf(m.channels)} 个健康通道`">
            <span class="pulse-dot" />
            <span class="mono">{{ activeOf(m.channels) }}</span>
          </span>
          <span v-else class="off-tag">离线</span>
        </div>

        <p class="m-desc">{{ m.description }}</p>

        <div class="m-meta mono">
          <span>{{ ctxLabel(m.contextWindow) }} 上下文</span>
          <i />
          <span>¥{{ m.inputPrice }} / M in</span>
        </div>

        <div class="m-tags">
          <span v-for="c in m.capabilities" :key="c" class="tag">{{ c }}</span>
          <span class="tag ch">通道 {{ m.channels.length }}</span>
        </div>

        <button class="deploy-btn" :disabled="activeOf(m.channels) === 0" @click="tryout(m.id)">
          试用
          <Icon name="playground" :size="15" />
        </button>
      </article>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 20px;
}
.cats {
  display: flex;
  gap: 4px;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.cat {
  padding: 6px 14px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  border-radius: 7px;
  cursor: pointer;
  transition: all 0.12s;
}
.cat:hover {
  color: var(--text);
}
.cat.on {
  background: var(--brand);
  color: #fff;
  box-shadow: 0 2px 8px var(--brand-glow);
}
.count {
  font-size: 12px;
  color: var(--text-faint);
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}
.skel {
  height: 230px;
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
  border: 1px solid var(--border);
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .skel {
    animation: none;
  }
}
.card {
  position: relative;
  padding: 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s, transform 0.15s, box-shadow 0.15s;
}
.card:hover {
  border-color: var(--border-strong);
  transform: translateY(-2px);
  box-shadow: var(--shadow);
}
.card.off {
  opacity: 0.55;
}
.card-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.m-icon {
  width: 42px;
  height: 42px;
  flex-shrink: 0;
  border-radius: 10px;
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 16px;
  color: #fff;
}
.m-name {
  margin: 0;
  font-size: 14.5px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.m-provider {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 1px;
}
.live {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--success);
  font-size: 12px;
}
.off-tag {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-faint);
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--surface-2);
}
.m-desc {
  margin: 14px 0 12px;
  font-size: 12.5px;
  color: var(--text-dim);
  line-height: 1.5;
  min-height: 38px;
}
.m-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
  font-size: 12.5px;
  color: var(--text-dim);
}
.m-meta i {
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: var(--text-faint);
}
.m-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 24px;
}
.tag {
  padding: 3px 9px;
  border-radius: 6px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  font-size: 11.5px;
  color: var(--text-dim);
}
.tag.ch {
  color: var(--text-faint);
}
.deploy-btn {
  margin-top: 18px;
  width: 100%;
  padding: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.12s;
}
.deploy-btn:hover:not(:disabled) {
  background: var(--brand);
  border-color: var(--brand);
  color: #fff;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.deploy-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
</style>
