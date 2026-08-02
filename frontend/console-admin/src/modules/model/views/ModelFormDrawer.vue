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
    width="620px"
    @submit="handleSubmit"
  />
</template>

<script lang="ts" setup>
import { ref, watch, reactive, computed } from 'vue'
import { FormDrawer } from '@/app/components'
import type { FormField, FormDrawerMode } from '@/app/components/FormDrawer/types'
import { ElMessage } from 'element-plus'
import { createModel, updateModel, type ModelInfo, type ModelCreateRequest } from '../api'

const props = defineProps<{
  modelValue: boolean
  mode: FormDrawerMode
  data: ModelInfo | null
}>()

const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  success: []
}>()

const visible = ref(props.modelValue)
const submitting = ref(false)
const formDrawerRef = ref<InstanceType<typeof FormDrawer>>()

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) initForm()
})
watch(visible, (v) => emit('update:modelValue', v))

// capabilities 用逗号分隔字符串录入（FormDrawer 无原生多值 input），提交时 split。
const formData = reactive({
  id: '',
  name: '',
  vendor: '',
  contextWindow: 8192,
  capabilitiesText: '',
  inputPrice: 0,
  outputPrice: 0,
  description: '',
})

const drawerTitle = computed(() => {
  if (props.mode === 'add') return '新建模型'
  if (props.mode === 'edit') return '编辑模型'
  return '模型详情'
})

const fields = computed<FormField[]>(() => [
  {
    prop: 'id',
    label: '模型 ID',
    type: 'input',
    span: 12,
    disabled: props.mode !== 'add',
    placeholder: '如 gpt-4o（路由键，全局唯一）',
  },
  { prop: 'name', label: '模型名称', type: 'input', span: 12 },
  { prop: 'vendor', label: '供应商', type: 'input', span: 12 },
  { prop: 'contextWindow', label: '上下文窗口', type: 'number', span: 12 },
  {
    prop: 'capabilitiesText',
    label: '能力',
    type: 'input',
    span: 24,
    placeholder: '逗号分隔：chat,vision,reasoning,embedding',
  },
  { prop: 'inputPrice', label: '输入单价(元/百万)', type: 'number', span: 12 },
  { prop: 'outputPrice', label: '输出单价(元/百万)', type: 'number', span: 12 },
  { prop: 'description', label: '描述', type: 'input', span: 24 },
])

const rules = computed(() => ({
  id: [
    { required: true, message: '请输入模型 ID', trigger: 'blur' },
    { pattern: /^[a-z0-9._-]+$/, message: '仅小写字母/数字/._-', trigger: 'blur' },
  ],
  name: [{ required: true, message: '请输入模型名称', trigger: 'blur' }],
  vendor: [{ required: true, message: '请输入供应商', trigger: 'blur' }],
}))

const initForm = () => {
  const empty = {
    id: '',
    name: '',
    vendor: '',
    contextWindow: 8192,
    capabilitiesText: '',
    inputPrice: 0,
    outputPrice: 0,
    description: '',
  }
  if ((props.mode === 'edit' || props.mode === 'view') && props.data) {
    const m = props.data
    Object.assign(formData, {
      id: m.id,
      name: m.name,
      vendor: m.vendor,
      contextWindow: m.contextWindow,
      capabilitiesText: (m.capabilities ?? []).join(','),
      inputPrice: m.inputPrice,
      outputPrice: m.outputPrice,
      description: m.description ?? '',
    })
  } else {
    Object.assign(formData, empty)
  }
}

const handleSubmit = async (data: Record<string, unknown>) => {
  submitting.value = true
  try {
    const { capabilitiesText, ...rest } = formData
    const capabilities = String(capabilitiesText || '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    const payload = { ...rest, ...data, capabilities } as unknown as ModelCreateRequest
    if (props.mode === 'add') {
      await createModel(payload)
      ElMessage.success('创建成功')
    } else {
      await updateModel(props.data!.id, payload)
      ElMessage.success('更新成功')
    }
    emit('success')
    emit('update:modelValue', false)
  } catch {
    // 失败由 http 拦截器提示
  } finally {
    submitting.value = false
  }
}
</script>
