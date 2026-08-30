<script setup lang="ts">
// 全局命令面板（Cmd/Ctrl+K）：应用/环境快速跳转 + 页面导航 + 常用动作。
// 对标 Vercel/Railway：键盘优先，打开即拉数据（不常驻轮询），Esc 关闭。
import { computed, onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { fetchAuth } from '@/api'

interface Item {
  key: string
  label: string
  hint?: string      // 右侧补充（如类型标签）
  section: string    // 分组标题：应用/环境/导航/动作
  icon: string
  run: () => void
}

const router = useRouter()
// 受控打开（顶栏点击）+ 全局 Cmd/Ctrl+K 快捷键；visible 双向绑定可选
const props = defineProps<{ modelValue?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
const localOpen = ref(false)
const q = ref('')
const sel = ref(0)
const inputRef = ref<HTMLInputElement | null>(null)
const visible = computed({
  get: () => props.modelValue ?? localOpen.value,
  set: (v: boolean) => { localOpen.value = v; emit('update:modelValue', v) },
})

const NAV: Item[] = [
  { key: 'nav:apps', label: '应用', section: '导航', icon: 'rocket', run: () => router.push('/applications') },
  { key: 'nav:devops', label: 'DevOps 中心', section: '导航', icon: 'job', run: () => router.push('/devops') },
  { key: 'nav:playground', label: 'Playground', section: '导航', icon: 'chat', run: () => router.push('/playground') },
  { key: 'nav:market', label: '模型市场', section: '导航', icon: 'market', run: () => router.push('/resources/models') },
  { key: 'nav:obs', label: '可观测', section: '导航', icon: 'chart', run: () => router.push('/platform/observability') },
  { key: 'nav:gov', label: '服务治理', section: '导航', icon: 'shield', run: () => router.push('/platform/governance') },
  { key: 'nav:config', label: '配置中心', section: '导航', icon: 'settings', run: () => router.push('/platform/config-center') },
  { key: 'nav:wl', label: '工作负载 · 服务', section: '导航', icon: 'server', run: () => router.push('/workloads/services') },
  { key: 'nav:envs', label: '环境管理', section: '导航', icon: 'shield', run: () => router.push('/environments') },
  { key: 'nav:keys', label: 'API 密钥', section: '导航', icon: 'key', run: () => router.push('/settings/api-keys') },
  { key: 'nav:bill', label: '配额与账单', section: '导航', icon: 'chart', run: () => router.push('/settings/billing') },
]

const ACTIONS: Item[] = [
  { key: 'act:newapp', label: '新建应用', section: '动作', icon: 'rocket', run: () => router.push('/applications?new=1') },
  { key: 'act:deploy', label: '部署工作负载', section: '动作', icon: 'server', run: () => router.push('/workloads/services?new=1') },
  { key: 'act:trace', label: '查 TraceID', hint: '可观测', section: '动作', icon: 'search', run: () => router.push('/platform/observability') },
]

interface App { id: string; name: string }
interface Env { id: string; name: string; type?: string }
const apps = ref<App[]>([])
const envs = ref<Env[]>([])

async function loadData() {
  // 面板打开才拉（非常驻）；失败静默——导航项仍可用
  try {
    const r = await fetchAuth('/api/applications')
    if (r.ok) apps.value = (await r.json()).data ?? []
  } catch { /* 静默 */ }
  try {
    const r = await fetchAuth('/api/environments')
    if (r.ok) envs.value = (await r.json()).data ?? []
  } catch { /* 静默 */ }
}

const all = computed<Item[]>(() => {
  const appItems: Item[] = apps.value.map((a) => ({
    key: 'app:' + a.id, label: a.name, hint: a.id, section: '应用', icon: 'rocket',
    run: () => router.push('/applications/' + a.id),
  }))
  const envItems: Item[] = envs.value.map((e) => ({
    key: 'env:' + e.id, label: e.name, hint: e.type === 'prod' ? '生产' : '测试', section: '环境', icon: 'shield',
    run: () => router.push('/environments/' + e.id),
  }))
  return [...appItems, ...envItems, ...NAV, ...ACTIONS]
})

// 过滤 + 分组（分组保序：应用→环境→导航→动作；每组内命中项连续展示）
const filtered = computed(() => {
  const kw = q.value.trim().toLowerCase()
  const hit = kw
    ? all.value.filter((i) => i.label.toLowerCase().includes(kw) || i.hint?.toLowerCase().includes(kw))
    : all.value
  const groups: { section: string; items: Item[] }[] = []
  for (const it of hit) {
    const g = groups.find((x) => x.section === it.section)
    if (g) g.items.push(it)
    else groups.push({ section: it.section, items: [it] })
  }
  return groups
})
const flat = computed(() => filtered.value.flatMap((g) => g.items))

function open() {
  visible.value = true
  q.value = ''
  sel.value = 0
  loadData()
  nextTick(() => inputRef.value?.focus())
}
function close() { visible.value = false }

function exec(it: Item) {
  close()
  it.run()
}

function onKeydown(e: KeyboardEvent) {
  if (!visible.value) return
  if (e.key === 'Escape') { close(); e.preventDefault() }
  else if (e.key === 'ArrowDown') { sel.value = Math.min(sel.value + 1, flat.value.length - 1); e.preventDefault() }
  else if (e.key === 'ArrowUp') { sel.value = Math.max(sel.value - 1, 0); e.preventDefault() }
  else if (e.key === 'Enter') {
    const it = flat.value[sel.value]
    if (it) exec(it)
    e.preventDefault()
  }
}
function onGlobalKeydown(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    if (visible.value) close()
    else open()
  }
}
// 外部 v-model 打开时（顶栏点击）也走 open 初始化（拉数据/清词/聚焦）
watch(() => props.modelValue, (v) => { if (v) open() })

watch(q, () => { sel.value = 0 })

onMounted(() => window.addEventListener('keydown', onGlobalKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onGlobalKeydown))
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="cp-mask" @click.self="close">
      <div class="cp-panel">
        <div class="cp-input-row">
          <span class="cp-search-icon">⌕</span>
          <input
ref="inputRef" v-model="q" class="cp-input" placeholder="搜索应用、环境或命令…"
            @keydown="onKeydown"
/>
          <kbd class="cp-esc">ESC</kbd>
        </div>
        <div class="cp-list">
          <template v-for="g in filtered" :key="g.section">
            <div class="cp-section">{{ g.section }}</div>
            <div
v-for="it in g.items" :key="it.key" class="cp-item"
              :class="{ sel: flat.indexOf(it) === sel }"
              @mouseenter="sel = flat.indexOf(it)" @click="exec(it)"
>
              <span class="cp-item-label">{{ it.label }}</span>
              <span v-if="it.hint" class="cp-hint">{{ it.hint }}</span>
            </div>
          </template>
          <div v-if="!flat.length" class="cp-none">无匹配结果</div>
        </div>
        <div class="cp-foot">
          <span><kbd>↑↓</kbd> 选择</span>
          <span><kbd>⏎</kbd> 打开</span>
          <span><kbd>esc</kbd> 关闭</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.cp-mask {
  position: fixed; inset: 0; z-index: 2000;
  background: rgba(0, 0, 0, 0.45);
  display: flex; justify-content: center; align-items: flex-start;
  padding-top: 12vh;
}
.cp-panel {
  width: min(560px, 92vw);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.35);
  overflow: hidden;
}
.cp-input-row {
  display: flex; align-items: center; gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--border);
}
.cp-search-icon { color: var(--text-faint); font-size: 16px; }
.cp-input {
  flex: 1; border: none; outline: none; background: transparent;
  color: var(--text); font-size: 14px; font-family: inherit;
}
.cp-esc {
  font-size: 10px; padding: 2px 6px; border-radius: 4px;
  border: 1px solid var(--border); color: var(--text-faint);
}
.cp-list { max-height: 46vh; overflow-y: auto; padding: 6px 0; }
.cp-section {
  padding: 8px 14px 4px;
  font-size: 11px; color: var(--text-faint);
  text-transform: uppercase; letter-spacing: 0.05em;
}
.cp-item {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 14px; cursor: pointer; font-size: 13px;
  color: var(--text);
}
.cp-item.sel { background: var(--brand-soft, rgba(99, 102, 241, 0.12)); }
.cp-hint { font-size: 11px; color: var(--text-faint); font-family: var(--font-mono, monospace); }
.cp-none { padding: 24px; text-align: center; color: var(--text-faint); font-size: 13px; }
.cp-foot {
  display: flex; gap: 14px; padding: 8px 14px;
  border-top: 1px solid var(--border);
  font-size: 11px; color: var(--text-faint);
}
.cp-foot kbd {
  border: 1px solid var(--border); border-radius: 3px;
  padding: 0 4px; font-size: 10px; margin-right: 2px;
}
</style>
