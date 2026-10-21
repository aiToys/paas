<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { apiError, fetchList } from '@/api'
import { listServices, type ServiceEntity } from '@/api/service'
import { listLanes, type Lane } from '@/api/lane'
import { createPipeline, listPipelines, listRepositories, listImages, previewRun, triggerRun, type PipelineRun, type RunPreview, type Repo, type Image } from '@/api/pipeline'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import type { Env } from '@/stores/env'
import type { DeliveryAction } from '@/domain/delivery'
const props = defineProps<{ modelValue: boolean; appId?: string; appName?: string; hint?: string; serviceId?: string; envId?: string; imageId?: string; action?: DeliveryAction }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'deployed', run: PipelineRun): void }>()
const actions = [{ id: 'build', label: '构建' }, { id: 'deploy', label: '部署已有镜像' }, { id: 'build-deploy', label: '构建并部署' }] as const
const operation = ref<DeliveryAction>('build-deploy')
const services = ref<ServiceEntity[]>([]), repos = ref<Repo[]>([]), images = ref<Image[]>([]), envs = ref<Env[]>([]), lanes = ref<Lane[]>([])
const targetServiceId = ref(''), repoId = ref(''), targetEnvId = ref(''), lane = ref('default'), branch = ref(''), commit = ref(''), dockerfile = ref(''), buildContext = ref(''), args = ref('{}')
const loading = ref(false), busy = ref(false), error = ref(''), preview = ref<RunPreview>(), pipelineId = ref('')
const service = computed(() => services.value.find(s => s.id === targetServiceId.value))
function repoLabel(r: Repo) {
  let path = ''
  try { const url = new URL(r.gitUrl || ''); path = url.hostname + url.pathname } catch { /* ID fallback */ }
  return r.giteaRepo || path || r.name || r.id
}
const repo = computed(() => repos.value.find(r => r.id === repoId.value))
const image = computed(() => images.value.find(i => i.id === selectedImageId.value))
const selectedImageId = ref('')
const builds = computed(() => operation.value !== 'deploy'), deploys = computed(() => operation.value !== 'build')
const label = computed(() => actions.find(a => a.id === operation.value)?.label ?? '')
const targetEnv = computed(() => envs.value.find(e => e.id === targetEnvId.value))
const availableLanes = computed(() => lanes.value.filter(l => l.envId === targetEnvId.value && l.status === 'active' && l.name !== 'default'))
const valid = computed(() => !!service.value && service.value.type !== 'cron' && (!builds.value || (!!repo.value && !!branch.value.trim())) && (!deploys.value || (!!targetEnv.value && !!lane.value && (lane.value === 'default' || availableLanes.value.some(l => l.name === lane.value)))) && (operation.value !== 'deploy' || !!image.value?.digest))
let seq = 0
watch(targetServiceId, () => {
  const s = service.value
  repoId.value = s?.repoId || (repos.value.length === 1 ? repos.value[0]!.id : '')
  args.value = JSON.stringify(s?.buildArgs ?? {}, null, 2)
})
watch(repoId, () => {
  commit.value = ''; branch.value = repo.value?.branch || ''
  dockerfile.value = repo.value?.dockerfile || 'Dockerfile'
  buildContext.value = repo.value?.buildContext || '.'
})
watch(targetEnvId, () => { lane.value = 'default' })
watch([operation, targetServiceId, repoId, targetEnvId, lane, branch, commit, selectedImageId, dockerfile, buildContext, args], () => { preview.value = undefined })
async function load() {
  const cur = ++seq
  preview.value = undefined; error.value = ''; loading.value = true
  services.value = []; repos.value = []; images.value = []; lanes.value = []; envs.value = []
  targetServiceId.value = ''; repoId.value = ''; targetEnvId.value = ''; commit.value = ''; branch.value = ''
  operation.value = props.action ?? 'build-deploy'; selectedImageId.value = props.imageId ?? ''
  if (!props.appId) { loading.value = false; error.value = '缺少产品，请重新打开'; return }
  try {
    const [ss, rs, es, ls, ims] = await Promise.all([listServices(props.appId), listRepositories(props.appId), fetchList<Env>('/api/environments'), listLanes(), listImages(props.appId)])
    if (cur !== seq) return
    services.value = ss; repos.value = rs; envs.value = es; lanes.value = ls; images.value = ims.filter(i => i.status === 'ready' && i.digest)
    targetServiceId.value = props.serviceId || ss.find(s => s.name === props.hint)?.id || (ss.length === 1 ? ss[0]!.id : '')
    targetEnvId.value = props.envId || ''
  } catch (e) { if (cur === seq) error.value = apiError(e, '交付配置加载失败') }
  finally { if (cur === seq) loading.value = false }
}
watch(() => [props.modelValue, props.appId] as const, ([open]) => { if (open) void load(); else ++seq }, { immediate: true })
function requestBody() {
  const params: Record<string, unknown> = {}
  const put = (index: number, values: Record<string, unknown>) => { for (const [k, v] of Object.entries(values)) params[`${index}.${k}`] = v }
  if (builds.value) {
    const buildArgs: unknown = JSON.parse(args.value)
    if (!buildArgs || typeof buildArgs !== 'object' || Array.isArray(buildArgs) || Object.values(buildArgs).some(v => typeof v !== 'string')) throw new Error('构建参数需为 JSON 对象，值均为字符串')
    if (!/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(commit.value)) throw new Error('请填写完整提交 SHA，或留空自动解析分支最新提交')
    put(0, { serviceId: targetServiceId.value, service: service.value!.name, repoId: repoId.value, commit: commit.value, dockerfile: dockerfile.value.trim(), buildContext: buildContext.value.trim(), buildArgs })
  }
  if (deploys.value) put(operation.value === 'deploy' ? 0 : 1, { serviceId: targetServiceId.value, service: service.value!.name, envId: targetEnvId.value, lane: lane.value, port: service.value!.port ?? 0, ...(operation.value === 'deploy' ? { imageId: selectedImageId.value } : {}) })
  return { branch: builds.value ? branch.value.trim() : image.value?.branch || 'image', commit: commit.value, stageParams: params }
}
async function prepare() {
  if (!props.appId || !valid.value) return
  busy.value = true; error.value = ''; preview.value = undefined
  try {
    if (builds.value && !commit.value.trim()) {
      const cs = await fetchList<{ sha: string }>(`/api/applications/${props.appId}/repositories/${repoId.value}/commits?limit=1&branch=${encodeURIComponent(branch.value.trim())}`)
      if (!cs[0]?.sha) throw new Error('未找到该分支提交，请检查仓库与分支')
      commit.value = cs[0].sha
    }
    const body = requestBody()
    const templateId = `tpl-delivery-${operation.value}`
    const pipes = await listPipelines(props.appId)
    let pipe = pipes.find(p => p.templateId === templateId && !p.disabled && !Object.keys(p.paramOverrides ?? {}).length)
    if (!pipe) pipe = await createPipeline(props.appId, { name: `标准交付 · ${label.value}`, kind: operation.value === 'deploy' ? 'cd' : 'ci', templateId, trigger: { type: 'manual' } })
    pipelineId.value = pipe.id
    preview.value = await previewRun(props.appId, pipe.id, body)
  } catch (e) { error.value = apiError(e, '无法预览执行计划') }
  finally { busy.value = false }
}
async function submit() {
  if (!preview.value || !props.appId || !valid.value) return
  busy.value = true; error.value = ''
  try {
    if (deploys.value && targetEnv.value?.type === 'prod' && !(await confirmDangerous({ action: label.value, target: `${service.value!.name} / ${targetEnv.value.name} / ${lane.value}`, isProd: true, requireNameConfirm: true }))) return
    const run = await triggerRun(props.appId, pipelineId.value, { ...requestBody(), planHash: preview.value.planHash })
    emit('update:modelValue', false); emit('deployed', run)
  } catch (e) { error.value = apiError(e, '无法创建交付运行'); preview.value = undefined }
  finally { busy.value = false }
}
</script>
<template>
  <el-dialog :model-value="modelValue" title="发起交付" width="min(680px, 94vw)" append-to-body :close-on-click-modal="false" :close-on-press-escape="!busy" :show-close="!busy" @update:model-value="emit('update:modelValue', $event)">
    <div v-loading="loading">
      <p>产品：<strong>{{ appName || appId }}</strong> · 执行过程统一记录到流水线</p>
      <p class="note">浏览环境不会改变执行目标。请选择本次服务、环境与泳道。</p>
      <p v-if="error" role="alert" class="error">{{ error }} <button v-if="!services.length" class="text-button" @click="load">重新加载</button></p>
      <el-form :disabled="busy || loading" label-position="top" @submit.prevent="prepare">
        <el-form-item label="交付操作"><el-radio-group v-model="operation"><el-radio-button v-for="a in actions" :key="a.id" :value="a.id">{{ a.label }}</el-radio-button></el-radio-group></el-form-item>
        <el-form-item label="目标服务" required><el-select v-model="targetServiceId" placeholder="选择服务"><el-option v-for="s in services" :key="s.id" :label="s.name" :value="s.id" :disabled="s.type === 'cron'" /></el-select><small v-if="service?.type === 'cron'">定时任务请使用专用工作负载流程。</small></el-form-item>
        <template v-if="builds">
          <el-form-item label="代码仓库" required><el-select v-model="repoId" placeholder="选择仓库"><el-option v-for="r in repos" :key="r.id" :label="`${repoLabel(r)} · ${r.id}`" :value="r.id" /></el-select></el-form-item>
          <el-form-item label="代码分支" required><el-input v-model="branch" placeholder="填写实际分支" @input="commit = ''" /></el-form-item>
          <el-form-item label="提交 SHA"><el-input v-model="commit" placeholder="留空在预览时解析分支最新提交并锁定" /></el-form-item>
          <div class="pair"><el-form-item label="Dockerfile"><el-input v-model="dockerfile" /></el-form-item><el-form-item label="构建目录"><el-input v-model="buildContext" /></el-form-item></div>
          <el-form-item label="构建参数（JSON）"><el-input v-model="args" type="textarea" :rows="3" /></el-form-item>
        </template>
        <el-form-item v-if="operation === 'deploy'" label="已有镜像" required><el-select v-model="selectedImageId" filterable placeholder="选择就绪镜像"><el-option v-for="im in images" :key="im.id" :value="im.id" :label="`${im.tag} · ${im.digest.slice(0, 19)} · ${im.id}`" /></el-select></el-form-item>
        <template v-if="deploys">
          <el-form-item label="目标环境" required><el-select v-model="targetEnvId" placeholder="明确选择本次目标环境"><el-option v-for="e in envs" :key="e.id" :label="`${e.name}（${e.type === 'prod' ? '生产' : '测试'}）`" :value="e.id" /></el-select></el-form-item>
          <el-form-item label="目标泳道" required><el-select v-model="lane"><el-option label="default · 基线" value="default" /><el-option v-for="l in availableLanes" :key="l.id" :label="l.name" :value="l.name" /></el-select></el-form-item>
        </template>
      </el-form>
      <section v-if="preview" aria-label="执行确认" class="target-preview">
        <strong>确认本次执行目标</strong>
        <p>{{ service?.name }}<template v-if="deploys"> → {{ targetEnv?.name }} / {{ lane }} · 滚动部署并等待就绪</template></p>
        <p v-if="builds" class="mono">{{ repo ? repoLabel(repo) : '' }} / {{ branch }}<br />{{ commit }}</p>
        <p v-if="builds" class="mono">Dockerfile：{{ dockerfile }} · 构建目录：{{ buildContext }}</p>
        <p v-if="image" class="mono">{{ image.id }}<br />{{ image.digest }}</p>
        <ol><li v-for="s in preview.stages" :key="s.index">{{ s.name }}：{{ s.resolved }}</li></ol>
        <p class="note">完成表示{{ deploys ? '工作负载就绪' : '镜像构建成功' }}；业务验收需另行检查。</p>
        <p v-for="m in preview.missing" :key="m" class="note">{{ m }}</p>
      </section>
    </div>
    <template #footer><el-button :disabled="busy" @click="emit('update:modelValue', false)">取消</el-button><el-button v-if="!preview" type="primary" :disabled="!valid || loading" :loading="busy" @click="prepare">预览执行计划</el-button><el-button v-else type="primary" :disabled="!valid" :loading="busy" @click="submit">确认{{ label }}</el-button></template>
  </el-dialog>
</template>
<style scoped>
.note { color: var(--text-faint); font-size: 12px; line-height: 1.7; }
.error { color: var(--danger); }
.target-preview { padding: 16px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); background: var(--surface-2); margin-top: 16px; overflow-wrap: anywhere; }
.target-preview li { margin-top: 8px; }
.pair { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.mono { font-family: monospace; }
.el-select { width: 100%; }
@media(max-width: 560px) { .pair { grid-template-columns: 1fr; gap: 0; } }
</style>
