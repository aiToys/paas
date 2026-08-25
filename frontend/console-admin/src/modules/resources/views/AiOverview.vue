<template>
  <div class="ai-overview">
    <el-tabs v-model="tab">
      <el-tab-pane label="Agent" name="agents">
        <el-table v-loading="loading" :data="filtered(agents)" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="120" />
          <el-table-column prop="model" label="模型" width="130" />
          <el-table-column label="Skill" min-width="120">
            <template #default="{ row }">
              <el-tag v-for="id in row.skills || []" :key="id" size="small" type="warning" class="tag">{{ skillName(id) }}</el-tag>
              <span v-if="!(row.skills || []).length" class="dim">—</span>
            </template>
          </el-table-column>
          <el-table-column label="工具" min-width="120">
            <template #default="{ row }">
              <el-tag v-for="id in row.tools || []" :key="id" size="small" class="tag">{{ toolName(id) }}</el-tag>
              <span v-if="!(row.tools || []).length" class="dim">—</span>
            </template>
          </el-table-column>
          <el-table-column label="知识库" min-width="110">
            <template #default="{ row }">
              <el-tag v-for="id in row.knowledgeBases || []" :key="id" size="small" type="success" class="tag">{{ kbName(id) }}</el-tag>
              <span v-if="!(row.knowledgeBases || []).length" class="dim">—</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="Skill" name="skills">
        <el-table v-loading="loading" :data="filtered(skills)" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="description" label="说明" min-width="220" show-overflow-tooltip />
          <el-table-column label="被引用" min-width="140">
            <template #default="{ row }">
              <el-tooltip v-if="skillUsage(row.id).length" :content="skillUsage(row.id).join('、')">
                <el-tag size="small" type="primary">{{ skillUsage(row.id).length }} 个 Agent</el-tag>
              </el-tooltip>
              <span v-else class="dim">未引用</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="工具" name="tools">
        <el-table v-loading="loading" :data="filtered(tools)" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column label="类型" width="90">
            <template #default="{ row }">
              <el-tag size="small">{{ TOOL_TYPE[row.type] || row.type }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="description" label="说明" min-width="220" show-overflow-tooltip />
          <el-table-column label="被引用" min-width="140">
            <template #default="{ row }">
              <el-tooltip v-if="toolUsage(row.id).length" :content="toolUsage(row.id).join('、')">
                <el-tag size="small" type="primary">{{ toolUsage(row.id).length }} 个 Agent</el-tag>
              </el-tooltip>
              <span v-else class="dim">未引用</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="知识库" name="kbs">
        <el-table v-loading="loading" :data="filtered(kbs)" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="150" />
          <el-table-column prop="embeddingModel" label="向量模型" width="170" />
          <el-table-column prop="embeddingDim" label="维度" width="90" />
          <el-table-column label="被引用" min-width="140">
            <template #default="{ row }">
              <el-tooltip v-if="kbUsage(row.id).length" :content="kbUsage(row.id).join('、')">
                <el-tag size="small" type="primary">{{ kbUsage(row.id).length }} 个 Agent</el-tag>
              </el-tooltip>
              <span v-else class="dim">未引用</span>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="提示词" name="prompts">
        <el-table v-loading="loading" :data="filtered(prompts)" size="small">
          <el-table-column prop="tenantId" label="租户" width="120">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.tenantId }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="150" />
          <el-table-column prop="version" label="版本" width="80" />
          <el-table-column prop="template" label="模板" min-width="260" show-overflow-tooltip />
        </el-table>
      </el-tab-pane>
      <el-tab-pane label="广场" name="market">
        <el-table v-loading="loading" :data="filteredMarket()" size="small">
          <el-table-column prop="entityType" label="类型" width="90">
            <template #default="{ row }"><el-tag size="small">{{ row.entityType }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="category" label="分类" width="100" />
          <el-table-column prop="description" label="说明" min-width="220" show-overflow-tooltip />
          <el-table-column prop="publisherTenant" label="发布租户" width="110">
            <template #default="{ row }"><el-tag size="small" type="info">{{ row.publisherTenant }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="installs" label="安装量" width="90" />
          <el-table-column prop="createdAt" label="发布时间" width="170">
            <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
