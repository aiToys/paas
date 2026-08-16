<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import NotificationBell from '@/components/NotificationBell.vue'
import { useEnvStore } from '@/stores/env'
import { useSessionStore } from '@/stores/session'
import { useTheme } from '@/composables/useTheme'
import { useNavState, type NavGroup } from '@/composables/useNavState'

const route = useRoute()
const router = useRouter()
const collapsed = ref(false)
const envStore = useEnvStore()
const { theme, toggle } = useTheme()
const searchQuery = ref('')

function onSearch() {
  const q = searchQuery.value.trim()
  router.push({ path: '/applications', query: q ? { q } : {} })
}

// 生产会话超时检查定时器
let prodTimer: number | undefined
onMounted(async () => {
  await envStore.loadEnvs()
  // 从 URL ?env=<id> 恢复环境上下文（分享链接保留环境，一次）。
  const q = route.query.env as string
  if (q) {
    const found = envStore.envs.find((e) => e.id === q)
    if (found) await envStore.switchEnv(found)
  }
  prodTimer = window.setInterval(() => {
    if (envStore.checkProdTimeout()) {
      ElMessageBox.alert('生产会话已超时，已自动切回。如需继续操作生产请重新进入。', '生产超时', { type: 'info' })
    }
  }, 5000)
})
onUnmounted(() => { if (prodTimer) window.clearInterval(prodTimer) })

// envStore.currentEnvId → URL：环境切换写 ?env=<id>（分享链接带环境上下文）。
// 用 router.replace 避免历史栈膨胀；切回全部环境时省略 query（URL 干净）。
watch(
  () => envStore.currentEnvId,
  (id) => {
    const cur = (route.query.env as string) ?? ''
    if ((id || '') !== cur) {
      router.replace({ query: { ...route.query, env: id || undefined } })
    }
  },
)

const session = useSessionStore()
// 当前身份视角（来自会话 profile）：用户名 + 首字母。
const identityLabel = computed(() => session.profile?.username ?? '未登录')
const identityInitial = computed(() => (session.profile?.username ?? 'U').charAt(0).toUpperCase())

// 环境选择器
const envLabel = computed(() => envStore.currentEnv?.name ?? '全部环境')
const prodRemaining = computed(() => {
  const s = envStore.prodRemainingSec
  if (s <= 0) return ''
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `${m}:${sec.toString().padStart(2, '0')}`
})

async function onPickEnv(cmd: string | number | object) {
  const id = String(cmd)
  if (id === '__all') {
    await envStore.switchEnv(null)
    return
  }
  const env = envStore.envs.find((e) => e.id === id)
  if (env) await envStore.switchEnv(env)
}

// 演示账号快切（dev/demo）：预设账号一键登录；__logout 退出。
async function onPickAccount(cmd: string | number | object) {
  const c = String(cmd)
  if (c === '__logout') {
    await session.logout()
    router.push('/login')
    return
  }
  const d = session.DEMO_ACCOUNTS.find((a) => a.username === c)
  if (!d) return
  try {
    await session.login(d.username, d.password)
    ElMessage.success(`已切换到 ${d.label}`)
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '切换失败')
  }
}

interface NavChild {
  label: string
  icon: string
  to: string
}
interface NavItem {
  label: string
  icon: string
  to?: string
  section?: 'main' | 'resources' // 分组标签归属（决定上方 section label）
  primary?: boolean              // 「应用」视觉强化
  group?: NavGroup               // 可折叠资源组的 key
  children?: NavChild[]
}

