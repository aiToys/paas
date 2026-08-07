<template>
  <el-drawer
    :model-value="modelValue"
    title="代建环境（指定归属租户）"
    size="420px"
    @update:model-value="(v) => emit('update:modelValue', v)"
    @open="loadTenants"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
      <el-form-item label="归属租户" prop="tenantId">
        <el-select v-model="form.tenantId" placeholder="选择租户" filterable style="width: 100%">
          <el-option v-for="t in tenants" :key="t.id" :label="`${t.name} (${t.id})`" :value="t.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="名称" prop="name">
        <el-input v-model="form.name" placeholder="如 prod / staging" />
      </el-form-item>
      <el-form-item label="实例 ID">
        <el-input v-model="form.id" placeholder="可选，留空后端生成" />
      </el-form-item>
      <el-form-item label="类型" prop="type">
        <el-select v-model="form.type" style="width: 100%">
          <el-option label="生产 prod" value="prod" />
          <el-option label="测试 test" value="test" />
        </el-select>
      </el-form-item>
      <el-form-item label="集群">
        <el-input v-model="form.cluster" placeholder="如 prod-bj" />
      </el-form-item>
      <el-form-item label="描述">
        <el-input v-model="form.desc" type="textarea" :rows="2" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="submit">代建</el-button>
    </template>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, reactive } from 'vue'
import type { FormInstance } from 'element-plus'
import { ElMessage } from 'element-plus'
import { fetchAllTenants, createEnvironmentForTenant, type AdminTenant } from '../api'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'created'): void }>()

const formRef = ref<FormInstance>()
const tenants = ref<AdminTenant[]>([])
const submitting = ref(false)

const form = reactive({
  tenantId: '',
  name: '',
  id: '',
  type: 'test' as 'prod' | 'test',
  cluster: '',
  desc: ''
})
const rules = {
  tenantId: [{ required: true, message: '请选择租户', trigger: 'change' }],
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  type: [{ required: true, message: '请选择类型', trigger: 'change' }]
}

const loadTenants = async () => {
  if (tenants.value.length) return
  try {
    tenants.value = (await fetchAllTenants()) ?? []
  } catch {
    /* ignore */
  }
}

const submit = async () => {
  if (!formRef.value) return
  await formRef.value.validate()
  submitting.value = true
  try {
    const body: Parameters<typeof createEnvironmentForTenant>[0] = {
      tenantId: form.tenantId,
      name: form.name,
      type: form.type
    }
    if (form.id) body.id = form.id
    if (form.cluster) body.cluster = form.cluster
    if (form.desc) body.desc = form.desc
    await createEnvironmentForTenant(body)
    ElMessage.success('已代建环境')
    emit('created')
    emit('update:modelValue', false)
  } finally {
    submitting.value = false
  }
}
</script>
