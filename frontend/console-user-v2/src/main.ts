import { createApp } from 'vue'
import { createPinia } from 'pinia'
// Element Plus 按需引入：组件与样式由 unplugin resolver 注入；
// 服务式组件（ElMessage/ElMessageBox/ElNotification/ElLoading）样式显式引入。
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import 'element-plus/es/components/notification/style/css'
import 'element-plus/es/components/loading/style/css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles/theme.css'
import App from './App.vue'
import router from './router'

// console-user-v2 入口：新 IA 试验田（/next/），与老版并行，基建与老版同源复制。
const app = createApp(App)
app.use(createPinia())
app.use(router)
router.isReady().then(() => app.mount('#app'))
