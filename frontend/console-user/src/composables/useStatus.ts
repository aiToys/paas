// 全站状态字典（单一真源）：状态 → 中文文案 + el-tag type。
// 消灭散落各页的本地 STATUS_META / statusType 映射（DRY），统一「同状态同文案同色」：
// - 进行中类（running/testing/releasing/pending）= warning（黄）
// - 成功终态（success/succeeded/released/...）= success（绿）
// - 失败终态（failed/conflict/error）= danger（红）
// - 中性态（abandoned/rolled-back/pending 排队）= info
// 未知状态兜底：原文案 + info，绝不渲染裸英文之外的崩溃（undefined.label）。
export type TagType = 'primary' | 'success' | 'warning' | 'danger' | 'info'

export interface StatusMeta {
  label: string
  type: TagType
}

const def = (label: string, type: TagType): StatusMeta => ({ label, type })

// 构建（BuildRun：success 枚举，非 succeeded）
export const BUILD_STATUS: Record<string, StatusMeta> = {
  pending: def('排队', 'info'),
  running: def('构建中', 'warning'),
  success: def('成功', 'success'),
  failed: def('失败', 'danger'),
}

// 发布（Release）
export const RELEASE_STATUS: Record<string, StatusMeta> = {
  pending: def('发布中', 'warning'),
  deploying: def('部署中', 'warning'),
  succeeded: def('已生效', 'success'),
  failed: def('失败', 'danger'),
  'rolled-back': def('已回滚', 'info'),
}

// 流水线运行（PipelineRun）
export const RUN_STATUS: Record<string, StatusMeta> = {
  pending: def('排队', 'info'),
  running: def('运行中', 'warning'),
  paused: def('等待审批', 'warning'),
  succeeded: def('成功', 'success'),
  failed: def('失败', 'danger'),
  aborted: def('已中止', 'info'),
}

// 变更（Change：released 在发布域统一「已上线」）
export const CHANGE_STATUS: Record<string, StatusMeta> = {
  open: def('开发中', 'info'),
  integrated: def('已集成', 'warning'),
  tested: def('已测试', 'warning'),
  released: def('已上线', 'success'),
  reverted: def('已回退', 'info'),
  abandoned: def('已放弃', 'info'),
}

// 集成批次（IntegrationBatch）
export const BATCH_STATUS: Record<string, StatusMeta> = {
  collecting: def('收集中', 'info'),
  building: def('合并中', 'warning'),
  conflict: def('合并冲突', 'danger'),
  testing: def('集成测试中', 'warning'),
  tested: def('测试通过', 'warning'),
  releasing: def('上线中', 'warning'),
  released: def('已上线', 'success'),
  failed: def('失败', 'danger'),
  abandoned: def('已放弃', 'info'),
}

// 兜底取值：未知状态回显原文（不丢信息）+ info 色
export function statusOf(dict: Record<string, StatusMeta>, s: string): StatusMeta {
  return dict[s] ?? def(s, 'info')
}
