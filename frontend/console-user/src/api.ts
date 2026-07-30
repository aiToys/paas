import { reactive } from 'vue'
import { ElMessage } from 'element-plus'

// 统一的 API Key 与鉴权封装。
// API Key 是 (租户, 用户, 角色) 三元组的凭证：换 Key 即换租户/权限视角。
// 后端 Platform Core 解析 Key 后按租户隔离应用数据；模型目录为平台级共享。

const STORAGE_KEY = 'paas.apiKey'
const DEFAULT_KEY = 'sk-acme-admin'

// 预设演示 Key，对应后端 seedIdentity。
export const PRESET_KEYS = [
  { key: 'sk-acme-admin', label: 'Acme · 管理员', tenant: 't-acme', role: 'tenant-admin' },
  { key: 'sk-acme-dev', label: 'Acme · 开发者', tenant: 't-acme', role: 'developer' },
  { key: 'sk-globex-admin', label: 'Globex · 管理员', tenant: 't-globex', role: 'tenant-admin' },
] as const

function loadKey(): string {
  return localStorage.getItem(STORAGE_KEY) || DEFAULT_KEY
}

export const auth = reactive({ key: loadKey() })

export function setApiKey(k: string) {
  auth.key = k
  localStorage.setItem(STORAGE_KEY, k)
  // 通知所有视图：租户/权限视角已变，需重载数据
  window.dispatchEvent(new CustomEvent('paas:key-changed'))
}

// 当前 Key 对应的预设信息（顶栏展示用）；自定义 Key 返回 undefined。
export function currentPreset() {
  return PRESET_KEYS.find((p) => p.key === auth.key)
}

// fetchAuth 统一注入 Bearer，处理 401/403 全局提示。返回原始 Response，由调用方解码。
export async function fetchAuth(path: string, opts: RequestInit = {}): Promise<Response> {
  const headers = new Headers(opts.headers)
  headers.set('Authorization', `Bearer ${auth.key}`)
  if (opts.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const resp = await fetch(path, { ...opts, headers })
  if (resp.status === 401) {
    ElMessage.error('API Key 无效，请在顶栏切换')
  } else if (resp.status === 403) {
    ElMessage.error('权限不足：当前 Key 缺少所需权限')
  } else if (resp.status === 429) {
    // 配额超限（资源 Create 横切拦截）：引导去配额页调整上限。所有写操作统一生效。
    ElMessage.warning('配额超限：可在「设置 → 配额与账单」调整配额上限')
  }
  return resp
}

// fetchJSON 是 fetchAuth 的类型化封装：自动解 JSON 并按生成的契约类型 T 标注响应。
// T 取自 src/api/types.gen.ts（pnpm gen:api 从后端 /openapi.json 生成）。
// 401/403/429 的全局提示同 fetchAuth；非 2xx 抛错由调用方 catch。
export async function fetchJSON<T>(path: string, opts?: RequestInit): Promise<T> {
  const resp = await fetchAuth(path, opts)
  return (await resp.json()) as T
}
