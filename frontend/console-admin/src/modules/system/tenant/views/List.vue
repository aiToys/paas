<template>
  <SearchTable
    title="租户管理"
    :loading="loading"
    :data="tableData"
    :columns="columns"
    :pagination="pagination"
    row-key="id"
    @search="handleSearch"
    @reset="handleReset"
    @page-change="handlePageChange"
  >
    <template #search>
      <el-input
        v-model="searchForm.keyword"
        placeholder="搜索租户 ID / 名称"
        clearable
        style="width: 240px"
        @keyup.enter="handleSearch"
      />
    </template>

    <template #actions>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openDrawer">新建租户</el-button>
      <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
    </template>

    <template #col-status="{ row }">
      <el-tooltip
        v-if="row.status === 'frozen'"
        :content="freezeHint(row)"
        placement="top"
      >
        <el-tag type="danger" size="small">已冻结</el-tag>
      </el-tooltip>
      <el-tag v-else type="success" size="small">运行中</el-tag>
    </template>

    <template #col-actions="{ row }">
      <el-button link type="primary" size="small" @click="openDetail(row)">详情</el-button>
      <!-- 冻结/恢复：租户管理写权限或安全写权限（后端同口径）；只读运维只看到「详情」 -->
      <el-button
        v-if="canFreeze"
        link
        :type="row.status === 'frozen' ? 'success' : 'warning'"
        size="small"
        @click="row.status === 'frozen' ? handleUnfreeze(row) : openFreeze(row)"
      >{{ row.status === 'frozen' ? '恢复' : '冻结' }}</el-button>
      <el-dropdown trigger="click" @command="handleCommand($event, row)">
        <el-button link type="primary" size="small">更多</el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="offboarding">注销预检</el-dropdown-item>
            <el-dropdown-item v-if="canWrite" command="delete" divided>删除租户</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </template>
  </SearchTable>

  <TenantFormDrawer v-model="drawerVisible" @success="onFormSuccess" />
  <TenantDetailDrawer
    v-model="detailVisible"
    :tenant="detailTenant"
  />
  <TenantOffboardingDialog v-model="offboardingVisible" :tenant="offboardingTenant" />

  <!-- 冻结：原因必填（后台操作需可追溯）；只关控制面准入，不停数据面业务 -->
  <el-dialog v-model="freezeVisible" title="冻结租户" width="480px">
    <div class="freeze-target">{{ freezeTenantName }}</div>
    <el-alert
      type="warning"
      :closable="false"
      show-icon
      title="冻结只关闭控制面准入（登录 / API Key），已部署的业务继续运行；已登录会话最多 15 分钟后无法续期。"
    />
    <el-input
      v-model="freezeReason"
      type="textarea"
      :rows="3"
      maxlength="200"
      show-word-limit
      placeholder="冻结原因（必填，例如：欠费停服 / 安全事件待查）"
      class="freeze-reason"
    />
    <template #footer>
      <el-button @click="freezeVisible = false">取消</el-button>
      <el-button
        type="warning"
        :loading="freezeSubmitting"
        :disabled="!freezeReason.trim()"
        @click="submitFreeze"
      >确认冻结</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted } from 'vue'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { SearchTable } from '@/app/components'
import { useCrud } from '@/app/composables/useCrud'
import { usePermissionStore } from '@/app/stores/permission'
import type { ColumnDef } from '@/app/components/SearchTable/types'
import {
  fetchTenantList,
  deleteTenant,
  freezeTenant,
  unfreezeTenant,
  type TenantInfo,
  type TenantSearchRequest
} from '../api'
import TenantFormDrawer from './TenantFormDrawer.vue'
import TenantDetailDrawer from './TenantDetailDrawer.vue'
import TenantOffboardingDialog from './TenantOffboardingDialog.vue'

// 平台权限（D5-2 细分角色）：写操作按钮按权限显隐，后端同口径兜底 403。
const permissionStore = usePermissionStore()
const canWrite = computed(() => permissionStore.hasPermission('platform:identity:write'))
// 冻结/恢复 = 安全动作：租户管理写或安全写任一即可（与后端 PlatformPermissionsFor 一致）。
const canFreeze = computed(
  () =>
    canWrite.value || permissionStore.hasPermission('platform:security:write')
)

// 租户详情抽屉（运营工作区）：概览开通清单 + 用户/资源/用量/审计子集
const detailVisible = ref(false)
const detailTenant = ref<TenantInfo | null>(null)
const openDetail = (row: Record<string, unknown>) => {
  detailTenant.value = row as unknown as TenantInfo
  detailVisible.value = true
}

