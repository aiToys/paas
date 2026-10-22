import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

test('首屏主题通过同源阻塞脚本恢复，兼容平台禁止内联脚本的 CSP', () => {
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
  const bootstrap = html.match(/<script\b[^>]*src="\/theme-init\.js"[^>]*><\/script>/)?.[0]
  assert.ok(bootstrap, '主题初始化必须使用同源外部脚本，内联脚本会被平台 CSP 拦截')
  assert.doesNotMatch(bootstrap, /\s(?:async|defer|type)(?:\s|=|>)/)
  assert.ok(html.indexOf(bootstrap) < html.indexOf('</head>'), '必须在首屏渲染前恢复主题')
  assert.doesNotMatch(html, /<script\b[^>]*>\s*[^<\s]/, '首页不应依赖内联脚本')
})

function loadPage({ saved = null, systemLight = false, storageBlocked = false, hasMeta = true } = {}) {
  const document = {
    documentElement: { dataset: {} },
    querySelector: () => hasMeta ? { setAttribute: (_, value) => { document.themeColor = value } } : null,
  }
  const localStorage = {
    getItem(key) {
      assert.equal(key, 'paas-theme')
      if (storageBlocked) throw new Error('storage blocked')
      return saved
    },
  }
  const script = readFileSync(new URL('../public/theme-init.js', import.meta.url), 'utf8')
  runInNewContext(script, { document, localStorage, window: { matchMedia: () => ({ matches: systemLight }) } })
  return document
}

for (const saved of ['light', 'dark']) {
  test(`刷新页面保留已选择的 ${saved}，优先于相反的系统主题`, () => {
    for (let reload = 0; reload < 2; reload++) {
      const page = loadPage({ saved, systemLight: saved === 'dark' })
      assert.equal(page.documentElement.dataset.theme, saved)
      assert.equal(page.themeColor, saved === 'light' ? '#f6f7fb' : '#0a0e1a')
    }
  })
}

for (const systemLight of [true, false]) {
  test(`未保存、保存值无效或存储不可用时采用系统${systemLight ? '亮' : '暗'}主题`, () => {
    for (const options of [{}, { saved: 'invalid' }, { storageBlocked: true }]) {
      assert.equal(loadPage({ ...options, systemLight }).documentElement.dataset.theme, systemLight ? 'light' : 'dark')
    }
  })
}

test('没有 theme-color 标签时仍恢复主题', () => {
  assert.equal(loadPage({ saved: 'light', hasMeta: false }).documentElement.dataset.theme, 'light')
})
