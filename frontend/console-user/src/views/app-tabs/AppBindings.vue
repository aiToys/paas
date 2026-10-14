<script setup lang="ts">
// 应用详情 - 资源绑定 tab：按类型分组的绑定卡片 + 绑定浮层 + 解绑。
// 绑定/解绑成功后 emit('updated', app) 回传最新应用实体（父组件更新 tab 徽标计数等）。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import Icon from '@/components/Icon.vue'
import { fetchJSON, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { typeMeta, DS_KINDS, TYPE_ORDER, type App, type Binding, type TypeKey } from './resource-meta'

const props = defineProps<{ app: App }>()
const emit = defineEmits<{ (e: 'updated', app: App): void }>()

const router = useRouter()

// 按 type 分组绑定项；仅展示有绑定项的分组。
const groups = computed(() => {
  const byType = new Map<string, Binding[]>()
  for (const b of props.app.bindings ?? []) {
    const arr = byType.get(b.type) ?? []
    arr.push(b)
    byType.set(b.type, arr)
  }
  return TYPE_ORDER.filter((t) => byType.has(t)).map((t) => ({ key: t, meta: typeMeta[t], items: byType.get(t)! }))
})

// —— 绑定资源浮层 ——
const showAdd = ref(false)
const form = ref<{ type: TypeKey; name: string }>({ type: 'models', name: '' })
const submitting = ref(false)

// 数据服务类型支持名称或 ID（后端 resolveDS 容错）；其他类型填名称。
const namePlaceholder = computed(() =>
  DS_KINDS.includes(form.value.type)
    ? '数据服务名称或 ID（如 acme-orders-db 或 ds-acme-db）'
    : '如 qwen-cs-route、mq-order-events',
)

const addOptions: { typeKey: TypeKey; label: string; icon: string; hint: string; color: string }[] = [
  { typeKey: 'models', label: '模型推理', icon: 'market', hint: '部署 LLM / Embedding 模型', color: '#6366f1' },
  { typeKey: 'db', label: '数据库', icon: 'database', hint: 'PostgreSQL / MySQL 实例', color: '#f59e0b' },
  { typeKey: 'cache', label: '缓存', icon: 'zap', hint: 'Redis 实例与集群', color: '#ef4444' },
  { typeKey: 'mq', label: '消息队列', icon: 'message', hint: '创建 Topic / 申请 MQ 实例', color: '#10b981' },
  { typeKey: 'storage', label: '对象存储', icon: 'storage', hint: 'Bucket / CDN / 生命周期', color: '#0ea5e9' },
  { typeKey: 'vector', label: '向量数据库', icon: 'layers', hint: '索引 / 检索 / Embedding', color: '#8b5cf6' },
  { typeKey: 'search', label: '搜索引擎', icon: 'search', hint: 'Elasticsearch / OpenSearch', color: '#06b6d4' },
]

function openAdd() {
  form.value = { type: 'models', name: '' }
  showAdd.value = true
}

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
    // 数据服务绑定时后端 best-effort 注入连接信息（OnBind 失败仅 log 不阻断绑定）；
    // 故文案用「将注入」不断言「已注入」，引导用户去「配置」tab 验证。
    if (DS_KINDS.includes(form.value.type)) {
      ElMessage.success(`已绑定 ${typeMeta[form.value.type].label}：${name}（连接信息将注入应用配置，可在「配置」tab 查看）`)
    } else {
      ElMessage.success(`已绑定 ${typeMeta[form.value.type].label}：${name}`)
    }
  } catch (e) {
    ElMessage.error(apiError(e, '绑定失败'))
  } finally {
    submitting.value = false
  }
}

async function unbind(b: Binding) {
  // 解绑属高危操作（数据服务解绑会清除 DATABASE_URL/REDIS_URL 等连接注入，影响线上 Pod）：
  // 统一走 confirmDangerous，生产环境要求输入绑定名称确认（与其他资源删除一致）。
  const isDsUnbind = DS_KINDS.includes(b.type as TypeKey)
  const ok = await confirmDangerous({
    action: '解绑',
    target: b.name,
    requireNameConfirm: true,
  })
  if (!ok) return
  try {
    const updated = await fetchJSON<App>(
      `/api/applications/${props.app.id}/bindings/${b.type}/${encodeURIComponent(b.name)}`,
      { method: 'DELETE' },
    )
    emit('updated', updated)
    ElMessage.success(`已解绑：${b.name}` + (isDsUnbind ? '（连接信息将同步清除）' : ''))
  } catch (e) {
    ElMessage.error(apiError(e, '解绑失败'))
  }
}