const nav: NavItem[] = [
  // —— 主操作：高频开发动作 ——
  { label: '应用', icon: 'deploy', to: '/applications', section: 'main', primary: true },
  { label: 'DevOps', icon: 'pipeline', to: '/devops', section: 'main' },
  { label: 'Playground', icon: 'playground', to: '/playground', section: 'main' },
  // AI 服务：模型是平台共享的「能力调用」（非租户私有存储资源），与 Playground 配套属高频。
  { label: 'AI 服务', icon: 'market', to: '/resources/models', section: 'main' },
  // —— 资源与能力：创建后少动，默认折叠，各自记忆展开态 ——
  {
    label: '资源中心', icon: 'database', section: 'resources', group: 'resources',
    children: [
      { label: '数据库', icon: 'database', to: '/resources/db' },
      { label: '缓存', icon: 'zap', to: '/resources/cache' },
      { label: '消息队列', icon: 'message', to: '/resources/mq' },
      { label: '对象存储', icon: 'storage', to: '/resources/storage' },
      { label: '向量数据库', icon: 'layers', to: '/resources/vector' },
      { label: '搜索引擎', icon: 'search', to: '/resources/search' },
      { label: '知识库', icon: 'book', to: '/resources/knowledgebase' },
    ],
  },
  {
    label: '工作负载', icon: 'server', section: 'resources', group: 'workloads',
    children: [
      { label: '服务', icon: 'server', to: '/workloads/services' },
      { label: '任务', icon: 'job', to: '/workloads/jobs' },
      { label: '定时', icon: 'clock', to: '/workloads/cronjobs' },
    ],
  },
  {
    label: 'AI 编排', icon: 'pipeline', section: 'resources', group: 'ai',
    children: [
      { label: '工具', icon: 'tool', to: '/ai/tools' },
      { label: '提示词', icon: 'prompt', to: '/ai/prompts' },
      { label: 'Agent', icon: 'bot', to: '/ai/agents' },
    ],
  },
  {
    label: '平台能力', icon: 'service', section: 'resources', group: 'platform',
    children: [
      { label: '服务治理', icon: 'service', to: '/platform/governance' },
      { label: '配置中心', icon: 'sliders', to: '/platform/config-center' },
      { label: '可观测', icon: 'activity', to: '/platform/observability' },
      { label: '安全', icon: 'shield', to: '/platform/security' },
    ],
  },
  // —— 环境：物理隔离单元（管理面 + 跨环境总览）——
  { label: '环境', icon: 'env', to: '/environments' },
]

const { isOpen: navGroupOpen, toggle: toggleNavGroup } = useNavState()

// section 首项索引：模板据此在对应项前插分组 label（主操作 / 资源与能力）。
const sectionStarts = computed(() => {
  const map: Record<string, number> = {}
  nav.forEach((item, i) => {
    if (item.section && !(item.section in map)) map[item.section] = i
  })
  return map
})
const sectionLabel: Record<string, string> = { main: '主操作', resources: '资源与能力' }

// 直链/刷新进入资源组子路由时自动展开对应父组（否则父组折叠，子项 active 高亮不可见）。
watch(
  () => route.path,
  (path) => {
    for (const item of nav) {
      if (
        item.group &&
        item.children?.some((c) => path === c.to || path.startsWith(c.to + '/')) &&
        !navGroupOpen(item.group)
      ) {
        toggleNavGroup(item.group)
      }
    }
  },
  { immediate: true },
)

const settings = [
  { label: 'API 密钥', icon: 'key', to: '/settings/api-keys' },
  { label: '配额与账单', icon: 'zap', to: '/settings/billing' },
]

const pageTitle = computed(() => (route.meta.title as string) || '控制台')

function isActive(to: string) {
  return route.path === to || route.path.startsWith(to + '/')
}
</script>

