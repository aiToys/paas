// DevOps 单据链接单一真源：id → 前端路由。
// 全部页面（运行视图/应用 tab/DevOps 中心/各详情页）统一 import，加单据类型只改此处。
// repoId/imageId 可选：带上时目标页自动定位（仓库 tab 自动开 RepoBrowser / 镜像 tab 自动展开该镜像）。
export function buildLink(id: string): string {
  return `/devops/builds/${id}`
}
export function releaseLink(id: string): string {
  return `/devops/releases/${id}`
}
export function runLink(id: string): string {
  return `/devops/runs/${id}`
}
export function changeLink(id: string): string {
  return `/devops/changes/${id}`
}
export function batchLink(id: string): string {
  return `/devops/batches/${id}`
}
// 应用镜像 tab（自动展开 imageId 对应行）
export function imageLink(appId: string, imageId?: string): string {
  return imageId
    ? `/applications/${appId}?tab=images&image=${encodeURIComponent(imageId)}`
    : `/applications/${appId}?tab=images`
}
// 应用代码仓库 tab（repoId 非空且为内置仓库时自动打开 RepoBrowser）
export function repoLink(appId: string, repoId?: string): string {
  return repoId
    ? `/applications/${appId}?tab=repositories&repo=${encodeURIComponent(repoId)}`
    : `/applications/${appId}?tab=repositories`
}
// 应用部署 tab（工作负载运行态）
export function deployLink(appId: string): string {
  return `/applications/${appId}?tab=deploy`
}
