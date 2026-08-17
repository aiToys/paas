// 泳道（lane）前端感知：全局 lane 状态 + fetch/EventSource 染色注入。
//
// 演示形态：header 泳道选择器输入 lane 名（如 feature-x），所有出站请求自动带
// x-paas-lane header -> bff LaneMiddleware 提取 -> trace span paas.lane 属性 +
// 透传下游（ApplyLaneHeader）-> 数据面泳道发现（/dp/instances?lane= 优先返
// 泳道实例）。切回「基线」即清除染色，流量回 default lane。
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
  return lane.value ? { [LANE_HEADER]: lane.value } : {}
}

/** laneFetch：fetch 包装——合并 lane header（不覆盖调用方显式设置的同名 header）。 */
export async function laneFetch(input: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers)
  if (lane.value && !headers.has(LANE_HEADER)) headers.set(LANE_HEADER, lane.value)
  return fetch(input, { ...init, headers })
}
