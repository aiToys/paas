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
// 触发配置（本地编辑，save 时写回 pipeline.trigger）
const triggerType = ref<string>('manual')
const triggerBranch = ref<string>('')

const stages = computed<StageDef[]>(() => template.value?.stages ?? [])
const templateName = computed(() => template.value?.name ?? pipeline.value?.templateId ?? '-')

// webhook URL（baseURL + /api/webhooks/pipeline/{pid}?token=<token>）；token 来自 get 响应（同租户可见）
const webhookUrl = computed(() => {
  if (triggerType.value !== 'webhook' || !pipeline.value) return ''
  const token = pipeline.value.trigger?.token ?? ''
  if (!token) return ''
  return `${window.location.origin}/api/webhooks/pipeline/${props.pid}?token=${token}`
})

async function load() {
  try {
    const [p, tpls] = await Promise.all([getPipeline(props.appId, props.pid), listTemplates()])
    pipeline.value = p
    template.value = tpls.find((t) => t.id === p.templateId) ?? null
    overrides.value = { ...(p.paramOverrides ?? {}) }
    triggerType.value = p.trigger?.type ?? 'manual'
    triggerBranch.value = p.trigger?.branch ?? ''
  } catch (e: any) {
    ElMessage.error(e?.message || '加载失败')
  }
}

// deploy stage 索引（可覆盖 envId 的 stage）
const deployStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'deploy'))
// build stage 索引（可覆盖 buildArgs，如多服务 Dockerfile ARG SERVICE）
const buildStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'build'))
// test stage 索引（smoke 探活 path / manual 人工确认 message）
const testStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'test'))
// approve stage 索引（审批提示 message）
const approveStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'approve'))
// release stage 索引（版本策略 versionStrategy：auto-increment/manual/tag）
const releaseStages = computed(() => stages.value.map((s, i) => ({ s, i })).filter((x) => x.s.type === 'release'))

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

// buildArgs 覆盖（map[string]string，如 {SERVICE: product}）。key 格式 "<stageIdx>.buildArgs"。
// 多服务构建必填（Dockerfile ARG SERVICE）；前端用动态 key-value 行编辑，提交时聚合为 map。
function getBuildArgs(stageIdx: number): Array<{ k: string; v: string }> {
  const m = (overrides.value[overrideKey(stageIdx, 'buildArgs')] as Record<string, string> | undefined) ?? {}
  return Object.entries(m).map(([k, v]) => ({ k, v }))
}
function setBuildArg(stageIdx: number, idx: number, field: 'k' | 'v', val: string) {
  const rows = getBuildArgs(stageIdx)
  if (idx >= rows.length) rows.push({ k: '', v: '' })
  rows[idx][field] = val
  commitBuildArgs(stageIdx, rows)
}
function addBuildArg(stageIdx: number) {
  const rows = getBuildArgs(stageIdx)
  rows.push({ k: '', v: '' })
  commitBuildArgs(stageIdx, rows)
}
function removeBuildArg(stageIdx: number, idx: number) {
  const rows = getBuildArgs(stageIdx)
  rows.splice(idx, 1)
  commitBuildArgs(stageIdx, rows)
}
function commitBuildArgs(stageIdx: number, rows: Array<{ k: string; v: string }>) {
  const m: Record<string, string> = {}
  for (const r of rows) {
    if (r.k) m[r.k] = r.v
  }
  const k = overrideKey(stageIdx, 'buildArgs')
  if (Object.keys(m).length) overrides.value[k] = m
  else delete overrides.value[k]
}

