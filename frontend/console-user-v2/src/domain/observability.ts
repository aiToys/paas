// 后端无 env/lane 查询参数时，按权威工作负载清单解析为精确实体查询。
export interface ObservationScope {
  app: string
  ds: string
  workload: string
  env: string
  lane: string
}
export interface ObservedWorkload {
  id: string
  appId: string
  envId: string
  laneId?: string
  name?: string
}
export interface ObservedDataService {
  id: string
  envId: string
  name: string
}
export function observationTargets(
  scope: ObservationScope,
  workloads: ObservedWorkload[],
  services: ObservedDataService[],
) {
  if (scope.ds)
    return services
      .filter((d) => d.id === scope.ds && (!scope.env || d.envId === scope.env))
      .map((d) => ({ targetType: 'dataservice', targetId: d.id }))
  if (scope.workload || scope.env || scope.lane) {
    const targets = workloads
      .filter(
        (w) =>
          (!scope.workload || w.id === scope.workload) &&
          (!scope.app || w.appId === scope.app) &&
          (!scope.env || w.envId === scope.env) &&
          (!scope.lane || (w.laneId || 'default') === scope.lane),
      )
      .map((w) => ({ targetType: 'workload', targetId: w.id }))
    if (!scope.app && !scope.workload && !scope.lane)
      targets.push(
        ...services
          .filter((d) => d.envId === scope.env)
          .map((d) => ({ targetType: 'dataservice', targetId: d.id })),
      )
    return targets
  }
  if (scope.app) return [{ targetType: 'app', targetId: scope.app }]
  return [{}] as { targetType?: string; targetId?: string }[]
}
export const metricDefinitions: Record<
  string,
  { label: string; group: string }
> = {
  cpu: { label: 'CPU 使用量', group: '资源' },
  mem: { label: '内存工作集', group: '资源' },
  disk_io: { label: '磁盘吞吐', group: '资源' },
  net_io: { label: '网络吞吐', group: '资源' },
  disk_usage: { label: '磁盘使用率', group: '资源' },
  rps: { label: '请求速率', group: '请求' },
  latency: { label: 'P95 延迟', group: '请求' },
  errorRate: { label: '错误率', group: '请求' },
  connections: { label: '连接数', group: '引擎' },
  qps: { label: '查询速率', group: '引擎' },
  hit_rate: { label: '缓存命中率', group: '引擎' },
  lag: { label: '消息速率', group: '引擎' },
  vectors: { label: '向量数', group: '引擎' },
}
export function metricLabel(name: string) {
  return metricDefinitions[name]?.label || name
}
export function finitePoints<T extends { ts: string; value: number }>(
  points: T[],
) {
  return points
    .filter(
      (p) => Number.isFinite(p.value) && Number.isFinite(Date.parse(p.ts)),
    )
    .sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts))
}

// 图表统一换算比例，轴刻度与悬停值使用同一单位，避免出现 Gi MiB 之类混合单位。
export function metricScale(unit: string, maximum: number) {
  if (unit === 'cores' && maximum > 0 && maximum < 0.1)
    return { factor: 1000, unit: 'mCPU' }
  if (unit === 'cores') return { factor: 1, unit: '核' }
  if (unit === 'MiB' && maximum >= 1024)
    return { factor: 1 / 1024, unit: 'GiB' }
  if (unit === 'ms' && maximum >= 1000) return { factor: 1 / 1000, unit: 's' }
  return { factor: 1, unit: unit || '数值' }
}
