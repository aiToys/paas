// admin 轮询治理（R3-1）：console-user 的 usePolling 已含「页面不可见暂停」，
// admin 侧 7 处裸 setInterval 无此治理——后台 tab 每 10s 照常打 API（trace 噪音 +
// 配额消耗）。本工具最小化接入：保留调用方 setInterval 结构，tick 首行守卫。
// 用法：const tick = visibleTick(() => { ... }); setInterval(tick, 10000)
export function visibleTick(fn: () => void): () => void {
  return () => {
    if (document.hidden) return
    fn()
  }
}
