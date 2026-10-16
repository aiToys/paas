import { test } from 'node:test'
import assert from 'node:assert/strict'
import { unwrapData, asList } from '../src/api/response.ts'
import { notificationLink, navigation, searchEntries } from '../src/navigation/index.ts'
import { baselineRunsFor } from '../src/domain/runtime.ts'

test('Go nil slice renders as an empty list, not the response envelope', () => {
  assert.deepEqual(asList(unwrapData({ data: null })), [])
  assert.deepEqual(asList(unwrapData({ data: [] })), [])
  assert.deepEqual(asList(unwrapData({ data: [{ id: 'one' }] })), [{ id: 'one' }])
  assert.throws(() => asList({ error: 'not a list' }), /列表响应格式错误/)
  assert.equal(unwrapData({ data: null }), null)
})
test('a failed build links directly to its logs and batches keep exact focus', () => {
  assert.equal(notificationLink({ targetType: 'build', targetId: 'build-1', appId: 'shop' }), '/builds/build-1')
  assert.equal(notificationLink({ targetType: 'run', targetId: 'run-1', appId: 'shop' }), '/runs/run-1')
  const url = new URL(notificationLink({ targetType: 'batch', targetId: 'batch/a', appId: 'shop' }), 'http://localhost')
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
  assert.deepEqual(baselineRunsFor('svc', rows).map((r) => r.id), ['test', 'prod'])
  assert.deepEqual(baselineRunsFor('svc', rows, 'prod').map((r) => r.id), ['prod'])
  assert.deepEqual(baselineRunsFor('svc', rows, 'missing'), [])
})
test('global capabilities stay discoverable without duplicate navigation targets', () => {
  const targets = navigation.flatMap((g) => g.items.map((i) => i.to))
  assert.equal(new Set(targets).size, targets.length)
  for (const target of ['/products', '/delivery', '/models', '/playground', '/config-center', '/api-keys']) assert.ok(targets.includes(target))
})

test('light and dark theme text and status colors meet AA contrast', async () => {
  const { readFile } = await import('node:fs/promises')
  const css = await readFile(new URL('../src/styles/theme.css', import.meta.url), 'utf8')
  const luminance = (hex) => {
    const channels = hex.match(/[a-f0-9]{2}/gi).map((v) => parseInt(v, 16) / 255).map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
    return channels.reduce((total, v, i) => total + v * [0.2126, 0.7152, 0.0722][i], 0)
  }
  const ratio = (a, b) => { const [low, high] = [luminance(a), luminance(b)].sort((x, y) => x - y); return (high + 0.05) / (low + 0.05) }
  for (const theme of [':root', 'html:not(.dark)']) {
    const block = css.slice(css.indexOf(theme + ' {')).split('}')[0]
    const tokens = Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[a-f0-9]{6})/gi)].map((m) => [m[1], m[2]]))
    for (const text of ['text', 'text-dim', 'text-faint']) for (const bg of ['bg', 'surface', 'surface-2']) assert.ok(ratio(tokens[text], tokens[bg]) >= 4.5, `${theme}: ${text} on ${bg}`)
    for (const status of ['success', 'warning', 'danger']) assert.ok(ratio(tokens[status], tokens[status + '-soft']) >= 4.5, `${theme}: ${status}`)
    assert.ok(ratio('#ffffff', tokens['brand-solid']) >= 4.5, `${theme}: primary button`)
    assert.ok(ratio(tokens['border-strong'], tokens['surface']) >= 3, `${theme}: input border`)
  }
})


test('command search ranks an exact destination ahead of broad parent keywords', () => {
  const results = searchEntries([
    { label: 'AI 开发', keywords: '智能体 知识库 工具', to: '/ai' },
    { label: '知识库', to: '/ai/knowledgebases' },
  ], '知识库')
  assert.equal(results[0].to, '/ai/knowledgebases')
})
