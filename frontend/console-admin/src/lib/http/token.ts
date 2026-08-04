// Token 读取接口（避免 http ↔ auth 循环依赖）
// M3 阶段 authService 会注入真正的实现
export interface TokenReader {
  getAccessToken(): string | null
}

// 占位实现：M3 阶段被替换
export const noopTokenReader: TokenReader = {
  getAccessToken: () => null
}

// 运行时持有的 TokenReader（由 auth 模块在启动时设置）
let activeTokenReader: TokenReader = noopTokenReader

export function setTokenReader(r: TokenReader): void {
  activeTokenReader = r
}

export function getAccessToken(): string | null {
  return activeTokenReader.getAccessToken()
}

// Token 刷新接口（http ↔ auth 解耦，与 setTokenReader 同模式）
// authService 在启动时注入 refresh 实现，http 拦截器 401 时调用以透明续期。
export interface RefreshHandler {
  refresh(): Promise<unknown>
}

// 运行时持有的 RefreshHandler（由 auth 模块在启动时设置）
let activeRefreshHandler: RefreshHandler | null = null

export function setRefreshHandler(h: RefreshHandler): void {
  activeRefreshHandler = h
}

// refreshAccessToken 触发注入的 refresh 实现（并发保护由 authService.refresh 内 refreshPromise 承担）。
// 未注入或 refresh 失败（如无 refresh token、已登出）抛错，调用方走 401 原错误路径。
export async function refreshAccessToken(): Promise<unknown> {
  if (!activeRefreshHandler) {
    throw new Error('no refresh handler registered')
  }
  return activeRefreshHandler.refresh()
}
