// 放弃变更/批次确认（共享）：此前同文案三份拷贝散落 AppChanges/AppPipelines/ChangeDetail/BatchDetail。
// 统一文案与行为：变更提示分支保留、批次提示批内变更回退 open。
import { ElMessageBox } from 'element-plus'

export async function confirmAbandon(kind: 'change' | 'batch', title: string): Promise<boolean> {
  const msg =
    kind === 'change'
      ? `放弃变更「${title}」？分支保留，可重新引用。`
      : `放弃批次「${title}」？批内变更将回退为待发布，可重新入批。`
  try {
    await ElMessageBox.confirm(msg, '放弃确认', { type: 'warning' })
    return true
  } catch {
    return false
  }
}
