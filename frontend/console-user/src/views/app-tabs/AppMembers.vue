<script setup lang="ts">
// 应用成员与权限（应用级权限）：成员 CRUD + 受限模式开关。
// restricted=false：租户级 RBAC 即可写（现状）；开启后写操作需成员角色匹配
// （app-developer 不可发布等）——「测试人员无发布权限」的落地面。
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, apiError } from '@/api'

interface Member { id: string; appId: string; userId: string; userName?: string; role: string; createdAt?: string }
interface AppInfo { id: string; name: string; restricted?: boolean }
interface UserItem { id: string; name: string; email?: string }

const props = defineProps<{ app: AppInfo }>()

const members = ref<Member[]>([])
const users = ref<UserItem[]>([])
const loading = ref(false)

const ROLE_LABEL: Record<string, string> = {
  'app-owner': '所有者',
  'app-maintainer': '维护者（可发布）',
  'app-developer': '开发者（不可发布）',
  'app-viewer': '只读',
}
const ROLE_TYPE: Record<string, string> = {
  'app-owner': 'danger',
  'app-maintainer': 'warning',
  'app-developer': 'primary',
  'app-viewer': 'info',
}

const restricted = ref(false)
const switching = ref(false)

async function load() {
  loading.value = true
  try {
    const [ms, us] = await Promise.all([
      fetchJSON<Member[]>(`/api/applications/${props.app.id}/members`),
      fetchJSON<UserItem[]>('/api/users').catch(() => [] as UserItem[]),
    ])
    members.value = ms
    users.value = us
    restricted.value = !!props.app.restricted
  } catch {
    // 成员接口 404/403 不阻塞 tab（应用可能未开启受限）
    members.value = []
  } finally {
    loading.value = false
  }
}
onMounted(load)
// 父组件刷新应用详情后（restricted 字段可能变化）同步开关状态。
watch(() => props.app.restricted, (v) => { restricted.value = !!v })

// 非成员可选用户（已成员的不再出现在下拉）
const candidateUsers = computed(() =>
  users.value.filter((u) => !members.value.some((m) => m.userId === u.id)),
)

async function toggleRestricted(v: boolean | string | number) {
  const want = !!v
  if (want) {
    try {
      await ElMessageBox.confirm(
        '开启后，本应用的发布/回滚/审批、工作负载写、绑定/配置写操作需应用成员角色匹配（app-developer 不可发布）。非成员（除租户管理员）将被拒绝。',
        '开启应用级权限',
        { type: 'warning' },
      )
    } catch { return }
  }
  switching.value = true
  try {
    const updated = await fetchJSON<AppInfo>(`/api/applications/${props.app.id}/restrict`, {
      method: 'PUT',
      body: JSON.stringify({ restricted: want }),
    })
    restricted.value = !!updated.restricted
    ElMessage.success(want ? '已开启应用级权限' : '已关闭应用级权限')
  } catch (e) {
    ElMessage.error(apiError(e, '切换失败'))
  } finally {
    switching.value = false
  }
}

const showAdd = ref(false)
const form = ref({ userId: '', role: 'app-developer' })
const submitting = ref(false)

async function add() {
  if (!form.value.userId) { ElMessage.warning('请选择用户'); return }
  submitting.value = true
  try {
    await fetchJSON(`/api/applications/${props.app.id}/members`, {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    ElMessage.success('成员已添加')
    showAdd.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '添加失败'))
  } finally {
    submitting.value = false
  }
}

async function changeRole(m: Member, role: string) {
  try {
    await fetchJSON(`/api/applications/${props.app.id}/members`, {
      method: 'POST',
      body: JSON.stringify({ userId: m.userId, role }),
    })
    ElMessage.success('角色已更新')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '更新失败'))
  }
}

async function remove(m: Member) {
  try {
    await ElMessageBox.confirm(`确认移除成员「${m.userName || m.userId}」？`, '移除成员', { type: 'warning' })
  } catch { return }
  try {
    await fetchJSON(`/api/applications/${props.app.id}/members/${m.userId}`, { method: 'DELETE' })
    ElMessage.success('已移除')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '移除失败'))
  }
}
</script>

<template>
  <div class="members">
    <el-alert
type="info" :closable="false" class="hint"
      title="应用级权限：按应用维度控制成员可执行的动作（角色：所有者 > 维护者 > 开发者 > 只读）"
/>

    <div class="bar">
      <div class="switch-line">
        <el-switch :model-value="restricted" :loading="switching" @change="toggleRestricted" />
        <span class="switch-label">受限模式</span>
        <el-tag v-if="restricted" type="danger" size="small">已开启：写操作需成员角色</el-tag>
        <el-tag v-else type="info" size="small">未开启：租户角色即可写</el-tag>
      </div>
      <el-button type="primary" @click="showAdd = true">添加成员</el-button>
    </div>

    <el-table v-loading="loading" :data="members">
      <template #empty>
        <el-empty :image-size="64" description="暂无成员——开启受限模式前请先添加所有者，否则非管理员将被锁定在只读" />
      </template>
      <el-table-column label="用户" min-width="160">
        <template #default="{ row }">
          {{ row.userName || row.userId }}
          <span class="dim">（{{ row.userId }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="角色" min-width="200">
        <template #default="{ row }">
          <el-select :model-value="row.role" size="small" style="width: 200px" @change="(v: string) => changeRole(row, v)">
            <el-option v-for="(label, r) in ROLE_LABEL" :key="r" :label="label" :value="r" />
          </el-select>
        </template>
      </el-table-column>
      <el-table-column label="权限说明" min-width="220">
        <template #default="{ row }">
          <el-tag :type="(ROLE_TYPE[row.role] ?? 'info') as any" size="small">{{ ROLE_LABEL[row.role] || row.role }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="90">
        <template #default="{ row }">
          <el-button size="small" type="danger" link @click="remove(row)">移除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showAdd" title="添加应用成员" width="460px">
      <el-form label-width="80px">
        <el-form-item label="用户">
          <el-select v-model="form.userId" filterable placeholder="选择本租户用户">
            <el-option v-for="u in candidateUsers" :key="u.id" :label="`${u.name}（${u.id}）`" :value="u.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="角色">
          <el-select v-model="form.role">
            <el-option v-for="(label, r) in ROLE_LABEL" :key="r" :label="label" :value="r" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAdd = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="add">添加</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.members { max-width: 960px; }
.hint { margin-bottom: 12px; }
.bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.switch-line { display: flex; align-items: center; gap: 8px; }
.switch-label { font-weight: 600; }
.dim { color: var(--el-text-color-placeholder); font-size: 12px; }
</style>
