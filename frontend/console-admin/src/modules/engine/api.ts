// 引擎目录 API（平台级，super_admin 由 adminGuard 兜底）。
// 对接 core /api/admin/engines。响应解包由 http interceptor 处理（list {data:[...]}→数组，item 对象）。
import { api } from '@/lib/http/client'

// 与后端 dataservice.Kind* 对齐
export const KINDS = [
  { label: '数据库', value: 'db' },
  { label: '缓存', value: 'cache' },
  { label: '消息队列', value: 'mq' },
  { label: '对象存储', value: 'storage' },
  { label: '向量数据库', value: 'vector' },
  { label: '搜索引擎', value: 'search' },
]

// 与后端 EngineMode* 对齐
export const ENGINE_MODES = [
  { label: '平台托管（拉起独占实例）', value: 'managed' },
  { label: '共享集群（admin 配连接，多租户复用）', value: 'external-shared' },
  { label: '独占外部（用户自填连接）', value: 'external-dedicated' },
]

export interface Engine {
  id: string
  kind: string
  engine: string
  label: string
  description?: string
  mode: string
  enabled: boolean
  connection?: Record<string, string>
  order?: number
}

const BASE = '/api/admin/engines'

export const fetchEngineList = () => api.get<Engine[]>(BASE)
export const createEngine = (data: Engine) => api.post<Engine>(BASE, data)
export const updateEngine = (id: string, data: Engine) => api.put<Engine>(`${BASE}/${id}`, data)
export const deleteEngine = (id: string) => api.del(`${BASE}/${id}`)

// 假分页适配（引擎数量少，前端 keyword 过滤 + 分页，适配 useCrud）。
export interface EngineSearchRequest {
  keyword?: string
  page: number
  size: number
}
export interface EngineSearchResponse {
  records: Engine[]
  total: number
  current: number
  size: number
}
export const fetchEngineListPage = async (
  params: EngineSearchRequest
): Promise<EngineSearchResponse> => {
  const list = await fetchEngineList()
  let mapped = list ?? []
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (e) =>
        e.id.toLowerCase().includes(kw) ||
        e.label.toLowerCase().includes(kw) ||
        e.kind.toLowerCase().includes(kw) ||
        e.engine.toLowerCase().includes(kw)
    )
  }
  return {
    records: mapped,
    total: mapped.length,
    current: params.page,
    size: params.size,
  }
}
