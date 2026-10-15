<script setup lang="ts">
// CD 部署确认弹窗：找产品 CD 流水线 → 触发 run（分支可改）。
// 两处复用：产品服务面未部署行「去部署」、ServiceRunDrawer「部署」主按钮——
// 成功后 emit deployed，由调用方跳 v2 交付面看运行（不跳老版）。
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { listPipelines, triggerRun, type PipelineRun } from '@/api/pipeline'

const props = defineProps<{ modelValue: boolean; appId?: string; hint?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'deployed', run: PipelineRun): void }>()

const loading = ref(false)
const deploying = ref(false)
const cdPipelineId = ref('')
const branch = ref('main')

async function loadCd() {
  cdPipelineId.value = ''
  branch.value = 'main'
  if (!props.appId) return
  loading.value = true
  try {
    const pipes = await listPipelines(props.appId)
    cdPipelineId.value = pipes.find((p) => p.kind === 'cd')?.id ?? ''
  } catch {
    cdPipelineId.value = ''
  } finally {
    loading.value = false
  }
}
watch(() => props.modelValue, (open) => { if (open) void loadCd() })

async function doDeploy() {
  if (!props.appId || !cdPipelineId.value) return
  deploying.value = true
  try {
    const run = await triggerRun(props.appId, cdPipelineId.value, { branch: branch.value.trim() || 'main' })
    emit('update:modelValue', false)
    emit('deployed', run)
  } catch (e) {
    ElMessage.error((e as Error).message || '触发部署失败')
  } finally {
    deploying.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="部署到当前环境"
    width="440px"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="loading">
      <template v-if="cdPipelineId">
        <p class="deploy-hint">
          触发部署流水线：构建最新代码 → 部署{{ hint ? `「${hint}」` : '' }} → 冒烟（泳道/金丝雀由流水线阶段编排）。
        </p>
        <el-form label-position="top" @submit.prevent="doDeploy">
          <el-form-item label="分支">
            <el-input v-model="branch" placeholder="main" />
          </el-form-item>
        </el-form>
      </template>
      <p v-else-if="!loading" class="deploy-hint">产品还没有 CD 流水线，无法部署。</p>
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!cdPipelineId" :loading="deploying" @click="doDeploy">部署</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.deploy-hint {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.6;
}
</style>
