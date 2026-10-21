import { ref } from 'vue'
import { configureLanes, detectEnv, laneError, type LaneOption } from './lane'

export interface PlatformContext {
  envId: string; envName: string; envType: 'test' | 'prod'; appId: string
  consoleUrl: string; discovery: boolean; lanes: LaneOption[]
}
export const platform = ref<PlatformContext | null>(null)
export const platformLoading = ref(false)
export const platformError = ref('')

export async function loadPlatform() {
  platformLoading.value = true
  platformError.value = ''
  try {
    const response = await fetch('/api/platform/lanes', { signal: AbortSignal.timeout(10000) })
    const body = await response.json()
    if (!response.ok) throw new Error(body.error || `平台读取失败（${response.status}）`)
    const p = body.data as PlatformContext
    if (!p?.envId || !['test', 'prod'].includes(p.envType) || !Array.isArray(p.lanes) ||
      p.lanes.some(l => !l.id || !l.name || l.envId !== p.envId || l.status !== 'active')) throw new Error('平台泳道响应格式错误')
    platform.value = p
    configureLanes(p.envId, p.envType, p.discovery ? p.lanes : [])
  } catch (err) {
    platform.value = null
    configureLanes('', 'prod', [])
    platformError.value = err instanceof Error ? err.message : '平台暂不可用'
    laneError.value = detectEnv(location.hostname).kind === 'lane' ? '泳道域名无法核实，已停止请求，请从测试基线入口重试' : ''
  } finally { platformLoading.value = false }
}

export function consoleLink(path: string): string {
  const base = platform.value?.consoleUrl
  if (!base) return ''
  return base + path.replace(/^\//, '')
}
