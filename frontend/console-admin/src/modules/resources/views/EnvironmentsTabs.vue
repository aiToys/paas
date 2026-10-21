<template>
  <el-tabs v-model="tab">
    <el-tab-pane label="环境" name="environments">
      <Environments />
    </el-tab-pane>
    <el-tab-pane label="泳道" name="lanes" lazy>
      <Lanes />
    </el-tab-pane>
  </el-tabs>
</template>

<script lang="ts" setup>
// 环境与泳道（D4）：同一入口两 tab，支持 ?tab= 深链。
import { useRouteTab } from '@/app/composables'
import Environments from './Environments.vue'
import Lanes from './Lanes.vue'

const tab = useRouteTab('environments', ['environments', 'lanes'])
</script>
