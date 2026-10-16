<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import NotificationBell from '@/components/NotificationBell.vue'
import CommandPalette from '@/components/CommandPalette.vue'
import { navigation } from '@/navigation'
import { useEnvStore } from '@/stores/env'
import { useSessionStore } from '@/stores/session'
import { useTheme } from '@/composables/useTheme'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()
const session = useSessionStore()
const { theme, toggle } = useTheme()
const mobileNav = ref(false)
const menuButton = ref<HTMLButtonElement>()
watch(mobileNav, async (open) => {
  await nextTick()
  if (open) document.querySelector<HTMLAnchorElement>('#main-navigation .nav-link')?.focus()
  else menuButton.value?.focus()
})
const searchOpen = ref(false)
const isPublic = computed(() => route.meta.public === true)
const profileName = computed(() => session.profile?.username ?? '用户')
const pageTitle = computed(() => String(route.meta.title || '控制台'))
const currentGroup = computed(() => navigation.find((g) => g.items.some((i) => active(i.to)))?.label ?? '工作空间')
const scoped = computed(() => /^\/(products\/.+|workloads|dataservices|observability|governance)/.test(route.path))
const envLabel = computed(() => envStore.currentEnv?.name ?? '全部环境')
const now = ref(Date.now())
const prodRemaining = computed(() => {
  return envStore.prodDeadline ? Math.max(0, Math.floor((envStore.prodDeadline - now.value) / 1000)) : 0
})
function active(to: string) {
  return route.path === to || route.path.startsWith(to + '/')
}
async function pickEnv(id: string) {
  const env = envStore.envs.find((e) => e.id === id) ?? null
  if (await envStore.switchEnv(env)) {
    await router.replace({ query: { ...route.query, env: env?.id } })
  }
}
function clearContext() {
  void envStore.switchEnv(null)
  envStore.envs = []
}
async function account(cmd: string) {
  if (cmd === 'logout') {
    await session.logout()
    clearContext()
    await router.push('/login')
  } else if (cmd === 'keys') await router.push('/api-keys')
}
async function restoreEnv() {
  const id = route.query.env
  if (typeof id !== 'string' || id === envStore.currentEnvId) return
  if (!envStore.envs.length) await envStore.loadEnvs()
  const env = envStore.envs.find((e) => e.id === id)
  if (env && !await envStore.switchEnv(env)) {
    await router.replace({ query: { ...route.query, env: envStore.currentEnvId || undefined } })
  }
}
watch(() => route.query.env, restoreEnv)
watch(() => route.fullPath, () => {
  mobileNav.value = false
  if (scoped.value && !route.query.env && envStore.currentEnvId) void router.replace({ query: { ...route.query, env: envStore.currentEnvId } })
})
watch(() => session.profile?.id, async (id) => {
  if (!id) return
  await envStore.loadEnvs()
  await restoreEnv()
}, { immediate: true })
function expired() {
  if (isPublic.value) return
  const redirect = route.fullPath
  session.profile = null
  session.loaded = false
  clearContext()
  ElMessage.warning('登录已过期，请重新登录')
  void router.push({ path: '/login', query: { redirect } })
}
function shortcut(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k' && !isPublic.value) {
    e.preventDefault()
    searchOpen.value = !searchOpen.value
  }
  if (e.key === 'Escape') mobileNav.value = false
}
let timer: number | undefined
onMounted(() => {
  window.addEventListener('keydown', shortcut)
  window.addEventListener('paas:session-expired', expired)
  timer = window.setInterval(() => {
    now.value = Date.now()
    if (envStore.checkProdTimeout()) {
      void router.replace({ query: { ...route.query, env: undefined } })
      ElMessage.info('生产浏览会话已结束，已切回全部环境')
    }
  }, 1000)
})
onUnmounted(() => {
  window.removeEventListener('keydown', shortcut)
  window.removeEventListener('paas:session-expired', expired)
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <RouterView v-if="isPublic" />
  <div v-else class="console-shell" :class="{ 'production-scope': scoped && envStore.isProd }">
    <a href="#main-content" class="skip-link">跳到主要内容</a>
    <button v-if="mobileNav" class="sidebar-backdrop" aria-label="关闭导航" @click="mobileNav = false"></button>
    <aside id="main-navigation" class="console-sidebar" :class="{ 'is-open': mobileNav }" aria-label="主导航">
      <RouterLink to="/" class="console-brand"><span class="brand-mark">P</span><span>PaaS<span class="brand-caption">开发者控制台</span></span></RouterLink>
      <nav class="console-nav" aria-label="平台功能">
        <RouterLink to="/" class="nav-link overview-link" :class="{ active: route.path === '/' }" :aria-current="route.path === '/' ? 'page' : undefined">
          <Icon name="layers" :size="18" /><span>工作空间总览</span>
        </RouterLink>
        <section v-for="g in navigation" :key="g.label" class="nav-group">
          <h2>{{ g.label }}</h2>
          <RouterLink v-for="i in g.items" :key="i.to" :to="i.to" class="nav-link" :class="{ active: active(i.to) }" :aria-current="active(i.to) ? 'page' : undefined">
            <Icon :name="i.icon" :size="17" /><span>{{ i.label }}</span>
          </RouterLink>
        </section>
      </nav>
      <div class="sidebar-footer"><span class="connection-dot"></span><span>组织工作空间</span><button class="text-button" @click="searchOpen = true">快速访问 <kbd>⌘ K</kbd></button></div>
      <button class="mobile-nav-close act" @click="mobileNav = false">关闭导航</button>
    </aside>
    <div class="console-main" :inert="mobileNav || undefined">
      <header class="console-topbar">
        <button ref="menuButton" class="icon-button mobile-menu" :aria-expanded="mobileNav" aria-controls="main-navigation" aria-label="展开导航" @click="mobileNav = !mobileNav"><Icon name="layers" :size="20" /></button>
        <div class="context-path"><span>{{ currentGroup }}</span><span aria-hidden="true">/</span><strong>{{ pageTitle }}</strong></div>
        <div class="topbar-actions">
          <button class="search-trigger" aria-label="搜索产品和功能，快捷键 Ctrl 或 Command K" @click="searchOpen = true"><Icon name="search" :size="16" /><span>搜索产品或功能</span><kbd>⌘ K</kbd></button>
          <el-dropdown v-if="scoped" trigger="click" @command="pickEnv">
            <button class="scope-button" :class="{ production: envStore.isProd }" :aria-label="'浏览环境：' + envLabel">
              <Icon :name="envStore.isProd ? 'shield' : 'env'" :size="16" /><span>{{ envLabel }}</span>
              <small v-if="envStore.isProd">{{ Math.floor(prodRemaining / 60) }}:{{ String(prodRemaining % 60).padStart(2, '0') }}</small><Icon name="chevron" :size="13" />
            </button>
            <template #dropdown>
<el-dropdown-menu>
              <el-dropdown-item command="" :disabled="!envStore.currentEnv">全部环境</el-dropdown-item>
              <el-dropdown-item v-for="e in envStore.envs" :key="e.id" :command="e.id" :disabled="e.id === envStore.currentEnvId">{{ e.name }}{{ e.type === 'prod' ? ' · 生产' : '' }}</el-dropdown-item>
            </el-dropdown-menu>
</template>
          </el-dropdown>
          <NotificationBell />
          <button class="icon-button theme-toggle" :aria-label="theme === 'dark' ? '切换为亮色主题' : '切换为暗色主题'" @click="toggle"><Icon name="theme" :size="18" /></button>
          <el-dropdown trigger="click" @command="account">
            <button class="account-button" :aria-label="'账户菜单：' + profileName"><span class="account-avatar">{{ profileName.slice(0, 1).toUpperCase() }}</span><span>{{ profileName }}</span><Icon name="chevron" :size="12" /></button>
            <template #dropdown><el-dropdown-menu><el-dropdown-item disabled>{{ profileName }}</el-dropdown-item><el-dropdown-item command="keys" divided>API 密钥</el-dropdown-item><el-dropdown-item command="logout">退出登录</el-dropdown-item></el-dropdown-menu></template>
          </el-dropdown>
        </div>
      </header>
      <div v-if="scoped && envStore.isProd" class="production-banner"><Icon name="shield" :size="14" /> 正在浏览生产环境 · 写操作以实际资源或流水线配置为准</div>
      <main id="main-content" tabindex="-1" class="console-content"><RouterView /></main>
    </div>
    <CommandPalette v-model="searchOpen" />
  </div>
</template>

<style scoped>
.console-shell { display: flex; min-height: 100vh; }
.console-sidebar { width: 228px; flex-shrink: 0; position: fixed; inset: 0 auto 0 0; display: flex; flex-direction: column; z-index: 30; background: var(--surface); border-right: 1px solid var(--border); }
.console-brand { display: flex; align-items: center; gap: 11px; height: 76px; padding: 0 22px; font-size: 19px; font-weight: 700; text-decoration: none; letter-spacing: -.4px; }
.brand-mark { width: 33px; height: 33px; display: grid; place-items: center; border-radius: 8px; color: #fff; background: var(--brand-solid); font-size: 18px; }
.brand-caption { display: block; font-size: 11px; letter-spacing: .6px; font-weight: 400; color: var(--text-faint); margin-top: 2px; }
.console-nav { padding: 0 12px 16px; overflow-y: auto; flex: 1; }
.nav-group { margin-top: 20px; }
.nav-group h2 { font-size: 11px; font-weight: 600; color: var(--text-faint); margin: 0 12px 7px; letter-spacing: .7px; }
.nav-link { display: flex; gap: 11px; align-items: center; padding: 9px 12px; min-height: 36px; border-radius: 6px; margin: 2px 0; color: var(--text-dim); text-decoration: none; font-size: 13px; }
.nav-link:hover { background: var(--surface-hover); color: var(--text); }
.nav-link.active { color: var(--brand); background: var(--brand-soft); font-weight: 600; }
.overview-link { margin-bottom: 10px; }
.sidebar-footer { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 16px 22px; border-top: 1px solid var(--border); color: var(--text-faint); font-size: 11px; }
.sidebar-footer .text-button { font-size: 11px; padding: 0; }
.connection-dot { width: 6px; height: 6px; background: var(--text-faint); border-radius: 50%; }
.console-main { margin-left: 228px; flex: 1; min-width: 0; }
.console-topbar { height: 60px; padding: 0 28px; display: flex; align-items: center; gap: 16px; background: var(--surface); border-bottom: 1px solid var(--border); position: sticky; top: 0; z-index: 20; }
.context-path { display: flex; gap: 10px; align-items: center; min-width: 0; font-size: 12px; color: var(--text-faint); white-space: nowrap; }
.context-path strong { color: var(--text-dim); font-weight: 500; }
.topbar-actions { display: flex; align-items: center; gap: 10px; margin-left: auto; }
.search-trigger { display: flex; align-items: center; gap: 10px; color: var(--text-faint); background: var(--surface-2); border: 1px solid var(--border); padding: 7px 10px; border-radius: 6px; font: inherit; font-size: 12px; cursor: pointer; }
.search-trigger kbd { margin-left: 20px; }
.scope-button { display: flex; align-items: center; gap: 7px; border: 1px solid var(--border-strong); color: var(--text-dim); background: var(--surface); padding: 7px 10px; border-radius: 6px; font: inherit; font-size: 12px; cursor: pointer; white-space: nowrap; }
.scope-button.production { color: var(--danger); border-color: var(--danger); }
.account-button { display: flex; align-items: center; gap: 8px; color: var(--text-dim); background: transparent; border: 0; padding: 3px; font: inherit; font-size: 12px; cursor: pointer; }
.account-avatar { width: 28px; height: 28px; display: grid; place-items: center; background: var(--brand-soft); color: var(--brand); font-weight: 600; border-radius: 50%; }
.console-content { width: 100%; max-width: 1680px; margin: 0 auto; padding: 32px; outline: none; }
.production-banner { display: flex; align-items: center; justify-content: center; gap: 8px; font-size: 12px; padding: 8px; color: var(--danger); background: var(--danger-soft); border-bottom: 1px solid var(--border); }
.mobile-menu { display: none !important; }
.mobile-nav-close { display: none; margin: 10px 16px; }
@media (max-width: 1200px) { .search-trigger span, .search-trigger kbd { display: none; } .account-button > span:not(.account-avatar) { display: none; } .console-topbar { padding: 0 20px; } .console-content { padding: 24px; } }
@media (max-width: 1023px) { .console-sidebar { transform: translateX(-100%); visibility: hidden; } .console-sidebar.is-open { transform: none; visibility: visible; } .mobile-nav-close { display: flex; } .console-main { margin-left: 0; } .mobile-menu { display: inline-flex !important; } .sidebar-backdrop { position: fixed; inset: 0; z-index: 29; background: #0008; border: 0; } }
@media (max-width: 600px) { .console-content { padding: 20px 14px; } .console-topbar { gap: 8px; padding: 0 12px; } .context-path { display: none; } .topbar-actions { gap: 5px; } .theme-toggle { display: none !important; } }
</style>
