<template>
  <el-drawer
    :model-value="modelValue"
    :title="`配置命名空间详情：${detail?.namespace?.name ?? id}`"
    size="640px"
    @update:model-value="$emit('update:modelValue', $event)"
    @open="load"
  >
    <div v-loading="loading">
      <el-alert v-if="error" type="error" :closable="false" show-icon style="margin-bottom: 12px">
        {{ error }}
      </el-alert>

      <template v-if="detail">
        <el-descriptions :column="2" size="small" border style="margin-bottom: 16px">
          <el-descriptions-item label="租户">{{ detail.namespace.tenantId }}</el-descriptions-item>
          <el-descriptions-item label="scope">
            <el-tag size="small" :type="detail.namespace.scope === 'shared' ? 'warning' : 'info'">
              {{ detail.namespace.scope }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="归属应用">{{ detail.namespace.appId || '—' }}</el-descriptions-item>
          <el-descriptions-item label="环境">{{ detail.namespace.envId || '基线' }}</el-descriptions-item>
        </el-descriptions>

        <!-- 草稿：值全掩码（admin 只读不回明文；看键与类型即可排障） -->
        <h4 class="section-title">草稿（{{ detail.draft.length }} 项，值已掩码）</h4>
        <el-table :data="detail.draft" size="small" border>
          <el-table-column prop="key" label="键" min-width="140" />
          <el-table-column prop="value" label="值" width="90">
            <template #default>{{ '••••••' }}</template>
          </el-table-column>
          <el-table-column prop="type" label="类型" width="80" />
          <el-table-column prop="updatedAt" label="更新时间" width="160" />
        </el-table>

        <!-- active 与键 diff：只看键变化不看值（无 active=从未发布） -->
        <h4 class="section-title">已发布</h4>
        <template v-if="detail.active">
          <el-descriptions :column="2" size="small" style="margin-bottom: 8px">
            <el-descriptions-item label="active 版本">v{{ detail.active.version }}</el-descriptions-item>
            <el-descriptions-item label="发布时间">{{ detail.active.createdAt }}</el-descriptions-item>
          </el-descriptions>
          <div class="keys-line">
            快照键（{{ detail.active.keys.length }}）：
            <el-tag v-for="k in detail.active.keys" :key="k" size="small" class="key-tag">{{ k }}</el-tag>
          </div>
          <el-alert type="info" :closable="false" style="margin-top: 8px">
            未发布变更（键级 diff）：
            <el-tag v-if="detail.diff.added.length" size="small" type="success" class="key-tag">
              新增 {{ detail.diff.added.join('、') }}
            </el-tag>
            <el-tag v-if="detail.diff.removed.length" size="small" type="danger" class="key-tag">
              已删 {{ detail.diff.removed.join('、') }}
            </el-tag>
            <span v-if="!detail.diff.added.length && !detail.diff.removed.length">无（草稿与快照键一致）</span>
          </el-alert>
        </template>
        <el-empty v-else description="从未发布（无 active 版本）" :image-size="48" />

        <!-- 引用影响面：shared=被哪些应用 ns 引用；app=引用了哪些 shared -->
        <h4 class="section-title">引用{{ detail.namespace.scope === 'shared' ? '方（发布影响面）' : '的共享配置' }}</h4>
        <!-- refsError：查询失败 ≠ 无引用，不显示空表冒充安全 -->
        <el-alert
          v-if="detail.refsError"
          type="warning"
          :closable="false"
          :title="detail.refsError"
          show-icon
        />
        <el-table v-else-if="detail.refs.length" :data="detail.refs" size="small" border>
          <el-table-column
            :prop="detail.namespace.scope === 'shared' ? 'appNsId' : 'sharedNsId'"
            :label="detail.namespace.scope === 'shared' ? '引用方（应用 ns）' : '被引用（shared ns）'"
            min-width="180"
          />
          <el-table-column prop="createdAt" label="建立时间" width="160" />
        </el-table>
        <span v-else class="dim">无引用</span>

        <!-- 发布记录：版本降序，快照只列键数 -->
        <h4 class="section-title">发布记录（{{ detail.publishes.length }}）</h4>
        <el-table v-if="detail.publishes.length" :data="detail.publishes" size="small" border>
          <el-table-column prop="version" label="版本" width="70">
            <template #default="{ row }">v{{ row.version }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">
                {{ row.status === 'active' ? '当前生效' : '已回滚' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="快照键数" width="90">
            <template #default="{ row }">{{ row.keys.length }}</template>
          </el-table-column>
          <el-table-column prop="createdAt" label="发布时间" width="160" />
        </el-table>
        <span v-else class="dim">无发布记录</span>
      </template>
    </div>
  </el-drawer>
</template>

<script lang="ts" setup>
// 配置命名空间 admin 详情（D3 spec §8.6 四区）：草稿（掩码）/ 已发布（键清单 + 键级 diff）/
// 引用影响面 / 发布记录。值永不回明文——掩码是 admin 侧诚实语义，不是渲染层遮蔽。
import { ref } from 'vue'
import { fetchConfigcenterDetail, type AdminNSDetail } from '../api'

const props = defineProps<{ modelValue: boolean; id: string }>()
defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const loading = ref(false)
const error = ref('')
const detail = ref<AdminNSDetail | null>(null)

async function load() {
  if (!detail.value || detail.value.namespace.id !== props.id) {
    loading.value = true
    error.value = ''
    try {
      detail.value = await fetchConfigcenterDetail(props.id)
    } catch (e) {
      error.value = (e as Error).message || '加载失败'
    } finally {
      loading.value = false
    }
  }
}
</script>

<style scoped>
.section-title {
  margin: 16px 0 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.key-tag {
  margin: 2px 4px 2px 0;
}
.dim {
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}
</style>
