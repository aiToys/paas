<script setup lang="ts">
// 首页（spec §9.3）= 产品列表 + 待办横幅。禁止假指标卡墙（健康/副本/请求秒）——
// 行内只放真实可得的信号；服务数/环境就绪聚合列待后端聚合端点（后续批次）。
// 待办数据源 = /api/notifications（实时聚合，无持久化），抽屉三列直达老版深链。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchJSON, apiError } from '@/api'

// —— 新建产品（v2 原生，POST /api/applications {name, desc}；环境归属由服务/工作负载决定）——
const showCreate = ref(false)
const creating = ref(false)
const createForm = ref({ name: '', desc: '' })
async function createProduct() {
  if (!createForm.value.name.trim()) {
    ElMessage.warning('请输入产品名称')
    return
  }
  creating.value = true
  try {
    await fetchJSON('/api/applications', {
      method: 'POST',
      body: JSON.stringify({ name: createForm.value.name.trim(), desc: createForm.value.desc.trim() }),
    })
    ElMessage.success('产品创建成功')
    showCreate.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    creating.value = false
  }
}

const router = useRouter()

interface Product {
  id: string
  name: string
  desc?: string
  bindings?: { type: string }[]
}
const products = ref<Product[]>([])
const loading = ref(true)
// 每产品服务数（spec §9.3 产品行信号；真实计数非假指标，失败的产品留空不显示）。
const svcCount = ref<Record<string, number>>({})

interface Notification {
  ID?: string
  id: string
  type: string
  severity: 'error' | 'warning' | 'info'
  title: string
  appId: string
  targetType: string
  targetId: string
  at: string
}
const notifications = ref<Notification[]>([])
const notifFailed = ref(false)
const showTodo = ref(false)

