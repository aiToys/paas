<script setup lang="ts">
// 应用详情 - 代码仓库 tab：绑定/解绑 Git 仓库（构建来源）。
import { ref, onMounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string }>()

interface Repo {
  id: string
  gitUrl: string
  branch: string
  dockerfile: string
  buildContext: string
  status: string
  createdAt: string
}

const repos = ref<Repo[]>([])
const loading = ref(false)
const showBind = ref(false)
const form = ref({ gitUrl: '', branch: 'main', dockerfile: 'Dockerfile', buildContext: '.' })

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/repositories`)
    if (resp.ok) repos.value = (await resp.json()).data ?? []
  } finally {
    loading.value = false
  }
}

async function bind() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/repositories`, {
    method: 'POST',
    body: JSON.stringify(form.value),
  })
  if (resp.ok) {
    ElMessage.success('仓库已绑定')
    showBind.value = false
    form.value = { gitUrl: '', branch: 'main', dockerfile: 'Dockerfile', buildContext: '.' }
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '绑定失败')
  }
}

async function unbind(r: Repo) {
  try {
    await ElMessageBox.confirm(`确认解绑仓库「${r.gitUrl}」？`, '解绑确认', { type: 'warning' })
  } catch {
    return
  }
  const resp = await fetchAuth(`/api/applications/${props.appId}/repositories/${r.id}`, {
    method: 'DELETE',
  })
  if (resp.ok) {
    ElMessage.success('已解绑')
    load()
  }
}

onMounted(load)
watch(() => props.appId, load)
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">代码仓库</span>
      <el-button type="primary" size="small" @click="showBind = true">+ 绑定仓库</el-button>
    </div>
    <el-table :data="repos" v-loading="loading" size="small" empty-text="尚未绑定代码仓库">
      <el-table-column label="仓库地址" min-width="280">
        <template #default="{ row }"><span class="mono">{{ row.gitUrl }}</span></template>
      </el-table-column>
      <el-table-column prop="branch" label="分支" width="100" />
      <el-table-column prop="dockerfile" label="Dockerfile" width="110" />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-button text type="danger" size="small" @click="unbind(row)">解绑</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showBind" title="绑定代码仓库" width="480px">
      <el-form label-width="90px">
        <el-form-item label="Git URL">
          <el-input v-model="form.gitUrl" placeholder="https://github.com/org/repo.git" />
        </el-form-item>
        <el-form-item label="分支"><el-input v-model="form.branch" /></el-form-item>
        <el-form-item label="Dockerfile"><el-input v-model="form.dockerfile" /></el-form-item>
        <el-form-item label="构建上下文"><el-input v-model="form.buildContext" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showBind = false">取消</el-button>
        <el-button type="primary" :disabled="!form.gitUrl" @click="bind">绑定</el-button>
      </template>
    </el-dialog>
  </div>
</template>
