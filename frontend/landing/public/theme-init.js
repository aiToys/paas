// 同源经典脚本在首屏渲染前执行，兼容平台禁止内联脚本的 CSP。
// 用户手选优先于系统偏好；与 SiteNav 共用 paas-theme 存储键。
(function () {
  var theme = null
  try { theme = localStorage.getItem('paas-theme') } catch (e) {}
  if (theme !== 'light' && theme !== 'dark') {
    theme = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  }
  document.documentElement.dataset.theme = theme
  var meta = document.querySelector('meta[name="theme-color"]')
  if (meta) meta.setAttribute('content', theme === 'light' ? '#f6f7fb' : '#0a0e1a')
})()
