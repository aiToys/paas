<template>
  <div class="marketplace-page">
  <el-alert type="info" :closable="false" show-icon style="margin-bottom: 12px">
    {{ t('common.platformScope') }}
  </el-alert>
    <div class="page-toolbar">
      <el-select
        v-model="entityType"
        clearable
        placeholder="全部类型"
        style="width: 140px"
      >
        <el-option
          v-for="(label, value) in ENTITY_LABELS"
          :key="value"
          :label="label"
          :value="value"
        />
      </el-select>
      <el-input
        v-model="keyword"
        clearable
        placeholder="搜索名称 / 发布租户"
        style="width: 220px"
      />
      <el-button @click="load">
        刷新
      </el-button>
      <span
        v-if="error"
        class="load-error"
      >{{ error }}</span>
    </div>

    <el-table
      v-loading="loading"
      :data="filtered"
      size="small"
    >
      <el-table-column
        label="类型"
        width="100"
      >
        <template #default="{ row }">
          <el-tag size="small">
            {{ ENTITY_LABELS[row.entityType] ?? row.entityType }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        prop="name"
        label="名称"
        min-width="140"
      />
      <el-table-column
        prop="category"
        label="分类"
        width="100"
      >
        <template #default="{ row }">
          {{ row.category || '—' }}
        </template>
      </el-table-column>
      <el-table-column
        prop="description"
        label="说明"
        min-width="220"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          {{ row.description || '—' }}
        </template>
      </el-table-column>
      <el-table-column
        prop="publisherTenant"
        label="发布租户"
        width="110"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            type="info"
          >
            {{ row.publisherTenant }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        prop="installs"
        label="安装量"
        width="90"
        sortable
      />
      <el-table-column
        prop="createdAt"
        label="发布时间"
        width="170"
      >
        <template #default="{ row }">
          {{ new Date(row.createdAt).toLocaleString() }}
        </template>
      </el-table-column>
      <el-table-column
        label="操作"
        width="90"
        fixed="right"
      >
        <template #default="{ row }">
          <el-button
            link
            type="danger"
            size="small"
            data-testid="market-unpublish"
            @click="unpublish(row)"
          >
            下架
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-empty
      v-if="!loading && !error && filtered.length === 0"
      description="暂无已发布内容；租户在用户控制台「智能体 → 广场」发布后在此可见"
      :image-size="64"
    />
    <div
      v-if="error"
      class="error-actions"
    >
      <el-button
        type="primary"
        @click="load"
      >
        重试
      </el-button>
    </div>
  </div>
</template>

<script lang="ts" setup>
// 资源广场（spec 2026-10-19 §5 + D3 治理闭环）：跨租户已发布内容总览（super_admin）。
// 数据源 GET /api/admin/ai/marketplace；下架走独立治理契约
// DELETE /api/admin/ai/marketplace/{id}?reason=（原因必填入审计；已安装副本不受影响，
// 安装是 fork 到租户无反向依赖——影响确认如实标注，不提供「撤回所有安装」的假能力）。
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { t } from '@/lib/i18n'
import { fetchAiMarketList, unpublishMarketItem, type AdminMarketItem } from '../api'

const ENTITY_LABELS: Record<string, string> = {
  skill: '技能',
  prompt: '提示词',
  tool: '工具',
  agent: '智能体',
  knowledgebase: '知识库',
  workflow: '工作流'
}

const loading = ref(false)
const error = ref('')
const items = ref<AdminMarketItem[]>([])
const entityType = ref('')
const keyword = ref('')

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return items.value.filter(
    (m) =>
      (!entityType.value || m.entityType === entityType.value) &&
      (!kw ||
        m.name.toLowerCase().includes(kw) ||
        m.publisherTenant.toLowerCase().includes(kw))
  )
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await fetchAiMarketList({ page: 1, size: 1000 })
    items.value = res.records
  } catch (e) {
    // 独立失败态：网络/权限错误不伪装成空列表（空态文案会误导「无发布内容」）
    error.value = (e as Error).message || '加载失败'
  } finally {
    loading.value = false
  }
}

// 下架（D3）：影响确认（安装量如实展示——已安装副本不受影响）+ 原因必填 + 审计（服务端）。
const unpublish = async (row: AdminMarketItem) => {
  let reason: string
  try {
    const { value } = await ElMessageBox.prompt(
      `下架「${row.name}」（${ENTITY_LABELS[row.entityType] ?? row.entityType}，发布租户 ${row.publisherTenant}）。\n` +
        `安装量 ${row.installs}：已安装副本不受影响（安装是 fork 到租户），仅从广场列表移除。\n` +
        `请输入下架原因（必填，入审计）：`,
      '下架广场条目',
      {
        confirmButtonText: '下架',
        cancelButtonText: '取消',
        type: 'warning',
        inputValidator: (v: string) => (v && v.trim().length >= 2 ? true : '请填写下架原因（至少 2 字）')
      }
    )
    reason = value.trim()
  } catch {
    return // 用户取消
  }
  try {
    const res = await unpublishMarketItem(row.id, reason)
    ElMessage.success(`已下架（安装量 ${res.installs}，已安装副本不受影响）`)
    load()
  } catch {
    // 拦截器提示（400 缺原因 / 403 非超管 / 404 不存在）
  }
}

onMounted(load)
</script>

<style scoped>
.marketplace-page {
  padding: 12px;
}

.page-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.load-error {
  color: var(--el-color-danger);
  font-size: 13px;
}

.error-actions {
  margin-top: 12px;
  text-align: center;
}
</style>
