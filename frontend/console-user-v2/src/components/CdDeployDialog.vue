<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { listPipelines, listTemplates, triggerRun, type Pipeline, type PipelineTemplate, type PipelineRun } from '@/api/pipeline'
import { apiError } from '@/api'
import { useEnvStore } from '@/stores/env'
const props = defineProps<{ modelValue: boolean; appId?: string; appName?: string; hint?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'deployed', run: PipelineRun): void }>()
const envStore = useEnvStore()
const loading = ref(false)
const deploying = ref(false)
const error = ref('')
const pipelines = ref<Pipeline[]>([])
const templates = ref<PipelineTemplate[]>([])
const selected = ref('')
const branch = ref('main')
const pipe = computed(() => pipelines.value.find((p) => p.id === selected.value))
const stages = computed(() => {
  const template = templates.value.find((t) => t.id === pipe.value?.templateId)
  return (template?.stages ?? []).map((s, i) => {
    const env = pipe.value?.paramOverrides?.[i + '.envId'] ?? s.params?.envId
    const service = pipe.value?.paramOverrides?.[i + '.serviceId'] ?? s.params?.serviceId ?? pipe.value?.paramOverrides?.[i + '.service'] ?? s.params?.service
    return { name: s.name, type: s.type, env: typeof env === 'string' ? envStore.envs.find((e) => e.id === env)?.name ?? env : '', service: typeof service === 'string' ? service : '' }
  }).filter((s) => ['deploy', 'promote', 'canary', 'release'].includes(s.type))
})
let seq = 0
async function load() {
  const cur = ++seq
  selected.value = ''
  pipelines.value = []
  templates.value = []
  branch.value = 'main'
  error.value = ''
  if (!props.appId) return
  loading.value = true
  try {
    const [ps, ts] = await Promise.all([listPipelines(props.appId), listTemplates()])
    if (cur !== seq) return
    pipelines.value = ps.filter((p) => p.kind === 'cd' && !p.disabled)
    templates.value = ts
    if (pipelines.value.length === 1) selected.value = pipelines.value[0].id
  } catch (e) { if (cur === seq) error.value = apiError(e, '无法加载部署流水线') }
  finally { if (cur === seq) loading.value = false }
}
watch(() => props.modelValue, (v) => { if (v) void load() })
async function submit() {
  if (!props.appId || !pipe.value || !branch.value.trim()) return
  deploying.value = true
  try {
    const run = await triggerRun(props.appId, pipe.value.id, { branch: branch.value.trim() })
    emit('update:modelValue', false)
    emit('deployed', run)
  } catch (e) { ElMessage.error(apiError(e, '无法运行流水线')) }
  finally { deploying.value = false }
}
</script>
<template>
  <el-dialog :model-value="modelValue" title="运行部署流水线" width="560px" append-to-body @update:model-value="emit('update:modelValue', $event)">
    <div v-loading="loading">
      <p class="deploy-context">产品：<strong>{{ appName || appId }}</strong><span v-if="hint"> · 发起位置：{{ hint }}</span></p>
      <div class="deploy-notice">部署目标由所选流水线配置决定。顶栏环境与发起服务只表示浏览位置，不会自动覆盖部署目标。</div>
      <p v-if="error" role="alert">{{ error }} <button class="text-button" @click="load">重试</button></p>
      <el-form v-else-if="pipelines.length" label-position="top" @submit.prevent="submit">
        <el-form-item label="部署流水线" required><el-select v-model="selected" placeholder="选择要执行的流水线" style="width:100%"><el-option v-for="p in pipelines" :key="p.id" :label="p.name" :value="p.id" /></el-select></el-form-item>
        <div v-if="pipe" class="target-preview"><strong>配置中的部署阶段</strong><ul v-if="stages.length"><li v-for="(s, i) in stages" :key="i"><span>{{ s.name }}</span><small>{{ s.env ? '环境：' + s.env : '环境由阶段配置解析' }}{{ s.service ? ' · 服务：' + s.service : '' }}</small></li></ul><p v-else>此流水线未声明可展示的目标，请先核对流水线配置。</p><p>含占位符的目标会在运行时解析；发布与提升阶段可能涉及其他环境。</p></div>
        <el-form-item label="代码分支" required><el-input v-model="branch" placeholder="例如 main" /></el-form-item>
      </el-form>
      <p v-else-if="!loading">没有可运行的 CD 流水线。<RouterLink :to="'/products/' + appId + '?tab=delivery&section=pipelines'" class="text-button" @click="emit('update:modelValue', false)">查看流水线配置 →</RouterLink></p>
    </div>
    <template #footer><el-button @click="emit('update:modelValue', false)">取消</el-button><el-button type="primary" :disabled="!pipe || !branch.trim() || loading || !!error" :loading="deploying" @click="submit">运行所选流水线</el-button></template>
  </el-dialog>
</template>
<style scoped>
.deploy-context { color: var(--text-dim); font-size: 12px; overflow-wrap: anywhere; }
.deploy-notice { padding: 12px 14px; background: var(--warning-soft); color: var(--warning); font-size: 13px; border-radius: var(--radius-sm); margin: 16px 0; }
.target-preview { background: var(--surface-2); padding: 14px; border-radius: var(--radius-sm); margin-bottom: 18px; font-size: 13px; }
.target-preview ul { margin: 12px 0; padding-left: 18px; }
.target-preview small { display: block; color: var(--text-dim); overflow-wrap: anywhere; }
.target-preview p { font-size: 12px; color: var(--text-faint); }
</style>
