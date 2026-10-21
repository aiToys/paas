<template>
  <el-dialog
    v-model="visible"
    title="注销预检"
    width="640px"
    destroy-on-close
  >
    <div class="ob-head">
      <div class="ob-target">
        {{ tenant?.name ?? '' }}
        <span class="ob-id">{{ tenant?.id ?? '' }}</span>
      </div>
      <div class="ob-sub">删除租户前需处理的资源清单（只读，不会删除任何资源）</div>
    </div>

    <!-- 失败：明确报错，不展示上一份快照冒充最新 -->
    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      show-icon
      :title="`预检失败：${error}`"
    />

    <div v-else-if="loading" class="ob-loading">
      <el-skeleton :rows="4" animated />
    </div>

    <template v-else-if="report">
      <!-- 结论先行：阻塞 / 无阻塞 -->
      <div class="ob-verdict" :class="report.blocking ? 'blocked' : 'clear'">
        <div class="ob-verdict-title">
          {{ report.blocking ? '存在阻塞项，当前无法删除该租户' : '无阻塞项，租户记录可以删除' }}
        </div>
        <div class="ob-verdict-desc">
          {{ report.blocking
            ? '请先处理下方标记「阻塞」的资源，再执行删除。'
            : '注意：平台不会代删数据面实例，仍请按下方提示逐项下线。' }}
        </div>
      </div>

      <el-table :data="report.items" size="small" class="ob-table">
        <el-table-column prop="label" label="资源" width="120" />
        <el-table-column prop="count" label="数量" width="80" />
        <el-table-column label="性质" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.blocking && row.count > 0" type="danger" size="small">阻塞</el-tag>
            <el-tag v-else type="info" size="small">提示</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="note" label="说明" min-width="240" show-overflow-tooltip />
      </el-table>

      <ul class="ob-notes">
        <li v-for="n in report.notes" :key="n">{{ n }}</li>
      </ul>
    </template>

    <template #footer>
      <el-button @click="visible = false">关闭</el-button>
      <el-button :loading="loading" @click="load">重新预检</el-button>
    </template>
  </el-dialog>
</template>

<script lang="ts" setup>
// 注销预检对话框：只读列出删除租户前需处理的资源。
// 「0 项」与「统计失败」严格区分——失败展示错误态，不显示空表。
import { ref, watch } from 'vue'
import { fetchTenantOffboarding, type OffboardingReport, type TenantInfo } from '../api'

const props = defineProps<{
  modelValue: boolean
  tenant: TenantInfo | null
}>()

const emit = defineEmits<{ 'update:modelValue': [v: boolean] }>()

const visible = ref(props.modelValue)
watch(() => props.modelValue, (v) => {
  visible.value = v
  if (v) load()
})
watch(visible, (v) => emit('update:modelValue', v))

const report = ref<OffboardingReport | null>(null)
const loading = ref(false)
const error = ref('')

async function load() {
  const id = props.tenant?.id
  if (!id) return
  loading.value = true
  error.value = ''
  report.value = null // 清空旧快照，避免失败时误读上一个租户的数据
  try {
    report.value = await fetchTenantOffboarding(id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '未知错误'
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.ob-head {
  margin-bottom: 12px;
}
.ob-target {
  font-size: 15px;
  font-weight: 600;
}
.ob-id {
  margin-left: 8px;
  font-size: 12px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
}
.ob-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.ob-loading {
  padding: 8px 0;
}
.ob-verdict {
  padding: 12px 14px;
  border-radius: 6px;
  margin-bottom: 12px;
  border: 1px solid transparent;
}
.ob-verdict.blocked {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-7);
}
.ob-verdict.clear {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-7);
}
.ob-verdict-title {
  font-weight: 600;
}
.ob-verdict-desc {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-regular);
}
.ob-table {
  margin-bottom: 8px;
}
.ob-notes {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}
</style>
