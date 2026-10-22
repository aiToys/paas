<script setup lang="ts">
// AI 编排 -> Skill（P3.x）：可复用指令能力包，Agent 绑定后运行时注入 system prompt。
// 与 Prompt 互补：Prompt 是整体 system prompt 模板；Skill 是可叠加的能力指令。
// 广场（2026-08-24）：支持发布到广场共享 + 「来自广场」来源标记 + 详情抽屉（useCases/examples）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth, fetchJSON, fetchList, fetchSettled, respError, apiError } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import { CATEGORIES, catLabel } from '@/api/marketplace'
import { usePublish } from '@/composables/usePublish'
import type { components } from '@/api/skills.gen'

interface Skill {
  id: string; name: string; description: string
  instructions: string; category?: string; useCases?: string; examples?: string
  installedFrom?: string; enabled: boolean; createdAt: string
}
interface Agent { id: string; name: string; skills: string[] | null }
type SkillTemplate = components['schemas']['Template']

const router = useRouter()
const skills = ref<Skill[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)
// 主数据失败文案（''=正常）：与「真的没有技能」区分开
const loadError = ref('')
const referencesKnown = ref(false)
const tab = ref<'mine' | 'templates'>('mine')
const templates = ref<SkillTemplate[]>([])
const templatesError = ref('')
const templatesLoading = ref(false)
const installing = ref('')
const previewTemplate = ref<SkillTemplate | null>(null)

async function loadTemplates() {
  templatesLoading.value = true
  templatesError.value = ''
  try {
    templates.value = await fetchList<SkillTemplate>('/api/skills/templates')
  } catch (e) {
    templatesError.value = apiError(e, '模板目录加载失败')
  } finally {
    templatesLoading.value = false
  }
}

async function installTemplate(t: SkillTemplate) {
  if (installing.value) return
  installing.value = t.slug
  try {
    await fetchJSON<Skill>(`/api/skills/templates/${encodeURIComponent(t.slug)}/install`, { method: 'POST' })
    ElMessage.success('已复制到我的技能；重复安装保留现有编辑，可绑定 Agent 后试用')
    tab.value = 'mine'
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '安装失败'))
  } finally {
    installing.value = ''
  }
}

async function downloadTemplate(t: SkillTemplate) {
  try {
    const resp = await fetchAuth(`/api/skills/templates/${encodeURIComponent(t.slug)}/download`)
    if (!resp.ok) throw new Error(await respError(resp, '下载失败：'))
    const url = URL.createObjectURL(await resp.blob())
    const link = document.createElement('a')
    link.href = url
    link.download = `${t.slug}-SKILL.md`
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    ElMessage.success(`请保存到 ${t.slug}/SKILL.md 后由外部客户端加载`)
  } catch (e) {
    ElMessage.error(apiError(e, '下载失败'))
  }
}

