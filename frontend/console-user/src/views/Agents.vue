<script setup lang="ts">
// AI 编排 -> Agent（P3）：组装 system prompt + skill 能力 + 工具 + KB RAG 调底层 LLM。
// 虚拟模型 agent:{id} 经 /v1/chat/completions 调用；此处提供 CRUD + 试运行 + 评估入口。
// 绑定资源（工具/知识库/Skill）用「名称+描述+类型」富选择器展示（非裸 ID）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
const router = useRouter()
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth, respError } from '@/api'
import Icon from '@/components/Icon.vue'
import { usePublish } from '@/composables/usePublish'

interface Agent {
  id: string; name: string; description: string
  model: string; systemPrompt: string; promptRef: string
  tools: string[] | null; knowledgeBases: string[] | null; skills: string[] | null
  category?: string; installedFrom?: string
  maxSteps: number; enabled: boolean
  createdAt: string
}
interface Model { id: string; vendor: string }
interface Tool { id: string; name: string; description: string; type: string; enabled: boolean }
interface KB { id: string; name: string; embeddingModel: string }
interface Skill { id: string; name: string; description: string; enabled: boolean }
interface Prompt { id: string; name: string; version: number; active: boolean }
interface EvalCase {
  id: string; name: string; input: string; expected: string; matchType: string
}
interface EvalResult {
  caseId: string; name: string; passed: boolean; output: string; reason: string; durationMs: number
}
interface EvalRun {
  id: string; agentId: string; total: number; passed: number
  results: EvalResult[] | null; durationMs: number; createdAt: string
}

const TOOL_TYPE_LABEL: Record<string, string> = { mcp: 'MCP', http: 'HTTP', builtin: '内置' }

const agents = ref<Agent[]>([])
const models = ref<Model[]>([])
const tools = ref<Tool[]>([])
const kbs = ref<KB[]>([])
const skills = ref<Skill[]>([])
const prompts = ref<Prompt[]>([])
const loading = ref(false)

// id -> 实体 索引（列表展示具名 tag 用）
const toolById = computed(() => Object.fromEntries(tools.value.map((t) => [t.id, t])))
const kbById = computed(() => Object.fromEntries(kbs.value.map((k) => [k.id, k])))
const skillById = computed(() => Object.fromEntries(skills.value.map((s) => [s.id, s])))

