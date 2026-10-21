<template>
  <div
    v-if="visible"
    class="tenant-scope"
  >
    <span class="scope-label">浏览范围</span>
    <el-select
      :model-value="scopeStore.tenantId"
      filterable
      clearable
      placeholder="全部租户"
      size="small"
      class="scope-select"
      @visible-change="onDropdown"
      @change="onChange"
    >
      <el-option
        label="全部租户"
        value=""
      />
      <el-option
        v-for="t in scopeStore.tenants"
        :key="t.id"
        :label="`${t.name}（${t.id}）`"
        :value="t.id"
      />
    </el-select>
    <span
      v-if="scopeStore.tenantsError"
      class="scope-error"
      :title="scopeStore.tenantsError"
    >租户列表加载失败</span>
  </div>
</template>

<script lang="ts" setup>
// 顶栏浏览范围选择器（spec 2026-10-19 §7.1 / §8.1）：
// 纯过滤器语义——切换只影响列表请求的 tenantId query，不改变身份与写目标；
// 平台级供给页不消费（页面自有「平台级」声明）。仅 super_admin 显示。
import { computed, onMounted } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { useScopeStore } from '@/app/stores/scope'
import { useUserStore } from '@/app/stores/user'

const scopeStore = useScopeStore()
const userStore = useUserStore()
const route = useRoute()
const router = useRouter()

const visible = computed(() => userStore.roles.includes('super_admin'))

const onDropdown = (open: boolean) => {
  if (open) void scopeStore.loadTenants()
}

const onChange = (tid: string | undefined | null) => {
  scopeStore.setTenant(tid ?? '')
  const query: LocationQueryRaw = { ...route.query, tenantId: tid || undefined }
  // 手工换范围时旧实体/环境不随新租户保留；URL与顶栏保持一致。
  for (const key of ['id', 'ruleId', 'targetId', 'targetType', 'firedAt', 'envId', 'appId', 'page']) delete query[key]
  void router.replace({ query, hash: '' })
}

onMounted(() => {
  // 超管登录后即预载一次（选择器默认可见，避免首次点开空白）
  if (visible.value) void scopeStore.loadTenants()
})
</script>

<style scoped>
.tenant-scope {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-right: 8px;
}

.scope-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}

.scope-select {
  width: 200px;
}

.scope-error {
  font-size: 12px;
  color: var(--el-color-danger);
  white-space: nowrap;
}
</style>
