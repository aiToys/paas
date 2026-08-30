// usePolling 统一轮询 composable：页面不可见自动暂停、恢复可见立即补拉。
// 消除各页手写 setInterval 的后台 tab 请求风暴（trace/指标噪音 + 无谓配额消耗）。
// onUnmounted 自动清理，调用方无需手动 clearInterval。
import { onMounted, onUnmounted } from 'vue'

// opts.active：条件轮询（如「有 pending 构建才拉」「run 未终态才拉」）。
// 返回 false 时跳过本次 tick（不发请求），定时器保留——替代各页手写的
// 「启动/停止 setInterval」条件轮询模式，语义等价且自带不可见暂停。
export function usePolling(
  fn: () => void | Promise<void>,
  intervalMs: number,
  opts?: { active?: () => boolean },
): void {
  let timer: number | undefined
  let inFlight = false // 上一次 fn 未返回时跳过本次 tick，防慢请求堆叠
  const tick = async () => {
    if (inFlight) return
    if (opts?.active && !opts.active()) return
    inFlight = true
    try {
      await fn()
    } finally {
      inFlight = false
    }
  }

  function start() {
    if (timer === undefined) {
      tick() // 启动/恢复立即执行一次
      timer = window.setInterval(tick, intervalMs)
    }
  }
  function stop() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }
  function onVisibility() {
    if (document.hidden) stop()
    else start()
  }

  onMounted(() => {
    start()
    document.addEventListener('visibilitychange', onVisibility)
  })
  onUnmounted(() => {
    stop()
    document.removeEventListener('visibilitychange', onVisibility)
  })
}
