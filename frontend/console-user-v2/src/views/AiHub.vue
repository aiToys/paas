<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 智能体中心（v2 原生，AI 域聚合页 2026-10-16）：七 tab——智能体/技能/提示词/工具/
// 知识库/广场（Marketplace）/Agent 运行。五实体基础 CRUD + 广场安装 + 运行取消。
// 深度编辑（工作流编排/eval/文档解析配置）后补；此处保证「看得见、建得了、删得掉」。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError, fetchList } from '@/api'
import { listMarket, type MarketItem } from '@/api/marketplace'
import { listRuns, cancelRun, type AgentRunItem } from '@/api/agentrun'

const router = useRouter()
const props = defineProps<{ initialTab?: 'runs' }>()
type Tab = 'agents' | 'skills' | 'prompts' | 'tools' | 'kbs' | 'market' | 'runs'
const tab = ref<Tab>(props.initialTab || 'agents')
const TABS: { v: Tab; label: string }[] = [
  { v: 'agents', label: '智能体' }, { v: 'skills', label: '技能' }, { v: 'prompts', label: '提示词' },
  { v: 'tools', label: '工具' }, { v: 'kbs', label: '知识库' }, { v: 'market', label: '广场' }, { v: 'runs', label: '运行' },
]

// —— 实体类型 ——
interface Agent {
  id: string; name: string; description: string; model: string; systemPrompt: string
  tools: string[] | null; knowledgeBases: string[] | null; skills: string[] | null
  maxSteps: number; enabled: boolean; createdAt: string
}
interface Skill { id: string; name: string; description: string; instructions: string; enabled: boolean }
interface Prompt { id: string; name: string; template: string; version: number; active: boolean }
interface Tool { id: string; name: string; description: string; type: string; config: Record<string, string>; enabled: boolean }
interface KnowledgeBase {
  id: string; name: string; vectorStoreRef: string; objectStoreRef: string
  embeddingModel: string; embeddingDim: number; createdAt: string
}
const agents = ref<Agent[]>([])
const skills = ref<Skill[]>([])
const prompts = ref<Prompt[]>([])
const tools = ref<Tool[]>([])
const kbs = ref<KnowledgeBase[]>([])
const marketItems = ref<MarketItem[]>([])
const agentRuns = ref<AgentRunItem[]>([])
const loading = ref(false)

async function loadTab(t: Tab) {
  loading.value = true
  try {
    if (t === 'agents') agents.value = await fetchList<Agent>('/api/agents').catch(() => [])
    else if (t === 'skills') skills.value = await fetchList<Skill>('/api/skills').catch(() => [])
    else if (t === 'prompts') prompts.value = await fetchList<Prompt>('/api/prompts').catch(() => [])
    else if (t === 'tools') tools.value = await fetchList<Tool>('/api/tools').catch(() => [])
    else if (t === 'kbs') kbs.value = await fetchList<KnowledgeBase>('/api/knowledgebases').catch(() => [])
    else if (t === 'market') marketItems.value = await listMarket().catch(() => [])
    else if (t === 'runs') agentRuns.value = await listRuns().catch(() => [])
  } finally {
    loading.value = false
  }
}
onMounted(() => loadTab(tab.value))
async function switchTab(t: Tab) {
  tab.value = t
  await loadTab(t)
}

