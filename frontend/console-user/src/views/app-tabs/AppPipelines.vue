<script setup lang="ts">
// 应用详情 - 流水线 tab：按 Kind 分组卡片，新建（从模板/空白/微服务快捷）、删除、编辑、运行。
// 设计器抽屉（PipelineDesigner）+ 运行视图抽屉（PipelineRunView）。
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'
import {
  type Pipeline, type PipelineTemplate, type PipelineRun, type StageDef,
  listPipelines, createPipeline, deletePipeline, listTemplates, triggerRun,
} from '@/api/pipeline'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import PipelineDesigner from './PipelineDesigner.vue'

const props = defineProps<{ appId: string }>()
const router = useRouter()
const envStore = useEnvStore()

const pipelines = ref<Pipeline[]>([])
const templates = ref<PipelineTemplate[]>([])
const loading = ref(false)

// 新建弹窗
const createDlg = ref(false)
const creating = ref(false)
const createForm = ref<{ name: string; kind: 'ci' | 'cd'; templateId: string }>({ name: '', kind: 'ci', templateId: 'tpl-ci' })

// 设计器抽屉（运行详情走独立页 /devops/runs/:id，不再用嵌入式抽屉）
const designerPid = ref<string | null>(null)

// CD 运行对话框（收集 version + branch；CI 直接默认 branch 触发）
const cdRunDlg = ref(false)
const cdRunForm = ref<{ pipeline: Pipeline | null; branch: string; version: string }>({
  pipeline: null, branch: 'main', version: '',
})

const ciPipelines = computed(() => pipelines.value.filter((p) => p.kind === 'ci'))
const cdPipelines = computed(() => pipelines.value.filter((p) => p.kind === 'cd'))

async function load() {
  loading.value = true
  try {
    const [ps, ts] = await Promise.all([listPipelines(props.appId), listTemplates()])
    pipelines.value = ps
    templates.value = ts
  } catch (e: any) {
    ElMessage.error(e.message || '加载流水线失败')
  } finally {
    loading.value = false
  }
}
onMounted(load)

function openCreate() {
  createForm.value = { name: '', kind: 'ci', templateId: 'tpl-ci' }
  createDlg.value = true
}

async function doCreate() {
  if (!createForm.value.name.trim()) { ElMessage.warning('请输入流水线名称'); return }
  creating.value = true
  try {
    const created = await createPipeline(props.appId, {
      name: createForm.value.name.trim(),
      kind: createForm.value.kind,
      templateId: createForm.value.templateId || undefined,
    })
    ElMessage.success('已创建（从模板初始化，可编辑修改）')
    createDlg.value = false
    pipelines.value.push(created)
    designerPid.value = created.id // 创建后直接进设计器
  } catch (e: any) {
    ElMessage.error(e.message || '创建失败')
  } finally {
    creating.value = false
  }
}

async function remove(p: Pipeline) {
  try {
    await ElMessageBox.confirm(`删除流水线「${p.name}」？此操作不可逆。`, '删除确认', { type: 'warning' })
    await deletePipeline(props.appId, p.id)
    pipelines.value = pipelines.value.filter((x) => x.id !== p.id)
    ElMessage.success('已删除')
  } catch (e: any) {
    if (e !== 'cancel' && e?.message) ElMessage.error(e.message)
  }
}

// 触发运行：CI 直接默认 branch；CD 弹对话框收集 version（baseline 写入用）+ branch。
// 含 prod deploy 的流水线二次确认（生产误触发风险）。
async function run(p: Pipeline) {
  if (p.kind === 'cd') {
    cdRunForm.value = { pipeline: p, branch: p.trigger.branch || 'main', version: '' }
    cdRunDlg.value = true
    return
  }
  await doTriggerRun(p, p.trigger.branch || 'main')
}