<template>
  <div class="app" :class="{ collapsed, 'env-prod': envStore.isProd }">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">P</div>
        <div v-if="!collapsed" class="brand-text">
          <div class="brand-name">paas</div>
          <div class="brand-sub">统一控制台</div>
        </div>
      </div>

      <nav class="nav">
        <template v-for="(item, i) in nav" :key="item.label">
          <!-- section 分组 label（主操作 / 资源与能力） -->
          <div
            v-if="item.section && sectionStarts[item.section] === i && !collapsed"
            class="nav-section-label"
          >
            {{ sectionLabel[item.section] }}
          </div>

          <!-- 普通导航项（含应用强化） -->
          <RouterLink
            v-if="item.to"
            :to="item.to"
            class="nav-item"
            :class="{ active: isActive(item.to!), primary: item.primary }"
          >
            <span class="nav-bar" />
            <Icon :name="item.icon" :size="19" class="nav-icon" />
            <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
          </RouterLink>

          <!-- 可折叠资源组 -->
          <div v-else class="nav-group" :class="{ open: item.group && navGroupOpen(item.group) }">
            <div class="nav-item group-title" @click="item.group && !collapsed && toggleNavGroup(item.group)">
              <Icon :name="item.icon" :size="19" class="nav-icon" />
              <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
              <Icon
                v-if="!collapsed && item.group"
                name="chevron"
                :size="14"
                class="group-chev"
              />
            </div>
            <div v-if="!collapsed" v-show="navGroupOpen(item.group!)" class="sub-nav">
              <RouterLink
                v-for="c in item.children"
                :key="c.to"
                :to="c.to"
                class="sub-item"
                :class="{ active: isActive(c.to) }"
              >
                <Icon :name="c.icon" :size="15" />
                <span>{{ c.label }}</span>
              </RouterLink>
            </div>
          </div>
        </template>
      </nav>

      <div class="nav-section-label" v-if="!collapsed">设置</div>
      <div class="nav settings-nav">
        <RouterLink
          v-for="s in settings"
          :key="s.to"
          :to="s.to"
          class="nav-item"
          :class="{ active: isActive(s.to) }"
        >
          <Icon :name="s.icon" :size="18" class="nav-icon" />
          <span v-if="!collapsed" class="nav-label">{{ s.label }}</span>
        </RouterLink>
      </div>

      <button class="collapse-btn" @click="collapsed = !collapsed">
        <Icon name="collapse" :size="18" />
        <span v-if="!collapsed">收起</span>
      </button>
    </aside>

    <div class="main">
      <header class="topbar">
        <div class="title-block">
          <h1 class="page-title">{{ pageTitle }}</h1>
        </div>
        <div class="topbar-right">
          <NotificationBell />
          <el-dropdown trigger="click" @command="onPickEnv">
            <div class="env-chip" :class="{ prod: envStore.isProd }">
              <Icon :name="envStore.isProd ? 'shield' : 'server'" :size="14" />
              <span v-if="!collapsed">{{ envLabel }}</span>
              <span v-if="envStore.isProd && prodRemaining" class="prod-timer mono">{{ prodRemaining }}</span>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="__all" :disabled="!envStore.currentEnv">全部环境</el-dropdown-item>
                <el-dropdown-item
                  v-for="e in envStore.envs"
                  :key="e.id"
                  :command="e.id"
                  :disabled="envStore.currentEnv?.id === e.id"
                >
                  <div class="key-row">
                    <span>{{ e.name }}</span>
                    <span class="key-role" :class="{ prodtag: e.type === 'prod' }">{{ e.type === 'prod' ? '生产' : '测试' }}</span>
                  </div>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <div class="search">
            <Icon name="search" :size="16" />
            <input
              v-model="searchQuery"
              class="search-input"
              placeholder="搜索应用…"
              @keydown.enter="onSearch"
            />
            <kbd>⏎</kbd>
          </div>
          <button
            class="theme-toggle"
            :title="theme === 'dark' ? '切换到亮色' : '切换到暗色'"
            :aria-label="theme === 'dark' ? '切换到亮色' : '切换到暗色'"
            @click="toggle"
          >
            <!-- 暗色下显示太阳（点击切亮）；亮色下显示月亮（点击切暗） -->
            <svg v-if="theme === 'dark'" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <circle cx="12" cy="12" r="4" />
              <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />
            </svg>
            <svg v-else width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
            </svg>
          </button>
          <el-dropdown trigger="click" @command="onPickAccount">
            <div class="tenant-chip">
              <div class="t-avatar">{{ identityInitial }}</div>
              <span v-if="!collapsed">{{ identityLabel }}</span>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item
                  v-for="d in session.DEMO_ACCOUNTS"
                  :key="d.username"
                  :command="d.username"
                  :disabled="d.username === session.profile?.username"
                >
                  <div class="key-row">
                    <span>{{ d.label }}</span>
                    <span class="key-role">{{ d.role }}</span>
                  </div>
                </el-dropdown-item>
                <el-dropdown-item command="__logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <div class="user-avatar">{{ identityInitial }}</div>
        </div>
      </header>

      <div v-if="envStore.isProd" class="prod-banner">
        <Icon name="shield" :size="14" />
        <span>⚠️ 您正在生产环境，操作请谨慎</span>
        <span v-if="prodRemaining" class="prod-banner-timer mono">会话剩余 {{ prodRemaining }}</span>
      </div>

      <main class="content">
        <RouterView v-slot="{ Component }">
          <transition name="route" mode="out-in">
            <component :is="Component" />
          </transition>
        </RouterView>
      </main>
    </div>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

