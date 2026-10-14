import { http } from 'msw'
import { ok } from './_utils'

// 平台级 Secret 选项（模型通道 CredentialRef 下拉用，真实后端 /api/security/secrets）
export const securityHandlers = [
  http.get('/api/security/secrets', () =>
    ok([
      { id: 'sec-platform-airouter', name: 'airouter 网关凭证', scope: 'platform' },
      { id: 'sec-platform-demo', name: '演示平台密钥', scope: 'platform' },
      { id: 'sec-tenant-demo', name: '租户级密钥', scope: 'tenant' }
    ])
  )
]
