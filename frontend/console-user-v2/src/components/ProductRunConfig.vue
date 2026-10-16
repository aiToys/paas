<script setup lang="ts">
// 产品运行配置（v2 原生，替换跳老版 appconfig tab）：工作负载级 env/Secret 键值，
// 静态注入——改完重启生效。与 ProductDynamicConfig（运行时热更新）严格区分。
// 依赖顶栏环境；Secret 后端掩码 ••••••（编辑不回填真值，需重新输入）。
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

interface ConfigItem {
  id: string; envId: string; key: string; value: string
  type: 'env' | 'secret'; updatedAt: string
}

const items = ref<ConfigItem[]>([])
const loading = ref(true)
const envId = computed(() => envStore.currentEnv?.id)

// env 明文一组 / secret 凭证一组（启动注入的真实敏感凭证）。
const envItems = computed(() => items.value.filter((i) => i.type === 'env'))
const secretItems = computed(() => items.value.filter((i) => i.type === 'secret'))

let loadReqId = 0
async function load() {
  const reqId = ++loadReqId
  if (!envId.value) {
    items.value = []
    loading.value = false
    return
  }
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/configs?envId=${envId.value}`)
    if (reqId !== loadReqId) return // 竞态丢弃：切环境后旧响应迟到
    if (resp.ok) items.value = (await resp.json()).data ?? []
  } finally {
    if (reqId === loadReqId) loading.value = false
  }
}
onMounted(load)
watch(() => [props.appId, envId.value] as const, () => { if (envId.value) void load(); else items.value = [] })

// —— 编辑浮层（同 key upsert；secret 编辑不回填） ——
const showEdit = ref(false)
const submitting = ref(false)
const form = ref<{ id: string; key: string; value: string; type: 'env' | 'secret' }>({ id: '', key: '', value: '', type: 'env' })
const isEditSecret = computed(() => !!form.value.id && form.value.type === 'secret')

function openAdd() {
  form.value = { id: '', key: '', value: '', type: 'env' }
  showEdit.value = true
}
function openEdit(row: ConfigItem) {
  form.value = { id: row.id, key: row.key, value: row.type === 'secret' ? '' : row.value, type: row.type }
  showEdit.value = true
}
async function submit() {
  if (!form.value.key.trim() || !form.value.value) {
    ElMessage.warning(isEditSecret.value ? '编辑 Secret 需重新输入值' : '请填写 Key 与 Value')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/configs`, {
      method: 'POST',
      body: JSON.stringify({ envId: envId.value, key: form.value.key.trim(), value: form.value.value, type: form.value.type }),
    })
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}))
      throw new Error(err.error || '保存失败')
    }
    ElMessage.success(form.value.id ? '已更新（重启后生效）' : '已新增（重启后生效）')
    showEdit.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '保存失败'))
  } finally {
    submitting.value = false
  }
}

async function remove(row: ConfigItem) {
  // 生产删除高危：输入 Key 确认（老版同款语义）。
  const ok = await confirmDangerous({ action: '删除配置', target: row.key, requireNameConfirm: envStore.isProd })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/configs/${row.id}`, { method: 'DELETE' })
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}))
      throw new Error(err.error || '删除失败')
    }
    ElMessage.success('已删除（重启后生效）')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">运行配置</span>
      <span class="card-sub">启动注入（环境：{{ envStore.currentEnv?.name ?? '请先选择环境' }}），改完重启生效</span>
      <button class="act primary add-btn" :disabled="!envId" @click="openAdd">新增</button>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <div v-else-if="!items.length" class="empty-hint">
      {{ envId ? '暂无配置——环境变量与凭证在此注入，工作负载启动时读取' : '先在顶栏选择环境' }}
    </div>
    <template v-else>
      <div v-if="envItems.length" class="sec-title">环境变量（{{ envItems.length }}）</div>
      <div class="table-scroll" data-table-scroll tabindex="0" v-if="envItems.length">
<table class="kv-table">
        <thead><tr><th>Key</th><th>Value</th><th class="col-act"></th></tr></thead>
        <tbody>
          <tr v-for="it in envItems" :key="it.id">
            <td><span class="mono k">{{ it.key }}</span></td>
            <td><span class="mono v">{{ it.value }}</span></td>
            <td class="col-act">
              <button class="act" @click="openEdit(it)">编辑</button>
              <button class="act danger" @click="remove(it)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
</div>

      <div v-if="secretItems.length" class="sec-title">凭证 / 密钥（{{ secretItems.length }}，掩码展示）</div>
      <div class="table-scroll" data-table-scroll tabindex="0" v-if="secretItems.length">
<table class="kv-table">
        <thead><tr><th>Key</th><th>Value</th><th class="col-act"></th></tr></thead>
        <tbody>
          <tr v-for="it in secretItems" :key="it.id">
            <td><span class="mono k">{{ it.key }}</span></td>
            <td><span class="mono v">••••••</span></td>
            <td class="col-act">
              <button class="act" @click="openEdit(it)">编辑</button>
              <button class="act danger" @click="remove(it)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
</div>
    </template>

    <el-dialog v-model="showEdit" title="配置条目" width="480px" append-to-body>
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item label="类型">
          <el-radio-group v-model="form.type" :disabled="!!form.id">
            <el-radio value="env">环境变量</el-radio>
            <el-radio value="secret">凭证（掩码存储）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="Key" required>
          <el-input v-model="form.key" class="mono" placeholder="如 DATABASE_HOST" />
        </el-form-item>
        <el-form-item :label="isEditSecret ? 'Value（需重新输入）' : 'Value'" required>
          <el-input
            v-model="form.value" type="textarea" :rows="3" class="mono"
            :placeholder="isEditSecret ? '原值不回填，输入新值覆盖' : '字符串值'"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showEdit = false">取消</button>
        <button class="act primary" :disabled="submitting" @click="submit">{{ submitting ? '保存中…' : '保存' }}</button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.add-btn { margin-left: auto; }
.loading-block { height: 120px; }
.kv-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.kv-table th {
  text-align: left;
  padding: 6px 8px;
  color: var(--text-faint);
  font-weight: 500;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
}
.kv-table td { padding: 8px; border-bottom: 1px solid var(--border); }
.kv-table .k { font-weight: 600; }
.kv-table .v { color: var(--text-dim); word-break: break-all; }
.col-act { text-align: right; }
.col-act .act { margin-left: 6px; }
</style>
