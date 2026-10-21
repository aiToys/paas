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
  >
    <!-- 凭证：供应商带入后仍可改（覆盖快照）；内嵌创建（spec §8.3） -->
    <template #field-credentialRef>
      <div class="credential-field">
        <el-select
          v-model="formData.credentialRef"
          filterable
          clearable
          :disabled="mode === 'view'"
          placeholder="平台级 Secret（openai-compatible 需要）"
          data-testid="channel-credential-select"
        >
          <el-option
            v-for="s in secretOptions"
            :key="s.id"
            :label="`${s.name} (${s.id})`"
            :value="s.id"
          />
        </el-select>
        <el-button
          :disabled="mode === 'view'"
          data-testid="channel-credential-create"
          @click="secretDialog = true"
        >
          新建凭证
        </el-button>
      </div>
    </template>
  </FormDrawer>

  <PlatformSecretCreateDialog
    v-model="secretDialog"
    @created="onSecretCreated"
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
  fetchVendorList,
  fetchPlatformSecrets,
  CHANNEL_TYPES,
  CHANNEL_STATUS,
  type ModelChannel,
  type ChannelCreateRequest,
  type Vendor,
  type SecretOption
} from '../api'
import PlatformSecretCreateDialog from './PlatformSecretCreateDialog.vue'

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
const vendorOptions = ref<Vendor[]>([])
const secretOptions = ref<SecretOption[]>([])
const secretDialog = ref(false)

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
  credentialRef: '',
  vendorId: ''
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
    placeholder: '如 glm-5.2#airouter'
  },
  { prop: 'type', label: '类型', type: 'select', span: 12, options: CHANNEL_TYPES },
  { prop: 'priority', label: '优先级', type: 'number', span: 12, placeholder: '数字越小越优先' },
  // 手动路由标记（非探活诊断）：offline=摘出路由；观测证据在详情「最近诊断」列。
  { prop: 'status', label: '路由标记', type: 'select', span: 12, options: CHANNEL_STATUS },
  {
    prop: 'vendorId',
    label: '供应商',
    type: 'select',
    span: 24,
    options: vendorOptions.value.map((v) => ({ label: `${v.name} (${v.id})`, value: v.id })),
    placeholder: '选供应商带入 BaseURL/凭证快照（供应商后续修改不同步）',
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  },
  {
    prop: 'upstreamModel',
    label: '上游模型',
    type: 'input',
    span: 24,
    placeholder: '供应商侧模型名（如 glm-5.2 / qwen-plus）',
    dependencies: [{ trigger: 'type', show: (v) => isReal(v) }]
  },
  {
    prop: 'credentialRef',
    label: '凭证',
    type: 'select',
    span: 24,
    placeholder: '平台级 Secret（选供应商自动带入，可覆盖）',
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
    credentialRef: '',
    vendorId: ''
  }
  if ((props.mode === 'edit' || props.mode === 'view') && props.data) {
    Object.assign(formData, props.data)
  } else {
    Object.assign(formData, empty)
  }
}

// 凭证创建成功：刷新下拉 + 回填；通道表单草稿（含供应商带入的其他字段）不动。
const onSecretCreated = async (s: SecretOption) => {
  try {
    secretOptions.value = await fetchPlatformSecrets()
  } catch {
    secretOptions.value = [s]
  }
  formData.credentialRef = s.id
  ElMessage.success(`凭证已回填：${s.id}`)
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
    vendorOptions.value = await fetchVendorList()
  } catch {
    // 加载失败静默（下拉为空，可先去供应商管理创建）
  }
  try {
    secretOptions.value = await fetchPlatformSecrets()
  } catch {
    // 凭证下拉加载失败静默（新建后 onSecretCreated 会再刷新）
  }
})
</script>

<style scoped>
.credential-field {
  display: flex;
  gap: 8px;
  width: 100%;
}
.credential-field .el-select {
  flex: 1;
}
</style>
