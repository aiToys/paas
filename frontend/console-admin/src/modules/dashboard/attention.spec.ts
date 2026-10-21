import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/lib/http/client'
import type { AdminAlert, AdminPipelineRun, AdminWorkload } from '@/modules/resources/api'
import {
  fetchAttention,
  runLocation,
  workloadLocation,
  alertLocation,
  alertCondition,
  findFocusedAlert,
} from './attention'

vi.mock('@/lib/http/client', () => ({ api: { get: vi.fn() }, http: {} }))
const run = (i: number, status = 'succeeded'): AdminPipelineRun => ({
  id: `run-${i}`,
  tenantId: 't-globex',
  appId: 'app-g',
  pipelineId: 'pipeline-g',
  branch: 'main',
  commit: '',
  status,
  currentStage: '',
  createdAt: `2026-10-21T10:${String(i).padStart(2, '0')}:00Z`,
})
const workload = (i: number, ready = 1): AdminWorkload => ({
  id: `wl-${i}`,
  name: `worker-${i}`,
  tenantId: 't-globex',
  appId: 'app-g',
  envId: 'prod',
  type: 'service',
  status: 'running',
  replicas: 1,
  ready,
})
const alert = (i: number, status = 'resolved'): AdminAlert => ({
  ruleId: `rule-${i}`,
  ruleName: '低流量',
  tenantId: 't-globex',
  targetType: 'workload',
  targetId: `wl-${i}`,
  metricName: 'requests',
  value: 1,
  threshold: 5,
  operator: '<',
  severity: 'warning',
  status,
  firedAt: '2026-10-21T00:00:00Z',
})

beforeEach(() => vi.resetAllMocks())
describe('首页异常完整性和定位', () => {
  it('第51条以后的失败、告警和异常副本也被聚合，不在异常筛选前分页', async () => {
    vi.mocked(api.get).mockImplementation(async (path) => {
      if (path.includes('pipelineruns'))
        return [...Array.from({ length: 50 }, (_, i) => run(i)), run(51, 'failed')]
      if (path.includes('observability'))
        return [...Array.from({ length: 50 }, (_, i) => alert(i)), alert(51, 'firing')]
      return [...Array.from({ length: 50 }, (_, i) => workload(i)), workload(51, 0)]
    })
    const result = await fetchAttention('t-globex')
    expect(result.runs.records.map((r) => r.id)).toEqual(['run-51'])
    expect(result.alerts.records.map((r) => r.ruleId)).toEqual(['rule-51'])
    expect(result.workloads.records.map((r) => r.id)).toEqual(['wl-51'])
    expect(api.get).toHaveBeenCalledWith('/api/admin/pipelineruns', { params: { tenantId: 't-globex', status: 'failed' } })
    expect(api.get).toHaveBeenCalledWith('/api/admin/workloads', {
      params: { tenantId: 't-globex' },
    })
  })

  it('单源失败与协议变成分页对象时明确报错，其他来源继续展示', async () => {
    vi.mocked(api.get).mockImplementation(async (path) => {
      if (path.includes('pipelineruns')) throw new Error('没有权限')
      if (path.includes('observability')) return { records: [], total: 100 }
      return [workload(1, 0)]
    })
    const result = await fetchAttention()
    expect(result.runs.error).toBe('没有权限')
    expect(result.alerts.error).toContain('无法确认完整数据范围')
    expect(result.workloads.records).toHaveLength(1)
  })

  it('取最近5条失败，不把缺失的ready误判为0', async () => {
    vi.mocked(api.get).mockImplementation(async (path) => {
      if (path.includes('pipelineruns'))
        return Array.from({ length: 8 }, (_, i) => run(i, 'failed'))
      if (path.includes('observability')) return null
      return [{ ...workload(1), ready: undefined }]
    })
    const result = await fetchAttention()
    expect(result.runs.records.map((r) => r.id)).toEqual([
      'run-7',
      'run-6',
      'run-5',
      'run-4',
      'run-3',
    ])
    expect(result.workloads.records).toEqual([])
    expect(result.alerts.error).toBe('')
  })

  it('链接带具体实体、租户与适用环境/应用/对象上下文', () => {
    expect(runLocation(run(3))).toMatchObject({
      path: '/resources/pipelineruns',
      query: { tab: 'runs', tenantId: 't-globex', id: 'run-3', appId: 'app-g' },
    })
    expect(workloadLocation(workload(3))).toMatchObject({
      query: { tenantId: 't-globex', id: 'wl-3', appId: 'app-g', envId: 'prod' },
    })
    expect(alertLocation(alert(3))).toMatchObject({
      query: {
        tab: 'events',
        ruleId: 'rule-3',
        targetId: 'wl-3',
        tenantId: 't-globex',
        firedAt: alert(3).firedAt,
      },
    })
  })

  it.each([
    ['>', '>'],
    ['>=', '≥'],
    ['<', '<'],
    ['<=', '≤'],
  ])('按真实运算符%s展示', (operator, symbol) => {
    expect(alertCondition({ value: 1, threshold: 5, operator })).toBe(`1 ${symbol} 5`)
  })

  it('相同规则的不同租户和对象不会串详情；当前评估时间变化不丢失对象定位', () => {
    const target = alert(3)
    const others = [{ ...target, tenantId: 't-acme' }, { ...target, targetId: 'wl-other' }, target]
    const query = {
      tenantId: 't-globex',
      ruleId: 'rule-3',
      targetType: 'workload',
      targetId: 'wl-3',
      firedAt: 'older-snapshot',
    }
    expect(findFocusedAlert(others, query)).toBe(target)
    expect(findFocusedAlert(others, { ...query, targetId: 'absent' })).toBeUndefined()
    expect(findFocusedAlert(others, { ...query, tenantId: ['t-acme', 't-globex'] })).toBeUndefined()
  })
})
