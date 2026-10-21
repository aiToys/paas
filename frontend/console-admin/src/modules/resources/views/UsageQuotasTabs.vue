<template>
  <el-tabs v-model="tab">
    <el-tab-pane label="用量总览" name="usage">
      <UsageOverview />
    </el-tab-pane>
    <el-tab-pane label="配额" name="quotas" lazy>
      <Quotas />
    </el-tab-pane>
  </el-tabs>
</template>

<script lang="ts" setup>
// 用量与配额（D4）：同一入口两 tab，支持 ?tab= 深链（旧 /resources/quotas 直达「配额」）。
import { useRouteTab } from '@/app/composables'
import UsageOverview from './UsageOverview.vue'
import Quotas from './Quotas.vue'

const tab = useRouteTab('usage', ['usage', 'quotas'])
</script>
