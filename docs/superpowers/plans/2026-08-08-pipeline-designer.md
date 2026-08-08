# 前端流水线设计器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** console-user 应用详情新增「流水线」tab，开发者能可视化创建/编辑流水线（从模板）、手动触发运行、查看运行进度与 stage 输出、审批/取消运行；DevOps 中心加跨应用「最近运行」观察区。

**Architecture:** 纯前端（零后端改动，对接 Plan 1 已交付的 8 个流水线端点）。三个新组件（列表 tab / 设计器 / 运行视图）+ 两处既有文件接入（ApplicationDetail tab 注册、DevOps 最近运行 section）。复用既有 `fetchJSON<T>`（智能解包 `{data:T}`）、`useDangerConfirm`（生产 deploy 确认）、`envStore`（环境列表）。按 `Kind`（ci/cd）分组展示，CD 卡片带版本选择器与「发布」按钮。

**Tech Stack:** Vue 3 `<script setup>` + TypeScript + Element Plus + Pinia（既有栈，无新依赖）。

## Global Constraints

- **契约解包**：`fetchJSON<T>` 自动解包 `{data:T}`——列表端点返数组、item 端点返对象。写操作（POST/PUT/DELETE）用 `fetchAuth` + 手动 `resp.json().data`（与 AppBuilds/AppReleases 同款模式，因需处理 4xx 错误体）。
- **权限模型**：developer 持 `pipeline:read/write` 但无 `prod:write`。deploy stage 到 prod 环境、或 promote 链路目标 prod，后端返 403 `forbidden: missing prod:write`（前端 `fetchAuth` 非 2xx 抛错，ElMessage.error 提示）。
- **生产视觉强隔离**：流水线含 deploy 到 prod 环境 stage 时，设计器标红警示；触发含 prod 的 run 走 `confirmDangerous({isProd:true})`。`isProd` 显式按目标 env.type 传（覆盖顶栏 scope）。
- **无前端单测**：console-user 无 vitest/test-utils。每个任务验证 = `pnpm exec vue-tsc --noEmit`（类型）+ `pnpm build`（构建）+ 手动 e2e 步骤（启动 core 或集群 curl 验证端点交互）。
- **响应式引用坑**（Plan 1 审查发现）：流式/异步更新对象属性需取 reactive proxy 再改（`arr.value[i].x = y` 而非改原始对象引用）。run 轮询更新 stageRuns 时直接替换整个 run 对象（`run.value = await fetchJSON(...)`），不就地改属性。
- **注释语言**：与代码库一致用简体中文。
- **不开 git 操作**：任务末尾 commit 由 SDD 流程统一处理（`git add` + `git commit -m`），不 push/分支。
- **样式**：复用既有 tab 组件风格（卡片 + el-table + el-dialog/el-drawer），不引新 UI 库。

### 后端端点契约（Plan 1 已交付，前端对接）

| 方法 | 路径 | 请求体 | 响应 `{data:T}` |
|------|------|--------|------------------|
| GET | `/api/pipeline-templates` | — | `PipelineTemplate[]` |
| GET | `/api/applications/{id}/pipelines` | — | `Pipeline[]` |
| POST | `/api/applications/{id}/pipelines` | `{name,kind,templateId?,stages?,trigger}` | `Pipeline`（201） |
| GET | `/api/applications/{id}/pipelines/{pid}` | — | `Pipeline` |
| PUT | `/api/applications/{id}/pipelines/{pid}` | `Pipeline` | `Pipeline` |
| DELETE | `/api/applications/{id}/pipelines/{pid}` | — | `{deleted:pid}` |
| POST | `/api/applications/{id}/pipelines/{pid}/run` | `{branch,commit?,version?}` | `PipelineRun`（201） |
| GET | `/api/pipelineruns?appId=&pipelineId=&status=` | — | `PipelineRun[]` |
| GET | `/api/pipelineruns/{id}` | — | `PipelineRun` |
| POST | `/api/pipelineruns/{id}/stages/{idx}/approve` | — | `{resumed:id}` |
| POST | `/api/pipelineruns/{id}/abort` | — | `{aborted:id}` |

### Stage 类型与 Params schema（与后端 `pipeline/model.go` 对齐）

- `build`：`{buildArgs?: map<string,string>, branchOverride?: string}`
- `deploy`：`{envId: string, imageSource: "priorBuild"|"selected"|"latestReady", imageId?: string(selected 时必填), strategy?: "rolling"}`（envId 必填，后端 fail-fast 400）
- `test`：`{mode: "smoke"|"manual", path?: string(smoke 默认 /livez), message?: string(manual)}`
- `approve`：`{message?: string}`
- `promote`：`{}`（提升前序 deploy 的 release，无参数）
- `baseline`：`{mainBranch?: string, versionStrategy?: "auto-increment", mergeMode?: "squash"|"ff"|"rebase"}`

### 状态枚举

- `PipelineRun.status`：`running | paused | succeeded | failed | aborted`
- `StageRun.status`：`pending | running | success | failed | waiting | skipped`
- `StageRun.output`：`{imageId?}`(build) / `{releaseId?, workloadDomain?}`(deploy) / `{result?, url?}`(test)
- `StageRun.error`：失败原因（字符串）

---

## File Structure

| 文件 | 职责 | 动作 |
|------|------|------|
| `frontend/console-user/src/api/pipeline.ts` | TS 类型（Pipeline/StageDef/PipelineRun/StageRun/PipelineTemplate）+ CRUD API 函数 | 新建 |
| `frontend/console-user/src/views/app-tabs/AppPipelines.vue` | 流水线列表 tab（按 Kind 分组卡片 + 新建弹窗 + 删除 + 触发 run 入口） | 新建 |
| `frontend/console-user/src/views/app-tabs/PipelineDesigner.vue` | 设计器抽屉（stage 有序列表 + 增删改 + 每 stage 参数面板 + 保存） | 新建 |
| `frontend/console-user/src/views/app-tabs/PipelineRunView.vue` | 运行视图抽屉（stage 进度条 + 5s 轮询 + approve/abort + Output/Error 展示） | 新建 |
| `frontend/console-user/src/views/ApplicationDetail.vue` | DevOps 组首位注册「流水线」tab，挂载 AppPipelines | 修改 |
| `frontend/console-user/src/views/DevOps.vue` | 加「最近运行」section（跨应用 PipelineRun 列表 + 跳应用详情） | 修改 |

