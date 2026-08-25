<script setup lang="ts">
// AI 编排 -> Skill（P3.x）：可复用指令能力包，Agent 绑定后运行时注入 system prompt。
// 与 Prompt 互补：Prompt 是整体 system prompt 模板；Skill 是可叠加的能力指令。
// 广场（2026-08-24）：支持发布到广场共享 + 「来自广场」来源标记 + 详情抽屉（useCases/examples）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth } from '@/api'
import { CATEGORIES, catLabel } from '@/api/marketplace'
import { usePublish } from '@/composables/usePublish'

interface Skill {
  id: string; name: string; description: string
  instructions: string; category?: string; useCases?: string; examples?: string
  installedFrom?: string; enabled: boolean; createdAt: string
}
interface Agent { id: string; name: string; skills: string[] | null }

const router = useRouter()
const skills = ref<Skill[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)

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
  try {
    const [s, a] = await Promise.all([
      fetchJSON<Skill[]>('/api/skills'),
      fetchJSON<Agent[]>('/api/agents'),
    ])
    skills.value = s
    agents.value = a
  } catch (e) {
    ElMessage.error('加载 Skill 失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

// 创建/编辑弹窗
const showForm = ref(false)
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
  const f = form.value
  if (!f.name || !f.instructions) {
    ElMessage.warning('名称与指令内容必填')
    return
  }
  const method = editing.value ? 'PUT' : 'POST'
  const url = editing.value ? `/api/skills/${editing.value.id}` : '/api/skills'
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

async function remove(s: Skill) {
  const used = usage.value[s.id]
  if (used?.length) {
    ElMessage.warning(`该 Skill 正被 ${used.length} 个 Agent 引用（${used.join('、')}），请先在 Agent 中解绑`)
    return
  }
  await ElMessageBox.confirm(`确定删除 Skill「${s.name}」？`, '删除确认', { type: 'warning' })
  const resp = await fetchAuth(`/api/skills/${s.id}`, { method: 'DELETE' })
  if (!resp.ok) {
    ElMessage.error('删除失败')
    return
  }
  ElMessage.success('已删除')
  load()
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

onMounted(load)
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
    <el-table v-loading="loading" :data="skills">
      <template #empty>
        <el-empty description="暂无 Skill，去广场逛逛或创建一个把常用能力沉淀为可复用指令" :image-size="64">
          <el-button @click="router.push('/ai/explore')">逛广场</el-button>
          <el-button type="primary" @click="openCreate">新建 Skill</el-button>
        </el-empty>
      </template>
      <el-table-column prop="name" label="名称" min-width="140">
        <template #default="{ row }">
          <el-link type="primary" :underline="false" @click="detail = row">{{ row.name }}</el-link>
          <el-tag v-if="row.installedFrom" size="small" type="warning" class="from-tag">来自广场</el-tag>
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
          <template v-if="usage[row.id]?.length">
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
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" link @click="detail = row">详情</el-button>
          <el-button size="small" type="primary" link @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="primary" link @click="publish(row)">发布</el-button>
          <el-button size="small" type="danger" link @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detail" :title="detail?.name" size="480px">
      <template v-if="detail">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="说明">{{ detail.description || '—' }}</el-descriptions-item>
          <el-descriptions-item label="分类">{{ catLabel(detail.category) }}</el-descriptions-item>
          <el-descriptions-item label="被引用">{{ usage[detail.id]?.length ? usage[detail.id].join('、') : '未引用' }}</el-descriptions-item>
          <el-descriptions-item v-if="detail.installedFrom" label="来源">
            <el-link type="primary" @click="router.push('/ai/explore')">广场</el-link>
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
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
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
