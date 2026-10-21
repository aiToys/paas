<template>
  <div class="delivery-records">
    <el-tabs v-model="tab">
      <el-tab-pane
        label="流水线运行"
        name="runs"
        lazy
      >
        <PipelineRuns />
      </el-tab-pane>
      <el-tab-pane
        label="构建"
        name="builds"
        lazy
      >
        <BuildRuns />
      </el-tab-pane>
      <el-tab-pane
        label="镜像"
        name="images"
        lazy
      >
        <Images />
      </el-tab-pane>
      <el-tab-pane
        label="发布"
        name="releases"
        lazy
      >
        <Releases />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
// 交付记录（spec 2026-10-19 §5，I06 收敛）：流水线运行 / 构建 / 镜像 / 发布同一故障链
// 不拆断——排障时 run→build→image→release 一页切换。复用既有 List 组件，读写能力不变；
// 旧链接 /resources/buildruns|images|releases 重定向到对应 tab（?tab= 可分享）。
import { useRouteTab } from '@/app/composables'
import PipelineRuns from './PipelineRuns.vue'
import BuildRuns from './BuildRuns.vue'
import Images from './Images.vue'
import Releases from './Releases.vue'

const tab = useRouteTab('runs', ['runs', 'builds', 'images', 'releases'])
</script>

<style scoped>
.delivery-records {
  padding: 12px;
}
</style>
