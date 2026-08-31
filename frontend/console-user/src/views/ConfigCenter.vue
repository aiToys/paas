<script setup lang="ts">
// 平台能力 → 配置中心（治理四件套：运行时动态配置）。
// 命名空间列表 + 命名空间详情（draft 配置项编辑 + 发布历史 + 发布/回滚 + 客户端发现视图）。
// 与 appconfig（工作负载级、静态、重启注入）正交：本页是版本化动态配置，跨实例共享，热更新。
// 配置中心独立于物理环境；发布/回滚高危走 confirmDangerous（统一二次确认）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, fetchJSON } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import DetailShell from '@/components/DetailShell.vue'
import AppDynamicConfigs from './app-tabs/AppDynamicConfigs.vue'

const route = useRoute()
const router = useRouter()

interface Namespace { id: string; name: string; scope?: string; serviceId?: string; desc?: string; updatedAt: string }
interface Service { id: string; name: string }
interface ConfigItem { id: string; namespaceId: string; key: string; value: string; type: string; updatedAt: string }
interface Publish {
  id: string; namespaceId: string; version: number;
  snapshot: Record<string, string>; status: string; createdAt: string
}
interface Published { published: boolean; version?: number; snapshot?: Record<string, string>; publishId?: string }

const namespaces = ref<Namespace[]>([])
const services = ref<Service[]>([])
const cur = ref<Namespace | null>(null)

// 双视图：按应用（主路径，应用维度动态配置）/ 共享配置（ns 维度，既有逻辑原样保留）。
const viewMode = ref<'app' | 'shared'>('app')
const apps = ref<{ id: string; name: string }[]>([])
const selectedAppId = ref('')

async function loadApps() {
  try {
    apps.value = await fetchJSON<{ id: string; name: string }[]>('/api/applications')
  } catch (e) {
    apps.value = []
    ElMessage.error('加载应用列表失败：' + (e as Error).message)
  }
}
const items = ref<ConfigItem[]>([])
const publishes = ref<Publish[]>([])
const published = ref<Published | null>(null)
const loading = ref(false)

const showItem = ref(false)
const itemForm = ref({ id: '', key: '', value: '', type: 'text' })
const itemSubmitting = ref(false)

const showNs = ref(false)
const nsForm = ref({ name: '', serviceId: '', desc: '' })
const nsSubmitting = ref(false)
const rollingBack = ref('')

const types = [
  { value: 'text', label: 'Text' },
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
]

function isDetail() {
  return !!route.params.nsId
}

// viewMode 切换同步路由 query：按应用视图清掉 ?serviceId= 残留（防刷新跳回共享视图），
// 共享视图保留已有 serviceId（服务详情跳转入口语义）。
function onModeChange(mode: 'app' | 'shared') {
  if (mode === 'app') {
    if (route.query.serviceId) router.replace({ query: { ...route.query, serviceId: undefined } })
  } else if (route.query.serviceId) {
    // 保持 serviceId（无需动作）
  }
}

async function loadNamespaces() {
  try {
    const resp = await fetchAuth('/api/configcenter/namespaces')
    if (resp.ok)
      // 共享视图只展示手工命名空间：scope=app 的应用派生 ns 归应用详情「动态配置」区块管理（写路径后端已 403 拒绝）。
      namespaces.value = ((await resp.json()).data ?? []).filter((n: Namespace) => n.scope !== 'app')
    else namespaces.value = []
    loadRefCounts()
  } catch (e) {
    namespaces.value = []
    ElMessage.error('加载命名空间失败：' + (e as Error).message)
  }
}

// 影响面：各 shared ns 被多少应用引用（发布时的影响面提示；失败静默——非关键信息）。
const refCounts = ref<Record<string, number>>({})
async function loadRefCounts() {
  for (const n of namespaces.value) {
    fetchAuth(`/api/configcenter/namespaces/${n.id}/ref-users`)
      .then(async r => { if (r.ok) refCounts.value[n.id] = ((await r.json()).data ?? []).length })
      .catch(() => {})
  }
}

