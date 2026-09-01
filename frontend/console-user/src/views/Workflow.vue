<script setup lang="ts">
// AI 服务 → 工作流：把 Agent/Tool 串成多步流程（llm/condition/approve/end）。
// 表单化节点编辑（同 PipelineDesigner 模式，非画布——首刀取舍）；运行视图独立抽屉。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON } from '@/api'
import {
  listWorkflows, createWorkflow, updateWorkflow, deleteWorkflow,
  type WorkflowDef, type NodeDef,
} from '@/api/workflow'
import WorkflowRunView from './WorkflowRunView.vue'

interface AgentOption { id: string; name: string; enabled: boolean }
interface ToolOption { id: string; name: string; type: string; enabled: boolean }

const workflows = ref<WorkflowDef[]>([])
const agents = ref<AgentOption[]>([])
const tools = ref<ToolOption[]>([])
const loading = ref(false)

const NODE_TYPES = [
  { value: 'start', label: '开始' },
  { value: 'llm', label: 'LLM（Agent）' },
  { value: 'tool', label: '工具（MCP）' },
  { value: 'condition', label: '条件分支' },
  { value: 'approve', label: '人工确认' },
  { value: 'end', label: '结束' },
]
const nodeTypeLabel = (t: string) => NODE_TYPES.find(n => n.value === t)?.label ?? t
const nodeTypeTag = (t: string) =>
  ({ start: 'info', llm: 'primary', tool: 'success', condition: 'warning', approve: 'danger', end: 'info' } as Record<string, string>)[t] ?? 'info'