async function save() {
  if (!pipeline.value) return
  saving.value = true
  try {
    // trigger：webhook 带 branch glob；manual 无需 token（后端清）。token 不传，后端保留原/生成新。
    const trigger = triggerType.value === 'webhook'
      ? { type: 'webhook', branch: triggerBranch.value.trim() }
      : { type: 'manual' }
    const updated = await updatePipeline(props.appId, props.pid, {
      ...pipeline.value,
      paramOverrides: { ...overrides.value },
      trigger,
    })
    pipeline.value = updated // 更新含 token（webhook 时展示 URL）
    ElMessage.success('已保存')
    emit('saved')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function copyWebhookUrl() {
  if (!webhookUrl.value) return
  try {
    await navigator.clipboard.writeText(webhookUrl.value)
    ElMessage.success('已复制 webhook URL（含 token，请妥善保管）')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
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

    <!-- 触发配置 -->
    <div class="section">
      <div class="section-title">触发方式</div>
      <el-radio-group v-model="triggerType">
        <el-radio value="manual">手动触发</el-radio>
        <el-radio value="webhook">Webhook 自动触发（Git push）</el-radio>
      </el-radio-group>
      <template v-if="triggerType === 'webhook'">
        <div class="override-label" style="margin-top: 12px;">分支匹配（glob，留空=全部分支）</div>
        <el-input v-model="triggerBranch" placeholder="如 main 或 feature-*（留空匹配全部）" clearable style="max-width: 360px;" />
        <div class="override-hint">push 到匹配分支时自动触发；不匹配的分支静默忽略。</div>
        <div v-if="webhookUrl" class="webhook-url-box">
          <div class="override-label">Webhook URL（在 Gitea/GitLab 仓库 Webhooks 配置粘贴，Content-Type: application/json）</div>
          <el-input :model-value="webhookUrl" readonly type="textarea" :rows="2" />
          <el-button size="small" type="primary" plain @click="copyWebhookUrl" style="margin-top: 6px;">复制 URL（含 token）</el-button>
          <div class="override-hint">token 用于鉴权，请妥善保管。保存后此处展示完整 URL（同租户可见）。</div>
        </div>
        <el-alert v-else type="warning" :closable="false" style="margin-top: 8px;">
          保存后生成 webhook URL（首次切到 webhook 会自动生成 token）。
        </el-alert>
      </template>
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
        <div class="override-label" style="margin-top: 12px;">阶段「{{ d.s.name }}」泳道（lane）</div>
        <el-input :model-value="getOverride(d.i, 'lane')" @update:model-value="(v: string) => setOverride(d.i, 'lane', v)"
                  placeholder="默认 {{run.branch}}（分支独立泳道联调）；生产填 default" clearable />
        <div class="override-hint">留空 = 用运行期分支名作 lane（测试泳道联调）；生产基线填 default。</div>
        <div class="override-label" style="margin-top: 12px;">阶段「{{ d.s.name }}」服务（service）</div>
        <el-input :model-value="getOverride(d.i, 'service')" @update:model-value="(v: string) => setOverride(d.i, 'service', v)"
                  placeholder="留空 = 单服务；多服务应用填服务名（如 product/recommend）" clearable />
        <div class="override-hint">同 app 多服务场景区分各服务 Workload（与构建 buildArgs SERVICE 对齐）；留空 = 单服务。</div>
        <div class="override-label" style="margin-top: 12px;">阶段「{{ d.s.name }}」端口（port）</div>
        <el-input :model-value="getOverride(d.i, 'port')" @update:model-value="(v: string) => setOverride(d.i, 'port', v)"
                  placeholder="留空 = 不建 Service；新建 Workload 时驱动建 K8s Service（如 8081）" clearable />
        <div class="override-hint">ContainerPort 缺省取 port；端口驱动 reconciler 建 Service 供 smoke 探活/服务发现。</div>
      </div>
      <div v-for="b in buildStages" :key="b.i" class="override-item">
        <div class="override-label">阶段「{{ b.s.name }}」构建参数（buildArgs，如 SERVICE=product）</div>
        <div v-for="(row, idx) in getBuildArgs(b.i)" :key="idx" class="buildarg-row">
          <el-input :model-value="row.k" @update:model-value="(v: string) => setBuildArg(b.i, idx, 'k', v)"
                    placeholder="参数名（如 SERVICE）" style="width: 40%;" />
          <el-input :model-value="row.v" @update:model-value="(v: string) => setBuildArg(b.i, idx, 'v', v)"
                    placeholder="参数值（如 product）" style="width: 40%;" />
          <el-button text type="danger" @click="removeBuildArg(b.i, idx)">删</el-button>
        </div>
        <el-button text type="primary" @click="addBuildArg(b.i)">+ 添加参数</el-button>
        <div class="override-hint">多服务构建必填（Dockerfile ARG，透传 --build-arg K=V）；留空 = 无构建参数。</div>
      </div>
      <div v-for="t in testStages" :key="t.i" class="override-item">
        <div class="override-label">阶段「{{ t.s.name }}」测试模式</div>
        <el-select :model-value="getOverride(t.i, 'mode')" @update:model-value="(v: string) => setOverride(t.i, 'mode', v)"
                   placeholder="默认 smoke（HTTP 探活自动）" clearable style="width: 100%;">
          <el-option value="smoke" label="smoke（HTTP 探活自动）" />
          <el-option value="manual" label="manual（人工确认暂停）" />
        </el-select>
        <div class="override-label" style="margin-top: 12px;" v-if="getOverride(t.i, 'mode') !== 'manual'">探活路径</div>
        <el-input v-if="getOverride(t.i, 'mode') !== 'manual'"
                  :model-value="getOverride(t.i, 'path')" @update:model-value="(v: string) => setOverride(t.i, 'path', v)"
                  placeholder="默认 /livez（如应用用 /healthz 在此覆盖）" clearable />
        <div class="override-hint">smoke 模式探活前序 deploy 的 domain + path 轮询 2xx；路径需与应用真实健康端点一致。</div>
      </div>
      <div v-for="a in approveStages" :key="a.i" class="override-item">
        <div class="override-label">阶段「{{ a.s.name }}」审批提示</div>
        <el-input :model-value="getOverride(a.i, 'message')" @update:model-value="(v: string) => setOverride(a.i, 'message', v)"
                  placeholder="默认「等待审批」（如：请确认生产发布）" clearable />
        <div class="override-hint">审批门禁暂停时展示给确认者的提示文案。</div>
      </div>
      <div v-for="r in releaseStages" :key="r.i" class="override-item">
        <div class="override-label">阶段「{{ r.s.name }}」版本策略</div>
        <el-select :model-value="getOverride(r.i, 'versionStrategy')" @update:model-value="(v: string) => setOverride(r.i, 'versionStrategy', v)"
                   placeholder="默认 auto-increment（分支-runID）" clearable style="width: 100%;">
          <el-option value="auto-increment" label="auto-increment（分支-runID 自动）" />
          <el-option value="manual" label="manual（触发时填的版本）" />
          <el-option value="tag" label="tag（commit 短 sha）" />
        </el-select>
        <div class="override-hint">打版本号里程碑的策略（git tag + Image.Version）。</div>
      </div>
      <el-empty v-if="!deployStages.length && !buildStages.length && !testStages.length && !approveStages.length && !releaseStages.length" description="无可覆盖阶段" :image-size="60" />
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
.buildarg-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.override-label { font-size: 13px; margin-bottom: 6px; color: var(--el-text-color-regular); }
.override-hint { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 4px; }
.webhook-url-box { margin-top: 12px; padding: 12px; background: var(--el-fill-color-light); border-radius: 6px; }
.footer { margin-top: 24px; text-align: right; }
</style>
