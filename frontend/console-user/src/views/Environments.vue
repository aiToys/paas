<script setup lang="ts">
// 环境视图：列出当前租户的物理环境（生产/测试），点进看该环境工作负载。
// 环境是独立一等公民（非应用子节点）；应用×环境多对多。换 Key 自动重载。
import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'

interface Env {
  id: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
  desc?: string
}

const envs = ref<Env[]>([])
const loading = ref(true)
const router = useRouter()

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth('/api/environments')
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const json = await resp.json()
    envs.value = (json.data ?? []) as Env[]
  } catch (e) {
    ElMessage.error('加载环境失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

function open(e: Env) {
  router.push(`/workloads/services?env=${e.id}`)
}

function onKeyChanged() {
  load()
}
onMounted(() => {
  load()
  window.addEventListener('paas:key-changed', onKeyChanged)
})
onUnmounted(() => window.removeEventListener('paas:key-changed', onKeyChanged))
</script>

<template>
  <div class="page">
    <div class="head">
      <div>
        <h2 class="title">环境</h2>
        <p class="sub">物理隔离单元（生产 / 测试）；应用 × 环境多对多，环境内分基线与泳道。</p>
      </div>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 3" :key="i" class="skel" />
    </div>
    <div v-else class="grid">
      <article v-for="e in envs" :key="e.id" class="env-card" :class="{ prod: e.type === 'prod' }" @click="open(e)">
        <div class="card-top">
          <div class="e-icon" :class="e.type">
            <Icon :name="e.type === 'prod' ? 'shield' : 'server'" :size="18" />
          </div>
          <div class="e-titles">
            <div class="e-name-row">
              <h3 class="e-name">{{ e.name }}</h3>
              <span class="type-badge" :class="e.type">{{ e.type === 'prod' ? '生产' : '测试' }}</span>
            </div>
            <div class="e-id mono">{{ e.id }}</div>
          </div>
        </div>
        <div class="card-foot">
          <div class="foot-stat">
            <span class="k">物理落点</span>
            <span class="v mono">{{ e.cluster || '默认' }}</span>
          </div>
        </div>
      </article>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.head {
  margin-bottom: 20px;
}
.title {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 600;
}
.sub {
  margin: 0;
  font-size: 13px;
  color: var(--text-dim);
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 14px;
}
.skel {
  height: 120px;
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2, #1a1f2e) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
  border: 1px solid var(--border);
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
.env-card {
  padding: 18px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  cursor: pointer;
  transition: border-color 0.15s, transform 0.15s, box-shadow 0.15s;
}
.env-card:hover {
  border-color: var(--border-strong);
  transform: translateY(-2px);
  box-shadow: var(--shadow);
}
.card-top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.e-icon {
  width: 40px;
  height: 40px;
  flex-shrink: 0;
  border-radius: 10px;
  display: grid;
  place-items: center;
  background: var(--brand-soft);
  color: var(--brand);
}
.e-icon.prod {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  color: var(--warning, #f59e0b);
}
.e-name-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.e-name {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.type-badge {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 10.5px;
  font-weight: 500;
}
.type-badge.prod {
  background: var(--warning-soft, rgba(245, 158, 11, 0.12));
  color: var(--warning, #f59e0b);
}
.type-badge.test {
  background: var(--surface-2, #1e2433);
  color: var(--text-dim);
}
.e-id {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 2px;
}
.card-foot {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}
.foot-stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.foot-stat .k {
  font-size: 11px;
  color: var(--text-faint);
}
.foot-stat .v {
  font-size: 13px;
  font-weight: 600;
}
</style>
