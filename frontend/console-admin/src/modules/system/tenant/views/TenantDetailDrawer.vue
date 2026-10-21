<template>
  <el-drawer
    v-model="visible"
    :title="`租户详情：${tenant?.name ?? tenantId}`"
    size="min(960px, 92vw)"
    destroy-on-close
  >
    <div
      v-if="tenant"
      class="tenant-detail"
    >
      <!-- 页头：身份 + 查看该租户资源（显式带租户进范围视图） -->
      <div class="detail-head">
        <div>
          <span class="t-name">{{ tenant.name }}</span>
          <el-tag
            size="small"
            type="info"
          >{{ tenant.id }}</el-tag>
          <!-- 冻结态在详情里也必须可见：列表页标签只是一处，运营从详情进来同样要看到准入已关闭 -->
          <el-tooltip
            v-if="tenant.status === 'frozen'"
            :content="frozenHint"
            placement="top"
          >
            <el-tag
              size="small"
              type="danger"
            >已冻结</el-tag>
          </el-tooltip>
          <span
            v-if="tenant.createdAt"
            class="t-meta"
          >创建于 {{ tenant.createdAt }}</span>
        </div>
        <el-button
          type="primary"
          size="small"
          @click="viewScoped()"
        >查看该租户资源</el-button>
      </div>

      <el-tabs v-model="tab">
        <!-- 概览：开通清单（运营工作区，替代大图表） -->
        <el-tab-pane
          label="概览"
          name="overview"
        >
          <div class="steps">
            <div
              v-for="step in steps"
              :key="step.key"
              class="step"
              :class="{ done: step.done }"
            >
              <div class="step-icon">
                <el-icon v-if="step.done"><CircleCheckFilled /></el-icon>
                <el-icon v-else><CircleClose /></el-icon>
              </div>
              <div class="step-body">
                <div class="step-title">{{ step.title }}</div>
                <div class="step-desc">{{ step.desc }}</div>
              </div>
              <el-button
                v-if="step.action"
                link
                type="primary"
                size="small"
                @click="step.action()"
              >{{ step.actionLabel }}</el-button>
            </div>
          </div>
          <div class="login-hint">
            交付登录入口：租户用户经 <code>/console/</code>（用户控制台）密码登录；
            平台侧不代发凭证，密码由租户管理员在首次登录前经创建用户时设定。
          </div>
        </el-tab-pane>

        <!-- 用户与授权 -->
        <el-tab-pane
          :label="`用户与授权（${users.length}）`"
          name="users"
          lazy
        >
          <el-table
            v-loading="loading.users"
            :data="users"
            size="small"
          >
            <el-table-column
              prop="id"
              label="用户 ID"
              min-width="140"
            />
            <el-table-column
              prop="name"
              label="姓名"
              min-width="100"
            />
            <el-table-column
              label="角色"
              min-width="160"
            >
              <template #default="{ row }">
                <el-tag
                  v-for="r in row.roles || []"
                  :key="r"
                  size="small"
                  class="role-tag"
                >{{ r }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column
              prop="status"
              label="状态"
              width="90"
            />
          </el-table>
        </el-tab-pane>

        <!-- 产品与资源 -->
        <el-tab-pane
          label="产品与资源"
          name="resources"
          lazy
        >
          <div class="res-grid">
            <div
              v-for="r in resCards"
              :key="r.label"
              class="res-card"
            >
              <div class="res-num">{{ r.count }}</div>
              <div class="res-label">{{ r.label }}</div>
              <el-button
                link
                type="primary"
                size="small"
                @click="goList(r.path)"
              >查看</el-button>
            </div>
          </div>
        </el-tab-pane>

        <!-- 用量与账单 -->
        <el-tab-pane
          label="用量与账单"
          name="billing"
          lazy
        >
          <h4 class="sec-title">资源配额</h4>
          <el-table
            v-loading="loading.quotas"
            :data="quotaRows"
            size="small"
          >
            <el-table-column
              prop="resource"
              label="资源"
              min-width="140"
            />
            <el-table-column
              prop="limit"
              label="上限"
              width="90"
            />
          </el-table>
          <h4 class="sec-title">
            未结账单（{{ unpaidBills.length }}）
          </h4>
          <el-table
            v-loading="loading.bills"
            :data="unpaidBills"
            size="small"
          >
            <el-table-column
              prop="period"
              label="周期"
              min-width="120"
            />
            <el-table-column
              prop="total"
              label="金额"
              width="100"
            />
            <el-table-column
              prop="status"
              label="状态"
              width="90"
            />
          </el-table>
        </el-tab-pane>

        <!-- 审计（最近 10 条） -->
        <el-tab-pane
          label="审计"
          name="audit"
          lazy
        >
          <el-table
            v-loading="loading.audit"
            :data="auditLogs"
            size="small"
          >
            <el-table-column
              prop="at"
              label="时间"
              width="160"
            />
            <el-table-column
              prop="actor"
              label="操作者"
              min-width="110"
            />
            <el-table-column
              prop="action"
              label="动作"
              width="90"
            />
            <el-table-column
              prop="detail"
              label="详情"
              min-width="200"
              show-overflow-tooltip
            />
          </el-table>
          <div class="audit-more">
            <el-button
              link
              type="primary"
              size="small"
              @click="goAudit"
            >查看完整审计</el-button>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
// 租户详情抽屉（spec 2026-10-19 §8.2 运营工作区）：
// 概览以开通清单替代大图表（每步可独立重试/跳转），四类只读子集 tab 复用
// 现有 admin API + 服务端 tenantId 过滤；「查看该租户资源」显式把浏览范围
// 切到该租户并进入列表视图（范围仍是过滤器，不改身份）。
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { CircleCheckFilled, CircleClose } from '@element-plus/icons-vue'
import { api } from '@/lib/http/client'
import { useScopeStore } from '@/app/stores/scope'
import {
  fetchQuotaList,
  fetchBillList,
  fetchAuditLogList,
  type AdminQuota,
  type AdminBill,
  type AdminAuditLog
} from '@/modules/resources/api'
import type { TenantInfo } from '../api'

const props = defineProps<{
  modelValue: boolean
  tenant: TenantInfo | null
}>()

const emit = defineEmits<{ 'update:modelValue': [v: boolean] }>()

const visible = ref(props.modelValue)
watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) loadAll()
})
watch(visible, (v) => emit('update:modelValue', v))

