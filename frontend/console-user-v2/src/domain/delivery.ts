import type { PipelineRun, BuildRun, Release } from '../api/pipeline'
export type DeliveryAction = 'build' | 'deploy' | 'build-deploy'
export interface DeliveryRecord {
  id: string; appId: string; kind: 'pipeline' | 'build' | 'deploy'; status: string
  branch: string; createdAt: string; to: string; source: string; sourceRunId?: string
  envId?: string; lane?: string; imageId?: string; service?: string
}
export function deliveryRecords(runs: PipelineRun[], builds: BuildRun[], releases: Release[]): DeliveryRecord[] {
  const buildParents = new Map<string, string>()
  for (const run of runs) for (const s of run.stageRuns ?? []) {
    const id = s.input?.buildRunId
    if (typeof id === 'string') buildParents.set(id, run.id)
  }
  return [
    ...runs.map((r): DeliveryRecord => {
      const target = r.stageRuns?.find(s => s.type === 'deploy' || s.type === 'canary')?.input
      return { id: r.id, appId: r.appId, kind: 'pipeline', status: r.status, branch: r.branch, createdAt: r.createdAt, to: '/runs/' + r.id, source: '流水线 · ' + r.trigger, envId: String(target?.envId ?? ''), lane: String(target?.lane ?? ''), service: String(target?.service ?? '') }
    }),
    ...builds.map((b): DeliveryRecord => ({ id: b.id, appId: b.appId, kind: 'build', status: b.status, branch: b.branch, createdAt: b.startedAt, to: '/builds/' + b.id, source: buildParents.has(b.id) ? '流水线构建' : '独立构建 · ' + b.trigger, sourceRunId: buildParents.get(b.id) })),
    ...releases.map((r): DeliveryRecord => ({ id: r.id, appId: r.appId, kind: 'deploy', status: r.status, branch: '', createdAt: r.createdAt, to: '/releases/' + r.id, source: r.sourceRunId ? '流水线部署' : '独立部署 · 未关联流水线', sourceRunId: r.sourceRunId, envId: r.envId, lane: r.laneId || 'default', imageId: r.imageId })),
  ].sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''))
}
