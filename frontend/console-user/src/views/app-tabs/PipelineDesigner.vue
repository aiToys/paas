<script setup lang="ts">
// 流水线绑定编辑器（参数覆盖）：binding 模型下不改 stage 序列（从模板），只覆盖参数。
// 显示模板 stages 只读 + paramOverrides 表单（关键字段如 deploy.envId 可覆盖）。
// 默认占位符 {{app.env.test}}/{{app.env.prod}} 触发时自动解析，无需覆盖；仅定制时填。
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getPipeline, updatePipeline, listTemplates,
  type Pipeline, type StageDef, type PipelineTemplate,
} from '@/api/pipeline'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string; pid: string }>()
const emit = defineEmits<{ (e: 'saved'): void }>()

const envStore = useEnvStore()
const pipeline = ref<Pipeline | null>(null)
const template = ref<PipelineTemplate | null>(null)
const overrides = ref<Record<string, unknown>>({})
const saving = ref(false)

const stages = computed<StageDef[]>(() => template.value?.stages ?? [])
const templateName = computed(() => template.value?.name ?? pipeline.value?.templateId ?? '-')

async function load() {
  try {
    const [p, tpls] = await Promise.all([getPipeline(props.appId, props.pid), listTemplates()])
    pipeline.value = p
    template.value = tpls.find((t) => t.id === p.templateId) ?? null
    overrides.value = { ...(p.paramOverrides ?? {}) }
  } catch (e: any) {
    ElMessage.error(e?.message || '加载失败')
  }
}

// deploy stage 索引（可覆盖 envId 的 stage）
const deployStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'deploy'))

// 覆盖 key: "<stageIdx>.<param>"
function overrideKey(stageIdx: number, param: string): string {
  return `${stageIdx}.${param}`
}
function getOverride(stageIdx: number, param: string): string {
  return (overrides.value[overrideKey(stageIdx, param)] as string) ?? ''
}
function setOverride(stageIdx: number, param: string, val: string) {
  const k = overrideKey(stageIdx, param)
  if (val) overrides.value[k] = val
  else delete overrides.value[k]
}

async function save() {
  if (!pipeline.value) return
  saving.value = true
  try {
    await updatePipeline(props.appId, props.pid, { ...pipeline.value, paramOverrides: { ...overrides.value } })
    ElMessage.success('已保存参数覆盖')
    emit('saved')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="binding-editor" v-if="pipeline">
    <el-alert type="info" :closable="false" style="margin-bottom: 16px;">
      绑定模板：<b>{{ templateName }}</b>（{{ pipeline.kind }}）。默认参数从模板解析（占位符自动取应用租户环境），仅定制时覆盖。
    </el-alert>

    <!-- 模板 stages 只读预览 -->
    <div class="section">
      <div class="section-title">阶段序列（模板定义，只读）</div>
      <div class="stages-preview">
        <span v-for="(s, i) in stages" :key="i" class="stage-chip">
          <span class="stage-idx">{{ i + 1 }}</span>{{ s.name }}
          <el-tag size="small" type="info" effect="plain">{{ s.type }}</el-tag>
        </span>
      </div>
    </div>

    <!-- 参数覆盖表单 -->
    <div class="section">
      <div class="section-title">参数覆盖（可选）</div>
      <div v-for="d in deployStages" :key="d.i" class="override-item">
        <div class="override-label">阶段「{{ d.s.name }}」目标环境</div>
        <el-select :model-value="getOverride(d.i, 'envId')" @update:model-value="(v: string) => setOverride(d.i, 'envId', v)"
                   placeholder="默认（{{app.env.test}} 自动解析）" clearable style="width: 100%;">
          <el-option v-for="e in envStore.envs" :key="e.id" :value="e.id" :label="`${e.name} (${e.type})`" />
        </el-select>
        <div class="override-hint">留空 = 用模板默认占位符自动解析；选具体环境 = 覆盖。</div>
      </div>
      <el-empty v-if="!deployStages.length" description="无 deploy 阶段可覆盖" :image-size="60" />
    </div>

    <div class="footer">
      <el-button type="primary" :loading="saving" @click="save">保存覆盖</el-button>
    </div>
  </div>
</template>

<style scoped>
.binding-editor { padding: 16px 20px; }
.section { margin-bottom: 24px; }
.section-title { font-weight: 600; margin-bottom: 12px; color: var(--el-text-color-primary); }
.stages-preview { display: flex; flex-wrap: wrap; gap: 8px; }
.stage-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; background: var(--el-fill-color-light); border-radius: 14px;
  font-size: 13px;
}
.stage-idx {
  width: 18px; height: 18px; border-radius: 50%; background: var(--el-color-primary);
  color: #fff; font-size: 11px; display: inline-flex; align-items: center; justify-content: center;
}
.override-item { margin-bottom: 16px; }
.override-label { font-size: 13px; margin-bottom: 6px; color: var(--el-text-color-regular); }
.override-hint { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 4px; }
.footer { margin-top: 24px; text-align: right; }
</style>