const router = useRouter()
const scopeStore = useScopeStore()
const tab = ref('overview')

interface AdminUserRow {
  id: string
  name: string
  roles?: string[]
  status?: string
}
interface AdminEnvRow { id: string; tenantId: string; name: string }
interface AdminAppRow { id: string; tenantId: string; name: string }
interface AdminDsRow { id: string; tenantId: string; name: string }

const users = ref<AdminUserRow[]>([])
const apps = ref<AdminAppRow[]>([])
const workloads = ref<AdminDsRow[]>([])
const dataservices = ref<AdminDsRow[]>([])
const environments = ref<AdminEnvRow[]>([])
const quotas = ref<AdminQuota[]>([])
const bills = ref<AdminBill[]>([])
const auditLogs = ref<AdminAuditLog[]>([])
const loading = ref({ users: false, res: false, quotas: false, bills: false, audit: false })

const tenantId = computed(() => props.tenant?.id ?? '')

// 冻结提示：只关控制面准入，数据面业务照常运行（措辞与列表页一致）。
const frozenHint = computed(() => {
  const t = props.tenant
  const parts: string[] = []
  if (t?.frozenAt) parts.push(`冻结于 ${new Date(t.frozenAt).toLocaleString()}`)
  parts.push(t?.frozenReason ? `原因：${t.frozenReason}` : '原因未记录')
  parts.push('登录与 API Key 已关闭，数据面业务继续运行')
  return parts.join('；')
})

async function fetchScoped<T>(url: string): Promise<T[]> {
  const list = await api.get<T[]>(url, { params: { tenantId: tenantId.value } })
  return list ?? []
}

async function loadAll() {
  if (!tenantId.value) return
  loading.value = { users: false, res: false, quotas: false, bills: false, audit: false }
  // 并行拉取各域子集（服务端过滤）；互不阻塞，失败各自呈现空态由拦截器提示
  loading.value.users = true
  loading.value.res = true
  loading.value.quotas = true
  loading.value.bills = true
  loading.value.audit = true
  try {
    fetchScoped<AdminUserRow>('/api/admin/users').then((l) => { users.value = l }).finally(() => { loading.value.users = false })
    const res = await Promise.allSettled([
      fetchScoped<AdminAppRow>('/api/admin/applications'),
      fetchScoped<AdminDsRow>('/api/admin/workloads'),
      fetchScoped<AdminDsRow>('/api/admin/dataservices'),
      fetchScoped<AdminEnvRow>('/api/admin/environments')
    ])
    if (res[0].status === 'fulfilled') apps.value = res[0].value
    if (res[1].status === 'fulfilled') workloads.value = res[1].value
    if (res[2].status === 'fulfilled') dataservices.value = res[2].value
    if (res[3].status === 'fulfilled') environments.value = res[3].value
  } finally {
    loading.value.res = false
  }
  const tid = tenantId.value
  const req = { page: 1, size: 100, keyword: '', tenantId: tid } as unknown as Parameters<typeof fetchQuotaList>[0]
  fetchQuotaList(req)
    .then((r) => { quotas.value = r.records ?? [] })
    .finally(() => { loading.value.quotas = false })
  fetchBillList(req)
    .then((r) => { bills.value = r.records ?? [] })
    .finally(() => { loading.value.bills = false })
  fetchAuditLogList({ ...req, size: 10 } as unknown as Parameters<typeof fetchAuditLogList>[0])
    .then((r) => { auditLogs.value = r.records ?? [] })
    .finally(() => { loading.value.audit = false })
}

