<script setup lang="ts">
// 产品设置面（spec §9.4 facet=settings）：绑定资源 / 动态配置 / 运行配置 / 成员与权限
// 全 v2 原生（2026-10-16 全量化，零跳老版）。
// 绑定/解绑成功后 emit('updated') 回传最新应用实体（父组件刷新绑定计数）。
import { computed, ref, watch } from 'vue'
import { listDataServices, listModels, type DataServiceEntity, type ModelInfo } from '@/api/platform'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import ProductDynamicConfig from '@/components/ProductDynamicConfig.vue'
import ProductRunConfig from '@/components/ProductRunConfig.vue'
import ProductMembers from '@/components/ProductMembers.vue'

interface Binding {
  type: string
  name: string
  note?: string
}
interface App {
  id: string
  bindings?: Binding[]
  restricted?: boolean
}
const props = defineProps<{ app: App; section?: string }>()
const emit = defineEmits<{ (e: 'updated', app: App): void }>()

// 类型元信息（v2 极简版：标签 + 是否数据服务——绑定后自动注入连接信息）。
const TYPE_LABEL: Record<string, string> = {
  models: '模型推理', db: '数据库', cache: '缓存', mq: '消息队列',
  storage: '对象存储', vector: '向量库', search: '搜索', dal: '数据访问层', gov: '服务治理',
}
const DS_KINDS = ['db', 'cache', 'mq', 'storage', 'vector', 'search']
const TYPE_ORDER = ['models', 'db', 'cache', 'mq', 'storage', 'vector', 'search', 'dal', 'gov']
const BINDABLE = ['models', 'db', 'cache', 'mq', 'storage', 'vector', 'search']

// 按 type 分组；仅展示有绑定项的分组。
const groups = computed(() => {
  const byType = new Map<string, Binding[]>()
  for (const b of props.app.bindings ?? []) {
    const arr = byType.get(b.type) ?? []
    arr.push(b)
    byType.set(b.type, arr)
  }
  return TYPE_ORDER.filter((t) => byType.has(t)).map((t) => ({ key: t, items: byType.get(t)! }))
})

// —— 绑定（POST /api/applications/{id}/bindings）——
const showAdd = ref(false)
const form = ref<{ type: string; name: string }>({ type: 'db', name: '' })
const submitting = ref(false)
const candidates = ref<DataServiceEntity[]>([])
const models = ref<ModelInfo[]>([])
const optionsLoading = ref(false)
const optionsError = ref('')
const options = computed(() => form.value.type === 'models'
  ? models.value.map((m) => ({ value: m.id, label: m.name || m.id }))
  : candidates.value.filter((d) => d.kind === form.value.type).map((d) => ({ value: d.id, label: d.name })))
async function loadOptions() {
  optionsLoading.value = true
  optionsError.value = ''
  try { [candidates.value, models.value] = await Promise.all([listDataServices(), listModels()]) }
  catch (e) { optionsError.value = apiError(e, '可绑定资源加载失败') }
  finally { optionsLoading.value = false }
}
watch(showAdd, (open) => { if (open) { form.value.name = ''; void loadOptions() } })
watch(() => form.value.type, () => { form.value.name = '' })

async function submitBind() {
  const name = form.value.name.trim()
  if (!name) {
    ElMessage.warning('请填写资源名称')
    return
  }
  submitting.value = true
  try {
    const updated = await fetchJSON<App>(`/api/applications/${props.app.id}/bindings`, {
      method: 'POST',
      body: JSON.stringify({ type: form.value.type, name }),
    })
    showAdd.value = false
    emit('updated', updated)
    ElMessage.success(
      DS_KINDS.includes(form.value.type)
        ? `已绑定（连接信息将注入应用配置）：${name}`
        : `已绑定：${name}`,
    )
  } catch (e) {
    ElMessage.error(apiError(e, '绑定失败'))
  } finally {
    submitting.value = false
  }
}

// —— 解绑（DELETE；高危：DS 解绑清除连接注入影响线上 Pod，统一 confirmDangerous）——
async function unbind(b: Binding) {
  const ok = await confirmDangerous({ action: '解绑', target: b.name, requireNameConfirm: true })
  if (!ok) return
  try {
    const updated = await fetchJSON<App>(
      `/api/applications/${props.app.id}/bindings/${b.type}/${encodeURIComponent(b.name)}`,
      { method: 'DELETE' },
    )
    emit('updated', updated)
    ElMessage.success(`已解绑：${b.name}` + (DS_KINDS.includes(b.type) ? '（连接信息将同步清除）' : ''))
  } catch (e) {
    ElMessage.error(apiError(e, '解绑失败'))
  }
}

