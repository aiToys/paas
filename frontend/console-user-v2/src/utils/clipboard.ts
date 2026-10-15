// 统一剪贴板工具（深度审计 R10-2/3）：9 处散落的 navigator.clipboard 直调收敛于此。
// 处理两类历史问题：
// 1. 非 secure context（http 内网部署）navigator.clipboard 为 undefined——execCommand 降级
// 2. 失败仍报成功 / 无 catch 产生 unhandledrejection
import { ElMessage } from 'element-plus'

async function copyWithFallback(text: string): Promise<boolean> {
  // 安全上下文优先走异步 Clipboard API
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text)
    return true
  }
  // 降级：隐藏 textarea + execCommand（http 部署）
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    return document.execCommand('copy')
  } finally {
    ta.remove()
  }
}

// copyText 复制并统一反馈；成功提示可自定义（如含安全提醒的场景），失败 warning。
export async function copyText(text: string, successMsg = '已复制'): Promise<void> {
  try {
    const ok = await copyWithFallback(text)
    if (ok) {
      ElMessage.success(successMsg)
    } else {
      ElMessage.warning('复制失败，请手动选择复制')
    }
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}
