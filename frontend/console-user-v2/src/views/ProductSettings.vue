<script setup lang="ts">
// 产品设置面（spec §9.4 facet=settings）：绑定资源 v2 原生化（中频：新服务接库/缓存来这），
// 其余低频设置（运行配置/成员/用量）并行期桥接老版。
// 绑定/解绑成功后 emit('updated') 回传最新应用实体（父组件刷新绑定计数）。
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError } from '@/api'
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface Binding {
  type: string
  name: string
  note?: string
}
interface App {
  id: string
  bindings?: Binding[]
}
const props = defineProps<{ app: App }>()
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
const namePlaceholder = computed(() =>
  DS_KINDS.includes(form.value.type)
    ? '数据服务名称或 ID'
    : '如 qwen-cs-route',
)

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

// 低频设置：桥接老版（to=产品 tab 深链；href=老版完整路径）。
const BRIDGE_LINKS: { label: string; desc: string; to?: string; href?: string }[] = [
  { label: '运行配置', desc: '启动注入的环境变量，改完重启生效', to: 'configs' },
  { label: '动态配置', desc: '运行时热更新，带版本与回滚', href: '/console/platform/config-center' },
  { label: '密钥引用', desc: '产品级密钥与证书（平台密钥库）', href: '/console/platform/security' },
  { label: '成员与权限', desc: '产品成员角色 / 受限开关', to: 'members' },
  { label: '用量', desc: '配额 / 用量归因 / 账单', to: 'usage' },
]
</script>

<template>
  <div class="settings">
    <!-- 绑定资源（v2 原生） -->
    <div class="card">
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
              <a
                :href="`/console/resources/${g.key}`"
                class="b-link"
                title="在经典版资源中心查看"
              >详情 ↗</a>
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
          <input v-model="form.name" class="name-input mono" :placeholder="namePlaceholder" @keyup.enter="submitBind" />
        </el-form-item>
      </el-form>
      <template #footer>
        <button class="act" @click="showAdd = false">取消</button>
        <button class="act primary" :disabled="submitting" @click="submitBind">
          {{ submitting ? '绑定中…' : '绑定' }}
        </button>
      </template>
    </el-dialog>

    <!-- 低频设置：桥接 -->
    <div class="card">
      <div class="card-head">
        <span class="card-title">更多设置</span>
        <span class="card-sub">低频管理面，在经典版中打开</span>
      </div>
      <div class="bridge-grid">
        <a
          v-for="l in BRIDGE_LINKS" :key="l.label"
          :href="l.href ?? `/console/applications/${app.id}?tab=${l.to}`"
          class="bridge-item"
        >
          <span class="b-label">{{ l.label }} ↗</span>
          <span class="b-desc">{{ l.desc }}</span>
        </a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.card {
  padding: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
}
.card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.card-title {
  font-size: 14px;
  font-weight: 600;
}
.card-sub {
  font-size: 12px;
  color: var(--text-faint);
}
.add-btn {
  margin-left: auto;
}
.empty-hint {
  padding: 22px 0;
  font-size: 12.5px;
  color: var(--text-faint);
  text-align: center;
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
.act {
  padding: 6px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12.5px;
  cursor: pointer;
}
.act.primary {
  border-color: var(--brand);
  background: var(--brand);
  color: #fff;
}
.act.primary:hover { opacity: 0.9; }
.act:disabled { opacity: 0.5; cursor: not-allowed; }
/* 桥接 */
.bridge-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
}
.bridge-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  text-decoration: none;
  transition: border-color 0.12s, background 0.12s;
}
.bridge-item:hover {
  border-color: var(--border-strong);
  background: var(--surface-2);
}
.bridge-item .b-label {
  font-size: 13px;
  color: var(--brand);
}
.bridge-item .b-desc {
  font-size: 11.5px;
  color: var(--text-faint);
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
