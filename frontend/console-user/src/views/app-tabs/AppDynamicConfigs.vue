<script setup lang="ts">
// 应用详情 - 动态配置（热更新）区块：应用维度动态配置（scope=app，按环境隔离）。
// 与上方 AppConfigs（工作负载级静态 env/Secret，重启注入）正交：本区块是
// 版本化动态配置——draft 编辑 → 发布出不可变快照 → 客户端按版本发现 → 可回滚。
// 环境维度：全部请求带顶栏 scope envId（与 AppConfigs 同款交互），同应用 test/prod
// 各一份独立配置/版本/闸门；发布目标 prod 走 confirmDangerous 生产档。
// 泳道覆盖子区：key 级差异集，即时生效（无版本链），随泳道回收消失。
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useEnvStore } from '@/stores/env'
import { listLanes } from '@/api/lane'
import {
  fetchAppDynamicConfigs, upsertAppDynamicConfig, deleteAppDynamicConfig,
  publishAppDynamicConfigs, fetchAppPublishes, fetchAppPublished, rollbackAppPublish,
  fetchLaneOverrides, upsertLaneOverride, deleteLaneOverride,
  type DynamicConfigItem, type ConfigPublish, type ConfigPublished, type LaneOverride,
} from '@/api/configcenter'

const props = defineProps<{ appId: string }>()

const envStore = useEnvStore()
const envId = computed(() => envStore.currentEnvId)
const envName = computed(() => envStore.currentEnv?.name ?? '')
const hasEnv = computed(() => !!envId.value)

const items = ref<DynamicConfigItem[]>([])
const publishes = ref<ConfigPublish[]>([])
const published = ref<ConfigPublished | null>(null)
const loading = ref(false)
const publishing = ref(false)
const removing = ref('')
const rollingBack = ref('')

const showEdit = ref(false)
const submitting = ref(false)
const form = ref<{ id: string; key: string; value: string; type: string }>({ id: '', key: '', value: '', type: 'text' })

// ---- 泳道覆盖子区状态 ----
const lanes = ref<{ name: string; mode: string }[]>([])
const laneSel = ref('')
const overrides = ref<LaneOverride[]>([])
const laneLoading = ref(false)
const ovRemoving = ref('')
const showOvEdit = ref(false)
const ovSubmitting = ref(false)
const ovForm = ref<{ key: string; value: string }>({ key: '', value: '' })

const types = [
  { value: 'text', label: 'Text' },
  { value: 'json', label: 'JSON' },
  { value: 'yaml', label: 'YAML' },
]

const snapshotEntries = (snap?: Record<string, string>) => (snap ? Object.entries(snap) : [])

async function load() {
  if (!hasEnv.value) return
  loading.value = true
  try {
    const [its, pubs, pub] = await Promise.all([
      fetchAppDynamicConfigs(props.appId, envId.value),
      fetchAppPublishes(props.appId, envId.value),
      fetchAppPublished(props.appId, { envId: envId.value }).catch(() => null),
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

async function loadLanes() {
  if (!hasEnv.value) { lanes.value = []; return }
  try {
    lanes.value = (await listLanes(envId.value) ?? [])
      .filter(l => l.status === 'active')
      .map(l => ({ name: l.name, mode: l.mode }))
  } catch { lanes.value = [] }
}

async function loadOverrides() {
  if (!laneSel.value) { overrides.value = []; return }
  laneLoading.value = true
  try {
    overrides.value = (await fetchLaneOverrides(props.appId, envId.value, laneSel.value)) ?? []
  } catch (e) {
    ElMessage.error('加载泳道覆盖失败：' + (e as Error).message)
  } finally {
    laneLoading.value = false
  }
}

watch(laneSel, loadOverrides)

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
    }, envId.value)
    ElMessage.success('已保存，发布后生效')
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
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  removing.value = row.id
  try {
    await deleteAppDynamicConfig(props.appId, row.id, envId.value)
    ElMessage.success('已删除，发布后生效')
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '删除失败')
  } finally {
    removing.value = ''
  }
}

async function publish() {
  const ok = await confirmDangerous({
    action: '发布动态配置',
    target: envName.value || props.appId,
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  publishing.value = true
  try {
    const pub = await publishAppDynamicConfigs(props.appId, envId.value)
    ElMessage.success(`已发布 v${pub.version}`)
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '发布失败')
  } finally {
    publishing.value = false
  }
}

async function rollback(p: ConfigPublish) {
  const ok = await confirmDangerous({ action: '回滚到', target: `v${p.version}`, requireNameConfirm: envStore.isProd })
  if (!ok) return
  rollingBack.value = p.id
  try {
    await rollbackAppPublish(props.appId, p.id, envId.value)
    ElMessage.success(`已回滚到 v${p.version}`)
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '回滚失败')
  } finally {
    rollingBack.value = ''
  }
}

// ---- 泳道覆盖操作（即时生效，无「发布」步骤） ----

function openOvAdd() {
  ovForm.value = { key: '', value: '' }
  showOvEdit.value = true
}