// 引用方列表（详情展示 + 发布影响面确认）。
interface RefUser { id: string; appNsId: string; appNsName?: string; createdAt: string }
const refUsers = ref<RefUser[]>([])
async function refUserList(nsId: string): Promise<RefUser[]> {
  const resp = await fetchAuth(`/api/configcenter/namespaces/${nsId}/ref-users`)
  if (!resp.ok) return []
  return (await resp.json()).data ?? []
}

// 服务列表（关联服务下拉用，governance 租户内全部服务）。
async function loadServices() {
  const resp = await fetchAuth('/api/services')
  if (resp.ok) services.value = (await resp.json()).data ?? []
}
const serviceName = (id: string) => services.value.find((s) => s.id === id)?.name ?? id

async function loadDetail() {
  const id = route.params.nsId as string
  if (!id) return
  loading.value = true
  try {
    const [nr, ir, pr, drr] = await Promise.all([
      fetchAuth(`/api/configcenter/namespaces/${id}`),
      fetchAuth(`/api/configcenter/namespaces/${id}/items`),
      fetchAuth(`/api/configcenter/namespaces/${id}/publishes`),
      fetchAuth(`/api/configcenter/namespaces/${id}/published`),
    ])
    if (nr.ok) cur.value = (await nr.json()).data ?? null
    if (ir.ok) items.value = (await ir.json()).data ?? []
    if (pr.ok) publishes.value = (await pr.json()).data ?? []
    if (drr.ok) published.value = await drr.json()
    refUsers.value = await refUserList(id).catch(() => [])
  } catch (e) {
    ElMessage.error('加载命名空间详情失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

async function load() {
  if (isDetail()) await loadDetail()
  else await loadNamespaces()
}

function openNamespace(id: string) {
  router.push(`/platform/config-center/${id}`)
}

function createNamespace() {
  nsForm.value = { name: '', serviceId: '', desc: '' }
  showNs.value = true
}

async function submitNs() {
  if (!nsForm.value.name.trim()) {
    ElMessage.warning('请输入名称')
    return
  }
  nsSubmitting.value = true
  try {
    const resp = await fetchAuth('/api/configcenter/namespaces', {
      method: 'POST', body: JSON.stringify(nsForm.value),
    })
    if (resp.ok) {
      ElMessage.success('已创建')
      showNs.value = false
      loadNamespaces()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败')
    }
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    nsSubmitting.value = false
  }
}

function openItem(existing?: ConfigItem) {
  itemForm.value = existing
    ? { id: existing.id, key: existing.key, value: existing.value, type: existing.type }
    : { id: '', key: '', value: '', type: 'text' }
  showItem.value = true
}

async function saveItem() {
  if (!itemForm.value.key.trim() || !itemForm.value.value) {
    ElMessage.warning('请填写 Key 和 Value')
    return
  }
  if (!cur.value) return
  itemSubmitting.value = true
  try {
    const resp = await fetchAuth(`/api/configcenter/namespaces/${cur.value.id}/items`, {
      method: 'POST',
      body: JSON.stringify({ key: itemForm.value.key, value: itemForm.value.value, type: itemForm.value.type }),
    })
    if (resp.ok) {
      ElMessage.success('已保存，发布后生效')
      showItem.value = false
      loadDetail()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '保存失败')
    }
  } catch (e) {
    ElMessage.error('保存失败：' + (e as Error).message)
  } finally {
    itemSubmitting.value = false
  }
}

// 删除命名空间（级联清 item+publish，高危：输入命名空间名确认）。
async function deleteNamespace(row: Namespace) {
  const ok = await confirmDangerous({ action: '删除命名空间', target: row.name, requireNameConfirm: true })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/configcenter/namespaces/${row.id}`, { method: 'DELETE' })
    if (resp.ok) {
      ElMessage.success('已删除命名空间')
      loadNamespaces()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '删除失败')
    }
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

async function deleteItem(row: ConfigItem) {
  if (!cur.value) return
  const ok = await confirmDangerous({ action: '删除配置项', target: row.key })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/configcenter/namespaces/${cur.value.id}/items/${row.id}`, { method: 'DELETE' })
    if (resp.ok) {
      ElMessage.success('已删除')
      loadDetail()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '删除失败')
    }
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

// draft vs active 是否有变更（发布按钮 disabled 依据；后端 ErrNoChanges 409 兜底）
const hasChanges = computed(() => {
  const snap = published.value?.snapshot ?? {}
  const byKey = new Map(items.value.map(i => [i.key, i.value]))
  if (byKey.size !== Object.keys(snap).length) return true
  for (const [k, v] of byKey) {
    if (!(k in snap) || snap[k] !== v) return true
  }
  return false
})

async function publish() {
  if (!cur.value) return
  const users = await refUserList(cur.value.id).catch(() => [])
  const impact = users.length > 0 ? `该共享配置正被 ${users.length} 个应用引用，发布后引用方将自动热更新。` : ''
  const ok = await confirmDangerous({ action: '发布', target: cur.value.name ?? '', message: impact })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/configcenter/namespaces/${cur.value.id}/publish`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success('已发布新版本')
      loadDetail()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '发布失败')
    }
  } catch (e) {
    ElMessage.error('发布失败：' + (e as Error).message)
  }
}

async function rollback(p: Publish) {
  const ok = await confirmDangerous({ action: '回滚到', target: `v${p.version}` })
  if (!ok) return
  rollingBack.value = p.id
  try {
    const resp = await fetchAuth(`/api/configcenter/publishes/${p.id}/rollback`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success(`已回滚到 v${p.version}`)
      loadDetail()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '回滚失败')
    }
  } catch (e) {
    ElMessage.error('回滚失败：' + (e as Error).message)
  } finally {
    rollingBack.value = ''
  }
}

const snapshotEntries = (snap?: Record<string, string>) => (snap ? Object.entries(snap) : [])

onMounted(async () => {
  // ?serviceId= 路由参数兼容（服务详情「关联配置」跳转）：进入时切共享视图
  if (route.query.serviceId) viewMode.value = 'shared'
  await Promise.all([loadServices(), load(), loadApps()])
})
watch(() => route.params.nsId, load)
</script>

<template>
  <div class="cc-page">
    <!-- 列表视图 -->
    <template v-if="!isDetail()">
      <div class="page-head">
        <div>
          <h2>配置中心</h2>
          <p class="sub">运行时动态配置 · 版本/发布/回滚 · 跨实例共享（区别于应用配置的静态注入）</p>
        </div>
        <div style="display: flex; align-items: center; gap: 12px">
          <el-radio-group v-model="viewMode" size="small" @change="(m: any) => onModeChange(m)">
            <el-radio-button value="app">按应用</el-radio-button>
            <el-radio-button value="shared">共享配置</el-radio-button>
          </el-radio-group>
          <el-button v-if="viewMode === 'shared'" type="primary" @click="createNamespace">+ 创建命名空间</el-button>
        </div>
      </div>

      <!-- 按应用视图（主路径）：左侧应用列表 + 右侧应用维度动态配置 -->
      <div v-if="viewMode === 'app'" class="app-view">
        <aside class="app-list">
          <div
            v-for="a in apps" :key="a.id"
            class="app-item" :class="{ active: selectedAppId === a.id }"
            @click="selectedAppId = a.id"
          >
            <span class="mono">{{ a.id }}</span>
            <span class="app-name">{{ a.name }}</span>
          </div>
          <div v-if="!apps.length" class="empty-line">暂无应用</div>
        </aside>
        <div class="app-panel">
          <AppDynamicConfigs v-if="selectedAppId" :key="selectedAppId" :app-id="selectedAppId" />
          <div v-else class="empty-line" style="padding: 48px 0; text-align: center">
            从左侧选择一个应用，管理其动态配置。
          </div>
        </div>
      </div>

      <el-table v-else :data="namespaces" v-loading="loading" size="default" empty-text="暂无命名空间">
        <el-table-column label="命名空间" min-width="180">
          <template #default="{ row }">
            <a class="link" @click="openNamespace(row.id)">{{ row.name }}</a>
          </template>
        </el-table-column>
        <el-table-column label="关联服务" min-width="140">
          <template #default="{ row }">
            <a v-if="row.serviceId" class="svc-link" @click="router.push(`/platform/governance/services/${row.serviceId}`)">{{ serviceName(row.serviceId) }}</a>
            <span v-else class="dim">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="desc" label="描述" min-width="200" show-overflow-tooltip />
        <el-table-column label="被引用" width="100">
          <template #default="{ row }">
            <el-tag v-if="refCounts[row.id]" size="small" type="info">{{ refCounts[row.id] }} 应用</el-tag>
            <span v-else class="dim">-</span>
          </template>
        </el-table-column>
        <el-table-column label="更新时间" width="180">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="140">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openNamespace(row.id)">进入</el-button>
            <el-button text type="danger" size="small" @click="deleteNamespace(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <!-- 详情视图 -->
    <template v-else>
      <DetailShell
        :crumbs="[{ label: '配置中心', to: '/platform/config-center' }, { label: cur?.name ?? (route.params.nsId as string) }]"
        :tags="published?.published
          ? [{ label: `生效中 v${published.version}`, type: 'success' }]
          : [{ label: '未发布', type: 'info' }]"
        :loading="loading && !cur"
      />

      <div v-loading="loading">
        <!-- 配置项（编辑即 draft，点「发布生效」才对客户端可见；生效版本 tag 内联，不单独设展示区） -->
        <section class="block">
          <div class="block-head">
            <span class="block-title">配置项</span>
            <div>
              <span v-if="published?.published" class="ver-tag mono">生效中 v{{ published.version }}</span>
              <span v-else class="none">未发布</span>
              <el-button size="small" :disabled="published?.published && !hasChanges" @click="publish">发布生效</el-button>
              <el-button size="small" type="primary" @click="openItem()">+ 新增配置项</el-button>
            </div>
          </div>
          <el-table :data="items" size="small" empty-text="暂无配置项">
            <el-table-column prop="key" label="Key" min-width="160">
              <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
            </el-table-column>
            <el-table-column prop="type" label="类型" width="80" />
            <el-table-column prop="value" label="Value" min-width="200" show-overflow-tooltip />
            <el-table-column label="更新时间" width="170">
              <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
            </el-table-column>
            <el-table-column label="操作" width="120">
              <template #default="{ row }">
                <el-button text type="primary" size="small" @click="openItem(row)">编辑</el-button>
                <el-button text type="danger" size="small" @click="deleteItem(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </section>

        <!-- 发布历史 -->
        <section class="block">
          <div class="block-head">
            <span class="block-title">发布历史</span>
          </div>
          <el-table :data="publishes" size="small" empty-text="暂无发布记录">
            <el-table-column label="版本" width="80">
              <template #default="{ row }"><span class="mono">v{{ row.version }}</span></template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row }">
                <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="配置项数" width="100">
              <template #default="{ row }">{{ snapshotEntries(row.snapshot).length }}</template>
            </el-table-column>
            <el-table-column label="发布时间" width="180">
              <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
            </el-table-column>
            <el-table-column label="操作" width="90">
              <template #default="{ row }">
                <el-button
                  v-if="row.status !== 'active'"
                  text type="warning" size="small"
                  :loading="rollingBack === row.id"
                  @click="rollback(row)"
                >
回滚到此
</el-button>
              </template>
            </el-table-column>
          </el-table>
        </section>

        <!-- 引用方（影响面：哪些应用派生 ns 引用了此共享配置） -->
        <section class="block">
          <div class="block-head">
            <span class="block-title">引用方</span>
            <span class="dim">引用本共享配置的应用（发布后自动热更新）</span>
          </div>
          <el-table :data="refUsers" size="small" empty-text="暂无应用引用。可在应用详情「配置」tab 引用本共享配置。">
            <el-table-column label="应用命名空间" min-width="200">
              <template #default="{ row }"><span class="mono">{{ row.appNsName || row.appNsId }}</span></template>
            </el-table-column>
            <el-table-column label="引用时间" width="180">
              <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
            </el-table-column>
          </el-table>
        </section>
      </div>

      <!-- 配置项编辑弹窗 -->
      <el-dialog v-model="showItem" :title="itemForm.id ? '编辑配置项' : '新增配置项'" width="500px">
        <el-form label-width="70px">
          <el-form-item label="Key">
            <el-input v-model="itemForm.key" :disabled="!!itemForm.id" placeholder="如 feature.newui" />
          </el-form-item>
          <el-form-item label="类型">
            <el-select v-model="itemForm.type" style="width: 100%">
              <el-option v-for="t in types" :key="t.value" :label="t.label" :value="t.value" />
            </el-select>
          </el-form-item>
          <el-form-item label="Value">
            <el-input v-model="itemForm.value" type="textarea" :rows="4" />
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="showItem = false">取消</el-button>
          <el-button type="primary" :disabled="itemSubmitting" @click="saveItem">
            {{ itemSubmitting ? '保存中…' : '保存' }}
          </el-button>
        </template>
      </el-dialog>
</template>

    <!-- 创建命名空间（挂两视图之外：按钮在共享列表视图，弹窗错挂详情 v-else 分支时列表页不渲染 → 点击无反应） -->
    <el-dialog v-model="showNs" title="创建命名空间" width="460px">
      <el-form label-width="80px">
        <el-form-item label="名称">
          <el-input v-model="nsForm.name" placeholder="租户内唯一" />
        </el-form-item>
        <el-form-item label="关联服务">
          <el-select v-model="nsForm.serviceId" clearable placeholder="可选，关联 governance 服务" style="width: 100%">
            <el-option v-for="s in services" :key="s.id" :label="s.name" :value="s.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="nsForm.desc" type="textarea" :rows="2" placeholder="可选" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showNs = false">取消</el-button>
        <el-button type="primary" :disabled="nsSubmitting" @click="submitNs">
          {{ nsSubmitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.cc-page { max-width: 1100px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.link { font-weight: 600; color: var(--brand); cursor: pointer; }
.link:hover { text-decoration: underline; }
.dim { color: var(--text-dim); }
.block { margin-bottom: 24px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-title { font-size: 14px; font-weight: 600; }
.ver-tag { padding: 2px 8px; background: var(--success-soft); color: var(--success); border-radius: 4px; font-size: 12px; }
.none { font-size: 12px; color: var(--text-faint); }
.kv-list { display: flex; flex-direction: column; gap: 6px; padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.kv-row { display: flex; gap: 16px; font-size: 13px; }
.kv-key { color: var(--brand); min-width: 200px; }
.kv-val { color: var(--text); word-break: break-all; }
.svc-link { color: var(--brand); cursor: pointer; font-weight: 500; }
.svc-link:hover { text-decoration: underline; }
.app-view { display: flex; gap: 20px; align-items: flex-start; }
.app-list { width: 220px; flex-shrink: 0; display: flex; flex-direction: column; gap: 4px; }
.app-item {
  display: flex; flex-direction: column; gap: 2px; padding: 8px 10px;
  border: 1px solid var(--border); border-radius: var(--radius); cursor: pointer; font-size: 13px;
}
.app-item:hover { background: var(--surface-2, transparent); }
.app-item.active { border-color: var(--brand); background: var(--surface-2, transparent); }
.app-item .mono { font-size: 11px; color: var(--text-faint); }
.app-name { font-weight: 600; }
.app-panel { flex: 1; min-width: 0; }
.empty-line { font-size: 12.5px; color: var(--text-faint); }
</style>
