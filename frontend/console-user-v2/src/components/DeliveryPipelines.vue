<script setup lang="ts">
import StatePanel from './StatePanel.vue'
// 交付·流水线管理（v2 原生）：绑定列表（kind 徽章 + 触发方式）+ 手动触发。
// 触发后跳 v2 RunDetail 跟进。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { listPipelines, triggerRun, type Pipeline } from '@/api/pipeline'
import { apiError } from '@/api'

const props = defineProps<{ appId: string }>()
const router = useRouter()

const pipelines = ref<Pipeline[]>([])
const loading = ref(true)
const error = ref('')
const triggering = ref('')
const showRun = ref(false)
const selected = ref<Pipeline | null>(null)
const branch = ref('main')
function prepare(p: Pipeline) { selected.value = p; branch.value = 'main'; showRun.value = true }

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

const KIND_LABEL: Record<string, string> = { ci: 'CI', cd: 'CD', agent: '智能' }

async function run() {
  const p = selected.value
  if (!p || !branch.value.trim()) return
  triggering.value = p.id
  try {
    const run = await triggerRun(props.appId, p.id, { branch: branch.value.trim() })
    showRun.value = false
    ElMessage.success('已触发')
    router.push(`/runs/${run.id}`)
  } catch (e) {
    ElMessage.error(apiError(e, '触发失败'))
  } finally {
    triggering.value = ''
  }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">流水线</span>
      <span class="card-sub">模板绑定（内置模板自动创建，可参数覆盖）</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <div v-else-if="!pipelines.length" class="empty-hint">暂无流水线——创建产品时自动绑定内置 CI/CD</div>
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
            <button class="act" :disabled="triggering === p.id || p.disabled" @click="prepare(p)">
              {{ triggering === p.id ? '触发中…' : '运行' }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
</div>
    <el-dialog v-model="showRun" title="运行流水线" width="480px" append-to-body>
      <p>流水线：<strong>{{ selected?.name }}</strong></p>
      <p class="dim">服务、环境和审批阶段按此流水线配置执行，顶栏浏览环境不会覆盖这些目标。</p>
      <el-form label-position="top" @submit.prevent="run"><el-form-item label="代码分支" required><el-input v-model="branch" placeholder="例如 main" /></el-form-item></el-form>
      <template #footer><el-button @click="showRun = false">取消</el-button><el-button type="primary" :loading="!!triggering" :disabled="!branch.trim()" @click="run">运行此流水线</el-button></template>
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
</style>