async function submitOverride() {
  if (!ovForm.value.key.trim() || !ovForm.value.value) {
    ElMessage.warning('请填写 Key 和 Value')
    return
  }
  // 泳道覆盖即时生效，生产环境与其他 prod 写同强度二次确认。
  const ok = await confirmDangerous({
    action: '保存泳道覆盖', target: `${laneSel.value} / ${ovForm.value.key}`,
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  ovSubmitting.value = true
  try {
    await upsertLaneOverride(props.appId, envId.value, laneSel.value, {
      key: ovForm.value.key.trim(),
      value: ovForm.value.value,
    })
    ElMessage.success('覆盖已生效（即时生效，无需发布）')
    showOvEdit.value = false
    loadOverrides()
  } catch (e) {
    ElMessage.error((e as Error).message || '保存失败')
  } finally {
    ovSubmitting.value = false
  }
}

async function removeOverride(row: LaneOverride) {
  const ok = await confirmDangerous({
    action: '删除泳道覆盖',
    target: row.key,
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  ovRemoving.value = row.key
  try {
    await deleteLaneOverride(props.appId, envId.value, laneSel.value, row.key)
    loadOverrides()
  } catch (e) {
    ElMessage.error((e as Error).message || '删除失败')
  } finally {
    ovRemoving.value = ''
  }
}

function init() {
  load()
  loadLanes()
  laneSel.value = ''
  overrides.value = []
}

onMounted(init)
watch(() => props.appId, init)
watch(envId, init)
</script>

<template>
  <div class="dyn-block" v-loading="loading">
    <div v-if="!hasEnv" class="env-hint">请先在顶栏选择环境，动态配置按环境独立（test/prod 各一份）。</div>
    <template v-else>
    <div class="block-head">
      <div>
        <span class="block-title">动态配置（热更新）</span>
        <span class="block-hint">环境：{{ envName }}（按环境独立）· 添加配置后发布生效</span>
      </div>
      <div>
        <!-- 生效版本 tag 内联到标题行：当前生效即发布历史 active 那条，不单独设展示区 -->
        <span v-if="published?.published" class="ver-tag mono" style="margin-right: 10px">生效中 v{{ published.version }}</span>
        <span v-else class="none" style="margin-right: 10px">未发布</span>
        <el-button size="small" :loading="publishing" @click="publish">发布生效</el-button>
        <el-button size="small" type="primary" @click="openAdd">+ 新增配置</el-button>
      </div>
    </div>

    <!-- 配置项（编辑即 draft，点「发布生效」才对客户端可见） -->
    <section class="cfg-group">
      <div class="group-title">配置项<span class="group-cnt mono">{{ items.length }}</span></div>
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
            <el-button text type="danger" size="small" :loading="removing === row.id" @click="remove(row)">删除</el-button>
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
            <el-button v-if="row.status !== 'active'" text type="warning" size="small" :loading="rollingBack === row.id" @click="rollback(row)">回滚到此</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 泳道覆盖（key 级差异集，即时生效，随泳道回收消失） -->
    <section class="cfg-group">
      <div class="group-title">
        泳道覆盖
        <span class="lane-hint">即时生效（无需发布）· 随泳道回收消失 · 服务端 merge 到基线快照上</span>
      </div>
      <div class="lane-bar">
        <el-select v-model="laneSel" placeholder="选择泳道" size="small" style="width: 240px" clearable>
          <el-option v-for="l in lanes" :key="l.name" :value="l.name" :label="l.name + (l.mode === 'permanent' ? '（常驻）' : '')" />
        </el-select>
        <el-button size="small" type="primary" :disabled="!laneSel" @click="openOvAdd">+ 新增覆盖</el-button>
      </div>
      <el-table v-if="laneSel" v-loading="laneLoading" :data="overrides" size="small"
        empty-text="该泳道暂无覆盖；覆盖后带该泳道发现的客户端立即拿到差异值，基线不变。">
        <el-table-column prop="key" label="Key" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column prop="value" label="覆盖值" min-width="220" show-overflow-tooltip>
          <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="170">
          <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button text type="danger" size="small" :loading="ovRemoving === row.key" @click="removeOverride(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>
    </template>

    <!-- 编辑弹窗（基线 draft） -->
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

    <!-- 泳道覆盖弹窗 -->
    <el-dialog v-model="showOvEdit" title="新增泳道覆盖" width="500px">
      <div class="ov-hint">覆盖即时生效：带泳道 <code>{{ laneSel }}</code> 发现的客户端立即拿到该值；其余实例不受影响。</div>
      <el-form label-width="70px">
        <el-form-item label="Key">
          <el-input v-model="ovForm.key" placeholder="如 recommend_topk" />
        </el-form-item>
        <el-form-item label="覆盖值">
          <el-input v-model="ovForm.value" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showOvEdit = false">取消</el-button>
        <el-button type="primary" :disabled="ovSubmitting" @click="submitOverride">
          {{ ovSubmitting ? '保存中…' : '保存并生效' }}
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
.lane-hint { font-size: 11.5px; font-weight: 400; color: var(--text-faint); }
.lane-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.env-hint { font-size: 13px; color: var(--text-faint); padding: 20px 0; }
.ver-tag { padding: 2px 8px; background: var(--success-soft); color: var(--success); border-radius: 4px; font-size: 12px; }
.none { font-size: 12px; color: var(--text-faint); font-weight: 400; }
.ov-hint { font-size: 12.5px; color: var(--text-dim); margin-bottom: 12px; }
.mono { font-family: var(--font-mono, monospace); }
</style>
