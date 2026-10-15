<script setup lang="ts">
// console-user-v2 壳（spec §9.2）：248px 侧栏一级只有「首页(产品)」与「更多」；
// 48px 顶栏 = 上下文标题 + 环境切换（唯一切换点）+ 待办铃铛 + 用户菜单。
// 「更多」低频能力并行期跳老版 /console/ 对应页（不重复实现；v2 达成后老版整体退役）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import NotificationBell from '@/components/NotificationBell.vue'
import { useEnvStore } from '@/stores/env'
import { useSessionStore } from '@/stores/session'
import { useTheme } from '@/composables/useTheme'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
const session = useSessionStore()

const isPublic = computed(() => route.meta.public === true)
const pageTitle = computed(() => (route.meta.title as string) || '控制台')

// —— 侧栏：常用一级 +「更多」二级 + 尾部管理面 ——
// 2026-10-15 用户实测反馈「常用菜单都没了」：模型/数据服务/可观测提一级（仍是并行期跳老版 ↗），
// 真正低频的（试玩/流量/密钥）留在「更多」二级。极简不等于藏起常用的。
const navItems = [
  { label: '模型', icon: 'market', to: '/console/resources/models' },
  { label: '数据服务', icon: 'database', to: '/console/resources/db' },
  { label: '可观测', icon: 'activity', to: '/console/platform/observability' },
]
const moreOpen = ref(false)
const moreItems = [
  { label: '试玩', icon: 'playground', to: '/console/playground' },
  { label: '流量', icon: 'service', to: '/console/platform/governance' },
  { label: '密钥', icon: 'shield', to: '/console/platform/security' },
]
// 管理面（spec：环境=CRUD 管理不切 scope；组织设置）。
const tailItems = [
  { label: '环境', icon: 'env', to: '/console/environments' },
  { label: '组织设置', icon: 'usage', to: '/console/settings/billing' },
]

// —— 顶栏环境切换（唯一切换点；生产 15min 超时/强隔离逻辑在 envStore）——
const envLabel = computed(() => envStore.currentEnv?.name ?? '全部环境')
const prodRemaining = computed(() => {
  const s = envStore.prodRemainingSec
  return s > 0 ? `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}` : ''
})
async function onPickEnv(cmd: string) {
  if (cmd === '__all') {
    await envStore.switchEnv(null)
    return
  }
  const env = envStore.envs.find((e) => e.id === cmd)
  if (env) await envStore.switchEnv(env)
}

// —— 用户菜单 ——
const { theme, toggle: toggleTheme } = useTheme()
async function onAccount(cmd: string) {
  if (cmd === '__logout') {
    await session.logout()
    router.push('/login')
  } else if (cmd === '__keys') {
    window.open('/console/settings/api-keys', '_blank')
  } else if (cmd === '__theme') {
    toggleTheme() // spec §9.2：主题进用户菜单（useTheme 同源持久化，防 FOUC）
  }
}
const profileName = computed(() => session.profile?.username ?? '用户')

onMounted(() => {
  session.loadProfile()
  envStore.loadEnvs()
})

// URL ?env= 恢复环境上下文（分享/刷新保持 scope）。
watch(
  () => route.query.env,
  async (id) => {
    if (!id || envStore.currentEnv?.id === id) return
    const found = envStore.envs.find((e) => e.id === id) ?? null
    if (found) await envStore.switchEnv(found)
  },
  { immediate: true },
)
</script>

