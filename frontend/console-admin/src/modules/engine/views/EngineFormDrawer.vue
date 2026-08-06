<template>
  <el-dialog
    v-model="visible"
    :title="drawerTitle"
    width="560px"
    :close-on-click-modal="false"
    @open="initForm"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
      <el-form-item label="引擎 ID" prop="id">
        <el-input v-model="form.id" :disabled="mode !== 'add'" placeholder="如 pg-managed / milvus-shared" />
      </el-form-item>
      <el-form-item label="展示名" prop="label">
        <el-input v-model="form.label" placeholder="如 PostgreSQL（平台托管）" />
      </el-form-item>
      <el-form-item label="Kind" prop="kind">
        <el-select v-model="form.kind" :disabled="mode !== 'add'" style="width: 100%">
          <el-option v-for="k in KINDS" :key="k.value" :label="k.label" :value="k.value" />
        </el-select>
      </el-form-item>
      <el-form-item label="Engine" prop="engine">
        <el-input v-model="form.engine" :disabled="mode !== 'add'" placeholder="postgres / milvus / qdrant" />
      </el-form-item>
      <el-form-item label="模式" prop="mode">
        <el-select v-model="form.mode" style="width: 100%">
          <el-option v-for="m in ENGINE_MODES" :key="m.value" :label="m.label" :value="m.value" />
        </el-select>
      </el-form-item>
      <el-form-item label="排序">
        <el-input-number v-model="form.order" :min="0" :max="999" />
        <span class="hint">数字小→大排列</span>
      </el-form-item>
      <el-form-item label="对用户启用">
        <el-switch v-model="form.enabled" />
      </el-form-item>
      <el-form-item label="描述">
        <el-input v-model="form.description" placeholder="引擎说明（可选）" />
      </el-form-item>
      <el-form-item v-if="form.mode === 'external-shared'" label="共享集群连接">
        <el-input v-model="connText" type="textarea" :rows="4"
          placeholder='JSON，如 {"host":"milvus.internal","port":"19530","token":"xxx"}' />
        <span class="hint">admin 配置，用户创建实例时复用此连接</span>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="submit">确定</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
import { ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import type { FormInstance } from 'element-plus'
import { createEngine, updateEngine, KINDS, ENGINE_MODES, type Engine } from '../api'

const props = defineProps<{ modelValue: boolean; mode: 'add' | 'edit'; data: Engine | null }>()
const emit = defineEmits<{ 'update:modelValue': [v: boolean]; success: [] }>()

const visible = computed({ get: () => props.modelValue, set: (v) => emit('update:modelValue', v) })
const submitting = ref(false)
const formRef = ref<FormInstance>()
const connText = ref('')

const form = ref<Engine>({
  id: '', kind: 'db', engine: '', label: '', description: '',
  mode: 'managed', enabled: true, connection: {}, order: 0,
})

const drawerTitle = computed(() => (props.mode === 'add' ? '新建引擎' : '编辑引擎'))

const rules = {
  id: [{ required: true, message: '请输入引擎 ID', trigger: 'blur' }],
  label: [{ required: true, message: '请输入展示名', trigger: 'blur' }],
  kind: [{ required: true, message: '请选择 Kind', trigger: 'change' }],
  engine: [{ required: true, message: '请输入 Engine', trigger: 'blur' }],
}

function initForm() {
  if (props.mode === 'edit' && props.data) {
    form.value = { ...props.data, connection: { ...(props.data.connection || {}) } }
    connText.value = props.data.connection && Object.keys(props.data.connection).length
      ? JSON.stringify(props.data.connection, null, 2) : ''
  } else {
    form.value = { id: '', kind: 'db', engine: '', label: '', description: '', mode: 'managed', enabled: true, connection: {}, order: 0 }
    connText.value = ''
  }
}

async function submit() {
  if (!formRef.value) return
  await formRef.value.validate(async (ok) => {
    if (!ok) return
    submitting.value = true
    try {
      const payload = { ...form.value }
      if (form.value.mode === 'external-shared') {
        if (connText.value.trim()) {
          try { payload.connection = JSON.parse(connText.value) }
          catch { ElMessage.error('连接 JSON 格式错误'); return }
        } else { payload.connection = {} }
      } else {
        payload.connection = {}
      }
      if (props.mode === 'add') {
        await createEngine(payload)
        ElMessage.success('创建成功')
      } else {
        await updateEngine(props.data!.id, payload)
        ElMessage.success('更新成功')
      }
      emit('success')
      visible.value = false
    } catch {
      // 拦截器提示
    } finally {
      submitting.value = false
    }
  })
}
</script>

<style scoped>
.hint { margin-left: 8px; font-size: 12px; color: var(--el-text-color-secondary); }
</style>