const bindTarget = ref<Skill | null>(null)
const bindAgentId = ref('')
const binding = ref(false)
async function bindAndTry() {
  if (!bindTarget.value || !bindAgentId.value || binding.value) return
  binding.value = true
  try {
    // 回读完整配置，保留模型、工具、知识库等字段，不用列表摘要覆盖 Agent。
    const current = await fetchJSON<Agent>(`/api/agents/${encodeURIComponent(bindAgentId.value)}`)
    const selected = [...new Set([...(current.skills || []), bindTarget.value.id])]
    await fetchJSON(`/api/agents/${encodeURIComponent(current.id)}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...current, skills: selected }),
    })
    bindTarget.value = null
    await router.push({ path: '/playground', query: { model: `agent:${current.id}` } })
  } catch (e) {
    ElMessage.error(apiError(e, '绑定失败'))
  } finally {
    binding.value = false
  }
}

// 被 Agent 引用计数（skill id -> N 个 Agent 使用）
const usage = computed(() => {
  const m: Record<string, string[]> = {}
  for (const a of agents.value) {
    for (const sid of a.skills || []) {
      ;(m[sid] ||= []).push(a.name)
    }
  }
  return m
})

async function load() {
  loading.value = true
  loadError.value = ''
  referencesKnown.value = false
  try {
    // 主数据与引用数据独立结算（AI-01）：Agent 引用失败不拖垮技能列表
    const [s, a] = await Promise.all([
      fetchSettled<Skill[]>('/api/skills', '技能'),
      fetchSettled<Agent[]>('/api/agents', '智能体'),
    ])
    if (s) skills.value = s
    else loadError.value = '技能列表加载失败，请重试'
    if (a) { agents.value = a; referencesKnown.value = true }
  } finally {
    loading.value = false
  }
}

// 创建/编辑弹窗
const showForm = ref(false)
// 请求中禁重复提交（§3：连点不重复建）
const submitting = ref(false)
const editing = ref<Skill | null>(null)
const form = ref<Skill>(emptyForm())

function emptyForm(): Skill {
  return { id: '', name: '', description: '', instructions: '', enabled: true, createdAt: '' }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showForm.value = true
}

function openEdit(s: Skill) {
  editing.value = s
  form.value = { ...s }
  showForm.value = true
}

async function submit() {
  if (submitting.value) return
  submitting.value = true
  try {
  const f = form.value
  if (!f.name || !f.instructions) {
    ElMessage.warning('名称与指令内容必填')
    return
  }
  const method = editing.value ? 'PUT' : 'POST'
  const url = editing.value ? `/api/skills/${editing.value.id}` : '/api/skills'
  // 创建时空 createdAt="" 会让后端 time.Time 解析失败（invalid body）——创建请求不带该字段
  const body: Record<string, unknown> = { ...f }
  if (!body.createdAt) delete body.createdAt
  if (!body.id) delete body.id
  try {
    const resp = await fetchAuth(url, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
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
  } finally {
    submitting.value = false
  }
}

async function remove(s: Skill) {
  if (!referencesKnown.value) {
    ElMessage.warning('引用情况未知，请重新加载后再删除')
    return
  }
  const used = usage.value[s.id]
  if (used?.length) {
    ElMessage.warning(`该 Skill 正被 ${used.length} 个 Agent 引用（${used.join('、')}），请先在 Agent 中解绑`)
    return
  }
  try {
    await ElMessageBox.confirm(`确定删除 Skill「${s.name}」？`, '删除确认', { type: 'warning' })
    const resp = await fetchAuth(`/api/skills/${s.id}`, { method: 'DELETE' })
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

// 详情抽屉（instructions + useCases + examples 全文查看）
const detail = ref<Skill | null>(null)

// 发布到广场（composable：选分类/确认/发布/分类回写）
const { publish } = usePublish('skill', async (row, category) => {
  await fetchAuth(`/api/skills/${row.id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...row, category }),
  })
}, load)

