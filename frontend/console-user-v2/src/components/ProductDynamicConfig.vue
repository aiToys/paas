<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// 产品动态配置（v2 原生，替换跳老版 config-center）：key/value 条目 + 版本化发布 + 回滚。
// envId 跟随顶栏当前环境；数据面经发现端点热更新（无入侵）。
import { onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  fetchAppDynamicConfigs, upsertAppDynamicConfig, deleteAppDynamicConfig,
  publishAppDynamicConfigs, fetchAppPublishes, rollbackAppPublish,
  type DynamicConfigItem, type ConfigPublish,
} from '@/api/configcenter'
import { apiError } from '@/api'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

const items = ref<DynamicConfigItem[]>([])
const publishes = ref<ConfigPublish[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const envId = envStore.currentEnv?.id
    const [its, pubs] = await Promise.all([
      fetchAppDynamicConfigs(props.appId, envId),
      fetchAppPublishes(props.appId, envId).catch(() => []),
    ])
    items.value = its
    publishes.value = pubs
  } catch (e) {
    ElMessage.error(apiError(e, '加载动态配置失败'))
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => [props.appId, envStore.currentEnv?.id] as const, () => { if (props.appId) void load() })

// —— 条目编辑 ——
const editing = ref(false)
const saving = ref(false)
const form = ref({ key: '', value: '' })

function openEdit(item?: DynamicConfigItem) {
  form.value = { key: item?.key ?? '', value: item?.value ?? '' }
  editing.value = true
}
async function save() {
  if (!form.value.key.trim()) {
    ElMessage.warning('请填写 key')
    return
  }
  saving.value = true
  try {
    await upsertAppDynamicConfig(props.appId, { key: form.value.key.trim(), value: form.value.value }, envStore.currentEnv?.id)
    ElMessage.success('已保存（发布后生效）')
    editing.value = false
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '保存失败'))
  } finally {
    saving.value = false
  }
}
async function remove(item: DynamicConfigItem) {
  if (!await confirmAction(`删除配置 ${item.key}？`)) return
  try {
    await deleteAppDynamicConfig(props.appId, item.id, envStore.currentEnv?.id)
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}

// —— 发布与回滚 ——
const publishing = ref(false)
async function publish() {
  publishing.value = true
  try {
    const pub = await publishAppDynamicConfigs(props.appId, envStore.currentEnv?.id)
    // 诚实标注：平台只能确认「已发布」，实例是否已拉取采用无回报通道（spec 专项04 §3）
    ElMessage.success(`已发布 v${pub.version}（客户端采用情况未知——实例下次拉取时生效）`)
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '发布失败'))
  } finally {
    publishing.value = false
  }
}
async function rollback(p: ConfigPublish) {
  if (!await confirmAction(`回滚到版本 ${p.version}？当前版本内容将被替换。`)) return
  try {
    await rollbackAppPublish(props.appId, p.id, envStore.currentEnv?.id)
    ElMessage.success(`已回滚到 v${p.version}`)
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '回滚失败'))
  }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">动态配置</span>
      <span class="card-sub">运行时热更新（环境：{{ envStore.currentEnv?.name ?? '全部' }}），带版本与回滚</span>
      <div class="head-acts">
        <button class="act" @click="openEdit()">新增</button>
        <button class="act primary" :disabled="publishing || !items.length" @click="publish">
          {{ publishing ? '发布中…' : '发布' }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <div v-else-if="!items.length" class="empty-hint">
      暂无配置条目——新增 key/value 后发布，运行中的服务热更新生效
    </div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="kv-table">
      <thead>
        <tr><th>Key</th><th>Value</th><th class="col-act"></th></tr>
      </thead>
      <tbody>
        <tr v-for="it in items" :key="it.id">
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

    <div v-if="publishes.length" class="pub-section">
      <div class="pub-title">发布历史</div>
      <div v-for="p in publishes.slice(0, 5)" :key="p.id" class="pub-row">
        <span class="mono">v{{ p.version }}</span>
        <span class="pub-time">{{ (p.createdAt ?? '').replace('T', ' ').slice(0, 16) }}</span>
        <button class="act small" @click="rollback(p)">回滚</button>
      </div>
    </div>

    <el-dialog v-model="editing" title="配置条目" width="480px" append-to-body>
      <el-form label-position="top" @submit.prevent="save">
        <el-form-item label="Key" required>
          <el-input v-model="form.key" class="mono" placeholder="如 feature.flags.newCheckout" />
        </el-form-item>
        <el-form-item label="Value" required>
          <el-input v-model="form.value" type="textarea" :rows="3" class="mono" placeholder="字符串值（服务端按需解析 JSON/数字）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editing = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.head-acts { margin-left: auto; display: flex; gap: 6px; }
.loading-block { height: 120px; }
.kv-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.kv-table th {
  text-align: left;
  padding: 6px 8px;
  color: var(--text-faint);
  font-weight: 500;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
}
.kv-table td {
  padding: 8px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.kv-table .k { font-weight: 600; }
.kv-table .v { color: var(--text-dim); word-break: break-all; }
.col-act { text-align: right; }
.col-act .act { margin-left: 6px; }
.pub-section { margin-top: 12px; }
.pub-title {
  font-size: 12px;
  color: var(--text-faint);
  margin-bottom: 6px;
}
.pub-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 5px 0;
  font-size: 12.5px;
  border-bottom: 1px dashed var(--border);
}
.pub-time { color: var(--text-faint); font-size: 12px; }
.pub-row .act { margin-left: auto; }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
