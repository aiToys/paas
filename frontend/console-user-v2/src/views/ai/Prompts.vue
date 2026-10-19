<script setup lang="ts">
// AI 服务 -> 提示词（P2）：版本化模板，同 name 多版本。
// 创建可选「立即激活」（默认开=兼容旧语义）或「保存草稿」（现有 active 不动）；
// 激活任意版本前展示引用该名称的 Agent 影响（C3 显式激活）。
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth, fetchSettled, respError, apiError } from '@/api'
import { usePublish } from '@/composables/usePublish'

interface Prompt {
  id: string; name: string; template: string
  variables?: string[]
  category?: string; installedFrom?: string
  version: number; active: boolean
  createdAt: string
}
interface Agent { id: string; name: string; promptRef: string }

const prompts = ref<Prompt[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    prompts.value = await fetchJSON<Prompt[]>('/api/prompts')
  } catch (e) {
    ElMessage.error(apiError(e, '加载提示词失败'))
  } finally {
    loading.value = false
  }
}

// 引用 Agent（激活影响确认用）：独立结算，失败时确认框提示「引用情况未知」不阻塞激活
async function loadAgents() {
  const a = await fetchSettled<Agent[]>('/api/agents', '智能体引用')
  if (a) agents.value = a
}

function agentsUsing(name: string): { names: string[]; known: boolean } {
  if (!agents.value.length) return { names: [], known: false }
  return { names: agents.value.filter((x) => x.promptRef === name).map((x) => x.name), known: true }
}

// 按名称分组（同 name 多版本）
function grouped(): { name: string; versions: Prompt[] }[] {
  const m = new Map<string, Prompt[]>()
  for (const p of prompts.value) {
    if (!m.has(p.name)) m.set(p.name, [])
    m.get(p.name)!.push(p)
  }
  return Array.from(m.entries()).map(([name, versions]) => ({
    name,
    versions: versions.sort((a, b) => b.version - a.version),
  }))
}

// 创建弹窗（保存后立即激活=默认开，兼容旧语义；关闭=草稿版本）
const showForm = ref(false)
// 请求中禁重复提交（§3：连点不重复建）
const submitting = ref(false)
const form = ref({ name: '', template: '', variables: '' })
const activateNow = ref(true)

function openCreate() {
  form.value = { name: '', template: '', variables: '' }
  activateNow.value = true
  showForm.value = true
}

async function submit() {
  if (submitting.value) return
  submitting.value = true
  try {
  const f = form.value
  if (!f.name || !f.template) {
    ElMessage.warning('名称与模板必填')
    return
  }
  const body: { name: string; template: string; variables?: string[]; activate?: boolean } = {
    name: f.name,
    template: f.template,
  }
  if (!activateNow.value) body.activate = false
  if (f.variables.trim()) {
    body.variables = f.variables.split(',').map((s) => s.trim()).filter(Boolean)
  }
  try {
    const resp = await fetchAuth('/api/prompts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!resp.ok) {
      ElMessage.error(await respError(resp, '创建失败：'))
      return
    }
    ElMessage.success(activateNow.value ? '已创建并激活' : '已保存为草稿版本（现有激活版本未变动）')
    showForm.value = false
    load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  }
  } finally {
    submitting.value = false
  }
}

async function activate(p: Prompt) {
  // 激活前展示引用影响：引用该名称的 Agent 立即改用此版本
  const ref = agentsUsing(p.name)
  const impact = ref.known
    ? (ref.names.length
        ? `引用此名称的 Agent：${ref.names.join('、')}——激活后立即改用 v${p.version}。`
        : '当前没有 Agent 引用此名称。')
    : '引用情况未知（Agent 列表加载失败），激活将影响所有引用此名称的 Agent。'
  try {
    await ElMessageBox.confirm(`激活「${p.name} v${p.version}」？\n\n${impact}`, '激活确认', { type: 'warning' })
    const resp = await fetchAuth(`/api/prompts/${p.id}/activate`, { method: 'POST' })
    if (!resp.ok) {
      ElMessage.error(await respError(resp, '激活失败：'))
      return
    }
    ElMessage.success('已激活')
    load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(apiError(e, '激活失败'))
  }
}

// 发布到广场（发布 active 版本快照）
const { publish } = usePublish('prompt', async (row, category) => {
  // Prompt 无 Update 端点（版本不可变）——分类只随发布快照走，不回写
  void row; void category
}, load)

async function remove(p: Prompt) {
  try {
    await ElMessageBox.confirm(`删除「${p.name} v${p.version}」？`, '删除确认', { type: 'warning' })
    const resp = await fetchAuth(`/api/prompts/${p.id}`, { method: 'DELETE' })
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

onMounted(() => { load(); loadAgents() })
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>提示词</h2>
      <el-button type="primary" @click="openCreate">新建提示词</el-button>
    </div>
    <el-collapse v-loading="loading">
      <el-collapse-item v-for="g in grouped()" :key="g.name" :name="g.name">
        <template #title>
          <span class="group-title">{{ g.name }}</span>
          <el-tag size="small" type="info" class="ml">{{ g.versions.length }} 版</el-tag>
        </template>
        <el-table :data="g.versions" size="small">
          <el-table-column label="版本" width="80">
            <template #default="{ row }">v{{ row.version }}</template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag v-if="row.active" type="success">激活</el-tag>
              <el-tag v-else type="info">未激活</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="template" label="模板" min-width="300" show-overflow-tooltip />
          <el-table-column label="操作" width="180">
            <template #default="{ row }">
              <el-button size="small" :disabled="row.active" @click="activate(row)">激活</el-button>
              <el-button size="small" type="primary" link @click="publish(row)">发布</el-button>
              <el-button size="small" type="danger" link @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-collapse-item>
    </el-collapse>
    <el-empty v-if="!loading && prompts.length === 0" description="暂无提示词，沉淀可复用的 Prompt 模板" :image-size="64">
      <el-button type="primary" @click="openCreate">新建提示词</el-button>
    </el-empty>

    <el-dialog v-model="showForm" title="新建提示词" width="640px">
      <el-form label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="同 name 自动 version+1" />
        </el-form-item>
        <el-form-item label="创建后激活">
          <el-switch v-model="activateNow" />
          <span class="switch-note">{{ activateNow ? '立即激活，引用此名称的智能体随即改用新版本' : '保存为草稿，现有激活版本不动；稍后在列表手动激活' }}</span>
        </el-form-item>
        <el-form-item label="模板">
          <el-input v-model="form.template" type="textarea" :rows="8" placeholder="支持 {{.var}} 模板变量" />
        </el-form-item>
        <el-form-item label="变量">
          <el-input v-model="form.variables" placeholder="逗号分隔，如 topic,lang" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showForm = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="submit">{{ submitting ? '保存中…' : (activateNow ? '创建并激活' : '保存草稿') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.page-header h2 { margin: 0; }
.group-title { font-weight: 600; }
.ml { margin-left: 8px; }
.switch-note { margin-left: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
</style>
