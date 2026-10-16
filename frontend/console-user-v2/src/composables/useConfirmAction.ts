import { ElMessageBox } from 'element-plus'
// 普通确认采用统一对话框与取消语义；需要输入名称的高危操作仍走 useDangerConfirm。
export async function confirmAction(message: string, title = '确认操作'): Promise<boolean> {
  try {
    await ElMessageBox.confirm(message, title, { type: 'warning', confirmButtonText: '确认', cancelButtonText: '取消', closeOnClickModal: false })
    return true
  } catch { return false }
}
