<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 产品成员与权限（v2 原生）：应用级角色（owner/maintainer/developer/viewer）
// + Restricted 受限开关（开启后非成员 fail-closed，防自封提权：成员管理与开关本身仅租户管理员）。
// 专项06 §2：管理控件按真实权限显隐（后端 403 兜底，前端不做角色真源）；
// 成员/用户选择器独立失败态；同名用户以 userId 为身份；角色变更失败回读服务端真实旧角色。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError, fetchSettled } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useSessionStore } from '@/stores/session'

interface Member { id: string; appId: string; userId: string; userName?: string; role: string; createdAt?: string }
interface UserItem { id: string; name: string; email?: string }

const props = defineProps<{ appId: string; restricted: boolean }>()
const emit = defineEmits<{ (e: 'updated', restricted: boolean): void }>()

const members = ref<Member[]>([])
const users = ref<UserItem[]>([])
const loading = ref(true)
// 成员主数据失败文案（''=正常）：失败≠「暂无成员」
const loadError = ref('')
// 可添加用户列表独立失败（专项06 §2）：失败≠「本租户没有其他用户」，禁选并说明
const usersError = ref('')
const restricted = ref(props.restricted)

// 成员管理与受限开关仅租户管理员（后端领域规则；tenant:admin 与 * 通行，与 Role.Grants 同语义）。
// 普通用户只读：不渲染可误点的管理控件（直接 API 越权仍由后端拒绝）。
const session = useSessionStore()
const isTenantAdmin = computed(() => {
  const perms = session.profile?.permissions ?? []
  return perms.includes('*') || perms.includes('tenant:admin')
})

async function load() {
  loading.value = true
  loadError.value = ''
  usersError.value = ''
  try {
    // AC-01：成员主数据与用户列表独立结算——失败≠「暂无成员」（误导权限管理判断）
    const [ms, us] = await Promise.all([
      fetchSettled<Member[]>(`/api/applications/${props.appId}/members`, '应用成员'),
      fetchSettled<UserItem[]>('/api/users', '用户'),
    ])
    if (ms) members.value = ms
    else loadError.value = '成员列表加载失败'
    if (us) users.value = us
    else usersError.value = '用户列表加载失败'
  } finally {
    loading.value = false
  }
}
onMounted(load)

const ROLE_LABEL: Record<string, string> = {
  'app-owner': '负责人', 'app-maintainer': '维护者', 'app-developer': '开发者', 'app-viewer': '观察者',
}
const ROLE_ORDER = ['app-owner', 'app-maintainer', 'app-developer', 'app-viewer']

async function toggleRestricted() {
  const want = !restricted.value
  if (want && !await confirmAction(
    '开启后，本应用的发布/审批、工作负载写、绑定/配置写需应用成员角色匹配；非成员（除租户管理员）将被拒绝。'
    + '你以租户管理员身份操作，开启后保留全部权限。确认开启？')) return
  try {
    const updated = await fetchJSON<{ restricted?: boolean }>(`/api/applications/${props.appId}/restrict`, {
      method: 'PUT',
      body: JSON.stringify({ restricted: want }),
    })
    restricted.value = !!updated.restricted
    emit('updated', restricted.value)
    ElMessage.success(want ? '已开启应用级权限' : '已关闭应用级权限')
  } catch (e) {
    ElMessage.error(apiError(e, '切换失败'))
  }
}

// —— 添加 / 改角色（POST 同 userId upsert）/ 移除 ——
const showAdd = ref(false)
const form = ref({ userId: '', role: 'app-developer' })
const submitting = ref(false)
// 以 userId 为身份（同名用户互不误过滤）；显示层用名称+邮箱区分。
const memberUserIds = computed(() => new Set(members.value.map((m) => m.userId)))
const addableUsers = computed(() => users.value.filter((u) => !memberUserIds.value.has(u.id)))

async function add() {
  if (!form.value.userId) {
    ElMessage.warning('请选择用户')
    return
  }
  submitting.value = true
  try {
    await fetchJSON(`/api/applications/${props.appId}/members`, {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    ElMessage.success('成员已添加')
    showAdd.value = false
    form.value = { userId: '', role: 'app-developer' }
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '添加失败'))
  } finally {
    submitting.value = false
  }
}

async function changeRole(m: Member, role: string) {
  try {
    await fetchJSON(`/api/applications/${props.appId}/members`, {
      method: 'POST',
      body: JSON.stringify({ userId: m.userId, role }),
    })
    ElMessage.success('角色已更新')
  } catch (e) {
    ElMessage.error(apiError(e, '更新失败'))
  }
  // 成功失败都回读服务端真实状态（专项06 §8：失败保留真实旧角色——select 显示值不漂移）
  await load()
}

