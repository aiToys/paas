<script setup lang="ts">
// AI 服务 -> 提示词（P2）：版本化模板，同 name 多版本，最新版自动激活。
// 创建同 name 自动 version+1 且激活；可手动激活历史版本。
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth, respError } from '@/api'
import { usePublish } from '@/composables/usePublish'

interface Prompt {
  id: string; name: string; template: string
  variables?: string[]
  category?: string; installedFrom?: string
  version: number; active: boolean
  createdAt: string
}

const prompts = ref<Prompt[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    prompts.value = await fetchJSON<Prompt[]>('/api/prompts')
  } catch (e) {
    ElMessage.error('加载提示词失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
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

// 创建弹窗
const showForm = ref(false)
const form = ref({ name: '', template: '', variables: '' })

function openCreate() {
  form.value = { name: '', template: '', variables: '' }
  showForm.value = true
}

async function submit() {
  const f = form.value
  if (!f.name || !f.template) {
    ElMessage.warning('名称与模板必填')
    return
  }
  const body: { name: string; template: string; variables?: string[] } = {
    name: f.name,
    template: f.template,
  }
  if (f.variables.trim()) {
    body.variables = f.variables.split(',').map((s) => s.trim()).filter(Boolean)
  }
  const resp = await fetchAuth('/api/prompts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!resp.ok) {
    ElMessage.error(await respError(resp, '创建失败：'))
    return
  }
  ElMessage.success('已创建（同 name 自动 version+1 且激活）')
  showForm.value = false
  load()
}

async function activate(p: Prompt) {
  await ElMessageBox.confirm(`激活「${p.name} v${p.version}」？`, '激活确认', { type: 'warning' })
  const resp = await fetchAuth(`/api/prompts/${p.id}/activate`, { method: 'POST' })
  if (!resp.ok) {
    ElMessage.error('激活失败')
    return
  }
  ElMessage.success('已激活')
  load()
}

// 发布到广场（发布 active 版本快照）
const { publish } = usePublish('prompt', async (row, category) => {
  // Prompt 无 Update 端点（版本不可变）——分类只随发布快照走，不回写
  void row; void category
}, load)

async function remove(p: Prompt) {
  await ElMessageBox.confirm(`删除「${p.name} v${p.version}」？`, '删除确认', { type: 'warning' })
  const resp = await fetchAuth(`/api/prompts/${p.id}`, { method: 'DELETE' })
  if (!resp.ok) {
    ElMessage.error('删除失败')
    return
  }
  ElMessage.success('已删除')
  load()
}

onMounted(load)
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
              <el-tag v-else type="info">历史</el-tag>
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
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="同 name 自动 version+1 且激活" />
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
        <el-button type="primary" @click="submit">创建</el-button>
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
</style>
