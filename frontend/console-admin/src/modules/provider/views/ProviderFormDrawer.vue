<template>
  <FormDrawer
    ref="formDrawerRef"
    v-model="visible"
    :title="drawerTitle"
    :mode="mode"
    :form-data="formData"
    :fields="fields"
    :rules="rules"
    :loading="submitting"
    width="600px"
    @submit="handleSubmit"
  />
</template>

<script lang="ts" setup>
import { ref, watch, reactive, computed, onMounted } from 'vue'
import { FormDrawer } from '@/app/components'
import type { FormField, FormDrawerMode } from '@/app/components/FormDrawer/types'
import { ElMessage } from 'element-plus'
import {
  createVendor,
  updateVendor,
  fetchPlatformSecrets,
  PROVIDER_TYPES,
  type Vendor,
  type SecretOption
} from '../api'

const props = defineProps<{
  modelValue: boolean
  mode: FormDrawerMode
  data: Vendor | null
}>()

const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  success: []
}>()

const visible = ref(props.modelValue)
const submitting = ref(false)
const formDrawerRef = ref<InstanceType<typeof FormDrawer>>()
const secretOptions = ref<SecretOption[]>([])

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) initForm()
})
watch(visible, (v) => emit('update:modelValue', v))

const formData = reactive<Vendor>({
  id: '',
  name: '',
  type: 'openai-compatible',
  baseUrl: '',
  credentialRef: '',
  description: ''
})

const drawerTitle = computed(() => {
  if (props.mode === 'add') return '新建供应商'
  if (props.mode === 'edit') return '编辑供应商'
  return '供应商详情'
})

const fields = computed<FormField[]>(() => [
  {
    prop: 'id',
    label: '供应商 ID',
    type: 'input',
    span: 12,
    disabled: props.mode !== 'add',
    placeholder: '如 airouter / openai'
  },
  { prop: 'name', label: '展示名', type: 'input', span: 12, placeholder: '如 airouter 网关' },
  { prop: 'type', label: '类型', type: 'select', span: 12, options: PROVIDER_TYPES },
  {
    prop: 'baseUrl',
    label: 'BaseURL',
    type: 'input',
    span: 24,
    placeholder: 'https://airouter.ddmc-inc.com/api/v1'
  },
  {
    prop: 'credentialRef',
    label: '凭证',
    type: 'select',
    span: 24,
    options: secretOptions.value.map((s) => ({ label: `${s.name} (${s.id})`, value: s.id })),
    placeholder: '平台级 Secret（先在「平台能力->安全」创建）'
  },
  { prop: 'description', label: '描述', type: 'input', span: 24, placeholder: '供应商说明（可选）' }
])

const rules = computed(() => ({
  id: [{ required: true, message: '请输入供应商 ID', trigger: 'blur' }],
  name: [{ required: true, message: '请输入展示名', trigger: 'blur' }],
  baseUrl: [{ required: true, message: '请输入 BaseURL', trigger: 'blur' }]
}))

const initForm = () => {
  const empty: Vendor = {
    id: '',
    name: '',
    type: 'openai-compatible',
    baseUrl: '',
    credentialRef: '',
    description: ''
  }
  if ((props.mode === 'edit' || props.mode === 'view') && props.data) {
    Object.assign(formData, props.data)
  } else {
    Object.assign(formData, empty)
  }
}

const handleSubmit = async (data: Record<string, unknown>) => {
  submitting.value = true
  try {
    const payload = { ...formData, ...data } as Vendor
    if (props.mode === 'add') {
      await createVendor(payload)
      ElMessage.success('创建成功')
    } else {
      await updateVendor(props.data!.id, payload)
      ElMessage.success('更新成功')
    }
    emit('success')
    emit('update:modelValue', false)
  } catch {
    // 拦截器提示
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  try {
    secretOptions.value = await fetchPlatformSecrets()
  } catch {
    // 加载失败静默
  }
})
</script>
