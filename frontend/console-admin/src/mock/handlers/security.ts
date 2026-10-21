import { http } from 'msw'
import { ok } from './_utils'

// 平台级 Secret 选项（模型通道 CredentialRef 下拉用，真实后端 /api/security/secrets）。
// POST 同端点 = 供应商/通道表单内嵌的平台凭证创建（spec 2026-10-19 §8.3，mock 内存态）。
const created = [{ id: 'sec-platform-airouter', name: 'airouter 网关凭证', scope: 'platform' },
  { id: 'sec-platform-demo', name: '演示平台密钥', scope: 'platform' },
  { id: 'sec-tenant-demo', name: '租户级密钥', scope: 'tenant' }]

export const securityHandlers = [
  http.get('/api/security/secrets', () => ok(created)),
  http.post('/api/security/secrets', async ({ request }) => {
    const body = (await request.json()) as { name?: string; id?: string }
    const id = body.id || `sec-platform-${Date.now()}`
    created.unshift({ id, name: body.name ?? id, scope: 'platform' })
    return ok({ id, name: body.name ?? id, scope: 'platform', value: '******' })
  })
]
