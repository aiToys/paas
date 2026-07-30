<script setup lang="ts">
// 平台能力 → 安全（密钥/证书 + 审计日志）。
// Secret/证书是租户级平台资产（区别于 appconfig 应用级 Secret）。
// 值后端明文存储、API 掩码返回；写操作（创建/删除）自动记审计，删除走二次确认。
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface Secret {
  id: string; name: string; type: string; scope: string; value: string; desc?: string; updatedAt: string
}
interface AuditLog {
  id: string; actor: string; action: string; resourceType: string
  resourceId: string; detail?: string; at: string
}

const TYPE_SECRET = 'secret'
const TYPE_CERT = 'certificate'
const SCOPE_TENANT = 'tenant'
const SCOPE_PLATFORM = 'platform'

const secrets = ref<Secret[]>([])
const audits = ref<AuditLog[]>([])
const loading = ref(false)
const auditFilter = ref('') // action 过滤

const showCreate = ref(false)
const form = ref({ name: '', type: TYPE_SECRET, scope: SCOPE_TENANT, value: '', desc: '' })
const submitting = ref(false)

const actionOpts = [
  { value: '', label: '全部动作' },
  { value: 'create', label: '创建' },
  { value: 'delete', label: '删除' },
  { value: 'update', label: '更新' },
]

async function loadSecrets() {
  const resp = await fetchAuth('/api/security/secrets')
  if (resp.ok) secrets.value = (await resp.json()).data ?? []
}

async function loadAudits() {
  const q = auditFilter.value ? `?action=${auditFilter.value}` : ''
  const resp = await fetchAuth(`/api/security/audit-logs${q}`)
  if (resp.ok) audits.value = (await resp.json()).data ?? []
}

async function load() {
  loading.value = true
  try {
    await Promise.all([loadSecrets(), loadAudits()])
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.value = { name: '', type: TYPE_SECRET, scope: SCOPE_TENANT, value: '', desc: '' }
  showCreate.value = true
}

async function create() {
  if (!form.value.name.trim() || !form.value.value) {
    ElMessage.warning('请填写名称与值')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth('/api/security/secrets', {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    if (resp.ok) {
      ElMessage.success('已创建（值掩码存储，展示与传输均不可见明文）')
      showCreate.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败')
    }
  } finally {
    submitting.value = false
  }
}

async function remove(row: Secret) {
  const ok = await confirmDangerous({ action: '删除密钥', target: row.name, requireNameConfirm: true })
  if (!ok) return
  const resp = await fetchAuth(`/api/security/secrets/${row.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已删除')
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '删除失败')
  }
}

const actionLabel: Record<string, string> = { create: '创建', delete: '删除', update: '更新' }
const actionType: Record<string, string> = {
  create: 'success', delete: 'danger', update: 'warning',
}

onMounted(load)
</script>

<template>
  <div class="sec-page">
    <div class="page-head">
      <div>
        <h2>安全</h2>
        <p class="sub">租户级密钥/证书管理 + 审计日志（区别于应用配置的应用级 Secret）</p>
      </div>
      <el-button type="primary" @click="openCreate">+ 创建密钥</el-button>
    </div>

    <!-- 密钥/证书 -->
    <section class="block" v-loading="loading">
      <div class="block-title">密钥与证书</div>
      <el-table :data="secrets" size="small" empty-text="暂无密钥资产">
        <el-table-column label="名称" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="类型" width="110">
          <template #default="{ row }">
            <el-tag :type="row.type === TYPE_CERT ? 'warning' : 'info'" size="small">
              {{ row.type === TYPE_CERT ? '证书' : '密钥' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="作用域" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.scope === SCOPE_PLATFORM" type="danger" size="small" effect="plain">平台共享</el-tag>
            <span v-else class="dim">租户私有</span>
          </template>
        </el-table-column>
        <el-table-column label="值" min-width="180">
          <template #default="{ row }"><span class="mono masked">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column prop="desc" label="描述" min-width="160" show-overflow-tooltip />
        <el-table-column label="更新时间" width="170">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 审计日志 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">审计日志</span>
        <el-select v-model="auditFilter" style="width: 140px" @change="loadAudits">
          <el-option v-for="o in actionOpts" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
      </div>
      <el-table :data="audits" size="small" empty-text="暂无审计记录">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ new Date(row.at).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="动作" width="90">
          <template #default="{ row }">
            <el-tag :type="(actionType[row.action] as any) || 'info'" size="small">
              {{ actionLabel[row.action] || row.action }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="actor" label="操作人" width="150" />
        <el-table-column label="资源" width="200">
          <template #default="{ row }"><span class="mono">{{ row.resourceType }}/{{ row.resourceId }}</span></template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" min-width="200" show-overflow-tooltip />
      </el-table>
    </section>

    <!-- 创建弹窗 -->
    <el-dialog v-model="showCreate" title="创建密钥/证书" width="480px">
      <el-form label-width="70px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="租户内唯一，如 db-password" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type">
            <el-radio :value="TYPE_SECRET">密钥</el-radio>
            <el-radio :value="TYPE_CERT">证书</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="作用域">
          <el-radio-group v-model="form.scope">
            <el-radio :value="SCOPE_TENANT">租户私有</el-radio>
            <el-radio :value="SCOPE_PLATFORM">平台共享</el-radio>
          </el-radio-group>
          <div class="scope-hint" v-if="form.scope === SCOPE_PLATFORM">
            平台级凭证全租户共享（如第三方供应商 API Key），仅租户管理员可创建/删除。
          </div>
        </el-form-item>
        <el-form-item label="值">
          <el-input
            v-model="form.value"
            type="textarea"
            :rows="4"
            :placeholder="form.type === TYPE_CERT ? '粘贴 PEM 证书内容' : '敏感值，保存后以掩码展示'"
          />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.desc" placeholder="可选" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="create">
          {{ submitting ? '创建中…' : '创建' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.sec-page { max-width: 1100px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.block { margin-bottom: 24px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.block-title { font-size: 14px; font-weight: 600; }
.masked { color: var(--text-faint); letter-spacing: 2px; }
.dim { font-size: 12.5px; color: var(--text-dim); }
.scope-hint { margin-top: 4px; font-size: 12px; color: var(--el-color-danger); line-height: 1.5; }
</style>
