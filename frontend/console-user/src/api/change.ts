// 变更管理 API（Change/IntegrationBatch，火车发车模型）。
// 模式照抄 pipeline.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface Change {
  id: string; appId: string; repoId: string; title: string; type: 'feat' | 'hotfix'
  branch: string; branchCreated: boolean; baseBranch: string
  status: 'open' | 'integrated' | 'tested' | 'released' | 'reverted' | 'abandoned'
  batchId: string; conflictWith: string; createdBy?: string; createdAt: string; updatedAt?: string
}
export interface IntegrationBatch {
  id: string; appId: string; repoId: string; title: string; branch: string
  status: 'collecting' | 'building' | 'conflict' | 'testing' | 'tested' | 'releasing' | 'released' | 'failed' | 'abandoned'
  changeIds: string[]; pipelineId: string; runId: string; releaseIds: string[]
  createdBy?: string; createdAt: string; finishedAt?: string
}
export interface ChangeInput { title: string; type: 'feat' | 'hotfix'; branch: string; baseBranch?: string; createBranch: boolean }

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listChanges = (appId: string, status = '') =>
  fetchAuth(`/api/applications/${appId}/changes${status ? `?status=${status}` : ''}`).then(r => unwrap<Change[]>(r))
export const createChange = (appId: string, body: ChangeInput) =>
  fetchAuth(`/api/applications/${appId}/changes`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then(r => unwrap<Change>(r))
export const abandonChange = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/changes/${id}`, { method: 'DELETE' })
export const listBatches = (appId: string, status = '') =>
  fetchAuth(`/api/applications/${appId}/batches${status ? `?status=${status}` : ''}`).then(r => unwrap<IntegrationBatch[]>(r))
export const getBatch = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${id}`).then(r => unwrap<IntegrationBatch>(r))
export const createBatch = (appId: string, body: { title: string; branch: string }) =>
  fetchAuth(`/api/applications/${appId}/batches`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then(r => unwrap<IntegrationBatch>(r))
export const abandonBatch = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${id}`, { method: 'DELETE' })
export const addChangeToBatch = (appId: string, bid: string, changeId: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/changes`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ changeId }) })
export const removeChangeFromBatch = (appId: string, bid: string, cid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/changes/${cid}`, { method: 'DELETE' })
export const integrateBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/integrate`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))
export const approveBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/approve`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))
export const releaseBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/release`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))

// ---------- 跨应用（DevOps 中心档案室 + 详情页） ----------

export interface Notification {
  id: string; type: string; severity: 'error' | 'warning' | 'info'
  title: string; appId: string; targetType: 'batch' | 'run' | 'change'; targetId: string; at: string
}

export const listAllChanges = (appId = '', status = '') =>
  fetchAuth(`/api/changes${appId || status ? `?${new URLSearchParams({ ...(appId && { appId }), ...(status && { status }) }).toString()}` : ''}`).then(r => unwrap<Change[]>(r))
export const getChange = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/changes/${id}`).then(r => unwrap<Change>(r))
export const listAllBatches = (appId = '', status = '') =>
  fetchAuth(`/api/batches${appId || status ? `?${new URLSearchParams({ ...(appId && { appId }), ...(status && { status }) }).toString()}` : ''}`).then(r => unwrap<IntegrationBatch[]>(r))
export const listNotifications = () =>
  fetchAuth('/api/notifications').then(r => unwrap<Notification[]>(r))
