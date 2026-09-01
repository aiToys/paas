import { createApp, watch } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from '@/router'
// 图标按需注册（R8-3：全量 * import 把 ~300 个图标全打进主包）。
// 仅注册实际用到的：模板 <Xxx/> 组件用法 + 菜单/按钮 icon:'Xxx' 字符串用法。
// 新增图标时在此追加具名导入即可（漏配表现为该图标不渲染，构建不报错——
// 需要视觉验证）。
import {
  Avatar, Bell, Box, ChatDotRound, Coin, Connection, Cpu, CreditCard, Document, DocumentCopy,
  Expand, Files, Fold, FolderOpened, Grid, HomeFilled, Key, Loading, Lock, MagicStick, Menu,
  Monitor, Moon, Odometer, OfficeBuilding, Promotion, Setting, SetUp, Share, Stamp, Stopwatch,
  Sunny, Ticket, Tools, User, UserFilled, VideoPlay, Wallet,
} from '@element-plus/icons-vue'
const ElementPlusIconsUsed = {
  Avatar, Bell, Box, ChatDotRound, Coin, Connection, Cpu, CreditCard, Document, DocumentCopy,
  Expand, Files, Fold, FolderOpened, Grid, HomeFilled, Key, Loading, Lock, MagicStick, Menu,
  Monitor, Moon, Odometer, OfficeBuilding, Promotion, Setting, SetUp, Share, Stamp, Stopwatch,
  Sunny, Ticket, Tools, User, UserFilled, VideoPlay, Wallet,
}
import locale from 'element-plus/es/locale/lang/zh-cn'
import { defaultMonitor } from '@/lib/error/monitor'
import { installGlobalErrorHandlers } from '@/lib/error/installGlobalErrorHandlers'
import { vPermission } from '@/app/directives/permission'
import { installGuards } from '@/lib/router/guards'
import { useLayoutStore } from '@/app/stores/layout'
import { applyPrimaryColor } from '@/lib/theme/colors'
import { i18n, setLocale } from '@/lib/i18n'

// Element Plus 按需引入（R8：此前配了 ElementPlusResolver 却仍全量注册，2MB→按需）。
// 组件样式由 resolver 自动注入；ElMessage/ElMessageBox/ElLoading 服务式组件样式在此显式引入
//（服务式调用不经模板编译，resolver 捕获不到）。locale 经 ElConfigProvider 注入（App.vue 已有）。
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import 'element-plus/es/components/loading/style/css'
import 'element-plus/es/components/notification/style/css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import '@/assets/main.scss'

const app = createApp(App)
for (const [key, component] of Object.entries(ElementPlusIconsUsed)) {
  app.component(key, component)
}
// locale 由 App.vue 的 ElConfigProvider 包裹注入（按需引入下的标准做法）
const pinia = createPinia()
app.use(pinia)

// 注册权限指令（v-permission）
app.directive('permission', vPermission)

// 全局 provide monitor（依赖注入，便于替换 Sentry 等）
app.provide('monitor', defaultMonitor)

// 全局错误处理器：Vue 运行时 / window.onerror / 未捕获 Promise 拒绝
installGlobalErrorHandlers(app, defaultMonitor)

// 主题色：监听 layout store.primaryColor，写入主色 + 6 阶派生色（light-3/5/7/8/9, dark-2）
// 派生色由 lib/theme/colors 按 Element Plus 官方 SCSS mix 语义在运行时生成
const layoutStore = useLayoutStore()
watch(
  () => layoutStore.primaryColor,
  (color) => {
    applyPrimaryColor(color)
  },
  { immediate: true }
)

// 国际化：注册 vue-i18n + 同步 layout store.locale
app.use(i18n)
watch(
  () => layoutStore.locale,
  (l) => {
    setLocale(l)
  },
  { immediate: true }
)

async function bootstrap() {
  // MSW 双重门控：import.meta.env.DEV（生产构建编译期为 false，tree-shake 消除整段 mock 分支）
  // && VITE_ENABLE_MOCK=true。防 .env.production 误配 mock=true 导致 admin/123456 演示凭证在生产可登录。
  // 默认走真实后端（PaaS core）。
  const enableMock = import.meta.env.DEV && import.meta.env.VITE_ENABLE_MOCK === 'true'
  if (enableMock) {
    const { worker } = await import('@/mock/browser')
    await worker.start({
      onUnhandledRequest: 'bypass',
      serviceWorker: { url: `${import.meta.env.BASE_URL}mockServiceWorker.js` }
    })
  }

  // 安装 4 步全局守卫（必须在 app.use(router) 之前，且在 MSW worker 启动之后，
  // 否则首次导航时的 bootstrap 请求可能绕过 mock，导致动态路由注册失败）
  installGuards(router)
  app.use(router)
  app.mount('#app')
}
bootstrap()
