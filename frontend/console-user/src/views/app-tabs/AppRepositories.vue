<script setup lang="ts">
// 应用详情 - 代码仓库 tab：绑定/解绑 Git 仓库（构建来源）。
// 一站式：支持「内置 Gitea 创建」（PaaS 调 Gitea API 建仓，内网 clone）+「绑定外部仓库」（填 gitUrl）。
// 内置仓库支持平台内浏览（文件树/提交历史），外部仓库仅记录 gitUrl 供构建 clone。
import { ref, computed, onMounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'
import RepoBrowser from './RepoBrowser.vue'

const props = defineProps<{ appId: string }>()

interface Repo {
  id: string
  gitUrl: string
  branch: string
  dockerfile: string
  buildContext: string
  status: string
  source: string // internal | external（空视为 external 兼容历史）
  giteaOwner?: string
  giteaRepo?: string
  createdAt: string
}

const repos = ref<Repo[]>([])
const loading = ref(false)
const showBind = ref(false)
// 来源：internal（内置 Gitea 创建）/ external（绑定外部 gitUrl）
const source = ref<'internal' | 'external'>('internal')
const form = ref({ giteaRepo: '', branch: 'main', gitUrl: '', dockerfile: 'Dockerfile', buildContext: '.' })

// 内置创建：必填 giteaRepo；外部绑定：必填 gitUrl
const canSubmit = computed(() =>
  source.value === 'internal' ? !!form.value.giteaRepo.trim() : !!form.value.gitUrl.trim(),
)

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
  const body =
    source.value === 'internal'
      ? { source: 'internal', giteaRepo: form.value.giteaRepo.trim(), branch: form.value.branch }
      : { source: 'external', gitUrl: form.value.gitUrl.trim(), branch: form.value.branch, dockerfile: form.value.dockerfile, buildContext: form.value.buildContext }
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/repositories`, {
      method: 'POST',
      body: JSON.stringify(body),
    })
    if (resp.ok) {
      ElMessage.success(source.value === 'internal' ? '内置仓库已创建' : '仓库已绑定')
      showBind.value = false
      form.value = { giteaRepo: '', branch: 'main', gitUrl: '', dockerfile: 'Dockerfile', buildContext: '.' }
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '操作失败')
    }
  } catch (e) {
    ElMessage.error('操作失败：' + (e as Error).message)
  }
}

async function unbind(r: Repo) {
  const label = r.source === 'internal' ? r.giteaRepo : r.gitUrl
  try {
    await ElMessageBox.confirm(`确认解绑仓库「${label}」？${r.source === 'internal' ? '（内置 Gitea 仓库本身不会被删除）' : ''}`, '解绑确认', { type: 'warning' })
  } catch {
    return
  }
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/repositories/${r.id}`, { method: 'DELETE' })
    if (resp.ok) {
      ElMessage.success('已解绑')
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '解绑失败')
    }
  } catch (e) {
    ElMessage.error('解绑失败：' + (e as Error).message)
  }
}

// 浏览抽屉
const browseRepo = ref<Repo | null>(null)

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
      <el-table-column label="仓库" min-width="260">
        <template #default="{ row }">
          <div class="repo-cell">
            <el-tag size="small" :type="row.source === 'internal' ? 'success' : 'info'" effect="plain">
              {{ row.source === 'internal' ? '内置' : '外部' }}
            </el-tag>
            <span class="mono">{{ row.source === 'internal' ? row.giteaRepo : row.gitUrl }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="branch" label="分支" width="100" />
      <el-table-column prop="dockerfile" label="Dockerfile" width="110" />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="130">
        <template #default="{ row }">
          <el-button v-if="row.source === 'internal'" text size="small" @click="browseRepo = row">浏览</el-button>
          <el-button text type="danger" size="small" @click="unbind(row)">解绑</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showBind" title="绑定代码仓库" width="500px">
      <el-form label-width="90px">
        <el-form-item label="来源">
          <el-radio-group v-model="source">
            <el-radio value="internal">内置 Gitea 创建</el-radio>
            <el-radio value="external">绑定外部仓库</el-radio>
          </el-radio-group>
        </el-form-item>
        <template v-if="source === 'internal'">
          <el-form-item label="仓库名">
            <el-input v-model="form.giteaRepo" placeholder="小写字母/数字/中划线，如 orders-api" />
          </el-form-item>
          <el-form-item label="默认分支"><el-input v-model="form.branch" /></el-form-item>
        </template>
        <template v-else>
          <el-form-item label="Git URL">
            <el-input v-model="form.gitUrl" placeholder="https://github.com/org/repo.git" />
          </el-form-item>
          <el-form-item label="分支"><el-input v-model="form.branch" /></el-form-item>
          <el-form-item label="Dockerfile"><el-input v-model="form.dockerfile" /></el-form-item>
          <el-form-item label="构建上下文"><el-input v-model="form.buildContext" /></el-form-item>
        </template>
      </el-form>
      <template #footer>
        <el-button @click="showBind = false">取消</el-button>
        <el-button type="primary" :disabled="!canSubmit" @click="bind">
          {{ source === 'internal' ? '创建' : '绑定' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- 内置仓库浏览（文件树 + 提交历史） -->
    <RepoBrowser v-if="browseRepo" :app-id="appId" :repo="browseRepo" @close="browseRepo = null" />
  </div>
</template>

<style scoped>
.devops-tab { padding: 0 4px; }
.tab-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.tab-title { font-size: 14px; font-weight: 600; }
.mono { font-family: var(--mono, ui-monospace, monospace); font-size: 12.5px; word-break: break-all; }
.repo-cell { display: flex; align-items: center; gap: 8px; }
</style>