const {
  listData,
  loading,
  pagination,
  searchForm,
  fetchList,
  handleSearch,
  handleReset,
  handlePageChange
} = useCrud<TenantInfo>({
  fetch: (params) => fetchTenantList(params as unknown as TenantSearchRequest),
  defaultSearchForm: { keyword: '' },
  pageSize: 10
})

const tableData = computed(() => listData.value as unknown as Record<string, unknown>[])

const columns = computed<ColumnDef[]>(() => [
  { prop: 'id', label: '租户 ID', minWidth: 160 },
  { prop: 'name', label: '租户名称', minWidth: 160 },
  { prop: 'status', label: '状态', width: 150, slot: 'status' },
  { prop: 'actions', label: '操作', width: 200, fixed: 'right', slot: 'actions' }
])

const drawerVisible = ref(false)
const openDrawer = () => {
  drawerVisible.value = true
}
const onFormSuccess = () => {
  drawerVisible.value = false
  fetchList()
}

// 删除租户：高危操作，输入租户 ID 确认（不可恢复）；core 有用户时返 409 引导先清用户。
const handleDelete = async (row: Record<string, unknown>) => {
  const id = row.id as string
  try {
    await ElMessageBox.prompt(`请输入租户 ID "${id}" 确认删除（不可恢复）：`, '删除租户', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
      inputValidator: (v: string) => v === id || `输入与租户 ID "${id}" 不一致`
    })
  } catch {
    return // 用户取消
  }
  try {
    await deleteTenant(id)
    ElMessage.success('删除成功')
    fetchList()
  } catch {
    // 409（有用户）等由 http 拦截器提示
  }
}

// —— 冻结 / 恢复 ——

const freezeVisible = ref(false)
const freezeSubmitting = ref(false)
const freezeReason = ref('')
const freezeTarget = ref<TenantInfo | null>(null)
const freezeTenantName = computed(() =>
  freezeTarget.value ? `${freezeTarget.value.name}（${freezeTarget.value.id}）` : '')

const openFreeze = (row: Record<string, unknown>) => {
  freezeTarget.value = row as unknown as TenantInfo
  freezeReason.value = ''
  freezeVisible.value = true
}

const submitFreeze = async () => {
  const target = freezeTarget.value
  if (!target || !freezeReason.value.trim()) return
  freezeSubmitting.value = true
  try {
    await freezeTenant(target.id, freezeReason.value.trim())
    ElMessage.success(`已冻结 ${target.name}`)
    freezeVisible.value = false
    fetchList()
  } catch {
    // 失败由 http 拦截器提示；保持弹窗与已填原因，便于重试
  } finally {
    freezeSubmitting.value = false
  }
}

const handleUnfreeze = async (row: Record<string, unknown>) => {
  const target = row as unknown as TenantInfo
  let reason = ''
  try {
    const { value } = await ElMessageBox.prompt(
      `恢复租户「${target.name}」的控制面准入（登录 / API Key）？`,
      '恢复租户',
      { confirmButtonText: '恢复', cancelButtonText: '取消', inputPlaceholder: '恢复原因（可选）' }
    )
    reason = (value ?? '').trim()
  } catch {
    return // 用户取消
  }
  try {
    await unfreezeTenant(target.id, reason)
    ElMessage.success(`已恢复 ${target.name}`)
    fetchList()
  } catch {
    // 拦截器已提示
  }
}

const freezeHint = (row: Record<string, unknown>) => {
  const parts: string[] = []
  if (row.frozenAt) parts.push(`冻结于 ${new Date(String(row.frozenAt)).toLocaleString()}`)
  parts.push(row.frozenReason ? `原因：${row.frozenReason}` : '原因未记录')
  return parts.join('；')
}

// —— 注销预检 ——

const offboardingVisible = ref(false)
const offboardingTenant = ref<TenantInfo | null>(null)

const handleCommand = (cmd: string | number | object, row: Record<string, unknown>) => {
  if (cmd === 'delete') {
    void handleDelete(row)
    return
  }
  if (cmd === 'offboarding') {
    offboardingTenant.value = row as unknown as TenantInfo
    offboardingVisible.value = true
  }
}

onMounted(() => {
  fetchList()
})
</script>

<style scoped>
.freeze-target {
  font-weight: 600;
  margin-bottom: 8px;
}
.freeze-reason {
  margin-top: 12px;
}
</style>
