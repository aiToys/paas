<script setup lang="ts">
// 服务创建弹窗（v2 原生，替代跳老版 ?tab=services）：
// 契约对齐老版 AppServices —— static 免仓库/port，agent 加模型/工具，cron 加调度。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { createService, type ServiceType } from '@/api/service'
import { apiError, fetchList } from '@/api'

const props = defineProps<{ modelValue: boolean; appId: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'created'): void }>()

interface Repo { id: string; name: string }
const repos = ref<Repo[]>([])

const TYPE_OPTIONS: { value: ServiceType; label: string; desc: string }[] = [
  { value: 'backend', label: 'Backend', desc: '常规后端服务（HTTP/API）' },
  { value: 'web', label: 'Web', desc: '前端站点/网关层' },
  { value: 'agent', label: 'Agent', desc: 'AI 智能体（挂模型与工具）' },
  { value: 'cron', label: 'Cron', desc: '定时任务' },
  { value: 'static', label: 'Static', desc: '静态资源（免代码仓库）' },
]

const form = ref({
  name: '', type: 'backend' as ServiceType, repoId: '', repoPath: '',
  port: 8080, replicas: 1, modelRef: '', tools: '', schedule: '',
})

async function loadRepos() {
  repos.value = []
  try {
    repos.value = await fetchList<Repo>(`/api/applications/${props.appId}/repositories`)
  } catch { /* 静默降级：仅无选项 */ }
}
watch(() => props.modelValue, (open) => {
  if (open) {
    form.value = { name: '', type: 'backend', repoId: '', repoPath: '', port: 8080, replicas: 1, modelRef: '', tools: '', schedule: '' }
    void loadRepos()
  }
})

const isStatic = computed(() => form.value.type === 'static')
const needRepo = computed(() => !isStatic.value)
const canSubmit = computed(() => !!form.value.name.trim() && (!needRepo.value || !!form.value.repoId))
const submitting = ref(false)

async function submit() {
  if (!canSubmit.value) {
    ElMessage.warning(needRepo.value ? '请填写服务名称并选择代码仓库' : '请填写服务名称')
    return
  }
  submitting.value = true
  try {
    const f = form.value
    await createService(props.appId, {
      name: f.name.trim(),
      type: f.type,
      repoId: needRepo.value ? f.repoId : undefined,
      repoPath: f.repoPath.trim() || undefined,
      port: isStatic.value ? undefined : f.port,
      replicas: f.replicas,
      modelRef: f.type === 'agent' ? (f.modelRef.trim() || undefined) : undefined,
      tools: f.type === 'agent' && f.tools.trim()
        ? f.tools.split(/[,，]/).map((t) => t.trim()).filter(Boolean)
        : undefined,
      schedule: f.type === 'cron' ? (f.schedule.trim() || undefined) : undefined,
    })
    ElMessage.success(`服务「${f.name.trim()}」已创建`)
    emit('update:modelValue', false)
    emit('created')
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="新建服务"
    width="520px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-position="top" @submit.prevent="submit">
      <el-form-item label="服务名" required>
        <el-input v-model="form.name" placeholder="如 bff / chatbot" />
      </el-form-item>
      <el-form-item label="类型" required>
        <el-select v-model="form.type">
          <el-option v-for="t in TYPE_OPTIONS" :key="t.value" :value="t.value" :label="`${t.label} — ${t.desc}`" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="needRepo" label="代码仓库" required>
        <el-select v-model="form.repoId" placeholder="选择代码仓库" style="width: 100%">
          <el-option v-for="r in repos" :key="r.id" :value="r.id" :label="r.name" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="needRepo" label="仓库内路径（可选）">
        <el-input v-model="form.repoPath" placeholder="如 services/chatbot（monorepo 子目录）" />
      </el-form-item>
      <div v-if="!isStatic" class="form-row">
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="副本数">
          <el-input-number v-model="form.replicas" :min="0" :max="20" />
        </el-form-item>
      </div>
      <template v-if="form.type === 'agent'">
        <el-form-item label="模型引用（可选）">
          <el-input v-model="form.modelRef" placeholder="如 glm-5.2" />
        </el-form-item>
        <el-form-item label="工具（可选，逗号分隔）">
          <el-input v-model="form.tools" placeholder="如 product_search, order_refund" />
        </el-form-item>
      </template>
      <el-form-item v-if="form.type === 'cron'" label="调度（cron 表达式，可选）">
        <el-input v-model="form.schedule" placeholder="如 */5 * * * *" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!canSubmit" :loading="submitting" @click="submit">创建</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.form-row {
  display: flex;
  gap: 16px;
}
.form-row .el-form-item {
  flex: 1;
}
</style>
