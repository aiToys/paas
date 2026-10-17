import { test } from 'node:test'
import assert from 'node:assert/strict'
import { unwrapData, asList } from '../src/api/response.ts'
import {
  notificationLink,
  navigation,
  searchEntries,
} from '../src/navigation/index.ts'
import { baselineRunsFor } from '../src/domain/runtime.ts'
import { instanceState, runtimeObservationLink } from '../src/domain/serviceRuntime.ts'

test('Pod Running does not imply ready, and 0/0 or missing readiness stays unknown', () => {
  const pod = { name: 'api', status: 'Running', restarts: 0, ready: '0/0' }
  assert.equal(instanceState(pod).cls, 'idle')
  assert.equal(instanceState({ ...pod, ready: '' }).cls, 'idle')
  assert.equal(instanceState({ ...pod, ready: '0/1' }).cls, 'warn')
  assert.equal(instanceState({ ...pod, ready: '1/2' }).cls, 'warn')
  assert.equal(instanceState({ ...pod, ready: '2/2' }).cls, 'ok')
  assert.equal(instanceState({ ...pod, ready: '1/1', status: 'Failed' }).cls, 'err')
  assert.equal(instanceState({ ...pod, status: 'Terminating' }).cls, 'idle')
})
test('runtime evidence links retain the actual workload environment and lane', () => {
  assert.deepEqual(runtimeObservationLink({ id: 'run/a', appId: 'shop', envId: 'prod', laneId: 'preview' }, 'metrics'), {
    path: '/observability', query: { workload: 'run/a', app: 'shop', env: 'prod', lane: 'preview', tab: 'metrics' },
  })
})

test('Go nil slice renders as an empty list, not the response envelope', () => {
  assert.deepEqual(asList(unwrapData({ data: null })), [])
  assert.deepEqual(asList(unwrapData({ data: [] })), [])
  assert.deepEqual(asList(unwrapData({ data: [{ id: 'one' }] })), [
    { id: 'one' },
  ])
  assert.throws(() => asList({ error: 'not a list' }), /列表响应格式错误/)
  assert.equal(unwrapData({ data: null }), null)
})
test('a failed build links directly to its logs and batches keep exact focus', () => {
  assert.equal(
    notificationLink({
      targetType: 'build',
      targetId: 'build-1',
      appId: 'shop',
    }),
    '/builds/build-1',
  )
  assert.equal(
    notificationLink({ targetType: 'run', targetId: 'run-1', appId: 'shop' }),
    '/runs/run-1',
  )
  const url = new URL(
    notificationLink({
      targetType: 'batch',
      targetId: 'batch/a',
      appId: 'shop',
    }),
    'http://localhost',
  )
  assert.equal(url.searchParams.get('section'), 'changes')
  assert.equal(url.searchParams.get('focus'), 'batch/a')
  assert.equal(url.searchParams.get('tab'), 'delivery')
})
test('all-environment view includes each baseline and excludes other lanes', () => {
  const rows = [
    { serviceId: 'svc', envId: 'test', laneId: 'preview', id: 'wrong' },
    { serviceId: 'svc', envId: 'test', laneId: 'default', id: 'test' },
    { serviceId: 'svc', envId: 'prod', laneId: 'default', id: 'prod' },
    { serviceId: 'other', envId: 'test', laneId: 'default', id: 'other' },
  ]
  assert.deepEqual(
    baselineRunsFor('svc', rows).map((r) => r.id),
    ['test', 'prod'],
  )
  assert.deepEqual(
    baselineRunsFor('svc', rows, 'prod').map((r) => r.id),
    ['prod'],
  )
  assert.deepEqual(baselineRunsFor('svc', rows, 'missing'), [])
})
test('global capabilities stay discoverable without duplicate navigation targets', () => {
  const targets = navigation.flatMap((g) => g.items.map((i) => i.to))
  assert.equal(new Set(targets).size, targets.length)
  for (const target of [
    '/products',
    '/delivery',
    '/models',
    '/playground',
    '/config-center',
    '/api-keys',
  ])
    assert.ok(targets.includes(target))
})

