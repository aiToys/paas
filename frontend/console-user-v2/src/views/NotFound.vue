<script setup lang="ts">
import StatePanel from '@/components/StatePanel.vue'
</script>
<template><StatePanel title="页面不存在" description="链接可能已失效，或该资源已经移动。可从总览重新进入，也可以使用顶部搜索。"><RouterLink to="/" class="act primary">返回工作空间</RouterLink><RouterLink to="/products" class="act">查看产品</RouterLink></StatePanel></template>
