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
  if (resp.status === 401 && !path.includes('/api/auth/')) {
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
