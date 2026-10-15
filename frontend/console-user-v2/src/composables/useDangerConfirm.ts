// 统一危险操作确认。
// 生产环境的写操作（删除/扩缩容等）强制二次确认，高危（删除）要求输入名称确认；
// 测试环境普通确认。防误操作生产。
import { ElMessageBox } from 'element-plus'
import { useEnvStore } from '@/stores/env'

export interface DangerOptions {
  /** 操作动作，如「删除」「扩缩容」 */
  action: string
  /** 操作目标名称，如工作负载名 */
  target: string
  /** 高危：要求输入目标名称确认（删除类操作） */
  requireNameConfirm?: boolean
  /** 显式 isProd：由调用方按资源所在环境判定传入，覆盖顶栏 scope。
   *  防顶栏 scope 与资源环境不一致时（如测试 scope 下操作生产资源）防护被削弱。 */
  isProd?: boolean
  /** 追加提示（如副作用警示「回滚将丢弃 N 项未发布草稿」），拼在确认文案之后 */
  message?: string
}

/**
 * confirmDangerous 弹出危险操作确认。
 * - 生产环境 + 高危（requireNameConfirm）：要求输入目标名称
 * - 生产环境普通：强制确认（type error）
 * - 测试环境：普通确认（type warning）
 * 返回 true 表示用户确认。
 */
export async function confirmDangerous(opt: DangerOptions): Promise<boolean> {
  const envStore = useEnvStore()
  const isProd = opt.isProd ?? envStore.isProd
  const prefix = isProd ? `⚠️ [生产环境] ` : ''

  // 仅生产环境的高危操作要求输入名称确认；测试环境走普通确认
  if (opt.requireNameConfirm && isProd) {
    try {
      const { value } = await ElMessageBox.prompt(
        `${prefix}确认${opt.action}「${opt.target}」？此操作不可逆。${opt.message ?? ''}请输入名称「${opt.target}」确认。`,
        `${prefix}${opt.action}确认`,
        {
          type: 'error',
          confirmButtonText: `确认${opt.action}`,
          cancelButtonText: '取消',
          inputPlaceholder: opt.target,
          inputValidator: (v) => v === opt.target || `请输入「${opt.target}」以确认`,
        },
      )
      return value === opt.target
    } catch {
      return false
    }
  }

  try {
    await ElMessageBox.confirm(
      `${prefix}确认${opt.action}「${opt.target}」？${opt.message ?? ''}`,
      `${prefix}${opt.action}确认`,
      {
        type: isProd ? 'error' : 'warning',
        confirmButtonText: `确认${opt.action}`,
        cancelButtonText: '取消',
      },
    )
    return true
  } catch {
    return false
  }
}
