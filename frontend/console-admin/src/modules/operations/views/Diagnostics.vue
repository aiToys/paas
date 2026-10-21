<template>
  <PageContainer title="平台诊断（只读）">
    <template #header>
      <span v-if="data" class="generated">生成于 {{ data.generatedAt }}</span>
      <el-button
        :icon="Refresh"
        :loading="loading"
        @click="load"
      >
        刷新
      </el-button>
    </template>

    <el-alert
      type="info"
      :closable="false"
      show-icon
      title="全部检查只读：不写集群、不改数据、不触发 drift 修复。"
      description="漂移检查请到「工作负载 → Drift 对账」；依赖探测只报可达性（2s 超时），failed 可能仅本 Pod 网络受限，不代表数据面不可用。"
    />

    <el-alert
      v-if="error"
      class="gap"
      type="error"
      :closable="false"
      show-icon
      :title="error"
    />

    <template v-else-if="data">
      <div class="grid">
        <el-card
          shadow="never"
          header="版本与运行态"
        >
          <el-descriptions
            :column="1"
            size="small"
            border
          >
            <el-descriptions-item label="版本">
              {{ data.version.version }}
            </el-descriptions-item>
            <el-descriptions-item label="模块">
              {{ data.version.module || '—' }}
            </el-descriptions-item>
            <el-descriptions-item label="修订">
              <span class="mono">{{ data.version.revision || '—' }}</span>
              <el-tag
                v-if="data.version.modified === 'true'"
                size="small"
                type="warning"
                class="tag"
              >
                构建时工作区有未提交改动
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="Go / 平台">
              {{ data.version.goVersion }} · {{ data.version.platform }}
            </el-descriptions-item>
            <el-descriptions-item label="启动时间">
              {{ data.runtime.startedAt }}
              <span class="dim">（已运行 {{ uptimeText }}）</span>
            </el-descriptions-item>
            <el-descriptions-item label="Goroutine">
              {{ data.runtime.goroutines }}
            </el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card
          shadow="never"
          header="部署与集群"
        >
          <el-descriptions
            :column="1"
            size="small"
            border
          >
            <el-descriptions-item label="部署模式">
              <el-tag
                size="small"
                :type="data.deploy.mode === 'kubernetes' ? 'success' : 'info'"
              >
                {{ modeLabel }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="Kubeconfig 来源">
              {{ data.deploy.kubeconfigSource }}
            </el-descriptions-item>
            <el-descriptions-item label="命名空间范围">
              {{ data.deploy.namespaceScope || '全部命名空间' }}
            </el-descriptions-item>
            <el-descriptions-item label="集群连通性">
              <el-tag
                size="small"
                :type="reachType(data.deploy.connectivity)"
              >
                {{ reachLabel(data.deploy.connectivity) }}
              </el-tag>
              <span
                v-if="data.deploy.serverVersion"
                class="dim mono"
              > {{ data.deploy.serverVersion }}</span>
              <div
                v-if="data.deploy.error"
                class="dim"
              >
                {{ data.deploy.error }}
              </div>
            </el-descriptions-item>
            <el-descriptions-item label="RBAC（列 Workload CRD）">
              <el-tag
                size="small"
                :type="rbacType(data.deploy.rbacListWorkloads)"
              >
                {{ rbacLabel(data.deploy.rbacListWorkloads) }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="RBAC（列命名空间）">
              <el-tag
                size="small"
                :type="rbacType(data.deploy.rbacListNamespaces)"
              >
                {{ rbacLabel(data.deploy.rbacListNamespaces) }}
              </el-tag>
            </el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card
          shadow="never"
          header="元数据存储"
        >
          <el-descriptions
            :column="1"
            size="small"
            border
          >
            <el-descriptions-item label="后端">
              {{ data.storage.backend }}
            </el-descriptions-item>
            <el-descriptions-item label="连通性">
              <el-tag
                size="small"
                :type="data.storage.pingOk ? 'success' : 'danger'"
              >
                {{ data.storage.pingOk ? `正常（${data.storage.latencyMs}ms）` : '不可用' }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="迁移版本">
              <span class="mono">{{ data.storage.migrationVersion }}</span>
              <el-tag
                v-if="data.storage.migrationDirty"
                size="small"
                type="danger"
                class="tag"
              >
                dirty：迁移未完成，需人工处理
              </el-tag>
            </el-descriptions-item>
          </el-descriptions>
          <p
            v-if="data.storage.error"
            class="dim error"
          >
            {{ data.storage.error }}
          </p>
        </el-card>
      </div>

      <el-card
        shadow="never"
        header="外部依赖可达性"
        class="gap"
      >
        <el-table
          :data="data.dependencies"
          size="small"
          border
        >
          <el-table-column
            prop="name"
            label="依赖"
            width="150"
          />
          <el-table-column
            label="类型"
            width="110"
          >
            <template #default="{ row }">
              {{ kindLabel(row.kind) }}
            </template>
          </el-table-column>
          <el-table-column
            label="配置"
            width="90"
          >
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="row.configured ? 'success' : 'info'"
              >
                {{ row.configured ? '已配置' : '未配置' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column
            label="可达性"
            width="110"
          >
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="reachType(row.reachable)"
              >
                {{ reachLabel(row.reachable) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column
            prop="note"
            label="说明"
            min-width="260"
          />
        </el-table>
      </el-card>

      <el-card
        shadow="never"
        header="安全配置（只报状态，不回显值）"
        class="gap"
      >
        <el-table
          :data="data.security"
          size="small"
          border
        >
          <el-table-column
            prop="label"
            label="配置项"
            min-width="220"
          />
          <el-table-column
            label="状态"
            width="110"
          >
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="statusTagType(row.status)"
              >
                {{ statusLabel(row.status) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column
            label="级别"
            width="90"
          >
            <template #default="{ row }">
              <el-tag
                size="small"
                :type="row.severity === 'warn' ? 'warning' : 'info'"
              >
                {{ row.severity === 'warn' ? '需处理' : '正常' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column
            prop="note"
            label="说明"
            min-width="280"
          />
        </el-table>
      </el-card>

      <el-card
        v-if="data.notes.length"
        shadow="never"
        header="说明"
        class="gap"
      >
        <ul class="notes">
          <li
            v-for="n in data.notes"
            :key="n"
          >
            {{ n }}
          </li>
        </ul>
      </el-card>
    </template>

    <el-skeleton
      v-else
      :rows="6"
      animated
    />
  </PageContainer>
</template>

<script lang="ts" setup>
// 平台诊断（D4）：消费只读诊断端点，展示版本/部署/存储迁移/依赖/安全配置状态。
// 语义约束（与后端一致）：无法确认的项显示原值（unknown / 未配置），不派生「正常」。
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { PageContainer } from '@/app/components'
import { fetchDiagnostics, type DiagnosticSnapshot } from '../api'

const data = ref<DiagnosticSnapshot | null>(null)
const loading = ref(false)
const error = ref('')

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    data.value = await fetchDiagnostics()
  } catch {
    // 拦截器已提示；此处保留失败态，不展示旧快照冒充最新
    data.value = null
    error.value = '诊断数据读取失败，请稍后重试。'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const uptimeText = computed(() => {
  const s = data.value?.runtime.uptimeSeconds ?? 0
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分`
  return `${m}分${s % 60}秒`
})

const modeLabel = computed(() => (data.value?.deploy.mode === 'kubernetes' ? 'Kubernetes' : '本地进程'))

const reachType = (s: string) => (({ ok: 'success', failed: 'danger' }) as Record<string, string>)[s] ?? 'info'
const reachLabel = (s: string) =>
  (({ ok: '可达', failed: '不可达', not_configured: '未配置' }) as Record<string, string>)[s] ?? s
const rbacType = (s: string) =>
  (({ allowed: 'success', denied: 'danger' }) as Record<string, string>)[s] ?? 'info'
const rbacLabel = (s: string) =>
  (({ allowed: '允许', denied: '拒绝', unknown: '无法确认' }) as Record<string, string>)[s] ?? s
const kindLabel = (s: string) =>
  (({
    database: '数据库',
    kubernetes: '集群',
    metrics: '指标',
    logs: '日志',
    tracing: '链路',
    bus: '事件总线'
  }) as Record<string, string>)[s] ?? s
const statusTagType = (s: string) =>
  (({
    set: 'success',
    enabled: 'success',
    on: 'success',
    unset: 'warning',
    disabled: 'warning',
    off: 'info'
  }) as Record<string, string>)[s] ?? 'info'
const statusLabel = (s: string) =>
  (({
    set: '已配置',
    unset: '未配置',
    enabled: '已启用',
    disabled: '未启用',
    on: '已开启',
    off: '已关闭'
  }) as Record<string, string>)[s] ?? s
</script>

<style scoped>
.generated {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 16px;
  margin-top: 16px;
}

.gap {
  margin-top: 16px;
}

.tag {
  margin-left: 8px;
}

.dim {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.dim.error {
  margin: 8px 0 0;
  color: var(--el-color-danger);
}

.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}

.notes {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  line-height: 1.9;
  color: var(--el-text-color-regular);
}
</style>
