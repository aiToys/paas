<script setup lang="ts">
// 资源中心 → 数据服务（DB/缓存/MQ/存储/向量/搜索）。
// 6 种 kind 共用此组件，由路由 props.kind 区分。KindMeta 从 /api/dataservices/meta 拉取（表单字段元数据），
// 引擎目录从 /api/engines 拉取（enabled，按 kind 过滤）——用户选 engine 决定 mode（平台托管/共享集群/独占外部）。
// 租户私有；写操作生产环境需 prod:write（developer 生产只读），删除/停库走 useDangerConfirm。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

type TagType = '' | 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface SpecField { key: string; label: string; type: string; options?: string[]; default: string }
interface KindMeta { kind: string; label: string; icon: string; fields: SpecField[] }
interface Engine {
  id: string; kind: string; engine: string; label: string; description: string
  mode: string; enabled: boolean; connection?: Record<string, string>; order: number
}
interface DataService {
  id: string; kind: string; name: string; spec: Record<string, string>; source?: string
  status: string; envId: string; engineId?: string; createdAt: string; updatedAt: string
}

const props = defineProps<{ kind: string }>()

const router = useRouter()
const envStore = useEnvStore()
const metas = ref<KindMeta[]>([])
const engines = ref<Engine[]>([])
const items = ref<DataService[]>([])
const loading = ref(false)

const meta = computed(() => metas.value.find((m) => m.kind === props.kind))
// 当前 kind 的 enabled 引擎（用户创建时选）
const kindEngines = computed(() => engines.value.filter((e) => e.kind === props.kind))
const envLabel = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

const STATUS_LABEL: Record<string, string> = { running: '运行中', stopped: '已停止', creating: '创建中' }
const STATUS_TYPE: Record<string, TagType> = { running: 'success', stopped: 'info', creating: 'warning' }
const MODE_LABEL: Record<string, string> = {
  'managed': '平台托管',
  'external-shared': '共享集群',
  'external-dedicated': '独占外部',
}

// 创建弹窗
const showCreate = ref(false)
const form = ref<{
  engineId: string; name: string; envId: string
  spec: Record<string, string>; connectionUri: string
}>({ engineId: '', name: '', envId: '', spec: {}, connectionUri: '' })
const submitting = ref(false)

const selectedEngine = computed(() => kindEngines.value.find((e) => e.id === form.value.engineId))
// spec 字段（排除 engine——由 engineId 决定，不重复让用户选）
const specFields = computed(() => (meta.value?.fields ?? []).filter((f) => f.key !== 'engine'))

function specEntries(spec: Record<string, string>, fields?: SpecField[]): { label: string; value: string }[] {
  if (!fields) return []
  return fields.map((f) => ({ label: f.label, value: spec[f.key] ?? '-' }))
}

const isRowProd = (row: DataService) => envStore.envs.find((e) => e.id === row.envId)?.type === 'prod'

function openCreate() {
  const def: Record<string, string> = {}
  meta.value?.fields.forEach((f) => { if (f.key !== 'engine') def[f.key] = f.default })
  form.value = {
    engineId: kindEngines.value[0]?.id ?? '',
    name: '', envId: envStore.currentEnv?.id ?? '',
    spec: def, connectionUri: '',
  }
  showCreate.value = true
}

async function create() {
  if (!form.value.engineId) { ElMessage.warning('请选择引擎'); return }
  if (!form.value.name.trim()) { ElMessage.warning('请填写名称'); return }
  if (!form.value.envId) { ElMessage.warning('请选择环境'); return }
  // external-dedicated 需填连接 uri（独占外部实例，用户提供连接串）
  if (selectedEngine.value?.mode === 'external-dedicated' && !form.value.connectionUri.trim()) {
    ElMessage.warning('独占外部模式需填写连接 URI'); return
  }
  submitting.value = true
  try {
    const body: Record<string, unknown> = {
      engineId: form.value.engineId,
      name: form.value.name, envId: form.value.envId, spec: form.value.spec,
    }
    if (selectedEngine.value?.mode === 'external-dedicated') {
      body.connection = { uri: form.value.connectionUri }
    }
    const resp = await fetchAuth('/api/dataservices', { method: 'POST', body: JSON.stringify(body) })
    if (resp.ok) { ElMessage.success('已创建'); showCreate.value = false; load() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '创建失败') }
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

async function toggle(row: DataService) {
  const stop = row.status === 'running'
  if (stop && isRowProd(row)) {
    const ok = await confirmDangerous({
      action: '停止数据服务', target: row.name, requireNameConfirm: true, isProd: true,
    })
    if (!ok) return
  }
  try {
    const resp = await fetchAuth(`/api/dataservices/${row.id}/${stop ? 'stop' : 'start'}`, { method: 'POST' })
    if (resp.ok) { ElMessage.success(stop ? '已停止' : '已启动'); load() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '操作失败') }
  } catch (e) {
    ElMessage.error('操作失败：' + (e as Error).message)
  }
}