async function load() {
  loading.value = true
  try {
    const [w, a, t] = await Promise.all([
      listWorkflows(),
      fetchJSON<AgentOption[]>('/api/agents'),
      fetchJSON<ToolOption[]>('/api/tools'),
    ])
    workflows.value = w
    agents.value = a
    tools.value = t
  } catch (e) {
    ElMessage.error('加载工作流失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// —— 编辑器（创建/更新共用）——
const showForm = ref(false)
const editingId = ref<string | null>(null)
const saving = ref(false)
const form = ref(emptyForm())

function emptyForm(): WorkflowDef {
  return {
    id: '', name: '', desc: '', enabled: true,
    nodes: [
      { id: 's', type: 'start', nextId: 'e' },
      { id: 'e', type: 'end' },
    ],
  }
}

function openCreate() {
  editingId.value = null
  form.value = emptyForm()
  argRowsMap.clear()
  showForm.value = true
}

function openEdit(w: WorkflowDef) {
  editingId.value = w.id
  form.value = JSON.parse(JSON.stringify(w))
  argRowsMap.clear()
  // 旧数据兜底：每个节点确保 config 与类型匹配（防模板 `n.config!.x` 崩溃）
  for (const n of form.value.nodes) ensureConfig(n)
  showForm.value = true
}

// 按节点类型补齐默认 config（切换类型/载入旧数据时兜底，防 config 缺失崩溃）
function ensureConfig(n: NodeDef) {
  if (n.type === 'llm') {
    n.config = { agentId: n.config?.agentId ?? '', inputTemplate: n.config?.inputTemplate ?? '{{inputs.input}}' }
  } else if (n.type === 'tool') {
    n.config = { toolId: n.config?.toolId ?? '', toolName: n.config?.toolName ?? '', args: n.config?.args ?? {} }
  } else if (n.type === 'approve') {
    n.config = { message: n.config?.message ?? '' }
  } else {
    // start/condition/end 无 config 语义，清空防脏数据
    n.config = undefined
  }
}

const nodeIds = computed(() => form.value.nodes.map(n => n.id))

// 找最大不重复的 n<k> 新 ID（删除节点后按 length 生成会撞已有 ID）
function nextNodeId(): string {
  const used = new Set(form.value.nodes.map(n => n.id))
  for (let k = 1; ; k++) {
    const id = `n${k}`
    if (!used.has(id)) return id
  }
}

function addNode() {
  form.value.nodes.splice(form.value.nodes.length - 1, 0, {
    id: nextNodeId(), type: 'llm', name: '',
    nextId: form.value.nodes[form.value.nodes.length - 1]?.id || 'e',
    config: { agentId: agents.value[0]?.id ?? '', inputTemplate: '{{inputs.input}}' },
  })
}

function removeNode(i: number) {
  const id = form.value.nodes[i].id
  form.value.nodes.splice(i, 1)
  // 清悬挂连线：指向被删节点的 nextId 重指到 end
  for (const n of form.value.nodes) {
    if (n.nextId === id) n.nextId = 'e'
    if (n.elseId === id) n.elseId = 'e'
    for (const b of n.branches || []) if (b.nextId === id) b.nextId = 'e'
  }
}

// 参数编辑行（本地可变副本，change 时同步回 config.args）
const argRowsMap = new Map<NodeDef, { k: string; v: string }[]>()
function argRows(n: NodeDef): { k: string; v: string }[] {
  let rows = argRowsMap.get(n)
  if (!rows) {
    rows = Object.entries(n.config?.args || {}).map(([k, v]) => ({ k, v }))
    argRowsMap.set(n, rows)
  }
  return rows
}
function syncArgs(n: NodeDef) {
  const args: Record<string, string> = {}
  for (const { k, v } of argRowsMap.get(n) || []) {
    if (k.trim()) args[k.trim()] = v
  }
  ;(n.config ||= {}).args = args
}

function addBranch(n: NodeDef) {
  ;(n.branches ||= []).push({ when: '', nextId: 'e' })
}
function removeBranch(n: NodeDef, i: number) {
  n.branches?.splice(i, 1)
}

// 节点类型切换：重置为该类型的默认 config（防旧 config 残留/缺失）
function onTypeChange(n: NodeDef) {
  ensureConfig(n)
}

async function save() {
  if (!form.value.name.trim()) {
    ElMessage.warning('请输入工作流名')
    return
  }
  // 节点 ID 校验：非空 + 唯一（重复时指明哪个 ID 重复）
  const seen = new Set<string>()
  for (const n of form.value.nodes) {
    const id = n.id.trim()
    if (!id) {
      ElMessage.error(`节点「${n.name || n.type}」的 ID 不能为空`)
      return
    }
    if (seen.has(id)) {
      ElMessage.error(`节点 ID「${id}」重复，请修改后重试`)
      return
    }
    seen.add(id)
  }
  // 参数行收口：@change 未触发（如最后一行未失焦）时兜底同步回 config.args
  for (const n of form.value.nodes) {
    if (n.type === 'tool') syncArgs(n)
  }
  saving.value = true
  try {
    const body = { ...form.value }
    if (editingId.value) {
      await updateWorkflow(editingId.value, body)
      ElMessage.success('已保存')
    } else {
      await createWorkflow(body)
      ElMessage.success('已创建')
    }
    showForm.value = false
    await load()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    saving.value = false
  }
}

async function remove(w: WorkflowDef) {
  try {
    await ElMessageBox.confirm(
      `删除工作流「${w.name}」？运行历史一并清除，不可恢复。`, '删除确认',
      { type: 'warning', confirmButtonText: '删除' },
    )
    await deleteWorkflow(w.id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error((e as Error).message)
  }
}

// —— 运行抽屉 ——
const runViewWorkflow = ref<WorkflowDef | null>(null)
function openRuns(w: WorkflowDef) {
  runViewWorkflow.value = w
}
</script>

<template>
  <div class="page">
    <div class="head">
      <div>
        <h2>工作流</h2>
        <p class="sub">把 Agent / 工具串成多步流程：LLM 分类 → 条件分流 → 人工确认 → 自动执行</p>
      </div>
      <el-button type="primary" @click="openCreate">+ 新建工作流</el-button>
    </div>

    <el-empty v-if="!loading && !workflows.length" description="暂无工作流——新建一个，把 Agent 串成多步流程" />
    <div v-else class="wf-list" v-loading="loading">
      <div v-for="w in workflows" :key="w.id" class="wf-card">
        <div class="wf-head">
          <b>{{ w.name }}</b>
          <el-tag size="small" :type="w.enabled ? 'success' : 'info'">{{ w.enabled ? '已启用' : '停用' }}</el-tag>
          <span class="grow" />
          <el-button size="small" @click="openRuns(w)">运行</el-button>
          <el-button size="small" @click="openEdit(w)">编辑</el-button>
          <el-button size="small" type="danger" plain @click="remove(w)">删除</el-button>
        </div>
        <p v-if="w.desc" class="wf-desc">{{ w.desc }}</p>
        <div class="wf-nodes">
          <template v-for="(n, i) in w.nodes" :key="n.id">
            <span class="wf-node" :class="n.type">
              <el-tag size="small" :type="nodeTypeTag(n.type)">{{ nodeTypeLabel(n.type) }}</el-tag>
              <span class="mono">{{ n.id }}</span>
            </span>
            <span v-if="i < w.nodes.length - 1" class="wf-arrow">→</span>
          </template>
        </div>
      </div>
    </div>

    <!-- 编辑弹窗 -->
    <el-dialog v-model="showForm" :title="editingId ? '编辑工作流' : '新建工作流'" width="760px" top="5vh">
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如：客服工单分流" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.desc" placeholder="可选" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>

      <div class="nodes-editor">
        <div class="nodes-title">
          节点（{{ form.nodes.length }}/50）
          <el-button size="small" @click="addNode" :disabled="form.nodes.length >= 50">+ 添加节点</el-button>
        </div>
        <div v-for="(n, i) in form.nodes" :key="n.id" class="node-card">
          <div class="node-row">
            <el-input v-model="n.id" placeholder="节点 ID（如 cls）" style="width: 120px" class="mono" />
            <el-select v-model="n.type" style="width: 140px" @change="onTypeChange(n)">
              <el-option v-for="t in NODE_TYPES" :key="t.value" :value="t.value" :label="t.label" />
            </el-select>
            <el-input v-model="n.name" placeholder="展示名（可选）" style="width: 130px" />
            <span class="grow" />
            <el-button v-if="n.type !== 'start' && n.type !== 'end'" size="small" type="danger" plain @click="removeNode(i)">删除</el-button>
          </div>

          <!-- llm -->
          <template v-if="n.type === 'llm'">
            <div class="node-row">
              <span class="lbl">Agent</span>
              <el-select v-model="n.config!.agentId" style="width: 240px" placeholder="选择 Agent">
                <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="a.name" :disabled="!a.enabled" />
              </el-select>
            </div>
            <div class="node-row">
              <span class="lbl">提示模板</span>
              <el-input
v-model="n.config!.inputTemplate" type="textarea" :rows="2"
                placeholder="支持 {{inputs.变量}} 与 {{nodes.节点ID.output}} 占位符"
/>
            </div>
          </template>

          <!-- tool -->
          <template v-if="n.type === 'tool'">
            <div class="node-row">
              <span class="lbl">工具</span>
              <el-select v-model="n.config!.toolId" style="width: 240px" placeholder="选择 MCP 工具">
                <el-option
v-for="t in tools" :key="t.id" :value="t.id"
                  :label="`${t.name}（${t.type}${t.enabled ? '' : '·已禁用'}）`" :disabled="t.type !== 'mcp' || !t.enabled"
/>
              </el-select>
              <el-input v-model="n.config!.toolName" placeholder="MCP 方法名" style="width: 160px" class="mono" />
            </div>
            <div class="node-row">
              <span class="lbl">参数</span>
              <div class="args">
                <div v-for="(a, ai) in argRows(n)" :key="ai" class="arg-row">
                  <el-input v-model="a.k" size="small" placeholder="key" class="mono" style="width: 110px" @change="syncArgs(n)" />
                  <el-input v-model="a.v" size="small" placeholder="值或 {{inputs.x}} 占位" @change="syncArgs(n)" />
                  <el-button size="small" text type="danger" @click="argRows(n).splice(ai, 1); syncArgs(n)">删</el-button>
                </div>
                <el-button size="small" text type="primary" @click="argRows(n).push({ k: '', v: '' })">+ 参数</el-button>
              </div>
            </div>
          </template>

          <!-- condition -->
          <template v-if="n.type === 'condition'">
            <div v-for="(b, bi) in n.branches || []" :key="bi" class="node-row">
              <span class="lbl">条件{{ bi + 1 }}</span>
              <el-input v-model="b.when" placeholder="nodes.cls.output == 售后（== != contains）" class="mono" />
              <span>→</span>
              <el-select v-model="b.nextId" style="width: 110px">
                <el-option v-for="id in nodeIds" :key="id" :value="id" :label="id" />
              </el-select>
              <el-button size="small" text type="danger" @click="removeBranch(n, bi)">删</el-button>
            </div>
            <div class="node-row">
              <el-button size="small" text type="primary" @click="addBranch(n)">+ 条件分支</el-button>
            </div>
            <div class="node-row">
              <span class="lbl">否则→</span>
              <el-select v-model="n.elseId" style="width: 110px">
                <el-option v-for="id in nodeIds" :key="id" :value="id" :label="id" />
              </el-select>
            </div>
          </template>

          <!-- approve -->
          <template v-if="n.type === 'approve'">
            <div class="node-row">
              <span class="lbl">说明</span>
              <el-input v-model="n.config!.message" placeholder="展示给确认人的信息（可选）" />
            </div>
          </template>

          <!-- 连线（非 condition/end） -->
          <div v-if="n.type !== 'condition' && n.type !== 'end'" class="node-row">
            <span class="lbl">下一节点</span>
            <el-select v-model="n.nextId" style="width: 110px">
              <el-option v-for="id in nodeIds" :key="id" :value="id" :label="id" />
            </el-select>
          </div>
        </div>
      </div>

      <template #footer>
        <el-button @click="showForm = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 运行抽屉 -->
    <el-drawer v-if="runViewWorkflow" :model-value="true" size="70%" @close="runViewWorkflow = null">
      <template #header>
        <b>运行 · {{ runViewWorkflow.name }}</b>
      </template>
      <WorkflowRunView :workflow="runViewWorkflow" :agents="agents" @changed="load" />
    </el-drawer>
  </div>
</template>

<style scoped>
.page { max-width: 1100px; margin: 0 auto; }
.head { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 18px; }
.head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.wf-list { display: flex; flex-direction: column; gap: 12px; }
.wf-card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 14px 16px; }
.wf-head { display: flex; align-items: center; gap: 10px; }
.wf-desc { margin: 6px 0 0; font-size: 12.5px; color: var(--text-dim); }
.wf-nodes { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 10px; }
.wf-node { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; }
.wf-arrow { color: var(--text-faint); }
.grow { flex: 1; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.nodes-editor { max-height: 55vh; overflow-y: auto; border: 1px solid var(--border); border-radius: var(--radius); padding: 10px; }
.nodes-title { display: flex; align-items: center; justify-content: space-between; font-size: 13px; font-weight: 600; margin-bottom: 8px; }
.node-card { border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; margin-bottom: 8px; display: flex; flex-direction: column; gap: 6px; }
.node-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.lbl { font-size: 12px; color: var(--text-faint); min-width: 52px; }
.args { flex: 1; display: flex; flex-direction: column; gap: 4px; }
.arg-row { display: flex; align-items: center; gap: 6px; }
.arg-k { color: var(--brand); min-width: 60px; }
</style>
