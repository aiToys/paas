<script setup lang="ts">
// 应用详情 - 代码仓库 tab：绑定/解绑 Git 仓库（构建来源）。
// 一站式：支持「内置 Gitea 创建」（PaaS 调 Gitea API 建仓，内网 clone）+「绑定外部仓库」（填 gitUrl）。
// 内置仓库支持平台内浏览（文件树/提交历史），外部仓库仅记录 gitUrl 供构建 clone。
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import RepoBrowser from './RepoBrowser.vue'

const router = useRouter()

const props = defineProps<{ appId: string; focusRepoId?: string }>()
const emit = defineEmits<{ (e: 'repoFocused'): void }>()

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

// Commit 是仓库最近提交（列表「最近提交」列展示，省得用户逐个点浏览抽屉才看到）。
interface Commit {
  sha: string
  message: string
  author: string
  date: string
}

const repos = ref<Repo[]>([])
// repoId -> 最近一次提交（internal 仓库 load 后并发拉取；external 无浏览能力不拉）。
const latestCommits = ref<Record<string, Commit>>({})
const loading = ref(false)
const showBind = ref(false)
// 来源：internal（内置 Gitea 创建）/ external（绑定外部 gitUrl）
const source = ref<'internal' | 'external'>('internal')
const form = ref({ giteaRepo: '', branch: 'main', gitUrl: '', dockerfile: 'Dockerfile', buildContext: '.' })

// 内置创建：必填 giteaRepo；外部绑定：必填 gitUrl
const canSubmit = computed(() =>
  source.value === 'internal' ? !!form.value.giteaRepo.trim() : !!form.value.gitUrl.trim(),
)

// 短 sha（前 8 位，GitHub/Gitea 惯例）。
function shortSha(sha: string): string {
  return sha ? sha.slice(0, 8) : ''
}

// 相对时间（如「2 小时前」），列表紧凑展示比绝对时间更易扫。
function relTime(date: string): string {
  if (!date) return ''
  const t = new Date(date).getTime()
  if (Number.isNaN(t)) return ''
  const diff = Date.now() - t
  const m = Math.floor(diff / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d} 天前`
  return new Date(date).toLocaleDateString()
}

// 展示 clone 命令（后端从 PAAS_GITEA_EXTERNAL_URL 运行时填充；凭证占位由用户替换）。
async function showClone(row: Repo & { cloneCommand?: string }) {
  try {
    await ElMessageBox.alert(row.cloneCommand, '本机克隆命令（替换 <用户名>:<密码> 为 Git 凭证）：', {
      confirmButtonText: '复制命令',
      distinguishCancelAndClose: true,
    })
    await navigator.clipboard.writeText(row.cloneCommand || '')
    ElMessage.success('已复制')
  } catch { /* 直接关闭 */ }
}

// 拉单个 internal 仓库最近 1 条提交（best-effort，失败静默——浏览抽屉仍可看全部）。
async function loadLatestCommit(repoId: string) {
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/repositories/${repoId}/commits?limit=1`)
    if (resp.ok) {
      const arr = (await resp.json()).data ?? []
      if (arr.length > 0) latestCommits.value[repoId] = arr[0]
    }
  } catch {
    /* external/不可达：留空，列表显示「—」 */
  }
}

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/repositories`)
    if (resp.ok) {
      repos.value = (await resp.json()).data ?? []
      // 并发拉每个 internal 仓库最近提交（external 无浏览能力跳过）。
      latestCommits.value = {}
      await Promise.all(
        repos.value.filter((r) => r.source === 'internal').map((r) => loadLatestCommit(r.id)),
      )
    }
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
    ElMessage.error(apiError(e, '操作失败'))
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
    ElMessage.error(apiError(e, '解绑失败'))
  }
}

// 浏览抽屉
const browseRepo = ref<Repo | null>(null)

// 深链定位：?repo=<id> 且为内置仓库时自动打开浏览抽屉（从构建/变更详情页跳入）
watch(() => [props.focusRepoId, repos.value.length] as const, ([rid]) => {
  if (!rid) return
  const r = repos.value.find((x) => x.id === rid)
  if (r && r.source === 'internal') {
    browseRepo.value = r
    emit('repoFocused')
  }
})

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
      <el-table-column label="最近提交" min-width="320">
        <template #default="{ row }">
          <div v-if="latestCommits[row.id]" class="commit-cell">
            <span class="commit-sha mono">{{ shortSha(latestCommits[row.id].sha) }}</span>
            <span class="commit-msg" :title="latestCommits[row.id].message">{{ latestCommits[row.id].message }}</span>
            <span class="commit-time">{{ relTime(latestCommits[row.id].date) }}</span>
          </div>
          <span v-else class="commit-empty">{{ row.source === 'internal' ? '—' : '外部仓库（点浏览查看）' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="180">
        <template #default="{ row }">
          <el-button v-if="row.source === 'internal'" text size="small" @click="browseRepo = row">浏览</el-button>
          <el-button v-if="row.source === 'internal'" text size="small" @click="router.push(`/apps/${props.appId}/repositories/${row.id}/pulls`)">评审</el-button>
          <!-- 迭代旅程审计：开发者需要本机可用的 clone 命令（集群内 FQDN 本机不可达） -->
          <el-button v-if="row.cloneCommand" text size="small" type="primary" @click="showClone(row)">克隆</el-button>
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
.commit-cell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.commit-sha { flex: 0 0 auto; color: var(--brand, #6366f1); font-size: 12px; }
.commit-msg { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; }
.commit-time { flex: 0 0 auto; color: var(--text-3, #94a3b8); font-size: 11.5px; }
.commit-empty { color: var(--text-3, #94a3b8); font-size: 12px; }
</style>
