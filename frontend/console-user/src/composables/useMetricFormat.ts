// 指标数字/单位格式化（对标 Grafana 单位格式化：SI 后缀自适应 + isFinite 守卫）。
// Observability 与 AppObservability 共用（此前两文件各自 fmtVal 把 0.0009 核显示为 "0.0"，误导性归零）。

/** 通用数值：>=100 取整，否则 1 位小数；非有限值返 "—"。 */
export function fmtVal(v: number): string {
  if (!Number.isFinite(v)) return '—'
  return v >= 100 ? Math.round(v).toString() : v.toFixed(1)
}

/** CPU 核数：<0.1 核显示 millicores（0.0009 核 → "0.9m"），>=1 显示核。 */
export function fmtCores(v: number): string {
  if (!Number.isFinite(v)) return '—'
  if (v > 0 && v < 0.1) return `${(v * 1000).toFixed(1)}m`
  return fmtVal(v)
}

/** 内存 MiB：>=1024 显示 GiB。 */
export function fmtMiB(v: number): string {
  if (!Number.isFinite(v)) return '—'
  if (v >= 1024) return `${(v / 1024).toFixed(1)}Gi`
  return fmtVal(v)
}

/** 毫秒：<1 显示 µs，>=1000 显示 s。 */
export function fmtMs(v: number): string {
  if (!Number.isFinite(v)) return '—'
  if (v > 0 && v < 1) return `${(v * 1000).toFixed(0)}µs`
  if (v >= 1000) return `${(v / 1000).toFixed(1)}s`
  return fmtVal(v)
}

/** 按指标单位选格式化器（CPU cores / 内存 MiB / 延迟 ms / 其余通用）。 */
export function fmtMetric(v: number, unit: string): string {
  if (unit === 'cores') return fmtCores(v)
  if (unit === 'MiB') return fmtMiB(v)
  if (unit === 'ms') return fmtMs(v)
  return fmtVal(v)
}

/** sparkline 高度归一化：分母用 max - min(0, min)（基线钉 0，平坦线不因 min-max 拉伸失真）。 */
export function sparkHeights(points: number[], maxH = 20): number[] {
  if (!points.length) return []
  let max = -Infinity
  let min = 0
  for (const p of points) {
    if (Number.isFinite(p)) {
      if (p > max) max = p
      if (p < min) min = p
    }
  }
  const denom = max - min
  if (!(denom > 0)) return points.map(() => 1)
  return points.map((p) => Math.max(1, Math.round(((p - min) / denom) * maxH)))
}