// 开通清单：每步按真实数据判定，不伪造状态
const quotaConfigured = computed(() => quotas.value.length > 0)

const steps = computed(() => [
  {
    key: 'tenant',
    title: '创建租户',
    desc: '租户实体已存在（即当前详情对象）。',
    done: true
  },
  {
    key: 'admin',
    title: '创建管理员',
    desc: users.value.length
      ? `已有 ${users.value.length} 个用户。`
      : '尚无用户：租户无人可登录，需要创建首个管理员。',
    done: users.value.length > 0,
    actionLabel: '去创建用户',
    action: () => goUsers()
  },
  {
    key: 'quota',
    title: '检查配额',
    desc: quotaConfigured.value
      ? '配额已设定（见「用量与账单」tab）。'
      : '配额未设定：应用/工作负载/数据服务的创建会被配额拦截。',
    done: quotaConfigured.value,
    actionLabel: '查看用量',
    action: () => { tab.value = 'billing' }
  },
  {
    key: 'provision',
    title: '按需分配环境 / 数据服务',
    desc: `环境 ${environments.value.length} 个、数据服务 ${dataservices.value.length} 个（admin 可代建，产品/工作负载由租户自助）。`,
    done: environments.value.length > 0,
    actionLabel: '去数据服务',
    action: () => viewScoped('/resources/dataservices')
  },
  {
    key: 'handover',
    title: '交付登录入口',
    desc: '告知租户访问 /console/ 并用创建的用户密码登录。',
    done: users.value.length > 0
  }
])

const resCards = computed(() => [
  { label: '产品（应用）', count: apps.value.length, path: '/resources/applications' },
  { label: '工作负载', count: workloads.value.length, path: '/resources/workloads' },
  { label: '数据服务', count: dataservices.value.length, path: '/resources/dataservices' },
  { label: '环境', count: environments.value.length, path: '/resources/environments' }
])

const unpaidBills = computed(() => bills.value.filter((b) => b.status === 'unpaid'))

// 配额行：AdminQuota.limits 是 资源名→上限 map（-1=无限），展开为表格行
const quotaRows = computed(() =>
  quotas.value.flatMap((q) =>
    Object.entries(q.limits ?? {}).map(([resource, limit]) => ({
      resource,
      limit: limit === -1 ? '无限' : limit
    }))))

const viewScoped = (path = '/resources/applications') => {
  scopeStore.setTenant(tenantId.value)
  visible.value = false
  void router.push(path)
  ElMessage.success(`浏览范围已切换为 ${props.tenant?.name ?? tenantId.value}`)
}
const goUsers = () => viewScoped('/system/user')
const goAudit = () => viewScoped('/resources/audit-logs')
const goList = (path: string) => viewScoped(path)
</script>

<style scoped>
.tenant-detail {
  padding: 0 4px;
}
.detail-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.t-name {
  font-size: 16px;
  font-weight: 600;
  margin-right: 8px;
}
.t-meta {
  margin-left: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.steps {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.step {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
}
.step-icon {
  font-size: 20px;
  color: var(--el-color-danger);
}
.step.done .step-icon {
  color: var(--el-color-success);
}
.step-body {
  flex: 1;
}
.step-title {
  font-weight: 500;
}
.step-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 2px;
}
.login-hint {
  margin-top: 12px;
  padding: 8px 12px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.res-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.res-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  padding: 14px;
  text-align: center;
}
.res-num {
  font-size: 24px;
  font-weight: 600;
}
.res-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin: 4px 0 8px;
}
.sec-title {
  margin: 12px 0 8px;
  font-size: 14px;
}
.role-tag {
  margin-right: 4px;
}
.audit-more {
  margin-top: 8px;
  text-align: right;
}
@media (max-width: 768px) {
  .res-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
