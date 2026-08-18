<script setup lang="ts">
// 应用详情 - 服务治理 tab：该应用注册的服务 + 实例发现 + 路由 + 熔断（只读）。
// 复用 GET /api/services?appId=（按应用过滤）+ GET /api/services/{id}（聚合实例）。
// 注册/注销/路由/熔断的写操作归 /platform/governance；本 tab 仅应用内可见性。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

const props = defineProps<{ appId: string }>()
const router = useRouter()
const envStore = useEnvStore()

interface Service {
  id: string; name: string; appId?: string; envId: string
  protocol: string; port: number; desc?: string; updatedAt: string
}
interface Instance {
  id: string; serviceId: string; addr: string; status: string
  laneId: string; updatedAt: string
}
interface Route {
  id: string; name: string; path: string; serviceId: string
  methods: string[]; stripPath: boolean; enabled: boolean; host?: string
}
interface WindowStats { requests: number; failures: number; slowCalls: number; rate: number }
interface Breaker {
  id: string; name: string; serviceId: string; strategy: string
  threshold: number; windowSecs: number; enabled: boolean
  state: string; stats: WindowStats
}

const services = ref<Service[]>([])
// 实例按 serviceID 缓存（懒加载：仅展开某服务时拉取）。
const instancesBySvc = ref<Record<string, Instance[]>>({})
const expanded = ref<Record<string, boolean>>({})
const routes = ref<Route[]>([])
const breakers = ref<Breaker[]>([])
const loading = ref(false)

const stateMeta: Record<string, { label: string; type: TagType }> = {
  closed: { label: '放行', type: 'success' },
  open: { label: '熔断', type: 'danger' },
  'half-open': { label: '半开', type: 'warning' },
}

// 该应用服务 ID 集合（用于过滤归属本应用的路由/熔断）。
const svcIds = computed(() => new Set(services.value.map((s) => s.id)))
const appRoutes = computed(() => routes.value.filter((r) => svcIds.value.has(r.serviceId)))
const appBreakers = computed(() => breakers.value.filter((b) => svcIds.value.has(b.serviceId)))

