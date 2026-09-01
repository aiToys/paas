// 统一时间/数字格式化（深度审计 R10-4/R7：40+ 处内联 toLocaleString 依赖浏览器
// locale 导致同页格式不一；钉死 zh-CN + 24h 制）。fmtTime 的 10 份局部拷贝收敛于此。

// formatDateTime 标准 datetime：YYYY/M/D HH:mm:ss（zh-CN + hour12:false）
export function formatDateTime(t: string | number | Date): string {
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleString('zh-CN', { hour12: false })
}

// formatDate 仅日期：YYYY/M/D
export function formatDate(t: string | number | Date): string {
  return new Date(t).toLocaleDateString('zh-CN')
}

// isZeroTime 判断后端零值时间（Time 零值序列化为 0001-01-01T00:00:00Z，
// 无 omitempty 的字段会返回该值而非缺省——前端按「未发生」处理）
export function isZeroTime(t?: string): boolean {
  return !t || t.startsWith('0001-01-01')
}

// formatTokens token 计数「千」单位（1000 进位，统一 Marketplace/Billing 进制打架问题）
export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}