---

## Task 1: TS 类型 + API 层（`api/pipeline.ts`）

**Files:**
- Create: `frontend/console-user/src/api/pipeline.ts`

**Interfaces:**
- Produces: 类型 `StageType`/`ImageSource`/`TestMode`/`StageDef`/`Pipeline`/`PipelineTemplate`/`PipelineRun`/`StageRun`/`RunStatus`/`StageStatus`；API 函数 `listPipelines`/`getPipeline`/`createPipeline`/`updatePipeline`/`deletePipeline`/`listTemplates`/`triggerRun`/`getRun`/`listRuns`/`approveStage`/`abortRun`。后续所有组件 import 这些。

- [ ] **Step 1: 写 `api/pipeline.ts`（类型 + API）**

```typescript
// pipeline.ts 流水线类型 + API（对接 Plan 1 已交付的 8 端点）。
// fetchJSON 自动解包 {data:T}；写操作用 fetchAuth + 手动取 .data（需处理 4xx 错误体）。
import { fetchJSON, fetchAuth } from '@/api'

// ---------- 枚举常量 ----------
export type StageType = 'build' | 'deploy' | 'test' | 'approve' | 'promote' | 'baseline'
export type ImageSource = 'priorBuild' | 'selected' | 'latestReady'
export type TestMode = 'smoke' | 'manual'
export type RunStatus = 'running' | 'paused' | 'succeeded' | 'failed' | 'aborted'
export type StageStatus = 'pending' | 'running' | 'success' | 'failed' | 'waiting' | 'skipped'
export type MergeMode = 'squash' | 'ff' | 'rebase'

// stage Params（与后端 model.go 对齐；用宽松 record，设计器按 type 动态渲染字段）
export type StageParams = Record<string, unknown>

// ---------- 领域类型 ----------
export interface StageDef {
  name: string
  type: StageType
  params?: StageParams
}

export interface PipelineTrigger {
  type?: string // 'manual' | 'webhook' | 'cron'（webhook/cron 留 Plan 3）
  branch?: string
}

export interface Pipeline {
  id: string
  tenantId?: string
  appId: string
  name: string
  kind: 'ci' | 'cd'
  templateId?: string
  stages: StageDef[]
  trigger: PipelineTrigger
  createdAt: string
}

export interface PipelineTemplate {
  id: string
  name: string
  kind: 'ci' | 'cd'
  builtin: boolean
  description?: string
  stages: StageDef[]
}

export interface StageRun {
  index: number
  type: StageType
  name: string
  status: StageStatus
  startedAt?: string
  finishedAt?: string
  input?: Record<string, unknown>
  output?: Record<string, unknown>
  error?: string
}

export interface PipelineRun {
  id: string
  pipelineId: string
  appId: string
  branch: string
  commit?: string
  version?: string
  repoId?: string
  trigger: string
  status: RunStatus
  currentStage: number
  stageRuns: StageRun[]
  createdAt: string
  finishedAt?: string
}

// ---------- API ----------
const listPipelines = (appId: string) =>
  fetchJSON<Pipeline[]>(`/api/applications/${appId}/pipelines`)

const getPipeline = (appId: string, pid: string) =>
  fetchJSON<Pipeline>(`/api/applications/${appId}/pipelines/${pid}`)

async function createPipeline(appId: string, body: {
  name: string; kind: 'ci' | 'cd'; templateId?: string; stages?: StageDef[]; trigger?: PipelineTrigger
}): Promise<Pipeline> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '创建失败')
  return json.data as Pipeline
}

async function updatePipeline(appId: string, pid: string, p: Pipeline): Promise<Pipeline> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '保存失败')
  return json.data as Pipeline
}

async function deletePipeline(appId: string, pid: string): Promise<void> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}`, { method: 'DELETE' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '删除失败')
  }
}

const listTemplates = () => fetchJSON<PipelineTemplate[]>('/api/pipeline-templates')

