<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'

interface Model {
  id: string
  name: string
  short: string
  provider: string
  params: string
  context: string
  category: 'dialogue' | 'reasoning' | 'code' | 'embed'
  tags: string[]
  active: number // 活跃实例数
  gradient: string
}

const models = ref<Model[]>([
  { id: 'qwen2.5-7b', name: 'Qwen2.5-7B-Instruct', short: 'Qwen', provider: '阿里云', params: '7B', context: '32K', category: 'dialogue', tags: ['对话', '中文'], active: 12, gradient: 'linear-gradient(135deg,#6366f1,#8b5cf6)' },
  { id: 'qwen2.5-72b', name: 'Qwen2.5-72B-Instruct', short: 'Qwen', provider: '阿里云', params: '72B', context: '128K', category: 'dialogue', tags: ['旗舰', '对话'], active: 3, gradient: 'linear-gradient(135deg,#6366f1,#8b5cf6)' },
  { id: 'deepseek-v3', name: 'DeepSeek-V3', short: 'DS', provider: 'DeepSeek', params: '671B·MoE', context: '64K', category: 'reasoning', tags: ['推理', 'MoE'], active: 8, gradient: 'linear-gradient(135deg,#10b981,#06b6d4)' },
  { id: 'deepseek-r1', name: 'DeepSeek-R1', short: 'DS', provider: 'DeepSeek', params: '671B', context: '128K', category: 'reasoning', tags: ['推理', '深度思考'], active: 5, gradient: 'linear-gradient(135deg,#10b981,#06b6d4)' },
  { id: 'llama3.3-70b', name: 'Llama-3.3-70B', short: 'Lm', provider: 'Meta', params: '70B', context: '128K', category: 'dialogue', tags: ['对话'], active: 2, gradient: 'linear-gradient(135deg,#f59e0b,#f43f5e)' },
  { id: 'glm-4-9b', name: 'GLM-4-9B-Chat', short: 'GLM', provider: '智谱', params: '9B', context: '128K', category: 'dialogue', tags: ['对话', '中文'], active: 6, gradient: 'linear-gradient(135deg,#ec4899,#8b5cf6)' },
  { id: 'qwen2.5-coder-32b', name: 'Qwen2.5-Coder-32B', short: 'Qwen', provider: '阿里云', params: '32B', context: '128K', category: 'code', tags: ['代码', '补全'], active: 4, gradient: 'linear-gradient(135deg,#06b6d4,#3b82f6)' },
  { id: 'bge-m3', name: 'BGE-M3', short: 'BGE', provider: 'BAAI', params: '568M', context: '8K', category: 'embed', tags: ['Embedding', '检索'], active: 15, gradient: 'linear-gradient(135deg,#64748b,#475569)' },
])

const categories = [
  { key: 'all', label: '全部' },
  { key: 'dialogue', label: '对话' },
  { key: 'reasoning', label: '推理' },
  { key: 'code', label: '代码' },
  { key: 'embed', label: 'Embedding' },
] as const

const activeCat = ref<string>('all')
const filtered = computed(() =>
  activeCat.value === 'all' ? models.value : models.value.filter((m) => m.category === activeCat.value)
)

const router = useRouter()
function deploy(m: Model) {
  router.push({ path: '/deployments', query: { model: m.id } })
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

    <div class="grid">
      <article v-for="m in filtered" :key="m.id" class="card">
        <div class="card-head">
          <div class="m-icon" :style="{ background: m.gradient }">{{ m.short }}</div>
          <div class="m-titles">
            <h3 class="m-name">{{ m.name }}</h3>
            <div class="m-provider">{{ m.provider }}</div>
          </div>
          <span v-if="m.active > 0" class="live" :title="`${m.active} 个运行中实例`">
            <span class="pulse-dot" />
            <span class="mono">{{ m.active }}</span>
          </span>
        </div>

        <div class="m-meta mono">
          <span>{{ m.params }}</span>
          <i />
          <span>{{ m.context }} 上下文</span>
        </div>

        <div class="m-tags">
          <span v-for="t in m.tags" :key="t" class="tag">{{ t }}</span>
        </div>

        <button class="deploy-btn" @click="deploy(m)">
          部署
          <Icon name="deploy" :size="15" />
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
  font-size: 14px;
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
.m-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 16px 0 12px;
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
.deploy-btn:hover {
  background: var(--brand);
  border-color: var(--brand);
  color: #fff;
  box-shadow: 0 4px 14px var(--brand-glow);
}
</style>