async function remove(row: DataService) {
  const ok = await confirmDangerous({
    action: '删除数据服务', target: row.name,
    requireNameConfirm: isRowProd(row), isProd: isRowProd(row),
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/dataservices/${row.id}`, { method: 'DELETE' })
    if (resp.ok) { ElMessage.success('已删除'); load() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '删除失败') }
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

async function loadMeta() {
  const resp = await fetchAuth('/api/dataservices/meta')
  if (resp.ok) metas.value = (await resp.json()).data ?? []
}
async function loadEngines() {
  const resp = await fetchAuth('/api/engines')
  if (resp.ok) engines.value = (await resp.json()).data ?? []
}
async function loadItems() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/dataservices?kind=${props.kind}`)
    if (resp.ok) items.value = (await resp.json()).data ?? []
  } finally {
    loading.value = false
  }
}
async function load() {
  await Promise.all([
    metas.value.length ? Promise.resolve() : loadMeta(),
    engines.value.length ? Promise.resolve() : loadEngines(),
    loadItems(),
  ])
}

function goDetail(row: DataService) { router.push(`/resources/${row.kind}/${row.id}`) }

watch(() => props.kind, () => { items.value = []; loadItems() })
onMounted(load)
</script>

<template>
  <div class="ds-page">
    <div class="page-head">
      <div>
        <h2>{{ meta?.label ?? '数据服务' }}</h2>
        <p class="sub">资源中心 · {{ meta?.label }} 实例管理（租户私有，生产写需管理员）</p>
      </div>
      <el-button type="primary" @click="openCreate">+ 创建{{ meta?.label ?? '' }}</el-button>
    </div>

    <section class="block" v-loading="loading">
      <el-table :data="items" size="small" empty-text="暂无实例，可点击右上角创建" row-class-name="clickable-row" @row-click="goDetail">
        <el-table-column label="名称" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="引擎/来源" width="180">
          <template #default="{ row }">
            <span class="mono">{{ row.spec?.engine ?? '-' }}</span>
            <el-tag size="small" type="info" style="margin-left:6px">{{ MODE_LABEL[row.source ?? 'managed'] ?? row.source }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="规格" min-width="220">
          <template #default="{ row }">
            <div class="spec-grid">
              <span v-for="e in specEntries(row.spec, specFields)" :key="e.label" class="spec-item">
                <span class="spec-label">{{ e.label }}</span>
                <span class="mono">{{ e.value }}</span>
              </span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="(STATUS_TYPE[row.status]) || 'info'" size="small">
              {{ STATUS_LABEL[row.status] || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="环境" width="120">
          <template #default="{ row }">{{ envLabel(row.envId) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="140">
          <template #default="{ row }">
            <el-button text :type="row.status === 'running' ? 'warning' : 'success'" size="small" @click.stop="toggle(row)">
              {{ row.status === 'running' ? '停止' : '启动' }}
            </el-button>
            <el-button text type="danger" size="small" @click.stop="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 创建弹窗 -->
    <el-dialog v-model="showCreate" :title="`创建${meta?.label ?? ''}`" width="520px">
      <el-form label-width="100px">
        <el-form-item label="引擎">
          <el-select v-model="form.engineId" placeholder="选择引擎" style="width: 100%">
            <el-option
              v-for="e in kindEngines" :key="e.id"
              :label="`${e.label}（${MODE_LABEL[e.mode] ?? e.mode}）`" :value="e.id"
            />
          </el-select>
        </el-form-item>
        <div v-if="selectedEngine" class="engine-hint">
          <el-tag size="small" :type="selectedEngine.mode === 'managed' ? 'success' : 'warning'">
            {{ MODE_LABEL[selectedEngine.mode] }}
          </el-tag>
          <span v-if="selectedEngine.mode === 'managed'">平台自动拉起独占实例，凭证自动生成。</span>
          <span v-else-if="selectedEngine.mode === 'external-shared'">复用管理员配置的共享集群连接（多租户共享）。</span>
          <span v-else>接入你已有的外部实例，下方填写连接 URI。</span>
        </div>
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="租户内唯一，如 orders-db" />
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.envId" placeholder="选择环境" style="width: 100%">
            <el-option v-for="e in envStore.envs" :key="e.id" :label="`${e.name}（${e.type === 'prod' ? '生产' : '测试'}）`" :value="e.id" />
          </el-select>
        </el-form-item>
        <!-- spec 字段（managed 模式有意义：version/size_gb/dimension 等；external 模式可选填逻辑单元名） -->
        <el-form-item v-for="f in specFields" :key="f.key" :label="f.label">
          <el-select v-if="f.type === 'select'" v-model="form.spec[f.key]" style="width: 100%">
            <el-option v-for="o in f.options" :key="o" :label="o" :value="o" />
          </el-select>
          <el-input v-else v-model="form.spec[f.key]" />
        </el-form-item>
        <!-- external-dedicated：用户填连接 URI -->
        <el-form-item v-if="selectedEngine?.mode === 'external-dedicated'" label="连接 URI">
          <el-input v-model="form.connectionUri" type="textarea" :rows="2"
            placeholder="如 postgresql://user:pass@host:5432/db" />
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
.ds-page { max-width: 1100px; margin: 0 auto; }
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
.page-head h2 { margin: 0 0 4px; font-size: 18px; }
.sub { margin: 0; font-size: 12.5px; color: var(--text-dim); }
.block { margin-bottom: 24px; }
.spec-grid { display: flex; flex-wrap: wrap; gap: 6px 18px; }
.spec-item { font-size: 12px; }
.spec-label { color: var(--text-faint); margin-right: 6px; }
.engine-hint { margin: -4px 0 14px 100px; font-size: 12px; color: var(--text-dim); display: flex; align-items: center; gap: 8px; }
:deep(.clickable-row) { cursor: pointer; }
</style>