// 绑定卡片点击：数据服务类型解析 name→id 跳详情（带 app 上下文），其他类型（models/gov）跳列表。
async function onBindingClick(g: { key: string }, it: { name: string }) {
  const t = g.key
  if (DS_KINDS.includes(t as TypeKey)) {
    // 拉该 kind DS 列表，按 name 或 id 匹配解析出 dsId（绑定时 name/id 都可能）。
    try {
      const list = await fetchJSON<{ id: string; name: string }[]>(`/api/dataservices?kind=${t}`)
      const ds = list.find((d) => d.name === it.name || d.id === it.name)
      if (ds) {
        router.push(`/resources/${t}/${ds.id}?app=${props.app.id}`)
        return
      }
    } catch {
      /* 解析失败降级到列表 */
    }
  }
  router.push(bindingListRoute(t))
}
// 兜底：列表路由（解析失败或非 DS 类型）。
function bindingListRoute(t: string): string {
  if (t === 'models') return '/resources/models'
  if (t === 'gov') return '/platform/governance'
  if (t === 'dal') return '/resources/db'
  return `/resources/${t}`
}
</script>

<template>
  <div class="bind-view">
    <div class="bind-head">
      <div class="bind-title">绑定的资源</div>
      <button class="add-btn" @click="openAdd">+ 绑定资源</button>
    </div>

    <div v-if="!groups.length" class="empty">
      <Icon name="rocket" :size="28" />
      <p>该应用尚未绑定任何资源</p>
      <button class="add-btn" @click="openAdd">绑定第一个资源</button>
    </div>

    <section v-for="g in groups" :key="g.key" class="res-group">
      <div class="group-head">
        <Icon :name="g.meta.icon" :size="16" :style="{ color: g.meta.color }" />
        <span class="group-label">{{ g.meta.label }}</span>
        <span class="group-count mono">{{ g.items.length }}</span>
      </div>
      <div class="group-items">
        <div v-for="it in g.items" :key="it.name" class="res-card clickable" @click="onBindingClick(g, it)">
          <div class="res-card-head">
            <span class="res-name mono">{{ it.name }}</span>
            <span class="res-status">已绑定</span>
          </div>
          <div class="res-type">{{ g.meta.label }}</div>
          <div v-if="it.note" class="res-detail">{{ it.note }}</div>
          <button class="unbind" @click.stop="unbind(it)">解绑</button>
        </div>
      </div>
      <!-- 模型推理绑定用法说明（绑定后自动生成应用级 Key + 注入配置） -->
      <div v-if="g.key === 'models'" class="usage-tip">
        <span class="tip-icon">💡</span>
        <div class="tip-body">
          <strong>用法：</strong>绑定模型后，平台已为本应用生成专属 API Key 并注入「配置」tab
          （<code class="mono">PAAS_LLM_API_KEY</code> / <code class="mono">PAAS_LLM_BASE_URL</code>）。
          应用工作负载重启后自动获得该凭证，代码用 OpenAI SDK 指向
          <code class="mono">PAAS_LLM_BASE_URL</code> 即可调用，<strong>token 用量自动归因到本应用</strong>（见「配额与账单」）。
        </div>
      </div>
    </section>

    <!-- 绑定资源浮层 -->
    <Teleport to="body">
      <div v-if="showAdd" class="overlay" @click.self="showAdd = false">
        <div class="sheet">
          <div class="sheet-head">
            <h3>为「{{ app.name }}」绑定资源</h3>
            <button class="close" @click="showAdd = false">×</button>
          </div>
          <p class="sheet-sub">资源将归属该应用，随应用生命周期管理。选择类型并命名：</p>

          <div class="field-label">资源类型</div>
          <div class="opt-grid">
            <button
              v-for="o in addOptions"
              :key="o.typeKey"
              class="opt"
              :class="{ on: form.type === o.typeKey }"
              @click="form.type = o.typeKey"
            >
              <div class="opt-icon" :style="{ background: o.color }"><Icon :name="o.icon" :size="18" /></div>
              <div class="opt-text">
                <div class="opt-label">{{ o.label }}</div>
                <div class="opt-hint">{{ o.hint }}</div>
              </div>
            </button>
          </div>

          <div class="field-label">资源名称</div>
          <input
            v-model="form.name"
            class="name-input"
            :placeholder="namePlaceholder"
            @keyup.enter="submitBind"
          />

          <div class="sheet-foot">
            <button class="ghost" @click="showAdd = false">取消</button>
            <button class="primary" :disabled="submitting" @click="submitBind">
              {{ submitting ? '绑定中…' : '确认绑定' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.bind-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.bind-title {
  font-size: 14px;
  font-weight: 600;
}
.add-btn {
  padding: 7px 14px;
  border: none;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 56px 0;
  color: var(--text-faint);
  text-align: center;
}
.empty :deep(svg) {
  color: var(--brand);
}
.empty p {
  margin: 0;
  font-size: 13px;
}
.res-group {
  margin-bottom: 20px;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.group-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-dim);
}
.group-count {
  font-size: 11px;
  color: var(--text-faint);
}
.group-items {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}
.res-card {
  position: relative;
  padding: 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  transition: border-color 0.12s;
}
.res-card.clickable {
  cursor: pointer;
}
.res-card.clickable:hover {
  border-color: var(--brand);
  background: var(--surface-2);
}
.res-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.res-name {
  font-size: 13px;
  font-weight: 600;
}
.res-status {
  font-size: 11px;
  color: var(--success);
}
.res-type {
  font-size: 12px;
  color: var(--text-dim);
  margin-top: 4px;
}
.res-detail {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}
.unbind {
  position: absolute;
  top: 12px;
  right: 12px;
  display: none;
  padding: 3px 8px;
  border: 1px solid var(--danger);
  border-radius: 5px;
  background: var(--danger-soft);
  color: var(--danger);
  font-family: inherit;
  font-size: 11px;
  cursor: pointer;
}
.res-card:hover .unbind {
  display: block;
}
.res-card:hover .res-status {
  display: none;
}
.usage-tip {
  display: flex;
  gap: 10px;
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-dim);
}
.usage-tip .tip-icon { flex-shrink: 0; }
.usage-tip code {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--surface);
  color: var(--brand);
  font-size: 11px;
}
.mono { font-family: 'JetBrains Mono', monospace; }

