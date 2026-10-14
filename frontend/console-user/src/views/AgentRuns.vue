<script setup lang="ts">
// Agent 运行列表（agentrun 观测通道，DevOps「Agent 运行」tab 内嵌）。
// 覆盖不经 paas pipeline 的 run（Gitea push 自动验收 / PR 自动审查 / console 手动）。
// 503=平台未配置 PAAS_AGENTRUN_URL（引导态）；运行中 10s 轮询；行点击进详情页。
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { formatDateTime } from '@/utils/format'
import { usePolling } from '@/composables/usePolling'
import { listRuns, phaseTagType, type AgentRunItem } from '@/api/agentrun'

const router = useRouter()
const runs = ref<AgentRunItem[]>([])
const loading = ref(true)
// 引导态：未配置（503）或需要登录会话（401）时显示，不当作空列表伪装
const unavailable = ref('')

async function load(): Promise<void> {
  try {
    const resp = await listRuns()
    runs.value = Array.isArray(resp) ? resp : []
    unavailable.value = ''
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    if (msg.includes('未配置') || msg.includes('PAAS_AGENTRUN_URL')) unavailable.value = '未配置'
    else if (msg.includes('登录')) unavailable.value = '会话'
    else unavailable.value = '不可达'
  } finally {
    loading.value = false
  }
}

const hasActive = () => runs.value.some((r) => r.phase === 'Running' || r.phase === 'Pending')
usePolling(load, 10000, { active: hasActive }) // usePolling 自带 onMounted 首拉 + 不可见暂停
</script>

<template>
  <div class="agent-runs">
    <el-alert v-if="unavailable === '未配置'" type="info" :closable="false" show-icon
      title="Agent 编排未接入"
      description="平台未配置 PAAS_AGENTRUN_URL。运维在 core 环境变量配置 agentrun 平台地址后，此处展示 AI 研发运行记录。" />
    <el-alert v-else-if="unavailable === '会话'" type="warning" :closable="false" show-icon
      title="需要登录会话" description="Agent 编排观测需要控制台登录态（API Key 通道不支持）。请重新登录后刷新。" />
    <el-alert v-else-if="unavailable === '不可达'" type="error" :closable="false" show-icon
      title="Agent 编排平台不可达" description="代理转发失败，请稍后重试或联系运维检查 agentrun 服务状态。" />
    <el-table v-else :data="runs" size="small" v-loading="loading" empty-text="暂无 Agent 运行记录"
      :row-style="{ cursor: 'pointer' }" @row-click="(row: AgentRunItem) => router.push(`/devops/agent-runs/${encodeURIComponent(row.name)}`)">
      <el-table-column label="名称" min-width="200">
        <template #default="{ row }">
          <span class="mono">{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="阶段" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.stage" size="small" type="info">{{ row.stage }}</el-tag>
          <span v-else class="faint">-</span>
        </template>
      </el-table-column>
      <el-table-column label="模式" width="90">
        <template #default="{ row }">{{ row.mode || '-' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag :type="phaseTagType(row.phase)" size="small">{{ row.phase || '-' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="判定" width="120">
        <template #default="{ row }">
          <span v-if="row.verdict" :class="{ 'verdict-fail': row.verdict !== 'pass' }">{{ row.verdict }}</span>
          <span v-else class="faint">-</span>
        </template>
      </el-table-column>
      <el-table-column label="产出分支" min-width="150">
        <template #default="{ row }">
          <span v-if="row.branch" class="mono">{{ row.branch }}</span>
          <span v-else class="faint">-</span>
        </template>
      </el-table-column>
      <el-table-column label="tokens" width="90" align="right">
        <template #default="{ row }">
          <span v-if="row.tokens" class="mono">{{ row.tokens }}</span>
          <span v-else class="faint">-</span>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="90">
        <template #default="{ row }">
          <el-button text type="primary" size="small"
            @click.stop="router.push(`/devops/agent-runs/${encodeURIComponent(row.name)}`)">详情</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.mono { font-family: var(--font-mono, monospace); }
.faint { color: var(--el-text-color-placeholder); }
.verdict-fail { color: var(--el-color-danger); }
</style>