// —— 通用删除 ——
async function removeItem(kind: string, id: string, name: string) {
  if (!await confirmAction(`删除${kind}「${name}」？`)) return
  try {
    await fetchJSON(`/api/${kind}/${id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadTab(tab.value)
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

// —— 创建弹窗（按 tab 动态字段，KISS：核心字段起步） ——
const showCreate = ref(false)
const creating = ref(false)
const cAgent = ref({ name: '', description: '', model: '', systemPrompt: '' })
const cSkill = ref({ name: '', description: '', instructions: '' })
const cPrompt = ref({ name: '', template: '' })
const cTool = ref({ name: '', description: '', type: 'http', url: '' })
const cKb = ref({ name: '', vectorStoreRef: '', objectStoreRef: '', embeddingModel: '', embeddingDim: 1024 })

function openCreate() {
  showCreate.value = true
}
async function submitCreate() {
  creating.value = true
  try {
    if (tab.value === 'agents') {
      if (!cAgent.value.name.trim() || !cAgent.value.model) throw new Error('请填写名称与模型')
      await fetchJSON('/api/agents', { method: 'POST', body: JSON.stringify({ ...cAgent.value, maxSteps: 8, enabled: true }) })
    } else if (tab.value === 'skills') {
      if (!cSkill.value.name.trim() || !cSkill.value.instructions) throw new Error('请填写名称与指令')
      await fetchJSON('/api/skills', { method: 'POST', body: JSON.stringify({ ...cSkill.value, enabled: true }) })
    } else if (tab.value === 'prompts') {
      if (!cPrompt.value.name.trim() || !cPrompt.value.template) throw new Error('请填写名称与模板')
      await fetchJSON('/api/prompts', { method: 'POST', body: JSON.stringify(cPrompt.value) })
    } else if (tab.value === 'tools') {
      if (!cTool.value.name.trim()) throw new Error('请填写名称')
      await fetchJSON('/api/tools', {
        method: 'POST',
        body: JSON.stringify({ name: cTool.value.name, description: cTool.value.description, type: cTool.value.type, config: { url: cTool.value.url } }),
      })
    } else if (tab.value === 'kbs') {
      if (!cKb.value.name.trim()) throw new Error('请填写名称')
      await fetchJSON('/api/knowledgebases', { method: 'POST', body: JSON.stringify({ ...cKb.value }) })
    }
    ElMessage.success('已创建')
    showCreate.value = false
    await loadTab(tab.value)
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    creating.value = false
  }
}

// —— 广场安装 ——
const installing = ref('')
async function install(m: MarketItem) {
  installing.value = m.id
  try {
    const { installFromMarket } = await import('@/api/marketplace')
    await installFromMarket(m.id)
    ElMessage.success(`已安装「${m.name}」到本租户`)
    await loadTab('market')
  } catch (e) {
    ElMessage.error(apiError(e, '安装失败'))
  } finally {
    installing.value = ''
  }
}

// —— Agent 运行取消 ——
async function cancel(name: string) {
  if (!await confirmAction(`取消运行「${name}」？已产生的消耗不退。`)) return
  try {
    await cancelRun(name)
    ElMessage.success('已请求取消')
    await loadTab('runs')
  } catch (e) {
    ElMessage.error(apiError(e, '取消失败'))
  }
}

const TYPE_LABEL: Record<string, string> = { mcp: 'MCP', http: 'HTTP', builtin: '内置' }
const ENTITY_LABEL: Record<string, string> = {
  skill: '技能', prompt: '提示词', tool: '工具', agent: '智能体', knowledgebase: '知识库', workflow: '工作流',
}
function fmtAgo(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const m = Math.floor(Math.max(0, Date.now() - t) / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  if (m < 1440) return `${Math.floor(m / 60)} 小时前`
  return `${Math.floor(m / 1440)} 天前`
}
</script>

<template>
  <div class="page">
    <div class="head-row">
      <h2 class="title">智能体</h2>
      <span class="sub">Agent · 技能 · 工具 · 知识库 · 广场</span>
      <button v-if="tab !== 'market' && tab !== 'runs'" class="head-create" @click="openCreate">新建</button>
    </div>

    <div class="tabs">
      <button v-for="t in (props.initialTab ? [] : TABS)" :key="t.v" class="tab" :class="{ on: tab === t.v }" @click="switchTab(t.v)">
        {{ t.label }}
      </button>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true"></div>

    <!-- 智能体 -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'agents'">
<table class="list-table">
      <thead><tr><th>名称</th><th>模型</th><th>挂载</th><th>状态</th><th>创建</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="a in agents" :key="a.id">
          <td>
            <span class="s-name">{{ a.name }}</span>
            <span v-if="a.description" class="dim desc">· {{ a.description }}</span>
          </td>
          <td class="mono dim">{{ a.model }}</td>
          <td class="dim">
            {{ [a.tools?.length ? `${a.tools.length} 工具` : '', a.skills?.length ? `${a.skills.length} 技能` : '', a.knowledgeBases?.length ? `${a.knowledgeBases.length} 知识库` : ''].filter(Boolean).join(' · ') || '—' }}
          </td>
          <td><span class="status" :class="a.enabled ? 'ok' : 'idle'">{{ a.enabled ? '启用' : '停用' }}</span></td>
          <td class="dim">{{ fmtAgo(a.createdAt) }}</td>
          <td class="col-act"><button class="act" @click="removeItem('agents', a.id, a.name)">删除</button></td>
        </tr>
        <tr v-if="!agents.length && !loading"><td colspan="6" class="empty small-empty">暂无智能体——新建后在试玩中对话调试</td></tr>
      </tbody>
    </table>
</div>

    <!-- 技能 -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'skills'">
<table class="list-table">
      <thead><tr><th>名称</th><th>说明</th><th>状态</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="s in skills" :key="s.id">
          <td><span class="s-name">{{ s.name }}</span></td>
          <td class="dim desc-cell">{{ s.description }}</td>
          <td><span class="status" :class="s.enabled ? 'ok' : 'idle'">{{ s.enabled ? '启用' : '停用' }}</span></td>
          <td class="col-act"><button class="act" @click="removeItem('skills', s.id, s.name)">删除</button></td>
        </tr>
        <tr v-if="!skills.length && !loading"><td colspan="4" class="empty small-empty">暂无技能——可叠加的能力指令包</td></tr>
      </tbody>
    </table>
</div>

    <!-- 提示词 -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'prompts'">
<table class="list-table">
      <thead><tr><th>名称</th><th>模板</th><th>版本</th><th>状态</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="p in prompts" :key="p.id">
          <td><span class="s-name">{{ p.name }}</span></td>
          <td class="dim desc-cell mono">{{ p.template.slice(0, 60) }}{{ p.template.length > 60 ? '…' : '' }}</td>
          <td class="mono dim">v{{ p.version }}</td>
          <td><span class="status" :class="p.active ? 'ok' : 'idle'">{{ p.active ? 'active' : '草稿' }}</span></td>
          <td class="col-act"><button class="act" @click="removeItem('prompts', p.id, p.name)">删除</button></td>
        </tr>
        <tr v-if="!prompts.length && !loading"><td colspan="5" class="empty small-empty">暂无提示词</td></tr>
      </tbody>
    </table>
</div>

    <!-- 工具 -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'tools'">
<table class="list-table">
      <thead><tr><th>名称</th><th>类型</th><th>说明</th><th>状态</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="t in tools" :key="t.id">
          <td><span class="s-name">{{ t.name }}</span></td>
          <td><span class="cap-tag">{{ TYPE_LABEL[t.type] ?? t.type }}</span></td>
          <td class="dim desc-cell">{{ t.description }}</td>
          <td><span class="status" :class="t.enabled ? 'ok' : 'idle'">{{ t.enabled ? '启用' : '停用' }}</span></td>
          <td class="col-act"><button class="act" @click="removeItem('tools', t.id, t.name)">删除</button></td>
        </tr>
        <tr v-if="!tools.length && !loading"><td colspan="5" class="empty small-empty">暂无工具——MCP/HTTP 能力接入</td></tr>
      </tbody>
    </table>
</div>

    <!-- 知识库 -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'kbs'">
<table class="list-table">
      <thead><tr><th>名称</th><th>向量库</th><th>Embedding</th><th>创建</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="k in kbs" :key="k.id">
          <td><span class="s-name">{{ k.name }}</span></td>
          <td class="mono dim">{{ k.vectorStoreRef || '—' }}</td>
          <td class="dim">{{ k.embeddingModel || '—' }}<span v-if="k.embeddingDim" class="dim mono"> · {{ k.embeddingDim }}d</span></td>
          <td class="dim">{{ fmtAgo(k.createdAt) }}</td>
          <td class="col-act"><button class="act" @click="removeItem('knowledgebases', k.id, k.name)">删除</button></td>
        </tr>
        <tr v-if="!kbs.length && !loading"><td colspan="5" class="empty small-empty">暂无知识库——绑定向量/对象存储数据服务后可建</td></tr>
      </tbody>
    </table>
</div>

    <!-- 广场 -->
    <div v-else-if="tab === 'market'" class="market-grid">
      <div v-if="!marketItems.length && !loading" class="empty small-empty">广场暂无内容</div>
      <div v-for="m in marketItems" :key="m.id" class="market-card">
        <div class="mk-head">
          <span class="cap-tag">{{ ENTITY_LABEL[m.entityType] ?? m.entityType }}</span>
          <span class="mk-name">{{ m.name }}</span>
        </div>
        <p class="mk-desc">{{ m.description || '—' }}</p>
        <div class="mk-foot">
          <span class="dim">{{ m.publisherName || '官方' }} · {{ m.installs ?? 0 }} 安装</span>
          <button class="act primary" :disabled="installing === m.id" @click="install(m)">
            {{ installing === m.id ? '安装中…' : '安装' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Agent 运行（phase: Pending|Running|Succeeded|Failed） -->
    <div class="table-scroll" data-table-scroll tabindex="0" v-else-if="tab === 'runs'">
<table class="list-table">
      <thead><tr><th>运行</th><th>状态</th><th>分支</th><th>需求</th><th>时间</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="r in agentRuns" :key="r.name">
          <td><span class="s-name mono"><button class="text-button" @click="router.push('/ai/runs/' + encodeURIComponent(r.name))">{{ r.name }}</button></span></td>
          <td><span class="status" :class="r.phase === 'Succeeded' ? 'ok' : r.phase === 'Running' ? 'warn' : r.phase === 'Failed' ? 'err' : 'idle'">{{ r.phase ?? '—' }}</span></td>
          <td class="mono dim">{{ r.branch || '—' }}</td>
          <td class="dim desc-cell">{{ (r.request || r.message || '—').slice(0, 40) }}</td>
          <td class="dim">{{ fmtAgo(r.createdAt ?? '') }}</td>
          <td class="col-act">
            <button v-if="r.phase === 'Running'" class="act" @click="cancel(r.name)">取消</button>
          </td>
        </tr>
        <tr v-if="!agentRuns.length && !loading"><td colspan="6" class="empty small-empty">暂无运行记录</td></tr>
      </tbody>
    </table>
</div>

    <!-- 创建弹窗（按 tab 切字段） -->
    <el-dialog v-model="showCreate" :title="`新建${TABS.find(t => t.v === tab)?.label ?? ''}`" width="520px" append-to-body>
      <el-form v-if="tab === 'agents'" label-position="top" @submit.prevent="submitCreate">
        <el-form-item label="名称" required><el-input v-model="cAgent.name" placeholder="如 客服小助手" /></el-form-item>
        <el-form-item label="模型" required><el-input v-model="cAgent.model" class="mono" placeholder="如 glm-5.2" /></el-form-item>
        <el-form-item label="说明"><el-input v-model="cAgent.description" /></el-form-item>
        <el-form-item label="系统提示词"><el-input v-model="cAgent.systemPrompt" type="textarea" :rows="4" /></el-form-item>
      </el-form>
      <el-form v-else-if="tab === 'skills'" label-position="top" @submit.prevent="submitCreate">
        <el-form-item label="名称" required><el-input v-model="cSkill.name" /></el-form-item>
        <el-form-item label="说明"><el-input v-model="cSkill.description" /></el-form-item>
        <el-form-item label="指令" required><el-input v-model="cSkill.instructions" type="textarea" :rows="5" placeholder="能力指令：何时触发、如何执行" /></el-form-item>
      </el-form>
      <el-form v-else-if="tab === 'prompts'" label-position="top" @submit.prevent="submitCreate">
        <el-form-item label="名称" required><el-input v-model="cPrompt.name" /></el-form-item>
        <el-form-item label="模板" required><el-input v-model="cPrompt.template" type="textarea" :rows="5" placeholder="支持 {{变量}} 占位" class="mono" /></el-form-item>
      </el-form>
      <el-form v-else-if="tab === 'tools'" label-position="top" @submit.prevent="submitCreate">
        <el-form-item label="名称" required><el-input v-model="cTool.name" /></el-form-item>
        <el-form-item label="类型">
          <select v-model="cTool.type" class="f-select">
            <option value="http">HTTP</option>
            <option value="mcp">MCP</option>
          </select>
        </el-form-item>
        <el-form-item label="端点 URL"><el-input v-model="cTool.url" class="mono" placeholder="https://…" /></el-form-item>
        <el-form-item label="说明"><el-input v-model="cTool.description" /></el-form-item>
      </el-form>
      <el-form v-else-if="tab === 'kbs'" label-position="top" @submit.prevent="submitCreate">
        <el-form-item label="名称" required><el-input v-model="cKb.name" /></el-form-item>
        <el-form-item label="向量库实例（数据服务名）"><el-input v-model="cKb.vectorStoreRef" class="mono" placeholder="如 shop-vector" /></el-form-item>
        <el-form-item label="对象存储实例（可选）"><el-input v-model="cKb.objectStoreRef" class="mono" /></el-form-item>
        <el-form-item label="Embedding 模型"><el-input v-model="cKb.embeddingModel" class="mono" placeholder="如 embedding-3" /></el-form-item>
        <el-form-item label="向量维度"><el-input-number v-model="cKb.embeddingDim" :min="64" :max="4096" :step="128" /></el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showCreate = false">取消</button>
        <button class="act primary" :disabled="creating" @click="submitCreate">{{ creating ? '创建中…' : '创建' }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import './list-page.css';
.tabs { display: flex; gap: 4px; flex-wrap: wrap; margin-bottom: 10px; }
.tab {
  padding: 6px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
}
.tab.on { border-color: var(--brand); color: var(--brand); background: var(--brand-soft); font-weight: 600; }
.desc { font-size: 11.5px; margin-left: 6px; }
.desc-cell { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.small-empty { padding: 20px 0; font-size: 12.5px; color: var(--text-faint); text-align: center; }
.col-act { text-align: right; }
.market-card {
  display: flex; flex-direction: column; gap: 8px;
  padding: 14px; border: 1px solid var(--border); border-radius: var(--radius);
  background: var(--surface);
}
.mk-head { display: flex; align-items: center; gap: 8px; }
.mk-name { font-weight: 600; font-size: 13.5px; }
.mk-desc { margin: 0; font-size: 12px; color: var(--text-dim); flex: 1; }
.mk-foot { display: flex; align-items: center; justify-content: space-between; font-size: 11.5px; }
.f-select {
  width: 100%; box-sizing: border-box; padding: 7px 10px;
  border: 1px solid var(--border-strong); border-radius: var(--radius);
  background: var(--surface-2); color: var(--text); font-family: inherit; font-size: 13px;
}
</style>
