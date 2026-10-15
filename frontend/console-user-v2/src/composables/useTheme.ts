// 亮/暗主题切换：沿用 Element Plus 的 `html.dark` 开关（dark/css-vars 仅 `.dark` 作用域生效）。
// theme.css 的 `html:not(.dark)` 块提供亮色自有变量覆盖；移除 dark 即整体切亮，与 EP 同步。
//
// 默认深色（index.html class="dark" 防 FOUC）；用户切换后持久化到 localStorage。
import { ref } from 'vue'

const THEME_KEY = 'paas.theme'
export type Theme = 'dark' | 'light'

function stored(): Theme {
  return localStorage.getItem(THEME_KEY) === 'light' ? 'light' : 'dark'
}

function apply(theme: Theme) {
  const el = document.documentElement
  el.classList.toggle('dark', theme === 'dark')
  el.classList.toggle('light', theme === 'light')
}

// 模块加载即按持久化偏好校正（修正 index.html 默认 dark 与用户选择的偏差）
const current = ref<Theme>(stored())
apply(current.value)

export function useTheme() {
  function toggle() {
    current.value = current.value === 'dark' ? 'light' : 'dark'
    apply(current.value)
    localStorage.setItem(THEME_KEY, current.value)
  }
  return { theme: current, toggle }
}
