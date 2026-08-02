import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchAuth, fetchJSON } from '@/api'

// 会话 store：登录态走 httpOnly cookie，前端只缓存 profile（不存 token）。
// 会话探测复用 /api/auth/users/me；401 由 api.ts 自动 refresh，refresh 失败触发 session-expired。

export interface UserProfile {
  id: string
  username: string
  roles: string[]
  permissions: string[]
}

// 演示账号快切（dev/demo）：预设账号一键登录，本质是调 /api/auth/sessions 拿 cookie。
// 生产关 demo（PAAS_DISABLE_DEMO_SEED=true）后这些账号不存在，快切会失败提示。
export interface DemoAccount {
  username: string
  password: string
  label: string
  role: string
}

export const useSessionStore = defineStore('session', () => {
  const profile = ref<UserProfile | null>(null)
  const loaded = ref(false)

  async function loadProfile(): Promise<boolean> {
    try {
      profile.value = await fetchJSON<UserProfile>('/api/auth/users/me')
      loaded.value = true
      return true
    } catch {
      profile.value = null
      loaded.value = true
      return false
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const resp = await fetchAuth('/api/auth/sessions', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    })
    if (!resp.ok) {
      const msg = (await resp.json().catch(() => ({}))).error || '登录失败'
      throw new Error(msg)
    }
    await loadProfile()
  }

  async function logout(): Promise<void> {
    await fetchAuth('/api/auth/sessions', { method: 'DELETE' })
    profile.value = null
  }

  const DEMO_ACCOUNTS: DemoAccount[] = [
    { username: 'acme-admin', password: '123456', label: 'Acme · 管理员', role: 'tenant-admin' },
    { username: 'acme-dev', password: '123456', label: 'Acme · 开发者', role: 'developer' },
    { username: 'globex-admin', password: '123456', label: 'Globex · 管理员', role: 'tenant-admin' },
  ]

  return { profile, loaded, loadProfile, login, logout, DEMO_ACCOUNTS }
})
