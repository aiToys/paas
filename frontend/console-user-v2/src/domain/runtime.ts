// 全部环境必须保留所有基线，不能以 find 任取一个实例，也不能混入测试泳道。
export function baselineRunsFor<T extends { serviceId?: string; envId: string; laneId: string }>(serviceId: string, workloads: T[], environment = ''): T[] {
  return workloads.filter((w) => w.serviceId === serviceId && (!w.laneId || w.laneId === 'default') && (!environment || w.envId === environment))
}
