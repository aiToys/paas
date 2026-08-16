// usePolling 统一轮询 composable：页面不可见自动暂停、恢复可见立即补拉。
// 消除各页手写 setInterval 的后台 tab 请求风暴（trace/指标噪音 + 无谓配额消耗）。
// onUnmounted 自动清理，调用方无需手动 clearInterval。
import { onMounted, onUnmounted } from 'vue'

export function usePolling(fn: () => void | Promise<void>, intervalMs: number): void {
  let timer: number | undefined

  function start() {
    if (timer === undefined) {
      fn() // 启动/恢复立即执行一次
      timer = window.setInterval(fn, intervalMs)
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
