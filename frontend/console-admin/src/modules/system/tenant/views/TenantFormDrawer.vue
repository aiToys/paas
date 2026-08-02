<template>
  <FormDrawer
    v-model="visible"
    title="新建租户"
    mode="add"
    :form-data="formData"
    :fields="fields"
    :rules="rules"
    :loading="submitting"
    width="500px"
    @submit="handleSubmit"
  />
</template>

<script lang="ts" setup>
import { ref, watch, reactive, computed } from 'vue'
import { FormDrawer } from '@/app/components'
import type { FormField } from '@/app/components/FormDrawer/types'
import { ElMessage } from 'element-plus'
import { createTenant, type TenantCreateRequest } from '../api'

// 租户创建抽屉（core 无租户更新端点，仅支持新建）。
const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  success: []
}>()

const visible = ref(props.modelValue)
const submitting = ref(false)

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) Object.assign(formData, { id: '', name: '' })
})
watch(visible, (v) => emit('update:modelValue', v))

const formData = reactive<TenantCreateRequest>({ id: '', name: '' })

const fields = computed<FormField[]>(() => [
  {
    prop: 'id',
    label: '租户 ID',
    type: 'input',
    span: 24,
    placeholder: '如 t-acme，仅小写字母/数字/连字符'
  },
  { prop: 'name', label: '租户名称', type: 'input', span: 24, placeholder: '如 Acme' }
])

const rules = {
  id: [
    { required: true, message: '请输入租户 ID', trigger: 'blur' },
    { pattern: /^[a-z0-9-]+$/, message: '仅小写字母、数字、连字符', trigger: 'blur' },
    { min: 2, max: 32, message: '长度 2-32', trigger: 'blur' }
  ],
  name: [
    { required: true, message: '请输入租户名称', trigger: 'blur' },
    { min: 2, max: 32, message: '长度 2-32', trigger: 'blur' }
  ]
}

const handleSubmit = async (data: Record<string, unknown>) => {
  submitting.value = true
  try {
    const payload = { ...formData, ...data } as TenantCreateRequest
    await createTenant(payload)
    ElMessage.success('租户创建成功')
    emit('success')
    emit('update:modelValue', false)
  } catch {
    // 失败由 http 拦截器提示，吞掉避免冒泡
  } finally {
    submitting.value = false
  }
}
</script>
