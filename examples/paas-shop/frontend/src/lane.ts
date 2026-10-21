// 泳道名称只从平台当前环境的活跃实体解析，持久化稳定 ID。
import { ref } from 'vue'

export interface LaneOption { id: string; name: string; envId: string; status: string; description?: string }
export const LANE_HEADER = 'x-paas-lane'
export const lane = ref('')
export const selectedLaneId = ref('')
export const laneError = ref('')
export const laneNotice = ref('')
export const domainPinned = ref(false)
let envKey = ''
let options: LaneOption[] = []

export function configureLanes(envId: string, envType: string, candidates: LaneOption[]) {
  envKey = `paas-shop:lane:${envId}`
  options = candidates
  lane.value = ''
  selectedLaneId.value = ''
  laneError.value = ''
  laneNotice.value = ''
  domainPinned.value = false
  if (envType === 'prod') return
  const domain = detectEnv(location.hostname)
  if (domain.kind === 'lane') {
    domainPinned.value = true
    const match = options.find(l => l.name === domain.lane)
    if (!match) { laneError.value = '此域名对应的泳道未在当前环境启用，请从测试基线入口访问'; return }
    selectedLaneId.value = match.id
    lane.value = match.name
    return
  }
  let stored = ''
  try { stored = localStorage.getItem(envKey) || '' } catch {}
  const available = options.some(l => l.id === stored)
  if (stored && !available) laneNotice.value = '原泳道已关闭或不可用，已切换到基线'
  setLane(available ? stored : '')
}

export function setLane(id: string) {
  if (domainPinned.value) return
  const item = options.find(l => l.id === id)
  selectedLaneId.value = item?.id || ''
  lane.value = item?.name || ''
  try {
    if (selectedLaneId.value) localStorage.setItem(envKey, selectedLaneId.value)
    else if (envKey) localStorage.removeItem(envKey)
  } catch {}
}

export function laneHeaders(): Record<string, string> {
  const l = currentLane()
  return l ? { [LANE_HEADER]: l } : {}
}

export async function laneFetch(input: string, init?: RequestInit): Promise<Response> {
  if (laneError.value) throw new Error(laneError.value)
  const headers = new Headers(init?.headers)
  headers.delete(LANE_HEADER)
  const l = currentLane()
  if (l) headers.set(LANE_HEADER, l)
  return fetch(input, { ...init, headers })
}

export function detectEnv(hostname: string): { kind: 'prod' | 'test' | 'lane'; lane?: string } {
  // 按域名结构判定而非具体后缀：演示域名后缀跟随平台 PAAS_DOMAIN_SUFFIX（可变），
  // 硬编码后缀会随环境漂移失效（曾硬编码 example.com，2026-10-21 改结构化判定）。
  // 约定：test 侧基线 = shop.test.<suffix>，泳道 = <lane>.shop.test.<suffix>；其余视为 prod。
  const host = hostname.split(':')[0]
  const m = host.match(/^([a-z0-9-]+)\.shop\.test\.[a-z0-9.-]+$/)
  if (m) return { kind: 'lane', lane: m[1] }
  if (/^shop\.test\./.test(host)) return { kind: 'test' }
  return { kind: 'prod' }
}

export function currentLane(): string { return lane.value }
