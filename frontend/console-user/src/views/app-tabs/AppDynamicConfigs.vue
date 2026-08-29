<script setup lang="ts">
// 应用详情 - 动态配置（热更新）区块：应用维度动态配置（scope=app）。
// 与上方 AppConfigs（工作负载级静态 env/Secret，重启注入）正交：本区块是
// 版本化动态配置——draft 编辑 → 发布出不可变快照 → 客户端按版本发现 → 可回滚。
// 发布/回滚高危走 confirmDangerous（生产 scope 输入应用名确认）。
import { onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import {
  fetchAppDynamicConfigs, upsertAppDynamicConfig, deleteAppDynamicConfig,
  publishAppDynamicConfigs, fetchAppPublishes, fetchAppPublished, rollbackAppPublish,
  type DynamicConfigItem, type ConfigPublish, type ConfigPublished,
} from '@/api/configcenter'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

const items = ref<DynamicConfigItem[]>([])
const publishes = ref<ConfigPublish[]>([])
const published = ref<ConfigPublished | null>(null)
const loading = ref(false)
const publishing = ref(false)

const showEdit = ref(false)
const submitting = ref(false)
const form = ref<{ id: string; key: string; value: string; type: string }>({ id: '', key: '', value: '', type: 'text' })

const types = [
  { value: 'text', label: 'Text' },
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
]

const isProd = () => envStore.isProd
const snapshotEntries = (snap?: Record<string, string>) => (snap ? Object.entries(snap) : [])

async function load() {
  loading.value = true
  try {
    const [its, pubs, pub] = await Promise.all([
      fetchAppDynamicConfigs(props.appId),
      fetchAppPublishes(props.appId),
      fetchAppPublished(props.appId).catch(() => null),
    ])
    items.value = its ?? []
    publishes.value = pubs ?? []
    published.value = pub
  } catch (e) {
    ElMessage.error('加载动态配置失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

function openAdd() {
  form.value = { id: '', key: '', value: '', type: 'text' }
  showEdit.value = true
}

function openEdit(row: DynamicConfigItem) {
  form.value = { id: row.id, key: row.key, value: row.value, type: row.type }
  showEdit.value = true
}

async function submit() {
  if (!form.value.key.trim() || !form.value.value) {
    ElMessage.warning('请填写 Key 和 Value')
    return
  }
  submitting.value = true
  try {
    await upsertAppDynamicConfig(props.appId, {
      key: form.value.key.trim(),
      value: form.value.value,
      type: form.value.type,
    })
    ElMessage.success('已保存（draft，发布后生效）')
    showEdit.value = false
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '保存失败')
  } finally {
    submitting.value = false
  }
}

async function remove(row: DynamicConfigItem) {
  const ok = await confirmDangerous({
    action: '删除动态配置项',
    target: row.key,
    requireNameConfirm: isProd(),
  })
  if (!ok) return
  try {
    await deleteAppDynamicConfig(props.appId, row.id)
    ElMessage.success('已删除（draft，发布后生效）')
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '删除失败')
  }
}

async function publish() {
  const ok = await confirmDangerous({
    action: '发布动态配置',
    target: props.appId,
    requireNameConfirm: isProd(),
  })
  if (!ok) return
  publishing.value = true
  try {
    const pub = await publishAppDynamicConfigs(props.appId)
    ElMessage.success(`已发布 v${pub.version}`)
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '发布失败')
  } finally {
    publishing.value = false
  }
}

async function rollback(p: ConfigPublish) {
  const ok = await confirmDangerous({ action: '回滚到', target: `v${p.version}`, requireNameConfirm: isProd() })
  if (!ok) return
  try {
    await rollbackAppPublish(props.appId, p.id)
    ElMessage.success(`已回滚到 v${p.version}`)
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '回滚失败')
  }
}

onMounted(load)
watch(() => props.appId, load)
</script>

<template>
  <div class="dyn-block" v-loading="loading">
    <div class="block-head">
      <div>
        <span class="block-title">动态配置（热更新）</span>
        <span class="block-hint">运行时热更新（无需重启）· 添加配置后发布生效</span>
      </div>
      <div>
        <el-button size="small" :loading="publishing" @click="publish">发布当前 draft</el-button>
        <el-button size="small" type="primary" @click="openAdd">+ 新增配置</el-button>
      </div>
    </div>

    <!-- 当前生效 -->
    <section class="cfg-group">
      <div class="group-title">
        当前生效
        <span v-if="published?.published" class="ver-tag mono">v{{ published.version }}</span>
        <span v-else class="none">未发布</span>
      </div>
      <div v-if="published?.published && snapshotEntries(published.snapshot).length" class="kv-list">
        <div v-for="[k, v] in snapshotEntries(published.snapshot)" :key="k" class="kv-row">
          <span class="kv-key mono">{{ k }}</span>
          <span class="kv-val mono">{{ v }}</span>
        </div>
      </div>
      <div v-else class="empty-line">尚未发布任何版本</div>
    </section>

    <!-- draft 配置项 -->
    <section class="cfg-group">
      <div class="group-title">配置项（draft）<span class="group-cnt mono">{{ items.length }}</span></div>
      <el-table :data="items" size="small" empty-text="动态配置用于运行时热更新（无需重启）。添加第一项配置后发布生效。">
        <el-table-column prop="key" label="Key" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column prop="type" label="类型" width="80" />
        <el-table-column prop="value" label="Value" min-width="220" show-overflow-tooltip>
          <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="170">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 发布历史 -->
    <section class="cfg-group" v-if="publishes.length">
      <div class="group-title">发布历史</div>
      <el-table :data="publishes" size="small">
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
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-button v-if="row.status !== 'active'" text type="warning" size="small" @click="rollback(row)">回滚到此</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 编辑弹窗 -->
    <el-dialog v-model="showEdit" :title="form.id ? '编辑动态配置' : '新增动态配置'" width="500px">
      <el-form label-width="70px">
        <el-form-item label="Key">
          <el-input v-model="form.key" :disabled="!!form.id" placeholder="如 feature.newui" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%">
            <el-option v-for="t in types" :key="t.value" :label="t.label" :value="t.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="Value">
          <el-input v-model="form.value" type="textarea" :rows="4" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEdit = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="submit">
          {{ submitting ? '保存中…' : '保存' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.dyn-block { margin-top: 8px; }
.block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.block-title { font-size: 14px; font-weight: 600; margin-right: 10px; }
.block-hint { font-size: 12px; color: var(--text-dim); }
.cfg-group { margin-bottom: 20px; }
.group-title {
  display: flex; align-items: center; gap: 8px;
  font-size: 13px; font-weight: 600; color: var(--text-dim); margin-bottom: 8px;
}
.group-cnt { font-size: 11px; color: var(--text-faint); padding: 1px 7px; background: var(--surface-2, transparent); border-radius: 8px; }
.ver-tag { padding: 2px 8px; background: var(--success-soft); color: var(--success); border-radius: 4px; font-size: 12px; }
.none { font-size: 12px; color: var(--text-faint); font-weight: 400; }
.kv-list { display: flex; flex-direction: column; gap: 6px; padding: 14px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); }
.kv-row { display: flex; gap: 16px; font-size: 13px; }
.kv-key { color: var(--brand); min-width: 200px; }
.kv-val { color: var(--text); word-break: break-all; }
.empty-line { font-size: 12.5px; color: var(--text-faint); }
</style>
