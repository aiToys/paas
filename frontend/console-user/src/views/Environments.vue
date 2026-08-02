<script setup lang="ts">
// 环境管理面：环境实体 CRUD + 跨环境总览。
// 与顶栏 scope（操作面）区分：这里「管理环境本身」，不切换工作环境。
// 卡片显示统计（工作负载数/健康度）；点击进环境详情页（不再跳工作负载列表）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

interface Env {
  id: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
}
interface Workload {
  id: string
  envId: string
  type: string
  replicas: number
  ready: number
  status: string
}

const envs = ref<Env[]>([])
const workloads = ref<Workload[]>([])
const loading = ref(true)
const router = useRouter()
const envStore = useEnvStore()

const showCreate = ref(false)
const form = ref({ name: '', type: 'test', cluster: '' })
const submitting = ref(false)

async function load() {
  loading.value = true
  try {
    const [eResp, wResp] = await Promise.all([
      fetchAuth('/api/environments'),
      fetchAuth('/api/workloads'),
    ])
    if (eResp.ok) envs.value = (await eResp.json()).data ?? []
    if (wResp.ok) workloads.value = (await wResp.json()).data ?? []
  } catch (e) {
    ElMessage.error('加载环境失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

const wlByEnv = computed(() => {
  const m = new Map<string, Workload[]>()
  for (const w of workloads.value) {
    const arr = m.get(w.envId) ?? []
    arr.push(w)
    m.set(w.envId, arr)
  }
  return m
})

function wlCount(envId: string) {
  return (wlByEnv.value.get(envId) ?? []).length
}

function health(envId: string): { label: string; cls: string } {
  const wls = wlByEnv.value.get(envId) ?? []
  if (!wls.length) return { label: '空闲', cls: 'idle' }
  if (wls.some((w) => w.status === 'failed' || w.ready < w.replicas)) {
    return { label: '有异常', cls: 'warn' }
  }
  return { label: '正常', cls: 'ok' }
}

function open(e: Env) {
  router.push(`/environments/${e.id}`)
}

async function create() {
  const body = { ...form.value }
  if (body.type !== 'prod') body.cluster = ''
  submitting.value = true
  try {
    const resp = await fetchAuth('/api/environments', {
      method: 'POST',
      body: JSON.stringify(body),
    })
    if (resp.ok) {
      ElMessage.success('环境已创建')
      showCreate.value = false
      form.value = { name: '', type: 'test', cluster: '' }
      load()
      envStore.loadEnvs() // 同步顶栏 scope 选择器
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败（生产环境需管理员权限）')
    }
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
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
        <p class="sub">物理隔离单元（生产/测试）。管理面：创建与总览；点环境进详情。切换工作环境走顶栏 scope。</p>
      </div>
      <button class="new-btn" @click="showCreate = true">+ 创建环境</button>
    </div>

    <div v-if="loading" class="grid">
      <div v-for="i in 3" :key="i" class="skel" />
    </div>
    <div v-else-if="!envs.length" class="empty">
      <Icon name="shield" :size="32" />
      <p>暂无环境，点击右上角创建</p>
    </div>
    <div v-else class="grid">
      <article
        v-for="e in envs"
        :key="e.id"
        class="env-card"
        :class="{ prod: e.type === 'prod' }"
        @click="open(e)"
      >
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
            <span class="k">工作负载</span>
            <span class="v mono">{{ wlCount(e.id) }}</span>
          </div>
          <div class="foot-stat">
            <span class="k">健康</span>
            <span class="v health" :class="health(e.id).cls">{{ health(e.id).label }}</span>
          </div>
          <div class="foot-stat">
            <span class="k">物理落点</span>
            <span class="v mono">{{ e.cluster || '默认' }}</span>
          </div>
        </div>
      </article>
    </div>

    <el-dialog v-model="showCreate" title="创建环境" width="440px">
      <el-form label-width="80px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如 生产-深圳" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%">
            <el-option label="测试" value="test" />
            <el-option label="生产" value="prod" />
          </el-select>
        </el-form-item>
        <el-form-item label="物理落点">
          <el-input v-model="form.cluster" placeholder="如 prod-sz（生产多区填，测试可空）" />
        </el-form-item>
      </el-form>
      <div class="create-hint">生产环境创建需管理员权限（prod:write）。</div>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="!form.name || submitting" @click="create">
          {{ submitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
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
.new-btn {
  padding: 8px 16px;
  border: none;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 14px;
}
.skel {
  height: 140px;
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
.env-card.prod {
  border-color: var(--warning-soft, rgba(245, 158, 11, 0.3));
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
  display: flex;
  gap: 18px;
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
.health.ok {
  color: var(--success);
}
.health.warn {
  color: var(--warning);
}
.health.idle {
  color: var(--text-faint);
}
.create-hint {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: -4px;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 60px 20px;
  color: var(--text-faint);
  text-align: center;
}
.empty :deep(svg) {
  color: var(--brand);
}
.empty p {
  margin: 0;
  font-size: 13px;
}
</style>
