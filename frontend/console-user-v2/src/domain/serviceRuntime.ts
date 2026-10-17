import type { Instance } from '../api/workload'

// Pod Phase 不等于就绪；尤其 Running + 0/0 不能渲染成健康。
export function instanceState(instance: Instance): { label: string; cls: string } {
  if (instance.status === 'Failed') return { label: '失败', cls: 'err' }
  if (instance.status === 'Succeeded') return { label: '已完成', cls: 'ok' }
  if (instance.status === 'Pending') return { label: '等待启动', cls: 'warn' }
  if (instance.status === 'Terminating') return { label: '终止中', cls: 'idle' }
  if (instance.status !== 'Running') return { label: instance.status || '状态未知', cls: 'idle' }
  const ready = /^(\d+)\/(\d+)$/.exec(instance.ready || '')
  if (!ready || Number(ready[2]) === 0) return { label: '就绪未知', cls: 'idle' }
  return Number(ready[1]) === Number(ready[2])
    ? { label: '容器就绪', cls: 'ok' }
    : { label: '未完全就绪', cls: 'warn' }
}

export function runtimeObservationLink(workload: { id: string; appId: string; envId: string; laneId: string }, tab: string) {
  return { path: '/observability', query: { workload: workload.id, app: workload.appId, env: workload.envId, lane: workload.laneId || 'default', tab } }
}
