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
  createChannel,
  updateChannel,
  fetchPlatformSecrets,
  CHANNEL_TYPES,
  CHANNEL_STATUS,
  type ModelChannel,
  type ChannelCreateRequest
} from '../api'

const props = defineProps<{
  modelValue: boolean
  mode: FormDrawerMode
  data: ModelChannel | null
  modelId: string
}>()

const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  success: []
}>()

const visible = ref(props.modelValue)
const submitting = ref(false)
const formDrawerRef = ref<InstanceType<typeof FormDrawer>>()
const secretOptions = ref<{ id: string; name: string }[]>([])

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) initForm()
})
watch(visible, (v) => emit('update:modelValue', v))

const formData = reactive<ChannelCreateRequest>({
  id: '',
  type: 'openai-compatible',
  priority: 0,
  status: 'healthy',
  endpoint: '',
  vendor: '',
  upstreamModel: '',
  credentialRef: ''
})

const drawerTitle = computed(() => {
  if (props.mode === 'add') return '新建通道'
  if (props.mode === 'edit') return '编辑通道'
  return '通道详情'
})

// openai-compatible 才需供应商配置；echo/mock 进程内无需。
const isReal = (values: Record<string, unknown>) => values.type === 'openai-compatible'

const fields = computed<FormField[]>(() => [
  {
    prop: 'id',
    label: '通道 ID',
    type: 'input',
    span: 12,
    disabled: props.mode !== 'add',
    placeholder: '如 gpt-4o#openai'
  },
  { prop: 'type', label: '类型', type: 'select', span: 12, options: CHANNEL_TYPES },
  { prop: 'priority', label: '优先级', type: 'number', span: 12, placeholder: '数字越小越优先' },
  { prop: 'status', label: '状态', type: 'select', span: 12, options: CHANNEL_STATUS },
  {
    prop: 'endpoint',
    label: 'BaseURL',
    type: 'input',
    span: 24,
    placeholder: 'https://api.openai.com/v1',
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  },
  {
    prop: 'vendor',
    label: '供应商',
    type: 'input',
    span: 12,
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  },
  {
    prop: 'upstreamModel',
    label: '上游模型',
    type: 'input',
    span: 12,
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  },
  {
    prop: 'credentialRef',
    label: '凭证',
    type: 'select',
    span: 24,
    options: secretOptions.value.map((s) => ({ label: `${s.name} (${s.id})`, value: s.id })),
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  }
])

const rules = computed(() => ({
  id: [{ required: true, message: '请输入通道 ID', trigger: 'blur' }],
  type: [{ required: true, message: '请选择类型', trigger: 'change' }]
}))

const initForm = () => {
  const empty: ChannelCreateRequest = {
    id: '',
    type: 'openai-compatible',
    priority: 0,
    status: 'healthy',
    endpoint: '',
    vendor: '',
    upstreamModel: '',
    credentialRef: ''
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
    const payload = { ...formData, ...data } as ChannelCreateRequest
    if (props.mode === 'add') {
      await createChannel(props.modelId, payload)
      ElMessage.success('创建成功')
    } else {
      await updateChannel(props.modelId, props.data!.id, payload)
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
    // 加载失败静默（下拉为空，可手填或不配）
  }
})
</script>
