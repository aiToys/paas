// 发布到广场的通用交互（Skill/Tool/Prompt/Agent 四页复用）：
// 无分类时 prompt 选分类 → 确认（明示快照语义 + Tool 凭证剔除）→ 调发布 API → 分类回写实体。
import { ElMessage, ElMessageBox } from 'element-plus'
import { respError } from '@/api'
import { publishToMarket, CATEGORIES } from '@/api/marketplace'

export interface PublishableRow {
  id: string
  name: string
  category?: string
  installedFrom?: string
}

export function usePublish(entityType: 'skill' | 'tool' | 'prompt' | 'agent', rewrite: (row: PublishableRow, category: string) => Promise<unknown>, reload?: () => void) {
  async function publish(row: PublishableRow) {
    let category = row.category || ''
    if (!category) {
      const { value } = await ElMessageBox.prompt(
        '发布到广场前请选择分类（发布后其他租户可浏览安装）', '发布到广场',
        {
          inputValue: '',
          inputPlaceholder: 'writing / coding / data / service / general',
          inputValidator: (v: string) => CATEGORIES.some(c => c.value === v) || '请输入合法分类（writing/coding/data/service/general）',
        },
      )
      category = value
    } else {
      await ElMessageBox.confirm(
        `确定把「${row.name}」发布到广场？发布的是当前内容的快照（后续修改需重新发布）。` +
        (entityType === 'tool' ? '工具凭证（apiKey/token 等）不会发布。' : ''),
        '发布到广场', { type: 'info' },
      )
    }
    const resp = await publishToMarket(entityType, row.id, category)
    if (!resp.ok) {
      ElMessage.error(await respError(resp, '发布失败：'))
      return
    }
    if (row.category !== category) {
      await rewrite(row, category)
      reload?.()
    }
    ElMessage.success('已发布到广场')
  }
  return { publish }
}
