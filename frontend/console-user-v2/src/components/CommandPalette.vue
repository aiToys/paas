<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { fetchList } from '@/api'
import { navigation, aiSections, searchEntries } from '@/navigation'
import Icon from './Icon.vue'
const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
const router = useRouter()
const q = ref('')
const selected = ref(0)
const loading = ref(false)
const error = ref(false)
const products = ref<{ id: string; name: string }[]>([])
const visible = computed({ get: () => props.modelValue, set: (v) => emit('update:modelValue', v) })
const input = ref<HTMLInputElement>()
const results = computed(() => {
  const entries = [
    ...products.value.map((p) => ({ label: p.name, to: '/products/' + p.id, icon: 'deploy', group: '产品', keywords: p.id })),
    ...navigation.flatMap((g) => g.items.map((i) => ({ ...i, group: g.label }))),
    ...aiSections.map((s) => ({ label: s.label, to: '/ai/' + s.id, icon: s.icon, group: 'AI 开发', keywords: s.id })),
  ]
  return searchEntries(entries, q.value)
})
async function load() {
  loading.value = true
  error.value = false
  try { products.value = await fetchList<{ id: string; name: string }>('/api/applications') }
  catch { error.value = true }
  finally { loading.value = false }
}
watch(() => props.modelValue, (v) => { if (v) { q.value = ''; selected.value = 0; void load() } })
watch(q, () => { selected.value = 0 })
watch(selected, async (i) => { await nextTick(); document.getElementById('command-' + i)?.scrollIntoView({ block: 'nearest' }) })
function go(to?: string) {
  if (!to) return
  visible.value = false
  void router.push(to)
}
function move(delta: number) {
  selected.value = Math.max(0, Math.min(results.value.length - 1, selected.value + delta))
}
</script>
<template>
  <el-dialog v-model="visible" title="快速访问" width="600px" class="command-dialog" @opened="input?.focus()">
    <div class="command-input">
      <Icon name="search" :size="20" />
      <input
ref="input" v-model="q" aria-label="搜索产品或功能" placeholder="搜索产品、模型、工作流或功能…"
        role="combobox" aria-autocomplete="list" :aria-expanded="visible" aria-controls="command-results"
        :aria-activedescendant="results.length ? 'command-' + selected : undefined"
        @keydown.down.prevent="move(1)" @keydown.up.prevent="move(-1)" @keydown.enter.prevent="go(results[selected]?.to)"
/>
    </div>
    <p v-if="loading" class="command-note" role="status">正在加载产品…</p>
    <p v-if="error" class="command-note" role="status">产品暂时无法加载，仍可访问各功能。<button class="text-button" @click="load">重试</button></p>
    <div id="command-results" class="command-results" role="listbox" aria-label="搜索结果">
      <div
v-for="(item, i) in results" :id="'command-' + i" :key="item.to" role="option" :aria-selected="selected === i"
        class="command-result" :class="{ selected: selected === i }" @mousemove="selected = i" @click="go(item.to)"
>
        <Icon :name="item.icon" :size="17" /><span>{{ item.label }}</span><small>{{ item.group }}</small><span aria-hidden="true">↵</span>
      </div>
      <div v-if="!results.length" class="empty-hint">没有匹配结果，换一个关键词试试</div>
    </div>
    <template #footer><span class="command-note">↑ ↓ 选择 · Enter 打开 · Esc 关闭</span></template>
  </el-dialog>
</template>
<style scoped>
.command-input { display: flex; align-items: center; gap: 12px; padding: 8px 0 16px; border-bottom: 1px solid var(--border); }
.command-input input { flex: 1; min-width: 0; border: 0; background: transparent; color: var(--text); font: inherit; padding: 6px; }
.command-results { max-height: 48vh; overflow: auto; padding: 8px 0; }
.command-result { display: flex; align-items: center; gap: 12px; padding: 11px; border-radius: var(--radius-sm); cursor: pointer; }
.command-result.selected { background: var(--brand-soft); color: var(--brand); }
.command-result small { margin-left: auto; color: var(--text-faint); }
.command-note { font-size: 12px; color: var(--text-faint); }
</style>