async function load() {
  loading.value = true
  try {
    const [ps, ns] = await Promise.allSettled([
      fetchJSON<Product[]>('/api/applications'),
      fetchJSON<Notification[]>('/api/notifications'),
    ])
    if (ps.status === 'fulfilled') products.value = ps.value
    else ElMessage.error(apiError(ps.reason, '加载产品列表失败'))
    if (ns.status === 'fulfilled') notifications.value = ns.value
    else notifFailed.value = true
    // 并发拉各产品服务数（轻请求；失败的产品留空）。
    const counts = await Promise.allSettled(
      products.value.map((p) => fetchJSON<unknown[]>(`/api/applications/${p.id}/services`)),
    )
    const m: Record<string, number> = {}
    products.value.forEach((p, i) => {
      if (counts[i].status === 'fulfilled') m[p.id] = counts[i].value.length
    })
    svcCount.value = m
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 待办横幅：两类需要行动的信号（失败 / 待决策）；info 类进行中不进横幅数字（噪音控制）。
const failedCount = computed(() => notifications.value.filter((n) => n.severity === 'error').length)
const pendingCount = computed(() =>
  notifications.value.filter((n) => n.type === 'run_paused' || n.type === 'batch_approve').length,
)
const hasTodo = computed(() => failedCount.value + pendingCount.value > 0)

// 抽屉三列（spec §9.6）：失败待处理 / 等待决策 / 进行中。
const failedItems = computed(() => notifications.value.filter((n) => n.severity === 'error'))
const pendingItems = computed(() =>
  notifications.value.filter((n) => n.type === 'run_paused' || n.type === 'batch_approve'),
)
const runningItems = computed(() =>
  notifications.value.filter((n) => n.type === 'run_running' || n.type === 'batch_testing' || n.type === 'batch_releasing'),
)

function productName(appId: string): string {
  return products.value.find((p) => p.id === appId)?.name ?? appId
}
// 待办类型 tag（spec §9.6：审批与评审用 tag 区分）。
function todoTag(n: Notification): string {
  if (n.type === 'run_paused') return '审批'
  if (n.type === 'batch_approve') return '发车'
  if (n.type === 'build_failed') return '构建'
  if (n.type === 'alert') return '告警'
  return ''
}
// 待办直达：run 落 v2 简版详情（可审批/终止/重试闭环）；batch/build/change/alert
// 的详情页 v2 未建，并行期跳老版深链。↗ 只标跳老版（v2 内链不带）。
function todoLink(n: Notification): string {
  if (n.targetType === 'run') return `/next/runs/${n.targetId}`
  if (n.targetType === 'batch') return `/console/devops/batches/${n.targetId}`
  if (n.targetType === 'build') return `/console/devops/builds/${n.targetId}`
  if (n.targetType === 'alert') return '/console/platform/observability'
  if (n.targetType === 'change') return `/console/devops/changes/${n.targetId}`
  return '/console/devops'
}
// 链接是否离开 v2（跳老版才显示 ↗）。
function isExternal(href: string): boolean {
  return !href.startsWith('/next/')
}
function fmtAgo(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const diff = Math.max(0, Date.now() - t)
  const m = Math.floor(diff / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}
</script>

<template>
  <div class="home">
    <!-- 待办横幅：窄条，非卡墙；点开抽屉 -->
    <div v-if="hasTodo" class="todo-banner" @click="showTodo = true">
      <span class="todo-item err">⚠ 失败 {{ failedCount }}</span>
      <span class="todo-sep">·</span>
      <span class="todo-item warn">待审批 {{ pendingCount }}</span>
      <span class="todo-open">查看待办 ▸</span>
    </div>

    <div class="head-row">
      <h2 class="title">产品</h2>
      <div class="head-actions">
        <a class="primary-btn" @click="showCreate = true">新建产品</a>
      </div>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载产品…"></div>

    <div v-else-if="!products.length" class="empty">
      <Icon name="rocket" :size="28" />
      <p>还没有产品——从一个产品开始组织你的服务</p>
      <a class="primary-btn" @click="showCreate = true">去创建第一个产品</a>
    </div>

    <div v-else class="product-list">
      <div
        v-for="p in products"
        :key="p.id"
        class="product-row clickable"
        @click="router.push(`/products/${p.id}`)"
      >
        <div class="p-main">
          <span class="p-name">{{ p.name }}</span>
          <span v-if="p.desc" class="p-desc">{{ p.desc }}</span>
        </div>
        <div class="p-side">
          <span class="p-meta" v-if="svcCount[p.id] !== undefined">{{ svcCount[p.id] }} 个服务</span>
          <span class="p-meta" v-else-if="p.bindings?.length">{{ p.bindings.length }} 项绑定资源</span>
          <span class="p-enter">进入 →</span>
        </div>
      </div>
    </div>

    <!-- 待办抽屉（spec §9.6 三列 + 底部「查看全部运行」出口） -->
    <el-drawer v-model="showTodo" title="待办" size="720px">
      <div v-if="notifFailed" class="drawer-empty">待办加载失败，稍后重试</div>
      <div v-else class="todo-cols">
        <div class="todo-col">
          <div class="col-title err">失败待处理（{{ failedItems.length }}）</div>
          <div v-for="n in failedItems" :key="n.id" class="todo-card">
            <div class="t-head"><span v-if="todoTag(n)" class="t-tag">{{ todoTag(n) }}</span>{{ productName(n.appId) }} · {{ n.title }}</div>
            <div class="t-foot">
              <span class="t-time">{{ fmtAgo(n.at) }}</span>
              <a :href="todoLink(n)" class="t-link">查看{{ isExternal(todoLink(n)) ? ' ↗' : ' →' }}</a>
            </div>
          </div>
          <div v-if="!failedItems.length" class="col-empty">无</div>
        </div>
        <div class="todo-col">
          <div class="col-title warn">等待决策（{{ pendingItems.length }}）</div>
          <div v-for="n in pendingItems" :key="n.id" class="todo-card">
            <div class="t-head"><span v-if="todoTag(n)" class="t-tag">{{ todoTag(n) }}</span>{{ productName(n.appId) }} · {{ n.title }}</div>
            <div class="t-foot">
              <span class="t-time">{{ fmtAgo(n.at) }}</span>
              <a :href="todoLink(n)" class="t-link">去处理{{ isExternal(todoLink(n)) ? ' ↗' : ' →' }}</a>
            </div>
          </div>
          <div v-if="!pendingItems.length" class="col-empty">无</div>
        </div>
        <div class="todo-col">
          <div class="col-title info">进行中（{{ runningItems.length }}）</div>
          <div v-for="n in runningItems" :key="n.id" class="todo-card">
            <div class="t-head"><span v-if="todoTag(n)" class="t-tag">{{ todoTag(n) }}</span>{{ productName(n.appId) }} · {{ n.title }}</div>
            <div class="t-foot">
              <span class="t-time">{{ fmtAgo(n.at) }}</span>
              <a :href="todoLink(n)" class="t-link">看运行{{ isExternal(todoLink(n)) ? ' ↗' : ' →' }}</a>
            </div>
          </div>
          <div v-if="!runningItems.length" class="col-empty">无</div>
        </div>
      </div>
      <template #footer>
        <a href="/console/devops" class="all-runs-link">查看全部运行 →</a>
      </template>
    </el-drawer>

    <!-- 新建产品（v2 原生表单） -->
    <el-dialog v-model="showCreate" title="新建产品" width="440px">
      <el-form label-position="top" @submit.prevent="createProduct">
        <el-form-item label="产品名" required>
          <el-input v-model="createForm.name" placeholder="如 paas-shop" @keyup.enter="createProduct" />
        </el-form-item>
        <el-form-item label="描述（可选）">
          <el-input v-model="createForm.desc" type="textarea" :rows="2" placeholder="一句话说明这个产品是做什么的" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="createProduct">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.home {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.todo-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  font-size: 13px;
  cursor: pointer;
  transition: border-color 0.12s;
}
.todo-banner:hover {
  border-color: var(--border-strong);
}
.todo-item.err { color: var(--danger, #f43f5e); font-weight: 600; }
.todo-item.warn { color: var(--warning, #f59e0b); font-weight: 600; }
.todo-sep { color: var(--text-faint); }
.todo-open {
  margin-left: auto;
  color: var(--text-dim);
  font-size: 12px;
}
.head-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.title {
  margin: 0;
  font-size: 16px;
}
.ghost-btn {
  padding: 7px 14px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  color: var(--text);
  font-size: 13px;
  text-decoration: none;
}
.ghost-btn:hover {
  background: var(--surface-2);
}
.primary-btn {
  padding: 7px 14px;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-size: 13px;
  text-decoration: none;
  cursor: pointer;
}
.loading-block {
  height: 200px;
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 64px 0;
  color: var(--text-faint);
  text-align: center;
}
.empty p {
  margin: 0;
  font-size: 13px;
}
.product-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.product-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.product-row.clickable {
  cursor: pointer;
  transition: background 0.12s, border-color 0.12s;
}
.product-row.clickable:hover {
  background: var(--surface-2);
  border-color: var(--border-strong);
}
.p-main {
  display: flex;
  align-items: baseline;
  gap: 12px;
  min-width: 0;
}
.p-name {
  font-size: 14px;
  font-weight: 600;
}
.p-desc {
  font-size: 12.5px;
  color: var(--text-dim);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.p-side {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-shrink: 0;
}
.p-meta {
  font-size: 12px;
  color: var(--text-faint);
}
.p-enter {
  font-size: 12.5px;
  color: var(--brand);
}

/* 待办抽屉 */
.drawer-empty {
  padding: 40px 0;
  text-align: center;
  color: var(--text-faint);
  font-size: 13px;
}
.todo-cols {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}
.col-title {
  font-size: 12.5px;
  font-weight: 600;
  margin-bottom: 10px;
}
.col-title.err { color: var(--danger, #f43f5e); }
.col-title.warn { color: var(--warning, #f59e0b); }
.col-title.info { color: var(--text-dim); }
.todo-card {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 8px;
  background: var(--surface);
}
.t-head {
  font-size: 12.5px;
  line-height: 1.5;
}
.t-tag {
  display: inline-block;
  margin-right: 6px;
  padding: 0 5px;
  border: 1px solid var(--border-strong);
  border-radius: 3px;
  font-size: 10px;
  color: var(--text-dim);
  vertical-align: 1px;
}
.t-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 6px;
}
.t-time {
  font-size: 11px;
  color: var(--text-faint);
}
.t-link {
  font-size: 11.5px;
  color: var(--brand);
  text-decoration: none;
}
.t-link:hover {
  text-decoration: underline;
}
.col-empty {
  font-size: 12px;
  color: var(--text-faint);
  padding: 8px 0;
}
.all-runs-link {
  color: var(--brand);
  font-size: 13px;
  text-decoration: none;
}
</style>
