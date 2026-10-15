// 发布到广场的通用交互（Skill/Tool/Prompt/Agent/知识库/工作流六页复用）：
// 无分类时 prompt 选分类 → 确认（明示快照语义 + Tool 凭证剔除 + KB/工作流数据不带走）→ 调发布 API → 分类回写实体。
import { ElMessage, ElMessageBox } from 'element-plus'
import { respError } from '@/api'
import { publishToMarket, CATEGORIES } from '@/api/marketplace'

export interface PublishableRow {
  id: string
  name: string
  category?: string
  installedFrom?: string
}

export type PublishableType = 'skill' | 'tool' | 'prompt' | 'agent' | 'knowledgebase' | 'workflow'

// 各类型的快照语义提示（确认弹窗明示，避免安装方误解带走内容）
const SNAPSHOT_HINT: Record<PublishableType, string> = {
  skill: '',
  prompt: '',
  tool: '工具凭证（apiKey/token 等）不会发布。',
  agent: '',
  knowledgebase: '只发布配置骨架（嵌入模型/维度/检索参数）；文档与数据服务引用不会带走。',
  workflow: '节点引用的 Agent/工具会一并打包快照（工具凭证剔除）。',
}

export function usePublish(entityType: PublishableType, rewrite: (row: PublishableRow, category: string) => Promise<unknown>, reload?: () => void) {
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
        `确定把「${row.name}」发布到广场？发布的是当前内容的快照（后续修改需重新发布）。` + SNAPSHOT_HINT[entityType],
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
