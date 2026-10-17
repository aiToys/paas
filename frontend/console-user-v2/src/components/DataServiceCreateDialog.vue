<script setup lang="ts">
// 数据服务创建弹窗（v2 原生）：Kind → 引擎（/api/engines，mode 决定托管/外部）→
// 名称/环境 → spec 动态字段（/api/dataservices/meta KindMetas 驱动）；
// external-dedicated 引擎补 connection.uri 输入。契约对齐老版 DataServices。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchJSON, apiError, fetchList } from '@/api'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'created'): void }>()

const envStore = useEnvStore()

interface Engine { id: string; kind: string; label: string; mode: string; enabled: boolean }
interface MetaField { key: string; label: string; type: string; options: string[] | null; default: unknown }
interface KindMeta { kind: string; label: string; fields: MetaField[] }

const engines = ref<Engine[]>([])
const metas = ref<KindMeta[]>([])

const KINDS = [
  { kind: 'db', label: '数据库' }, { kind: 'cache', label: '缓存' }, { kind: 'mq', label: '消息' },
  { kind: 'storage', label: '对象存储' }, { kind: 'vector', label: '向量' }, { kind: 'search', label: '搜索' },
]

const kind = ref('db')
const engineId = ref('')
const name = ref('')
const envId = ref('')
const spec = ref<Record<string, string>>({})
const connectionUri = ref('')
const submitting = ref(false)

async function loadMeta() {
  const [es, ms] = await Promise.allSettled([
    fetchList<Engine>('/api/engines'),
    fetchList<KindMeta>('/api/dataservices/meta'),
  ])
  if (es.status === 'fulfilled') engines.value = es.value.filter((e) => e.enabled)
  if (ms.status === 'fulfilled') metas.value = ms.value
}
watch(() => props.modelValue, (open) => {
  if (open) {
    kind.value = 'db'; engineId.value = ''; name.value = ''
    envId.value = envStore.currentEnv?.id ?? envStore.envs[0]?.id ?? ''
    spec.value = {}; connectionUri.value = ''
    void loadMeta()
  }
})

const kindEngines = computed(() => engines.value.filter((e) => e.kind === kind.value))
const selectedEngine = computed(() => kindEngines.value.find((e) => e.id === engineId.value))
const needUri = computed(() => selectedEngine.value?.mode === 'external-dedicated')
const metaFields = computed(() => metas.value.find((m) => m.kind === kind.value)?.fields ?? [])

// 切 kind 重置引擎与动态字段（字段 default 回填）。
watch(kind, () => {
  engineId.value = ''
  resetSpec()
})
watch(engineId, () => resetSpec())
function resetSpec() {
  const m: Record<string, string> = {}
  for (const f of metaFields.value) m[f.key] = String(f.default ?? '')
  spec.value = m
}

const canSubmit = computed(() => !!name.value.trim() && !!engineId.value && !!envId.value && (!needUri.value || !!connectionUri.value.trim()))

async function submit() {
  if (!canSubmit.value) {
    ElMessage.warning(needUri.value ? '请填写名称/引擎/环境/连接 URI' : '请填写名称/引擎/环境')
    return
  }
  submitting.value = true
  try {
    const body: Record<string, unknown> = {
      engineId: engineId.value,
      name: name.value.trim(),
      envId: envId.value,
      spec: spec.value,
    }
    if (needUri.value) body.connection = { uri: connectionUri.value.trim() }
    await fetchJSON('/api/dataservices', { method: 'POST', body: JSON.stringify(body) })
    ElMessage.success(`实例「${name.value.trim()}」创建中（拉起需约半分钟）`)
    emit('update:modelValue', false)
    emit('created')
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="新建数据服务"
    width="520px"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-position="top" @submit.prevent="submit">
      <el-form-item label="类型" required>
        <div class="kind-row">
          <button
            v-for="k in KINDS" :key="k.kind"
            type="button"
            class="kind-chip" :class="{ on: kind === k.kind }"
            @click="kind = k.kind"
          >
{{ k.label }}
</button>
        </div>
      </el-form-item>
      <el-form-item label="引擎" required>
        <el-select v-model="engineId" placeholder="选择引擎" style="width: 100%">
          <el-option v-for="e in kindEngines" :key="e.id" :value="e.id" :label="e.label" />
        </el-select>
        <span v-if="selectedEngine" class="mode-hint">
          {{ selectedEngine.mode === 'managed' ? '平台托管：自动拉起实例（备份记录不等于真实快照）' : selectedEngine.mode === 'external-shared' ? '共享集群：平台侧连接池' : '独占外部：连接你自管的实例' }}
        </span>
      </el-form-item>
      <el-form-item label="实例名" required>
        <el-input v-model="name" placeholder="如 shop-pg-prod" />
      </el-form-item>
      <el-form-item label="环境" required>
        <el-select v-model="envId" style="width: 100%">
          <el-option v-for="e in envStore.envs" :key="e.id" :value="e.id" :label="e.name" />
        </el-select>
      </el-form-item>
      <div v-if="metaFields.length" class="spec-grid">
        <el-form-item v-for="f in metaFields" :key="f.key" :label="f.label">
          <el-select v-if="f.type === 'select'" v-model="spec[f.key]" style="width: 100%">
            <el-option v-for="o in f.options ?? []" :key="o" :value="o" :label="o" />
          </el-select>
          <el-input v-else v-model="spec[f.key]" />
        </el-form-item>
      </div>
      <el-form-item v-if="needUri" label="连接 URI" required>
        <el-input v-model="connectionUri" class="mono" placeholder="如 postgres://user:pass@host:5432/db" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">创建</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.kind-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.kind-chip {
  padding: 5px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12.5px;
  cursor: pointer;
}
.kind-chip.on {
  border-color: var(--brand);
  color: var(--brand);
  background: var(--brand-soft);
}
.mode-hint {
  display: block;
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--text-faint);
  line-height: 1.5;
}
.spec-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0 16px;
}
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