async function confirmCdRun() {
  const p = cdRunForm.value.pipeline
  if (!p) return
  if (!cdRunForm.value.branch.trim()) {
    ElMessage.error('请填写分支')
    return
  }
  cdRunDlg.value = false
  await doTriggerRun(p, cdRunForm.value.branch.trim(), cdRunForm.value.version.trim() || undefined)
}

async function doTriggerRun(p: Pipeline, branch: string, version?: string) {
  // 含 prod deploy -> 生产二次确认（防误触发生产发布）
  if (hasProdDeploy(p)) {
    const ok = await confirmDangerous({
      action: '运行流水线', target: p.name, isProd: true,
    })
    if (!ok) return
  }
  try {
    const r = await triggerRun(props.appId, p.id, { branch, version })
    ElMessage.success('已触发运行')
    // 触发即跳运行详情页（GitHub Actions 式全屏看实时进度，闭环）
    router.push(`/devops/runs/${r.id}`)
  } catch (e: any) {
    ElMessage.error(e.message || '触发失败') // 含 403 prod:write / 409 单实例串行 / 400 envId 缺失
  }
}

// 流水线是否含 prod deploy（卡片标红警示）。binding 无 stages，从模板解析。
function hasProdDeploy(p: Pipeline): boolean {
  return templateStages(p.templateId).some((s) => s.type === 'deploy' &&
    envStore.envs.find((e) => e.id === s.params?.envId)?.type === 'prod')
}

// 从模板缓存取 stages（binding 无 stages 字段，运行时从模板解析；列表 stage chips 用）
function templateStages(tid?: string): StageDef[] {
  if (!tid) return []
  return templates.value.find((t) => t.id === tid)?.stages ?? []
}

// 最近一次运行状态（卡片角标）
const latestRuns = ref<Record<string, PipelineRun>>({})
async function loadLatest() {
  try {
    const runs = await fetchAuth('/api/pipelineruns?appId=' + props.appId).then((r) => r.json()).then((j) => j.data ?? [])
    const map: Record<string, PipelineRun> = {}
    for (const r of runs as PipelineRun[]) {
      if (!map[r.pipelineId]) map[r.pipelineId] = r // 列表已按时间倒序
    }
    latestRuns.value = map
  } catch { /* 非关键 */ }
}
onMounted(loadLatest)

const statusTag = (s?: string) => {
  if (!s) return null
  const map: Record<string, string> = { succeeded: 'success', failed: 'danger', aborted: 'info', running: 'warning', paused: 'warning' }
  return { type: map[s] || 'info', label: s }
}
</script>

