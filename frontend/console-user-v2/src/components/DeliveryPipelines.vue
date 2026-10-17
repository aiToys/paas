<script setup lang="ts">
import StatePanel from './StatePanel.vue'
// 交付·流水线管理（v2 原生）：绑定列表（kind 徽章 + 触发方式）+ 手动触发。
// 触发后跳 v2 RunDetail 跟进。
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { listPipelines, triggerRun, previewRun, deletePipeline, createPipeline, listTemplates, type PipelineTemplate, type Pipeline, type RunPreview } from '@/api/pipeline'
import { apiError } from '@/api'
import PipelineDesigner from './PipelineDesigner.vue'
import { confirmAction } from '@/composables/useConfirmAction'

const props = defineProps<{ appId: string }>()
const router = useRouter()

const templates = ref<PipelineTemplate[]>([])
const showCreate = ref(false), creating = ref(false), templateId = ref(''), pipelineName = ref('')
async function openCreate() { try { templates.value = await listTemplates(); showCreate.value = true } catch (e) { ElMessage.error(apiError(e)) } }
async function createBinding() {
 const template = templates.value.find(t => t.id === templateId.value)
 if (!template || !pipelineName.value.trim()) return
 creating.value = true
 try { const p = await createPipeline(props.appId, { name: pipelineName.value.trim(), kind: template.kind, templateId: template.id, trigger: { type: 'manual' } }); showCreate.value = false; await load(); designerPid.value = p.id }
 catch (e) { ElMessage.error(apiError(e)) } finally { creating.value = false }
}
const designerPid = ref('')
async function remove(p: Pipeline) {
  if (!await confirmAction(`删除流水线「${p.name}」？绑定配置将被删除，历史运行保留。`)) return
  try { await deletePipeline(props.appId, p.id); await load(); ElMessage.success('已删除') }
  catch (e) { ElMessage.error(apiError(e, '删除失败')) }
}
const pipelines = ref<Pipeline[]>([])
const loading = ref(true)
const error = ref('')
const triggering = ref('')
const showRun = ref(false)
const selected = ref<Pipeline | null>(null)
const branch = ref('main')
function prepare(p: Pipeline) { selected.value = p; branch.value = 'main'; preview.value = null; showRun.value = true }

