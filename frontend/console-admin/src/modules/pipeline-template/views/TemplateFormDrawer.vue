<template>
  <el-drawer
    v-model="visible"
    :title="drawerTitle"
    size="780px"
    :close-on-click-modal="false"
    @closed="onClosed"
  >
    <el-form ref="formRef" :model="form" label-width="100px" label-position="right">
      <el-form-item label="模板 ID" prop="id" required>
        <el-input v-model="form.id" placeholder="如 tpl-my-ci（创建后不可改）" :disabled="mode === 'edit'" />
      </el-form-item>
      <el-form-item label="名称" prop="name" required>
        <el-input v-model="form.name" placeholder="如 我的 CI 流水线" />
      </el-form-item>
      <el-form-item label="分类" prop="kind" required>
        <el-select v-model="form.kind" style="width: 100%">
          <el-option v-for="k in PIPELINE_KINDS" :key="k.value" :label="k.label" :value="k.value" />
        </el-select>
      </el-form-item>
      <el-form-item label="描述">
        <el-input v-model="form.description" type="textarea" :rows="2" placeholder="模板用途说明" />
      </el-form-item>

      <el-divider content-position="left">阶段序列（Stage）</el-divider>
      <div class="stage-list">
        <div v-for="(s, i) in form.stages" :key="i" class="stage-row">
          <div class="stage-head">
            <span class="stage-idx">{{ i + 1 }}</span>
            <el-select v-model="s.type" placeholder="阶段类型" style="width: 200px" @change="onTypeChange(s)">
              <el-option v-for="t in STAGE_TYPES" :key="t.value" :label="t.label" :value="t.value" />
            </el-select>
            <el-input v-model="s.name" placeholder="阶段名（如 build/deploy-test）" style="flex: 1" />
            <el-button text type="danger" :icon="Delete" @click="removeStage(i)" />
          </div>
          <div class="stage-params">
            <div class="params-title">参数（key-value，透传 stage.Params）</div>
            <div v-for="(p, j) in paramRows(s)" :key="j" class="param-row">
              <el-input v-model="p.k" placeholder="参数名（如 envId/path）" style="width: 38%" />
              <el-input v-model="p.v" placeholder="参数值（如 {{app.env.test}} / /healthz）" style="flex: 1" />
              <el-button text type="danger" :icon="Delete" @click="removeParam(s, j)" />
            </div>
            <el-button text type="primary" :icon="Plus" @click="addParam(s)">+ 参数</el-button>
          </div>
        </div>
        <el-button class="add-stage" type="primary" plain :icon="Plus" @click="addStage">+ 添加阶段</el-button>
      </div>
    </el-form>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">保存</el-button>
    </template>
  </el-drawer>
</template>

<script lang="ts" setup>
import { ref, reactive, computed, watch } from 'vue'
import { ElMessage, type FormInstance } from 'element-plus'
import { Plus, Delete } from '@element-plus/icons-vue'
import {
  createTemplate,
  updateTemplate,
  STAGE_TYPES,
  PIPELINE_KINDS,
  type PipelineTemplate,
  type StageDef
} from '../api'

const props = defineProps<{ modelValue: boolean; mode: 'add' | 'edit'; data: PipelineTemplate | null }>()
const emit = defineEmits<{ 'update:modelValue': [v: boolean]; success: [] }>()

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v)
})
const drawerTitle = computed(() => (props.mode === 'add' ? '新建模板' : '编辑模板'))

interface FormState {
  id: string
  name: string
  kind: string
  description: string
  stages: StageDef[]
}
const form = reactive<FormState>({ id: '', name: '', kind: 'ci', description: '', stages: [] })
const formRef = ref<FormInstance>()
const submitting = ref(false)

watch(
  () => props.modelValue,
  (v) => {
    if (!v) return
    if (props.mode === 'edit' && props.data) {
      form.id = props.data.id
      form.name = props.data.name
      form.kind = props.data.kind
      form.description = props.data.description ?? ''
      form.stages = (props.data.stages ?? []).map((s) => ({
        name: s.name,
        type: s.type,
        params: { ...(s.params ?? {}) }
      }))
    } else {
      form.id = ''
      form.name = ''
      form.kind = 'ci'
      form.description = ''
      form.stages = [{ name: 'build', type: 'build', params: {} }]
    }
  }
)

// 参数行：params 是 Record<string, unknown>，编辑用 key-value 行（值转 string）
interface ParamRow { k: string; v: string }
const paramRowMaps = ref<WeakMap<StageDef, ParamRow[]>>(new WeakMap())
const paramRows = (s: StageDef): ParamRow[] => {
  let rows = paramRowMaps.value.get(s)
  if (!rows) {
    rows = Object.entries(s.params ?? {}).map(([k, v]) => ({ k, v: String(v ?? '') }))
    paramRowMaps.value.set(s, rows)
  }
  return rows
}
const syncParams = (s: StageDef) => {
  const rows = paramRowMaps.value.get(s) ?? []
  const p: Record<string, unknown> = {}
  for (const r of rows) {
    if (r.k) p[r.k] = r.v
  }
  s.params = p
}
const addParam = (s: StageDef) => {
  paramRows(s).push({ k: '', v: '' })
}
const removeParam = (s: StageDef, idx: number) => {
  paramRows(s).splice(idx, 1)
  syncParams(s)
}

const addStage = () => {
  form.stages.push({ name: '', type: 'deploy', params: {} })
}
const removeStage = (idx: number) => {
  form.stages.splice(idx, 1)
}
const onTypeChange = (_s: StageDef) => {
  // 类型改变时不自动清参数（用户可能想保留）；仅记录
}

const handleSubmit = async () => {
  if (!form.id || !form.name || !form.kind) {
    ElMessage.warning('请填完 ID / 名称 / 分类')
    return
  }
  if (!form.stages.length) {
    ElMessage.warning('至少一个阶段')
    return
  }
  for (const s of form.stages) {
    if (!s.name || !s.type) {
      ElMessage.warning('每个阶段需填名称和类型')
      return
    }
    syncParams(s)
  }
  const payload: PipelineTemplate = {
    id: form.id,
    name: form.name,
    kind: form.kind,
    description: form.description,
    stages: form.stages
  }
  submitting.value = true
  try {
    if (props.mode === 'edit') {
      await updateTemplate(form.id, payload)
      ElMessage.success('已更新')
    } else {
      await createTemplate(payload)
      ElMessage.success('已创建')
    }
    emit('success')
  } catch {
    // 拦截器提示
  } finally {
    submitting.value = false
  }
}

const onClosed = () => {
  formRef.value?.resetFields()
  form.stages = []
  paramRowMaps.value = new WeakMap()
}
</script>

<style scoped>
.stage-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.stage-row {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 12px;
}
.stage-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.stage-idx {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  background: var(--el-color-primary);
  color: #fff;
  font-size: 12px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.stage-params {
  padding-left: 30px;
}
.params-title {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 6px;
}
.param-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.add-stage {
  align-self: flex-start;
}
</style>
