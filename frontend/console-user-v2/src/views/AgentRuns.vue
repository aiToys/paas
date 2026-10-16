<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listRuns, cancelRun, phaseTagType, type AgentRunItem } from '@/api/agentrun'
import { usePolling } from '@/composables/usePolling'
import { apiError } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
const items = ref<AgentRunItem[]>([])
const loading = ref(true)
const error = ref('')
const q = ref('')
const page = ref(1)
const cancelling = ref('')
async function load() {
  try { items.value = await listRuns(); error.value = '' }
  catch (e) { error.value = apiError(e, 'AI 执行服务暂时不可用') }
  finally { loading.value = false }
}
usePolling(load, 5000, { active: () => items.value.some((r) => r.phase === 'Running' || r.phase === 'Pending') })
const filtered = computed(() => items.value.filter((r) => (r.name + r.request).toLowerCase().includes(q.value.trim().toLowerCase())))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
async function cancel(r: AgentRunItem) {
  try { await ElMessageBox.confirm('终止执行“' + r.name + '”？运行将停止，已完成的产物仍可查看。', '终止 AI 执行', { type: 'warning' }) } catch { return }
  cancelling.value = r.name
  try { await cancelRun(r.name); ElMessage.success('已发送终止请求'); await load() }
  catch (e) { ElMessage.error(apiError(e, '终止失败')) }
  finally { cancelling.value = '' }
}
</script>
<template>
  <section>
    <div class="filter-bar"><input v-model="q" class="filter-select" aria-label="搜索 AI 执行记录" placeholder="搜索执行名称或需求" @input="page = 1" /><button class="act" @click="load">刷新</button><span class="result-count">{{ filtered.length }} 条记录</span></div>
    <div v-if="loading" v-loading="true" style="height:200px"></div>
    <StatePanel v-else-if="error" title="无法加载 AI 执行记录" :description="error" error><button class="act" @click="load">重新加载</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="暂无 AI 执行记录" description="通过智能研发或智能体发起任务后，在这里跟进日志与产物。" />
    <template v-else><div class="table-scroll" data-table-scroll tabindex="0"><table class="list-table"><thead><tr><th scope="col">执行</th><th scope="col">阶段</th><th scope="col">状态</th><th scope="col">Tokens</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="r in rows" :key="r.name"><td><RouterLink :to="'/ai/runs/' + encodeURIComponent(r.name)" class="text-button">{{ r.name }}</RouterLink><div class="run-request">{{ r.request }}</div></td><td>{{ r.stage || '—' }}</td><td><el-tag :type="phaseTagType(r.phase)" size="small">{{ r.phase || '未知' }}</el-tag></td><td class="mono">{{ r.tokens ?? '—' }}</td><td><button v-if="r.phase === 'Running' || r.phase === 'Pending'" class="act danger small" :disabled="cancelling === r.name" @click="cancel(r)">终止</button><RouterLink v-else :to="'/ai/runs/' + encodeURIComponent(r.name)" class="text-button">查看详情</RouterLink></td></tr></tbody></table></div><TablePager v-model="page" :total="filtered.length" /></template>
  </section>
</template>
<style scoped>.run-request { max-width: 540px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 12px; color: var(--text-faint); margin-top: 4px; }</style>
