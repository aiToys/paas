<template>
  <div class="home">
    <!-- 公告横幅 -->
    <NoticeBanner />

    <!-- 欢迎卡 -->
    <WelcomeCard
      :username="displayName"
      :is-admin="permissionStore.isSuperAdmin"
      :role-label="roleLabel"
      @action-click="onWelcomeAction"
    />

    <!-- 管理员视角：统计 + 图表 -->
    <template v-if="permissionStore.isSuperAdmin">
      <StatsCards
        :items="statItems"
        :error="statsError"
      />
      <div
        v-if="statsLoadedAt && !statsError"
        class="freshness-note"
      >统计截至 {{ statsLoadedAt }}（打开页面时快照，不自动刷新）</div>

      <!-- 需要关注：失败运行/触发告警/异常负载聚合，点击直达（spec §6 / 复核 F06） -->
      <AttentionPanel />

      <ChartCards
        :trend-data="charts.trend"
        :distribution-data="charts.distribution"
        :range="range"
        :loading="chartsLoading"
        :error="chartsError"
        @range-change="onRangeChange"
      />
    </template>

    <!-- 双列：动态 + 快捷入口 -->
    <div class="bottom-grid">
      <ActivityTimeline
        :activities="activities"
        :error="activitiesError"
      />
      <QuickActions
        :actions="quickActions"
        @select="onQuickAction"
      />
    </div>
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted, type Component } from 'vue'
import { useRouter } from 'vue-router'
import {
  UserFilled,
  OfficeBuilding,
  MagicStick,
  ChatDotRound,
  VideoPlay,
  Coin
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { t } from '@/lib/i18n'
import { useUserStore } from '@/app/stores/user'
import { usePermissionStore } from '@/app/stores/permission'
import {
  fetchDashboardStats,
  fetchDashboardCharts,
  fetchDashboardActivities,
  type DashboardStats,
  type DashboardCharts,
  type Activity,
  type ChartRange
} from '../api'
import NoticeBanner from '../components/NoticeBanner.vue'
import WelcomeCard from '../components/WelcomeCard.vue'
import StatsCards from '../components/StatsCards.vue'
import AttentionPanel from '../components/AttentionPanel.vue'
import ChartCards from '../components/ChartCards.vue'
import ActivityTimeline from '../components/ActivityTimeline.vue'
import QuickActions from '../components/QuickActions.vue'

const router = useRouter()
const userStore = useUserStore()
const permissionStore = usePermissionStore()

const displayName = computed(
  () => userStore.profile?.nickname || userStore.profile?.username || t('dashboard.defaultUser')
)

const roleLabel = computed(() => {
  if (permissionStore.isSuperAdmin) return t('dashboard.roleSuperAdmin')
  return t('dashboard.roleNormal')
})

// 统计数据（每源独立结算：一源失败不拖垮其余，失败 ≠ 0/空——R4 审计 H-3）
const stats = ref<DashboardStats | null>(null)
const statsError = ref('')
const chartsError = ref('')
const activitiesError = ref('')
const statsLoadedAt = ref('')
const statItems = computed(() => {
  if (!stats.value) return []
  const s = stats.value
  return [
    { key: 'users', label: t('dashboard.stat.users'), value: s.users.value, trendPct: s.users.trendPct },
    { key: 'orders', label: t('dashboard.stat.orders'), value: s.orders.value, trendPct: s.orders.trendPct },
    { key: 'revenue', label: t('dashboard.stat.revenue'), value: s.revenue.value, trendPct: s.revenue.trendPct },
    { key: 'active', label: t('dashboard.stat.active'), value: s.active.value, trendPct: s.active.trendPct }
  ]
})

// 图表数据
const range = ref<ChartRange>('7d')
const charts = ref<DashboardCharts>({ trend: [], distribution: [] })
const chartsLoading = ref(false)

const loadCharts = async () => {
  chartsLoading.value = true
  chartsError.value = ''
  try {
    charts.value = await fetchDashboardCharts(range.value)
  } catch (e) {
    chartsError.value = '图表加载失败，请稍后重试'
    console.warn('[dashboard] charts 加载失败:', e)
  } finally {
    chartsLoading.value = false
  }
}

const onRangeChange = (r: ChartRange) => {
  range.value = r
  loadCharts()
}

// 动态
const activities = ref<Activity[]>([])

// 快捷入口（含权限过滤）
interface QAction {
  key: string
  label: string
  icon: Component
  path?: string
  perm?: string[]
}
const quickActions = computed<QAction[]>(() => {
  // 指向 2026-10-19 IA 重构后的六域高频入口（menus.go 真源；/system/role 等旧路径
  // 由后端兼容 redirect 兜底，快捷入口直接引用新路径）。
  // F7：按菜单注册表过滤——无对应菜单权限的入口不显示（点了 403 的假入口不如不给）。
  const actions: QAction[] = [
    { key: 'tenant', label: '租户管理', icon: OfficeBuilding, path: '/system/tenant' },
    { key: 'user', label: '用户与授权', icon: UserFilled, path: '/system/user' },
    { key: 'model', label: '模型与通道', icon: MagicStick, path: '/model' },
    { key: 'ai', label: 'AI 资产', icon: ChatDotRound, path: '/resources/ai' },
    { key: 'delivery', label: '交付记录', icon: VideoPlay, path: '/resources/pipelineruns' },
    { key: 'dataservices', label: '数据服务', icon: Coin, path: '/resources/dataservices' }
  ]
  const registered = new Set(router.getRoutes().map((r) => r.path))
  return actions.filter((a) => !a.path || registered.has(a.path))
})

const onQuickAction = (action: QAction) => {
  if (action.path) {
    router.push(action.path)
  } else {
    ElMessage.info(t('dashboard.message.inDevelopment'))
  }
}

const onWelcomeAction = (action: string) => {
  if (action === 'profile') router.push('/profile')
  else if (action === 'docs') {
    ElMessage.info(t('dashboard.message.docsBuilding'))
  }
}

// errText 把 HttpError 归类为可读文案（403 = 无权限；其余 = 加载失败），不回显原始错误。
const errText = (e: unknown) => {
  const msg = e instanceof Error ? e.message : ''
  return msg.includes('403') || msg.includes('401')
    ? '无访问权限（需要平台管理员或相应平台角色）'
    : '加载失败，请稍后重试'
}

onMounted(async () => {
  // 普通用户不需要加载统计和图表；动态失败显示错误态而非空态（不再静默吞成 []）
  if (!permissionStore.isSuperAdmin) {
    try {
      activities.value = await fetchDashboardActivities()
    } catch (e) {
      activitiesError.value = errText(e)
    }
    return
  }
  // allSettled 三源独立结算：stats 挂了 charts/activities 照常显示
  const [s, c, a] = await Promise.allSettled([
    fetchDashboardStats(),
    fetchDashboardCharts(range.value),
    fetchDashboardActivities()
  ])
  if (s.status === 'fulfilled') {
    stats.value = s.value
    statsError.value = ''
    statsLoadedAt.value = new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  } else {
    statsError.value = errText(s.reason)
  }
  if (c.status === 'fulfilled') {
    charts.value = c.value
    chartsError.value = ''
  } else {
    chartsError.value = errText(c.reason)
  }
  if (a.status === 'fulfilled') {
    activities.value = a.value
    activitiesError.value = ''
  } else {
    activitiesError.value = errText(a.reason)
  }
})
</script>

<style scoped>
.home {
  padding: 16px;
}

.freshness-note {
  margin: -8px 0 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.bottom-grid {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 16px;
}

@media (max-width: 992px) {
  .bottom-grid {
    grid-template-columns: 1fr;
  }
}
</style>
