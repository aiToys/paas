// 流水线模板管理 API（平台级，super_admin）。
// 对接 core /api/admin/pipeline-templates（builtin 模板拒改删，后端 store 层保护）。
// 响应解包由 http interceptor 处理：list 端点 {data:[...]} → 数组；item 端点对象直接返回。
import { api } from '@/lib/http/client'

const BASE = '/api/admin/pipeline-templates'

// stage 类型（与后端 pipeline.StageBuild/Deploy/... 对齐）。
export const STAGE_TYPES = [
  { label: 'build（构建镜像）', value: 'build' },
  { label: 'deploy（部署到环境×泳道）', value: 'deploy' },
  { label: 'test（smoke 探活/manual 确认）', value: 'test' },
  { label: 'approve（人工审批门禁）', value: 'approve' },
  { label: 'release（打版本里程碑）', value: 'release' },
  { label: 'promote（提升到下一阶序环境）', value: 'promote' },
  { label: 'canary（金丝雀并行验证）', value: 'canary' },
  { label: 'baseline（合并主干）', value: 'baseline' },
]

// 流水线分类。
export const PIPELINE_KINDS = [
  { label: 'CI（测试联调）', value: 'ci' },
  { label: 'CD（上线发布）', value: 'cd' },
  { label: '自定义', value: 'custom' },
]

export interface StageDef {
  name: string
  type: string
  params?: Record<string, unknown>
}

export interface PipelineTemplate {
  id: string
  name: string
  kind: string
  description?: string
  stages: StageDef[]
  params?: unknown[]
  builtin?: boolean
  tenantId?: string
}

export const fetchTemplateList = () => api.get<PipelineTemplate[]>(BASE)
export const createTemplate = (data: PipelineTemplate) => api.post<PipelineTemplate>(BASE, data)
export const updateTemplate = (id: string, data: PipelineTemplate) =>
  api.put<PipelineTemplate>(`${BASE}/${id}`, data)
export const deleteTemplate = (id: string) => api.del(`${BASE}/${id}`)

// 假分页适配（模板数量少，前端 keyword 过滤 + 分页，适配 useCrud）。
export interface TemplateSearchRequest {
  keyword?: string
  page: number
  size: number
}
export interface TemplateSearchResponse {
  records: PipelineTemplate[]
  total: number
  current: number
  size: number
}
export const fetchTemplateListPage = async (
  params: TemplateSearchRequest
): Promise<TemplateSearchResponse> => {
  const list = await fetchTemplateList()
  let mapped = list ?? []
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (t) =>
        t.id.toLowerCase().includes(kw) ||
        t.name.toLowerCase().includes(kw) ||
        t.kind.toLowerCase().includes(kw)
    )
  }
  const total = mapped.length
  const start = (params.page - 1) * params.size
  const records = mapped.slice(start, start + params.size)
  return { records, total, current: params.page, size: params.size }
}
