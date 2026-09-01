<script setup lang="ts">
// AI 服务 -> 工具（P2）：Agent 可调用的外部能力（MCP server / HTTP / 内置）。
// 租户私有；test/invoke 仅 mcp 类型（initialize + tools/list + tools/call）。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth, respError, apiError } from '@/api'
import { usePublish } from '@/composables/usePublish'

interface Tool {
  id: string; name: string; description: string
  type: string // mcp | http | builtin
  config: Record<string, string>
  category?: string; installedFrom?: string
  enabled: boolean
  createdAt: string
}
interface Agent { id: string; name: string; tools: string[] | null }

const TYPE_LABEL: Record<string, string> = { mcp: 'MCP', http: 'HTTP', builtin: '内置' }

const tools = ref<Tool[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)

// 被 Agent 引用计数（tool id -> 引用它的 Agent 名列表）
const usage = computed(() => {
  const m: Record<string, string[]> = {}
  for (const a of agents.value) {
    for (const tid of a.tools || []) {
      ;(m[tid] ||= []).push(a.name)
    }
  }
  return m
})

async function load() {
  loading.value = true
  try {
    const [t, a] = await Promise.all([
      fetchJSON<Tool[]>('/api/tools'),
      fetchJSON<Agent[]>('/api/agents'),
    ])
    tools.value = t
    agents.value = a
  } catch (e) {
    ElMessage.error(apiError(e, '加载工具失败'))
  } finally {
    loading.value = false
  }
}

// 创建/编辑弹窗
const showForm = ref(false)
const editing = ref<Tool | null>(null)
const form = ref<Tool>(emptyForm())

function emptyForm(): Tool {
  return { id: '', name: '', description: '', type: 'mcp', config: {}, enabled: true, createdAt: '' }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showForm.value = true
}

function openEdit(t: Tool) {
  editing.value = t
  form.value = { ...t, config: { ...t.config } }
  showForm.value = true
}

async function submit() {
  const f = form.value
  if (!f.name || !f.type) {
    ElMessage.warning('名称与类型必填')
    return
  }
  const method = editing.value ? 'PUT' : 'POST'
  const url = editing.value ? `/api/tools/${editing.value.id}` : '/api/tools'
  try {
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
  } catch (e) {
    ElMessage.error(apiError(e, '保存失败'))
  }
}

// 发布到广场（凭证自动剔除，装完自行补填）
const { publish } = usePublish('tool', async (row, category) => {
  await fetchAuth(`/api/tools/${row.id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...row, category }),
  })
}, load)

async function remove(t: Tool) {
  try {
    await ElMessageBox.confirm(`确定删除工具「${t.name}」？`, '删除确认', { type: 'warning' })
    const resp = await fetchAuth(`/api/tools/${t.id}`, { method: 'DELETE' })
    if (!resp.ok) {
      ElMessage.error(await respError(resp, '删除失败：'))
      return
    }
    ElMessage.success('已删除')
    load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e, '删除失败'))
  }
}

// 测试 MCP 工具（initialize + tools/list）
async function testTool(t: Tool) {
  const resp = await fetchAuth(`/api/tools/${t.id}/test`, { method: 'POST' })
  // body 只能读一次：先 json() 再从已解析对象取 error 字段（respError 会重复消费 body 丢文案）
  const j = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    const msg = (j as { error?: string }).error || `HTTP ${resp.status}`
    ElMessage.error('测试失败：' + msg)
    return
  }
  // 响应兼容 {data:{tools:[]}} 与裸 {tools:[]} 两种形态
  const outer = j as { data?: { tools?: unknown[] }; tools?: unknown[] }
  const tools = outer.data?.tools ?? outer.tools ?? []
  if (!Array.isArray(tools) || tools.length === 0) {
    ElMessage.info('连接成功，但未暴露工具')
    return
  }
  ElMessage.success(`连接成功，暴露 ${tools.length} 个工具`)
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>工具</h2>
      <el-button type="primary" @click="openCreate">新建工具</el-button>
    </div>
    <el-table v-loading="loading" :data="tools">
      <template #empty>
        <el-empty description="暂无工具，注册 MCP/HTTP 工具供 Agent 调用" :image-size="64">
          <el-button type="primary" @click="openCreate">新建工具</el-button>
        </el-empty>
      </template>
      <el-table-column label="名称" min-width="150">
        <template #default="{ row }">
          {{ row.name }}
          <el-tag v-if="row.installedFrom" size="small" type="warning" style="margin-left: 6px">来自广场</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="类型" width="100">
        <template #default="{ row }">{{ TYPE_LABEL[row.type] || row.type }}</template>
      </el-table-column>
      <el-table-column prop="description" label="说明" min-width="200" show-overflow-tooltip />
      <el-table-column label="被引用" min-width="140">
        <template #default="{ row }">
          <template v-if="usage[row.id]?.length">
            <el-tooltip :content="usage[row.id].join('、')">
              <el-tag size="small" type="primary">{{ usage[row.id].length }} 个 Agent</el-tag>
            </el-tooltip>
          </template>
          <span v-else style="color: var(--el-text-color-placeholder)">未引用</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="240">
        <template #default="{ row }">
          <el-button size="small" @click="testTool(row)">测试</el-button>
          <el-button size="small" type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="primary" link @click="publish(row)">发布</el-button>
          <el-button size="small" type="danger" link @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showForm" :title="editing ? '编辑工具' : '新建工具'" width="560px">
      <el-form label-width="90px">
        <el-form-item label="名称"><el-input v-model="form.name" placeholder="租户内唯一" /></el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type">
            <el-option label="MCP" value="mcp" />
            <el-option label="HTTP" value="http" />
            <el-option label="内置" value="builtin" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明"><el-input v-model="form.description" type="textarea" :rows="2" /></el-form-item>
        <template v-if="form.type === 'mcp'">
          <el-form-item label="serverURL"><el-input v-model="form.config.serverURL" placeholder="http://srv:8080" /></el-form-item>
          <el-form-item label="apiKey"><el-input v-model="form.config.apiKey" placeholder="可空" /></el-form-item>
        </template>
        <template v-else-if="form.type === 'http'">
          <el-form-item label="endpoint"><el-input v-model="form.config.endpoint" /></el-form-item>
        </template>
        <template v-else>
          <el-form-item label="handler"><el-input v-model="form.config.handler" /></el-form-item>
        </template>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showForm = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.page-header h2 { margin: 0; }
</style>