/* 绑定资源浮层 */
.overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  backdrop-filter: blur(4px);
  display: grid;
  place-items: center;
  z-index: 100;
}
.sheet {
  width: 520px;
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-lg);
  padding: 24px;
  box-shadow: var(--shadow);
}
.sheet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.sheet-head h3 {
  margin: 0;
  font-size: 16px;
}
.close {
  border: none;
  background: transparent;
  color: var(--text-faint);
  font-size: 22px;
  cursor: pointer;
  line-height: 1;
}
.sheet-sub {
  font-size: 13px;
  color: var(--text-dim);
  margin: 6px 0 18px;
}
.field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-dim);
  margin: 8px 0 8px;
}
.opt-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.opt {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  font-family: inherit;
  text-align: left;
  transition: all 0.12s;
}
.opt:hover {
  border-color: var(--brand);
}
.opt.on {
  border-color: var(--brand);
  background: var(--brand-soft);
  box-shadow: 0 0 0 1px var(--brand) inset;
}
.opt-icon {
  width: 38px;
  height: 38px;
  border-radius: 9px;
  display: grid;
  place-items: center;
  color: #fff;
  flex-shrink: 0;
}
.opt-label {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--text);
}
.opt-hint {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 2px;
}
.name-input {
  width: 100%;
  box-sizing: border-box;
  padding: 10px 12px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  font-family: 'JetBrains Mono', monospace;
  font-size: 13px;
  outline: none;
  transition: border-color 0.12s;
}
.name-input:focus {
  border-color: var(--brand);
}
.name-input::placeholder {
  color: var(--text-faint);
}
.sheet-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
.ghost,
.primary {
  padding: 8px 16px;
  border-radius: var(--radius);
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.12s;
}
.ghost {
  border: 1px solid var(--border-strong);
  background: transparent;
  color: var(--text);
}
.ghost:hover {
  background: var(--surface-2);
}
.primary {
  border: none;
  background: var(--brand);
  color: #fff;
  box-shadow: 0 4px 14px var(--brand-glow);
}
.primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
  box-shadow: none;
}
</style>
