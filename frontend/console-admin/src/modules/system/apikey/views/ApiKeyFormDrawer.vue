<template>
  <FormDrawer
    ref="formDrawerRef"
    v-model="visible"
    title="新建 API 密钥"
    mode="add"
    :form-data="formData"
    :fields="fields"
    :rules="rules"
    :loading="submitting"
    width="560px"
    @submit="handleSubmit"
  />
</template>

<script lang="ts" setup>
import { ref, watch, reactive, computed, onMounted } from 'vue'
import { FormDrawer } from '@/app/components'
import type { FormField } from '@/app/components/FormDrawer/types'
import { ElMessage } from 'element-plus'
import {
  createApiKey,
  fetchAllTenants,
  ROLE_OPTIONS,
  type ApiKeyCreateRequest
} from '../api'

const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  // 创建成功回传明文 key（仅此一次可见），供父组件弹展示框。
  created: [key: string, info: { id: string; roles: string[] }]
}>()

const visible = ref(props.modelValue)
const submitting = ref(false)
const formDrawerRef = ref<InstanceType<typeof FormDrawer>>()
const tenantOptions = ref<{ id: string; name: string }[]>([])

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) initForm()
})
watch(visible, (v) => emit('update:modelValue', v))

// 表单收集单角色（role string）；提交时包成 roles[]（core 契约）。
const formData = reactive({
  tenantId: '',
  userId: '',
  role: 'developer'
})

const fields = computed<FormField[]>(() => [
  {
    prop: 'tenantId',
    label: '归属租户',
    type: 'select',
    span: 24,
    options: tenantOptions.value.map((t) => ({ label: `${t.name} (${t.id})`, value: t.id })),
    placeholder: '选择密钥所属租户'
  },
  { prop: 'userId', label: '归属用户 ID', type: 'input', span: 24, placeholder: '如 u-acme-dev' },
  {
    prop: 'role',
    label: '角色',
    type: 'select',
    span: 24,
    options: ROLE_OPTIONS
  }
])

const rules = computed(() => ({
  tenantId: [{ required: true, message: '请选择租户', trigger: 'change' }],
  userId: [{ required: true, message: '请输入用户 ID', trigger: 'blur' }],
  role: [{ required: true, message: '请选择角色', trigger: 'change' }]
}))

const initForm = () => {
  Object.assign(formData, { tenantId: '', userId: '', role: 'developer' })
}

const handleSubmit = async (data: Record<string, unknown>) => {
  submitting.value = true
  try {
    const role = (data.role as string) || formData.role
    const payload: ApiKeyCreateRequest = {
      tenantId: (data.tenantId as string) || formData.tenantId,
      userId: (data.userId as string) || formData.userId,
      roles: [role]
    }
    const k = await createApiKey(payload)
    ElMessage.success('创建成功，请立即复制完整密钥')
    emit('created', k.key, { id: k.id, roles: k.roles })
    emit('update:modelValue', false)
  } catch {
    // 失败由 http 拦截器提示
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  try {
    tenantOptions.value = await fetchAllTenants()
  } catch {
    // 加载失败静默（下拉为空，提示去租户管理建租户）
  }
})
</script>