async function triggerRun(appId: string, pid: string, body: {
  branch: string; commit?: string; version?: string
}): Promise<PipelineRun> {
  const resp = await fetchAuth(`/api/applications/${appId}/pipelines/${pid}/run`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  const json = await resp.json()
  if (!resp.ok) throw new Error(json.error || '触发失败')
  return json.data as PipelineRun
}

const getRun = (id: string) => fetchJSON<PipelineRun>(`/api/pipelineruns/${id}`)

const listRuns = (q: { appId?: string; pipelineId?: string; status?: string } = {}) => {
  const qs = new URLSearchParams()
  if (q.appId) qs.set('appId', q.appId)
  if (q.pipelineId) qs.set('pipelineId', q.pipelineId)
  if (q.status) qs.set('status', q.status)
  const s = qs.toString()
  return fetchJSON<PipelineRun[]>(`/api/pipelineruns${s ? '?' + s : ''}`)
}

async function approveStage(runId: string, stageIdx: number): Promise<void> {
  const resp = await fetchAuth(`/api/pipelineruns/${runId}/stages/${stageIdx}/approve`, { method: 'POST' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '审批失败')
  }
}

async function abortRun(runId: string): Promise<void> {
  const resp = await fetchAuth(`/api/pipelineruns/${runId}/abort`, { method: 'POST' })
  if (!resp.ok) {
    const json = await resp.json().catch(() => ({}))
    throw new Error(json.error || '取消失败')
  }
}

export {
  listPipelines, getPipeline, createPipeline, updatePipeline, deletePipeline,
  listTemplates, triggerRun, getRun, listRuns, approveStage, abortRun,
}
```

- [ ] **Step 2: 类型检查通过**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit`
Expected: 无错误（新增文件类型自洽）。

- [ ] **Step 3: commit**

```bash
git add frontend/console-user/src/api/pipeline.ts
git commit -m "feat(pipeline-fe): TS 类型 + API 层（对接 8 流水线端点）"
```

---

## Task 2: 流水线列表 tab（`AppPipelines.vue`）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/AppPipelines.vue`
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`（DevOps 组首位注册 tab）

**Interfaces:**
- Consumes: Task 1 全部类型与 API。
- Produces: `AppPipelines` 组件（props `{appId}`），子组件 `PipelineDesigner`（Task 3）/`PipelineRunView`（Task 4）通过动态 import 占位（Task 3/4 接入前先用 `el-dialog` 占位提示「设计器/运行视图待接入」，确保 Task 2 独立可测）。

- [ ] **Step 1: 写 `AppPipelines.vue`（列表 + 新建弹窗 + 删除 + 触发 run 入口）**

```vue
<script setup lang="ts">
// 应用详情 - 流水线 tab：按 Kind 分组卡片，新建（从模板/空白/微服务快捷）、删除、编辑、运行。
import { ref, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchAuth } from '@/api'
import {
  type Pipeline, type PipelineTemplate, type PipelineRun,
  listPipelines, createPipeline, deletePipeline, listTemplates, triggerRun,
} from '@/api/pipeline'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

const pipelines = ref<Pipeline[]>([])
const templates = ref<PipelineTemplate[]>([])
const loading = ref(false)

// 新建弹窗
const createDlg = ref(false)
const creating = ref(false)
constcreateForm = ref<{ name: string; kind: 'ci' | 'cd'; templateId: string }>({ name: '', kind: 'ci', templateId: 'tpl-ci' })

// 设计器 / 运行视图抽屉（Task 3/4 接入；先占位）
const designerPid = ref<string | null>(null)
const runViewId = ref<string | null>(null)

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

// 微服务快捷：一次建 ci + cd 两条
async function createMicroservice() {
  const { value: name } = await ElMessageBox.prompt('微服务名（用作 ci/cd 流水线名前缀）', '新建微服务流水线', {
    confirmButtonText: '创建 ci+cd', cancelButtonText: '取消', inputPlaceholder: '如 product',
  }).catch(() => ({ value: '' }))
  if (!name) return
  try {
    const ci = await createPipeline(props.appId, { name: `${name}-ci`, kind: 'ci', templateId: 'tpl-ci' })
    const cd = await createPipeline(props.appId, { name: `${name}-cd`, kind: 'cd', templateId: 'tpl-cd' })
    pipelines.value.push(ci, cd)
    ElMessage.success(`已创建 ${name}-ci + ${name}-cd`)
  } catch (e: any) {
    ElMessage.error(e.message || '创建失败')
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

// 触发运行（CD 走版本选择，简化：默认 branch=main）
async function run(p: Pipeline) {
  try {
    const run = await triggerRun(props.appId, p.id, { branch: p.trigger.branch || 'main' })
    runViewId.value = run.id
    ElMessage.success('已触发运行')
  } catch (e: any) {
    ElMessage.error(e.message || '触发失败') // 含 403 prod:write / 409 单实例串行 / 400 envId 缺失
  }
}

// 流水线是否含 prod deploy（卡片标红警示）
function hasProdDeploy(p: Pipeline): boolean {
  return p.stages.some((s) => s.type === 'deploy' &&
    envStore.envs.find((e) => e.id === s.params?.envId)?.type === 'prod')
}

// 最近一次运行状态（卡片角标，按 pipelineId 取最近 run；首屏不阻塞，可选加载）
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
    <div class="actions">
      <el-button type="primary" @click="openCreate">＋ 新建流水线</el-button>
      <el-button @click="createMicroservice">微服务（ci+cd）</el-button>
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
                      :type="statusTag(latestRuns[p.id]?.status)!.type">
                {{ statusTag(latestRuns[p.id]?.status)!.label }}
              </el-tag>
            </div>
            <div class="pipe-stages">
              <span v-for="(s, i) in p.stages" :key="i" class="stage-chip">{{ s.name }}</span>
            </div>
            <div class="pipe-actions">
              <el-button size="small" @click="designerPid = p.id">编辑</el-button>
              <el-button size="small" type="primary" @click="run(p)">运行</el-button>
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

    <!-- 设计器 / 运行视图：Task 3/4 接入，先占位 -->
    <el-drawer v-if="designerPid" v-model="designerPid" :model-value="!!designerPid" size="60%" title="流水线设计器"
               @close="designerPid = null">
      <PipelineDesigner :app-id="appId" :pid="designerPid" @saved="load" />
    </el-drawer>
    <el-drawer v-if="runViewId" v-model="runViewId" :model-value="!!runViewId" size="50%" title="运行视图"
               @close="runViewId = null">
      <PipelineRunView :run-id="runViewId" />
    </el-drawer>
  </div>
</template>

<style scoped>
.actions { margin-bottom: 16px; }
.group { margin-bottom: 24px; }
.group-title { font-weight: 600; color: var(--el-text-color-primary); margin-bottom: 12px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px; }
.pipe-card { border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 14px; background: var(--el-bg-color); position: relative; }
.pipe-card.prod-warn { border-color: var(--el-color-danger); }
.pipe-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.pipe-name { font-weight: 600; }
.pipe-stages { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.stage-chip { background: var(--el-fill-color-light); padding: 2px 8px; border-radius: 10px; font-size: 12px; }
.pipe-actions { display: flex; gap: 8px; }
.prod-badge { color: var(--el-color-danger); font-size: 12px; margin-top: 8px; }
</style>
```

注意：组件依赖 `PipelineDesigner`/`PipelineRunView`（Task 3/4 创建），本步先用动态 import 占位——在 `<script setup>` 顶部加：

```typescript
// Task 3/4 接入前的占位组件（避免本任务编译报错；Task 3/4 接入后替换为真实 import）
const PipelineDesigner = { template: '<div class="todo">设计器待接入（Task 3）</div>' }
const PipelineRunView = { template: '<div class="todo">运行视图待接入（Task 4）</div>' }
```

Task 3/4 完成后把这两行替换为 `import PipelineDesigner from './PipelineDesigner.vue'` / `import PipelineRunView from './PipelineRunView.vue'`。

`el-drawer` 双向绑定修正：占位阶段用 `:model-value` + `@close` 控制（避免 v-model 与 model-value 同时写）。Task 3/4 接入时改为标准 `v-model="designerPid"` + `v-if` 控制卸载。

- [ ] **Step 2: 接入 ApplicationDetail.vue「流水线」tab（DevOps 组首位）**

修改 `frontend/console-user/src/views/ApplicationDetail.vue`：

1. 顶部 import 区加：
```typescript
import AppPipelines from './app-tabs/AppPipelines.vue'
```

2. `tabGroups`（约 line 250-254）DevOps 组首位加「流水线」：
```typescript
const tabGroups = [
  { label: '运行态', tabs: ['概览', '部署', '服务治理', '可观测'] as const },
  { label: '资源', tabs: ['资源绑定', '配置', '用量'] as const },
  { label: 'DevOps', tabs: ['流水线', '代码仓库', '构建', '镜像', '发布'] as const },
]
```

3. `TabName` 联合类型（约 line 255）首位加 `'流水线'`：
```typescript
type TabName = '概览' | '部署' | '服务治理' | '可观测' | '资源绑定' | '配置' | '用量' | '流水线' | '代码仓库' | '构建' | '镜像' | '发布'
```

4. tabs 渲染区（约 line 355-360，`<template v-for="g in tabGroups">` 内的 `<el-tab-pane>` 列表），在「代码仓库」前加：
```vue
<el-tab-pane v-if="g.label === 'DevOps'" label="流水线" name="流水线">
  <AppPipelines :app-id="appId" />
</el-tab-pane>
```
（参考既有 `AppBuilds`/`AppRepositories` 的 tab-pane 写法对齐位置；若既有写法是集中渲染则在该处补 `name === '流水线'` 分支挂 `<AppPipelines :app-id="appId" />`。）

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过（占位组件使组件自洽）。

- [ ] **Step 4: 手动 e2e（启动 core 或集群）**

```bash
./bin/core  # 或集群 http://paas.k8s.dd/console/
# 浏览器登录 console-user → 选任一应用 → 「流水线」tab：
#   - 空列表显空状态
#   - 点「新建流水线」→ 选 ci + tpl-ci 模板 → 创建 → 列表出现卡片 + 自动进设计器抽屉（占位文案）
#   - 点「微服务（ci+cd）」→ 输入 product → 创建 ci+cd 两条卡片
#   - 点卡片「删除」→ 确认 → 卡片消失
#   - 点卡片「运行」→ 触发 run（build-only 会 succeeded；含 deploy 到无 envId 会 400 提示）
```

- [ ] **Step 5: commit**

```bash
git add frontend/console-user/src/views/app-tabs/AppPipelines.vue frontend/console-user/src/views/ApplicationDetail.vue
git commit -m "feat(pipeline-fe): 流水线列表 tab（按 Kind 分组 + 新建/删除/触发）"
```

---

## Task 3: 流水线设计器（`PipelineDesigner.vue`）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/PipelineDesigner.vue`
- Modify: `frontend/console-user/src/views/app-tabs/AppPipelines.vue`（替换占位 import）

**Interfaces:**
- Consumes: Task 1 `Pipeline`/`StageDef`/`getPipeline`/`updatePipeline`，`envStore`。
- Produces: `PipelineDesigner` 组件（props `{appId, pid}`，emits `saved`）。

- [ ] **Step 1: 写 `PipelineDesigner.vue`**

```vue
<script setup lang="ts">
// 流水线设计器：stage 有序列表 + 增删改 + 每 stage 参数面板（按 type 动态渲染）。
// 从 getPipeline 加载全量 → 本地编辑 stages 副本 → 保存调 updatePipeline。
import { ref, watch, computed } from 'vue'
import { ElMessage } from 'element-plus'
import {
  type Pipeline, type StageDef, type StageType, type ImageSource, type TestMode, type MergeMode,
  getPipeline, updatePipeline,
} from '@/api/pipeline'
import { useEnvStore } from '@/stores/env'

const props = defineProps<{ appId: string; pid: string }>()
const emit = defineEmits<{ (e: 'saved'): void }>()
const envStore = useEnvStore()

const pipeline = ref<Pipeline | null>(null)
const loading = ref(false)
const saving = ref(false)

const STAGE_TYPES: { type: StageType; label: string }[] = [
  { type: 'build', label: '构建' },
  { type: 'deploy', label: '部署' },
  { type: 'test', label: '测试' },
  { type: 'approve', label: '审批' },
  { type: 'promote', label: '提升' },
  { type: 'baseline', label: '写基线' },
]

async function load() {
  loading.value = true
  try {
    pipeline.value = await getPipeline(props.appId, props.pid)
  } catch (e: any) {
    ElMessage.error(e.message || '加载失败')
  } finally {
    loading.value = false
  }
}
watch(() => props.pid, load, { immediate: true })

function addStage(type: StageType) {
  if (!pipeline.value) return
  const name = STAGE_TYPES.find((t) => t.type === type)!.label
  const stage: StageDef = { name, type, params: defaultParams(type) }
  pipeline.value.stages.push(stage)
}
function defaultParams(type: StageType): Record<string, unknown> {
  switch (type) {
    case 'deploy': return { envId: '', imageSource: 'priorBuild' as ImageSource, strategy: 'rolling' }
    case 'test': return { mode: 'smoke' as TestMode, path: '/livez' }
    case 'approve': return { message: '等待审批' }
    case 'baseline': return { mainBranch: 'main', versionStrategy: 'auto-increment', mergeMode: 'squash' as MergeMode }
    default: return {}
  }
}
function removeStage(i: number) { pipeline.value?.stages.splice(i, 1) }
function moveStage(i: number, delta: number) {
  if (!pipeline.value) return
  const j = i + delta
  if (j < 0 || j >= pipeline.value.stages.length) return
  const arr = pipeline.value.stages
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

// deploy stage 目标环境是否 prod（标红 + 保存提示）
function stageEnvType(s: StageDef): string | undefined {
  if (s.type !== 'deploy') return undefined
  return envStore.envs.find((e) => e.id === s.params?.envId)?.type
}

async function save() {
  if (!pipeline.value) return
  // deploy stage 必填 envId（前端预校验，与后端 fail-fast 一致）
  for (const s of pipeline.value.stages) {
    if (s.type === 'deploy' && !s.params?.envId) {
      ElMessage.warning(`deploy stage「${s.name}」缺环境`); return
    }
  }
  saving.value = true
  try {
    pipeline.value = await updatePipeline(props.appId, props.pid, pipeline.value)
    ElMessage.success('已保存')
    emit('saved')
  } catch (e: any) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-loading="loading" class="designer">
    <template v-if="pipeline">
      <div class="meta">
        <span class="name">{{ pipeline.name }}</span>
        <el-tag size="small">{{ pipeline.kind.toUpperCase() }}</el-tag>
      </div>

      <div class="stages">
        <div v-for="(s, i) in pipeline.stages" :key="i" class="stage-item"
             :class="{ 'prod-env': stageEnvType(s) === 'prod' }">
          <div class="stage-head">
            <el-input v-model="s.name" size="small" style="width: 140px" />
            <el-tag size="small" type="info">{{ s.type }}</el-tag>
            <el-tag v-if="stageEnvType(s) === 'prod'" size="small" type="danger">生产环境</el-tag>
            <div class="stage-ops">
              <el-button size="small" text :disabled="i === 0" @click="moveStage(i, -1)">↑</el-button>
              <el-button size="small" text :disabled="i === pipeline.stages.length - 1" @click="moveStage(i, 1)">↓</el-button>
              <el-button size="small" text type="danger" @click="removeStage(i)">✕</el-button>
            </div>
          </div>

          <!-- deploy 参数 -->
          <div v-if="s.type === 'deploy'" class="params">
            <label>环境</label>
            <el-select v-model="s.params!.envId" size="small" placeholder="选环境" style="width: 160px">
              <el-option v-for="e in envStore.envs" :key="e.id" :value="e.id"
                         :label="`${e.name}（${e.type}）`" />
            </el-select>
            <label>镜像来源</label>
            <el-select v-model="s.params!.imageSource" size="small" style="width: 130px">
              <el-option value="priorBuild" label="前序构建" />
              <el-option value="selected" label="指定镜像" />
              <el-option value="latestReady" label="最新可用" />
            </el-select>
            <template v-if="s.params!.imageSource === 'selected'">
              <label>镜像 ID</label>
              <el-input v-model="s.params!.imageId" size="small" placeholder="img-xxx" style="width: 140px" />
            </template>
          </div>

          <!-- test 参数 -->
          <div v-else-if="s.type === 'test'" class="params">
            <label>模式</label>
            <el-radio-group v-model="s.params!.mode" size="small">
              <el-radio value="smoke">冒烟（HTTP 探活）</el-radio>
              <el-radio value="manual">人工确认</el-radio>
            </el-radio-group>
            <template v-if="s.params!.mode === 'smoke'">
              <label>探活路径</label>
              <el-input v-model="s.params!.path" size="small" style="width: 140px" />
            </template>
            <template v-else>
              <label>提示语</label>
              <el-input v-model="s.params!.message" size="small" style="width: 200px" />
            </template>
          </div>

          <!-- approve 参数 -->
          <div v-else-if="s.type === 'approve'" class="params">
            <label>审批提示</label>
            <el-input v-model="s.params!.message" size="small" style="width: 260px" />
          </div>

          <!-- baseline 参数 -->
          <div v-else-if="s.type === 'baseline'" class="params">
            <label>主干分支</label>
            <el-input v-model="s.params!.mainBranch" size="small" style="width: 120px" placeholder="留空=不合并" />
            <label>合并方式</label>
            <el-select v-model="s.params!.mergeMode" size="small" style="width: 100px">
              <el-option value="squash" label="squash" />
              <el-option value="ff" label="fast-forward" />
              <el-option value="rebase" label="rebase" />
            </el-select>
          </div>

          <!-- build 参数 -->
          <div v-else-if="s.type === 'build'" class="params">
            <label>分支覆盖</label>
            <el-input v-model="s.params!.branchOverride" size="small" style="width: 140px" placeholder="留空=用触发分支" />
          </div>

          <!-- promote 无参数 -->
          <div v-else-if="s.type === 'promote'" class="params hint">提升前序部署的 release 到下一阶环境（无参数）</div>
        </div>
      </div>

      <div class="add-stage">
        <el-dropdown @command="addStage" trigger="click">
          <el-button size="small">＋ 添加阶段</el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="t in STAGE_TYPES" :key="t.type" :command="t.type">{{ t.label }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>

      <div class="footer">
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.designer { padding: 0 20px; }
.meta { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.meta .name { font-size: 16px; font-weight: 600; }
.stages { display: flex; flex-direction: column; gap: 10px; }
.stage-item { border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 10px; }
.stage-item.prod-env { border-color: var(--el-color-danger); }
.stage-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.stage-ops { margin-left: auto; }
.params { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; }
.params label { color: var(--el-text-color-secondary); }
.params.hint { color: var(--el-text-color-secondary); font-style: italic; }
.add-stage { margin: 16px 0; }
.footer { position: sticky; bottom: 0; padding: 12px 0; }
</style>
```

- [ ] **Step 2: 替换 AppPipelines.vue 占位 import**

`AppPipelines.vue` `<script setup>` 顶部，删除占位两行：
```typescript
const PipelineDesigner = { template: '<div class="todo">设计器待接入（Task 3）</div>' }
```
改为：
```typescript
import PipelineDesigner from './PipelineDesigner.vue'
```
（保留 `PipelineRunView` 占位直到 Task 4。）同时把设计器 `el-drawer` 改为标准 `v-model="designerPid"`（去掉 `:model-value`）。

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过。

- [ ] **Step 4: 手动 e2e**

```bash
# console-user 应用详情「流水线」tab → 点卡片「编辑」→ 设计器抽屉：
#   - 加载现有 stages（从模板创建的有 4 个）
#   - 点「添加阶段」→ 选「审批」→ 列表新增，参数面板显审批提示输入
#   - deploy stage 选环境下拉（从 envStore 加载）→ 选 prod 环境 → stage 标红 + 「生产环境」tag
#   - 拖动 ↑↓ 调整顺序
#   - 删除某 stage
#   - 点「保存」→ ElMessage 成功 → 卡片 stage 缩略更新
#   - deploy 不选环境保存 → 前端预校验「缺环境」提示（不调后端）
```

- [ ] **Step 5: commit**

```bash
git add frontend/console-user/src/views/app-tabs/PipelineDesigner.vue frontend/console-user/src/views/app-tabs/AppPipelines.vue
git commit -m "feat(pipeline-fe): 流水线设计器（stage CRUD + 参数面板 + 生产环境标红）"
```

---

## Task 4: 运行视图（`PipelineRunView.vue`）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/PipelineRunView.vue`
- Modify: `frontend/console-user/src/views/app-tabs/AppPipelines.vue`（替换占位 import + 修复 drawer 绑定）

**Interfaces:**
- Consumes: Task 1 `PipelineRun`/`getRun`/`approveStage`/`abortRun`。
- Produces: `PipelineRunView` 组件（props `{runId}`）。

- [ ] **Step 1: 写 `PipelineRunView.vue`（5s 轮询 + stage 进度 + approve/abort）**

```vue
<script setup lang="ts">
// 运行视图：stage 进度条 + 5s 轮询到终态 + paused 显 approve + 失败展开 error + 取消按钮。
// 关键：轮询直接整体替换 run（避免 reactive 引用坑）；终态停止轮询；onUnmounted 清理。
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { type PipelineRun, type StageRun, getRun, approveStage, abortRun } from '@/api/pipeline'

const props = defineProps<{ runId: string }>()

const run = ref<PipelineRun | null>(null)
const loading = ref(false)
let timer: number | null = null

const TERMINAL = ['succeeded', 'failed', 'aborted']
const isTerminal = computed(() => !!run.value && TERMINAL.includes(run.value.status))

async function load() {
  try {
    const r = await getRun(props.runId) // 整体替换（reactive 引用坑规避）
    run.value = r
    if (TERMINAL.includes(r.status)) stopPoll()
  } catch (e: any) {
    ElMessage.error(e.message || '加载运行失败')
    stopPoll()
  }
}
function startPoll() {
  stopPoll()
  timer = window.setInterval(load, 5000)
}
function stopPoll() { if (timer) { clearInterval(timer); timer = null } }
onMounted(() => { loading.value = true; load().finally(() => { loading.value = false }); startPoll() })
onUnmounted(stopPoll)

async function approve(sr: StageRun) {
  try {
    await approveStage(props.runId, sr.index)
    ElMessage.success('已通过，继续运行')
    await load()
  } catch (e: any) { ElMessage.error(e.message || '审批失败') }
}
async function abort() {
  try {
    await abortRun(props.runId)
    ElMessage.success('已取消')
    await load()
  } catch (e: any) { ElMessage.error(e.message || '取消失败') }
}

const statusTagType = (s: string) => ({
  succeeded: 'success', failed: 'danger', aborted: 'info',
  running: 'warning', paused: 'warning', waiting: 'warning',
  pending: 'info', skipped: 'info',
} as Record<string, string>)[s] || 'info'

// stage output 友好展示
function outputSummary(sr: StageRun): string {
  if (!sr.output) return ''
  const o = sr.output as Record<string, unknown>
  if (o.imageId) return `镜像: ${o.imageId}`
  if (o.releaseId) return `发布: ${o.releaseId}${o.workloadDomain ? ' · ' + o.workloadDomain : ''}`
  if (o.result) return `${o.result}${o.url ? ' · ' + o.url : ''}`
  return JSON.stringify(o)
}
</script>

<template>
  <div v-loading="loading" class="run-view">
    <template v-if="run">
      <div class="run-head">
        <el-tag :type="statusTagType(run.status) as any">{{ run.status }}</el-tag>
        <span class="branch">分支 {{ run.branch }}</span>
        <span class="commit" v-if="run.commit">{{ run.commit.slice(0, 8) }}</span>
        <el-button v-if="!isTerminal" size="small" type="danger" plain @click="abort">取消运行</el-button>
      </div>

      <div class="stages-progress">
        <div v-for="sr in run.stageRuns" :key="sr.index" class="stage-row"
             :class="{ failed: sr.status === 'failed', waiting: sr.status === 'waiting' }">
          <div class="stage-info">
            <el-tag :type="statusTagType(sr.status) as any" size="small">{{ sr.status }}</el-tag>
            <span class="stage-name">{{ sr.name }}</span>
            <span class="stage-type">({{ sr.type }})</span>
          </div>
          <div class="stage-detail">
            <div v-if="outputSummary(sr)" class="output">✓ {{ outputSummary(sr) }}</div>
            <div v-if="sr.error" class="error">✕ {{ sr.error }}</div>
            <div v-if="sr.status === 'waiting'" class="waiting-action">
              <span>{{ (sr.input as any)?.message || '等待操作' }}</span>
              <el-button size="small" type="primary" @click="approve(sr)">
                {{ sr.type === 'approve' ? '通过审批' : '确认通过' }}
              </el-button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="!isTerminal" class="polling-hint">每 5 秒自动刷新…</div>
    </template>
  </div>
</template>

<style scoped>
.run-view { padding: 0 20px; }
.run-head { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.branch, .commit { color: var(--el-text-color-secondary); font-size: 13px; }
.stages-progress { display: flex; flex-direction: column; gap: 8px; }
.stage-row { border-left: 3px solid var(--el-border-color); padding: 8px 12px; background: var(--el-fill-color-blank); }
.stage-row.failed { border-left-color: var(--el-color-danger); }
.stage-row.waiting { border-left-color: var(--el-color-warning); }
.stage-info { display: flex; align-items: center; gap: 8px; }
.stage-name { font-weight: 600; }
.stage-type { color: var(--el-text-color-secondary); font-size: 12px; }
.stage-detail { margin-top: 6px; font-size: 13px; }
.output { color: var(--el-color-success); }
.error { color: var(--el-color-danger); word-break: break-all; }
.waiting-action { display: flex; align-items: center; gap: 12px; margin-top: 6px; }
.polling-hint { margin-top: 16px; color: var(--el-text-color-secondary); font-size: 12px; }
</style>
```

- [ ] **Step 2: 替换 AppPipelines.vue 占位 + 修复 drawer**

`AppPipelines.vue` 删除剩余占位：
```typescript
const PipelineRunView = { template: '<div class="todo">运行视图待接入（Task 4）</div>' }
```
改为：
```typescript
import PipelineRunView from './PipelineRunView.vue'
```
运行视图 `el-drawer` 改标准 `v-model="runViewId"`（去掉 `:model-value`）。

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过。

- [ ] **Step 4: 手动 e2e（含审批/取消流转）**

```bash
# 1) build-only pipeline 运行：触发 → 运行视图 → stage running → succeeded（轮询停止）
# 2) 含 approve stage 的 pipeline（设计器加 approve）：
#    触发 → engine 推进到 approve 暂停 → 运行视图 stage 显 waiting + 「通过审批」按钮
#    → 点通过 → stage success → 继续推进
# 3) 运行中点「取消运行」→ run.status=aborted，轮询停止
# 4) 失败 stage（deploy 缺 envId 已被前端拦；用 deploy 到不存在 env 触发后端错误）→ 展开错误
```

- [ ] **Step 5: commit**

```bash
git add frontend/console-user/src/views/app-tabs/PipelineRunView.vue frontend/console-user/src/views/app-tabs/AppPipelines.vue
git commit -m "feat(pipeline-fe): 运行视图（stage 进度 + 5s 轮询 + approve/abort）"
```

---

## Task 5: CD 版本选择器 + 触发确认（`AppPipelines.vue` 增强）

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/AppPipelines.vue`

**Interfaces:**
- Consumes: Task 1 `triggerRun`，Task 4 运行视图，`useDangerConfirm`，envStore。

**背景**：Task 2 的 `run()` 是简化版（直接 branch=main 触发）。本任务补：① CD 卡片显版本/镜像选择器（`imageSource=selected` 时让用户选镜像，从 `/api/applications/{id}/images` 加载）；② 含 prod deploy 的 run 触发前 `confirmDangerous({isProd:true})`。

- [ ] **Step 1: 改 `AppPipelines.vue` 的 `run()` + 加 CD 触发弹窗**

在 `<script setup>` 加（import 区补 `import { confirmDangerous } from '@/composables/useDangerConfirm'`）：

```typescript
import { confirmDangerous } from '@/composables/useDangerConfirm'

interface Image { id: string; tag: string; status: string }
const cdRunDlg = ref(false)
const cdRunForm = ref<{ pipeline: Pipeline | null; branch: string; version: string; imageId: string }>(
  { pipeline: null, branch: 'main', version: '', imageId: '' }
)
const images = ref<Image[]>([])

// 判断 pipeline 是否需要选镜像（含 imageSource=selected 的 deploy stage）
function needsImage(p: Pipeline): boolean {
  return p.stages.some((s) => s.type === 'deploy' && s.params?.imageSource === 'selected')
}

// 判断 pipeline 是否含 prod deploy（触发确认用，按资源 env.type 显式判）
function isProdPipeline(p: Pipeline): boolean {
  return hasProdDeploy(p)
}

function openRun(p: Pipeline) {
  if (needsImage(p) || p.kind === 'cd') {
    // CD / 需选镜像：打开弹窗
    cdRunForm.value = { pipeline: p, branch: p.trigger.branch || 'main', version: '', imageId: '' }
    cdRunDlg.value = true
    loadImages()
  } else {
    // CI 简化：直接触发（含 prod 走确认）
    doRun(p, { branch: p.trigger.branch || 'main' })
  }
}

async function loadImages() {
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/images`)
    images.value = (await resp.json()).data ?? []
  } catch { /* 非关键 */ }
}

async function confirmCdRun() {
  const p = cdRunForm.value.pipeline
  if (!p) return
  if (needsImage(p) && !cdRunForm.value.imageId) { ElMessage.warning('请选择镜像'); return }
  // 含 prod deploy 走危险确认（显式 isProd 按资源 env.type）
  if (isProdPipeline(p)) {
    const ok = await confirmDangerous({
      action: '发布', target: p.name, isProd: true,
    })
    if (!ok) return
  }
  await doRun(p, {
    branch: cdRunForm.value.branch,
    version: cdRunForm.value.version || undefined,
  })
  cdRunDlg.value = false
}

async function doRun(p: Pipeline, body: { branch: string; version?: string }) {
  try {
    const r = await triggerRun(props.appId, p.id, body)
    runViewId.value = r.id
    ElMessage.success('已触发运行')
  } catch (e: any) {
    ElMessage.error(e.message || '触发失败') // 403 prod:write / 409 单实例 / 400 envId
  }
}
```

把原 `run()` 函数删除（被 `openRun` 替代），模板里卡片「运行」按钮 `@click="run(p)"` 改为 `@click="openRun(p)"`。

- [ ] **Step 2: 模板加 CD 触发弹窗**

`AppPipelines.vue` 模板末尾（新建弹窗 dialog 后）加：

```vue
<el-dialog v-model="cdRunDlg" title="触发发布流水线" width="460px">
  <el-form label-width="80px" v-if="cdRunForm.pipeline">
    <el-form-item label="分支"><el-input v-model="cdRunForm.branch" /></el-form-item>
    <el-form-item label="版本"><el-input v-model="cdRunForm.version" placeholder="留空=自动" /></el-form-item>
    <el-form-item v-if="needsImage(cdRunForm.pipeline)" label="镜像">
      <el-select v-model="cdRunForm.imageId" placeholder="选镜像" style="width: 100%">
        <el-option v-for="img in images.filter(i => i.status === 'ready')" :key="img.id"
                   :value="img.id" :label="`${img.id} (${img.tag})`" />
      </el-select>
    </el-form-item>
    <el-form-item v-if="isProdPipeline(cdRunForm.pipeline)">
      <el-alert type="warning" :closable="false" show-icon
                title="⚠️ 此流水线部署到生产环境，触发需二次确认" />
    </el-form-item>
  </el-form>
  <template #footer>
    <el-button @click="cdRunDlg = false">取消</el-button>
    <el-button type="primary" @click="confirmCdRun">触发</el-button>
  </template>
</el-dialog>
```

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过。

- [ ] **Step 4: 手动 e2e**

```bash
# CI 卡片（build-only 或 deploy test）→ 点「运行」→ 直接触发（无弹窗）
# CD 卡片（含 deploy selected / prod）→ 点「运行」→ 弹窗（分支/版本/镜像选择）
#   - 选 prod 环境 deploy 的 CD → 弹窗显生产警示 → 触发弹 confirmDangerous（生产二次确认）
#   - developer 触发 prod CD → 后端 403 → ElMessage「forbidden: missing prod:write」
#   - tenant-admin 触发 → 成功 → 运行视图打开
```

- [ ] **Step 5: commit**

```bash
git add frontend/console-user/src/views/app-tabs/AppPipelines.vue
git commit -m "feat(pipeline-fe): CD 版本/镜像选择器 + 生产触发二次确认"
```

---

## Task 6: DevOps 中心「最近运行」section（`DevOps.vue`）

**Files:**
- Modify: `frontend/console-user/src/views/DevOps.vue`

**Interfaces:**
- Consumes: Task 1 `listRuns`/`PipelineRun` 类型。

**背景**：spec「DevOps 中心增强」——现有流水线矩阵 tab（app×env）保留，新增「最近运行」section（跨应用所有 PipelineRun，看谁在跑/卡住/失败）。

- [ ] **Step 1: `DevOps.vue` 加最近运行数据加载 + section**

在 `<script setup>` 加（import 区补 `import { listRuns, type PipelineRun } from '@/api/pipeline'`）：

```typescript
import { listRuns, type PipelineRun } from '@/api/pipeline'

const recentRuns = ref<PipelineRun[]>([])
async function loadRecentRuns() {
  try {
    recentRuns.value = await listRuns() // 全租户跨应用最近运行
  } catch { /* 非关键 */ }
}
// 复用既有 onMounted 轮询注册（buildruns 5s / releases 10s）；最近运行挂到 releases 轮询同频
```

在既有 `onMounted`（DevOps.vue 已有 buildruns/releases 轮询 setup）调用 `loadRecentRuns()`，并把 `loadRecentRuns` 加到 releases 的 10s `setInterval` 里（与 `loadReleases` 并列调用）。`onUnmounted` 清理沿用既有 clearInterval（无需新增 timer）。

- [ ] **Step 2: 模板加「最近运行」section**

在 DevOps.vue 流水线矩阵 tab 的矩阵视图下方（或独立 section），加：

```vue
<el-divider>最近流水线运行</el-divider>
<el-table :data="recentRuns.slice(0, 20)" size="small" empty-text="暂无运行">
  <el-table-column prop="appId" label="应用" min-width="120">
    <template #default="{ row }">
      <el-link type="primary" @click="$router.push('/applications/' + row.appId)">{{ row.appId }}</el-link>
    </template>
  </el-table-column>
  <el-table-column prop="pipelineId" label="流水线" min-width="140" />
  <el-table-column prop="branch" label="分支" min-width="90" />
  <el-table-column prop="status" label="状态" min-width="100">
    <template #default="{ row }">
      <el-tag size="small" :type="({succeeded:'success',failed:'danger',aborted:'info',running:'warning',paused:'warning'} as any)[row.status] || 'info'">
        {{ row.status }}
      </el-tag>
    </template>
  </el-table-column>
  <el-table-column prop="currentStage" label="当前阶段" min-width="80">
    <template #default="{ row }">{{ row.currentStage + 1 }} / {{ row.stageRuns.length }}</template>
  </el-table-column>
  <el-table-column prop="createdAt" label="开始时间" min-width="160" />
</el-table>
```

- [ ] **Step 3: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过。

- [ ] **Step 4: 手动 e2e**

```bash
# DevOps 中心 → 流水线 tab → 底部「最近流水线运行」section：
#   - 触发几个 run 后，列表出现（跨应用聚合）
#   - 应用列可点跳应用详情
#   - 状态列 running/paused 黄色，succeeded 绿色，failed 红色
#   - 10s 自动刷新（看到 run 推进）
```

- [ ] **Step 5: commit**

```bash
git add frontend/console-user/src/views/DevOps.vue
git commit -m "feat(pipeline-fe): DevOps 中心加最近流水线运行 section（跨应用观察）"
```

---

## Task 7: 集成验证 + 部署

**Files:** 无新文件（全量验证）。

- [ ] **Step 1: 全量类型检查 + 三套前端构建**

Run:
```bash
cd frontend && pnpm install && pnpm build
```
Expected: console-user / console-admin / landing 三套均通过（console-admin/landing 不受影响，仅 console-user 改动）。

- [ ] **Step 2: 后端无回归**

Run: `go test ./...`
Expected: 全绿（本计划零后端改动，确认未误碰）。

- [ ] **Step 3: 集群部署 + e2e**

```bash
./scripts/deploy-k8s.sh
# 浏览器验证完整闭环：
#   1. console-user 登录 → 选应用 → 「流水线」tab（DevOps 组首位）
#   2. 新建 ci 流水线（tpl-ci 模板）→ 自动进设计器 → deploy stage 选 test 环境 → 保存
#   3. 点「运行」→ 运行视图打开 → build stage 推进（集群内 mock builder 派生 image）→ deploy → test smoke → baseline
#      （注意：集群 PG 路径 + K8s reconciler，deploy 的 PollWorkloadReady 在 K8s 下 ready 会涨；mock workload 可能超时——这是后端留后续项，前端只验证 UI 流转）
#   4. CD 流水线：含 approve → 运行到 approve 暂停 → 运行视图「通过审批」→ 继续
#   5. developer 账号触发 prod CD → 403 提示；tenant-admin → 二次确认 → 成功
#   6. DevOps 中心「最近流水线运行」section 显示刚跑的 run
```

- [ ] **Step 4: commit 部署产物（若有 chart/Dockerfile 变更，本计划无）**

无提交（纯前端，已逐任务 commit）。

---

## Self-Review

**1. Spec 覆盖**：
- 应用详情「流水线」tab（DevOps 组首位）→ Task 2 ✓
- 按 Kind 分组展示 + 卡片 trigger/stage 缩略/最近运行状态 → Task 2 ✓
- CI 卡 webhook 地址（复制）→ **未覆盖**（webhook 属 Plan 3 触发器，本计划不实现 webhook；CI 卡暂不显 webhook 地址）
- CD 卡「发布」按钮 + 版本选择器 → Task 5 ✓
- 流水线设计器（stage 增删改 + 参数面板 + trigger + 从模板初始化 + 生产 deploy 标 prod）→ Task 3 ✓（trigger 编辑简化：模板带入 branch，设计器未暴露 trigger.type 编辑——manual 是默认，webhook/cron 属 Plan 3）
- 运行视图（stage 进度轮询 + Output + paused approve + 失败 error + 取消）→ Task 4 ✓
- DevOps 中心「最近运行」section → Task 6 ✓
- 模板选择（开发/发布/空白 + 微服务快捷）→ Task 2 ✓
- 预置模板渲染（从 `/api/pipeline-templates` 加载）→ Task 2 ✓

**显式不做（归 Plan 3 或后续）**：webhook 地址复制（Plan 3 触发器）；trigger.type 在设计器编辑（Plan 3）；cron 定时（Plan 3）。

**2. 占位扫描**：无 TBD/TODO；占位组件在 Task 2 有明确替换步骤（Task 3/4）。`el-drawer` 双向绑定 Task 2 占位用 `:model-value`+`@close`，Task 3/4 明确改回 `v-model`。

**3. 类型一致性**：`StageDef.params` 全用 `Record<string, unknown>`（与后端 JSON map 对齐），设计器 `v-model` 绑 `s.params!.xxx`（非空断言，因 defaultParams 保证 deploy/test 等都有 params；promote 无 params 不渲染面板）。`Pipeline`/`PipelineRun`/`StageRun` 字段名与后端 JSON tag 一致（camelCase：appId/pipelineId/currentStage/stageRuns/status）。API 函数名 Task 1 定义、Task 2-6 消费一致（listPipelines/createPipeline/triggerRun/getRun/approveStage/abortRun/listRuns）。
