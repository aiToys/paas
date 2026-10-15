// 全局环境上下文 store。
// 环境从「页面过滤」升为「全局上下文」：当前环境贯穿所有页面，类似 K8s current context。
// 生产环境 gated 进入（确认 + 15 分钟超时切回全部环境），防误操作生产。
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'

export interface Env {
  id: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
  promoteOrder?: number // 发布流水线阶序（升序），0/缺省=不参与
}

const PROD_TIMEOUT_MS = 15 * 60 * 1000 // 生产会话 15 分钟超时

export const useEnvStore = defineStore('env', () => {
  const envs = ref<Env[]>([])
  const currentEnv = ref<Env | null>(null)
  const enteredProdAt = ref<number>(0)
  const prodDeadline = ref<number>(0) // 生产超时截止时间戳；0 表示非生产或未计时

  const isProd = computed(() => currentEnv.value?.type === 'prod')
  const currentEnvId = computed(() => currentEnv.value?.id ?? '')

  let inflight: Promise<void> | null = null
  async function loadEnvs(): Promise<void> {
    // 并发去重：多视图 onMounted 同时调（Applications/Workloads/DevOps/Observability），
    // 不去重登录后首屏最多 3-4 路重复 /api/environments。
    if (inflight) return inflight
    inflight = (async () => {
      const resp = await fetchAuth('/api/environments')
      if (resp.ok) {
        // JSON 防护：网关 502 返回 HTML 时 json() throw 会沿 inflight 拒绝，
        // 所有 await loadEnvs() 的调用点均无 catch → unhandledrejection（R9-1）
        const json = await resp.json().catch(() => null)
        envs.value = ((json?.data as Env[]) ?? [])
      }
    })().finally(() => { inflight = null })
    return inflight
  }

  // switchEnv 切换当前环境。生产需二次确认 + 启动超时计时。
  async function switchEnv(env: Env | null): Promise<boolean> {
    if (env?.type === 'prod') {
      try {
        await ElMessageBox.confirm(
          '你将进入生产环境，所有操作请谨慎。生产会话 15 分钟后自动退出生产（切回全部环境）。',
          '进入生产环境',
          { type: 'warning', confirmButtonText: '确认进入', cancelButtonText: '取消' },
        )
      } catch {
        return false // 用户取消
      }
      enteredProdAt.value = Date.now()
      prodDeadline.value = Date.now() + PROD_TIMEOUT_MS
    } else {
      enteredProdAt.value = 0
      prodDeadline.value = 0
    }
    currentEnv.value = env
    window.dispatchEvent(new CustomEvent('paas:env-changed'))
    return true
  }

  // 超时回退：生产会话到期自动切回无环境（全部）
  function checkProdTimeout(): boolean {
    if (prodDeadline.value > 0 && Date.now() >= prodDeadline.value) {
      currentEnv.value = null
      prodDeadline.value = 0
      enteredProdAt.value = 0
      window.dispatchEvent(new CustomEvent('paas:env-changed'))
      return true
    }
    return false
  }

  // 剩余生产会话时间（秒），非生产返回 0
  const prodRemainingSec = computed(() => {
    if (prodDeadline.value === 0) return 0
    return Math.max(0, Math.floor((prodDeadline.value - Date.now()) / 1000))
  })

  return {
    envs,
    currentEnv,
    isProd,
    currentEnvId,
    prodRemainingSec,
    loadEnvs,
    switchEnv,
    checkProdTimeout,
  }
})
