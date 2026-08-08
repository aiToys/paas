<script setup lang="ts">
// 流水线设计器：stage 有序列表 + 增删改 + 每 stage 参数面板（按 type 动态渲染）。
// 从 getPipeline 加载全量 → 本地编辑 stages 副本 → 保存调 updatePipeline。
// deploy stage 目标环境为 prod 时标红警示（生产视觉强隔离）。
import { ref, watch } from 'vue'
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

function defaultParams(type: StageType): Record<string, unknown> {
  switch (type) {
    case 'deploy': return { envId: '', imageSource: 'priorBuild' as ImageSource, strategy: 'rolling' }
    case 'test': return { mode: 'smoke' as TestMode, path: '/livez' }
    case 'approve': return { message: '等待审批' }
    case 'baseline': return { mainBranch: 'main', versionStrategy: 'auto-increment', mergeMode: 'squash' as MergeMode }
    default: return {}
  }
}
function addStage(type: StageType) {
  if (!pipeline.value) return
  const name = STAGE_TYPES.find((t) => t.type === type)!.label
  pipeline.value.stages.push({ name, type, params: defaultParams(type) })
}
function removeStage(i: number) { pipeline.value?.stages.splice(i, 1) }
function moveStage(i: number, delta: number) {
  if (!pipeline.value) return
  const j = i + delta
  if (j < 0 || j >= pipeline.value.stages.length) return
  const arr = pipeline.value.stages
  const tmp = arr[i]; arr[i] = arr[j]; arr[j] = tmp
}

// deploy stage 目标环境是否 prod（标红）
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
            <el-select v-model="s.params!.mergeMode" size="small" style="width: 110px">
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
