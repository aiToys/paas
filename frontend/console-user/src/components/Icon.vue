<script setup lang="ts">
defineProps<{ name: string; size?: number }>()
</script>

<template>
  <svg
    :width="size || 20"
    :height="size || 20"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <!-- 模型市场：网格 -->
    <template v-if="name === 'market'">
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </template>
    <!-- 部署：层叠 -->
    <template v-else-if="name === 'deploy'">
      <polygon points="12 2 22 8.5 12 15 2 8.5 12 2" />
      <polyline points="2 15.5 12 22 22 15.5" />
      <polyline points="2 12 12 18.5 22 12" />
    </template>
    <!-- Playground：终端 -->
    <template v-else-if="name === 'playground'">
      <rect x="2" y="4" width="20" height="16" rx="2" />
      <polyline points="6 9 9 12 6 15" />
      <line x1="12" y1="15" x2="16" y2="15" />
    </template>
    <!-- API Key：钥匙 -->
    <template v-else-if="name === 'key'">
      <circle cx="8" cy="15" r="4" />
      <line x1="10.85" y1="12.15" x2="19" y2="4" />
      <line x1="18" y1="5" x2="20" y2="7" />
      <line x1="15" y1="8" x2="17" y2="10" />
    </template>
    <!-- 用量：柱状图 -->
    <template v-else-if="name === 'usage'">
      <line x1="6" y1="20" x2="6" y2="14" />
      <line x1="12" y1="20" x2="12" y2="4" />
      <line x1="18" y1="20" x2="18" y2="10" />
    </template>
    <!-- 折叠 -->
    <template v-else-if="name === 'collapse'">
      <polyline points="11 17 6 12 11 7" />
      <polyline points="18 17 13 12 18 7" />
    </template>
    <!-- 搜索 -->
    <template v-else-if="name === 'search'">
      <circle cx="11" cy="11" r="7" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
    </template>
    <!-- 服务治理：节点拓扑 -->
    <template v-else-if="name === 'service'">
      <circle cx="12" cy="5" r="2.5" />
      <circle cx="5" cy="19" r="2.5" />
      <circle cx="19" cy="19" r="2.5" />
      <line x1="12" y1="7.5" x2="6" y2="16.5" />
      <line x1="12" y1="7.5" x2="18" y2="16.5" />
      <line x1="7.5" y1="19" x2="16.5" y2="19" />
    </template>
    <!-- 消息平台：消息框 -->
    <template v-else-if="name === 'message'">
      <path d="M21 11.5a8.38 8.38 0 0 1-8.5 8.5 8.5 8.5 0 0 1-3.8-.9L3 21l1.9-5.7A8.5 8.5 0 1 1 21 11.5z" />
    </template>
    <!-- DAL：数据库圆柱 -->
    <template v-else-if="name === 'database'">
      <ellipse cx="12" cy="5" rx="8" ry="3" />
      <path d="M4 5v6c0 1.66 3.58 3 8 3s8-1.34 8-3V5" />
      <path d="M4 11v6c0 1.66 3.58 3 8 3s8-1.34 8-3v-6" />
    </template>
    <!-- DevOps：流水线/分支 -->
    <template v-else-if="name === 'pipeline'">
      <circle cx="6" cy="6" r="2.5" />
      <circle cx="6" cy="18" r="2.5" />
      <circle cx="18" cy="12" r="2.5" />
      <path d="M8.5 6H14a2 2 0 0 1 2 2v0a2 2 0 0 1-2 2H10a2 2 0 0 0-2 2v0" />
      <path d="M8.5 18H14a2 2 0 0 0 2-2v0" />
    </template>
    <!-- 下拉箭头 -->
    <template v-else-if="name === 'chevron'">
      <polyline points="6 9 12 15 18 9" />
    </template>
    <!-- 勾选 -->
    <template v-else-if="name === 'check'">
      <polyline points="20 6 9 17 4 12" />
    </template>
    <!-- 火箭（即将上线） -->
    <template v-else-if="name === 'rocket'">
      <path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91 0z" />
      <path d="M12 15l-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z" />
      <path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0" />
      <path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5" />
    </template>
    <!-- 缓存：闪电 -->
    <template v-else-if="name === 'zap'">
      <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
    </template>
    <!-- 对象存储：桶 -->
    <template v-else-if="name === 'storage'">
      <path d="M5 5h14l-1.4 14.2a2 2 0 0 1-2 1.8H8.4a2 2 0 0 1-2-1.8L5 5z" />
      <line x1="3" y1="5" x2="21" y2="5" />
      <line x1="9" y1="11" x2="15" y2="11" />
    </template>
    <!-- 向量数据库：堆叠菱形 -->
    <template v-else-if="name === 'layers'">
      <polygon points="12 2 22 8 12 14 2 8 12 2" />
      <polyline points="2 13 12 19 22 13" />
    </template>
    <!-- 服务（工作负载）：服务器 -->
    <template v-else-if="name === 'server'">
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <line x1="7" y1="7.5" x2="7.01" y2="7.5" />
      <line x1="7" y1="16.5" x2="7.01" y2="16.5" />
    </template>
    <!-- 任务 Job：圆中播放 -->
    <template v-else-if="name === 'job'">
      <circle cx="12" cy="12" r="9" />
      <polygon points="10 8 16 12 10 16 10 8" />
    </template>
    <!-- 定时 CronJob：时钟 -->
    <template v-else-if="name === 'clock'">
      <circle cx="12" cy="12" r="9" />
      <polyline points="12 7 12 12 16 14" />
    </template>
    <!-- 可观测：波形 -->
    <template v-else-if="name === 'activity'">
      <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
    </template>
    <!-- 安全：盾牌 -->
    <template v-else-if="name === 'shield'">
      <path d="M12 2l8 4v6c0 5-3.5 8.5-8 10-4.5-1.5-8-5-8-10V6l8-4z" />
    </template>
  </svg>
</template>
