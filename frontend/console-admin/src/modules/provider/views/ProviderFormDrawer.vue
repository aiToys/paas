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
    <!-- 编辑模式：影响边界说明（spec §8.3 修改不得暗示存量已同步）+ 引用预检 -->
    <template
      v-if="mode === 'edit'"
      #prepend
    >
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
      >
        供应商是<strong>通道配置源</strong>：修改 BaseURL / 凭证只影响之后新建的通道
        （选供应商时快照带入）；存量通道的 BaseURL / 凭证<strong>不会自动同步</strong>，
        需逐个编辑通道更新。
      </el-alert>
      <div
        v-loading="refsLoading"
        class="refs-block"
      >
        <div class="refs-title">
          关联通道（{{ channelRefs.length }}）
          <span class="refs-sub">删除或修改供应商前请确认以下引用</span>
        </div>
        <div
          v-if="!refsLoading && channelRefs.length === 0"
          class="refs-empty"
        >
          暂无通道引用该供应商
        </div>
        <div
          v-for="refItem in channelRefs.slice(0, 8)"
          :key="refItem.key"
          class="refs-item"
        >
          <el-tag
            size="small"
            type="info"
          >{{ refItem.model }}</el-tag>
          <span>{{ refItem.channel }}</span>
          <el-tag
            v-if="refItem.needsCredential"
            size="small"
            type="danger"
          >缺少凭证</el-tag>
        </div>
        <div
          v-if="channelRefs.length > 8"
          class="refs-more"
        >
          …共 {{ channelRefs.length }} 个通道
        </div>
      </div>
    </template>

    <!-- 凭证：选择 + 内嵌创建（spec §8.3 凭证缺失时从表单打开创建抽屉，成功回填且草稿不丢） -->
    <template #field-credentialRef>
      <div class="credential-field">
        <el-select
          v-model="formData.credentialRef"
          filterable
          clearable
          :disabled="mode === 'view'"
          placeholder="平台级 Secret（可选）"
          data-testid="vendor-credential-select"
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
          data-testid="vendor-credential-create"
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
  createVendor,
  updateVendor,
  fetchPlatformSecrets,
  fetchModelList,
  PROVIDER_TYPES,
  type Vendor,
  type SecretOption,
  type ModelInfo
} from '@/modules/model/api'
import PlatformSecretCreateDialog from '@/modules/model/views/PlatformSecretCreateDialog.vue'

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
const secretDialog = ref(false)

// 引用预检（编辑模式）：该供应商被哪些模型的通道引用。
const channelRefs = ref<{ key: string; model: string; channel: string; needsCredential: boolean }[]>([])
const refsLoading = ref(false)

watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) {
    initForm()
    if (props.mode === 'edit') loadRefs()
  }
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
    placeholder: 'https://api.openai.com/v1'
  },
  {
    prop: 'credentialRef',
    label: '凭证',
    type: 'select',
    span: 24,
    placeholder: '平台级 Secret（可选）'
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

// 引用预检：全量模型 → 展开通道 → vendorId 匹配。通道凭证缺失一并列出
// （该供应商改凭证后这些通道不会自动同步，缺失更需人工处理）。
const loadRefs = async () => {
  if (!props.data?.id) return
  refsLoading.value = true
  try {
    const models = (await fetchModelList()) ?? []
    const refs: typeof channelRefs.value = []
    for (const m of models as ModelInfo[]) {
      for (const ch of m.channels ?? []) {
        if (ch.vendorId !== props.data.id) continue
        refs.push({
          key: `${m.id}/${ch.id}`,
          model: m.id,
          channel: ch.id,
          needsCredential: ch.type === 'openai-compatible' && !ch.credentialRef
        })
      }
    }
    channelRefs.value = refs
  } catch {
    channelRefs.value = []
    // 引用加载失败不阻塞编辑（拦截器已提示）；删除入口另有独立预检
  } finally {
    refsLoading.value = false
  }
}

// 凭证创建成功：刷新下拉 + 回填；表单草稿其他字段不动（对话框独立状态）。
const onSecretCreated = async (s: SecretOption) => {
  try {
    secretOptions.value = await fetchPlatformSecrets()
  } catch {
    secretOptions.value = [s] // 刷新失败至少保证新凭证可选
  }
  formData.credentialRef = s.id
  ElMessage.success(`凭证已回填：${s.id}`)
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

<style scoped>
.credential-field {
  display: flex;
  gap: 8px;
  width: 100%;
}
.credential-field .el-select {
  flex: 1;
}
.refs-block {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  padding: 10px 12px;
  margin-bottom: 16px;
}
.refs-title {
  font-weight: 600;
  margin-bottom: 6px;
}
.refs-sub {
  font-weight: 400;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-left: 8px;
}
.refs-empty {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.refs-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
  font-size: 13px;
}
.refs-more {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}
</style>
