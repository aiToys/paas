<template>
  <div class="panel">
    <div class="panel-header">
      <span class="panel-title">{{ t('dashboard.latestActivity') }}</span>
    </div>
    <div class="panel-body">
      <el-timeline v-if="activities.length">
        <el-timeline-item
          v-for="item in activities"
          :key="item.id"
          :timestamp="item.time"
          :type="item.type"
        >
          <div class="activity-title">
            {{ item.title }}
          </div>
          <div class="activity-desc">
            {{ item.desc }}
          </div>
        </el-timeline-item>
      </el-timeline>
      <div v-if="error" class="panel-error">
        {{ error }}
      </div>
      <el-empty
        v-else
        :description="t('dashboard.noActivity')"
        :image-size="80"
      />
    </div>
  </div>
</template>

<script lang="ts" setup>
import type { Activity } from '../api'
import { t } from '@/lib/i18n'

defineProps<{
  activities: Activity[]
  error?: string
}>()
</script>

<style scoped>
.panel-error {
  padding: 16px 20px;
  color: var(--el-color-danger);
  font-size: 13px;
}

.panel {
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  display: flex;
  flex-direction: column;
  height: 100%;
}

.panel-header {
  padding: 16px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.panel-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.panel-body {
  padding: 20px;
  flex: 1;
  overflow-y: auto;
}

.activity-title {
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-primary);
  margin-bottom: 4px;
}

.activity-desc {
  font-size: 13px;
  color: var(--el-text-color-regular);
  line-height: 1.5;
}
</style>
