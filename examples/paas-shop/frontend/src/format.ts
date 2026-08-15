// 通用格式化函数（ProductCard / App 共用）
export function fmtPrice(p: number) {
  return '¥' + p.toFixed(2)
}
