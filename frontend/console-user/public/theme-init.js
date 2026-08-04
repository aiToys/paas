// 防 FOUC：首帧即按持久化偏好应用主题，避免默认深色闪一下。
// 外置为独立 JS（非内联），符合 CSP script-src 'self'（禁止 inline）。
// 默认深色（html class="dark"）；用户偏好 light 时立即切回。
;(function () {
  try {
    if (localStorage.getItem('paas.theme') === 'light') {
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
    }
  } catch (e) {}
})()
