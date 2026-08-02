<script setup lang="ts">
// 资源中心 → 数据服务（DB/缓存/MQ/存储/向量/搜索）。
// 6 种 kind 共用此组件，由路由 props.kind 区分。KindMeta 从后端 /api/dataservices/meta 拉取（权威）。
// 租户私有；写操作生产环境需 prod:write（developer 生产只读），删除走 useDangerConfirm。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface SpecField {
  key: string; label: string; type: string; options?: string[]; default: string
}
interface KindMeta {
  kind: string; label: string; icon: string; fields: SpecField[]
}
interface DataService {
  id: string; kind: string; name: string; spec: Record<string, string>
  status: string; envId: string; createdAt: string; updatedAt: string
}

const props = defineProps<{ kind: string }>()

const router = useRouter()
const envStore = useEnvStore()
const metas = ref<KindMeta[]>([])
const items = ref<DataService[]>([])
const loading = ref(false)

const meta = computed(() => metas.value.find((m) => m.kind === props.kind))
const envLabel = (id: string) => envStore.envs.find((e) => e.id === id)?.name ?? id

const STATUS_LABEL: Record<string, string> = { running: '运行中', stopped: '已停止', creating: '创建中' }
const STATUS_TYPE: Record<string, string> = { running: 'success', stopped: 'info', creating: 'warning' }

// 创建弹窗
const showCreate = ref(false)
const form = ref<{ name: string; envId: string; spec: Record<string, string> }>({ name: '', envId: '', spec: {} })
const submitting = ref(false)

function specEntries(spec: Record<string, string>, fields?: SpecField[]): { label: string; value: string }[] {
  if (!fields) return []
  return fields.map((f) => ({ label: f.label, value: spec[f.key] ?? '-' }))
}

// 行所在环境是否生产（列表不按 scope 过滤，测试 scope 下也可能含生产资源行；
// 删除/停库等高危操作须按资源所属环境类型判断，而非当前 scope）。
const isRowProd = (row: DataService) => envStore.envs.find((e) => e.id === row.envId)?.type === 'prod'

function openCreate() {
  const def: Record<string, string> = {}
  meta.value?.fields.forEach((f) => { def[f.key] = f.default })
  form.value = { name: '', envId: envStore.currentEnv?.id ?? '', spec: def }
  showCreate.value = true
}

async function create() {
  if (!form.value.name.trim()) { ElMessage.warning('请填写名称'); return }
  if (!form.value.envId) { ElMessage.warning('请选择环境'); return }
  // text 必填字段（Default 为空 = 必填，如 storage.bucket），提前校验避免提交才返 400。
  const missing = meta.value?.fields.find(
    (f) => f.type === 'text' && !f.default && !form.value.spec[f.key]?.trim(),
  )
  if (missing) { ElMessage.warning(`请填写${missing.label}`); return }
  submitting.value = true
  try {
    const resp = await fetchAuth('/api/dataservices', {
      method: 'POST',
      body: JSON.stringify({ kind: props.kind, name: form.value.name, envId: form.value.envId, spec: form.value.spec }),
    })
    if (resp.ok) {
      ElMessage.success('已创建')
      showCreate.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '创建失败')
    }
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

async function toggle(row: DataService) {
  const next = row.status === 'running' ? 'stopped' : 'running'
  // 生产环境停止数据服务属高危（停库影响线上），走危险确认（输入名称）；启动不需。
  if (next === 'stopped' && isRowProd(row)) {
    const ok = await confirmDangerous({
      action: '停止数据服务', target: row.name,
      requireNameConfirm: true,
      isProd: true, // 仅 isRowProd(row) 为 true 时进入此分支
    })
    if (!ok) return
  }
  try {
    const resp = await fetchAuth(`/api/dataservices/${row.id}`, {
      method: 'PUT',
      body: JSON.stringify({ status: next }),
    })
    if (resp.ok) { ElMessage.success(next === 'running' ? '已启动' : '已停止'); load() }
    else { const err = await resp.json().catch(() => ({})); ElMessage.error(err.error || '操作失败') }
  } catch (e) {
    ElMessage.error('操作失败：' + (e as Error).message)
  }
}

async function remove(row: DataService) {
  const ok = await confirmDangerous({
    action: '删除数据服务', target: row.name,
    requireNameConfirm: isRowProd(row),
    isProd: isRowProd(row),
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
  await Promise.all([metas.value.length ? Promise.resolve() : loadMeta(), loadItems()])
}

// 行点击跳详情（操作列按钮 @click.stop 防误触）
function goDetail(row: DataService) {
  router.push(`/resources/${row.kind}/${row.id}`)
}

// kind 切换先清旧数据再加载，避免 loading 缝隙期短暂显示旧 kind 数据。
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
        <el-table-column label="名称" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
        </el-table-column>
        <el-table-column label="规格" min-width="260">
          <template #default="{ row }">
            <div class="spec-grid">
              <span v-for="e in specEntries(row.spec, meta?.fields)" :key="e.label" class="spec-item">
                <span class="spec-label">{{ e.label }}</span>
                <span class="mono">{{ e.value }}</span>
              </span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="(STATUS_TYPE[row.status] as any) || 'info'" size="small">
              {{ STATUS_LABEL[row.status] || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="环境" width="130">
          <template #default="{ row }">{{ envLabel(row.envId) }}</template>
        </el-table-column>
        <el-table-column label="创建时间" width="170">
          <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
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
    <el-dialog v-model="showCreate" :title="`创建${meta?.label ?? ''}`" width="500px">
      <el-form label-width="100px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="租户内唯一，如 orders-db" />
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.envId" placeholder="选择环境" style="width: 100%">
            <el-option v-for="e in envStore.envs" :key="e.id" :label="`${e.name}（${e.type === 'prod' ? '生产' : '测试'}）`" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-for="f in meta?.fields ?? []" :key="f.key" :label="f.label">
          <el-select v-if="f.type === 'select'" v-model="form.spec[f.key]" style="width: 100%">
            <el-option v-for="o in f.options" :key="o" :label="o" :value="o" />
          </el-select>
          <el-input v-else v-model="form.spec[f.key]" />
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
:deep(.clickable-row) { cursor: pointer; }
</style>