// 绑定项「详情」内链（v2 原生页）：模型 → /models；数据服务六类 → /dataservices。
function v2Link(type: string): string {
  if (type === 'models') return '/models'
  if (DS_KINDS.includes(type)) return '/dataservices'
  return ''
}
</script>

<template>
  <div class="settings">
    <!-- 绑定资源（v2 原生） -->
    <div v-if="!section || section === 'resources'" class="card">
      <div class="card-head">
        <span class="card-title">绑定资源</span>
        <span class="card-sub">数据服务绑定后自动注入连接信息（DATABASE_URL / REDIS_URL …）</span>
        <button class="act primary add-btn" @click="showAdd = true">绑定资源</button>
      </div>
      <div v-if="!groups.length" class="empty-hint">还没有绑定——服务要用的库/缓存/队列在这里接进来</div>
      <div v-else class="bind-groups">
        <div v-for="g in groups" :key="g.key" class="bind-group">
          <div class="g-label">{{ TYPE_LABEL[g.key] ?? g.key }}</div>
          <div class="g-items">
            <div v-for="b in g.items" :key="b.type + b.name" class="bind-item">
              <span class="b-name mono">{{ b.name }}</span>
              <span v-if="b.note" class="b-note">{{ b.note }}</span>
              <RouterLink v-if="v2Link(g.key)" :to="v2Link(g.key)" class="b-link">详情 →</RouterLink>
              <button class="b-unbind" @click="unbind(b)">解绑</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 绑定浮层 -->
    <el-dialog v-model="showAdd" title="绑定资源" width="440px" append-to-body>
      <el-form label-position="top">
        <el-form-item label="类型">
          <select v-model="form.type" class="type-select">
            <option v-for="t in BINDABLE" :key="t" :value="t">{{ TYPE_LABEL[t] }}</option>
          </select>
        </el-form-item>
        <el-form-item label="资源名称">
          <el-select v-model="form.name" filterable :loading="optionsLoading" placeholder="选择已存在的资源" style="width:100%"><el-option v-for="item in options" :key="item.value" :value="item.value" :label="item.label" /></el-select>
          <p v-if="optionsError" role="alert">{{ optionsError }} <button class="text-button" @click="loadOptions">重试</button></p>
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showAdd = false">取消</button>
        <button class="act primary" :disabled="submitting || !form.name || !!optionsError || optionsLoading" @click="submitBind">
          {{ submitting ? '绑定中…' : '绑定' }}
        </button>
      </template>
    </el-dialog>

    <!-- 动态配置（v2 原生：key/value + 版本化发布 + 回滚，跟随顶栏环境） -->
    <ProductDynamicConfig v-if="!section || section === 'configuration'" :app-id="app.id" />

    <!-- 运行配置（v2 原生：env/Secret 启动注入，重启生效） -->
    <ProductRunConfig v-if="!section || section === 'configuration'" :app-id="app.id" />

    <!-- 成员与权限（v2 原生：应用级角色 + 受限开关） -->
    <ProductMembers v-if="!section || section === 'members'" :app-id="app.id" :restricted="!!app.restricted" />
  </div>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.add-btn {
  margin-left: auto;
}
.bind-groups {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.bind-group {
  display: flex;
  gap: 12px;
}
.g-label {
  width: 72px;
  flex-shrink: 0;
  padding-top: 7px;
  font-size: 12px;
  color: var(--text-faint);
}
.g-items {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.bind-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 12.5px;
}
.b-name {
  color: var(--text);
}
.b-note {
  font-size: 11.5px;
  color: var(--text-faint);
}
.b-link {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--brand);
  text-decoration: none;
}
.b-unbind {
  border: none;
  background: transparent;
  color: var(--danger, #f43f5e);
  font-size: 11.5px;
  cursor: pointer;
  opacity: 0.8;
}
.b-unbind:hover {
  opacity: 1;
  text-decoration: underline;
}
/* 浮层 */
.type-select {
  width: 100%;
  padding: 7px 10px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-size: 13px;
}
.name-input {
  width: 100%;
  box-sizing: border-box;
  padding: 7px 10px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  background: var(--surface-2);
  color: var(--text);
  font-size: 13px;
}
.name-input:focus, .type-select:focus {
  outline: none;
  border-color: var(--brand);
}
</style>
