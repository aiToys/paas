// 应用服务模型 API（ServiceEntity，Phase 1）：服务是应用的组成单元，驱动构建/部署。
// 模式照抄 workload.ts：fetchJSON 自动解包 {data:T}。
import { fetchJSON } from '@/api'

export type ServiceType = 'web' | 'backend' | 'agent' | 'static' | 'cron'

export interface ServiceEntity {
  id: string
  appId: string
  name: string
  type: ServiceType
  repoId?: string
  repoPath?: string
  port?: number
  replicas?: number
  buildArgs?: Record<string, string>
  env?: Record<string, string>
  modelRef?: string
  tools?: string[]
  schedule?: string
  createdAt?: string
}

export const listServices = (appId: string) =>
  fetchJSON<ServiceEntity[]>(`/api/applications/${appId}/services`)

export const getService = (appId: string, id: string) =>
  fetchJSON<ServiceEntity>(`/api/applications/${appId}/services/${id}`)

export const createService = (appId: string, body: Partial<ServiceEntity>) =>
  fetchJSON<ServiceEntity>(`/api/applications/${appId}/services`, {
    method: 'POST',
    body: JSON.stringify(body),
  })

export const updateService = (appId: string, id: string, body: Partial<ServiceEntity>) =>
  fetchJSON<ServiceEntity>(`/api/applications/${appId}/services/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })

export const deleteService = (appId: string, id: string) =>
  fetchJSON<void>(`/api/applications/${appId}/services/${id}`, { method: 'DELETE' })
