<template>
  <div class="dataservices-tabs">
    <el-tabs v-model="tab">
      <el-tab-pane
        label="实例"
        name="instances"
        lazy
      >
        <Dataservices />
      </el-tab-pane>
      <el-tab-pane
        label="引擎目录"
        name="engines"
        lazy
      >
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 8px">
      {{ t('common.platformScope') }}
    </el-alert>
    <EngineList />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
// 数据服务（spec 2026-10-19 §5，I04 收敛）：实例 / 引擎目录两 tab——引擎是平台级供给，
// 与租户实例同一管理域。复用既有组件，读写能力不变；
// 旧链接 /resources/engines 重定向到 ?tab=engines。
import { useRouteTab } from '@/app/composables'
import { t } from '@/lib/i18n'
import Dataservices from './Dataservices.vue'
import EngineList from '@/modules/engine/views/List.vue'

const tab = useRouteTab('instances', ['instances', 'engines'])
</script>

<style scoped>
.dataservices-tabs {
  padding: 12px;
}
</style>
