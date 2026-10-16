<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
import StatePanel from './StatePanel.vue'
// 交付·变更与集成批次（v2 原生）：Change（分支粒度）+ IntegrationBatch（火车发车状态机）。
// 待办 batch_approve 直达此处审批（approveBatch/releaseBatch 闭环）。
import { nextTick, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  listChanges, listBatches, integrateBatch, approveBatch, releaseBatch,
  type Change, type IntegrationBatch,
} from '@/api/change'
import { apiError } from '@/api'
import { statusOf, tagTypeToCls, CHANGE_STATUS, BATCH_STATUS } from '@/composables/useStatus'

const route = useRoute()
const props = defineProps<{ appId: string }>()

const changes = ref<Change[]>([])
const batches = ref<IntegrationBatch[]>([])
const loading = ref(true)
const error = ref('')
const acting = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [cs, bs] = await Promise.all([
      listChanges(props.appId),
      listBatches(props.appId),
    ])
    changes.value = cs
    batches.value = bs
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
    await nextTick()
    const focus = route.query.focus
    if (typeof focus === 'string') { const row = document.getElementById('entity-' + focus); row?.scrollIntoView({ block: 'center' }); row?.focus() }
  }
}
onMounted(load)

async function act(kind: 'integrate' | 'approve' | 'release', b: IntegrationBatch) {
  if (!await confirmAction(`确认对批次「${b.title}」执行「${kind === 'integrate' ? '集成' : kind === 'approve' ? '审批发车' : '发布'}」？`)) return
  acting.value = `${kind}:${b.id}`
  try {
    if (kind === 'integrate') await integrateBatch(props.appId, b.id)
    else if (kind === 'approve') await approveBatch(props.appId, b.id)
    else await releaseBatch(props.appId, b.id)
    ElMessage.success('操作成功')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '操作失败'))
  } finally {
    acting.value = ''
  }
}
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">变更与集成批次</span>
      <span class="card-sub">代码分支 → 集成验证 → 审批发布</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <template v-else>
      <div v-if="!changes.length && !batches.length" class="empty-hint">
        暂无变更——流水线会自动登记，或从「智能研发」发起
      </div>
      <template v-if="changes.length">
        <div class="sec-title">变更（{{ changes.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>变更</th><th>分支</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="c in changes" :key="c.id" :id="'entity-' + c.id" tabindex="-1" :class="{ 'focus-row': route.query.focus === c.id }">
              <td class="t-title">{{ c.title || c.id }}</td>
              <td><span class="mono dim">{{ c.branch }}</span></td>
              <td>
                <span class="status" :class="tagTypeToCls(statusOf(CHANGE_STATUS, c.status).type)">
                  {{ statusOf(CHANGE_STATUS, c.status).label }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
      <template v-if="batches.length">
        <div class="sec-title">集成批次（{{ batches.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>批次</th><th>状态</th><th class="col-act"></th></tr></thead>
          <tbody>
            <tr v-for="b in batches" :key="b.id" :id="'entity-' + b.id" tabindex="-1" :class="{ 'focus-row': route.query.focus === b.id }">
              <td class="t-title">{{ b.title || b.id }}</td>
              <td>
                <span class="status" :class="tagTypeToCls(statusOf(BATCH_STATUS, b.status).type)">
                  {{ statusOf(BATCH_STATUS, b.status).label }}
                </span>
              </td>
              <td class="col-act">
                <button
                  v-if="b.status === 'tested'" class="act primary"
                  :disabled="acting === `approve:${b.id}`" @click="act('approve', b)"
                >
审批发车
</button>
                <button
                  v-else-if="b.status === 'collecting'" class="act"
                  :disabled="acting === `integrate:${b.id}`" @click="act('integrate', b)"
                >
集成
</button>
                <button
                  v-else-if="b.status === 'releasing'" class="act" disabled
                >
发布中
</button>
              </td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
    </template>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.col-act { text-align: right; }
</style>