async function load() {
  loading.value = true
  try {
    const q = envStore.currentEnvId
      ? `?appId=${props.appId}&envId=${envStore.currentEnvId}`
      : `?appId=${props.appId}`
    const [svcResp, routeResp, breakerResp] = await Promise.all([
      fetchAuth(`/api/services${q}`),
      fetchAuth('/api/routes'),
      fetchAuth('/api/breakers'),
    ])
    if (svcResp.ok) services.value = (await svcResp.json()).data ?? []
    else ElMessage.error(`加载服务列表失败：HTTP ${svcResp.status}`)
    if (routeResp.ok) routes.value = (await routeResp.json()).data ?? []
    if (breakerResp.ok) breakers.value = (await breakerResp.json()).data ?? []
    instancesBySvc.value = {}
    expanded.value = {}
  } catch (e) {
    ElMessage.error('加载服务治理数据失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

// 懒加载某服务的实例（首次展开时）。GET /api/services/{id} 返 {service,instances}。
// 用 EP @expand-change(row, expandedRows) 事件：展开状态以 EP 内部为准，避免自翻转对冲。
async function onExpand(row: Service, expandedRows: Service[]) {
  const isOpen = expandedRows.some((r) => r.id === row.id)
  expanded.value[row.id] = isOpen
  if (isOpen && !instancesBySvc.value[row.id]) {
    try {
      const resp = await fetchAuth(`/api/services/${row.id}`)
      if (resp.ok) {
        const json = await resp.json()
        const payload = json && typeof json === 'object' && 'service' in json ? json : (json?.data ?? {})
        instancesBySvc.value[row.id] = payload.instances ?? []
      } else {
        instancesBySvc.value[row.id] = []
      }
    } catch {
      instancesBySvc.value[row.id] = []
      ElMessage.error(`加载服务「${row.name}」实例失败`)
    }
  }
}

function goRegistry() {
  router.push('/platform/governance')
}

onMounted(load)
watch(() => props.appId, load)
watch(() => envStore.currentEnvId, load)
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">服务治理</span>
      <span class="tab-hint">该应用注册的服务 · 实例 · 路由 · 熔断（只读）</span>
      <el-button text type="primary" size="small" style="margin-left: auto" @click="goRegistry">
        在服务治理中管理 →
      </el-button>
    </div>

    <div v-if="!services.length && !loading" class="empty">
      <p>该应用尚未注册服务</p>
      <el-button size="small" @click="goRegistry">去服务治理注册</el-button>
    </div>

    <el-table v-else :data="services" v-loading="loading" size="small" row-key="id" @expand-change="onExpand">
      <el-table-column type="expand">
        <template #default="{ row }">
          <div class="inst-sub">
            <span v-if="!expanded[row.id]" class="faint">点击行首展开加载实例</span>
            <el-table v-else :data="instancesBySvc[row.id] || []" size="small" empty-text="该服务暂无实例">
              <el-table-column label="地址" min-width="200">
                <template #default="{ row: ins }"><span class="mono">{{ ins.addr }}</span></template>
              </el-table-column>
              <el-table-column label="状态" width="100">
                <template #default="{ row: ins }">
                  <el-tag :type="ins.status === 'healthy' ? 'success' : 'danger'" size="small">{{ ins.status }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="laneId" label="泳道" width="100" />
            </el-table>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="服务名" min-width="180">
        <template #default="{ row }">
          <span class="mono">{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="协议" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.protocol === 'grpc' ? 'warning' : 'info'">{{ row.protocol.toUpperCase() }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="port" label="端口" width="70" />
      <el-table-column prop="desc" label="描述" min-width="140" show-overflow-tooltip />
    </el-table>

    <!-- 路由 -->
    <section v-if="services.length" class="sub-block">
      <div class="sub-title">API 网关路由（{{ appRoutes.length }}）</div>
      <el-table :data="appRoutes" size="small" empty-text="该应用服务暂无路由">
        <el-table-column label="名称" min-width="120">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="路径" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.path }}</span></template>
        </el-table-column>
        <el-table-column label="对外域名" min-width="180">
          <template #default="{ row }">
            <span v-if="row.host" class="mono">{{ row.host }}</span>
            <span v-else class="faint">不限</span>
          </template>
        </el-table-column>
        <el-table-column label="方法" width="140">
          <template #default="{ row }">
            <el-tag v-for="m in row.methods" :key="m" size="small" style="margin-right:4px">{{ m }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 熔断 -->
    <section v-if="services.length" class="sub-block">
      <div class="sub-title">熔断器（{{ appBreakers.length }}）</div>
      <el-table :data="appBreakers" size="small" empty-text="该应用服务暂无熔断规则">
        <el-table-column label="名称" min-width="120">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="策略" width="90">
          <template #default="{ row }">{{ row.strategy === 'slow_call' ? '慢调用率' : '错误率' }}</template>
        </el-table-column>
        <el-table-column label="阈值" width="80">
          <template #default="{ row }">≥{{ row.threshold }}%</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.enabled" :type="(stateMeta[row.state]?.type) || 'info'" size="small">
              {{ stateMeta[row.state]?.label || row.state }}
            </el-tag>
            <el-tag v-else type="info" size="small">停用</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.tab-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.tab-title { font-size: 14px; font-weight: 600; }
.tab-hint { font-size: 12px; color: var(--text-faint); }
.empty { padding: 40px 0; text-align: center; color: var(--text-faint); font-size: 13px; }
.empty p { margin: 0 0 10px; }
.inst-sub { padding: 8px 16px 8px 40px; }
.faint { font-size: 12px; color: var(--text-faint); }
.sub-block { margin-top: 20px; }
.sub-title { font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px; }
.mono { font-family: var(--font-mono); }
</style>