.sidebar {
  width: 248px;
  flex-shrink: 0;
  background: var(--sidebar-bg);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  transition: width 0.2s ease;
  overflow-y: auto;
}
.app.collapsed .sidebar {
  width: 72px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 20px 20px 18px;
}
.brand-mark {
  width: 36px;
  height: 36px;
  flex-shrink: 0;
  border-radius: 10px;
  background: linear-gradient(135deg, #6366f1, #8b5cf6);
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 18px;
  color: #fff;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.brand-name {
  font-weight: 700;
  font-size: 16px;
  letter-spacing: -0.01em;
}
.brand-sub {
  font-size: 11px;
  color: var(--text-faint);
  letter-spacing: 0.04em;
}

.nav {
  padding: 6px 12px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.nav.settings-nav {
  padding-top: 2px;
}
.nav-section-label {
  padding: 14px 20px 4px;
  font-size: 11px;
  color: var(--text-faint);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.nav-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border-radius: var(--radius);
  color: var(--text-dim);
  text-decoration: none;
  font-size: 13.5px;
  font-weight: 500;
  transition: background 0.12s, color 0.12s;
  cursor: pointer;
}
.nav-item:hover:not(.active) {
  background: var(--surface);
  color: var(--text);
}
.nav-item.active {
  background: var(--brand-soft);
  color: var(--brand);
}
.nav-bar {
  position: absolute;
  left: -12px;
  top: 50%;
  transform: translateY(-50%) scaleY(0);
  width: 3px;
  height: 20px;
  border-radius: 0 3px 3px 0;
  background: var(--brand);
  transition: transform 0.18s;
}
.nav-item.active .nav-bar {
  transform: translateY(-50%) scaleY(1);
}
.nav-icon {
  flex-shrink: 0;
}
.app.collapsed .nav-item {
  justify-content: center;
  padding: 11px;
}

.sub-nav {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding-left: 30px;
}
.sub-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 7px 10px;
  border-radius: 7px;
  color: var(--text-faint);
  text-decoration: none;
  font-size: 13px;
  transition: all 0.12s;
}
.sub-item:hover {
  background: var(--surface);
  color: var(--text);
}
.sub-item.active {
  color: var(--brand);
}

/* 可折叠资源组标题：可点击 + chevron 展开/收起标记 */
.nav-item.group-title {
  cursor: pointer;
  user-select: none;
  font-weight: 500;
}
.nav-item.group-title:hover {
  background: var(--surface);
  color: var(--text-dim);
}
.group-chev {
  margin-left: auto;
  color: var(--text-faint);
  transition: transform 0.18s;
  /* Icon 的 chevron 默认朝下（points 6 9 → 12 15 → 18 9）：
     未展开态向左旋转 90° 朝右；展开态恢复 0° 朝下。 */
  transform: rotate(-90deg);
}
.nav-group.open .group-chev {
  transform: rotate(0);
}

/* 「应用」强化：brand 色左条常显 + brand 色文字 + 字重 600（主线锚点） */
.nav-item.primary {
  color: var(--brand);
  font-weight: 600;
}
.nav-item.primary .nav-bar {
  transform: translateY(-50%) scaleY(1);
  background: var(--brand);
}
.nav-item.primary:hover {
  background: var(--brand-soft);
  color: var(--brand);
}
.nav-item.primary.active {
  background: var(--brand-soft);
  color: var(--brand);
}

.collapse-btn {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 8px 12px 14px;
  padding: 8px 12px;
  border: none;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-faint);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: background 0.12s, color 0.12s;
}
.collapse-btn:hover {
  background: var(--surface);
  color: var(--text-dim);
}
.app.collapsed .collapse-btn {
  justify-content: center;
}
.app.collapsed .collapse-btn :deep(svg) {
  transform: rotate(180deg);
}
.app.collapsed .sub-nav,
.app.collapsed .nav-section-label {
  display: none;
}