// AI 编排跨租户总览（super_admin 只读）：Agent/Skill/工具/知识库/提示词五实体单页 tab 聚合。
// 引用计数（被 N 个 Agent 使用）由本页 agents 数据前端聚合，直观呈现资产使用情况。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  fetchAiAgentList,
  fetchAiSkillList,
  fetchAiToolList,
  fetchAiKnowledgeBaseList,
  fetchAiPromptList,
  fetchAiMarketList,
  type AdminMarketItem,
  type AdminAgent,
  type AdminSkill,
  type AdminTool,
  type AdminKnowledgeBase,
  type AdminPrompt
} from '../api'

const tab = ref('agents')
const loading = ref(false)
const agents = ref<AdminAgent[]>([])
const skills = ref<AdminSkill[]>([])
const tools = ref<AdminTool[]>([])
const kbs = ref<AdminKnowledgeBase[]>([])
const prompts = ref<AdminPrompt[]>([])
const market = ref<AdminMarketItem[]>([])

// 广场列表（publisherTenant 维度过滤，与 filtered 的 tenantId 语义对齐）
function filteredMarket(): AdminMarketItem[] {
  return market.value.filter(
    (m) =>
      (!tenantFilter.value || m.publisherTenant === tenantFilter.value) &&
      (!kw.value ||
        m.id.toLowerCase().includes(kw.value) ||
        m.name.toLowerCase().includes(kw.value) ||
        m.entityType.toLowerCase().includes(kw.value))
  )
}
const keyword = ref('')
const tenantFilter = ref('')

const TOOL_TYPE: Record<string, string> = { mcp: 'MCP', http: 'HTTP', builtin: '内置' }

// id -> 名称 索引（Agent 行内 tag 展示具名，非裸 ID）
const skillName = (id: string) => skills.value.find((s) => s.id === id)?.name ?? id
const toolName = (id: string) => tools.value.find((t) => t.id === id)?.name ?? id
const kbName = (id: string) => kbs.value.find((k) => k.id === id)?.name ?? id

const skillUsage = (id: string) => agents.value.filter((a) => (a.skills || []).includes(id)).map((a) => a.name)
const toolUsage = (id: string) => agents.value.filter((a) => (a.tools || []).includes(id)).map((a) => a.name)
const kbUsage = (id: string) => agents.value.filter((a) => (a.knowledgeBases || []).includes(id)).map((a) => a.name)

// 前端 keyword/租户过滤（数据量 < 1000，客户端过滤足够）
const kw = computed(() => keyword.value.trim().toLowerCase())
function filtered<T extends { tenantId: string; id: string; name: string }>(list: T[]): T[] {
  return list.filter(
    (x) =>
      (!tenantFilter.value || x.tenantId === tenantFilter.value) &&
      (!kw.value ||
        x.id.toLowerCase().includes(kw.value) ||
        x.name.toLowerCase().includes(kw.value))
  )
}

async function load() {
  loading.value = true
  try {
    const [a, s, t, k, p, m] = await Promise.all([
      fetchAiAgentList({ page: 1, size: 1000 }),
      fetchAiSkillList({ page: 1, size: 1000 }),
      fetchAiToolList({ page: 1, size: 1000 }),
      fetchAiKnowledgeBaseList({ page: 1, size: 1000 }),
      fetchAiPromptList({ page: 1, size: 1000 }),
      fetchAiMarketList({ page: 1, size: 1000 })
    ])
    agents.value = a.records
    skills.value = s.records
    tools.value = t.records
    kbs.value = k.records
    prompts.value = p.records
    market.value = m.records
  } catch (e) {
    ElMessage.error('加载 AI 编排总览失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<style scoped>
.ai-overview { padding: 12px; }
.tag { margin: 2px 4px 2px 0; }
.dim { color: var(--el-text-color-placeholder); font-size: 12px; }
</style>
