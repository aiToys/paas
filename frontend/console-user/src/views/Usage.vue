<script setup lang="ts">
import { ref } from 'vue'
import { ElCard, ElStatistic, ElRow, ElCol } from 'element-plus'

// 用量与计费：Token 用量与成本。Plan 2 落地 Metering 后对接时序数据。
const stats = ref([
  { title: '本月 Token 用量', value: 12800000, suffix: 'tokens' },
  { title: '推理请求', value: 86400, suffix: '次' },
  { title: 'GPU 时长', value: 320, suffix: 'GPU·时' },
  { title: '本月费用', value: 2560.5, suffix: '元', precision: 2 },
])
</script>

<template>
  <el-row :gutter="16">
    <el-col v-for="s in stats" :key="s.title" :span="6">
      <el-card>
        <el-statistic :title="s.title" :value="s.value" :suffix="s.suffix" :precision="s.precision ?? 0" />
      </el-card>
    </el-col>
  </el-row>
</template>