<template>
  <!-- 登录等 public 路由：无壳全屏（spec §9.1） -->
  <template v-if="isPublic">
    <RouterView />
  </template>

  <div v-else class="shell">
    <aside class="sidebar">
      <RouterLink to="/" class="brand">
        <span class="brand-mark">P</span>
        <span class="brand-name">组织 · 控制台</span>
      </RouterLink>

      <nav class="nav">
        <RouterLink to="/" class="nav-item" :class="{ active: route.path === '/' }">
          <Icon name="deploy" :size="18" class="nav-icon" />
          <span class="nav-label">首页</span>
        </RouterLink>

        <!-- 常用能力一级直达（并行期跳老版，↗ 标记） -->
        <a
          v-for="n in navItems"
          :key="n.to"
          :href="n.to"
          class="nav-item"
          title="在经典版中打开"
        >
          <Icon :name="n.icon" :size="18" class="nav-icon" />
          <span class="nav-label">{{ n.label }}</span>
          <span class="ext">↗</span>
        </a>

        <!-- 更多：低频能力收进二级 -->
        <div class="nav-item more-toggle" :class="{ open: moreOpen }" @click="moreOpen = !moreOpen">
          <Icon name="layers" :size="18" class="nav-icon" />
          <span class="nav-label">更多</span>
          <span class="more-arrow">{{ moreOpen ? '▾' : '▸' }}</span>
        </div>
        <div v-show="moreOpen" class="more-list">
          <a
            v-for="m in moreItems"
            :key="m.to"
            :href="m.to"
            class="sub-item"
            title="在经典版中打开"
          >
            <Icon :name="m.icon" :size="15" />
            <span>{{ m.label }}</span>
            <span class="ext">↗</span>
          </a>
        </div>

        <div class="nav-sep"></div>
        <a
          v-for="t in tailItems"
          :key="t.to"
          :href="t.to"
          class="nav-item"
          title="在经典版中打开"
        >
          <Icon :name="t.icon" :size="18" class="nav-icon" />
          <span class="nav-label">{{ t.label }}</span>
          <span class="ext">↗</span>
        </a>
      </nav>
    </aside>

    <div class="main">
      <header class="topbar">
        <h1 class="page-title">{{ pageTitle }}</h1>
        <div class="topbar-right">
          <NotificationBell />
          <el-dropdown trigger="click" @command="onPickEnv">
            <div class="env-chip" :class="{ prod: envStore.isProd }">
              <Icon :name="envStore.isProd ? 'shield' : 'server'" :size="14" />
              <span>{{ envLabel }}</span>
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
                  {{ e.name }}<span v-if="e.type === 'prod'" class="prod-flag">生产</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>

          <el-dropdown trigger="click" @command="onAccount">
            <div class="avatar">
              <span class="avatar-letter">{{ profileName.slice(0, 1).toUpperCase() }}</span>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item disabled>{{ profileName }}</el-dropdown-item>
                <el-dropdown-item command="__keys" divided>API 密钥 ↗</el-dropdown-item>
                <el-dropdown-item command="__theme">{{ theme === 'dark' ? '切换为亮色' : '切换为暗色' }}</el-dropdown-item>
                <el-dropdown-item command="__logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <main class="content">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.shell {
  display: flex;
  min-height: 100vh;
}
/* —— 侧栏（一级 2 项 + 更多二级 + 管理面尾部）—— */
.sidebar {
  width: 248px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border);
  background: var(--surface);
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
  text-decoration: none;
  color: var(--text);
}
.brand-mark {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border-radius: 8px;
  background: var(--brand);
  color: #fff;
  font-weight: 700;
}
.brand-name {
  font-size: 14px;
  font-weight: 600;
}
.nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
  flex: 1;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 10px;
  border-radius: var(--radius);
  color: var(--text-dim);
  text-decoration: none;
  font-size: 13.5px;
  cursor: pointer;
  transition: background 0.12s, color 0.12s;
}
.nav-item:hover {
  background: var(--surface-2);
  color: var(--text);
}
.nav-item.active {
  background: var(--brand-soft);
  color: var(--brand);
  font-weight: 600;
}
.nav-icon {
  flex-shrink: 0;
}
.nav-label {
  flex: 1;
}
.more-arrow {
  font-size: 10px;
  color: var(--text-faint);
}
.more-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  margin-left: 18px;
  padding-left: 8px;
  border-left: 1px solid var(--border);
}
.sub-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  border-radius: var(--radius);
  color: var(--text-dim);
  text-decoration: none;
  font-size: 13px;
  transition: background 0.12s, color 0.12s;
}
.sub-item:hover {
  background: var(--surface-2);
  color: var(--text);
}
.ext {
  margin-left: auto;
  font-size: 10px;
  color: var(--text-faint);
}
.nav-sep {
  height: 1px;
  background: var(--border);
  margin: 8px 4px;
}

/* —— 主区 —— */
.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.topbar {
  height: 48px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 0 20px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.page-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}
.topbar-right {
  display: flex;
  align-items: center;
  gap: 14px;
}
.env-chip {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  font-size: 12.5px;
  color: var(--text-dim);
  cursor: pointer;
}
.env-chip.prod {
  border-color: var(--el-color-danger, #f43f5e);
  color: var(--el-color-danger, #f43f5e);
  background: rgba(244, 63, 94, 0.08);
}
.prod-timer {
  font-size: 11px;
}
.prod-flag {
  margin-left: 6px;
  font-size: 11px;
  color: var(--el-color-danger, #f43f5e);
}
.avatar {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: var(--brand-soft);
  color: var(--brand);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}
.content {
  flex: 1;
  padding: 20px;
  max-width: 1100px;
  width: 100%;
  margin: 0 auto;
  box-sizing: border-box;
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
