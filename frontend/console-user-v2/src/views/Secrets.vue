<script setup lang="ts">
import PageHeader from '@/components/PageHeader.vue'
import { formatDateTime } from '@/utils/format'
// 平台能力 → 安全（密钥/证书 + 审计日志）。
// Secret/证书是组织级平台资产（区别于 appconfig 应用级 Secret），作用域不随顶栏环境切换。
// 值后端明文存储、API 掩码返回；写操作（创建/删除）自动记审计，删除走二次确认。
// 专项06 §4：删除确认按真实作用域（平台共享需名称确认），引用关系平台未提供查询——
// 按「未知引用」提示，不按「0 引用」提示安全。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth, fetchList, apiError } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import TablePager from '@/components/TablePager.vue'
import { copyText } from '@/utils/clipboard'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

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
const readErrors = ref<Record<string, string>>({})
let auditSeq = 0
const auditFilter = ref('') // action 过滤
// 前端分页：dev 集群实测 1000+ 条密钥全量渲染卡顿（后端 API 无分页参数，slice 同其他列表页模式）。
const page = ref(1)
const pagedSecrets = computed(() => secrets.value.slice((page.value - 1) * 20, page.value * 20))
const auditPage = ref(1)
const pagedAudits = computed(() => audits.value.slice((auditPage.value - 1) * 20, auditPage.value * 20))

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
  readErrors.value.secrets = ''
  try { secrets.value = await fetchList<Secret>('/api/security/secrets') }
  catch (e) { readErrors.value.secrets = apiError(e); secrets.value = [] }
}

async function loadAudits() {
  const q = auditFilter.value ? `?action=${auditFilter.value}` : ''
  const rev = ++auditSeq
  readErrors.value.audits = ''
  auditPage.value = 1
  try { const list = await fetchList<AuditLog>(`/api/security/audit-logs${q}`); if (rev === auditSeq) audits.value = list }
  catch (e) { if (rev === auditSeq) { readErrors.value.audits = apiError(e); audits.value = [] } }
}

async function load() {
  loading.value = true
  try {
    await Promise.all([loadSecrets(), loadAudits()])
  } finally {
    loading.value = false
  }
}

function resetForm() {
  form.value = { name: '', type: TYPE_SECRET, scope: SCOPE_TENANT, value: '', desc: '' }
}

function openCreate() {
  resetForm()
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
      ElMessage.success('保存成功，后续页面仅展示掩码')
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
  // 按真实作用域定确认强度（平台共享影响全租户），不看顶栏环境浏览状态；
  // 引用关系未知（平台未提供查询），不提示「无引用安全」。
  const ok = await confirmDangerous({
    action: row.scope === SCOPE_PLATFORM ? '删除平台共享密钥' : '删除密钥',
    target: row.name,
    requireNameConfirm: row.scope === SCOPE_PLATFORM,
    isProd: row.scope === SCOPE_PLATFORM,
    message: row.scope === SCOPE_PLATFORM
      ? '平台级凭证全租户共享，删除影响所有引用它的通道/工具。'
      : '租户私有凭证。当前无法查询引用关系（未知引用），请自行确认没有消费方后再删除。',
  })
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
const actionType: Record<string, TagType> = {
  create: 'success', delete: 'danger', update: 'warning',
}

onMounted(load)
// 不随顶栏环境切换重拉：Secret 是组织级资产（后端无 envId 维度），切环境不改变作用域（专项06 §4）。
</script>

<template>
  <div class="sec-page">
    <PageHeader title="密钥与凭证" description="集中管理资源访问凭证，并追踪相关审计记录。" eyebrow="组织与访问"><el-button type="primary" @click="openCreate">+ 创建密钥</el-button></PageHeader>

    <!-- 密钥/证书 -->
    <section class="block" v-loading="loading">
      <div class="block-title">密钥与证书</div>
      <p class="scope-note">组织级资产：作用域为租户私有或平台共享，不随顶栏环境切换而变化；当前平台未提供引用关系查询。</p>
      <StatePanel v-if="readErrors.secrets" title="密钥加载失败" :description="readErrors.secrets" error><button class="act" @click="loadSecrets">重试</button></StatePanel>
      <el-table v-else :data="pagedSecrets" size="small" empty-text="暂无密钥资产">
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
          <template #default="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <TablePager v-model="page" :total="secrets.length" />
    </section>

    <!-- 审计日志 -->
    <section class="block">
      <div class="block-head">
        <span class="block-title">审计日志</span>
        <el-select v-model="auditFilter" style="width: 140px" @change="loadAudits">
          <el-option v-for="o in actionOpts" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
      </div>
      <StatePanel v-if="readErrors.audits" title="审计日志加载失败" :description="readErrors.audits" error><button class="act" @click="loadAudits">重试</button></StatePanel>
      <el-table v-else :data="pagedAudits" size="small" empty-text="暂无审计记录">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatDateTime(row.at) }}</template>
        </el-table-column>
        <el-table-column label="动作" width="90">
          <template #default="{ row }">
            <el-tag :type="(actionType[row.action]) || 'info'" size="small">
              {{ actionLabel[row.action] || row.action }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="actor" label="操作人" width="150" />
        <el-table-column label="资源" width="200">
          <template #default="{ row }">
            <span class="mono audit-res" title="点击复制资源标识" @click="copyText(`${row.resourceType}/${row.resourceId}`, '资源标识已复制')">{{ row.resourceType }}/{{ row.resourceId }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" min-width="200" show-overflow-tooltip />
      </el-table>
      <TablePager v-model="auditPage" :total="audits.length" />
    </section>

    <!-- 创建弹窗：@close 清明文草稿（覆盖 X/Esc/遮罩全部关闭路径，不等下次打开才重置） -->
    <el-dialog v-model="showCreate" title="创建密钥/证书" width="480px" @close="resetForm">
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
.scope-note { margin: 0 0 10px; font-size: 12px; color: var(--text-faint); }
.audit-res { cursor: pointer; }
.audit-res:hover { color: var(--brand); }
</style>
