import { describe, it, expect, beforeAll } from 'vitest'
import { api, http } from '@/lib/http/client'
import httpAdapter from 'axios/unsafe/adapters/http.js'

// jsdom 下 axios 默认走 XHR adapter，MSW node 无法拦截；
// 强制使用 Node http adapter，使测试中的真实 HTTP 请求能被 MSW server 处理
beforeAll(() => {
  http.defaults.adapter = httpAdapter as never
})

describe('导出 CSV 契约 (MSW)', () => {
  it('角色 导出返回 CSV 文本（含表头+数据行）', async () => {
    const csv = await api.get<string>('/api/role/export')
    expect(typeof csv).toBe('string')
    // 不再是占位 'export success' 或 data URI
    expect(csv).not.toBe('export success')
    expect(csv).not.toContain('data:text/csv')
    // CSV 至少含一行表头（含逗号或单字段）+ 至少一行数据
    const lines = csv.split('\n')
    expect(lines.length).toBeGreaterThanOrEqual(2)
  })
})
