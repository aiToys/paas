// agentrun 观测 API（经 /api/agentrun 代理，用户 JWT 由 cookie 自动携带）。
// 响应为 agentrun 裸 JSON（非 {data:T} 包裹），fetchJSON 智能解包已兼容。
// 503=平台未配置 PAAS_AGENTRUN_URL（页面显示引导态）；401=会话需要登录。
import { fetchAuth, fetchJSON, fetchList } from '@/api'

export interface AgentRunItem {
  name: string
  runtime?: string
  mode?: string
  stage?: string
  phase?: string // Pending|Running|Succeeded|Failed
  verdict?: string
  priority?: number
  tokens?: number
  branch?: string
  message?: string
  request?: string
  createdAt?: string
  finishedAt?: string
}

export interface AgentArtifact {
  key: string
  size?: number
  modifiedAt?: string
}

export const listRuns = (): Promise<AgentRunItem[]> => fetchList<AgentRunItem>('/api/agentrun/runs')

export const getRun = (name: string): Promise<AgentRunItem> =>
  fetchJSON<AgentRunItem>(`/api/agentrun/runs/${encodeURIComponent(name)}`)

export const cancelRun = (name: string): Promise<void> =>
  fetchAuth(`/api/agentrun/runs/${encodeURIComponent(name)}`, { method: 'DELETE' }).then((r) => {
    if (!r.ok) return r.json().then((j: { error?: string }) => { throw new Error(j.error || '取消失败') })
  })

export const listArtifacts = (name: string): Promise<AgentArtifact[]> =>
  fetchList<AgentArtifact>(`/api/agentrun/runs/${encodeURIComponent(name)}/artifacts`)

export const getArtifact = (name: string, key: string): Promise<string> =>
  // key 含斜杠（console 0.14.2 多级通配支持原样路径）：按段编码保留分隔符
  fetchJSON<string>(`/api/agentrun/runs/${encodeURIComponent(name)}/artifacts/${key.split('/').map(encodeURIComponent).join('/')}`)

export interface AgentPipelineDetail {
  name: string
  phase?: string
  currentStage?: string
  stages?: { name: string; phase?: string; agentRun?: string; summary?: string; verdict?: string }[]
  createdAt?: string
}

// 委派 pipeline 详情（Change.AgentRunName 存 pipeline 名——委派走两阶段 analyze→implement）
export const getPipeline = (name: string): Promise<AgentPipelineDetail> =>
  fetchJSON<AgentPipelineDetail>(`/api/agentrun/pipelines/${encodeURIComponent(name)}`)

export const phaseTagType = (phase?: string): 'success' | 'warning' | 'danger' | 'info' => {
  switch (phase) {
    case 'Succeeded': return 'success'
    case 'Failed': return 'danger'
    case 'Running': return 'warning'
    default: return 'info'
  }
}

// getRunLogs 拉取 run 工作日志（AI 执行过程）。对方语义：运行中实时日志；Pod 按 TTL 回收后
// 返回 JSON {error}（成功 run 回收快、失败 run 保留 30 分钟）——统一返回文本，JSON error
// 原样透出给上层按提示展示（fetchAuth 非 2xx 会 throw，这里吞掉拼进文本防中断页面）。
export const getRunLogs = (name: string): Promise<string> =>
  fetchAuth(`/api/agentrun/runs/${encodeURIComponent(name)}/logs`).then(async (r) => {
    const t = await r.text()
    try {
      const j = JSON.parse(t) as { error?: string }
      if (j && typeof j.error === 'string') return `ℹ️ ${j.error}`
    } catch { /* 纯文本日志 */ }
    return t || '（空日志）'
  }).catch((e: unknown) => `加载失败: ${e instanceof Error ? e.message : String(e)}`)