onMounted(() => { load(); loadTemplates() })
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2>Skill</h2>
        <p class="sub">可复用的能力指令包，绑定到 Agent 后在运行时注入——一个 Agent 可组合多个 Skill</p>
      </div>
      <el-button type="primary" @click="openCreate">新建 Skill</el-button>
    </div>
    <div class="skill-tabs">
      <el-button :type="tab === 'mine' ? 'primary' : 'default'" @click="tab = 'mine'">我的技能</el-button>
      <el-button :type="tab === 'templates' ? 'primary' : 'default'" @click="tab = 'templates'">平台模板与对外技能</el-button>
    </div>
    <template v-if="tab === 'templates'">
      <p class="sub">平台指令模板复制后绑定 Agent 使用；外部技能由有 HTTP 能力的客户端加载。安装不代表依赖已就绪，也不自动获得权限。</p>
      <StatePanel v-if="templatesError" :title="templatesError" error><el-button @click="loadTemplates">重试</el-button></StatePanel>
      <el-table v-else v-loading="templatesLoading" :data="templates">
        <el-table-column prop="name" label="模板" min-width="140" />
        <el-table-column prop="description" label="用途" min-width="240" />
        <el-table-column label="运行方式" width="130"><template #default="{ row }">{{ row.runtime === 'instructions' ? '平台 Agent 指令' : '外部 Agent' }}</template></el-table-column>
        <el-table-column prop="version" label="版本" width="80" />
        <el-table-column label="操作" width="250">
          <template #default="{ row }">
            <el-button link @click="previewTemplate = row">说明与示例</el-button>
            <el-button v-if="row.runtime === 'instructions'" link type="primary" :loading="installing === row.slug" @click="installTemplate(row)">复制到我的技能</el-button>
            <el-button v-else link type="primary" @click="downloadTemplate(row)">下载 SKILL.md</el-button>
          </template>
        </el-table-column>
      </el-table>
    </template>
    <template v-else>
    <StatePanel v-if="loadError" :title="loadError" description="列表加载失败与「暂无技能」是两种状态，重试或检查服务后再来。" error><button class="act" @click="load">重新加载</button></StatePanel>
    <el-table v-else v-loading="loading" :data="skills">
      <template #empty>
        <el-empty description="暂无 Skill，去广场逛逛或创建一个把常用能力沉淀为可复用指令" :image-size="64">
          <el-button @click="tab = 'templates'">选择平台模板</el-button>
          <el-button @click="router.push('/ai/explore')">逛广场</el-button>
          <el-button type="primary" @click="openCreate">新建 Skill</el-button>
        </el-empty>
      </template>
      <el-table-column prop="name" label="名称" min-width="140">
        <template #default="{ row }">
          <el-link type="primary" :underline="false" @click="detail = row">{{ row.name }}</el-link>
          <el-tag v-if="row.installedFrom" size="small" type="warning" class="from-tag">{{ row.installedFrom.startsWith('builtin:') ? '平台模板副本' : '来自广场' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="分类" width="100">
        <template #default="{ row }">
          <el-tag size="small" type="info">{{ catLabel(row.category) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
      <el-table-column label="被引用" min-width="140">
        <template #default="{ row }">
          <span v-if="!referencesKnown" class="dim">引用情况未知</span>
          <template v-else-if="usage[row.id]?.length">
            <el-tooltip :content="usage[row.id].join('、')">
              <el-tag size="small" type="primary">{{ usage[row.id].length }} 个 Agent</el-tag>
            </el-tooltip>
          </template>
          <span v-else class="dim">未引用</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="310" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" link @click="detail = row">详情</el-button>
          <el-button size="small" type="primary" link @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="primary" link :disabled="!row.enabled || !referencesKnown" @click="bindTarget = row; bindAgentId = ''">绑定试用</el-button>
          <el-button size="small" type="primary" link @click="publish(row)">发布</el-button>
          <el-button size="small" type="danger" link :disabled="!referencesKnown" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
    </template>

    <el-dialog :model-value="!!bindTarget" title="绑定到 Agent 后试用" width="520px" @close="bindTarget = null">
      <p>将「{{ bindTarget?.name }}」加入所选 Agent，保留已有能力；绑定后前往 Playground 手动发起推理。</p>
      <el-select v-model="bindAgentId" placeholder="选择已有 Agent" filterable style="width: 100%">
        <el-option v-for="a in agents" :key="a.id" :label="a.name" :value="a.id" />
      </el-select>
      <p v-if="!agents.length"><el-link @click="router.push('/ai/agents')">尚无 Agent，先去创建</el-link></p>
      <template #footer><el-button @click="bindTarget = null">取消</el-button><el-button type="primary" :disabled="!bindAgentId" :loading="binding" @click="bindAndTry">绑定并前往试用</el-button></template>
    </el-dialog>

    <el-drawer :model-value="!!previewTemplate" :title="previewTemplate?.name" size="520px" @close="previewTemplate = null">
      <template v-if="previewTemplate">
        <p>{{ previewTemplate.description }}</p>
        <p>版本 {{ previewTemplate.version }} · {{ previewTemplate.license }} · {{ previewTemplate.runtime === 'instructions' ? '平台 Agent 指令' : '外部 Agent 技能' }}</p>
        <h4>适用场景</h4><pre class="content">{{ previewTemplate.useCases }}</pre>
        <h4>示例与检查点</h4><pre class="content">{{ previewTemplate.examples }}</pre>
        <h4>指令</h4><pre class="content">{{ previewTemplate.instructions }}</pre>
        <p class="dim">内容 SHA-256：{{ previewTemplate.contentHash }}</p>
      </template>
    </el-drawer>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detail" :title="detail?.name" size="480px">
      <template v-if="detail">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="说明">{{ detail.description || '—' }}</el-descriptions-item>
          <el-descriptions-item label="分类">{{ catLabel(detail.category) }}</el-descriptions-item>
          <el-descriptions-item label="被引用">{{ !referencesKnown ? '引用情况未知' : usage[detail.id]?.length ? usage[detail.id].join('、') : '未引用' }}</el-descriptions-item>
          <el-descriptions-item v-if="detail.installedFrom" label="来源">
            <span v-if="detail.installedFrom.startsWith('builtin:')">{{ detail.installedFrom }}</span>
            <el-link v-else type="primary" @click="router.push('/ai/explore')">广场</el-link>
          </el-descriptions-item>
        </el-descriptions>
        <h4 class="sec">指令内容</h4>
        <pre class="content">{{ detail.instructions }}</pre>
        <template v-if="detail.useCases">
          <h4 class="sec">适用场景</h4>
          <pre class="content">{{ detail.useCases }}</pre>
        </template>
        <template v-if="detail.examples">
          <h4 class="sec">使用示例</h4>
          <pre class="content">{{ detail.examples }}</pre>
        </template>
      </template>
    </el-drawer>

    <el-dialog v-model="showForm" :title="editing ? '编辑 Skill' : '新建 Skill'" width="640px">
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如 写周报 / SQL 专家 / 客服话术" />
        </el-form-item>
        <el-form-item label="分类">
          <el-select v-model="form.category" placeholder="选择分类（发布广场时作为筛选维度）" clearable style="width: 100%">
            <el-option v-for="c in CATEGORIES" :key="c.value" :label="c.label" :value="c.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="2" placeholder="一句话用途（给管理员看）" />
        </el-form-item>
        <el-form-item label="指令内容">
          <el-input
            v-model="form.instructions" type="textarea" :rows="8"
            placeholder="给 LLM 的能力指令（做什么/怎么做/约束），运行时注入 system prompt"
          />
        </el-form-item>
        <el-form-item label="适用场景">
          <el-input v-model="form.useCases" type="textarea" :rows="3" placeholder="什么场景下适合用这个 Skill（给人看，降低试用门槛）" />
        </el-form-item>
        <el-form-item label="使用示例">
          <el-input v-model="form.examples" type="textarea" :rows="4" placeholder="markdown：输入 → 期望输出 示例" />
        </el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showForm = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="submit">{{ submitting ? '保存中…' : '保存' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.skill-tabs { display: flex; gap: 8px; margin-bottom: 16px; }
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.page-header h2 { margin: 0; }
.sub { margin: 4px 0 0; color: var(--el-text-color-secondary); font-size: 13px; }
.dim { color: var(--el-text-color-placeholder); font-size: 12px; }
.from-tag { margin-left: 6px; }
.sec { margin: 18px 0 8px; color: var(--el-text-color-secondary); font-size: 13px; }
.content {
  white-space: pre-wrap; word-break: break-word; margin: 0;
  background: var(--el-fill-color-light); padding: 10px; border-radius: 6px;
  font-size: 12.5px; line-height: 1.6; max-height: 320px; overflow: auto;
}
</style>
