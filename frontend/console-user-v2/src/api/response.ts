// 对象与列表分开处理：data:null 对对象仍为 null，对列表才解释为合法空列表。
export function unwrapData(value: unknown): unknown {
  return value !== null && typeof value === 'object' && 'data' in value ? (value as { data: unknown }).data : value
}
export function asList<T>(value: unknown): T[] {
  if (value == null) return []
  if (!Array.isArray(value)) throw new Error('列表响应格式错误，请重试或联系管理员')
  return value as T[]
}