async function remove(m: Member) {
  const ok = await confirmDangerous({ action: '移除成员', target: m.userName || m.userId, requireNameConfirm: false })
  if (!ok) return
  try {
    await fetchJSON(`/api/applications/${props.appId}/members/${m.userId}`, { method: 'DELETE' })
    ElMessage.success('已移除')
  } catch (e) {
    ElMessage.error(apiError(e, '移除失败'))
  }
  await load()
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">成员与权限</span>
      <span class="card-sub">{{ members.length }} 名成员</span>
      <button v-if="isTenantAdmin" class="act primary add-btn" @click="showAdd = true">添加成员</button>
    </div>

    <div class="restrict-row">
      <label v-if="isTenantAdmin" class="r-toggle">
        <input type="checkbox" :checked="restricted" @change="toggleRestricted" />
        <span>应用级权限（受限模式）——开启后非成员（除租户管理员）无法操作本应用</span>
      </label>
      <span v-else class="r-readonly">{{ restricted ? '受限模式已开启：非成员（除租户管理员）无法操作本应用' : '受限模式未开启：按租户级角色判定，成员管理与开关仅租户管理员可操作' }}</span>
    </div>

    <!-- 专项06 §2：「当前用户为什么可操作」——三类判定分开说明，不猜具体权限结果 -->
    <div class="perm-note">
      <div class="pn-row">
        <span>当前用户</span>
        <span class="pn-user">{{ session.profile?.username || '未知' }}</span>
        <span class="pn-tag" :class="isTenantAdmin ? 'admin' : ''">{{ isTenantAdmin ? '租户管理员' : '普通成员' }}</span>
      </div>
      <ul class="pn-list">
        <li>租户管理员：对本应用全权；成员管理与受限开关仅此角色可操作。</li>
        <li>应用角色：负责人 / 维护者 / 开发者 / 观察者，在受限模式开启时参与应用级判定。</li>
        <li>未开启受限模式时按租户级 RBAC 判定（创建者权限与当前账号相同）。</li>
      </ul>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <div v-else-if="loadError" class="empty-hint err">
      ⚠ {{ loadError }}——加载失败不代表没有成员（受限模式下成员表是权限判断依据），请重试。
      <button class="act" @click="load">重新加载</button>
    </div>
    <div v-else-if="!members.length" class="empty-hint">暂无成员——租户管理员默认全权</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="m-table">
      <thead><tr><th>成员</th><th>角色</th><th v-if="isTenantAdmin" class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="m in members" :key="m.userId">
          <td>
            <div class="m-name">{{ m.userName || m.userId }}</div>
            <div class="m-sub mono">{{ m.userId }}</div>
          </td>
          <td>
            <select v-if="isTenantAdmin" class="role-select" :value="m.role" @change="changeRole(m, ($event.target as HTMLSelectElement).value)">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ ROLE_LABEL[r] ?? r }}</option>
            </select>
            <span v-else>{{ ROLE_LABEL[m.role] ?? m.role }}</span>
          </td>
          <td v-if="isTenantAdmin" class="col-act">
            <button class="act danger" @click="remove(m)">移除</button>
          </td>
        </tr>
      </tbody>
    </table>
</div>

    <el-dialog v-model="showAdd" title="添加成员" width="440px" append-to-body>
      <el-form label-position="top" @submit.prevent="add">
        <el-form-item label="用户" required>
          <select v-model="form.userId" class="u-select" :disabled="!!usersError">
            <option value="" disabled>{{ usersError ? '用户列表加载失败' : '选择本租户用户' }}</option>
            <option v-for="u in addableUsers" :key="u.id" :value="u.id">{{ u.name }}{{ u.email ? `（${u.email}）` : `（${u.id}）` }}</option>
          </select>
          <div v-if="usersError" class="u-err">
            <span>⚠ {{ usersError }}——无法获取可添加用户，请重试；这不代表本租户没有其他用户。</span>
            <button class="act" type="button" @click="load">重新加载</button>
          </div>
        </el-form-item>
        <el-form-item label="角色" required>
          <select v-model="form.role" class="u-select">
            <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ ROLE_LABEL[r] ?? r }}</option>
          </select>
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showAdd = false">取消</button>
        <button class="act primary" :disabled="submitting || !!usersError" @click="add">{{ submitting ? '添加中…' : '添加' }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.add-btn { margin-left: auto; }
.empty-hint.err { color: var(--danger); }
.restrict-row {
  padding: 8px 10px;
  margin-bottom: 10px;
  border: 1px dashed var(--border);
  border-radius: var(--radius);
  font-size: 12.5px;
  color: var(--text-dim);
}
.r-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.r-readonly { line-height: 1.6; }
.perm-note {
  padding: 10px 12px;
  margin-bottom: 12px;
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 12px;
  color: var(--text-dim);
}
.pn-row { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.pn-user { font-weight: 600; color: var(--text); }
.pn-tag {
  padding: 1px 8px;
  border-radius: 999px;
  border: 1px solid var(--border-strong);
  font-size: 11px;
}
.pn-tag.admin { color: var(--brand); border-color: var(--brand); }
.pn-list { margin: 0; padding-left: 16px; line-height: 1.7; }
.loading-block { height: 100px; }
.m-table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.m-table th {
  text-align: left;
  padding: 5px 8px;
  color: var(--text-faint);
  font-weight: 500;
  border-bottom: 1px solid var(--border);
}
.m-table td { padding: 7px 8px; border-bottom: 1px solid var(--border); }
.m-name { font-weight: 500; }
.m-sub { font-size: 11px; color: var(--text-faint); }
.role-select, .u-select {
  padding: 4px 8px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-family: inherit;
  font-size: 12.5px;
}
.u-select { width: 100%; box-sizing: border-box; padding: 7px 10px; }
.u-select:disabled { opacity: 0.6; }
.u-err { margin-top: 6px; color: var(--danger); line-height: 1.6; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.col-act { text-align: right; }
</style>