async function load() {
  loading.value = true
  try {
    const [a, m, t, k, s, p] = await Promise.all([
      fetchJSON<Agent[]>('/api/agents'),
      fetchJSON<Model[]>('/api/models'),
      fetchJSON<Tool[]>('/api/tools'),
      fetchJSON<KB[]>('/api/knowledgebases'),
      fetchJSON<Skill[]>('/api/skills'),
      fetchJSON<Prompt[]>('/api/prompts'),
    ])
    agents.value = a
    models.value = m
    tools.value = t
    kbs.value = k
    skills.value = s
    // PromptRef 下拉只展示各 name 的激活版本（去重）
    const seen = new Set<string>()
    prompts.value = p.filter((pr) => pr.active && !seen.has(pr.name) && seen.add(pr.name))
  } catch (e) {
    ElMessage.error('加载 Agent 失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

// 创建/编辑弹窗
const showForm = ref(false)
const editing = ref<Agent | null>(null)
const form = ref<Agent>(emptyForm())

function emptyForm(): Agent {
  return {
    id: '', name: '', description: '', model: '', systemPrompt: '', promptRef: '',
    tools: [], knowledgeBases: [], skills: [], maxSteps: 5, enabled: true, createdAt: '',
  }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  if (models.value.length) form.value.model = models.value[0].id
  showForm.value = true
}

// 发布到广场（Agent 整包：引用的 skill/prompt/tool 一并快照，凭证剔除）
const { publish } = usePublish('agent', async (row, category) => {
  const cur = agents.value.find(x => x.id === row.id)
  if (!cur) return
  await fetchAuth(`/api/agents/${row.id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...cur, category }),
  })
}, load)

function openEdit(a: Agent) {
  editing.value = a
  form.value = {
    ...a,
    tools: a.tools ? [...a.tools] : [],
    knowledgeBases: a.knowledgeBases ? [...a.knowledgeBases] : [],
    skills: a.skills ? [...a.skills] : [],
  }
  showForm.value = true
}

async function submit() {
  const f = form.value
  if (!f.name || !f.model) {
    ElMessage.warning('名称与模型必填')
    return
  }
  const method = editing.value ? 'PUT' : 'POST'
  const url = editing.value ? `/api/agents/${editing.value.id}` : '/api/agents'
  const resp = await fetchAuth(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(f),
  })
  if (!resp.ok) {
    ElMessage.error(await respError(resp, '保存失败：'))
    return
  }
  ElMessage.success(editing.value ? '已更新' : '已创建')
  showForm.value = false
  load()
}

async function remove(a: Agent) {
  await ElMessageBox.confirm(`确定删除 Agent「${a.name}」？`, '删除确认', { type: 'warning' })
  const resp = await fetchAuth(`/api/agents/${a.id}`, { method: 'DELETE' })
  if (!resp.ok) {
    ElMessage.error('删除失败')
    return
  }
  ElMessage.success('已删除')
  load()
}

// 试运行（SSE 流式，OpenAI 兼容）
const runDialog = ref(false)
const runAgent = ref<Agent | null>(null)
const runInput = ref('')
const runOutput = ref('')
const runReasoning = ref('')
const runLoading = ref(false)

// 跳 Playground 选用该 Agent（虚拟模型 agent:{id}，Playground 模型列表已并入 agents）。
function goPlay(row: Agent) {
  router.push({ path: '/playground', query: { model: 'agent:' + row.id } })
}

// 操作列「更多」下拉分发
function onRowCommand(cmd: string, a: Agent) {
  if (cmd === 'playground') goPlay(a)
  else if (cmd === 'eval') openEval(a)
  else if (cmd === 'publish') publish(a)
  else if (cmd === 'delete') remove(a)
}

// 试运行会话 ID：同一 Agent 的试运行连续对话共享记忆（关闭弹窗重置开新会话）
const runConvId = ref('')

function openRun(a: Agent) {
  runAgent.value = a
  runInput.value = ''
  runOutput.value = ''
  runReasoning.value = ''
  runConvId.value = 'run-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8)
  runDialog.value = true
}

async function doRun() {
  if (!runAgent.value || !runInput.value) return
  runLoading.value = true
  runOutput.value = ''
  runReasoning.value = ''
  try {
    const resp = await fetchAuth('/v1/chat/completions', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
      },
      body: JSON.stringify({
        model: `agent:${runAgent.value.id}`,
        messages: [{ role: 'user', content: runInput.value }],
        stream: true,
        conversationId: runConvId.value,
      }),
    })
    if (!resp.ok || !resp.body) {
      ElMessage.error(await respError(resp, '运行失败：'))
      return
    }
    // 解析 SSE 流
    const reader = resp.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      const lines = buf.split('\n')
      buf = lines.pop() || ''
      for (const line of lines) {
        const t = line.trim()
        if (!t.startsWith('data:')) continue
        const payload = t.slice(5).trim()
        if (payload === '[DONE]') continue
        try {
          const obj = JSON.parse(payload)
          const delta = obj.choices?.[0]?.delta
          if (delta?.content) runOutput.value += delta.content
          if (delta?.reasoning_content) runReasoning.value += delta.reasoning_content
        } catch {
          // 跳过无法解析的行
        }
      }
    }
  } catch (e) {
    ElMessage.error('运行异常：' + (e as Error).message)
  } finally {
    runLoading.value = false
  }
}

// 评估用例
const evalDialog = ref(false)
const evalAgent = ref<Agent | null>(null)
const evalCases = ref<EvalCase[]>([])
const evalResults = ref<EvalResult[]>([])
const evalLoading = ref(false)
const newCase = ref({ name: '', input: '', expected: '', matchType: 'contains' })

async function openEval(a: Agent) {
  evalAgent.value = a
  evalResults.value = []
  evalDialog.value = true
  await Promise.all([loadEvalCases(), loadEvalRuns()])
}

// 评估历史（最近 20 次，跑完刷新——回归趋势）
const evalRuns = ref<EvalRun[]>([])
async function loadEvalRuns() {
  if (!evalAgent.value) return
  evalRuns.value = await fetchJSON<EvalRun[]>(`/api/agent-evals/runs?agentId=${evalAgent.value.id}`)
}

async function loadEvalCases() {
  if (!evalAgent.value) return
  evalCases.value = await fetchJSON<EvalCase[]>(`/api/agent-evals?agentId=${evalAgent.value.id}`)
}

async function addCase() {
  if (!evalAgent.value) return
  const c = newCase.value
  if (!c.input || !c.expected) {
    ElMessage.warning('输入与期望必填')
    return
  }
  const resp = await fetchAuth('/api/agent-evals', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...c, agentId: evalAgent.value.id }),
  })
  if (!resp.ok) {
    ElMessage.error(await respError(resp, '创建失败：'))
    return
  }
  newCase.value = { name: '', input: '', expected: '', matchType: 'contains' }
  loadEvalCases()
}

async function removeCase(c: EvalCase) {
  await ElMessageBox.confirm(`删除用例「${c.name || c.id}」？`, '删除确认', { type: 'warning' })
  await fetchAuth(`/api/agent-evals/${c.id}`, { method: 'DELETE' })
  loadEvalCases()
}

async function runEval() {
  if (!evalAgent.value) return
  evalLoading.value = true
  evalResults.value = []
  try {
    const resp = await fetchAuth(`/api/agent-evals/run?agentId=${evalAgent.value.id}`, { method: 'POST' })
    const j = await resp.json().catch(() => ({}))
    if (!resp.ok) {
      ElMessage.error(await respError(resp, '评估失败：'))
      return
    }
    evalResults.value = (j as { data?: EvalResult[] }).data ?? (j as unknown as EvalResult[])
  } finally {
    evalLoading.value = false
    loadEvalRuns()
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2>Agent</h2>
        <p class="sub">组合模型 + Skill 能力 + 工具 + 知识库的智能体，以 agent:{id} 虚拟模型对外提供服务</p>
      </div>
      <el-button type="primary" @click="openCreate">新建 Agent</el-button>
    </div>
    <el-table v-loading="loading" :data="agents">
      <template #empty>
        <el-empty description="暂无 Agent，创建一个开始编排模型与工具" :image-size="64">
          <el-button type="primary" @click="openCreate">新建 Agent</el-button>
        </el-empty>
      </template>
      <el-table-column label="名称" min-width="150">
        <template #default="{ row }">
          {{ row.name }}
          <el-tag v-if="row.installedFrom" size="small" type="warning" style="margin-left: 6px">来自广场</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="model" label="模型" width="130" />
      <el-table-column label="Skill" min-width="130">
        <template #default="{ row }">
          <template v-if="(row.skills || []).length">
            <el-tag v-for="sid in row.skills" :key="sid" size="small" class="tag" type="warning">
              {{ skillById[sid]?.name ?? sid }}
            </el-tag>
          </template>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="工具" min-width="130">
        <template #default="{ row }">
          <template v-if="(row.tools || []).length">
            <el-tag v-for="tid in row.tools" :key="tid" size="small" class="tag">
              {{ toolById[tid]?.name ?? tid }}
            </el-tag>
          </template>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="知识库" min-width="120">
        <template #default="{ row }">
          <template v-if="(row.knowledgeBases || []).length">
            <el-tag v-for="kid in row.knowledgeBases" :key="kid" size="small" class="tag" type="success">
              {{ kbById[kid]?.name ?? kid }}
            </el-tag>
          </template>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" link @click="openRun(row)">试运行</el-button>
          <el-button size="small" type="primary" link @click="openEdit(row)">编辑</el-button>
          <el-dropdown trigger="click" @command="(cmd: string) => onRowCommand(cmd, row)">
            <el-button size="small" type="primary" link>更多<Icon name="chevron" :size="12" style="transform: rotate(90deg); margin-left: 2px" /></el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="playground">Playground</el-dropdown-item>
                <el-dropdown-item command="eval">评估</el-dropdown-item>
                <el-dropdown-item command="publish">发布到广场</el-dropdown-item>
                <el-dropdown-item command="delete" class="danger-item" divided>删除</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </template>
      </el-table-column>
    </el-table>

    <!-- 创建/编辑 -->
    <el-dialog v-model="showForm" :title="editing ? '编辑 Agent' : '新建 Agent'" width="720px">
      <el-form label-width="100px">
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="模型">
          <el-select v-model="form.model" filterable>
            <el-option v-for="m in models" :key="m.id" :label="`${m.id} (${m.vendor})`" :value="m.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明"><el-input v-model="form.description" type="textarea" :rows="2" /></el-form-item>
        <el-form-item label="System Prompt">
          <el-input v-model="form.systemPrompt" type="textarea" :rows="4" placeholder="留空则用提示词模板" />
        </el-form-item>
        <el-form-item label="提示词模板">
          <el-select v-model="form.promptRef" clearable placeholder="引用激活版提示词（可选，System Prompt 优先）">
            <el-option v-for="p in prompts" :key="p.id" :label="p.name" :value="p.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="Skill">
          <el-select v-model="form.skills" multiple filterable placeholder="选择能力指令（运行时叠加注入）">
            <el-option v-for="s in skills" :key="s.id" :label="s.name" :value="s.id" :disabled="!s.enabled">
              <div class="opt">
                <span>{{ s.name }}</span>
                <span class="opt-desc">{{ s.description }}</span>
              </div>
            </el-option>
          </el-select>
          <div v-if="!skills.length" class="hint">还没有 Skill，可先到「AI 服务 → Skill」创建</div>
        </el-form-item>
        <el-form-item label="工具">
          <el-select v-model="form.tools" multiple filterable placeholder="选择可调用的外部工具">
            <el-option v-for="t in tools" :key="t.id" :label="t.name" :value="t.id" :disabled="!t.enabled">
              <div class="opt">
                <span>{{ t.name }}<el-tag size="small" class="opt-type">{{ TOOL_TYPE_LABEL[t.type] || t.type }}</el-tag></span>
                <span class="opt-desc">{{ t.description }}</span>
              </div>
            </el-option>
          </el-select>
          <div v-if="!tools.length" class="hint">还没有工具，可先到「AI 服务 → 工具」注册 MCP/HTTP 工具</div>
        </el-form-item>
        <el-form-item label="知识库">
          <el-select v-model="form.knowledgeBases" multiple filterable placeholder="选择 RAG 检索的知识库">
            <el-option v-for="k in kbs" :key="k.id" :label="k.name" :value="k.id">
              <div class="opt">
                <span>{{ k.name }}</span>
                <span class="opt-desc">{{ k.embeddingModel }}</span>
              </div>
            </el-option>
          </el-select>
          <div v-if="!kbs.length" class="hint">还没有知识库，可先到「资源中心 → 知识库」创建</div>
        </el-form-item>
        <el-form-item label="最大步数"><el-input-number v-model="form.maxSteps" :min="1" :max="20" /></el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showForm = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 试运行 -->
    <el-dialog v-model="runDialog" :title="`试运行：${runAgent?.name || ''}`" width="680px">
      <el-input v-model="runInput" type="textarea" :rows="2" placeholder="输入测试问题" />
      <div style="margin-top: 8px">
        <el-button type="primary" :loading="runLoading" @click="doRun">运行</el-button>
      </div>
      <el-collapse v-if="runReasoning" style="margin-top: 12px">
        <el-collapse-item title="思考过程">
          <pre class="stream">{{ runReasoning }}</pre>
        </el-collapse-item>
      </el-collapse>
      <div v-if="runOutput" class="out-label">输出</div>
      <pre v-if="runOutput" class="stream out">{{ runOutput }}</pre>
    </el-dialog>

    <!-- 评估 -->
    <el-dialog v-model="evalDialog" :title="`评估：${evalAgent?.name || ''}`" width="820px">
      <el-form :inline="true" size="small">
        <el-form-item label="名称"><el-input v-model="newCase.name" /></el-form-item>
        <el-form-item label="输入"><el-input v-model="newCase.input" /></el-form-item>
        <el-form-item label="期望"><el-input v-model="newCase.expected" /></el-form-item>
        <el-form-item label="匹配">
          <el-select v-model="newCase.matchType" style="width: 110px">
            <el-option label="包含" value="contains" />
            <el-option label="全等" value="exact" />
            <el-option label="正则" value="regex" />
          </el-select>
        </el-form-item>
        <el-form-item><el-button type="primary" @click="addCase">加用例</el-button></el-form-item>
      </el-form>
      <el-table :data="evalCases" size="small" style="margin-bottom: 12px">
        <el-table-column prop="name" label="名称" width="120" />
        <el-table-column prop="input" label="输入" min-width="140" show-overflow-tooltip />
        <el-table-column prop="expected" label="期望" min-width="120" show-overflow-tooltip />
        <el-table-column prop="matchType" label="匹配" width="80" />
        <el-table-column label="" width="80">
          <template #default="{ row }"><el-button size="small" link type="danger" @click="removeCase(row)">删</el-button></template>
        </el-table-column>
      </el-table>
      <el-button type="primary" :loading="evalLoading" @click="runEval">跑评估</el-button>
      <el-table v-if="evalResults.length" :data="evalResults" size="small" style="margin-top: 12px">
        <el-table-column prop="name" label="用例" width="120" />
        <el-table-column label="结果" width="80">
          <template #default="{ row }">
            <el-tag :type="row.passed ? 'success' : 'danger'">{{ row.passed ? '通过' : '失败' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="output" label="输出" min-width="200" show-overflow-tooltip />
        <el-table-column prop="reason" label="原因" min-width="120" show-overflow-tooltip />
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ row.durationMs }}ms</template>
        </el-table-column>
      </el-table>

      <!-- 评估历史（回归趋势，点开看逐用例结果） -->
      <template v-if="evalRuns.length">
        <div class="out-label" style="margin-top: 20px">评估历史</div>
        <el-table :data="evalRuns" size="small" @row-click="(run: EvalRun) => { evalResults = run.results || [] }">
          <el-table-column label="时间" width="170">
            <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="通过率" width="120">
            <template #default="{ row }">
              <el-tag :type="row.passed === row.total ? 'success' : row.passed > 0 ? 'warning' : 'danger'" size="small">
                {{ row.passed }}/{{ row.total }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="耗时" width="100">
            <template #default="{ row }">{{ (row.durationMs / 1000).toFixed(1) }}s</template>
          </el-table-column>
          <el-table-column label="" min-width="80">
            <template #default><span class="dim">点击回看结果</span></template>
          </el-table-column>
        </el-table>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.page-header h2 { margin: 0; }
.sub { margin: 4px 0 0; color: var(--el-text-color-secondary); font-size: 13px; }
.dim { color: var(--el-text-color-placeholder); }
.tag { margin: 2px 4px 2px 0; }
/* 富选择器 option：名称+类型 左 / 描述右 */
.opt { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.opt-desc { color: var(--el-text-color-secondary); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 320px; }
.opt-type { margin-left: 6px; }
.hint { font-size: 12px; color: var(--el-text-color-placeholder); margin-top: 4px; line-height: 1.4; }
.stream { white-space: pre-wrap; word-break: break-word; background: var(--el-fill-color-light); padding: 10px; border-radius: 6px; font-size: 13px; margin: 0; }
.out-label { margin-top: 12px; margin-bottom: 4px; color: var(--el-text-color-secondary); font-size: 13px; }
.out { background: var(--el-color-success-light-9); }
/* 下拉菜单在 body 上渲染，scoped 样式不生效，用 :deep 全局穿透 */
:deep(.el-dropdown-menu__item.danger-item) { color: var(--el-color-danger); }
</style>