test('light and dark theme text and status colors meet AA contrast', async () => {
  const { readFile } = await import('node:fs/promises')
  const css = await readFile(
    new URL('../src/styles/theme.css', import.meta.url),
    'utf8',
  )
  const luminance = (hex) => {
    const channels = hex
      .match(/[a-f0-9]{2}/gi)
      .map((v) => parseInt(v, 16) / 255)
      .map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
    return channels.reduce(
      (total, v, i) => total + v * [0.2126, 0.7152, 0.0722][i],
      0,
    )
  }
  const ratio = (a, b) => {
    const [low, high] = [luminance(a), luminance(b)].sort((x, y) => x - y)
    return (high + 0.05) / (low + 0.05)
  }
  for (const theme of [':root', 'html:not(.dark)']) {
    const block = css.slice(css.indexOf(theme + ' {')).split('}')[0]
    const tokens = Object.fromEntries(
      [...block.matchAll(/--([\w-]+):\s*(#[a-f0-9]{6})/gi)].map((m) => [
        m[1],
        m[2],
      ]),
    )
    for (const text of ['text', 'text-dim', 'text-faint'])
      for (const bg of ['bg', 'surface', 'surface-2'])
        assert.ok(
          ratio(tokens[text], tokens[bg]) >= 4.5,
          `${theme}: ${text} on ${bg}`,
        )
    for (const status of ['success', 'warning', 'danger'])
      assert.ok(
        ratio(tokens[status], tokens[status + '-soft']) >= 4.5,
        `${theme}: ${status}`,
      )
    assert.ok(
      ratio('#ffffff', tokens['brand-solid']) >= 4.5,
      `${theme}: primary button`,
    )
    assert.ok(
      ratio(tokens['border-strong'], tokens['surface']) >= 3,
      `${theme}: input border`,
    )
  }
})

test('command search ranks an exact destination ahead of broad parent keywords', () => {
  const results = searchEntries(
    [
      { label: 'AI 开发', keywords: '智能体 知识库 工具', to: '/ai' },
      { label: '知识库', to: '/ai/knowledgebases' },
    ],
    '知识库',
  )
  assert.equal(results[0].to, '/ai/knowledgebases')
})

// HTTP fetch 不会因为 403/405/500 而 reject，写操作必须显式拒绝这些结果。
test('write commands reject permission, method, gateway and HTML failures', async () => {
  const { requireOK } = await import('../src/api/response.ts')
  for (const status of [403, 405, 409, 500]) {
    await assert.rejects(
      requireOK(
        new Response(JSON.stringify({ error: '操作被拒绝' }), { status }),
      ),
      /操作被拒绝/,
    )
  }
  await assert.rejects(
    requireOK(new Response('<html>Bad gateway</html>', { status: 502 })),
    /HTTP 502/,
  )
  await requireOK(new Response(null, { status: 204 }))
})

test('data service commands keep PUT scale/upgrade and POST lifecycle contracts', async () => {
  const { dataServiceCommand } = await import('../src/domain/dataservice.ts')
  assert.equal(
    dataServiceCommand('ds/a', 'scale', { replicas: 2 }).path,
    '/api/dataservices/ds%2Fa/scale',
  )
  assert.equal(dataServiceCommand('ds', 'scale').options.method, 'PUT')
  assert.equal(dataServiceCommand('ds', 'upgrade').options.method, 'PUT')
  assert.equal(dataServiceCommand('ds', 'restart').options.method, 'POST')
  assert.equal(dataServiceCommand('ds', 'delete').options.method, 'DELETE')
})

test('observation scopes never broaden an empty environment or mix lanes', async () => {
  const { observationTargets } = await import('../src/domain/observability.ts')
  const scope = { app: '', ds: '', workload: '', env: 'test', lane: '' }
  const workloads = [
    { id: 'test-main', appId: 'app', envId: 'test', laneId: 'default' },
    { id: 'test-preview', appId: 'app', envId: 'test', laneId: 'preview' },
    { id: 'prod-main', appId: 'app', envId: 'prod', laneId: 'default' },
  ]
  const services = [
    { id: 'db-test', name: 'db', envId: 'test' },
    { id: 'db-prod', name: 'db', envId: 'prod' },
  ]
  assert.deepEqual(
    observationTargets(scope, workloads, services).map((t) => t.targetId),
    ['test-main', 'test-preview', 'db-test'],
  )
  assert.deepEqual(
    observationTargets({ ...scope, lane: 'preview' }, workloads, services),
    [{ targetType: 'workload', targetId: 'test-preview' }],
  )
  assert.deepEqual(
    observationTargets({ ...scope, env: 'missing' }, workloads, services),
    [],
  )
  assert.deepEqual(
    observationTargets({ ...scope, ds: 'db-prod' }, workloads, services),
    [],
  )
})

test('chart samples preserve zero, discard invalid timestamps and keep units consistent', async () => {
  const { finitePoints, metricScale } =
    await import('../src/domain/observability.ts')
  assert.deepEqual(
    finitePoints([
      { ts: 'bad', value: 1 },
      { ts: '2026-10-17T01:00:00Z', value: 0 },
      { ts: '2026-10-17T01:01:00Z', value: NaN },
    ]),
    [{ ts: '2026-10-17T01:00:00Z', value: 0 }],
  )
  assert.deepEqual(metricScale('MiB', 2048), { factor: 1 / 1024, unit: 'GiB' })
  assert.deepEqual(metricScale('cores', 0.005), { factor: 1000, unit: 'mCPU' })
  assert.deepEqual(metricScale('ms', 3000), { factor: 1 / 1000, unit: 's' })
})

test('delivery document links stay in the new console and preserve exact image/repo focus', async () => {
  const links = await import('../src/composables/useDevopsLinks.ts')
  assert.equal(links.releaseLink('r'), '/releases/r')
  assert.equal(links.batchLink('b'), '/batches/b')
  assert.equal(
    new URL(
      links.repoLink('app', 'repo/a'),
      'http://localhost',
    ).searchParams.get('repo'),
    'repo/a',
  )
  assert.equal(
    new URL(
      links.imageLink('app', 'img/a'),
      'http://localhost',
    ).searchParams.get('section'),
    'builds',
  )
})

test('trace waterfall keeps every span, parent depth and peer/error diagnostics', async () => {
  const { buildSpanTree, flattenSpanTree, spanServiceLabel, traceErrSummary } =
    await import('../src/composables/useSpanTree.ts')
  const spans = [
    {
      id: 'child',
      parentId: 'root',
      service: 'api',
      kind: 'client',
      peer: 'database',
      operation: 'query',
      startMs: 10,
      durationMs: 20,
      isError: true,
      errorMessage: 'timeout',
    },
    {
      id: 'root',
      service: 'api',
      operation: 'request',
      startMs: 0,
      durationMs: 100,
    },
    {
      id: 'orphan',
      parentId: 'missing',
      service: 'queue',
      operation: 'publish',
      startMs: 40,
      durationMs: 10,
    },
  ]
  const rows = flattenSpanTree(buildSpanTree(spans))
  assert.equal(rows.length, 3)
  assert.equal(rows.find((r) => r.span.id === 'child').depth, 1)
  assert.equal(rows.find((r) => r.span.id === 'orphan').depth, 0)
  assert.equal(spanServiceLabel(spans[0]), 'api → database')
  assert.equal(traceErrSummary({ spans }), 'timeout')
})
