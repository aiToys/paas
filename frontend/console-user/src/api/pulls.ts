// PR 评审（Code Review）API。真源 Gitea，平台不落库。
// 模式照抄 change.ts：fetchAuth + {data:T} 解包。
import { fetchAuth } from '@/api'

export interface PullRequest {
  number: number; title: string; body: string; state: 'open' | 'closed'
  head: string; base: string; user: string
  createdAt: string; merged: boolean; mergeable: boolean
}
export interface PullDetail { pr: PullRequest; diff: string; truncated: boolean }
export interface GlobalPull { repoId: string; repoName: string; appId: string; pr: PullRequest }

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json().catch(() => null)
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listPulls = (appId: string, repoId: string, state = 'open') =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls?state=${state}`).then(r => unwrap<PullRequest[]>(r))
export const getPullDetail = (appId: string, repoId: string, number: number) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}`).then(r => unwrap<PullDetail>(r))
export const reviewPull = (appId: string, repoId: string, number: number, doAction: string, body: string) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}/reviews`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ do: doAction, body }),
  }).then(async r => { if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j?.error || `HTTP ${r.status}`) } })
export const mergePull = (appId: string, repoId: string, number: number) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}/merge`, { method: 'POST' })
    .then(async r => { if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j?.error || `HTTP ${r.status}`) } })
export const listGlobalPulls = () =>
  fetchAuth('/api/pulls').then(r => unwrap<GlobalPull[]>(r))