async function load() {
  loading.value = true
  error.value = ''
  try {
    pipelines.value = await listPipelines(props.appId)
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.appId, load)

const KIND_LABEL: Record<string, string> = { ci: 'CI', cd: 'CD', agent: '智能' }

async function run() {
  const p = selected.value
  if (!p || !branch.value.trim()) return
  triggering.value = p.id
  try {
    // 带 preview 的 planHash 执行（spec §6）：预览到执行之间候选漂移 → 409，引导重新预览。
    const run = await triggerRun(props.appId, p.id, {
      branch: branch.value.trim(),
      planHash: preview.value?.planHash || undefined,
    })
    showRun.value = false
    ElMessage.success('已触发')
    router.push(`/runs/${run.id}`)
  } catch (e) {
    const msg = apiError(e, '触发失败')
    if (msg.includes('执行计划已变化')) {
      ElMessage.warning(`${msg}（正在重新预览…）`)
      preview.value = null
      await doPreview()
    } else {
      ElMessage.error(msg)
    }
  } finally {
    triggering.value = ''
  }
}

// ---------- 运行前执行计划预览（spec §6，评审 F4：看到实际发布哪个服务/环境/镜像） ----------
const preview = ref<RunPreview | null>(null)
const previewing = ref(false)
async function doPreview() {
  const p = selected.value
  if (!p || !branch.value.trim()) return
  previewing.value = true
  try {
    preview.value = await previewRun(props.appId, p.id, { branch: branch.value.trim() })
  } catch (e) {
    preview.value = null
    ElMessage.error(apiError(e, '预览失败'))
  } finally {
    previewing.value = false }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">流水线</span>
      <span class="card-sub">从平台模板创建绑定，再配置阶段参数与触发方式</span><button class="act primary" @click="openCreate">添加流水线</button>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <div v-else-if="!pipelines.length" class="empty-hint">暂无流水线，点击“添加流水线”选择平台模板</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="mini-table">
      <thead><tr><th>名称</th><th>类型</th><th>触发</th><th class="col-act"></th></tr></thead>
      <tbody>
        <tr v-for="p in pipelines" :key="p.id">
          <td class="t-name">{{ p.name }}</td>
          <td><span class="kind-chip">{{ KIND_LABEL[p.kind] ?? p.kind }}</span></td>
          <td class="dim">
            {{ p.trigger?.type === 'webhook' ? `webhook（${p.trigger.branch || '全部分支'}）` : '手动' }}
          </td>
          <td class="col-act">
            <button class="act" @click="designerPid = p.id">配置</button>
            <button class="act danger" @click="remove(p)">删除</button>
            <button class="act" :disabled="triggering === p.id || p.disabled" @click="prepare(p)">
              {{ triggering === p.id ? '触发中…' : '运行' }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
</div>
    <el-dialog v-model="showCreate" title="添加流水线" width="min(480px, 94vw)"><el-form label-position="top"><el-form-item label="名称"><el-input v-model="pipelineName" /></el-form-item><el-form-item label="平台模板"><el-select v-model="templateId" filterable><el-option v-for="t in templates" :key="t.id" :label="t.name+'（'+t.kind+'）'" :value="t.id" /></el-select></el-form-item></el-form><template #footer><el-button @click="showCreate=false">取消</el-button><el-button type="primary" :loading="creating" :disabled="!pipelineName.trim() || !templateId" @click="createBinding">创建并配置</el-button></template></el-dialog>
    <el-dialog :model-value="!!designerPid" title="流水线配置" width="min(1000px, 95vw)" destroy-on-close @close="designerPid = ''">
      <PipelineDesigner v-if="designerPid" :key="designerPid" :app-id="appId" :pid="designerPid" @saved="designerPid = ''; load()" />
    </el-dialog>
    <el-dialog v-model="showRun" title="运行流水线" width="min(560px, 94vw)" append-to-body>
      <p>流水线：<strong>{{ selected?.name }}</strong></p>
      <p class="dim">顶栏浏览环境不会覆盖流水线目标；执行前可预览实际解析结果。</p>
      <el-form label-position="top" @submit.prevent="run"><el-form-item label="代码分支" required><el-input v-model="branch" placeholder="例如 main" /></el-form-item></el-form>
      <div class="preview-block">
        <button class="act" :disabled="previewing || !branch.trim()" @click="doPreview">{{ previewing ? '预览中…' : (preview ? '刷新预览' : '预览执行计划') }}</button>
        <template v-if="preview">
          <div v-if="preview.artifact" class="pv-artifact">
            将发布镜像 <b class="mono">{{ preview.artifact.imageId }}</b>
            <span v-if="preview.artifact.digest" class="dim">（digest {{ preview.artifact.digest.slice(0, 19) }}… 触发时锁定，审批等待期间的新构建不改变此产物）</span>
          </div>
          <ol class="pv-stages">
            <li v-for="s in preview.stages" :key="s.index"><b>{{ s.name }}</b><span class="dim">（{{ s.type }}）—— {{ s.resolved }}</span></li>
          </ol>
          <div v-if="preview.missing?.length" class="pv-missing">
            <div v-for="m in preview.missing" :key="m">⚠ {{ m }}</div>
          </div>
        </template>
      </div>
      <template #footer>
        <el-button @click="showRun = false">取消</el-button>
        <el-button type="primary" :loading="!!triggering" :disabled="!branch.trim()" @click="run">
          {{ preview ? '按此计划执行' : '运行此流水线' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.kind-chip {
  display: inline-block;
  padding: 1px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 11px;
  color: var(--text-dim);
}
.dim { color: var(--text-faint); }
.col-act { text-align: right; }
.preview-block { margin-top: 8px; }
.pv-artifact { margin: 10px 0 4px; font-size: 12.5px; }
.pv-stages { margin: 8px 0 0; padding-left: 18px; font-size: 12.5px; line-height: 1.8; }
.pv-missing { margin-top: 8px; font-size: 12px; color: var(--text-faint); }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
