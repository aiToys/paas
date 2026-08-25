// Trace span 树形构建 + 瀑布图几何 + 关键元数据提取（observability trace 展示公共逻辑）。
//
// 抽自 Observability.vue / AppObservability.vue 两处重复的 span 渲染逻辑，单一真源（DRY）。
// 后端 Span 已含 parentId（树形构建）+ startMs（相对 trace 起点偏移 ms）+ durationMs（瀑布条长度），
// 此 composable 把 flat spans[] 转为深度优先有序的带 depth 数组，驱动树形缩进 + 时间轴瀑布。

/** Span：链路追踪单 span（与后端 observability.Span 对齐，parentId 空表根 span）。 */
export interface Span {
  id: string
  parentId?: string
  operation: string
  service: string
  kind?: string // OTel span.kind：server（入口）/ client（出站调用）/ producer / consumer / internal
  peer?: string // client span 的真实对端（peer.service > db.system > server.address）
  startMs: number
  durationMs: number
  tags?: Record<string, string>
  isError?: boolean
  errorType?: string
  errorMessage?: string
}

/** Trace：一条完整链路（含 spans 列表）。 */
export interface Trace {
  id: string
  appId: string
  operation: string
  service?: string
  status: string
  durationMs: number
  startedAt: string
  spans: Span[]
}

/** SpanNode：buildSpanTree 产出的树节点（depth 驱动左缩进）。 */
export interface SpanNode {
  span: Span
  depth: number
  children: SpanNode[]
}

/** FlattenedSpan：flattenSpanTree 产物，供 v-for 渲染（缩进 = depth * 20px）。 */
export interface FlattenedSpan {
  span: Span
  depth: number
}

/**
 * buildSpanTree：parentId 构建森林 + 按开始时间稳定排序。
 *
 * 鲁棒性：
 *   - parentId 指向不存在的 span（Jaeger 偶发截断）→ 当根处理，不丢 span。
 *   - 多根（无 parentId 的 span >1，如顶层并行入口）→ 各自作根。
 *   - 循环引用 → visited set 防死循环。
 *   - 空 spans → 返空数组。
 *
 * 排序：兄弟 span 按 startMs 升序（调用时序），保证瀑布图从上到下大体按时间。
 */
export function buildSpanTree(spans: Span[]): SpanNode[] {
  if (!spans || spans.length === 0) return []

  const byId = new Map<string, SpanNode>()
  for (const sp of spans) {
    byId.set(sp.id, { span: sp, depth: 0, children: [] })
  }

  const roots: SpanNode[] = []
  for (const node of byId.values()) {
    const pid = node.span.parentId
    // parentId 非空且指向存在的 span → 挂为子；否则当根（孤儿/顶层）。
    if (pid && byId.has(pid)) {
      byId.get(pid)!.children.push(node)
    } else {
      roots.push(node)
    }
  }

  // 深度优先标 depth + 兄弟按 startMs 排序；visited 防循环引用。
  const visit = (node: SpanNode, depth: number, visited: Set<string>) => {
    if (visited.has(node.span.id)) return
    visited.add(node.span.id)
    node.depth = depth
    node.children.sort((a, b) => a.span.startMs - b.span.startMs)
    for (const child of node.children) visit(child, depth + 1, visited)
  }
  roots.sort((a, b) => a.span.startMs - b.span.startMs)
  for (const root of roots) visit(root, 0, new Set<string>())

  return roots
}

/**
 * flattenSpanTree：深度优先遍历森林为带 depth 的有序数组，供 v-for 平铺渲染。
 * 顺序 = 树前序遍历（根 → 子），缩进 = depth 表层级，一眼看出调用关系。
 */
export function flattenSpanTree(roots: SpanNode[]): FlattenedSpan[] {
  const out: FlattenedSpan[] = []
  const walk = (node: SpanNode) => {
    out.push({ span: node.span, depth: node.depth })
    for (const child of node.children) walk(child)
  }
  for (const root of roots) walk(root)
  return out
}

/** spanWidth：瀑布条宽度（占 trace 时长比例，最小 8% 保证极短 span 可见）。 */
export function spanWidth(sp: Span, row: Trace): number {
  if (!row.durationMs) return 8
  return Math.max(8, Math.round((sp.durationMs / row.durationMs) * 100))
}

/** spanLeft：瀑布条左偏移（startMs 占 trace 时长比例，上限 92% 留尾），Gantt 式体现「何时开始」。 */
export function spanLeft(sp: Span, row: Trace): number {
  if (!row.durationMs) return 0
  return Math.min(92, Math.round((sp.startMs / row.durationMs) * 100))
}

/**
 * spanChips：从 OTel tags 提取关键元数据（HTTP 方法/路径/状态码、客户端 IP、RPC 系统）。
 * status>=500 或网络对端标记错误色，便于一眼定位异常来源。
 */
export function spanChips(sp: Span): { label: string; v: string; err?: boolean }[] {
  const t = sp.tags || {}
  const chips: { label: string; v: string; err?: boolean }[] = []
  const method = t['http.request.method'] || t['http.method']
  const path = t['url.path'] || t['http.target']
  const status = t['http.response.status_code'] || t['http.status_code']
  if (method) chips.push({ label: '方法', v: method })
  if (path) chips.push({ label: '路径', v: path })
  if (status) {
    const err = Number(status) >= 500
    chips.push({ label: '状态', v: status, err })
  }
  const ip = t['client.address'] || t['network.peer.address'] || t['net.peer.name']
  if (ip) chips.push({ label: '客户端 IP', v: ip })
  const rpc = t['rpc.system']
  if (rpc) chips.push({ label: 'RPC', v: rpc })
  return chips
}

/** errSpanCount：错误 span 计数（trace 列表「状态」列旁显「异常 2/3」便于定位）。 */
export function errSpanCount(row: Trace): number {
  return (row.spans || []).filter((s) => s.isError).length
}

/**
 * spanKindBadge：span.kind 徽标（区分入口/出站调用，Jaeger UI 同款语义）。
 * server ⇅ 入口 / client → 出站 / producer ◀ 生产 / consumer ▶ 消费 / internal 省略。
 */
export function spanKindBadge(kind?: string): { text: string; title: string } | null {
  switch (kind) {
    case 'server': return { text: '⇅ 入口', title: 'server：本服务接收的入口请求' }
    case 'client': return { text: '→ 出站', title: 'client：本服务发起的出站调用' }
    case 'producer': return { text: '◀ 生产', title: 'producer：消息生产' }
    case 'consumer': return { text: '▶ 消费', title: 'consumer：消息消费' }
    default: return null
  }
}

/**
 * spanServiceLabel：span 服务列文案——client span 显示「调用方 → 真实对端」。
 * 如 bff 的 redis client span 显示「bff → redis」，而非只显示调用方 bff（对端语义丢失）。
 */
/** spanLane：取 span 的泳道（paas.lane 属性；无 = default 基线）。 */
export function spanLane(span: Span): string {
  return span.tags?.['paas.lane'] || ''
}

/** traceHasLane：trace 是否含泳道 span（图例仅在含泳道时显示）。 */
export function traceHasLane(row: Trace): boolean {
  return (row.spans || []).some((s) => spanLane(s))
}

export function spanServiceLabel(sp: Span): string {
  if (sp.kind === 'client' && sp.peer) return `${sp.service} → ${sp.peer}`
  return sp.service
}
