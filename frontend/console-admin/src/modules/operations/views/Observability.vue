<template>
  <div class="observability-page">
    <el-alert
      class="scope-note"
      type="info"
      :closable="false"
      show-icon
      title="告警规则治理与告警/事件跨租户视图已接入；指标 / 日志 / trace 的跨租户视图待管理聚合 API（M20 后续批次）上线。"
    />
    <el-tabs v-model="tab">
      <el-tab-pane
        label="告警规则"
        name="rules"
        lazy
      >
        <AlertRules />
      </el-tab-pane>
      <el-tab-pane
        label="告警与事件"
        name="events"
        lazy
      >
        <AlertEvents />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
// 可观测（spec 2026-10-19 §5 / 复核 F03 收口）：规则与事件双 tab 平铺一层，
// 不再嵌套双层 tabs（外层单 tab + 内层双 tab 的层级冗余已撤）。
// 指标/日志/trace 跨租户视图依赖管理端聚合 API（M20 后续），没有真实数据源的
// 入口不提前上线为占位。旧链接 /resources/alert-rules 重定向到 ?tab=rules。
import { useRouteTab } from '@/app/composables'
import AlertRules from '@/modules/resources/views/AlertRules.vue'
import AlertEvents from '@/modules/resources/views/AlertEvents.vue'

const tab = useRouteTab('rules', ['rules', 'events'])
</script>

<style scoped>
.observability-page {
  padding: 12px;
}

.scope-note {
  margin-bottom: 8px;
}
</style>
