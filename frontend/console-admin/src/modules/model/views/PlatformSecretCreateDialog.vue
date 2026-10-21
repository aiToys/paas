<template>
  <el-dialog
    v-model="visible"
    title="新建平台凭证"
    width="480px"
    :close-on-click-modal="false"
    @closed="resetForm"
  >
    <el-form
      ref="formRef"
      :model="form"
      :rules="rules"
      label-width="90px"
    >
      <el-form-item
        label="名称"
        prop="name"
      >
        <el-input
          v-model="form.name"
          placeholder="如 sec-platform-airouter（平台级全平台唯一）"
          data-testid="platform-secret-name"
        />
      </el-form-item>
      <el-form-item
        label="凭证值"
        prop="value"
      >
        <el-input
          v-model="form.value"
          type="password"
          show-password
          autocomplete="new-password"
          placeholder="API Key 明文，仅提交不回显"
          data-testid="platform-secret-value"
        />
      </el-form-item>
      <el-form-item label="描述">
        <el-input
          v-model="form.desc"
          placeholder="用途说明（可选）"
        />
      </el-form-item>
    </el-form>
    <el-alert
      type="info"
      :closable="false"
      show-icon
      style="margin-top: 4px"
    >
      创建走平台既有安全通道（scope=platform，仅平台超管）；成功后自动回填到当前表单的凭证下拉。
    </el-alert>
    <template #footer>
      <el-button @click="visible = false">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="submitting"
        data-testid="platform-secret-submit"
        @click="submit"
      >
        创建
      </el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
// 平台凭证创建对话框（spec 2026-10-19 §8.3「凭证缺失时可从表单打开平台凭证创建抽屉」）。
// 由供应商/通道表单内嵌打开；成功 emit('created', {id,name}) 供调用方回填 credentialRef
// 并刷新下拉——调用方表单草稿不重置（对话框独立于表单状态）。
import { ref, reactive, watch } from 'vue'
import type { FormInstance, FormRules } from 'element-plus'
import { ElMessage } from 'element-plus'
import { createPlatformSecret, type SecretOption } from '../api'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{
  'update:modelValue': [v: boolean]
  created: [secret: SecretOption]
}>()

const visible = ref(props.modelValue)
watch(() => props.modelValue, (v) => { visible.value = v })
watch(visible, (v) => emit('update:modelValue', v))

const formRef = ref<FormInstance>()
const submitting = ref(false)
const form = reactive({ name: '', value: '', desc: '' })
const rules: FormRules = {
  name: [{ required: true, message: '请输入凭证名称', trigger: 'blur' }],
  value: [{ required: true, message: '请输入凭证值', trigger: 'blur' }]
}

const resetForm = () => {
  form.name = ''
  form.value = ''
  form.desc = ''
  formRef.value?.clearValidate()
}

const submit = async () => {
  if (!(await formRef.value?.validate().catch(() => false))) return
  submitting.value = true
  try {
    const created = await createPlatformSecret({
      name: form.name,
      value: form.value,
      desc: form.desc || undefined
    })
    ElMessage.success(`平台凭证 ${created.id} 已创建`)
    emit('created', { id: created.id, name: created.name ?? form.name })
    visible.value = false
  } catch {
    // 拦截器提示（403 非超管 / 409 重名等）
  } finally {
    submitting.value = false
  }
}
</script>
