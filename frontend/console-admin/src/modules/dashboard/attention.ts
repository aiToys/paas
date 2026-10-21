import type { LocationQuery, RouteLocationRaw } from 'vue-router'
import {
  fetchAdminAlertListAll,
  fetchFailedPipelineRunList,
  fetchWorkloadListAll,
  type AdminAlert,
  type AdminPipelineRun,
  type AdminWorkload,
} from '@/modules/resources/api'

export async function fetchAttention(tenantId = '') {
  const [runs, alerts, workloads] = await Promise.allSettled([
    fetchFailedPipelineRunList(tenantId),
    fetchAdminAlertListAll(tenantId),
    fetchWorkloadListAll(tenantId),
  ])
  return {
    runs:
      runs.status === 'fulfilled'
        ? {
            records: runs.value
              .filter((r) => r.status === 'failed')
              .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
              .slice(0, 5),
            error: '',
          }
        : { records: [] as AdminPipelineRun[], error: errorText(runs.reason) },
    alerts:
      alerts.status === 'fulfilled'
        ? {
            records: alerts.value
              .filter((a) => a.status === 'firing')
              .sort((a, b) => b.firedAt.localeCompare(a.firedAt))
              .slice(0, 5),
            error: '',
          }
        : { records: [] as AdminAlert[], error: errorText(alerts.reason) },
    workloads:
      workloads.status === 'fulfilled'
        ? {
            records: workloads.value
              .filter(
                (w) =>
                  w.status === 'failed' ||
                  (typeof w.ready === 'number' &&
                    typeof w.replicas === 'number' &&
                    w.replicas > 0 &&
                    w.ready < w.replicas),
              )
              .slice(0, 5),
            error: '',
          }
        : { records: [] as AdminWorkload[], error: errorText(workloads.reason) },
  }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : '数据源读取失败'
}

// 全部参数均是浏览定位；资源归属和可见性继续由管理端API校验。
export function runLocation(run: AdminPipelineRun): RouteLocationRaw {
  return {
    path: '/resources/pipelineruns',
    query: { tab: 'runs', tenantId: run.tenantId, id: run.id, appId: run.appId },
  }
}

export function workloadLocation(
  workload: Pick<AdminWorkload, 'id' | 'tenantId' | 'envId' | 'appId'>,
): RouteLocationRaw {
  return {
    path: '/resources/workloads',
    query: {
      tenantId: workload.tenantId,
      id: workload.id,
      envId: workload.envId,
      appId: workload.appId,
    },
  }
}

export function alertLocation(alert: AdminAlert): RouteLocationRaw {
  return {
    path: '/operations/observability',
    query: {
      tab: 'events',
      tenantId: alert.tenantId,
      ruleId: alert.ruleId,
      targetType: alert.targetType,
      targetId: alert.targetId,
      firedAt: alert.firedAt,
    },
  }
}

export function alertCondition(
  alert: Pick<AdminAlert, 'value' | 'operator' | 'threshold'>,
): string {
  const symbols: Record<string, string> = { '>': '>', '>=': '≥', '<': '<', '<=': '≤' }
  const operator = symbols[alert.operator]
  return operator
    ? `${alert.value} ${operator} ${alert.threshold}`
    : `${alert.value}（比较条件未提供；阈值 ${alert.threshold}）`
}

// 当前告警可能每次读取重新评估：按租户+规则+对象定位，来源时间仅作快照上下文。
export function findFocusedAlert(
  alerts: AdminAlert[],
  query: LocationQuery,
): AdminAlert | undefined {
  const keys = ['tenantId', 'ruleId', 'targetType', 'targetId'] as const
  if (keys.some((key) => typeof query[key] !== 'string' || !query[key])) return undefined
  return alerts.find((alert) => keys.every((key) => alert[key] === query[key]))
}
