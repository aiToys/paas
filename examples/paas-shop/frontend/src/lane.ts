// 泳道（lane）前端感知：全局 lane 状态 + fetch/EventSource 染色注入。
//
// 演示形态：header 泳道选择器输入 lane 名（如 feature-x），所有出站请求自动带
// x-paas-lane header -> bff LaneMiddleware 提取 -> trace span paas.lane 属性 +
// 透传下游（ApplyLaneHeader）-> 数据面泳道发现（/dp/instances?lane= 优先返
// 泳道实例）。切回「基线」即清除染色，流量回 default lane。
// 另一染色来源：经 <lane>.shop.test.example.com 泳道域名访问时（detectEnv 推断），
// hostname 首段优先于手动输入（currentLane）。
//
// 选择持久化 localStorage（刷新页面保持演示状态）；非法输入（空/空白）视为基线。

import { ref } from 'vue'

export const LANE_HEADER = 'x-paas-lane'
const STORAGE_KEY = 'paas-shop:lane'

// 当前 lane；空串 = 基线（default）。模块级单例，全组件共享。
export const lane = ref<string>(readStored())

function readStored(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) || ''
  } catch {
    return ''
  }
}

export function setLane(v: string) {
  lane.value = v.trim()
  try {
    if (lane.value) localStorage.setItem(STORAGE_KEY, lane.value)
    else localStorage.removeItem(STORAGE_KEY)
  } catch {}
}

/** laneHeaders：非基线时返回含 x-paas-lane 的 headers 对象，否则空对象。 */
export function laneHeaders(): Record<string, string> {
  const l = currentLane()
  return l ? { [LANE_HEADER]: l } : {}
}

/** laneFetch：fetch 包装——合并 lane header（不覆盖调用方显式设置的同名 header）。 */
export async function laneFetch(input: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers)
  const l = currentLane()
  if (l && !headers.has(LANE_HEADER)) headers.set(LANE_HEADER, l)
  return fetch(input, { ...init, headers })
}

// detectEnv 按 hostname 推断环境：生产/测试/泳道（三套域名方案，spec §10）。
// shop.example.com → prod；shop.test.example.com → test；<lane>.shop.test.example.com → lane。
export function detectEnv(hostname: string): { kind: 'prod' | 'test' | 'lane'; lane?: string } {
  const host = hostname.split(':')[0]
  if (host === 'shop.example.com') return { kind: 'prod' }
  if (host === 'shop.test.example.com') return { kind: 'test' }
  const m = host.match(/^([a-z0-9-]+)\.shop\.test\.example\.com$/)
  if (m) return { kind: 'lane', lane: m[1] }
  return { kind: 'test' } // 本地 dev/未知域名按测试基线
}

// currentLane 当前生效泳道：hostname 泳道域名首段优先于手动输入（spec §10 三套域名
// 下访问 <lane>.shop.test... 即自动染色，无需手动输入）。
export function currentLane(): string {
  const e = detectEnv(window.location.hostname)
  if (e.kind === 'lane' && e.lane) return e.lane
  return lane.value
}
