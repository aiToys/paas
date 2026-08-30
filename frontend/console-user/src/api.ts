import { ElMessage } from 'element-plus'

// 会话走 httpOnly cookie（后端 Set-Cookie），前端不存不读 token。
// 401 -> 自动 refresh（cookie 自带 refresh token）-> 重试原请求一次 -> 仍 401 触发会话过期事件。
// API Key 通道（Authorization: Bearer sk-...）仍可用于程序化调用，但浏览器登录态走 cookie。

let refreshing: Promise<boolean> | null = null
function refreshSession(): Promise<boolean> {
  if (refreshing) return refreshing
  refreshing = fetch('/api/auth/tokens/refresh', { method: 'POST', credentials: 'include' })
    .then((r) => r.ok)
    .finally(() => { refreshing = null })
  return refreshing
}

// fetchAuth 统一注入 credentials，处理 401/429 全局提示。返回原始 Response，由调用方解码。
export async function fetchAuth(path: string, opts: RequestInit = {}): Promise<Response> {
  const headers = new Headers(opts.headers)
  // FormData 不设 Content-Type --浏览器需自动生成 multipart/form-data; boundary=...，
  // 强加 application/json 会覆盖 boundary 致后端 ParseMultipartForm 失败。
  if (opts.body && !(opts.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const resp = await fetch(path, { ...opts, headers, credentials: 'include' })
  // 仅排除 refresh 自身（防递归）；登录/users/me 等会话端点仍走 refresh——
  // 此前 `!path.includes('/api/auth/')` 误伤 users/me：access cookie 过期（refresh 仍有效）
  // 时 loadProfile 401 不 refresh → 被误判未登录踢回登录页。
  if (resp.status === 401 && !path.includes('/api/auth/tokens/refresh')) {
    const ok = await refreshSession()
    if (ok) return fetch(path, { ...opts, headers, credentials: 'include' })
    window.dispatchEvent(new CustomEvent('paas:session-expired'))
  } else if (resp.status === 429) {
    ElMessage.warning('请求过多，请稍后再试')
  }
  return resp
}

// fetchJSON 是 fetchAuth 的类型化封装：自动解 JSON，**自动解包 {data:T} 契约**，
// 非 2xx 抛错（含后端 error 文案）。T 取自 src/api/types.gen.ts（pnpm gen:api 生成）。
//
// 架构防护：后端统一 {data:T} 契约（httputil.WriteData），但部分历史接口裸返回对象
// 或 {service,instances} 形态。fetchJSON 智能解包--仅当响应形如 {data:...} 时取 data，
// 否则原样返回，兼容两种契约。
export async function fetchJSON<T>(path: string, opts?: RequestInit): Promise<T> {
  const resp = await fetchAuth(path, opts)
  const json = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    const msg = (json && typeof json === 'object' && 'error' in json ? json.error : null) || `HTTP ${resp.status}`
    throw new Error(msg as string)
  }
  if (json && typeof json === 'object' && 'data' in json) {
    return (json as { data: T }).data
  }
  return json as T
}

// apiError 从 fetchJSON/fetchAuth 抛出的 unknown 错误中提取用户可读文案，
// 供 catch 分支使用（替代散落各页面的 `catch (e: any) e.message` 反模式）。
export function apiError(e: unknown, fallback = '请求失败'): string {
  if (e instanceof Error && e.message) return e.message
  return fallback
}

// respError 从非 2xx 的 Response 中提取后端 {error:msg} 文案，
// 供手写 fetchAuth（需读原始 Response 的场景，如 SSE/流式）使用。
export async function respError(resp: Response, prefix = ''): Promise<string> {
  const j = await resp.json().catch(() => null)
  const msg =
    j && typeof j === 'object' && 'error' in j && typeof (j as { error: unknown }).error === 'string'
      ? (j as { error: string }).error
      : `HTTP ${resp.status}`
  return prefix ? `${prefix}${msg}` : msg
}
