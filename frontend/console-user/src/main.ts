import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles/theme.css'
import App from './App.vue'
import router from './router'

// 用户控制台入口：挂载 Pinia / 路由 / Element Plus（深色主题）。
const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(ElementPlus)
// 等初始导航完成（含异步会话守卫）再挂载：App.vue onMounted 才能读到最终 URL
// 的 query（如 ?env= 环境恢复）——不等 isReady 时 route.query 是初始 '/' 的空值，
// 刷新后环境选择框回落「全部环境」（URL 标识仍在的 bug 根因）。
router.isReady().then(() => app.mount('#app'))
