<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 产品成员与权限（v2 原生，替换跳老版 members tab）：应用级角色（owner/maintainer/
// developer/viewer）+ Restricted 受限开关（开启后非成员 fail-closed，防自封提权：
// 成员管理与开关本身仅租户管理员）。
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError, fetchList } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface Member { id: string; appId: string; userId: string; userName?: string; role: string; createdAt?: string }
interface UserItem { id: string; name: string; email?: string }

const props = defineProps<{ appId: string; restricted: boolean }>()
const emit = defineEmits<{ (e: 'updated', restricted: boolean): void }>()

const members = ref<Member[]>([])
const users = ref<UserItem[]>([])
const loading = ref(true)
const restricted = ref(props.restricted)

async function load() {
  loading.value = true
  try {
    const [ms, us] = await Promise.all([
      fetchList<Member>(`/api/applications/${props.appId}/members`).catch(() => []),
      fetchList<UserItem>('/api/users').catch(() => []),
    ])
    members.value = ms
    users.value = us
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
  if (want && !await confirmAction('开启后，本应用的发布/审批、工作负载写、绑定/配置写需应用成员角色匹配；非成员（除租户管理员）将被拒绝。确认开启？')) return
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
const memberNames = computed(() => new Set(members.value.map((m) => m.userName || m.userId)))
// 可添加 = 本租户用户且尚未是成员。
const addableUsers = computed(() => users.value.filter((u) => !memberNames.value.has(u.name)))

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
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '更新失败'))
  }
}

async function remove(m: Member) {
  const ok = await confirmDangerous({ action: '移除成员', target: m.userName || m.userId, requireNameConfirm: false })
  if (!ok) return
  try {
    await fetchJSON(`/api/applications/${props.appId}/members/${m.userId}`, { method: 'DELETE' })
    ElMessage.success('已移除')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '移除失败'))
  }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">成员与权限</span>
      <span class="card-sub">{{ members.length }} 名成员</span>
      <button class="act primary add-btn" @click="showAdd = true">添加成员</button>
    </div>

    <div class="restrict-row">
      <label class="r-toggle">
        <input type="checkbox" :checked="restricted" @change="toggleRestricted" />
        <span>应用级权限（受限模式）——开启后非成员（除租户管理员）无法操作本应用</span>
      </label>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <div v-else-if="!members.length" class="empty-hint">暂无成员——租户管理员默认全权</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="m-table">
      <thead><tr><th>成员</th><th>角色</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="m in members" :key="m.userId">
          <td class="m-name">{{ m.userName || m.userId }}</td>
          <td>
            <select class="role-select" :value="m.role" @change="changeRole(m, ($event.target as HTMLSelectElement).value)">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ ROLE_LABEL[r] ?? r }}</option>
            </select>
          </td>
          <td class="col-act">
            <button class="act danger" @click="remove(m)">移除</button>
          </td>
        </tr>
      </tbody>
    </table>
</div>

    <el-dialog v-model="showAdd" title="添加成员" width="440px" append-to-body>
      <el-form label-position="top" @submit.prevent="add">
        <el-form-item label="用户" required>
          <select v-model="form.userId" class="u-select">
            <option value="" disabled>选择本租户用户</option>
            <option v-for="u in addableUsers" :key="u.id" :value="u.id">{{ u.name }}{{ u.email ? `（${u.email}）` : '' }}</option>
          </select>
        </el-form-item>
        <el-form-item label="角色" required>
          <select v-model="form.role" class="u-select">
            <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ ROLE_LABEL[r] ?? r }}</option>
          </select>
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showAdd = false">取消</button>
        <button class="act primary" :disabled="submitting" @click="add">{{ submitting ? '添加中…' : '添加' }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.add-btn { margin-left: auto; }
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
.col-act { text-align: right; }
</style>
