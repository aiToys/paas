<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { auth, setApiKey, currentPreset, PRESET_KEYS } from '@/api'

const route = useRoute()
const collapsed = ref(false)

// 当前身份视角（来自 API Key）：租户标签 + 头像首字母；自定义 Key 退化为通用展示。
const identityLabel = computed(() => currentPreset()?.label ?? '自定义 Key')
const identityInitial = computed(() => {
  const p = currentPreset()
  if (p) return p.tenant.replace('t-', '').charAt(0).toUpperCase()
  return 'U'
})

async function onPickKey(cmd: string | number | object) {
  const key = String(cmd)
  if (key === '__custom') {
    try {
      const { value } = await ElMessageBox.prompt('输入 API Key（绑定租户与角色）', '切换 API Key', {
        confirmButtonText: '切换',
        cancelButtonText: '取消',
        inputPlaceholder: 'sk-...',
      })
      if (value.trim()) setApiKey(value.trim())
    } catch {
      // 用户取消
    }
    return
  }
  setApiKey(key)
}

interface NavItem {
  label: string
  icon: string
  to?: string
  soon?: boolean
  children?: { label: string; icon: string; to: string; soon?: boolean }[]
}

const nav: NavItem[] = [
  { label: '应用', icon: 'deploy', to: '/applications' },
  { label: '环境', icon: 'shield', to: '/environments' },
  {
    label: '资源中心',
    icon: 'database',
    children: [
      { label: '模型推理', icon: 'market', to: '/resources/models' },
      { label: '数据库', icon: 'database', to: '/resources/db', soon: true },
      { label: '缓存', icon: 'zap', to: '/resources/cache', soon: true },
      { label: '消息队列', icon: 'message', to: '/resources/mq', soon: true },
      { label: '对象存储', icon: 'storage', to: '/resources/storage', soon: true },
      { label: '向量数据库', icon: 'layers', to: '/resources/vector', soon: true },
      { label: '搜索引擎', icon: 'search', to: '/resources/search', soon: true },
    ],
  },
  {
    label: '工作负载',
    icon: 'server',
    children: [
      { label: '服务', icon: 'server', to: '/workloads/services', soon: true },
      { label: '任务', icon: 'job', to: '/workloads/jobs', soon: true },
      { label: '定时', icon: 'clock', to: '/workloads/cronjobs', soon: true },
    ],
  },
  {
    label: '平台能力',
    icon: 'service',
    children: [
      { label: '服务治理', icon: 'service', to: '/platform/governance', soon: true },
      { label: '可观测', icon: 'activity', to: '/platform/observability', soon: true },
      { label: '安全', icon: 'shield', to: '/platform/security', soon: true },
    ],
  },
  { label: 'DevOps', icon: 'pipeline', to: '/devops', soon: true },
  { label: 'Playground', icon: 'playground', to: '/playground' },
]

const settings = [
  { label: 'API 密钥', icon: 'key', to: '/settings/api-keys' },
  { label: '用量', icon: 'usage', to: '/settings/usage' },
]

const pageTitle = computed(() => (route.meta.title as string) || '控制台')

function isActive(to: string) {
  if (to === '/coming-soon') return false
  return route.path === to || route.path.startsWith(to + '/')
}
</script>

<template>
  <div class="app" :class="{ collapsed }">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">P</div>
        <div v-if="!collapsed" class="brand-text">
          <div class="brand-name">paas</div>
          <div class="brand-sub">统一控制台</div>
        </div>
      </div>

      <nav class="nav">
        <template v-for="item in nav" :key="item.label">
          <RouterLink v-if="item.to" :to="item.to" class="nav-item" :class="{ active: isActive(item.to!) }">
            <span class="nav-bar" />
            <Icon :name="item.icon" :size="19" class="nav-icon" />
            <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
            <span v-if="!collapsed && item.soon" class="soon-tag">即将</span>
          </RouterLink>

          <div v-else class="nav-group">
            <div class="nav-item static">
              <Icon :name="item.icon" :size="19" class="nav-icon" />
              <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
            </div>
            <div v-if="!collapsed" class="sub-nav">
              <RouterLink
                v-for="c in item.children"
                :key="c.to"
                :to="c.to"
                class="sub-item"
                :class="{ active: isActive(c.to) }"
              >
                <Icon :name="c.icon" :size="15" />
                <span>{{ c.label }}</span>
                <span v-if="c.soon" class="soon-tag">即将</span>
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
          <div class="search">
            <Icon name="search" :size="16" />
            <span>搜索应用、资源…</span>
            <kbd>⌘K</kbd>
          </div>
          <el-dropdown trigger="click" @command="onPickKey">
            <div class="tenant-chip">
              <div class="t-avatar">{{ identityInitial }}</div>
              <span v-if="!collapsed">{{ identityLabel }}</span>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item
                  v-for="p in PRESET_KEYS"
                  :key="p.key"
                  :command="p.key"
                  :disabled="p.key === auth.key"
                >
                  <div class="key-row">
                    <span>{{ p.label }}</span>
                    <span class="key-role">{{ p.role }}</span>
                  </div>
                </el-dropdown-item>
                <el-dropdown-item command="__custom" divided>自定义 Key…</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <div class="user-avatar">{{ identityInitial }}</div>
        </div>
      </header>

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
  background: linear-gradient(180deg, #0d111c 0%, #0a0d14 100%);
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
.nav-item.static {
  cursor: default;
  color: var(--text-faint);
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  padding: 12px 12px 4px;
}
.nav-item:hover:not(.static):not(.active) {
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
.soon-tag {
  margin-left: auto;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--surface-2);
  color: var(--text-faint);
  font-size: 10px;
  font-weight: 500;
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
.sub-item .soon-tag {
  margin-left: auto;
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
  background: rgba(10, 13, 20, 0.6);
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
  padding: 7px 12px;
  border-radius: var(--radius);
  background: var(--surface);
  border: 1px solid var(--border);
  color: var(--text-faint);
  font-size: 13px;
  min-width: 260px;
  cursor: pointer;
  transition: border-color 0.15s;
}
.search:hover {
  border-color: var(--border-strong);
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
  .soon-tag,
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
</style>
