<script setup lang="ts">
// AI 服务 -> Agent（P3）：组装 system prompt + 工具 + KB RAG 调底层 LLM。
// 虚拟模型 agent:{id} 经 /v1/chat/completions 调用；此处提供 CRUD + 试运行 + 评估入口。
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth } from '@/api'

interface Agent {
  id: string; name: string; description: string
  model: string; systemPrompt: string; promptRef: string
  tools: string[] | null; knowledgeBases: string[] | null
  maxSteps: number; enabled: boolean
  createdAt: string
}
interface Model { id: string; vendor: string }
interface Tool { id: string; name: string }
interface KB { id: string; name: string }
interface EvalCase {
  id: string; name: string; input: string; expected: string; matchType: string
}
interface EvalResult {
  caseId: string; name: string; passed: boolean; output: string; reason: string; durationMs: number
}

const agents = ref<Agent[]>([])
const models = ref<Model[]>([])
const tools = ref<Tool[]>([])
const kbs = ref<KB[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const [a, m, t, k] = await Promise.all([
      fetchJSON<Agent[]>('/api/agents'),
      fetchJSON<Model[]>('/api/models'),
      fetchJSON<Tool[]>('/api/tools'),
      fetchJSON<KB[]>('/api/knowledgebases'),
    ])
    agents.value = a
    models.value = m
    tools.value = t
    kbs.value = k
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
    tools: [], knowledgeBases: [], maxSteps: 5, enabled: true, createdAt: '',
  }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  if (models.value.length) form.value.model = models.value[0].id
  showForm.value = true
}

function openEdit(a: Agent) {
  editing.value = a
  form.value = {
    ...a,
    tools: a.tools ? [...a.tools] : [],
    knowledgeBases: a.knowledgeBases ? [...a.knowledgeBases] : [],
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
    const j = await resp.json().catch(() => ({}))
    ElMessage.error('保存失败：' + ((j as any)?.error || resp.status))
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

function openRun(a: Agent) {
  runAgent.value = a
  runInput.value = ''
  runOutput.value = ''
  runReasoning.value = ''
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
      }),
    })
    if (!resp.ok || !resp.body) {
      const j = await resp.json().catch(() => ({}))
      ElMessage.error('运行失败：' + ((j as any)?.error || resp.status))
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
  await loadEvalCases()
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
    const j = await resp.json().catch(() => ({}))
    ElMessage.error('创建失败：' + ((j as any)?.error || resp.status))
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
      ElMessage.error('评估失败：' + ((j as any)?.error || resp.status))
      return
    }
    evalResults.value = (j as any)?.data ?? j ?? []
  } finally {
    evalLoading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>Agent</h2>
      <el-button type="primary" @click="openCreate">新建 Agent</el-button>
    </div>
    <el-table v-loading="loading" :data="agents" empty-text="暂无 Agent">
      <el-table-column prop="name" label="名称" min-width="140" />
      <el-table-column prop="model" label="模型" width="140" />
      <el-table-column label="工具" width="80">
        <template #default="{ row }">{{ (row.tools || []).length }}</template>
      </el-table-column>
      <el-table-column label="知识库" width="90">
        <template #default="{ row }">{{ (row.knowledgeBases || []).length }}</template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="320">
        <template #default="{ row }">
          <el-button size="small" @click="openRun(row)">试运行</el-button>
          <el-button size="small" @click="openEval(row)">评估</el-button>
          <el-button size="small" type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 创建/编辑 -->
    <el-dialog v-model="showForm" :title="editing ? '编辑 Agent' : '新建 Agent'" width="640px">
      <el-form label-width="100px">
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="模型">
          <el-select v-model="form.model" filterable>
            <el-option v-for="m in models" :key="m.id" :label="`${m.id} (${m.vendor})`" :value="m.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明"><el-input v-model="form.description" type="textarea" :rows="2" /></el-form-item>
        <el-form-item label="System Prompt">
          <el-input v-model="form.systemPrompt" type="textarea" :rows="4" placeholder="留空则用 PromptRef 模板" />
        </el-form-item>
        <el-form-item label="PromptRef"><el-input v-model="form.promptRef" placeholder="引用提示词 name（可选）" /></el-form-item>
        <el-form-item label="工具">
          <el-select v-model="form.tools" multiple filterable placeholder="可选">
            <el-option v-for="t in tools" :key="t.id" :label="t.name" :value="t.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="知识库">
          <el-select v-model="form.knowledgeBases" multiple filterable placeholder="可选">
            <el-option v-for="k in kbs" :key="k.id" :label="k.name" :value="k.id" />
          </el-select>
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
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.page-header h2 { margin: 0; }
.stream { white-space: pre-wrap; word-break: break-word; background: var(--el-fill-color-light); padding: 10px; border-radius: 6px; font-size: 13px; margin: 0; }
.out-label { margin-top: 12px; margin-bottom: 4px; color: var(--el-text-color-secondary); font-size: 13px; }
.out { background: var(--el-color-success-light-9); }
</style>