.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 18px 32px;
  border-bottom: 1px solid var(--border);
  background: var(--topbar-bg);
  backdrop-filter: blur(8px);
}
.page-title {
  margin: 0;
  font-size: 20px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.topbar-right {
  display: flex;
  align-items: center;
  gap: 12px;
}
.search {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 12px;
  border-radius: var(--radius);
  background: var(--surface);
  border: 1px solid var(--border);
  color: var(--text-faint);
  font-size: 13px;
  min-width: 260px;
  transition: border-color 0.15s;
}
.search:focus-within {
  border-color: var(--brand);
}
.search-input {
  flex: 1;
  background: transparent;
  border: none;
  outline: none;
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
}
.search-input::placeholder {
  color: var(--text-faint);
}
.search kbd {
  margin-left: auto;
  padding: 2px 6px;
  border-radius: 4px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  font-family: var(--font-mono);
  font-size: 11px;
}
.theme-toggle {
  display: grid;
  place-items: center;
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.15s;
}
.theme-toggle:hover {
  border-color: var(--border-strong);
  color: var(--text);
}

.tenant-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px 5px 5px;
  border-radius: 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  cursor: pointer;
  font-size: 12.5px;
  color: var(--text-dim);
}
.t-avatar {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: linear-gradient(135deg, #f59e0b, #f43f5e);
  display: grid;
  place-items: center;
  font-size: 10px;
  font-weight: 600;
  color: #fff;
}
.user-avatar {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: linear-gradient(135deg, #10b981, #06b6d4);
  display: grid;
  place-items: center;
  font-size: 13px;
  font-weight: 600;
  color: #04130f;
  cursor: pointer;
}

.key-row {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
}
.key-role {
  margin-left: auto;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--surface-2, #1e2433);
  color: var(--text-faint, #8b93a7);
  font-size: 11px;
}

.content {
  flex: 1;
  overflow-y: auto;
  padding: 28px 32px;
}

/* —— 响应式：窄屏自动图标化侧栏 —— */
@media (max-width: 960px) {
  .sidebar {
    width: 64px;
  }
  .brand-text,
  .nav-label,
  .sub-nav,
  .nav-section-label,
  .collapse-btn span,
  .search,
  .tenant-chip span {
    display: none;
  }
  .nav-item,
  .collapse-btn {
    justify-content: center;
  }
  .content {
    padding: 20px 16px;
  }
  .topbar {
    padding: 14px 16px;
  }
}

/* -- 生产安全防护：环境选择器 + 视觉强隔离 -- */
.env-chip {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  color: var(--text-dim);
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.env-chip:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.env-chip.prod {
  background: rgba(244, 63, 94, 0.12);
  border-color: #f43f5e;
  color: #f43f5e;
  font-weight: 600;
}
.prod-timer {
  font-size: 11px;
  opacity: 0.8;
}
.key-role.prodtag {
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}

/* 生产环境整页强隔离：红色边框 + 顶栏红条 */
.app.env-prod {
  box-shadow: inset 0 0 0 3px #f43f5e;
}
.app.env-prod .topbar {
  border-bottom: 2px solid #f43f5e;
  box-shadow: 0 2px 12px rgba(244, 63, 94, 0.25);
}
.prod-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 20px;
  background: rgba(244, 63, 94, 0.1);
  color: #f43f5e;
  font-size: 13px;
  font-weight: 500;
  border-bottom: 1px solid rgba(244, 63, 94, 0.3);
}
.prod-banner-timer {
  margin-left: auto;
  font-size: 12px;
  opacity: 0.85;
}
</style>