<template>
  <div class="app-pipelines" v-loading="loading">
    <div class="cross-link"><a @click="$router.push('/devops')">查看跨应用流水线总览 →</a></div>

    <div class="actions">
      <el-button type="primary" @click="openCreate">＋ 添加流水线</el-button>
      <el-button text @click="load">刷新</el-button>
    </div>

    <template v-for="kind in ['ci', 'cd']" :key="kind">
      <div class="group" v-if="(kind === 'ci' ? ciPipelines : cdPipelines).length">
        <div class="group-title">{{ kind === 'ci' ? '开发流水线 (CI)' : '发布流水线 (CD)' }}</div>
        <div class="cards">
          <div v-for="p in (kind === 'ci' ? ciPipelines : cdPipelines)" :key="p.id" class="pipe-card"
               :class="{ 'prod-warn': hasProdDeploy(p) }">
            <div class="pipe-head">
              <span class="pipe-name">{{ p.name }}</span>
              <el-tag v-if="statusTag(latestRuns[p.id]?.status)" size="small"
                      :type="statusTag(latestRuns[p.id]?.status)!.type"
                      class="run-tag" @click="latestRuns[p.id] && router.push(`/devops/runs/${latestRuns[p.id].id}`)">
                {{ statusTag(latestRuns[p.id]?.status)!.label }}
              </el-tag>
            </div>
            <div class="pipe-stages">
              <span v-for="(s, i) in templateStages(p.templateId)" :key="i" class="stage-chip">{{ s.name }}</span>
            </div>
            <div class="pipe-actions">
              <el-button size="small" @click="designerPid = p.id">编辑</el-button>
              <el-button size="small" type="primary" @click="run(p)">运行</el-button>
              <el-button v-if="latestRuns[p.id]" size="small" text type="primary"
                         @click="router.push(`/devops/runs/${latestRuns[p.id].id}`)">查看运行</el-button>
              <el-button size="small" text type="danger" @click="remove(p)">删除</el-button>
            </div>
            <div v-if="hasProdDeploy(p)" class="prod-badge">⚠️ 含生产环境</div>
          </div>
        </div>
      </div>
    </template>

    <el-empty v-if="!pipelines.length && !loading" description="暂无流水线">
      <el-button type="primary" @click="openCreate">新建第一条流水线</el-button>
    </el-empty>

    <!-- 新建弹窗 -->
    <el-dialog v-model="createDlg" title="新建流水线" width="460px">
      <el-form label-width="80px">
        <el-form-item label="名称"><el-input v-model="createForm.name" placeholder="如 product-ci" /></el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="createForm.kind" @change="createForm.templateId = createForm.kind === 'ci' ? 'tpl-ci' : 'tpl-cd'">
            <el-radio value="ci">CI 开发流水线</el-radio>
            <el-radio value="cd">CD 发布流水线</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="模板">
          <el-select v-model="createForm.templateId">
            <el-option v-for="t in templates.filter(t => t.kind === createForm.kind)" :key="t.id" :value="t.id" :label="t.name" />
            <el-option value="">空白（自定义）</el-option>
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="doCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- CD 运行对话框：收集 version（baseline 写入）+ branch -->
    <el-dialog v-model="cdRunDlg" :title="`运行发布流水线：${cdRunForm.pipeline?.name || ''}`" width="460px">
      <el-alert v-if="cdRunForm.pipeline && hasProdDeploy(cdRunForm.pipeline)" type="warning" :closable="false"
                title="⚠️ 该流水线含生产环境部署，确认后需再次二次确认。" style="margin-bottom: 12px;" />
      <el-form label-width="80px">
        <el-form-item label="分支">
          <el-input v-model="cdRunForm.branch" placeholder="如 main" />
        </el-form-item>
        <el-form-item label="版本">
          <el-input v-model="cdRunForm.version" placeholder="如 v1.2.0（留空则不写基线版本）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="cdRunDlg = false">取消</el-button>
        <el-button type="primary" @click="confirmCdRun">运行</el-button>
      </template>
    </el-dialog>

    <!-- 设计器抽屉 -->
    <el-drawer v-model="designerPid" size="60%" title="流水线设计器" @close="designerPid = null">
      <PipelineDesigner v-if="designerPid" :app-id="appId" :pid="designerPid" @saved="load" />
    </el-drawer>
  </div>
</template>

<style scoped>
.cross-link { margin-bottom: 12px; }
.cross-link a { color: var(--el-color-primary); cursor: pointer; font-size: 13px; }
.actions { margin-bottom: 16px; display: flex; gap: 8px; }
.group { margin-bottom: 24px; }
.group-title { font-weight: 600; color: var(--el-text-color-primary); margin-bottom: 12px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px; }
.pipe-card { border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 14px; background: var(--el-bg-color); position: relative; }
.pipe-card.prod-warn { border-color: var(--el-color-danger); }
.pipe-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.pipe-name { font-weight: 600; }
.run-tag { cursor: pointer; }
.pipe-stages { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.stage-chip { background: var(--el-fill-color-light); padding: 2px 8px; border-radius: 10px; font-size: 12px; }
.pipe-actions { display: flex; gap: 8px; }
.prod-badge { color: var(--el-color-danger); font-size: 12px; margin-top: 8px; }
</style>
