<template>
  <el-drawer
    :model-value="modelValue"
    :title="title"
    :size="width"
    @update:model-value="$emit('update:modelValue', $event)"
  >
    <div
      v-loading="loading"
      class="form-drawer-body"
    >
      <!-- prepend：表单顶部说明区（如编辑影响边界提示），可选 -->
      <slot name="prepend" />
      <el-form
        ref="formRef"
        :model="formData"
        :rules="rules"
        label-width="100px"
      >
        <el-row :gutter="16">
          <el-col
            v-for="field in visibleFields"
            :key="field.prop"
            :span="field.span || 24"
          >
            <el-form-item
              :label="field.label"
              :prop="field.prop"
              :rules="field.rules"
            >
              <slot
                :name="`field-${field.prop}`"
                :field="field"
              >
                <component
                  :is="resolveComponent(field.type)"
                  v-model="formData[field.prop]"
                  :placeholder="field.placeholder || t('common.placeholder.input', { label: field.label })"
                  :disabled="field.disabled || loading || mode === 'view'"
                  v-bind="resolveExtraProps(field)"
                >
                  <template v-if="field.type === 'select'">
                    <el-option
                      v-for="opt in field.options"
                      :key="String(opt.value)"
                      :label="opt.label"
                      :value="opt.value"
                    />
                  </template>
                  <template v-if="field.type === 'radio'">
                    <el-radio
                      v-for="opt in field.options"
                      :key="String(opt.value)"
                      :value="opt.value"
                    >
                      {{ opt.label }}
                    </el-radio>
                  </template>
                  <template v-if="field.type === 'treeSelect'">
                    <!-- formData 值为 unknown，组件对象化后 vue-tsc 真实检查 modelValue，此处窄化 -->
                    <el-tree-select
                      :model-value="formData[field.prop] as never"
                      :data="field.treeData"
                      :props="
                        field.treeProps || {
                          label: 'label',
                          children: 'children'
                        }
                      "
                      node-key="id"
                      check-strictly
                      :placeholder="field.placeholder || t('common.placeholder.select', { label: field.label })"
                      :disabled="field.disabled || loading || mode === 'view'"
                      @update:model-value="formData[field.prop] = $event"
                    />
                  </template>
                </component>
              </slot>
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
    </div>

    <template #footer>
      <el-button
        v-if="mode === 'view'"
        @click="handleCancel"
      >
        {{ t('common.action.close') }}
      </el-button>
      <template v-else>
        <el-button @click="handleCancel">
          {{ t('common.action.cancel') }}
        </el-button>
        <el-button
          type="primary"
          :loading="loading"
          @click="handleConfirm"
        >
          {{ t('common.action.confirm') }}
        </el-button>
      </template>
    </template>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, computed, type Component } from 'vue'
import { t } from '@/lib/i18n'
// 动态映射必须用显式 import 的组件对象，不能用 'el-select' 字符串：Element Plus
// 按需引入（unplugin 编译期解析）下运行时字符串解析不到真实组件，el-option 因
// 缺 el-select 祖先 throwError（2026-10-13 实测创建用户页整页崩进 ErrorBoundary）。
// 显式 import 经 tree-shake 只打包用到组件的 JS；样式不经模板编译需手动引入。
import {
  ElCascader, ElCheckboxGroup, ElDatePicker, ElInput, ElInputNumber,
  ElRadioGroup, ElSelect, ElSwitch, ElTreeSelect,
} from 'element-plus'
import 'element-plus/es/components/cascader/style/css'
import 'element-plus/es/components/checkbox/style/css'
import 'element-plus/es/components/date-picker/style/css'
import 'element-plus/es/components/input-number/style/css'
import 'element-plus/es/components/input/style/css'
import 'element-plus/es/components/radio/style/css'
import 'element-plus/es/components/select/style/css'
import 'element-plus/es/components/switch/style/css'
import 'element-plus/es/components/tree-select/style/css'
import type { FormInstance } from 'element-plus'
import type { FormDrawerProps, FormField, FormFieldType } from './types'

const props = withDefaults(defineProps<FormDrawerProps>(), {
  loading: false,
  width: '500px',
  mode: 'add'
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  submit: [data: Record<string, unknown>]
}>()

const formRef = ref<FormInstance>()

const componentMap: Record<FormFieldType, Component> = {
  input: ElInput,
  textarea: ElInput,
  number: ElInputNumber,
  select: ElSelect,
  radio: ElRadioGroup,
  checkbox: ElCheckboxGroup,
  switch: ElSwitch,
  date: ElDatePicker,
  password: ElInput,
  treeSelect: ElTreeSelect,
  cascader: ElCascader
}

const resolveComponent = (type: FormField['type']): Component =>
  componentMap[type] || ElInput

const resolveExtraProps = (field: FormField): Record<string, unknown> => {
  if (field.type === 'textarea') return { type: 'textarea', rows: 3 }
  if (field.type === 'password') return { type: 'password', showPassword: true }
  return {}
}

/** 声明式显隐：字段的 dependencies 全部 show 返回 true 时才可见 */
const visibleFields = computed(() => {
  return props.fields.filter((field) => {
    if (!field.dependencies || field.dependencies.length === 0) return true
    return field.dependencies.every((dep) =>
      dep.show(props.formData, { mode: props.mode })
    )
  })
})

const handleCancel = (): void => emit('update:modelValue', false)

const handleConfirm = async (): Promise<void> => {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
    emit('submit', { ...props.formData })
  } catch {
    // 校验失败，element-plus 会显示错误
  }
}

defineExpose({
  /** 暴露 formRef 供调用方手动触发关联字段重校验 */
  validateField: (prop: string) => formRef.value?.validateField(prop)
})
</script>

<style lang="scss" scoped>
.form-drawer-body {
  min-height: 100%;
}
</style>
