<template>
  <el-drawer
    :model-value="modelValue"
    title="代建数据服务（指定归属租户）"
    size="420px"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="load"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
      <el-form-item label="归属租户" prop="tenantId">
        <el-select v-model="form.tenantId" placeholder="选择租户" filterable style="width: 100%">
          <el-option v-for="t in tenants" :key="t.id" :label="`${t.name} (${t.id})`" :value="t.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="引擎" prop="engineId">
        <el-select v-model="form.engineId" placeholder="选择启用的引擎" filterable style="width: 100%" @change="onEngine">
          <el-option
            v-for="e in engines"
            :key="e.id"
            :label="`${e.id} (${e.kind}/${e.engine}, ${modeLabel(e.mode)})`"
            :value="e.id"
          />
        </el-select>
      </el-form-item>
      <el-alert v-if="selectedEngine?.mode === 'external-dedicated'" type="info" :closable="false" class="hint">
        external-dedicated 模式需填写连接 host（下方）。
      </el-alert>
      <el-form-item label="名称" prop="name">
        <el-input v-model="form.name" placeholder="如 mysql-prod" />
      </el-form-item>
      <el-form-item label="实例 ID">
        <el-input v-model="form.id" placeholder="可选，留空后端生成" />
      </el-form-item>
      <el-form-item label="环境">
        <el-input v-model="form.envId" placeholder="可选 envId" />
      </el-form-item>
      <el-form-item label="副本">
        <el-input-number v-model="form.replicas" :min="0" :max="10" />
      </el-form-item>
      <el-form-item label="CPU">
        <el-input v-model="form.cpu" placeholder="如 1" />
      </el-form-item>
      <el-form-item label="内存">
        <el-input v-model="form.memory" placeholder="如 1Gi" />
      </el-form-item>
      <el-form-item label="存储(GB)">
        <el-input-number v-model="form.storageGb" :min="0" />
      </el-form-item>
      <el-form-item v-if="selectedEngine?.mode === 'external-dedicated'" label="连接 host">
        <el-input v-model="connectionHost" placeholder="外部独占实例地址" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="submit">代建</el-button>
    </template>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, reactive, computed } from 'vue'
import type { FormInstance } from 'element-plus'
import { ElMessage } from 'element-plus'
import { api } from '@/lib/http/client'
import { fetchAllTenants, createDataserviceForTenant, type AdminTenant } from '../api'

interface Engine {
  id: string
  kind: string
  engine: string
  mode: string
  enabled: boolean
}

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'created'): void }>()

const formRef = ref<FormInstance>()
const tenants = ref<AdminTenant[]>([])
const engines = ref<Engine[]>([])
const submitting = ref(false)
const connectionHost = ref('')

const form = reactive({
  tenantId: '',
  engineId: '',
  name: '',
  id: '',
  envId: '',
  replicas: 1,
  cpu: '',
  memory: '',
  storageGb: 0
})
const rules = {
  tenantId: [{ required: true, message: '请选择租户', trigger: 'change' }],
  engineId: [{ required: true, message: '请选择引擎', trigger: 'change' }],
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }]
}

const selectedEngine = computed(() => engines.value.find((e) => e.id === form.engineId))
const modeLabel = (m: string) =>
  (({ managed: '平台托管', 'external-shared': '共享集群', 'external-dedicated': '独占外部' }) as Record<string, string>)[m] ?? m

const load = async () => {
  try {
    const [t, e] = await Promise.all([fetchAllTenants(), api.get<Engine[]>('/api/engines?enabled=true')])
    tenants.value = (t ?? []).filter((x) => x.id)
    engines.value = (e ?? []).filter((x) => x.enabled)
  } catch {
    /* ignore */
  }
}
const onEngine = () => {
  connectionHost.value = ''
}

const submit = async () => {
  if (!formRef.value) return
  await formRef.value.validate()
  submitting.value = true
  try {
    const body: Parameters<typeof createDataserviceForTenant>[0] = {
      tenantId: form.tenantId,
      engineId: form.engineId,
      name: form.name,
      replicas: form.replicas
    }
    if (form.id) body.id = form.id
    if (form.envId) body.envId = form.envId
    if (form.cpu) body.cpu = form.cpu
    if (form.memory) body.memory = form.memory
    if (form.storageGb > 0) body.storageGb = form.storageGb
    if (selectedEngine.value?.mode === 'external-dedicated' && connectionHost.value) {
      body.connection = { host: connectionHost.value }
    }
    await createDataserviceForTenant(body)
    ElMessage.success('已代建，实例计入目标租户配额')
    emit('created')
    emit('update:modelValue', false)
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.hint {
  margin: 0 0 12px;
}
</style>
